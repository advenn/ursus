package kernel

import (
	"cmp"
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
)

// fakeModeSpill is a ModeSpill in memory, of Float64 values. It checks each run is in
// order as it ends, and gives the entries back sorted, each key's in an order of its
// own and in batches of random sizes, so nothing reads either.
type fakeModeSpill struct {
	t    *testing.T
	rng  *rand.Rand
	runs [][]spilledMode
	open []spilledMode
	gone bool
}

type spilledMode struct {
	g           int32
	k           string
	rows, first int64
	v           float64
	ok          bool
}

func compareSpilledMode(x, y spilledMode) int {
	if c := cmp.Compare(x.g, y.g); c != 0 {
		return c
	}
	return cmp.Compare(x.k, y.k)
}

func (f *fakeModeSpill) Put(gids []int32, keys [][]byte, rows, first []int64, vals *data.Column) error {
	vs, valid := data.MustValues[float64](vals), vals.Validity()
	for i, g := range gids {
		f.open = append(f.open, spilledMode{g, string(keys[i]), rows[i], first[i], vs[i], valid.Get(i)})
	}
	return nil
}

func (f *fakeModeSpill) EndRun() error {
	if len(f.open) == 0 {
		f.t.Error("a run ended with nothing in it")
	}
	if !slices.IsSortedFunc(f.open, compareSpilledMode) {
		f.t.Errorf("run %d is not in order of group and key", len(f.runs))
	}
	f.runs = append(f.runs, f.open)
	f.open = nil
	return nil
}

func (f *fakeModeSpill) Scan(fn func(gids []int32, keys data.StringAccessor, rows, first []int64, vals *data.Column) error) error {
	if f.gone {
		f.t.Error("a mode's spill read after it was let go")
	}
	all := slices.Concat(f.runs...)
	f.rng.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
	slices.SortStableFunc(all, compareSpilledMode)
	for lo := 0; lo < len(all); {
		hi := min(len(all), lo+1+f.rng.IntN(40))
		part := all[lo:hi]
		gids, rows, first := make([]int32, len(part)), make([]int64, len(part)), make([]int64, len(part))
		keys, vs := make([]string, len(part)), make([]float64, len(part))
		ok := bitmap.NewBuilder(len(part))
		for i, e := range part {
			gids[i], rows[i], first[i], keys[i], vs[i] = e.g, e.rows, e.first, e.k, e.v
			ok.Append(e.ok)
		}
		k := data.NewString("k", keys, bitmap.AllSet(len(part)))
		if err := fn(gids, k.Strings(), rows, first, data.NewFixed("v", dtype.Float64, vs, ok.Finish())); err != nil {
			return err
		}
		lo = hi
	}
	return nil
}

func (f *fakeModeSpill) Ordered(fn func(gids []int32, keys data.StringAccessor, rows, first []int64, vals *data.Column) error) error {
	err := f.Scan(fn)
	f.gone = true
	return err
}

func newMode(t *testing.T, maintain bool) *modeAcc {
	t.Helper()
	bind, err := expr.ResolveAggBinding(expr.AggMode, dtype.Float64)
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewAccumulator(expr.AggMode, dtype.Float64, bind, expr.AggParams{MaintainOrder: maintain})
	if err != nil {
		t.Fatal(err)
	}
	return a.(*modeAcc)
}

// TestAModesSpilledEntriesGiveItsModes: a mode whose entries went to a spill at
// random points between batches answers as one that held them (step 340), and both
// as the list kernel answers over each group's values imploded, to the bit: over
// floats with NaN of two payloads, −0 and +0 in either order first, nulls, and ties
// of the most rows, in many groups and in one of most rows, in the order the values
// first appear and sorted. Every run is in order, and a spilled mode refuses a merge
// either way.
func TestAModesSpilledEntriesGiveItsModes(t *testing.T) {
	for _, maintain := range []bool{true, false} {
		rng := rand.New(rand.NewPCG(340, 1))
		pool := []float64{math.NaN(), math.Float64frombits(0x7ff8000000000001), 0, math.Copysign(0, -1), 1, 2, 3}
		held, spilled := newMode(t, maintain), newMode(t, maintain)
		sp := &fakeModeSpill{t: t, rng: rng}
		const nGroups = 12
		var all []float64
		var allOK []bool
		var allG []int32
		for range 90 {
			n := 1 + rng.IntN(120)
			groups, vals := make([]int32, n), make([]float64, n)
			ok := bitmap.NewBuilder(n)
			for i := range n {
				groups[i] = int32(rng.IntN(nGroups))
				if rng.IntN(2) == 0 {
					groups[i] = 4
				}
				vals[i] = pool[rng.IntN(len(pool))]
				if rng.IntN(3) == 0 {
					vals[i] = float64(rng.IntN(30))
				}
				v := rng.IntN(9) != 0
				ok.Append(v)
				all, allOK, allG = append(all, vals[i]), append(allOK, v), append(allG, groups[i])
			}
			col := data.NewFixed("v", dtype.Float64, vals, ok.Finish())
			for _, a := range []*modeAcc{held, spilled} {
				a.Reserve(nGroups)
				if err := a.AddBatch(groups, col); err != nil {
					t.Fatal(err)
				}
			}
			if rng.IntN(3) != 0 {
				continue
			}
			got, err := spilled.SpillModes(sp)
			if err != nil {
				t.Fatal(err)
			}
			if !got || len(spilled.gid) != 0 || spilled.nv != 0 {
				t.Fatalf("SpillModes said %v, and kept %d rows", got, len(spilled.gid))
			}
		}
		if len(sp.runs) < 2 {
			t.Fatal("fewer than two runs, so the merge of runs is not tested")
		}
		want, err := held.Finish("m", nGroups)
		if err != nil {
			t.Fatal(err)
		}
		got, err := spilled.Finish("m", nGroups)
		if err != nil {
			t.Fatal(err)
		}
		if !sp.gone {
			t.Error("the spill was not let go")
		}
		// The list kernel over each group's values in order, as the mode was before.
		offs := make([]int32, nGroups+1)
		for _, g := range allG {
			offs[g+1]++
		}
		for g := range nGroups {
			offs[g+1] += offs[g]
		}
		at := slices.Clone(offs[:nGroups])
		order := make([]int, len(all)) // each group's rows, in turn
		for i, g := range allG {
			order[at[g]] = i
			at[g]++
		}
		vs, ok := make([]float64, len(all)), bitmap.NewBuilder(len(all))
		for j, i := range order {
			vs[j] = all[i]
			ok.Append(allOK[i])
		}
		lists := data.NewList("m", offs, data.NewFixed("m", dtype.Float64, vs, ok.Finish()), bitmap.View{})
		kernel, err := listMode("m", lists, maintain)
		if err != nil {
			t.Fatal(err)
		}
		for name, c := range map[string]*data.Column{"spilled": got, "the list kernel's": kernel} {
			if d := modeDiff(want, c); d != "" {
				t.Errorf("maintain %v: %s against held: %s", maintain, name, d)
			}
		}
		other := newMode(t, maintain)
		if err := spilled.Merge(other, nil); err == nil {
			t.Error("a spilled mode took a merge")
		}
		if err := other.Merge(spilled, nil); err == nil {
			t.Error("a mode merged one that spilled")
		}
	}
}

// TestASpilledModeKeepsTheOrderValuesFirstAppear: ties of the most rows, each value
// first in a batch of its own and spilled between batches, come back in the order
// they first appeared with maintainOrder, and sorted without, as held ones do.
func TestASpilledModeKeepsTheOrderValuesFirstAppear(t *testing.T) {
	for _, maintain := range []bool{true, false} {
		held, spilled := newMode(t, maintain), newMode(t, maintain)
		sp := &fakeModeSpill{t: t, rng: rand.New(rand.NewPCG(340, 3))}
		// Values 7, 3, 9, 1, 5 first appear in that order, each three times, in each
		// of two groups.
		for _, v := range []float64{7, 3, 9, 1, 5, 7, 3, 9, 1, 5, 5, 1, 9, 3, 7} {
			col := data.NewFixed("v", dtype.Float64, []float64{v, v}, bitmap.AllSet(2))
			for _, a := range []*modeAcc{held, spilled} {
				a.Reserve(2)
				if err := a.AddBatch([]int32{0, 1}, col); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := spilled.SpillModes(sp); err != nil {
				t.Fatal(err)
			}
		}
		want, err := held.Finish("m", 2)
		if err != nil {
			t.Fatal(err)
		}
		got, err := spilled.Finish("m", 2)
		if err != nil {
			t.Fatal(err)
		}
		if d := modeDiff(want, got); d != "" {
			t.Errorf("maintain %v: spilled against held: %s", maintain, d)
		}
		order := []float64{7, 3, 9, 1, 5}
		if !maintain {
			order = []float64{1, 3, 5, 7, 9}
		}
		if vs := data.MustValues[float64](got.Lists().Child())[:5]; !slices.Equal(vs, order) {
			t.Errorf("maintain %v: %v, want %v", maintain, vs, order)
		}
	}
}

// TestAMergedModeSpillsAsOnePass: a mode merged from accumulators over the input's
// portions in turn, then spilled, answers as one that held every row: a merged
// entry's first row counts from the end of the rows before it.
func TestAMergedModeSpillsAsOnePass(t *testing.T) {
	rng := rand.New(rand.NewPCG(340, 2))
	const nGroups = 3
	one := newMode(t, true)
	parts := []*modeAcc{newMode(t, true), newMode(t, true), newMode(t, true)}
	for p, part := range parts {
		for range 4 {
			n := 1 + rng.IntN(30)
			groups, vals := make([]int32, n), make([]float64, n)
			for i := range n {
				groups[i], vals[i] = int32(rng.IntN(nGroups)), float64(rng.IntN(6)+p)
			}
			col := data.NewFixed("v", dtype.Float64, vals, bitmap.AllSet(n))
			for _, a := range []*modeAcc{one, part} {
				a.Reserve(nGroups)
				if err := a.AddBatch(groups, col); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	merged := parts[0]
	for _, p := range parts[1:] {
		if err := merged.Merge(p, nil); err != nil {
			t.Fatal(err)
		}
	}
	sp := &fakeModeSpill{t: t, rng: rng}
	if _, err := merged.SpillModes(sp); err != nil {
		t.Fatal(err)
	}
	want, err := one.Finish("m", nGroups)
	if err != nil {
		t.Fatal(err)
	}
	got, err := merged.Finish("m", nGroups)
	if err != nil {
		t.Fatal(err)
	}
	if d := modeDiff(want, got); d != "" {
		t.Errorf("merged and spilled, against one pass: %s", d)
	}
}

// modeDiff is where two List(Float64) columns differ, to the bit, or "".
func modeDiff(a, b *data.Column) string {
	if a.Len() != b.Len() {
		return "lengths differ"
	}
	al, bl := a.Lists(), b.Lists()
	ac, bc := al.Child(), bl.Child()
	av, bv := data.MustValues[float64](ac), data.MustValues[float64](bc)
	for g := range a.Len() {
		as, ae, _ := al.Get(g)
		bs, be, _ := bl.Get(g)
		if ae-as != be-bs {
			return "group " + itoaTest(g) + "'s lengths differ"
		}
		for i := range ae - as {
			x, y := int(as+i), int(bs+i)
			if ac.Validity().Get(x) != bc.Validity().Get(y) ||
				ac.Validity().Get(x) && math.Float64bits(av[x]) != math.Float64bits(bv[y]) {
				return "group " + itoaTest(g) + "'s values differ"
			}
		}
	}
	return ""
}

func itoaTest(i int) string {
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}

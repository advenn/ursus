package kernel_test

// Step 168: First and Last keep a row index a group and gather once a batch. They
// must answer as the rows say, positionally, nulls included, over any number of
// batches, merged, and for any type; and Last must not hold every row it ever saw.

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
)

// posBatch is n rows over groups [0, nGroups) and their values, as Int64 or
// String, one in five null; a group of id >= nGroups-3 never appears.
type posBatch struct {
	groups []int32
	vals   []int64
	ok     []bool
}

func posBatches(rng *rand.Rand, batches, n, nGroups int) []posBatch {
	out := make([]posBatch, batches)
	for b := range out {
		p := posBatch{groups: make([]int32, n), vals: make([]int64, n), ok: make([]bool, n)}
		for i := range n {
			p.groups[i] = int32(rng.IntN(nGroups - 3))
			p.vals[i], p.ok[i] = rng.Int64N(1_000_000), rng.IntN(5) != 0
		}
		out[b] = p
	}
	return out
}

func (p posBatch) column(dt dtype.DataType) *data.Column {
	valid := bitmap.NewBuilder(len(p.vals))
	for _, ok := range p.ok {
		valid.Append(ok)
	}
	if dt == dtype.String {
		s := make([]string, len(p.vals))
		for i, v := range p.vals {
			s[i] = fmt.Sprintf("s%d", v)
		}
		return data.NewString("v", s, valid.Finish())
	}
	return data.NewFixed("v", dtype.Int64, p.vals, valid.Finish())
}

func newPositionAcc(t *testing.T, op expr.AggOp, dt dtype.DataType) kernel.Accumulator {
	t.Helper()
	bind, err := expr.ResolveAggBinding(op, dt)
	if err != nil {
		t.Fatal(err)
	}
	acc, err := kernel.NewAccumulator(op, dt, bind, expr.AggParams{})
	if err != nil {
		t.Fatal(err)
	}
	return acc
}

// rendered is c's row i as text, "∅" for a null.
func rendered(c *data.Column, i int) string {
	if !c.IsValid(i) {
		return "∅"
	}
	if c.DType() == dtype.String {
		return c.Strings().Get(i)
	}
	return fmt.Sprint(data.MustValues[int64](c)[i])
}

func TestFirstAndLastAnswerAsTheRowsSay(t *testing.T) {
	const nGroups = 2_000
	for _, dt := range []dtype.DataType{dtype.Int64, dtype.String} {
		for _, op := range []expr.AggOp{expr.AggFirst, expr.AggLast} {
			t.Run(fmt.Sprintf("%s %s", op, dt), func(t *testing.T) {
				batches := posBatches(rand.New(rand.NewPCG(168, 1)), 25, 700, nGroups)
				acc := newPositionAcc(t, op, dt)
				want := make([]string, nGroups)
				for g := range want {
					want[g] = "∅"
				}
				have := make([]bool, nGroups)
				for _, b := range batches {
					col := b.column(dt)
					acc.Reserve(nGroups)
					if err := acc.AddBatch(b.groups, col); err != nil {
						t.Fatal(err)
					}
					for i, g := range b.groups {
						if op == expr.AggLast || !have[g] {
							want[g], have[g] = rendered(col, i), true
						}
					}
				}
				got, err := acc.Finish("out", nGroups)
				if err != nil {
					t.Fatal(err)
				}
				if got.Len() != nGroups || got.DType() != dt {
					t.Fatalf("%d rows of %s, want %d of %s", got.Len(), got.DType(), nGroups, dt)
				}
				for g := range nGroups {
					if r := rendered(got, g); r != want[g] {
						t.Fatalf("group %d: %s, want %s", g, r, want[g])
					}
				}
			})
		}
	}
}

// TestFirstAndLastMerge: a later portion merged into an earlier one answers as one
// accumulator over both, the later portion's groups numbered differently.
func TestFirstAndLastMerge(t *testing.T) {
	const nGroups = 300
	for _, op := range []expr.AggOp{expr.AggFirst, expr.AggLast} {
		rng := rand.New(rand.NewPCG(168, 2))
		early, late := posBatches(rng, 4, 400, nGroups), posBatches(rng, 4, 400, nGroups)
		whole := newPositionAcc(t, op, dtype.String)
		a, b := newPositionAcc(t, op, dtype.String), newPositionAcc(t, op, dtype.String)
		// b numbers group g as nGroups-1-g; remap takes it back.
		remap := make([]int32, nGroups)
		for g := range remap {
			remap[g] = int32(nGroups - 1 - g)
		}
		for _, p := range early {
			for _, acc := range []kernel.Accumulator{whole, a} {
				acc.Reserve(nGroups)
				if err := acc.AddBatch(p.groups, p.column(dtype.String)); err != nil {
					t.Fatal(err)
				}
			}
		}
		for _, p := range late {
			whole.Reserve(nGroups)
			if err := whole.AddBatch(p.groups, p.column(dtype.String)); err != nil {
				t.Fatal(err)
			}
			flipped := make([]int32, len(p.groups))
			for i, g := range p.groups {
				flipped[i] = remap[g]
			}
			b.Reserve(nGroups)
			if err := b.AddBatch(flipped, p.column(dtype.String)); err != nil {
				t.Fatal(err)
			}
		}
		if err := a.Merge(b, remap); err != nil {
			t.Fatal(err)
		}
		want, err := whole.Finish("out", nGroups)
		if err != nil {
			t.Fatal(err)
		}
		got, err := a.Finish("out", nGroups)
		if err != nil {
			t.Fatal(err)
		}
		for g := range nGroups {
			if rendered(got, g) != rendered(want, g) {
				t.Fatalf("%s, group %d: %s merged, %s whole", op, g, rendered(got, g), rendered(want, g))
			}
		}
	}
}

// TestLastHoldsAboutItsGroups: every batch supersedes every group's last row; what
// Last holds stays near its groups', not every row it saw, and its answer is the
// last batch's. Another 500 groups appear in the first batch alone, so the
// compactions after it must carry their rows, which no later batch rewrites.
func TestLastHoldsAboutItsGroups(t *testing.T) {
	const nGroups, early, batches = 10_000, 500, 40
	acc := newPositionAcc(t, expr.AggLast, dtype.Int64)
	for b := range batches {
		n := nGroups
		if b == 0 {
			n += early
		}
		groups := make([]int32, n)
		vals := make([]int64, n)
		for g := range n {
			groups[g], vals[g] = int32(g), int64(b*nGroups+g)
		}
		acc.Reserve(nGroups + early)
		if err := acc.AddBatch(groups, data.NewFixed("v", dtype.Int64, vals, bitmap.AllSet(n))); err != nil {
			t.Fatal(err)
		}
	}
	// 400,000 rows seen, 8 bytes each: 3.2 MB if every one were held.
	if n := acc.NBytes(); n > 2<<20 {
		t.Fatalf("Last holds %d bytes for %d groups after %d batches of them", n, nGroups, batches)
	}
	got, err := acc.Finish("out", nGroups+early)
	if err != nil {
		t.Fatal(err)
	}
	last := data.MustValues[int64](got)
	for g := range nGroups + early {
		want := int64((batches-1)*nGroups + g)
		if g >= nGroups {
			want = int64(g) // the first batch's
		}
		if last[g] != want {
			t.Fatalf("group %d: %d, want %d", g, last[g], want)
		}
	}
}

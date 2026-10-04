package ursus_test

// Spilling, by hand: v0.3-scope.md §2.4, "spills to disk rather than falling over"
// is half true.
//
// Each spilled case runs under a limit far below what the query holds, requires
// that it reached disk, and requires the answer it gives in memory — order
// included, except for a join, whose order under a limit is documented as
// unspecified. The refusals that stay must say which wall they hit, and an
// operator that cannot spill must be charged for what it really holds, so that it
// fails at its true peak rather than at half of it.

import (
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/ursustest"
)

// knownSpillDefects names each case that answers wrongly today, with what it
// answers.
var knownSpillDefects = map[string]string{}

// spillFrame is n rows: k = i*7919 % keys, seq = i, a 32-byte pad, and f, a Float64
// over fifty values with a NaN every seventh row and a null every eleventh.
func spillFrame(n, keys int) *ursus.LazyFrame { return ursus.Frame(spillColumns(n, keys)...) }

func spillColumns(n, keys int) []*ursus.Column {
	k := make([]int64, n)
	seq := make([]int64, n)
	pad := make([]string, n)
	f := make([]float64, n)
	fok := make([]bool, n)
	for i := range n {
		k[i] = int64((i * 7919) % keys)
		seq[i] = int64(i)
		pad[i] = fmt.Sprintf("%032d", i%97)
		f[i], fok[i] = float64(i%50), i%11 != 0
		if i%7 == 0 {
			f[i] = math.NaN()
		}
	}
	return []*ursus.Column{ursus.Values("k", k), ursus.Values("seq", seq), ursus.Values("pad", pad),
		ursus.ValuesNullable("f", f, fok)}
}

// nestedColumns are a List(String) — a null list every fourth row, an empty one
// after it, and a null element in the rest — and a Struct{a Int64, b String} with a
// null struct every fifth row and a null field every third.
func nestedColumns(n int) (*ursus.Column, *ursus.Column) {
	offs := []int32{0}
	var elems []string
	var eok, lok []bool
	for i := range n {
		switch i % 4 {
		case 0, 1:
		default:
			elems = append(elems, fmt.Sprint("e", i), "")
			eok = append(eok, true, i%3 != 0)
		}
		offs = append(offs, int32(len(elems)))
		lok = append(lok, i%4 != 0)
	}
	list := data.NewList("l", offs, data.NewString("item", elems, validity(eok...)), validity(lok...))
	a := make([]int64, n)
	b := make([]string, n)
	aok, bok, sok := make([]bool, n), make([]bool, n), make([]bool, n)
	for i := range n {
		a[i], b[i] = int64(i), fmt.Sprint("b", i%13)
		aok[i], bok[i], sok[i] = i%3 != 0, i%3 != 1, i%5 != 0
	}
	st := data.NewStruct("s", []*ursus.Column{
		data.NewFixed("a", dtype.Int64, a, validity(aok...)),
		data.NewString("b", b, validity(bok...)),
	}, validity(sok...))
	return list, st
}

// spillCollect runs lf at batch size 64 under limit, spilling into a fresh directory.
func spillCollect(t *testing.T, lf *ursus.LazyFrame, limit int64, dir string) (*ursus.DataFrame, ursus.MemoryStats, error) {
	t.Helper()
	var stats ursus.MemoryStats
	df, err := lf.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithMemoryLimit(limit),
		ursus.WithSpillDir(dir), ursus.WithMemoryStats(&stats))
	return df, stats, err
}

func TestSpillingByHand(t *testing.T) {
	c := ursus.Col
	const limit = 8 << 10
	n := 3000
	list, st := nestedColumns(n)
	base := spillFrame(n, 1500)
	nested := func() *ursus.LazyFrame {
		return ursus.Frame(append(spillColumns(n, 1500), list, st)...)
	}
	// spillsAt requires lf to reach disk under lim and to answer as it does in
	// memory.
	spillsAt := func(lim int64, lf *ursus.LazyFrame, opts ...ursustest.Option) func(*testing.T) string {
		return func(t *testing.T) string {
			want, err := lf.Collect(t.Context(), ursus.WithBatchSize(64))
			if err != nil {
				t.Fatalf("in memory: %v", err)
			}
			got, stats, err := spillCollect(t, lf, lim, t.TempDir())
			if err != nil {
				return err.Error()
			}
			if d := framesDiffer(t, got, want, opts...); d != "" {
				return d
			}
			if stats.Spills == 0 {
				return "answered without spilling, so it proves nothing"
			}
			return ""
		}
	}
	spills := func(lf *ursus.LazyFrame, opts ...ursustest.Option) func(*testing.T) string {
		return spillsAt(limit, lf, opts...)
	}
	// wide holds one partition of k % 7, or of f, which are about 430 rows each,
	// and not the input.
	const wide = 128 << 10
	refused := func(lf *ursus.LazyFrame, words ...string) func(*testing.T) string {
		return func(t *testing.T) string {
			df, _, err := spillCollect(t, lf, limit, t.TempDir())
			return refusal(df, err, ursus.ErrResource, words...)
		}
	}
	// peakAtLeast requires lf's unlimited peak to be at least times the bytes of of.
	peakAtLeast := func(lf, of *ursus.LazyFrame, times float64) func(*testing.T) string {
		return func(t *testing.T) string {
			_, stats, err := spillCollect(t, lf, 0, t.TempDir())
			if err != nil {
				return err.Error()
			}
			o, err := of.Collect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if need := int64(times * float64(o.Batch().NBytes())); stats.Peak < need {
				return fmt.Sprintf("Peak %d, under the %d it holds", stats.Peak, need)
			}
			return ""
		}
	}
	// leavesNoFiles spills lf, reads the result in batches and stops after about half
	// of it, and requires the spill directory to be empty after each run.
	leavesNoFiles := func(lf *ursus.LazyFrame) func(*testing.T) string {
		return func(t *testing.T) string {
			dir := t.TempDir()
			var stats ursus.MemoryStats
			if _, err := lf.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithMemoryLimit(limit),
				ursus.WithSpillDir(dir), ursus.WithMemoryStats(&stats)); err != nil {
				return err.Error()
			}
			if stats.Spills == 0 {
				return "answered without spilling, so it proves nothing"
			}
			if left, _ := os.ReadDir(dir); len(left) > 0 {
				return fmt.Sprintf("%d entries left after a full read", len(left))
			}
			want, err := lf.Count(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			read := 0
			for b, err := range lf.CollectBatches(t.Context(), ursus.WithBatchSize(64),
				ursus.WithMemoryLimit(limit), ursus.WithSpillDir(dir)) {
				if err != nil {
					return err.Error()
				}
				if read += b.Height(); read > int(want)/2 {
					break
				}
			}
			if left, _ := os.ReadDir(dir); len(left) > 0 {
				return fmt.Sprintf("%d entries left after an early break", len(left))
			}
			return ""
		}
	}
	twoKeyed := func() *ursus.LazyFrame {
		return base.WithColumns(c("seq").Sum().Over(c("k")).Alias("t"))
	}
	ordered := ursus.WindowSpec{PartitionBy: []ursus.Expr{c("k")}, OrderBy: []ursus.SortKey{ursus.Desc(c("seq"))}}
	ord := base.Select(c("k"), c("seq").Alias("__ord"), c("pad"))

	cases := []ioCase{
		// --- nested columns through the spill format ---
		{"sort carrying a List(String) and a Struct", spills(nested().Sort(ursus.Asc(c("k")), ursus.Asc(c("seq"))))},
		{"group_by MaintainOrder, First of a list", spills(nested().GroupBy(c("k")).MaintainOrder().
			Agg(c("l").First(), c("s").First(), c("seq").Sum()))},
		{"join with a Struct payload", spills(base.Join(ursus.Frame(ursus.Values("k", spillInts(0, 1500)), st.Slice(0, 1500)),
			ursus.JoinOn(c("k"))), ursustest.IgnoreRowOrder())},

		// --- unique ---
		{"Unique(k)", spills(base.Unique("k"))},
		{"Unique() of whole rows", spills(base.Select(c("k"), c("pad")).Unique())},
		{"Unique over NaN and null keys", spills(base.Unique("f", "pad"))},
		{"Unique carrying a List and a Struct", spills(nested().Unique("k"))},
		{"Unique(k).Head(5) still stops early", func(t *testing.T) string {
			got, _, err := spillCollect(t, base.Unique("k").Head(5), limit, t.TempDir())
			if err != nil {
				return err.Error()
			}
			want, err := base.Unique("k").Head(5).Collect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			return framesDiffer(t, got, want)
		}},
		{"Unique over a column named __ord", spills(ord.Unique("k"))},

		// --- over ---
		{"Sum().Over(k)", spills(twoKeyed())},
		{"Rank and CumSum ordered by seq descending", spills(base.WithColumns(
			c("seq").Rank(ursus.RankOrdinal, false).OverWith(ordered).Alias("r"),
			c("seq").CumSum(false).OverWith(ordered).Alias("cs")))},
		{"two partitionings, k and k % 7", spillsAt(wide, base.WithColumns(
			c("seq").Sum().Over(c("k")).Alias("t"), c("seq").Max().Over(c("k").Mod(7)).Alias("u")))},
		{"First() and Last() over k", spills(base.WithColumns(
			c("seq").First().Over(c("k")).Alias("first"), c("pad").Last().Over(c("k")).Alias("last")))},
		{"Over NaN and null partition keys", spillsAt(wide, base.WithColumns(c("seq").Sum().Over(c("f")).Alias("t")))},
		{"Over carrying a List and a Struct", spills(nested().WithColumns(c("seq").Sum().Over(c("k")).Alias("t")))},
		{"Over over a column named __ord", spills(ord.WithColumns(c("__ord").Sum().Over(c("k")).Alias("t")))},

		// --- group_by ---
		{"group_by MaintainOrder over a column named __ord", spills(ord.GroupBy(c("k")).MaintainOrder().
			Agg(c("__ord").Sum()))},

		// --- the walls that stay, each named ---
		{"Over() with no keys says it has no partition", refused(base.WithColumns(c("seq").Sum().Over().Alias("t")),
			"over", "no partition")},
		{"one partition larger than the limit says so", refused(spillFrame(n, 1).
			WithColumns(c("seq").Sum().Over(c("k")).Alias("t")), "over", "one partition")},

		// --- what an operator that holds its input is charged for ---
		{"over is charged for the copy Finish makes", peakAtLeast(twoKeyed(), base, 2)},
		{"join_asof is charged for the copy Probe makes", peakAtLeast(
			base.JoinAsOf(base.Select(c("seq"), c("k").Alias("r"), c("pad").Alias("p")), ursus.AsOfOn(c("seq"))),
			base.Select(c("seq"), c("k").Alias("r"), c("pad").Alias("p")), 2)},
		{"join_asof refuses between one and two right sides", func(t *testing.T) string {
			right := base.Select(c("seq"), c("k").Alias("r"), c("pad").Alias("p"))
			r, err := right.Collect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			// The keys the sink keeps are 8 bytes a row on top of the right side, so
			// 1.7 right sides hold it once and not twice.
			lim := int64(1.7 * float64(r.Batch().NBytes()))
			df, _, err := spillCollect(t, base.JoinAsOf(right, ursus.AsOfOn(c("seq"))), lim, t.TempDir())
			return refusal(df, err, ursus.ErrResource, "join_asof")
		}},
		{"group_by is charged for its key table", func(t *testing.T) string {
			// 20000 distinct 40-byte keys: the input batch in flight holds them once,
			// the key columns a second time, and the key table, encoded, a third.
			const keys = 20000
			s := make([]string, keys)
			for i := range s {
				s[i] = fmt.Sprintf("%040d", i)
			}
			_, stats, err := spillCollect(t, ursus.Frame(ursus.Values("g", s)).GroupBy(c("g")).Agg(ursus.Len()),
				0, t.TempDir())
			if err != nil {
				return err.Error()
			}
			if need := int64(3 * keys * 40); stats.Peak < need {
				return fmt.Sprintf("Peak %d, under the %d three copies of the keys take", stats.Peak, need)
			}
			return ""
		}},

		// --- the words ---
		{"the hint says what spills and what fails", func(t *testing.T) string {
			_, _, err := spillCollect(t, base.Reverse(), limit, t.TempDir())
			if !errors.Is(err, ursus.ErrResource) {
				return fmt.Sprintf("want a resource error, got %v", err)
			}
			for _, w := range []string{"unique and a partitioned over spill", "join_asof", "merge_sorted",
				"rolling", "group_by_dynamic", "an unpartitioned over"} {
				if !strings.Contains(err.Error(), w) {
					return fmt.Sprintf("the hint does not say %q: %v", w, err)
				}
			}
			return ""
		}},

		// --- cleanup ---
		{"a spilled unique leaves no files", leavesNoFiles(base.Unique("k"))},
		{"a spilled over leaves no files", leavesNoFiles(twoKeyed())},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wrong := tc.run(t)
			why, known := knownSpillDefects[tc.name]
			switch {
			case known && wrong == "":
				t.Errorf("answers correctly now; delete it from knownSpillDefects (%s)", why)
			case known:
				t.Logf("known defect (%s): %s", why, wrong)
			case wrong != "":
				t.Error(wrong)
			}
		})
	}
	for name := range knownSpillDefects {
		if !slices.ContainsFunc(cases, func(c ioCase) bool { return c.name == name }) {
			t.Errorf("knownSpillDefects names %q, which is not a case", name)
		}
	}
}

// spillInts is lo, lo+1, …, hi-1.
func spillInts(lo, hi int) []int64 {
	out := make([]int64, 0, hi-lo)
	for i := lo; i < hi; i++ {
		out = append(out, int64(i))
	}
	return out
}

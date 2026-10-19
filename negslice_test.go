package ursus_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestNegativeSlice is R3 of v0.6.7-scope.md (step 291): a slice from the end, the
// rows [height+offset, height+offset+length) clipped to the rows, in a frame, an
// expression in a Select and in an aggregation, and a list. Every answer is Polars
// 1.44.1's; a negative length is ursus's "to the end", Polars' None.
func TestNegativeSlice(t *testing.T) {
	c := ursus.Col
	a := c("a")
	lf := ursus.Frame(ursus.Values("a", []int64{0, 1, 2, 3, 4, 5}), ursus.Values("g", []int64{1, 1, 1, 1, 1, 1}))
	join := func(q *ursus.LazyFrame) string {
		t.Helper()
		lines := strings.Split(strings.TrimSpace(csvOf(t, q)), "\n")[1:]
		return strings.Join(lines, "|")
	}
	for _, tc := range []struct {
		off, length int
		want        string
	}{
		{-3, 2, "3|4"}, {-3, -1, "3|4|5"}, {-3, 10, "3|4|5"}, {-6, 2, "0|1"},
		{-7, 2, "0"}, {-10, 2, ""}, {-10, 5, "0"}, {-10, -1, "0|1|2|3|4|5"},
		{-1, 0, ""}, {-6, 0, ""}, {1, 2, "1|2"}, {7, 2, ""},
	} {
		name := strconv.Itoa(tc.off) + "," + strconv.Itoa(tc.length)
		if got := join(lf.Slice(tc.off, tc.length).Select(a)); got != tc.want {
			t.Errorf("frame %s: %q, want %q", name, got, tc.want)
		}
		if got := join(lf.Select(a.Slice(tc.off, tc.length))); got != tc.want {
			t.Errorf("expression %s: %q, want %q", name, got, tc.want)
		}
		agg := lf.GroupBy(c("g")).Agg(a.Slice(tc.off, tc.length).List().Eval(ursus.Element().Cast(ursus.String)).List().Join("|")).Select(a)
		if got := join(agg); got != tc.want && !(tc.want == "" && got == `""`) {
			t.Errorf("aggregation %s: %q, want %q", name, got, tc.want)
		}
		if tc.length < 0 {
			continue // List().Slice refuses a negative length; the rest is a long one
		}
		list := lf.Select(a.Implode().List().Slice(tc.off, tc.length).List().Eval(ursus.Element().Cast(ursus.String)).List().Join("|"))
		if got := join(list); got != tc.want && !(tc.want == "" && got == `""`) {
			t.Errorf("list %s: %q, want %q", name, got, tc.want)
		}
	}
	// The string and binary slices clip alike, by runes and bytes.
	text := ursus.Frame(ursus.Values("s", []string{"012345"})).WithColumns(c("s").Cast(ursus.Binary).Alias("b"))
	for _, tc := range []struct {
		off, length int
		want        string
	}{{-3, 2, "34"}, {-7, 2, "0"}, {-10, 2, ""}, {-10, 5, "0"}, {7, 2, ""}, {2, 99, "2345"}} {
		q := text.Select(c("s").Str().Slice(tc.off, tc.length).Alias("str"),
			c("b").Bin().Slice(tc.off, tc.length).Cast(ursus.String).Alias("bin"))
		got, err := q.Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		sv, _ := got.Column[string]("str")
		bv, _ := got.Column[string]("bin")
		if sv.Values()[0] != tc.want || bv.Values()[0] != tc.want {
			t.Errorf("text (%d, %d): str %q, bin %q, want %q", tc.off, tc.length, sv.Values()[0], bv.Values()[0], tc.want)
		}
	}
	// Polars' frame after a filter: the last rows of what is left.
	if got := join(lf.Filter(a.Gt(0)).Slice(-2, 1).Select(a)); got != "4" {
		t.Errorf("after a filter: %q", got)
	}
	if got := join(lf.Head(0).Slice(-2, 1).Select(a)); got != "" {
		t.Errorf("no rows: %q", got)
	}
	df, err := lf.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := df.Slice(t.Context(), -7, 2); err != nil || got.Height() != 1 {
		t.Errorf("eager: %v, %v", got, err)
	}
	if plan, err := lf.Slice(-3, 2).Explain(t.Context()); err != nil || !strings.Contains(plan, "TAIL 3 len 2") {
		t.Errorf("the plan: %v, %v", plan, err)
	}
}

// TestNegativeSliceSpills: a slice from the end holds the last -offset rows, as Tail
// does, and spills them past a memory limit. 20,000 rows in batches of 64 under
// 16 KiB, the window 15,000 rows from the end and 10,000 long, equal to the answer
// without a limit.
func TestNegativeSliceSpills(t *testing.T) {
	const n = 20_000
	v := make([]int64, n)
	for i := range n {
		v[i] = int64(i)
	}
	q := ursus.Frame(ursus.Values("v", v)).Slice(-15_000, 10_000)
	want, err := q.Collect(t.Context(), ursus.WithBatchSize(64))
	if err != nil {
		t.Fatal(err)
	}
	var st ursus.MemoryStats
	got, err := q.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithMemoryLimit(16<<10),
		ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&st))
	if err != nil {
		t.Fatalf("under 16 KiB: %v", err)
	}
	if st.Spills == 0 {
		t.Fatal("answered without spilling, so it proves nothing")
	}
	if got.Height() != 10_000 {
		t.Fatalf("%d rows, want 10,000", got.Height())
	}
	if first, _, _ := got.At[int64](0, "v"); first != 5_000 {
		t.Errorf("the first row is %d, want 5,000", first)
	}
	ursustest.AssertFrameEqual(t, got, want)
}

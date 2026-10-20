package ursus_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestFrameSample is R4 of v0.6.7-scope.md (step 292): a frame's rows drawn. A draw
// cannot be Polars', whose generator is its own (v0.6.6-scope.md §6.3), so its
// properties are held: Polars' sizes and refusals, every column's values of the same
// rows, each a row of the frame, none twice without replacement, the rows' order
// unshuffled, and one seed the same rows.
func TestFrameSample(t *testing.T) {
	letters := []string{"p", "q", "r", "s", "t", "u"}
	lf := ursus.Frame(ursus.Values("a", []int64{0, 1, 2, 3, 4, 5}), ursus.Values("b", letters))
	// rows is the drawn rows' a, each checked against its b.
	rows := func(q *ursus.LazyFrame) []int64 {
		t.Helper()
		df, err := q.Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(df.Columns(), ","); got != "a,b" {
			t.Fatalf("columns %s, want a,b", got)
		}
		a, _ := df.Column[int64]("a")
		b, _ := df.Column[string]("b")
		out := a.Values()
		for i, v := range out {
			if v < 0 || v > 5 || b.Values()[i] != letters[v] {
				t.Fatalf("row %d is (%d, %s), which is no row of the frame", i, v, b.Values()[i])
			}
		}
		return out
	}
	distinct := func(v []int64) bool {
		s := slices.Clone(v)
		slices.Sort(s)
		return len(slices.Compact(s)) == len(v)
	}
	for _, tc := range []struct {
		name          string
		q             *ursus.LazyFrame
		n             int
		repl, ordered bool
	}{
		{"3", lf.SampleN(3, false, false, 7), 3, false, true},
		{"half", lf.SampleFraction(0.5, false, false, 7), 3, false, true},
		{"0.4, truncated", lf.SampleFraction(0.4, false, false, 7), 2, false, true},
		{"10 with replacement", lf.SampleN(10, true, false, 7), 10, true, true},
		{"1.5 with replacement", lf.SampleFraction(1.5, true, false, 7), 9, true, true},
		{"none", lf.SampleN(0, false, false, 7), 0, false, true},
		{"none of none", lf.Head(0).SampleN(0, false, false, 7), 0, false, true},
		{"3 shuffled", lf.SampleN(3, false, true, 7), 3, false, false},
		{"shuffled", lf.Shuffle(7), 6, false, false},
	} {
		got := rows(tc.q)
		if len(got) != tc.n {
			t.Errorf("%s: %d rows, want %d", tc.name, len(got), tc.n)
		}
		if !tc.repl && !distinct(got) {
			t.Errorf("%s: a row twice without replacement: %v", tc.name, got)
		}
		if tc.ordered && !slices.IsSorted(got) {
			t.Errorf("%s: unshuffled, out of the rows' order: %v", tc.name, got)
		}
		if again := rows(tc.q); !slices.Equal(again, got) {
			t.Errorf("%s: one seed drew %v, then %v", tc.name, got, again)
		}
	}
	if got := rows(lf.Shuffle(7)); slices.IsSorted(got) {
		t.Errorf("a shuffle by seed 7 left the rows in order: %v", got)
	}
	if slices.Equal(rows(lf.SampleN(3, false, false, 7)), rows(lf.SampleN(3, false, false, 8))) {
		t.Error("seeds 7 and 8 drew alike")
	}
	if got := rows(lf.SampleN(6, false, false, 7)); !slices.Equal(got, []int64{0, 1, 2, 3, 4, 5}) {
		t.Errorf("all of them unshuffled: %v", got)
	}
	df, err := lf.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := df.SampleN(t.Context(), 2, false, false, 7); err != nil || got.Height() != 2 {
		t.Errorf("eager SampleN: %v, %v", got, err)
	}
	if got, err := df.Shuffle(t.Context(), 7); err != nil || got.Height() != 6 {
		t.Errorf("eager Shuffle: %v, %v", got, err)
	}
	for name, q := range map[string]*ursus.LazyFrame{
		// Polars: "cannot take a larger sample than the total population when
		// `with_replacement=false`".
		"10 without replacement":  lf.SampleN(10, false, false, 7),
		"1.5 without replacement": lf.SampleFraction(1.5, false, false, 7),
		"one of none":             lf.Head(0).SampleN(1, false, false, 7),
	} {
		if _, err := q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), "without replacement") {
			t.Errorf("%s: got %v, want a refusal", name, err)
		}
	}
	if _, err := lf.SampleN(-1, false, false, 7).Collect(t.Context()); err == nil {
		t.Error("a negative n: no error")
	}
}

// TestFrameSampleSpills: a shuffle of 20,000 rows, its join and its sort spilling
// under 256 KiB, is the shuffle without a limit, row for row: the sort by each drawn
// row's place keeps the order a join under a limit does not promise. The draw holds
// the row indices, 80 KB, as one list, which 16 KiB refuses.
func TestFrameSampleSpills(t *testing.T) {
	const n = 20_000
	v, s := make([]int64, n), make([]string, n)
	for i := range n {
		v[i], s[i] = int64(i), []string{"pear", "apple", "fig"}[i%3]
	}
	q := ursus.Frame(ursus.Values("v", v), ursus.Values("s", s)).Shuffle(11)
	want, err := q.Collect(t.Context(), ursus.WithBatchSize(64))
	if err != nil {
		t.Fatal(err)
	}
	var st ursus.MemoryStats
	got, err := q.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithMemoryLimit(256<<10),
		ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&st))
	if err != nil {
		t.Fatalf("under 256 KiB: %v", err)
	}
	if st.Spills == 0 {
		t.Fatal("answered without spilling, so it proves nothing")
	}
	ursustest.AssertFrameEqual(t, got, want)
	if _, err := q.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithMemoryLimit(16<<10),
		ursus.WithSpillDir(t.TempDir())); err == nil || !strings.Contains(err.Error(), "memory limit") {
		t.Errorf("under 16 KiB the indices do not fit: %v", err)
	}
}

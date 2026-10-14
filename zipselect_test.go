package ursus_test

import (
	"bytes"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestLengthChangingExpressionsInSelect is E4 (step 258): in Select, a reshape or a
// filter is a column of its own length. Every answer is Polars 1.44.1's on the same
// frame, Unique's with maintain_order, which ursus's first-seen order is.
func TestLengthChangingExpressionsInSelect(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(ursus.ValuesNullable("a", []int64{3, 1, 0, 2, 5}, []bool{true, true, false, true, true}),
		ursus.Values("b", []int64{10, 20, 30, 40, 50}),
		ursus.ValuesNullable("c", []float64{1, math.NaN(), 2, 0, 3}, []bool{true, true, true, false, true}))
	a, b := c("a"), c("b")
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		{"a sort beside a column", lf.Select(a.Sort(), b), "a,b\n,10\n1,20\n2,30\n3,40\n5,50\n"},
		{"two sorts", lf.Select(a.Sort(), b.SortDesc()), "a,b\n,50\n1,40\n2,30\n3,20\n5,10\n"},
		{"descending, reversed", lf.Select(a.SortDesc().Alias("d"), b.Reverse()), "d,b\n,50\n5,40\n3,30\n2,20\n1,10\n"},
		{"a head beside a tail", lf.Select(a.Head(3), b.Tail(3)), "a,b\n3,30\n1,40\n,50\n"},
		{"a head beside a sum", lf.Select(a.Head(3), b.Sum()), "a,b\n3,150\n1,150\n,150\n"},
		{"a head beside a literal", lf.Select(a.Head(3).Alias("x"), ursus.Lit(1)), "x,literal\n3,1\n1,1\n,1\n"},
		{"a head less its mean", lf.Select(a.Head(3).Sub(a.Mean())), "a\n0.25\n-1.75\n\n"},
		{"a sort plus a column", lf.Select(a.Sort().Add(b)), "a\n\n21\n32\n43\n55\n"},
		{"a chain", lf.Select(a.DropNulls().Mul(2).Head(2)), "a\n6\n2\n"},
		{"reversed, then a head", lf.Select(a.Reverse().Head(2)), "a\n5\n2\n"},
		{"drop_nulls", lf.Select(a.DropNulls()), "a\n3\n1\n2\n5\n"},
		{"drop_nans", lf.Select(c("c").DropNans()), "c\n1.0\n2.0\n\n3.0\n"},
		{"unique", lf.Select(a.Unique()), "a\n3\n1\n\n2\n5\n"},
		{"two filters", lf.Select(a.Filter(b.Gt(20)), b.Filter(b.Gt(20))), "a,b\n,30\n2,40\n5,50\n"},
		{"a slice", lf.Select(a.Slice(1, 2)), "a\n1\n\n"},
		{"a slice from the end", lf.Select(a.Slice(-2, 2)), "a\n2\n5\n"},
		{"get", lf.Select(a.Get(1)), "a\n1\n"},
		{"get beside a column", lf.Select(a.Get(1), b), "a,b\n1,10\n1,20\n1,30\n1,40\n1,50\n"},
		{"sort_by", lf.Select(a.SortByDesc(b)), "a\n5\n2\n\n1\n3\n"},
		{"gather_every", lf.Select(a.GatherEvery(2, 0)), "a\n3\n\n5\n"},
		{"gather_every from 1", lf.Select(a.GatherEvery(2, 1)), "a\n1\n2\n"},
		{"tail(0)", lf.Select(a.Tail(0)), "a\n"},
		{"a head past the end", lf.Select(a.Head(10)), "a\n3\n1\n\n2\n5\n"},
	} {
		for _, size := range []int{1, 3, 8192} {
			var got bytes.Buffer
			if err := tc.q.WriteCSV(t.Context(), &got, ursus.WithBatchSize(size)); err != nil {
				t.Fatalf("%s, batch %d: %v", tc.name, size, err)
			}
			if got.String() != tc.want {
				t.Errorf("%s, batch %d:\ngot  %q\nwant %q", tc.name, size, got.String(), tc.want)
			}
		}
	}
}

// TestLengthChangingExpressionsRefuse: columns of two lengths, as Polars refuses
// them ("Series length 3 doesn't match the DataFrame height of 5"), and what is not
// there yet.
func TestLengthChangingExpressionsRefuse(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(ursus.Values("a", []int64{3, 1, 2}), ursus.Values("b", []int64{1, 2, 3}))
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		"two lengths":         {lf.Select(c("a").Head(2), c("b")), "a Select's columns are of one length"},
		"two lengths, nested": {lf.Select(c("a").Head(2).Add(c("b"))), "a Select's columns are of one length"},
		"several columns":     {lf.Select(c("a", "b").Sort()), "name each column"},
		"a gather":            {lf.Select(c("a").Gather(ursus.Lit([]int64{0}))), "not there yet"},
		"sort_by a head":      {lf.Select(c("a").Head(2).SortBy(c("b"))), "keys of another length"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}

// TestLengthChangingExpressionsSpill: a sort and a reverse beside a column, under
// a tenth of the input, spill and answer as they do in memory, on one thread and on
// four, and leave no file behind.
func TestLengthChangingExpressionsSpill(t *testing.T) {
	const n = 60_000
	a, b := make([]int64, n), make([]int64, n)
	for i := range a {
		a[i] = int64((i * 7919) % 10_007)
		b[i] = int64(i)
	}
	c := ursus.Col
	q := ursus.Frame(ursus.Values("a", a), ursus.Values("b", b)).
		Select(c("a").Sort(), c("b"), c("b").Reverse().Alias("r"))
	want, err := q.Collect(t.Context(), ursus.WithBatchSize(500))
	if err != nil {
		t.Fatal(err)
	}
	for _, threads := range []int{1, 4} {
		var stats ursus.MemoryStats
		dir := t.TempDir()
		got, err := q.Collect(t.Context(), ursus.WithBatchSize(500), ursus.WithThreads(threads),
			ursus.WithMemoryLimit(n*16/10), ursus.WithSpillDir(dir), ursus.WithMemoryStats(&stats))
		if err != nil {
			t.Fatalf("%d threads: %v", threads, err)
		}
		if stats.Spills == 0 {
			t.Errorf("%d threads: nothing spilled", threads)
		}
		ursustest.AssertFrameEqual(t, got, want)
		if left, _ := os.ReadDir(dir); len(left) > 0 {
			t.Errorf("%d threads: %d entries left in the spill directory", threads, len(left))
		}
	}
}

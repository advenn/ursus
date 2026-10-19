package ursus_test

import (
	"math"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestUniqueKeep is R1 of v0.6.7-scope.md (step 289): Polars' unique(keep=,
// maintain_order=True). Every answer is Polars 1.44.1's.
func TestUniqueKeep(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(ursus.Values("a", []int64{1, 2, 1, 3, 2, 1}),
		ursus.Values("b", []string{"x", "y", "x", "z", "y", "w"}),
		ursus.Values("c", []int64{10, 20, 30, 40, 50, 60}))
	twice := ursus.Concat([]*ursus.LazyFrame{lf, lf.Head(2)})
	nan, negz := math.NaN(), math.Copysign(0, -1)
	nulls := ursus.Frame(ursus.ValuesNullable("k", []int64{0, 1, 0, 2, 1}, []bool{false, true, false, true, true}),
		ursus.Values("v", []int64{1, 2, 3, 4, 5}))
	floats := ursus.Frame(ursus.Values("k", []float64{nan, 0, nan, negz, 1}), ursus.Values("v", []int64{1, 2, 3, 4, 5}))
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		{"a, b first", lf.UniqueKeep(ursus.KeepFirst, "a", "b"), "a,b,c\n1,x,10\n2,y,20\n3,z,40\n1,w,60\n"},
		{"a, b last", lf.UniqueKeep(ursus.KeepLast, "a", "b"), "a,b,c\n1,x,30\n3,z,40\n2,y,50\n1,w,60\n"},
		{"a, b any", lf.UniqueKeep(ursus.KeepAny, "a", "b"), "a,b,c\n1,x,10\n2,y,20\n3,z,40\n1,w,60\n"},
		{"a, b none", lf.UniqueKeep(ursus.KeepNone, "a", "b"), "a,b,c\n3,z,40\n1,w,60\n"},
		{"a first", lf.UniqueKeep(ursus.KeepFirst, "a"), "a,b,c\n1,x,10\n2,y,20\n3,z,40\n"},
		{"a last", lf.UniqueKeep(ursus.KeepLast, "a"), "a,b,c\n3,z,40\n2,y,50\n1,w,60\n"},
		{"a none", lf.UniqueKeep(ursus.KeepNone, "a"), "a,b,c\n3,z,40\n"},
		{"whole rows first", twice.UniqueKeep(ursus.KeepFirst), "a,b,c\n1,x,10\n2,y,20\n1,x,30\n3,z,40\n2,y,50\n1,w,60\n"},
		{"whole rows last", twice.UniqueKeep(ursus.KeepLast), "a,b,c\n1,x,30\n3,z,40\n2,y,50\n1,w,60\n1,x,10\n2,y,20\n"},
		{"whole rows none", twice.UniqueKeep(ursus.KeepNone), "a,b,c\n1,x,30\n3,z,40\n2,y,50\n1,w,60\n"},
		{"null keys first", nulls.UniqueKeep(ursus.KeepFirst, "k"), "k,v\n,1\n1,2\n2,4\n"},
		{"null keys last", nulls.UniqueKeep(ursus.KeepLast, "k"), "k,v\n,3\n2,4\n1,5\n"},
		{"null keys none", nulls.UniqueKeep(ursus.KeepNone, "k"), "k,v\n2,4\n"},
		{"NaN and zeros first", floats.UniqueKeep(ursus.KeepFirst, "k").Select(c("v")), "v\n1\n2\n5\n"},
		{"NaN and zeros last", floats.UniqueKeep(ursus.KeepLast, "k").Select(c("v")), "v\n3\n4\n5\n"},
		{"NaN and zeros none", floats.UniqueKeep(ursus.KeepNone, "k").Select(c("v")), "v\n5\n"},
		{"no rows", lf.Head(0).UniqueKeep(ursus.KeepNone), "a,b,c\n"},
	} {
		if got := csvOf(t, tc.q); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
	df, err := lf.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := df.UniqueKeep(t.Context(), ursus.KeepLast, "a"); err != nil || got.Height() != 3 {
		t.Errorf("eager: %v, %v", got, err)
	}
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		// Polars: `unable to find column "zz"`.
		"an unknown column":   {lf.UniqueKeep(ursus.KeepLast, "zz"), "zz"},
		"a keep that is none": {lf.UniqueKeep(ursus.KeepStrategy(9), "a"), "not a KeepStrategy"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}

// TestUniqueKeepSpills: KeepLast and KeepNone are a filter by a window over the keys,
// whose partitions spill past a memory limit, as Over's do. 20,000 rows of 2,000 keys,
// some seen once, in batches of 64 under 16 KiB, equal to the answer without a limit.
func TestUniqueKeepSpills(t *testing.T) {
	const n = 20_000
	k, v := make([]int64, n), make([]int64, n)
	for i := range n {
		k[i], v[i] = int64(i*7919%2000), int64(i)
		if i%997 == 0 {
			k[i] = int64(5000 + i) // a key of its own
		}
	}
	lf := ursus.Frame(ursus.Values("k", k), ursus.Values("v", v))
	for name, q := range map[string]*ursus.LazyFrame{
		"last": lf.UniqueKeep(ursus.KeepLast, "k"),
		"none": lf.UniqueKeep(ursus.KeepNone, "k"),
	} {
		want, err := q.Collect(t.Context(), ursus.WithBatchSize(64))
		if err != nil {
			t.Fatal(err)
		}
		var st ursus.MemoryStats
		got, err := q.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithMemoryLimit(16<<10),
			ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&st))
		if err != nil {
			t.Fatalf("%s under 16 KiB: %v", name, err)
		}
		if st.Spills == 0 {
			t.Fatalf("%s answered without spilling, so it proves nothing", name)
		}
		ursustest.AssertFrameEqual(t, got, want)
	}
}

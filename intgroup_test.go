package ursus_test

// Step 160: a group-by of one integer column keys its integers, never encoded. Its
// answer must be the encoded keys' answer. Grouping by the key cast to String takes
// the encoded path, and the cast is one-to-one, null to null, so the two group
// alike: on one thread in the same first-appearance order, row for row, and on
// several, through the workers' merge, as the same set.

import (
	"fmt"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/ursustest"
)

// intKeyFrame is n rows of (k, v): k of T from raw values that repeat, reach every
// width's extremes and wrap at T's, one row in 20 null; v the row number.
func intKeyFrame[T int8 | int16 | int32 | int64 | uint8 | uint16 | uint32 | uint64](n int, seed uint64) *ursus.LazyFrame {
	rng := rand.New(rand.NewPCG(160, seed))
	edges := []int64{0, -1, 1, math.MinInt64, math.MaxInt64, math.MinInt32, math.MaxInt32,
		math.MaxUint32, -128, 127, 255, 1 << 32}
	ks := make([]T, n)
	valid := make([]bool, n)
	vs := make([]int64, n)
	for i := range n {
		r := int64(rng.IntN(n)) - int64(n/2)
		if rng.IntN(50) == 0 {
			r = edges[rng.IntN(len(edges))]
		}
		ks[i], valid[i], vs[i] = T(r), rng.IntN(20) != 0, int64(i)
	}
	return ursus.Frame(ursus.ValuesNullable("k", ks, valid), ursus.Values("v", vs))
}

func TestIntegerGroupKeysAnswerAsEncodedOnes(t *testing.T) {
	c := ursus.Col
	cases := []struct {
		name  string
		frame func(n int) *ursus.LazyFrame
		key   ursus.Expr
	}{
		{"Int8", func(n int) *ursus.LazyFrame { return intKeyFrame[int8](n, 1) }, c("k")},
		{"Int16", func(n int) *ursus.LazyFrame { return intKeyFrame[int16](n, 2) }, c("k")},
		{"Int32", func(n int) *ursus.LazyFrame { return intKeyFrame[int32](n, 3) }, c("k")},
		{"Int64", func(n int) *ursus.LazyFrame { return intKeyFrame[int64](n, 4) }, c("k")},
		{"Uint8", func(n int) *ursus.LazyFrame { return intKeyFrame[uint8](n, 5) }, c("k")},
		{"Uint16", func(n int) *ursus.LazyFrame { return intKeyFrame[uint16](n, 6) }, c("k")},
		{"Uint32", func(n int) *ursus.LazyFrame { return intKeyFrame[uint32](n, 7) }, c("k")},
		{"Uint64", func(n int) *ursus.LazyFrame { return intKeyFrame[uint64](n, 8) }, c("k")},
		{"Date", func(n int) *ursus.LazyFrame { return intKeyFrame[int32](n, 9) }, c("k").Cast(dtype.Date)},
		{"Datetime", func(n int) *ursus.LazyFrame { return intKeyFrame[int64](n, 10) },
			c("k").Cast(dtype.Datetime(dtype.Micro, ""))},
	}
	aggs := func(ordered bool) []ursus.Expr {
		out := []ursus.Expr{c("v").Sum().Alias("sum"), ursus.Len().Alias("n"), c("v").Min().Alias("min")}
		if ordered {
			out = append(out, c("v").First().Alias("first"), c("v").Last().Alias("last"))
		}
		return out
	}
	// The workers' merge, on 8 threads; the partitioned fold, which a few thousand
	// rows do not reach, is internal/physical's TestAPartitionedFoldOfIntegerKeys.
	for _, tc := range cases {
		for _, threads := range []int{1, 8} {
			t.Run(fmt.Sprintf("%s, %d threads", tc.name, threads), func(t *testing.T) {
				serial := threads == 1
				ints := tc.frame(5_000).GroupBy(tc.key.Alias("k")).Agg(aggs(serial)...).
					WithColumns(c("k").Cast(dtype.String))
				encoded := tc.frame(5_000).GroupBy(tc.key.Cast(dtype.String).Alias("k")).Agg(aggs(serial)...)
				got, err := ints.Collect(t.Context(), ursus.WithThreads(threads), ursus.WithBatchSize(512))
				if err != nil {
					t.Fatal(err)
				}
				want, err := encoded.Collect(t.Context(), ursus.WithThreads(threads), ursus.WithBatchSize(512))
				if err != nil {
					t.Fatal(err)
				}
				if want.Height() < 100 {
					t.Fatalf("the fixture has %d groups", want.Height())
				}
				if serial {
					ursustest.AssertFrameEqual(t, got, want)
				} else {
					// Merged in the order the workers finish.
					ursustest.AssertFrameEqual(t, got, want, ursustest.IgnoreRowOrder())
				}
			})
		}
	}

	// The same, in first-appearance order kept on purpose, with every thread.
	t.Run("MaintainOrder", func(t *testing.T) {
		for _, tc := range cases {
			got, err := tc.frame(20_000).GroupBy(tc.key.Alias("k")).MaintainOrder().Agg(aggs(true)...).
				WithColumns(c("k").Cast(dtype.String)).Collect(t.Context(), ursus.WithThreads(8))
			if err != nil {
				t.Fatal(err)
			}
			want, err := tc.frame(20_000).GroupBy(tc.key.Cast(dtype.String).Alias("k")).MaintainOrder().
				Agg(aggs(true)...).Collect(t.Context(), ursus.WithThreads(8))
			if err != nil {
				t.Fatal(err)
			}
			ursustest.AssertFrameEqual(t, got, want)
		}
	})
}

// TestASpilledIntegerGroupKeyKeepsItsNull: past a memory limit the sink freezes, and
// a null key, resident from the first row, must stay one group. Every null row's
// slot holds 0, which is a resident key too, so a null looked up by its slot's
// value would join group 0, and a resident null routed after the freeze would come
// back as a second null group.
func TestASpilledIntegerGroupKeyKeepsItsNull(t *testing.T) {
	const n = 20_000
	k := make([]int64, n)
	ok := make([]bool, n)
	v := make([]int64, n)
	for i := range n {
		k[i], ok[i], v[i] = int64((i*7919)%4_000), i%13 != 0, int64(i)
		if !ok[i] {
			k[i] = 0
		}
	}
	q := func() *ursus.LazyFrame {
		return ursus.Frame(ursus.ValuesNullable("k", k, ok), ursus.Values("v", v)).
			GroupBy(ursus.Col("k")).Agg(ursus.Len().Alias("n"), ursus.Col("v").Sum().Alias("sum"))
	}
	want, err := q().Collect(t.Context(), ursus.WithThreads(1), ursus.WithBatchSize(256))
	if err != nil {
		t.Fatal(err)
	}
	var stats ursus.MemoryStats
	got, err := q().Collect(t.Context(), ursus.WithThreads(1), ursus.WithBatchSize(256),
		ursus.WithMemoryLimit(32<<10), ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&stats))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Spills < 2 {
		t.Fatalf("spilled %d times, so it proves nothing", stats.Spills)
	}
	ursustest.AssertFrameEqual(t, got, want, ursustest.IgnoreRowOrder())
}

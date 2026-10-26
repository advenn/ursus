package ursus_test

// Steps 381 and 384: a group-by of several integer columns that fit two words, or
// three, with a null bit each packs them, never encoded. Its answer must be the
// encoded keys': grouping by the keys cast to String takes the encoded path, and each
// cast is one-to-one, null to null, so the two group alike, on one thread in the same
// first-appearance order, row for row, and on several, through the workers' merge,
// as the same set.

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/ursustest"
)

// packedKeyFrame is n rows of keys k0, k1, ... of the given types and a row number v:
// each key from few values, so rows share groups, and now and then a width's
// extreme; one row in 15 null.
func packedKeyFrame(n int, seed uint64, types ...dtype.DataType) *ursus.LazyFrame {
	rng := rand.New(rand.NewPCG(381, seed))
	edges := []int64{0, -1, math.MinInt64, math.MaxInt64, math.MinInt32, math.MaxInt32,
		math.MaxUint32, -128, 127, 255, -32768, 65535}
	cols := []*ursus.Column{}
	for k, t := range types {
		raw := make([]int64, n)
		ok := make([]bool, n)
		for i := range n {
			raw[i] = int64(rng.IntN(40)) - 20
			if rng.IntN(60) == 0 {
				raw[i] = edges[rng.IntN(len(edges))]
			}
			ok[i] = rng.IntN(15) != 0
		}
		name := fmt.Sprintf("k%d", k)
		var s *ursus.Column
		switch t {
		case dtype.Int8:
			s = ursus.ValuesNullable(name, narrowed[int8](raw), ok)
		case dtype.Int16:
			s = ursus.ValuesNullable(name, narrowed[int16](raw), ok)
		case dtype.Int32, dtype.Date:
			s = ursus.ValuesNullable(name, narrowed[int32](raw), ok)
		case dtype.Uint16:
			s = ursus.ValuesNullable(name, narrowed[uint16](raw), ok)
		case dtype.Uint64:
			s = ursus.ValuesNullable(name, narrowed[uint64](raw), ok)
		default:
			s = ursus.ValuesNullable(name, raw, ok)
		}
		cols = append(cols, s)
	}
	v := make([]int64, n)
	for i := range v {
		v[i] = int64(i)
	}
	f := ursus.Frame(append(cols, ursus.Values("v", v))...)
	for k, t := range types {
		if t == dtype.Date {
			f = f.WithColumns(ursus.Col(fmt.Sprintf("k%d", k)).Cast(dtype.Date))
		}
	}
	return f
}

func narrowed[T int8 | int16 | int32 | uint16 | uint64](v []int64) []T {
	out := make([]T, len(v))
	for i, x := range v {
		out[i] = T(x)
	}
	return out
}

func TestPackedGroupKeysAnswerAsEncodedOnes(t *testing.T) {
	c := ursus.Col
	cases := [][]dtype.DataType{
		{dtype.Int32, dtype.Int32}, // h2o gb6's
		{dtype.Int8, dtype.Uint16, dtype.Int64},
		{dtype.Uint64, dtype.Int32},
		{dtype.Date, dtype.Int16, dtype.Int8},
		{dtype.Int64, dtype.Int64},              // PDS-H q20's, three words since step 384
		{dtype.Int64, dtype.Date, dtype.Int32},  // q3's
		{dtype.Int64, dtype.Int64, dtype.Int64}, // 195 bits with their nulls: encoded, both ways
	}
	aggs := func(ordered bool) []ursus.Expr {
		out := []ursus.Expr{c("v").Sum().Alias("sum"), ursus.Len().Alias("n"), c("v").Min().Alias("min"),
			c("v").Mod(int64(7)).NUnique().Alias("nu")}
		if ordered {
			out = append(out, c("v").First().Alias("first"), c("v").Last().Alias("last"))
		}
		return out
	}
	queries := func(types []dtype.DataType, n int, ordered bool) (packed, encoded *ursus.LazyFrame) {
		var keys, strs, back []ursus.Expr
		for k := range types {
			name := fmt.Sprintf("k%d", k)
			keys = append(keys, c(name))
			strs = append(strs, c(name).Cast(dtype.String))
			back = append(back, c(name).Cast(dtype.String))
		}
		f := packedKeyFrame(n, uint64(len(types)*10+int(types[0].ID())), types...)
		gp, ge := f.GroupBy(keys...), f.GroupBy(strs...)
		if ordered {
			gp, ge = gp.MaintainOrder(), ge.MaintainOrder()
		}
		return gp.Agg(aggs(ordered)...).WithColumns(back...), ge.Agg(aggs(ordered)...)
	}
	for _, types := range cases {
		name := fmt.Sprint(types)
		for _, threads := range []int{1, 8} {
			t.Run(fmt.Sprintf("%s, %d threads", name, threads), func(t *testing.T) {
				serial := threads == 1
				packed, encoded := queries(types, 8_000, serial)
				got, err := packed.Collect(t.Context(), ursus.WithThreads(threads), ursus.WithBatchSize(512))
				if err != nil {
					t.Fatal(err)
				}
				want, err := encoded.Collect(t.Context(), ursus.WithThreads(threads), ursus.WithBatchSize(512))
				if err != nil {
					t.Fatal(err)
				}
				if want.Height() < 500 {
					t.Fatalf("the fixture has %d groups", want.Height())
				}
				if serial {
					ursustest.AssertFrameEqual(t, got, want)
				} else {
					ursustest.AssertFrameEqual(t, got, want, ursustest.IgnoreRowOrder())
				}
			})
		}
		t.Run(name+", MaintainOrder", func(t *testing.T) {
			packed, encoded := queries(types, 20_000, true)
			got, err := packed.Collect(t.Context(), ursus.WithThreads(8))
			if err != nil {
				t.Fatal(err)
			}
			want, err := encoded.Collect(t.Context(), ursus.WithThreads(8))
			if err != nil {
				t.Fatal(err)
			}
			ursustest.AssertFrameEqual(t, got, want)
		})
		// Past a memory limit the sink freezes, and a new key is routed to a
		// partition by its encoded bytes, as every path routes it.
		for _, threads := range []int{1, 4} {
			t.Run(fmt.Sprintf("%s, spilled on %d threads", name, threads), func(t *testing.T) {
				packed, encoded := queries(types, 20_000, false)
				want, err := encoded.Collect(t.Context(), ursus.WithThreads(1), ursus.WithBatchSize(256))
				if err != nil {
					t.Fatal(err)
				}
				var stats ursus.MemoryStats
				dir := t.TempDir()
				got, err := packed.Collect(t.Context(), ursus.WithThreads(threads), ursus.WithBatchSize(256),
					ursus.WithMemoryLimit(48<<10), ursus.WithSpillDir(dir), ursus.WithMemoryStats(&stats))
				if err != nil {
					t.Fatal(err)
				}
				if stats.Spills == 0 {
					t.Error("nothing spilled under 48 KiB")
				}
				ursustest.AssertFrameEqual(t, got, want, ursustest.IgnoreRowOrder())
				if left, _ := os.ReadDir(dir); len(left) > 0 {
					t.Errorf("%d entries left in the spill directory", len(left))
				}
			})
		}
	}
}

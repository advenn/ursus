package ursus_test

import (
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus"
)

// TestANarrowIntegersSumAndMeanAnswerAsInt64s: a sum and a mean of an integer of 32
// bits or fewer add in int64 partials (step 215). Each answers as the same column cast
// to Int64 does, through the 128-bit path: grouped on one thread and on eight, where
// the workers' partials merge, and as a window.
func TestANarrowIntegersSumAndMeanAnswerAsInt64s(t *testing.T) {
	rng := rand.New(rand.NewPCG(215, 2))
	const n = 30_000
	k := make([]int64, n)
	for i := range k {
		k[i] = int64(rng.IntN(300))
	}
	c := ursus.Col
	cols := []*ursus.Column{ursus.Values("k", k)}
	names := []string{}
	add := func(name string, col *ursus.Column) { cols = append(cols, col); names = append(names, name) }
	i8, i16, i32 := make([]int8, n), make([]int16, n), make([]int32, n)
	u8, u16, u32 := make([]uint8, n), make([]uint16, n), make([]uint32, n)
	ok := make([]bool, n)
	for i := range n {
		i8[i], i16[i], i32[i] = int8(rng.Int()), int16(rng.Int()), int32(rng.Int())
		u8[i], u16[i], u32[i] = uint8(rng.Int()), uint16(rng.Int()), uint32(rng.Int())
		ok[i] = rng.IntN(10) != 0
	}
	add("i8", ursus.ValuesNullable("i8", i8, ok))
	add("i16", ursus.Values("i16", i16))
	add("i32", ursus.ValuesNullable("i32", i32, ok))
	add("u8", ursus.Values("u8", u8))
	add("u16", ursus.ValuesNullable("u16", u16, ok))
	add("u32", ursus.Values("u32", u32))
	frame := ursus.Frame(cols...)

	for _, name := range names {
		narrow, wide := c(name), c(name).Cast(ursus.Int64)
		for _, threads := range []int{1, 8} {
			grouped := func(v ursus.Expr) *ursus.LazyFrame {
				return frame.GroupBy(c("k")).Agg(v.Sum().Alias("s"), v.Mean().Alias("m"))
			}
			got, err := grouped(narrow).Collect(t.Context(), ursus.WithThreads(threads), ursus.WithBatchSize(1024))
			if err != nil {
				t.Fatal(err)
			}
			want, err := grouped(wide).Collect(t.Context(), ursus.WithThreads(threads), ursus.WithBatchSize(1024))
			if err != nil {
				t.Fatal(err)
			}
			if !sameRows(t, got, want) {
				t.Fatalf("%s, %d threads: the grouped sum and mean differ from Int64's", name, threads)
			}
		}
		over := func(v ursus.Expr) *ursus.LazyFrame {
			return frame.Select(v.Sum().Over(c("k")).Alias("s"), v.Mean().Over(c("k")).Alias("m"))
		}
		got, err := over(narrow).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		want, err := over(wide).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if !sameRows(t, got, want) {
			t.Fatalf("%s: the window sum and mean differ from Int64's", name)
		}
	}
}

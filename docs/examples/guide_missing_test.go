package examples_test

import (
	"math"
	"time"

	"github.com/advenn/ursus"
)

// readings is the missing-data chapter's frame: hourly temperatures with gaps, and a
// backup reading for some hours.
func readings() *ursus.LazyFrame {
	ok := []bool{true, false, false, true, true, false}
	return ursus.Frame(
		ursus.Values("hour", []int64{0, 1, 2, 3, 4, 5}),
		ursus.ValuesNullable("temp", []float64{10, 0, 0, 13, 12, 0}, ok),
		ursus.ValuesNullable("backup", []float64{9, 11, 0, 0, 0, 14},
			[]bool{true, true, false, false, false, true}),
	)
}

// --- the user guide: missing data -------------------------------------------------

func Example_missingCount() {
	show(readings().Select(ursus.All().NullCount()))
	// Output:
	// shape: (1, 3)
	// ┌────────┬────────┬────────┐
	// │ hour   │ temp   │ backup │
	// │ Uint64 │ Uint64 │ Uint64 │
	// ├────────┼────────┼────────┤
	// │ 0      │ 3      │ 3      │
	// └────────┴────────┴────────┘
}

func Example_missingFillValue() {
	temp := ursus.Col("temp")
	show(readings().Select(
		ursus.Col("hour"),
		temp,
		temp.FillNullWith(0.0).Alias("zero"),
		temp.FillNull(ursus.FillMean).Alias("mean"),
		ursus.Coalesce(temp, ursus.Col("backup"), ursus.Lit(-1.0)).Alias("coalesced"),
	))
	// Output:
	// shape: (6, 5)
	// ┌───────┬─────────┬─────────┬────────────────────┬───────────┐
	// │ hour  │ temp    │ zero    │ mean               │ coalesced │
	// │ Int64 │ Float64 │ Float64 │ Float64            │ Float64   │
	// ├───────┼─────────┼─────────┼────────────────────┼───────────┤
	// │ 0     │ 10      │ 10      │ 10                 │ 10        │
	// │ 1     │ null    │ 0       │ 11.666666666666666 │ 11        │
	// │ 2     │ null    │ 0       │ 11.666666666666666 │ -1        │
	// │ 3     │ 13      │ 13      │ 13                 │ 13        │
	// │ 4     │ 12      │ 12      │ 12                 │ 12        │
	// │ 5     │ null    │ 0       │ 11.666666666666666 │ 14        │
	// └───────┴─────────┴─────────┴────────────────────┴───────────┘
}

func Example_missingFillNeighbours() {
	temp := ursus.Col("temp")
	show(readings().Select(
		ursus.Col("hour"),
		temp,
		temp.FillNull(ursus.FillForward).Alias("forward"),
		temp.ForwardFill(1).Alias("forward_1"),
		temp.FillNull(ursus.FillBackward).Alias("backward"),
		temp.Interpolate().Alias("interpolated"),
	))
	// Output:
	// shape: (6, 6)
	// ┌───────┬─────────┬─────────┬───────────┬──────────┬──────────────┐
	// │ hour  │ temp    │ forward │ forward_1 │ backward │ interpolated │
	// │ Int64 │ Float64 │ Float64 │ Float64   │ Float64  │ Float64      │
	// ├───────┼─────────┼─────────┼───────────┼──────────┼──────────────┤
	// │ 0     │ 10      │ 10      │ 10        │ 10       │ 10           │
	// │ 1     │ null    │ 10      │ 10        │ 13       │ 11           │
	// │ 2     │ null    │ 10      │ null      │ 13       │ 12           │
	// │ 3     │ 13      │ 13      │ 13        │ 13       │ 13           │
	// │ 4     │ 12      │ 12      │ 12        │ 12       │ 12           │
	// │ 5     │ null    │ 12      │ 12        │ null     │ null         │
	// └───────┴─────────┴─────────┴───────────┴──────────┴──────────────┘
}

func Example_missingDrop() {
	show(readings().DropNulls("temp"))
	show(readings().DropNulls())
	// Output:
	// shape: (3, 3)
	// ┌───────┬─────────┬─────────┐
	// │ hour  │ temp    │ backup  │
	// │ Int64 │ Float64 │ Float64 │
	// ├───────┼─────────┼─────────┤
	// │ 0     │ 10      │ 9       │
	// │ 3     │ 13      │ null    │
	// │ 4     │ 12      │ null    │
	// └───────┴─────────┴─────────┘
	//
	// shape: (1, 3)
	// ┌───────┬─────────┬─────────┐
	// │ hour  │ temp    │ backup  │
	// │ Int64 │ Float64 │ Float64 │
	// ├───────┼─────────┼─────────┤
	// │ 0     │ 10      │ 9       │
	// └───────┴─────────┴─────────┘
}

func Example_missingFrameFill() {
	show(readings().FillNull(0.0))
	// Output:
	// shape: (6, 3)
	// ┌───────┬─────────┬─────────┐
	// │ hour  │ temp    │ backup  │
	// │ Int64 │ Float64 │ Float64 │
	// ├───────┼─────────┼─────────┤
	// │ 0     │ 10      │ 9       │
	// │ 1     │ 0       │ 11      │
	// │ 2     │ 0       │ 0       │
	// │ 3     │ 13      │ 0       │
	// │ 4     │ 12      │ 0       │
	// │ 5     │ 0       │ 14      │
	// └───────┴─────────┴─────────┘
}

func Example_missingLimit() {
	show(readings().FillNull(ursus.FillForward.Limit(1)))
	// Output:
	// shape: (6, 3)
	// ┌───────┬─────────┬─────────┐
	// │ hour  │ temp    │ backup  │
	// │ Int64 │ Float64 │ Float64 │
	// ├───────┼─────────┼─────────┤
	// │ 0     │ 10      │ 9       │
	// │ 1     │ 10      │ 11      │
	// │ 2     │ null    │ 11      │
	// │ 3     │ 13      │ null    │
	// │ 4     │ 12      │ null    │
	// │ 5     │ 12      │ 14      │
	// └───────┴─────────┴─────────┘
}

func Example_missingDropNans() {
	nan := math.NaN()
	present := []bool{true, true, false, true} // the third is null
	ratios := ursus.Frame(
		ursus.Values("id", []int64{1, 2, 3, 4}),
		ursus.ValuesNullable("ratio", []float64{0.5, nan, 0, 2}, present),
	)
	show(ratios.DropNans())
	// Output:
	// shape: (3, 2)
	// ┌───────┬─────────┐
	// │ id    │ ratio   │
	// │ Int64 │ Float64 │
	// ├───────┼─────────┤
	// │ 1     │ 0.5     │
	// │ 3     │ null    │
	// │ 4     │ 2       │
	// └───────┴─────────┘
}

func Example_missingInterpolateFrame() {
	day := func(d int) time.Time { return time.Date(2026, 1, d, 0, 0, 0, 0, time.UTC) }
	ends := []bool{true, false, false, true} // two rows missing between
	gappy := ursus.Frame(
		ursus.ValuesNullable("day", []time.Time{day(1), {}, {}, day(7)}, ends),
		ursus.ValuesNullable("level", []int64{10, 0, 0, 40}, ends),
		ursus.ValuesNullable("note", []string{"start", "", "", "end"}, ends),
	).WithColumns(ursus.Col("day").Cast(ursus.Date))
	show(gappy.Interpolate())
	// Output:
	// shape: (4, 3)
	// ┌────────────┬─────────┬─────────┐
	// │ day        │ level   │ note    │
	// │ Date       │ Float64 │ String  │
	// ├────────────┼─────────┼─────────┤
	// │ 2026-01-01 │ 10      │ "start" │
	// │ 2026-01-03 │ 20      │ null    │
	// │ 2026-01-05 │ 30      │ null    │
	// │ 2026-01-07 │ 40      │ "end"   │
	// └────────────┴─────────┴─────────┘
}

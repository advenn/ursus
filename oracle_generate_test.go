package ursus_test

// The generate group (step 501, from generate_test.go's hand answers): int_range,
// linear_space, repeat, ones and zeros, and their per-row forms, each spelled as
// bench/scripts/oracle/generate.py asks Polars. A generator of literals alone has no
// input frame: its arguments are the case's params, read here, and it is selected
// from a frame of one row. Polars 2 names repeat after its first input, `literal`, and
// makes a literal integer an Int32, where ursus names it `repeat` and makes it an
// Int64 (feature-gaps.md §9 and §11).

import (
	"time"

	"github.com/advenn/ursus"
)

// oracleParamInt, oracleParamFloat and oracleParamString are the case's param name,
// a number or text.
func oracleParamInt(in oracleInputs, name string) int64 {
	v, ok := in.params[name]
	if !ok {
		in.t.Fatalf("the case has no param %q", name)
	}
	return oracleInt(in.t, v)
}

func oracleParamFloat(in oracleInputs, name string) float64 {
	v, ok := in.params[name]
	if !ok {
		in.t.Fatalf("the case has no param %q", name)
	}
	return oracleFloat(in.t, v)
}

func oracleParamString(in oracleInputs, name string) string {
	v, ok := in.params[name]
	if !ok {
		in.t.Fatalf("the case has no param %q", name)
	}
	return oracleString(in.t, v)
}

func init() {
	c := ursus.Col
	// lone selects a generator of literals alone from a frame of one row: its length
	// is the generator's own.
	lone := func(e func(in oracleInputs) ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			return ursus.Frame(ursus.Values(oracleHelper+"one", []int64{0})).Select(e(in))
		}
	}
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	intRange := lone(func(in oracleInputs) ursus.Expr {
		return ursus.IntRange(oracleParamInt(in, "start"), oracleParamInt(in, "end"), int(oracleParamInt(in, "step")))
	})
	closed := map[string][]ursus.Closed{"both": nil, "left": {ursus.ClosedLeft}, "right": {ursus.ClosedRight}, "none": {ursus.ClosedNone}}
	linearSpace := lone(func(in oracleInputs) ursus.Expr {
		return ursus.LinearSpace(oracleParamFloat(in, "start"), oracleParamFloat(in, "end"), int(oracleParamInt(in, "num_samples")),
			closed[oracleParamString(in, "closed")]...)
	})
	n := func(in oracleInputs) int { return int(oracleParamInt(in, "num_samples")) }
	day := func(d int) time.Time { return time.Date(2024, 1, d, 0, 0, 0, 0, time.UTC) }
	naive := ursus.Datetime(ursus.Micro, "")
	oracleGroups["generate"] = map[string]oracleQuery{
		"int_range_0_5_1":  intRange,
		"int_range_0_5_2":  intRange,
		"int_range_5_0_m2": intRange,
		"int_range_5_0_1":  intRange,
		"a_row_index": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().WithColumns(ursus.IntRange(0, ursus.Len(), 1).Alias("i")).Select(c("a", "i"))
		},
		"from_aggregates":              sel(ursus.IntRange(c("a").Min(), c("a").Max().Add(2), 1)),
		"beside_a_column":              sel(c("a"), ursus.IntRange(10, 13, 1).Alias("r")),
		"summed":                       sel(ursus.IntRange(0, 5, 1).Sum().Cast(ursus.Int64)),
		"linear_space_0_1_5":           linearSpace,
		"linear_space_far_end_exactly": linearSpace,
		"linear_space_not_a_step_past": linearSpace,
		"linear_space_closed_left":     linearSpace,
		"linear_space_closed_right":    linearSpace,
		"linear_space_closed_none":     linearSpace,
		"linear_space_one_sample_open": linearSpace,
		"linear_space_no_sample":       linearSpace,
		"linear_space_float32":         lone(func(in oracleInputs) ursus.Expr { return ursus.LinearSpace(float32(0), float32(1), n(in)) }),
		"linear_space_dates": lone(func(in oracleInputs) ursus.Expr {
			return ursus.LinearSpace(ursus.Lit(day(1)).Cast(ursus.Date), ursus.Lit(day(2)).Cast(ursus.Date), n(in))
		}),
		"linear_space_instants": lone(func(in oracleInputs) ursus.Expr {
			return ursus.LinearSpace(ursus.Lit(day(1)).Cast(naive), ursus.Lit(day(2)).Cast(naive), n(in))
		}),
		"linear_space_truncated_toward_zero": lone(func(in oracleInputs) ursus.Expr {
			return ursus.LinearSpace(ursus.Lit(day(1).Add(3*time.Microsecond)).Cast(naive), ursus.Lit(day(1)).Cast(naive), n(in)).Dt().Microsecond()
		}),
		"linear_space_durations": lone(func(in oracleInputs) ursus.Expr {
			return ursus.LinearSpace(ursus.Lit(time.Duration(0)), ursus.Lit(time.Second), n(in)).Cast(ursus.Int64)
		}),
		"repeat_1_3": lone(func(in oracleInputs) ursus.Expr {
			return ursus.Repeat(oracleParamInt(in, "value"), int(oracleParamInt(in, "n")))
		}),
		"repeat_a_string": lone(func(in oracleInputs) ursus.Expr {
			return ursus.Repeat(oracleParamString(in, "value"), int(oracleParamInt(in, "n")))
		}),
		"repeat_none": lone(func(in oracleInputs) ursus.Expr {
			return ursus.Repeat(oracleParamInt(in, "value"), int(oracleParamInt(in, "n")))
		}),
		"repeat_a_total":          sel(ursus.Repeat(c("a").Sum().Cast(ursus.Int64), ursus.Len())),
		"ones_2":                  lone(func(in oracleInputs) ursus.Expr { return ursus.Ones(int(oracleParamInt(in, "n"))) }),
		"zeros_2":                 lone(func(in oracleInputs) ursus.Expr { return ursus.Zeros(int(oracleParamInt(in, "n"))) }),
		"int_ranges":              sel(ursus.IntRanges(c("s"), c("e"), c("st"))),
		"linear_spaces":           sel(ursus.LinearSpaces(c("s"), c("e"), 3)),
		"linear_spaces_by_counts": sel(ursus.LinearSpaces(0, 1, c("a")).Alias("l")),
		"repeat_by":               sel(c("s").RepeatBy(c("a"))),
		"repeat_by_a_null_count":  sel(c("a").RepeatBy(c("s"))),
		"in_an_aggregation": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().GroupBy(c("g")).Agg(ursus.IntRange(0, ursus.Len(), 1).Alias("r")).Sort(ursus.Asc(c("g"))).Select(c("r"))
		},
		"int_ranges_gen":        sel(ursus.IntRanges(c("s"), c("e"), c("st"))),
		"linear_spaces_gen":     sel(ursus.LinearSpaces(c("s"), c("e"), c("n")).Alias("l"), ursus.LinearSpaces(c("f"), 1, 4).Alias("f")),
		"repeat_by_gen":         sel(c("f").RepeatBy(c("n"))),
		"linear_space_gen_ends": linearSpace,
	}
	// Repeat is named repeat here, and a literal integer in it is an Int64; Polars 2
	// names it after its first input, literal, and makes the integer an Int32
	// (feature-gaps.md §9, step 263; §11).
	repeat := func(dtype bool) oracleDifference {
		d := oracleDifference{why: "Repeat is named repeat here, literal in Polars 2", names: map[string]string{"literal": "repeat"}}
		if dtype {
			d.why += "; a literal integer in it is an Int64 here, an Int32 in Polars"
			d.dtypes = map[string]string{"literal": "Int64"}
		}
		return d
	}
	oracleDifferences["generate/repeat_1_3"] = repeat(true)
	oracleDifferences["generate/repeat_none"] = repeat(true)
	oracleDifferences["generate/repeat_a_string"] = repeat(false)
	oracleDifferences["generate/repeat_a_total"] = oracleDifference{
		why:   "Repeat is named repeat here; Polars 2 names it after its first input, a",
		names: map[string]string{"a": "repeat"},
	}
}

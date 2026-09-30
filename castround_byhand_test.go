package ursus_test

// A float64 is not a go-between: audit.md §6, S1, S2 and S4.
//
// One helper decided every conversion that touched a float — t := T(v), and a value
// was lossy if float64(t) != v — and it was wrong in three different ways:
//
//   - A STRING was parsed into a float64 first, so the test compared a value that
//     had already rounded with itself: "9007199254740993" became …992.
//   - The RESULT of a Float32 computation, which is meant to round, was nulled:
//     sqrt(2) as a Float32 was null, and ErrInternal in a non-nullable column.
//   - A CAST to a float refused, or nulled, the rounding that every float is:
//     0.1 cast to Float32.
//
// The rule these cases are answered by is the one chosen for this step: a cast to a
// float rounds once to the nearest value of that width, and only a finite value
// whose nearest float is an infinity is unrepresentable — refused by Cast, null
// under CastLossy. A string parses at the width it is cast to.

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// knownCastDefects names each case that answers wrongly today, with what it
// answers. Emptied by the commits that fix them; a listed case that answers
// correctly fails as stale, and an unlisted one that answers wrongly fails.
var knownCastDefects = map[string]string{}

type castCase struct {
	name string
	got  *ursus.LazyFrame
	want *ursus.LazyFrame // nil for a refusal
	// refuse lists what a refusal must name. It must be ErrValue, never
	// ErrInternal, and must not say notSay.
	refuse []string
	notSay string
}

func TestCastsAndParsesByHand(t *testing.T) {
	c := ursus.Col
	f32 := func(v ...float32) *ursus.LazyFrame { return ursus.Frame(ursus.Values("x", v)) }
	i64 := func(v ...int64) *ursus.LazyFrame { return ursus.Frame(ursus.Values("x", v)) }
	u64 := func(v ...uint64) *ursus.LazyFrame { return ursus.Frame(ursus.Values("x", v)) }
	str := func(v ...string) *ursus.LazyFrame { return ursus.Frame(ursus.Values("x", v)) }
	f64 := func(v ...float64) *ursus.LazyFrame { return ursus.Frame(ursus.Values("x", v)) }
	boolean := func(v ...bool) *ursus.LazyFrame { return ursus.Frame(ursus.Values("x", v)) }
	nullF32 := ursus.Frame(ursus.ValuesNullable("x", []float32{0}, []bool{false}))
	cast := func(lf *ursus.LazyFrame, to ursus.DataType) *ursus.LazyFrame {
		return lf.Select(c("x").Cast(to))
	}
	lossy := func(lf *ursus.LazyFrame, to ursus.DataType) *ursus.LazyFrame {
		return lf.Select(c("x").CastLossy(to))
	}
	column := func(dt dtype.DataType, v any) *ursus.LazyFrame {
		var col *data.Column
		switch v := v.(type) {
		case []int64:
			col = data.NewFixed("x", dt, v, bitmap.AllSet(len(v)))
		case []i128.Int128:
			col = data.NewFixed("x", dt, v, bitmap.AllSet(len(v)))
		}
		return ursus.Frame(col)
	}
	const (
		p24     = 1 << 24
		p53     = 1 << 53
		between = p53 + 1<<29 + 1 // rounds up to 2^53+2^30 as a float32, and to 2^53 through a float64
	)
	sum := func(v ...int64) *ursus.LazyFrame { return i64(v...).GroupBy().Agg(c("x").Sum().Alias("x")) }
	sqrt2 := float32(math.Sqrt(2))

	cases := []castCase{
		// --- casts to a float ---
		{name: "f64 0.1 → Float32", got: cast(f64(0.1), ursus.Float32), want: f32(float32(0.1))},
		{name: "f64 0.1 → Float32, lossy", got: lossy(f64(0.1), ursus.Float32), want: f32(float32(0.1))},
		// Above MaxFloat32, but nearer to it than to 2^128: it rounds down.
		{name: "f64 3.4028235e38 → Float32", got: cast(f64(3.4028235e38), ursus.Float32),
			want: f32(math.MaxFloat32)},
		{name: "i64 2^24+1 → Float32", got: cast(i64(p24+1), ursus.Float32), want: f32(p24)},
		{name: "i64 2^53+2^29+1 → Float32", got: cast(i64(between), ursus.Float32),
			want: f32(float32(p53 + 1<<30))},
		{name: "u64 2^53+2^29+1 → Float32", got: cast(u64(between), ursus.Float32),
			want: f32(float32(p53 + 1<<30))},
		// 3(2^63−1) + 2052 = 2^64+2^63+2^11+1, which is past half an ulp (2^11) above
		// 2^64+2^63: the nearest float64 is 2^64+2^63+2^12.
		{name: "Int128 2^64+2^63+2^11+1 → Float64",
			got:  sum(math.MaxInt64, math.MaxInt64, math.MaxInt64, 2052).Select(c("x").Cast(ursus.Float64)),
			want: f64(0x1p64 + 0x1p63 + 0x1p12)},
		// 2(2^63−1) + 2^40+3 = 2^64+2^40+1, past half a float32 ulp (2^40) above 2^64.
		{name: "Int128 2^64+2^40+1 → Float32",
			got:  sum(math.MaxInt64, math.MaxInt64, 1<<40+3).Select(c("x").Cast(ursus.Float32)),
			want: f32(0x1p64 + 0x1p41)},
		{name: "Decimal(38,0) 2^53+2^29+1 → Float32",
			got:  cast(column(dtype.Decimal(38, 0), []i128.Int128{i128.FromInt64(between)}), ursus.Float32),
			want: f32(float32(p53 + 1<<30))},
		{name: "Decimal(10,2) 0.10 → Float32",
			got:  cast(column(dtype.Decimal(10, 2), []i128.Int128{i128.FromInt64(10)}), ursus.Float32),
			want: f32(float32(0.1))},
		{name: "Datetime(ns) 1700000000000000001 → Uint64",
			got:  cast(column(dtype.Datetime(dtype.Nano, ""), []int64{1700000000000000001}), ursus.Uint64),
			want: u64(1700000000000000001)},
		{name: "Duration(ns) 2^53+1 → Int128",
			got:  cast(column(dtype.Duration(dtype.Nano), []int64{p53 + 1}), ursus.Int128),
			want: i64(p53 + 1).Select(c("x").Cast(ursus.Int128))},

		// --- strings ---
		{name: `"9007199254740993" → Int64`, got: cast(str("9007199254740993"), ursus.Int64),
			want: i64(p53 + 1)},
		{name: `"9007199254740993" ToInteger`, got: str("9007199254740993").Select(c("x").Str().ToInteger()),
			want: i64(p53 + 1)},
		{name: `"9223372036854775807" → Uint64`, got: cast(str("9223372036854775807"), ursus.Uint64),
			want: u64(math.MaxInt64)},
		{name: `"18446744073709551615" → Uint64`, got: cast(str("18446744073709551615"), ursus.Uint64),
			want: u64(math.MaxUint64)},
		{name: `"256" → Uint8`, got: cast(str("256"), ursus.Uint8),
			refuse: []string{`"256"`, "row 0", "Uint8"}, notSay: "cannot parse"},
		{name: `"256" → Uint8 beside a null`,
			got:    cast(ursus.Frame(ursus.ValuesNullable("x", []string{"256", ""}, []bool{true, false})), ursus.Uint8),
			refuse: []string{`"256"`, "Uint8"}, notSay: "cannot parse"},
		{name: `"-1" → Uint8`, got: cast(str("-1"), ursus.Uint8),
			refuse: []string{`"-1"`, "Uint8"}, notSay: "cannot parse"},
		{name: `"0.1" → Float32`, got: cast(str("0.1"), ursus.Float32), want: f32(float32(0.1))},
		{name: `"0.1" → Float32, lossy`, got: lossy(str("0.1"), ursus.Float32), want: f32(float32(0.1))},
		// Just above 1+2^-24, the midpoint between 1 and the next float32. Through a
		// float64 it lands ON the midpoint, and the tie goes to 1.
		{name: `"1.00000005960464477539063" → Float32`,
			got:  cast(str("1.00000005960464477539063"), ursus.Float32),
			want: f32(math.Float32frombits(0x3f800001))},
		{name: `"3.4e39" → Float32`, got: cast(str("3.4e39"), ursus.Float32),
			refuse: []string{`"3.4e39"`, "Float32"}, notSay: "cannot parse"},
		{name: `"1e400" → Float64`, got: cast(str("1e400"), ursus.Float64),
			refuse: []string{`"1e400"`, "Float64"}, notSay: "cannot parse"},
		{name: `"1e-50" → Float32`, got: cast(str("1e-50"), ursus.Float32), want: f32(0)},

		// --- Float32 maths ---
		{name: "f32 Sqrt(2)", got: f32(2).Select(c("x").Sqrt()), want: f32(sqrt2)},
		{name: "f32 Sqrt(2) beside a null",
			got:  ursus.Frame(ursus.ValuesNullable("x", []float32{2, 0}, []bool{true, false})).Select(c("x").Sqrt()),
			want: ursus.Frame(ursus.ValuesNullable("x", []float32{sqrt2, 0}, []bool{true, false}))},
		{name: "f32 Ln(2)", got: f32(2).Select(c("x").Ln()), want: f32(float32(math.Ln2))},
		{name: "f32 Exp(89)", got: f32(89).Select(c("x").Exp()), want: f32(float32(math.Inf(1)))},
		{name: "f32 Round(0.1, 1)", got: f32(0.1).Select(c("x").Round(1)), want: f32(0.1)},
		// float32(2.675) is 2.67499995…, which rounds down.
		{name: "f32 Round(2.675, 2)", got: f32(2.675).Select(c("x").Round(2)), want: f32(2.67)},
		{name: "folded Lit(float32(2)).Sqrt()",
			got:  boolean(true).Select(ursus.Lit(float32(2)).Sqrt().Alias("x")),
			want: f32(sqrt2)},

		// --- IsIn ---
		{name: "f32 IsIn(0.1)", got: f32(0.1, 0.2).Select(c("x").IsIn(0.1)), want: boolean(true, false)},
		{name: `i8 IsIn("300")`, got: ursus.Frame(ursus.Values("x", []int8{1, 2})).Select(c("x").IsIn("300")),
			refuse: []string{`"300"`, "Int8"}},
		{name: `i64 IsIn("9007199254740993")`,
			got:  i64(p53, p53+1).Select(c("x").IsIn("9007199254740993")),
			want: boolean(false, true)},
		{name: `u64 IsIn("18446744073709551615")`,
			got:  u64(math.MaxUint64).Select(c("x").IsIn("18446744073709551615")),
			want: boolean(true)},

		// --- controls: right today, and must stay right ---
		{name: "control: f64 1e39 → Float32", got: cast(f64(1e39), ursus.Float32),
			refuse: []string{"not representable", "Float32"}},
		{name: "control: f64 1e39 → Float32, lossy", got: lossy(f64(1e39), ursus.Float32), want: nullF32},
		{name: "control: NaN and ±Inf → Float32",
			got:  cast(f64(math.NaN(), math.Inf(1), math.Inf(-1)), ursus.Float32),
			want: f32(float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1)))},
		{name: "control: MaxUint64 → Float32", got: cast(u64(math.MaxUint64), ursus.Float32), want: f32(0x1p64)},
		{name: "control: f64 2^63 → Int64", got: cast(f64(0x1p63), ursus.Int64),
			refuse: []string{"not representable", "Int64"}},
		{name: `control: "256" → Uint8, lossy`, got: lossy(str("256"), ursus.Uint8),
			want: ursus.Frame(ursus.ValuesNullable("x", []uint8{0}, []bool{false}))},
		{name: `control: "+5" and "-0" → Uint8`, got: cast(str("+5", "-0"), ursus.Uint8),
			want: ursus.Frame(ursus.Values("x", []uint8{5, 0}))},
		{name: `control: "inf" and "NaN" → Float32`, got: cast(str("inf", "NaN"), ursus.Float32),
			want: f32(float32(math.Inf(1)), float32(math.NaN()))},
		{name: `control: "abc" → Int64`, got: cast(str("abc"), ursus.Int64), refuse: []string{"cannot parse"}},
		{name: `control: "1e3" → Int64`, got: cast(str("1e3"), ursus.Int64), refuse: []string{"cannot parse"}},
		{name: "control: f32 Sqrt(4)", got: f32(4).Select(c("x").Sqrt()), want: f32(2)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wrong := castWrong(t, tc)
			why, known := knownCastDefects[tc.name]
			switch {
			case known && wrong == "":
				t.Errorf("answers correctly now; delete it from knownCastDefects (%s)", why)
			case known:
				t.Logf("known defect (%s): %s", why, wrong)
			case wrong != "":
				t.Error(wrong)
			}
		})
	}
	for name := range knownCastDefects {
		if !slices.ContainsFunc(cases, func(c castCase) bool { return c.name == name }) {
			t.Errorf("knownCastDefects names %q, which is not a case", name)
		}
	}
}

// castWrong says how tc's answer differs from its hand answer, or "".
func castWrong(t *testing.T, tc castCase) string {
	df, err := tc.got.Collect(t.Context())
	if tc.want == nil {
		switch {
		case err == nil:
			return fmt.Sprintf("answered %s; want a refusal", strings.Join(strings.Fields(df.String()), " "))
		case errors.Is(err, ursus.ErrInternal):
			return "ErrInternal: " + err.Error()
		case !errors.Is(err, ursus.ErrValue):
			return fmt.Sprintf("a %s error, want a value error: %v", kindOf(err), err)
		}
		for _, w := range tc.refuse {
			if !strings.Contains(err.Error(), w) {
				return fmt.Sprintf("the refusal does not say %q: %v", w, err)
			}
		}
		if tc.notSay != "" && strings.Contains(err.Error(), tc.notSay) {
			return fmt.Sprintf("the refusal says %q: %v", tc.notSay, err)
		}
		return ""
	}
	if err != nil {
		if errors.Is(err, ursus.ErrInternal) {
			return "ErrInternal: " + err.Error()
		}
		return err.Error()
	}
	want, err := tc.want.Collect(t.Context())
	if err != nil {
		t.Fatalf("the hand answer: %v", err)
	}
	return framesDiffer(t, df, want)
}

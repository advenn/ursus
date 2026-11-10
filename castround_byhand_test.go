package ursus_test

// A float64 is not a go-between: audit.md §6, S1, S2 and S4.
//
// One helper decided every conversion that touched a float — t := T(v), and a value
// was lossy if float64(t) != v — and it was wrong in three different ways: a STRING
// was parsed into a float64 first, so "9007199254740993" became …992; the RESULT of
// a Float32 computation, which is meant to round, was nulled; and a CAST to a float
// refused, or nulled, the rounding that every float is. The rule since: a cast to a
// float rounds once to the nearest value of that width, and only a finite value whose
// nearest float is an infinity is unrepresentable — refused by Cast, null under
// CastLossy. A string parses at the width it is cast to.
//
// The answers are Polars 2.1.0's, recorded in the castround_byhand group of
// testdata/oracle and spelled in oracle_castround_byhand_test.go since step 497,
// which took this file's hand-typed values. What stays asserts the refusals and their
// wording, and "-0" read as a Uint8's 0, which Polars' unsigned parse refuses, held to
// the same text read through Int64.

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

type castCase struct {
	name string
	got  *ursus.LazyFrame
	want *ursus.LazyFrame // the same question another way; nil for a refusal
	// refuse lists what a refusal must name. It must be ErrValue, never
	// ErrInternal, and must not say notSay.
	refuse []string
	notSay string
}

func TestCastsAndParsesByHand(t *testing.T) {
	c := ursus.Col
	str := func(v ...string) *ursus.LazyFrame { return ursus.Frame(ursus.Values("x", v)) }
	f64 := func(v ...float64) *ursus.LazyFrame { return ursus.Frame(ursus.Values("x", v)) }
	cast := func(lf *ursus.LazyFrame, to ursus.DataType) *ursus.LazyFrame {
		return lf.Select(c("x").Cast(to))
	}

	cases := []castCase{
		// --- a string a type cannot hold is refused, naming the value and the type, and
		// not as "cannot parse", which it did ---
		{name: `"256" → Uint8`, got: cast(str("256"), ursus.Uint8),
			refuse: []string{`"256"`, "row 0", "Uint8"}, notSay: "cannot parse"},
		{name: `"256" → Uint8 beside a null`,
			got:    cast(ursus.Frame(ursus.ValuesNullable("x", []string{"256", ""}, []bool{true, false})), ursus.Uint8),
			refuse: []string{`"256"`, "Uint8"}, notSay: "cannot parse"},
		{name: `"-1" → Uint8`, got: cast(str("-1"), ursus.Uint8),
			refuse: []string{`"-1"`, "Uint8"}, notSay: "cannot parse"},
		{name: `"3.4e39" → Float32`, got: cast(str("3.4e39"), ursus.Float32),
			refuse: []string{`"3.4e39"`, "Float32"}, notSay: "cannot parse"},
		{name: `"1e400" → Float64`, got: cast(str("1e400"), ursus.Float64),
			refuse: []string{`"1e400"`, "Float64"}, notSay: "cannot parse"},
		{name: `"abc" → Int64`, got: cast(str("abc"), ursus.Int64), refuse: []string{"cannot parse"}},
		{name: `"1e3" → Int64`, got: cast(str("1e3"), ursus.Int64), refuse: []string{"cannot parse"}},

		// --- a finite float whose nearest float is an infinity is refused; Polars' cast
		// overflows to the infinity (the lossy null is the recorded difference) ---
		{name: "f64 1e39 → Float32", got: cast(f64(1e39), ursus.Float32),
			refuse: []string{"not representable", "Float32"}},
		{name: "f64 2^63 → Int64", got: cast(f64(0x1p63), ursus.Int64),
			refuse: []string{"not representable", "Int64"}},

		// --- "-0" is a Uint8's 0, as the same text read as an Int64 is ---
		{name: `"-0" and "+5" → Uint8, as through Int64`, got: cast(str("-0", "+5"), ursus.Uint8),
			want: str("-0", "+5").Select(c("x").Cast(ursus.Int64).Cast(ursus.Uint8))},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if wrong := castWrong(t, tc); wrong != "" {
				t.Error(wrong)
			}
		})
	}
}

// castWrong says how tc's answer differs from its refusal or its other spelling, or "".
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
		t.Fatalf("the other spelling: %v", err)
	}
	return framesDiffer(t, df, want)
}

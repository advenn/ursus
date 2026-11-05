package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestCastsAsPolars is step 325: Polars' str.to_time, str.to_decimal and
// dt.cast_time_unit, and a float cast to text as Polars writes it. The answers are
// Polars 2.1.0's, recorded in the castmore group of testdata/oracle and spelled in
// oracle_castmore_test.go since step 497, which took this file's pasted values. What
// stays asserts a strict parse's refusal, two result types and a Date's refusal.
func TestCastsAsPolars(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col

	t.Run("to_time", func(t *testing.T) {
		text := ursus.Frame(ursus.ValuesNullable("t", []string{"10:30:00", "bad", ""}, []bool{true, true, false}))
		if _, err := text.Select(c("t").Str().ToTime("%H:%M:%S", true)).Collect(ctx); err == nil ||
			!strings.Contains(err.Error(), "bad") {
			t.Errorf("a strict parse of %q: %v", "bad", err)
		}
	})

	t.Run("to_decimal", func(t *testing.T) {
		lf := ursus.Frame(ursus.Values("t", []string{"1.25", "3.5"}))
		s, err := lf.Select(c("t").Str().ToDecimal(2)).CollectSchema(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if dt := s.Field(0).Type; dt != ursus.Decimal(38, 2) {
			t.Errorf("to_decimal(scale=2) is %s, want Decimal(38, 2)", dt)
		}
	})

	t.Run("cast_time_unit", func(t *testing.T) {
		ticks := ursus.Frame(ursus.Values("v", []int64{-1999, 1999, -1000, -1}))
		s, err := ticks.Select(c("v").Cast(ursus.Datetime(ursus.Micro, "")).Dt().ReplaceTimeZone("Asia/Tashkent").
			Dt().CastTimeUnit(ursus.Milli)).CollectSchema(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if dt := s.Field(0).Type; dt != ursus.Datetime(ursus.Milli, "Asia/Tashkent") {
			t.Errorf("a zoned instant in ms is %s", dt)
		}
		if _, err := ticks.Select(c("v").Cast(ursus.Int32).Cast(ursus.Date).Dt().CastTimeUnit(ursus.Milli)).Collect(ctx); err == nil {
			t.Error("a Date cast to a time unit")
		}
	})
}

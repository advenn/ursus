package ursus_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestCastsAsPolars is step 325: Polars' str.to_time, str.to_decimal and
// dt.cast_time_unit, and a float cast to text as Polars writes it. Every answer is
// Polars 1.44.1's.
func TestCastsAsPolars(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	check := func(t *testing.T, lf, want *ursus.LazyFrame) {
		t.Helper()
		got, err := lf.Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		w, err := want.Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, w)
	}
	clock := func(h, m, s, ns int) int64 {
		return int64(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute +
			time.Duration(s)*time.Second + time.Duration(ns))
	}
	times := func(name string, ok []bool, ticks ...int64) *ursus.LazyFrame {
		return ursus.Frame(ursus.ValuesNullable(name, ticks, ok)).Select(c(name).Cast(ursus.TimeOf(ursus.Nano)))
	}

	t.Run("to_time", func(t *testing.T) {
		text := ursus.Frame(ursus.ValuesNullable("t", []string{"10:30:00", "bad", ""}, []bool{true, true, false}))
		check(t, text.Select(c("t").Str().ToTime("%H:%M:%S", false)),
			times("t", []bool{true, false, false}, clock(10, 30, 0, 0), 0, 0))
		check(t, ursus.Frame(ursus.Values("t", []string{"10h30"})).Select(c("t").Str().ToTime("%Hh%M", true)),
			times("t", []bool{true}, clock(10, 30, 0, 0)))
		// No format: each value as a cast reads it.
		check(t, ursus.Frame(ursus.Values("t", []string{"10:30:00.5"})).Select(c("t").Str().ToTime("", true)),
			times("t", []bool{true}, clock(10, 30, 0, 500_000_000)))
		if _, err := text.Select(c("t").Str().ToTime("%H:%M:%S", true)).Collect(ctx); err == nil ||
			!strings.Contains(err.Error(), "bad") {
			t.Errorf("a strict parse of %q: %v", "bad", err)
		}
	})

	t.Run("to_decimal", func(t *testing.T) {
		lf := ursus.Frame(ursus.ValuesNullable("t", []string{"1.25", "3.5", "x", "", "1.255", "-1.255", "1e3"},
			[]bool{true, true, true, false, true, true, true}))
		got, err := lf.Select(c("t").Str().ToDecimal(2).Cast(ursus.String)).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		want, err := ursus.Frame(ursus.ValuesNullable("t", []string{"1.25", "3.50", "", "", "1.26", "-1.26", "1000.00"},
			[]bool{true, true, false, false, true, true, true})).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, want)
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
		check(t, ticks.Select(c("v").Cast(ursus.Datetime(ursus.Micro, "")).Dt().CastTimeUnit(ursus.Milli).Cast(ursus.Int64).Alias("dt"),
			c("v").Cast(ursus.Duration(ursus.Micro)).Dt().CastTimeUnit(ursus.Milli).Cast(ursus.Int64).Alias("du"),
			c("v").Cast(ursus.Datetime(ursus.Micro, "")).Dt().CastTimeUnit(ursus.Nano).Cast(ursus.Int64).Alias("up")),
			ursus.Frame(ursus.Values("dt", []int64{-2, 1, -1, -1}), ursus.Values("du", []int64{-1, 1, -1, 0}),
				ursus.Values("up", []int64{-1_999_000, 1_999_000, -1_000_000, -1000})))
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

	t.Run("a float as text", func(t *testing.T) {
		f64 := []float64{1, math.Copysign(0, -1), 0, math.NaN(), math.Inf(1), math.Inf(-1), 0.1, 1.5, 100, 1234.5,
			123456789, 1e15, 1e16, 1e17, 1.5e16, 1e20, 1e21, 1.2345e21, 1e100, 1e-4, 1e-5, 1e-6, 1e-7, 1.5e-7,
			0.000123, 5e-324, math.MaxFloat64, 1 << 53, 0.30000000000000004, -2.5e-10}
		want := []string{"1.0", "-0.0", "0.0", "NaN", "inf", "-inf", "0.1", "1.5", "100.0", "1234.5", "123456789.0",
			"1000000000000000.0", "1e+16", "1e+17", "1.5e+16", "1e+20", "1e+21", "1.2345e+21", "1e+100", "0.0001",
			"0.00001", "1e-6", "1e-7", "1.5e-7", "0.000123", "5e-324", "1.7976931348623157e+308",
			"9007199254740992.0", "0.30000000000000004", "-2.5e-10"}
		check(t, ursus.Frame(ursus.Values("f", f64)).Select(c("f").Cast(ursus.String)),
			ursus.Frame(ursus.Values("f", want)))
		f32 := []float32{1, 0.1, 16777217, 3.4e38, 1e-45, 1e20, 1e-7, 123456.7, 1.5e-5}
		want32 := []string{"1.0", "0.1", "16777216.0", "3.4e+38", "1e-45", "1e+20", "1e-7", "123456.7", "0.000015"}
		check(t, ursus.Frame(ursus.Values("f", f32)).Select(c("f").Cast(ursus.String)),
			ursus.Frame(ursus.Values("f", want32)))
	})
}

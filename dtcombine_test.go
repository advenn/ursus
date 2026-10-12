package ursus_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/ursustest"
)

// TestDaysInMonthAndCombineAnswerAsPolars: dt.days_in_month and dt.combine (step
// 230), against Polars 1.44.1: February in a leap year and not, a null, a Date and a
// Datetime; a Time of day joined to each, a null on either side null; a Datetime
// keeping its millisecond unit; in a zone, the day read on its wall clock and a
// wall clock the zone skips refused.
func TestDaysInMonthAndCombineAnswerAsPolars(t *testing.T) {
	ctx := t.Context()
	day := func(s string) time.Time {
		tm, err := time.Parse("2006-01-02 15:04:05.000", s)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	ok := []bool{true, true, true, false}
	tod := []int64{(3*3600+4*60+5)*1e9 + 6e6, 0, 0, 3600e9}
	frame := ursus.Frame(
		ursus.ValuesNullable("ts", []time.Time{day("2024-02-10 23:00:00.000"), day("2023-02-01 00:00:00.000"),
			day("2024-12-31 12:00:00.000"), {}}, ok),
		ursus.ValuesNullable("tn", tod, []bool{true, true, false, true}),
	).WithColumns(ursus.Col("ts").Dt().ReplaceTimeZone("").Cast(dtype.Datetime(dtype.Micro, "")).Alias("ts")).
		WithColumns(ursus.Col("ts").Cast(ursus.Date).Alias("d"),
			ursus.Col("tn").Cast(dtype.Time(dtype.Nano)).Alias("t"),
			ursus.Col("ts").Cast(dtype.Datetime(dtype.Milli, "")).Alias("ms"))
	c := ursus.Col
	got, err := frame.Select(
		c("d").Dt().DaysInMonth().Alias("dim"), c("ts").Dt().DaysInMonth().Alias("dimts"),
		c("d").Dt().Combine(c("t")).Alias("c"), c("ts").Dt().Combine(c("t")).Alias("cts"),
		c("ms").Dt().Combine(c("t")).Alias("cms"),
	).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	combined := []time.Time{day("2024-02-10 03:04:05.006"), day("2023-02-01 00:00:00.000"), {}, {}}
	cok := []bool{true, true, false, false}
	want, err := ursus.Frame(
		ursus.ValuesNullable("dim", []int8{29, 28, 31, 0}, ok),
		ursus.ValuesNullable("dimts", []int8{29, 28, 31, 0}, ok),
		ursus.ValuesNullable("c", combined, cok),
	).WithColumns(ursus.Col("c").Dt().ReplaceTimeZone("").Cast(dtype.Datetime(dtype.Micro, "")).Alias("c")).
		WithColumns(ursus.Col("c").Alias("cts"), ursus.Col("c").Cast(dtype.Datetime(dtype.Milli, "")).Alias("cms")).
		Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, want)

	// In New York: 22:00 on 2024-03-11, 02:00 on the 12th in UTC, with 13:15 is 13:15
	// on the 11th, its day on New York's wall clock; 2024-03-10 with 02:30 is a wall
	// clock the clocks skipped.
	ny := dtype.Datetime(dtype.Nano, "America/New_York")
	zoned := func(s string) *ursus.LazyFrame {
		return ursus.Frame(ursus.Values("ts", []time.Time{day(s)})).
			WithColumns(c("ts").Dt().ReplaceTimeZone("America/New_York").Alias("ts"))
	}
	at := func(h, m int64) ursus.Expr {
		return ursus.Lit((h*3600 + m*60) * 1e9).Cast(dtype.Time(dtype.Nano))
	}
	gotZ, err := zoned("2024-03-11 22:00:00.000").Select(c("ts").Dt().Combine(at(13, 15))).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantZ, err := zoned("2024-03-11 13:15:00.000").Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if gotZ.Schema().FieldSlice()[0].Type != ny {
		t.Errorf("a New York Datetime combines to %s, want %s", gotZ.Schema().FieldSlice()[0].Type, ny)
	}
	ursustest.AssertFrameEqual(t, gotZ, wantZ)
	// February's 29 days in New York, at 21:00 on the 29th there, March 1 in UTC.
	dim, err := zoned("2024-02-29 21:00:00.000").Select(c("ts").Dt().DaysInMonth()).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v, _, err := dim.At[int8](0, "ts"); err != nil || v != 29 {
		t.Errorf("February 29 at 21:00 in New York has %d days in its month, want 29: %v", v, err)
	}
	_, err = zoned("2024-03-10 12:00:00.000").Select(c("ts").Dt().Combine(at(2, 30))).Collect(ctx)
	if !errors.Is(err, uerr.ErrValue) || !strings.Contains(err.Error(), "02:30") {
		t.Errorf("a skipped wall clock should be refused, naming it: %v", err)
	}

	// 23:59 on 2262-04-11 is past Datetime(ns)'s last instant, 23:47:16.854775807.
	late := ursus.Frame(ursus.Values("ts", []time.Time{day("2262-04-11 00:00:00.000")}))
	_, err = late.Select(c("ts").Dt().Combine(at(23, 59))).Collect(ctx)
	if !errors.Is(err, uerr.ErrValue) || !strings.Contains(err.Error(), "outside the range") {
		t.Errorf("a day and a time past Datetime(ns)'s range should be refused: %v", err)
	}

	if _, err := frame.Select(c("t").Dt().Combine(c("t"))).Collect(ctx); !errors.Is(err, uerr.ErrType) {
		t.Errorf("a Time has no day to combine: %v", err)
	}
	if _, err := frame.Select(c("d").Dt().Combine(c("tn"))).Collect(ctx); !errors.Is(err, uerr.ErrType) {
		t.Errorf("an Int64 is no Time of day: %v", err)
	}
}

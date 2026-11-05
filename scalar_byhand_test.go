package ursus_test

// Scalar functions: audit.md §6, S5–S13. Each case was a silent wrong answer or a
// crash the audit measured. Their answers are Polars 2.1.0's, recorded in the
// scalar_byhand group of testdata/oracle and spelled in oracle_scalar_byhand_test.go
// since step 497, which took this file's hand-typed values. What stays asserts the
// refusals (IsIn answers what Eq answers, so a value of another type is refused; a
// truncated instant outside the column's range is refused where Polars wraps it) and
// what Polars 2.1 has no spelling for: Truncate of a Go time.Duration floors the UTC
// instant, where Every("1h") and Polars' "1h" floor the wall clock, held to Go's own
// tick arithmetic; and a Duration's FloorDiv by an integer, which Polars 2.1 refuses,
// held to the same division through Int64.

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

type scalarCase struct {
	name   string
	got    *ursus.LazyFrame
	want   *ursus.LazyFrame              // the same question another way, or
	check  func(*ursus.DataFrame) string // a property of the answer, or nil with
	refuse error                         // a refusal of this kind, naming
	naming []string                      // these
}

// instants is one column ts of type Datetime(unit, zone) holding the given UTC
// instants. tsFrame parses wall clocks, which cannot name the second 01:30 of a
// fall-back night; an instant can.
func instants(t *testing.T, zone string, unit dtype.TimeUnit, utc ...string) *ursus.LazyFrame {
	t.Helper()
	if zone != "" && zone != "UTC" {
		if _, err := time.LoadLocation(zone); err != nil {
			t.Skipf("no tzdata for %s: %v", zone, err)
		}
	}
	dt := dtype.Datetime(unit, zone)
	ticks := make([]int64, len(utc))
	for i, s := range utc {
		tm, err := time.Parse("2006-01-02T15:04:05.999999999", s)
		if err != nil {
			t.Fatal(err)
		}
		tick, ok := dt.FromTime(tm)
		if !ok {
			t.Fatalf("cannot store %s as %s", s, dt)
		}
		ticks[i] = tick
	}
	return ursus.Frame(data.NewFixed("ts", dt, ticks, bitmap.AllSet(len(utc))))
}

// hourFloor is ticks floored to a whole hour of perHour ticks, Go's own arithmetic:
// what Truncate(time.Hour) answers on the UTC instant, whatever the zone.
func hourFloor(ticks, perHour int64) int64 {
	q, r := ticks/perHour, ticks%perHour
	if r < 0 {
		q--
	}
	return q * perHour
}

// flooredToTheHour reads an instant's ticks and Truncate(time.Hour)'s ticks side by
// side and checks each row against hourFloor.
func flooredToTheHour(perHour int64) func(*ursus.DataFrame) string {
	return func(df *ursus.DataFrame) string {
		for i := range df.Height() {
			tick, _, _ := df.At[int64](i, "t")
			got, ok, _ := df.At[int64](i, "h")
			if want := hourFloor(tick, perHour); !ok || got != want {
				return fmt.Sprintf("row %d: Truncate(time.Hour) of %d = %d (present %v), want %d", i, tick, got, ok, want)
			}
		}
		return ""
	}
}

func TestScalarFunctionsByHand(t *testing.T) {
	c := ursus.Col
	ts := c("ts")
	bools := func(v ...bool) *ursus.LazyFrame { return ursus.Frame(ursus.Values("v", v)) }
	i64 := func(name string, v ...int64) *ursus.LazyFrame { return ursus.Frame(ursus.Values(name, v)) }
	strs := func(v ...string) *ursus.LazyFrame { return ursus.Frame(ursus.Values("s", v)) }
	list := func(v *ursus.Column) *ursus.LazyFrame {
		return ursus.Frame(ursus.Values("g", []int64{1, 1}), v).
			GroupBy(c("g")).Agg(c("v").Implode()).Select(c("v"))
	}
	nsTicks := func(v ...int64) *ursus.LazyFrame {
		return i64("ts", v...).Select(ts.Cast(ursus.Datetime(ursus.Nano, "")))
	}
	hour := func(lf *ursus.LazyFrame) *ursus.LazyFrame {
		return lf.Select(ts.Cast(ursus.Int64).Alias("t"), ts.Dt().Truncate(time.Hour).Cast(ursus.Int64).Alias("h"))
	}
	const nsMin = math.MinInt64

	cases := []scalarCase{
		// --- S6: IsIn answers what Eq answers, so a value of another type is refused ---
		{name: "S6 Int64 IsIn(\"1\")",
			got:    i64("v", 1, 2).Select(c("v").IsIn("1")),
			refuse: ursus.ErrType, naming: []string{"is_in", "Int64", "String"}},
		{name: "S6 Bool IsIn(1)",
			got:    bools(true).Select(c("v").IsIn(1)),
			refuse: ursus.ErrType, naming: []string{"is_in"}},
		{name: "S6 String IsIn(1)",
			got:    strs("1").Select(c("s").IsIn(1)),
			refuse: ursus.ErrType, naming: []string{"is_in"}},
		{name: "S6 Date IsIn(2024-01-01T13:00)",
			got: instants(t, "", dtype.Micro, "2024-01-01T00:00:00").Select(ts.Cast(ursus.Date)).
				Select(ts.IsIn(time.Date(2024, 1, 1, 13, 0, 0, 0, time.UTC))),
			refuse: ursus.ErrType, naming: []string{"is_in"}},
		{name: "S6 List(Int64) Contains(\"1\")",
			got:    list(ursus.Values("v", []int64{1, 2})).Select(c("v").List().Contains("1")),
			refuse: ursus.ErrType, naming: []string{"list.contains"}},

		// --- S8: a Duration's FloorDiv floors, as the Int64 division does; Polars 2.1
		// refuses "// on duration and dyn int" ---
		{name: "S8 Duration -7s FloorDiv 2 is the Int64 floor division",
			got:  i64("a", -7, 7, -8).Select(c("a").Cast(ursus.Duration(ursus.Second)).FloorDiv(2)),
			want: i64("a", -7, 7, -8).Select(c("a").FloorDiv(2).Cast(ursus.Duration(ursus.Second)))},

		// --- S10: a Go time.Duration floors the UTC instant, where Every("1h") floors
		// the column's wall clock (the recorded group, Kolkata's 04:30Z) ---
		{name: "control: Kolkata Truncate(time.Hour) floors the instant",
			got:   hour(instants(t, "Asia/Kolkata", dtype.Micro, "2024-01-01T05:17:00", "2024-01-01T00:00:00", "1969-12-31T23:30:00")),
			check: flooredToTheHour(3_600_000_000)},

		// --- S11: a truncated instant the column cannot hold is refused ---
		{name: "S11 Truncate(Every(1mo)) near the ns minimum",
			got:    nsTicks(nsMin + 3600e9).Select(ts.Dt().Truncate(ursus.Every("1mo"))),
			refuse: ursus.ErrValue, naming: []string{"truncate"}},
		{name: "S11 Truncate(time.Hour) at the ns minimum",
			got:    nsTicks(nsMin + 5).Select(ts.Dt().Truncate(time.Hour)),
			refuse: ursus.ErrValue, naming: []string{"truncate"}},
		{name: "S11 GroupByDynamic(Every(1mo)) near the ns minimum",
			got: nsTicks(nsMin+3600e9).
				GroupByDynamic(ts, ursus.DynamicOptions{Every: ursus.Every("1mo")}).Agg(ursus.Len()),
			refuse: ursus.ErrValue, naming: []string{"window bound"}},
		{name: "control: Truncate(time.Hour) an hour above the ns minimum",
			got:   hour(nsTicks(nsMin + 3600e9)),
			check: flooredToTheHour(3_600_000_000_000)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if wrong := scalarWrong(t, tc); wrong != "" {
				t.Error(wrong)
			}
		})
	}
}

// TestIsInRefusesAtPlanTime: the refusal is the resolver's, so CollectSchema and
// Explain say it before any data is read, as they do for Eq.
func TestIsInRefusesAtPlanTime(t *testing.T) {
	lf := ursus.Frame(ursus.Values("v", []int64{1})).Select(ursus.Col("v").IsIn("1"))
	if _, err := lf.CollectSchema(t.Context()); !errors.Is(err, ursus.ErrType) {
		t.Errorf("CollectSchema of Int64 IsIn(\"1\"): %v, want a type error", err)
	}
	if _, err := lf.Explain(t.Context()); !errors.Is(err, ursus.ErrType) {
		t.Errorf("Explain of Int64 IsIn(\"1\"): %v, want a type error", err)
	}
}

func scalarWrong(t *testing.T, tc scalarCase) string {
	df, err := tc.got.Collect(t.Context())
	if tc.refuse != nil {
		switch {
		case err == nil:
			return fmt.Sprintf("answered %s; want a refusal", strings.Join(strings.Fields(df.String()), " "))
		case !errors.Is(err, tc.refuse):
			return fmt.Sprintf("a %s error, want %v: %v", kindOf(err), tc.refuse, err)
		}
		for _, w := range tc.naming {
			if !strings.Contains(err.Error(), w) {
				return fmt.Sprintf("the refusal does not say %q: %v", w, err)
			}
		}
		return ""
	}
	if err != nil {
		if errors.Is(err, ursus.ErrInternal) {
			return "ErrInternal: " + err.Error()
		}
		return err.Error()
	}
	if tc.check != nil {
		return tc.check(df)
	}
	want, err := tc.want.Collect(t.Context())
	if err != nil {
		t.Fatalf("the other spelling: %v", err)
	}
	return framesDiffer(t, df, want)
}

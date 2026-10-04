package ursus_test

// Scalar functions, against answers worked out by hand: audit.md §6, S5–S13.
//
// Each case is a silent wrong answer or a crash the audit measured, answered under
// the rule this step chose for it. Most are the answer Polars gives. Where Polars
// differs, the case says so: IsIn answers what Eq answers, and a truncated instant
// outside the column's range is refused where Polars wraps it.

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// knownScalarDefects names each case that answers wrongly today, with what it answers.
var knownScalarDefects = map[string]string{
	"S6 Int64 IsIn(\"1\")":                         "matched",
	"S6 Bool IsIn(1)":                              "matched",
	"S6 String IsIn(1)":                            "matched",
	"S6 Date IsIn(2024-01-01T13:00)":               "matched",
	"S6 Int8 IsIn(5000)":                           "refused: not representable as Int8",
	"S6 Uint64 IsIn(-1)":                           "refused: not representable as Uint64",
	"S6 Float32 IsIn(0.1)":                         "matched the float32 nearest 0.1",
	"S6 Datetime(s) IsIn(00:00:01.5)":              "matched",
	"S6 List(Int8) Contains(5000)":                 "refused: not representable as Int8",
	"S6 List(Int64) Contains(\"1\")":               "matched",
	"S7 Replace (a)(b) with $2$1":                  "$2$1 ab",
	"S7 Replace (a)(b) with ${1}x":                 "${1}x ab",
	"S8 Int64 FloorDiv and Mod by a column":        "truncated",
	"S8 Int64 FloorDiv and Mod by a constant":      "truncated",
	"S8 Float64 Mod":                               "truncated",
	"S8 Duration -7s FloorDiv 2":                   "-3s",
	"S9 SplitN with n = 0":                         "[]",
	"S10 Kolkata Truncate(Every(1h))":              "10:30, the UTC grid",
	"S10 New York Truncate(Every(2h))":             "03:00, the UTC grid",
	"S10 New York spring-forward Truncate(2h)":     "01:00 EST, the UTC grid",
	"S10 Kolkata GroupByDynamic(Every(1h))":        "one window at 10:30",
	"S11 Truncate(Every(1mo)) near the ns minimum": "ErrInternal",
	"S11 Truncate(time.Hour) at the ns minimum":    "wrapped to 2262",
	"S12 CountMatches of an empty literal":         "null",
	"S13 StripCharsStart and End of Unicode space": "only ASCII stripped",
}

type scalarCase struct {
	name   string
	got    *ursus.LazyFrame
	want   *ursus.LazyFrame              // the hand answer, or
	check  func(*ursus.DataFrame) string // a property of it, or nil with
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

func TestScalarFunctionsByHand(t *testing.T) {
	c := ursus.Col
	bools := func(v ...bool) *ursus.LazyFrame { return ursus.Frame(ursus.Values("v", v)) }
	i64 := func(name string, v ...int64) *ursus.LazyFrame { return ursus.Frame(ursus.Values(name, v)) }
	f64 := func(name string, v ...float64) *ursus.LazyFrame { return ursus.Frame(ursus.Values(name, v)) }
	strs := func(v ...string) *ursus.LazyFrame { return ursus.Frame(ursus.Values("s", v)) }
	list := func(v *ursus.Column) *ursus.LazyFrame {
		return ursus.Frame(ursus.Values("g", []int64{1, 1}), v).
			GroupBy(c("g")).Agg(c("v").Implode()).Select(c("v"))
	}
	nsTicks := func(v ...int64) *ursus.LazyFrame {
		return i64("ts", v...).Select(c("ts").Cast(ursus.Datetime(ursus.Nano, "")))
	}
	const nsMin = math.MinInt64

	cases := []scalarCase{
		// --- S5: Epoch floors to the second an instant lies in ---
		{name: "S5 Epoch of 1969-12-31T23:59:59.5",
			got:  instants(t, "", dtype.Micro, "1969-12-31T23:59:59.5").Select(c("ts").Dt().Epoch()),
			want: i64("ts", -1)},
		{name: "S5 Epoch of -1ns",
			got:  nsTicks(-1).Select(c("ts").Dt().Epoch()),
			want: i64("ts", -1)},
		{name: "control: Epoch of 1969-12-31T23:59:59",
			got:  instants(t, "", dtype.Micro, "1969-12-31T23:59:59").Select(c("ts").Dt().Epoch()),
			want: i64("ts", -1)},

		// --- S6: IsIn answers what Eq answers ---
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
			got: instants(t, "", dtype.Micro, "2024-01-01T00:00:00").Select(c("ts").Cast(ursus.Date)).
				Select(c("ts").IsIn(time.Date(2024, 1, 1, 13, 0, 0, 0, time.UTC))),
			refuse: ursus.ErrType, naming: []string{"is_in"}},
		{name: "S6 Int8 IsIn(5000)",
			got:  ursus.Frame(ursus.Values("v", []int8{1, 2})).Select(c("v").IsIn(5000)),
			want: bools(false, false)},
		{name: "S6 Uint64 IsIn(-1)",
			got:  ursus.Frame(ursus.Values("v", []uint64{math.MaxUint64})).Select(c("v").IsIn(-1)),
			want: bools(false)},
		{name: "S6 Float32 IsIn(0.1)",
			got:  ursus.Frame(ursus.Values("v", []float32{0.1})).Select(c("v").IsIn(0.1)),
			want: bools(false)},
		{name: "S6 Datetime(s) IsIn(00:00:01.5)",
			got: instants(t, "UTC", dtype.Second, "2024-01-01T00:00:01").
				Select(c("ts").IsIn(time.Date(2024, 1, 1, 0, 0, 1, 500_000_000, time.UTC)).Alias("v")),
			want: bools(false)},
		{name: "S6 List(Int8) Contains(5000)",
			got:  list(ursus.Values("v", []int8{1, 2})).Select(c("v").List().Contains(5000)),
			want: bools(false)},
		{name: "S6 List(Int64) Contains(\"1\")",
			got:    list(ursus.Values("v", []int64{1, 2})).Select(c("v").List().Contains("1")),
			refuse: ursus.ErrType, naming: []string{"list.contains"}},
		{name: "control: Int32 IsIn(1, 3)",
			got:  ursus.Frame(ursus.Values("v", []int32{1, 2, 3})).Select(c("v").IsIn(1, 3)),
			want: bools(true, false, true)},
		{name: "control: Float32 IsIn(float32(0.1))",
			got:  ursus.Frame(ursus.Values("v", []float32{0.1})).Select(c("v").IsIn(float32(0.1))),
			want: bools(true)},
		{name: "control: NaN IsIn(NaN), grouping equality",
			got:  f64("v", math.NaN(), 0).Select(c("v").IsIn(math.NaN())),
			want: bools(true, false)},
		{name: "control: List(Int8) Contains(2)",
			got:  list(ursus.Values("v", []int8{1, 2})).Select(c("v").List().Contains(2)),
			want: bools(true)},

		// --- S7: a first-match regex Replace expands its groups ---
		{name: "S7 Replace (a)(b) with $2$1",
			got:  strs("ab ab").Select(c("s").Str().Replace(`(a)(b)`, "$2$1", false)),
			want: strs("ba ab")},
		{name: "S7 Replace (a)(b) with ${1}x",
			got:  strs("ab ab").Select(c("s").Str().Replace(`(a)(b)`, "${1}x", false)),
			want: strs("ax ab")},
		{name: "control: a literal Replace with $1",
			got:  strs("ab ab").Select(c("s").Str().Replace("ab", "$1", true)),
			want: strs("$1 ab")},

		// --- S8: FloorDiv floors and Mod is its remainder ---
		{name: "S8 Int64 FloorDiv and Mod by a column",
			got: ursus.Frame(ursus.Values("a", []int64{-7, -7, 7, 7}), ursus.Values("b", []int64{2, -2, 2, -2})).
				Select(c("a").FloorDiv(c("b")).Alias("d"), c("a").Mod(c("b")).Alias("m")),
			want: ursus.Frame(ursus.Values("d", []int64{-4, 3, 3, -4}), ursus.Values("m", []int64{1, -1, 1, -1}))},
		{name: "S8 Int64 FloorDiv and Mod by a constant",
			got:  i64("a", -7, 7).Select(c("a").FloorDiv(2).Alias("d"), c("a").Mod(-2).Alias("m")),
			want: ursus.Frame(ursus.Values("d", []int64{-4, 3}), ursus.Values("m", []int64{-1, -1}))},
		{name: "S8 Float64 Mod",
			got: ursus.Frame(ursus.Values("a", []float64{-7, 7, -7.5}), ursus.Values("b", []float64{2, -2, 2})).
				Select(c("a").Mod(c("b")).Alias("m")),
			want: f64("m", 1, -1, 0.5)},
		{name: "S8 Duration -7s FloorDiv 2",
			got:  i64("a", -7).Select(c("a").Cast(ursus.Duration(ursus.Second)).FloorDiv(2)),
			want: i64("a", -4).Select(c("a").Cast(ursus.Duration(ursus.Second)))},
		{name: "control: Uint8 7 FloorDiv 2",
			got:  ursus.Frame(ursus.Values("a", []uint8{7})).Select(c("a").FloorDiv(uint8(2))),
			want: ursus.Frame(ursus.Values("a", []uint8{3}))},
		{name: "control: FloorDiv and Mod by zero are null",
			got: ursus.Frame(ursus.Values("a", []int64{-7}), ursus.Values("b", []int64{0})).
				Select(c("a").FloorDiv(c("b")).Alias("d"), c("a").Mod(c("b")).Alias("m")),
			want: ursus.Frame(ursus.ValuesNullable("d", []int64{0}, []bool{false}),
				ursus.ValuesNullable("m", []int64{0}, []bool{false}))},

		// --- S9: SplitN with n <= 0 is unlimited ---
		{name: "S9 SplitN with n = 0",
			got:  strs("a b c").Select(c("s").Str().SplitN(" ", 0).List().Len()),
			want: ursus.Frame(ursus.Values("s", []uint32{3}))},
		{name: "control: SplitN with n = -1",
			got:  strs("a b c").Select(c("s").Str().SplitN(" ", -1).List().Len()),
			want: ursus.Frame(ursus.Values("s", []uint32{3}))},

		// --- S10: an Interval floors on the column's wall clock ---
		// Kolkata is +05:30: 10:47 local is 05:17Z, the local hour starts at 04:30Z,
		// and the UTC hour at 05:00Z, which is 10:30 local.
		{name: "S10 Kolkata Truncate(Every(1h))",
			got:  instants(t, "Asia/Kolkata", dtype.Micro, "2024-01-01T05:17:00").Select(c("ts").Dt().Truncate(ursus.Every("1h"))),
			want: instants(t, "Asia/Kolkata", dtype.Micro, "2024-01-01T04:30:00")},
		{name: "control: Kolkata Truncate(time.Hour) floors the instant",
			got:  instants(t, "Asia/Kolkata", dtype.Micro, "2024-01-01T05:17:00").Select(c("ts").Dt().Truncate(time.Hour)),
			want: instants(t, "Asia/Kolkata", dtype.Micro, "2024-01-01T05:00:00")},
		// 03:30 EST is 08:30Z; the local two-hour grid has 02:00 EST, 07:00Z.
		{name: "S10 New York Truncate(Every(2h))",
			got:  instants(t, "America/New_York", dtype.Micro, "2024-01-01T08:30:00").Select(c("ts").Dt().Truncate(ursus.Every("2h"))),
			want: instants(t, "America/New_York", dtype.Micro, "2024-01-01T07:00:00")},
		// 2024-11-03: 01:30 EDT is 05:30Z, and 01:30 EST is 06:30Z. Each floors to
		// 01:00 in its own offset, as Polars keeps the fold.
		{name: "control: New York fall-back, both 01:30s, Truncate(Every(1h))",
			got: instants(t, "America/New_York", dtype.Micro, "2024-11-03T05:30:00", "2024-11-03T06:30:00").
				Select(c("ts").Dt().Truncate(ursus.Every("1h"))),
			want: instants(t, "America/New_York", dtype.Micro, "2024-11-03T05:00:00", "2024-11-03T06:00:00")},
		// 2024-03-10: 03:30 EDT is 07:30Z. The two-hour grid's 02:00 does not exist
		// that night; it moves forward to 03:00 EDT, 07:00Z, as Polars answers.
		{name: "S10 New York spring-forward Truncate(2h)",
			got:  instants(t, "America/New_York", dtype.Micro, "2024-03-10T07:30:00").Select(c("ts").Dt().Truncate(ursus.Every("2h"))),
			want: instants(t, "America/New_York", dtype.Micro, "2024-03-10T07:00:00")},
		{name: "S10 Kolkata GroupByDynamic(Every(1h))",
			got: instants(t, "Asia/Kolkata", dtype.Micro, "2024-01-01T05:17:00", "2024-01-01T05:50:00").
				GroupByDynamic(c("ts"), ursus.DynamicOptions{Every: ursus.Every("1h")}).Agg(ursus.Len().Alias("n")),
			want: instants(t, "Asia/Kolkata", dtype.Micro, "2024-01-01T04:30:00", "2024-01-01T05:30:00").
				WithColumns(ursus.Lit(uint64(1)).Alias("n"))},

		// --- S11: a truncated instant the column cannot hold is refused ---
		{name: "S11 Truncate(Every(1mo)) near the ns minimum",
			got:    nsTicks(nsMin + 3600e9).Select(c("ts").Dt().Truncate(ursus.Every("1mo"))),
			refuse: ursus.ErrValue, naming: []string{"truncate"}},
		{name: "S11 Truncate(time.Hour) at the ns minimum",
			got:    nsTicks(nsMin + 5).Select(c("ts").Dt().Truncate(time.Hour)),
			refuse: ursus.ErrValue, naming: []string{"truncate"}},
		{name: "control: Truncate(time.Hour) an hour above the ns minimum",
			got:  nsTicks(nsMin + 3600e9).Select(c("ts").Dt().Truncate(time.Hour)),
			want: instants(t, "", dtype.Nano, "1677-09-21T01:00:00")},

		// --- S12: an empty literal pattern is counted, as the regex one is ---
		{name: "S12 CountMatches of an empty literal",
			got:  strs("abc", "", "é").Select(c("s").Str().CountMatches("", true)),
			want: ursus.Frame(ursus.Values("s", []uint32{4, 1, 2}))},
		{name: "control: CountMatches of an empty regex",
			got:  strs("abc", "", "é").Select(c("s").Str().CountMatches("", false)),
			want: ursus.Frame(ursus.Values("s", []uint32{4, 1, 2}))},

		// --- S13: an empty cutset is Unicode whitespace, on either side ---
		{name: "S13 StripCharsStart and End of Unicode space",
			got: strs("   x 　\v\f").Select(
				c("s").Str().StripCharsStart("").Alias("l"), c("s").Str().StripCharsEnd("").Alias("r")),
			want: ursus.Frame(ursus.Values("l", []string{"x 　\v\f"}), ursus.Values("r", []string{"   x"}))},
		{name: "control: StripChars of Unicode space",
			got:  strs("   x 　\v\f").Select(c("s").Str().StripChars("")),
			want: strs("x")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wrong := scalarWrong(t, tc)
			why, known := knownScalarDefects[tc.name]
			switch {
			case known && wrong == "":
				t.Errorf("answers correctly now; delete it from knownScalarDefects (%s)", why)
			case known:
				t.Logf("known defect (%s): %s", why, wrong)
			case wrong != "":
				t.Error(wrong)
			}
		})
	}
	for name := range knownScalarDefects {
		if !slices.ContainsFunc(cases, func(c scalarCase) bool { return c.name == name }) {
			t.Errorf("knownScalarDefects names %q, which is not a case", name)
		}
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
		t.Fatalf("the hand answer: %v", err)
	}
	return framesDiffer(t, df, want)
}

package ursus_test

import (
	"strings"
	"testing"
	"time"

	"ursus"
	"ursus/dtype"
	"ursus/internal/bitmap"
	"ursus/internal/data"
	"ursus/internal/source/memsrc"
)

// tsFrame builds a Datetime column in a named zone, which Values cannot do — it maps
// []time.Time to Datetime(ns, UTC).
func tsFrame(t testing.TB, tz string, unit dtype.TimeUnit, stamps ...string) *memsrc.Source {
	t.Helper()
	dt := dtype.Datetime(unit, tz)
	loc := time.UTC
	if tz != "" {
		var err error
		if loc, err = time.LoadLocation(tz); err != nil {
			t.Skipf("no tzdata for %s: %v", tz, err)
		}
	}
	schema := dtype.MustSchema(dtype.NotNull("ts", dt), dtype.NotNull("v", dtype.Int64))

	ticks := make([]int64, len(stamps))
	vs := make([]int64, len(stamps))
	for i, s := range stamps {
		wall, err := time.ParseInLocation("2006-01-02T15:04:05", s, loc)
		if err != nil {
			t.Fatal(err)
		}
		tick, ok := dt.FromTime(wall)
		if !ok {
			t.Fatalf("cannot store %s as %s", s, dt)
		}
		ticks[i], vs[i] = tick, int64(i)
	}
	b, err := data.NewBatch(schema, []*data.Column{
		data.NewFixed("ts", dt, ticks, bitmap.AllSet(len(stamps))),
		data.NewFixed("v", dtype.Int64, vs, bitmap.AllSet(len(stamps))),
	})
	if err != nil {
		t.Fatal(err)
	}
	src, err := memsrc.New(schema, b)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

// TestTruncateByCalendarIntervalIsLocal is the gap Interval exists to close.
//
// Flooring a tick count lands on 00:00 UTC, which in New York is seven or eight in
// the evening of the PREVIOUS day. So every row between local midnight and UTC
// midnight falls into the wrong bucket — by a whole day — and nothing errors. The
// Duration form still floors absolutely, because that is what a Duration means; the
// calendar form reads the column's zone.
func TestTruncateByCalendarIntervalIsLocal(t *testing.T) {
	src := tsFrame(t, "America/New_York", dtype.Micro,
		"2024-06-15T00:30:00", // just after local midnight, still June 14 in UTC
		"2024-06-15T09:30:00",
		"2024-06-15T23:45:00", // still June 15 locally, June 16 in UTC
	)

	df, err := ursus.Scan(src).Select(
		ursus.Col("ts").Dt().Truncate(ursus.Every("1d")).Dt().ToString().Alias("day"),
		ursus.Col("ts").Dt().Truncate(ursus.Every("1mo")).Dt().ToString().Alias("month"),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	days, err := df.Column[string]("day")
	if err != nil {
		t.Fatal(err)
	}
	for i, got := range days.Values() {
		// All three rows are the same LOCAL day, so all three must agree.
		if want := "2024-06-15T00:00:00.000000-04:00"; got != want {
			t.Errorf("row %d truncated to %s, want %s — the column's zone was ignored",
				i, got, want)
		}
	}
	months, _ := df.Column[string]("month")
	for i, got := range months.Values() {
		if want := "2024-06-01T00:00:00.000000-04:00"; got != want {
			t.Errorf("row %d month = %s, want %s", i, got, want)
		}
	}
}

// TestTruncateByDurationStaysAbsolute pins the other half of the contract, so the
// distinction is asserted rather than merely documented — and so the shipped
// Duration behaviour cannot drift while nobody is looking.
func TestTruncateByDurationStaysAbsolute(t *testing.T) {
	src := tsFrame(t, "America/New_York", dtype.Micro, "2024-06-15T09:30:00")
	df, err := ursus.Scan(src).Select(
		ursus.Col("ts").Dt().Truncate(24 * time.Hour).Dt().ToString().Alias("d"),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := df.At[string](0, "d")
	if err != nil {
		t.Fatal(err)
	}
	// 09:30 New York is 13:30 UTC; flooring the instant by 24h gives 00:00 UTC, which
	// is 20:00 the previous evening in New York.
	if want := "2024-06-14T20:00:00.000000-04:00"; got != want {
		t.Errorf("Truncate(24h) = %s, want %s — a Duration floors the INSTANT", got, want)
	}
}

// TestTruncateRefusesCalendarOnTime: a Time is a wall clock with no date, so it has
// no month to floor to.
func TestTruncateRefusesCalendarOnTime(t *testing.T) {
	f := ursus.Frame(ursus.Values("v", []int64{1}))
	_, err := f.Select(
		ursus.Col("v").Cast(ursus.TimeOf(ursus.Micro)).Dt().Truncate(ursus.Every("1mo")),
	).Collect(t.Context())
	if err == nil {
		t.Fatal("truncating a Time by a month was accepted")
	}
}

// TestEveryErrorSurfacesInTheQuery: Every carries its parse failure rather than
// returning it, so the failure has to reach Collect.
func TestEveryErrorSurfacesInTheQuery(t *testing.T) {
	f := ursus.Frame(ursus.Values("v", []int64{1}))
	_, err := f.Select(
		ursus.Col("v").Cast(ursus.Datetime(ursus.Micro, "")).
			Dt().Truncate(ursus.Every("1 fortnight")),
	).Collect(t.Context())
	if err == nil {
		t.Fatal("a bad interval string was accepted")
	}
	if !strings.Contains(err.Error(), "cannot parse interval") {
		t.Errorf("the error should name the parse failure:\n%v", err)
	}
}

// TestIsBetweenClosed: the default is closed-both, which is what IsBetween has always
// meant, and each of the other three moves exactly the endpoint it names.
func TestIsBetweenClosed(t *testing.T) {
	f := ursus.Frame(ursus.Values("v", []int64{0, 1, 2, 3, 4}))
	for _, c := range []struct {
		name string
		e    ursus.Expr
		want []int64
	}{
		{"default", ursus.Col("v").IsBetween(int64(1), int64(3)), []int64{1, 2, 3}},
		{"both", ursus.Col("v").IsBetween(int64(1), int64(3), ursus.ClosedBoth), []int64{1, 2, 3}},
		{"left", ursus.Col("v").IsBetween(int64(1), int64(3), ursus.ClosedLeft), []int64{1, 2}},
		{"right", ursus.Col("v").IsBetween(int64(1), int64(3), ursus.ClosedRight), []int64{2, 3}},
		{"none", ursus.Col("v").IsBetween(int64(1), int64(3), ursus.ClosedNone), []int64{2}},
	} {
		t.Run(c.name, func(t *testing.T) {
			df, err := f.Filter(c.e).Collect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			got := int64Col(t, df, "v")
			if len(got) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("got %v, want %v", got, c.want)
				}
			}
		})
	}

	// Two Closed values is a mistake, not a silent last-one-wins.
	if _, err := f.Filter(ursus.Col("v").
		IsBetween(int64(1), int64(3), ursus.ClosedLeft, ursus.ClosedRight)).
		Collect(t.Context()); err == nil {
		t.Error("passing two Closed values was accepted")
	}
}

// TestClosedNamesAreDistinct: an unnamed enum constant renders as the empty string
// and collides with every other unnamed one — the failure eight name tables had
// before step 11 audited them.
func TestClosedNamesAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range []ursus.Closed{
		ursus.ClosedLeft, ursus.ClosedRight, ursus.ClosedBoth, ursus.ClosedNone,
	} {
		s := c.String()
		if s == "" || s == "?" {
			t.Errorf("Closed(%d) has no name", uint8(c))
		}
		if seen[s] {
			t.Errorf("two Closed constants both render as %q", s)
		}
		seen[s] = true
	}
}

// TestComparingAcrossTemporalUnits is the gap this promotion rule closes, seen from a
// query rather than from the type lattice.
//
// A time.Time literal always lifts to Datetime(ns, UTC), so before step 15
// `Col("ts").Gt(t)` failed on every column at any other resolution — and step 14's
// TestRollingMatchesAManualFilter had to build a nanosecond fixture to write its
// oracle at all.
func TestComparingAcrossTemporalUnits(t *testing.T) {
	// A microsecond column against a nanosecond literal.
	src := tsFrame(t, "UTC", dtype.Micro,
		"2024-01-01T00:00:00", "2024-01-01T01:00:00", "2024-01-01T02:00:00")
	cut := time.Date(2024, 1, 1, 0, 30, 0, 0, time.UTC)

	df, err := ursus.Scan(src).Filter(ursus.Col("ts").Gt(cut)).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 2 {
		t.Fatalf("%d rows, want 2 — the us column and the ns literal must compare", df.Height())
	}

	// And the comparison is EXACT, not approximate: a value that differs only below
	// the coarser column's resolution must still order correctly, because the coarse
	// side is widened rather than the fine side truncated.
	fine := time.Date(2024, 1, 1, 1, 0, 0, 1, time.UTC) // 1ns after the second row
	df2, err := ursus.Scan(src).Filter(ursus.Col("ts").Lt(fine)).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df2.Height() != 2 {
		t.Errorf("%d rows, want 2 — the 01:00 row is 1ns before the cut", df2.Height())
	}
}

// TestTemporalArithmeticAndComparisonAgree: the library used to answer the same
// question two ways — `a - b` retimed to the finer unit and worked, `a > b` refused.
// TimeUnit.Finer's doc claimed promotion used it; only arithmetic did.
func TestTemporalArithmeticAndComparisonAgree(t *testing.T) {
	us := tsFrame(t, "UTC", dtype.Micro, "2024-01-01T00:00:00")
	lf := ursus.Scan(us).Select(
		ursus.Col("ts").Alias("a"),
		ursus.Col("ts").Cast(ursus.Datetime(ursus.Nano, "UTC")).Alias("b"),
	)
	// Both must plan. Before step 15 the subtraction did and the comparison did not.
	if _, err := lf.Select(ursus.Col("a").Sub(ursus.Col("b")).Alias("d")).
		CollectSchema(t.Context()); err != nil {
		t.Errorf("subtraction across units should work: %v", err)
	}
	if _, err := lf.Select(ursus.Col("a").Gt(ursus.Col("b")).Alias("g")).
		CollectSchema(t.Context()); err != nil {
		t.Errorf("comparison across units should work too: %v", err)
	}
}

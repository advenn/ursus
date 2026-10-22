package ursus_test

import (
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// at is a UTC instant on the hour.
func at(y, m, d, h int) time.Time { return time.Date(y, time.Month(m), d, h, 0, 0, 0, time.UTC) }

// timed is a frame of t, the instants given with a zero time.Time a null, as a
// Datetime(us), as Polars' default, beside cols.
func timed(ts []time.Time, cols ...*ursus.Column) *ursus.LazyFrame {
	ok := make([]bool, len(ts))
	for i, v := range ts {
		ok[i] = !v.IsZero()
	}
	return ursus.Frame(append([]*ursus.Column{ursus.ValuesNullable("t", ts, ok)}, cols...)...).
		WithColumns(ursus.Col("t").Cast(ursus.Datetime(ursus.Micro, "")))
}

// TestUpsample is P4 of v0.6.7-scope.md (step 306): Polars' upsample, a row at each
// step from each group's first instant to its last, a row of the frame on the grid
// kept and nulls where there is none. Every answer is Polars 1.44.1's but where a
// comment says otherwise.
func TestUpsample(t *testing.T) {
	ctx := t.Context()
	day := ursus.Every("1d")
	three := timed([]time.Time{at(2024, 1, 1, 0), at(2024, 1, 3, 0), at(2024, 1, 4, 0)}, i64s("v", 1, 2, 3))
	grouped := timed([]time.Time{at(2024, 1, 2, 0), at(2024, 1, 4, 0), at(2024, 1, 1, 0), at(2024, 1, 3, 0)},
		strs("g", "b", "b", "a", "a"), i64s("v", 3, 4, 1, 2))
	ams := func(lf *ursus.LazyFrame) *ursus.LazyFrame {
		return lf.WithColumns(ursus.Col("t").Dt().ReplaceTimeZone("Europe/Amsterdam"))
	}
	asDate := func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.WithColumns(ursus.Col("t").Cast(ursus.Date)) }
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want *ursus.LazyFrame
	}{
		{"daily", three.Upsample("t", day),
			timed([]time.Time{at(2024, 1, 1, 0), at(2024, 1, 2, 0), at(2024, 1, 3, 0), at(2024, 1, 4, 0)},
				i64s("v", 1, nil, 2, 3))},
		{"every 12h", three.Upsample("t", ursus.Every("12h")),
			timed([]time.Time{at(2024, 1, 1, 0), at(2024, 1, 1, 12), at(2024, 1, 2, 0), at(2024, 1, 2, 12),
				at(2024, 1, 3, 0), at(2024, 1, 3, 12), at(2024, 1, 4, 0)}, i64s("v", 1, nil, nil, nil, 2, nil, 3))},
		{"a Duration", three.Upsample("t", 12*time.Hour), three.Upsample("t", ursus.Every("12h"))},
		// The grid is Jan 1 and 3: Jan 4 falls between, and is left out.
		{"every 2d", three.Upsample("t", ursus.Every("2d")),
			timed([]time.Time{at(2024, 1, 1, 0), at(2024, 1, 3, 0)}, i64s("v", 1, 2))},
		{"a row between two steps", timed([]time.Time{at(2024, 1, 1, 0), at(2024, 1, 2, 12), at(2024, 1, 4, 0)},
			i64s("v", 1, 2, 3)).Upsample("t", day),
			timed([]time.Time{at(2024, 1, 1, 0), at(2024, 1, 2, 0), at(2024, 1, 3, 0), at(2024, 1, 4, 0)},
				i64s("v", 1, nil, nil, 3))},
		{"two rows at one instant", timed([]time.Time{at(2024, 1, 1, 0), at(2024, 1, 1, 0), at(2024, 1, 3, 0)},
			i64s("v", 1, 2, 3)).Upsample("t", day),
			timed([]time.Time{at(2024, 1, 1, 0), at(2024, 1, 1, 0), at(2024, 1, 2, 0), at(2024, 1, 3, 0)},
				i64s("v", 1, 2, nil, 3))},
		{"monthly from Jan 31", timed([]time.Time{at(2024, 1, 31, 0), at(2024, 4, 30, 0)}, i64s("v", 1, 2)).
			Upsample("t", ursus.Every("1mo")),
			timed([]time.Time{at(2024, 1, 31, 0), at(2024, 2, 29, 0), at(2024, 3, 31, 0), at(2024, 4, 30, 0)},
				i64s("v", 1, nil, nil, 2))},
		// Polars with maintain_order=True: b first, as it first appears.
		{"grouped", grouped.Upsample("t", day, "g"),
			timed([]time.Time{at(2024, 1, 2, 0), at(2024, 1, 3, 0), at(2024, 1, 4, 0), at(2024, 1, 1, 0),
				at(2024, 1, 2, 0), at(2024, 1, 3, 0)}, strs("g", "b", "b", "b", "a", "a", "a"), i64s("v", 3, nil, 4, 1, nil, 2))},
		{"a null key", timed([]time.Time{at(2024, 1, 1, 0), at(2024, 1, 3, 0)}, strs("g", nil, nil), i64s("v", 1, 2)).
			Upsample("t", day, "g"),
			timed([]time.Time{at(2024, 1, 1, 0), at(2024, 1, 2, 0), at(2024, 1, 3, 0)}, strs("g", nil, nil, nil),
				i64s("v", 1, nil, 2))},
		{"a Date", asDate(timed([]time.Time{at(2024, 1, 1, 0), at(2024, 1, 3, 0)}, i64s("v", 1, 2))).Upsample("t", day),
			asDate(timed([]time.Time{at(2024, 1, 1, 0), at(2024, 1, 2, 0), at(2024, 1, 3, 0)}, i64s("v", 1, nil, 2)))},
		// A day of Amsterdam's wall clock, 23 hours over the change to summer time.
		{"days in a zone", ams(timed([]time.Time{at(2024, 3, 30, 0), at(2024, 4, 1, 0)}, i64s("v", 1, 2))).Upsample("t", day),
			ams(timed([]time.Time{at(2024, 3, 30, 0), at(2024, 3, 31, 0), at(2024, 4, 1, 0)}, i64s("v", 1, nil, 2)))},
		// Hours over the change: 02:00 is not, so 01:00, 03:00, 04:00.
		{"hours in a zone", ams(timed([]time.Time{at(2024, 3, 31, 1), at(2024, 3, 31, 4)}, i64s("v", 1, 2))).
			Upsample("t", ursus.Every("1h")),
			ams(timed([]time.Time{at(2024, 3, 31, 1), at(2024, 3, 31, 3), at(2024, 3, 31, 4)}, i64s("v", 1, nil, 2)))},
		{"one row", three.Head(1).Upsample("t", day), three.Head(1)},
		// Polars refuses a frame not sorted by its time column; this sorts it.
		{"unsorted", timed([]time.Time{at(2024, 1, 3, 0), at(2024, 1, 1, 0)}, i64s("v", 2, 1)).Upsample("t", day),
			timed([]time.Time{at(2024, 1, 1, 0), at(2024, 1, 2, 0), at(2024, 1, 3, 0)}, i64s("v", 1, nil, 2))},
		// Polars refuses a null instant ("not sorted"); its row has no step here.
		{"a null instant", timed([]time.Time{at(2024, 1, 1, 0), {}, at(2024, 1, 3, 0)}, i64s("v", 1, 2, 3)).Upsample("t", day),
			timed([]time.Time{at(2024, 1, 1, 0), at(2024, 1, 2, 0), at(2024, 1, 3, 0)}, i64s("v", 1, nil, 3))},
		// Polars refuses no rows, "cannot determine upsample boundaries".
		{"no rows", three.Head(0).Upsample("t", day), three.Head(0)},
	} {
		got, err := tc.q.Collect(ctx)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		want, err := tc.want.Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(tc.name, func(t *testing.T) { ursustest.AssertFrameEqual(t, got, want) })
	}

	t.Run("refused", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			q    *ursus.LazyFrame
			want string
		}{
			// Polars: `interval` must be positive.
			{"a negative step", three.Upsample("t", ursus.Every("-1d")), "upsample steps by a positive interval, and -1d is not one"},
			{"no step", three.Upsample("t", time.Duration(0)), "positive interval"},
			// An integer index steps by "i" (step 312); Polars: must be a parsed integer.
			{"an integer index by a day", ursus.Frame(i64s("t", 1, 4)).Upsample("t", day), "every=1d is an interval of time"},
			// Polars steps a Date by 12h into each date twice.
			{"a Date by 12h", asDate(three).Upsample("t", ursus.Every("12h")), "upsample steps a Date by whole days, and 12h is not"},
			{"an unknown time column", three.Upsample("u", day), `"u"`},
			{"an unknown group", three.Upsample("t", day, "g"), `"g"`},
			{"the time column a group", three.Upsample("t", day, "t"), `"t" is named twice`},
		} {
			_, err := tc.q.Collect(ctx)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: got %v, want an error with %q", tc.name, err, tc.want)
			}
		}
	})

	t.Run("eager", func(t *testing.T) {
		df, err := grouped.Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got, err := df.Upsample(ctx, "t", day, "g")
		if err != nil {
			t.Fatal(err)
		}
		want, err := grouped.Upsample("t", day, "g").Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, want)
	})
}

// TestUpsampleUnderAMemoryLimit (step 306): an upsample is a group-by, a join and a
// sort, each spilling past a limit, and its rows keep the grid's order there too, so
// that a fill forward after it reads the rows it should: 40 stations' 500 days, a
// reading every third day, under 64 KiB in batches of 64, answer as without a limit.
func TestUpsampleUnderAMemoryLimit(t *testing.T) {
	ctx := t.Context()
	var ts []time.Time
	var station []string
	var reading []int64
	for s := range 40 {
		for d := 0; d < 500; d += 3 {
			ts = append(ts, at(2024, 1, 1+d, 0))
			station = append(station, string(rune('A'+s%26))+string(rune('a'+s/26)))
			reading = append(reading, int64(s*1000+d))
		}
	}
	q := timed(ts, ursus.Values("station", station), ursus.Values("reading", reading)).
		Upsample("t", ursus.Every("1d"), "station").
		WithColumns(ursus.Col("reading").FillNull(ursus.FillForward))
	want, err := q.Collect(ctx, ursus.WithBatchSize(64))
	if err != nil {
		t.Fatal(err)
	}
	var st ursus.MemoryStats
	got, err := q.Collect(ctx, ursus.WithBatchSize(64), ursus.WithMemoryLimit(64<<10), ursus.WithSpillDir(t.TempDir()),
		ursus.WithMemoryStats(&st))
	if err != nil {
		t.Fatalf("under 64 KiB: %v", err)
	}
	if st.Spills == 0 {
		t.Error("answered without spilling, so it proves nothing")
	}
	ursustest.AssertFrameEqual(t, got, want)
	if got.Height() != 40*499 {
		t.Errorf("%d rows, want %d", got.Height(), 40*499)
	}
}

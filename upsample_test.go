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
// kept and nulls where there is none. The rows are the upsample group's, Polars
// 2.1.0's (oracle_upsample_test.go); here stay a frame of no rows, which Polars
// refuses and which answers none here (feature-gaps.md §9), the refusals and the eager
// form.
func TestUpsample(t *testing.T) {
	ctx := t.Context()
	day := ursus.Every("1d")
	three := timed([]time.Time{at(2024, 1, 1, 0), at(2024, 1, 3, 0), at(2024, 1, 4, 0)}, i64s("v", 1, 2, 3))
	grouped := timed([]time.Time{at(2024, 1, 2, 0), at(2024, 1, 4, 0), at(2024, 1, 1, 0), at(2024, 1, 3, 0)},
		strs("g", "b", "b", "a", "a"), i64s("v", 3, 4, 1, 2))
	asDate := func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.WithColumns(ursus.Col("t").Cast(ursus.Date)) }

	t.Run("no rows", func(t *testing.T) {
		// Polars refuses no rows, "cannot determine upsample boundaries".
		got, err := three.Head(0).Upsample("t", day).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got.Height() != 0 || strings.Join(got.Columns(), ",") != "t,v" {
			t.Errorf("no rows upsampled: %d rows of %v", got.Height(), got.Columns())
		}
	})

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

// TestUpsampleKeepsTheGridsOrder: since step 383 the join of the grid to the frame
// keeps the grid's order and each step's rows in the frame's, where a sort by the
// two, numbered, restored it. Over 20,000 rows of 60 groups, a null group among them,
// in no order, with instants repeated, between two steps, and null, on one thread and
// eight in batches of 97, the answer is the numbered sort's, row for row.
func TestUpsampleKeepsTheGridsOrder(t *testing.T) {
	const n = 20_000
	ts := make([]time.Time, n)
	g, gOK := make([]int64, n), make([]bool, n)
	v := make([]int64, n)
	for i := range n {
		h := uint64(i) * 0x9E3779B97F4A7C15 >> 9
		g[i], gOK[i] = int64(h%60), h%61 != 0
		ts[i] = at(2024, 1, 1, 0).Add(time.Duration(h%900) * time.Hour)
		if h%7 == 0 {
			ts[i] = ts[i].Add(30 * time.Minute) // between two steps
		}
		if h%101 == 0 {
			ts[i] = time.Time{}
		}
		v[i] = int64(i)
	}
	lf := timed(ts, ursus.ValuesNullable("g", g, gOK), ursus.Values("v", v))
	c := ursus.Col
	// The composition step 306 built: the grid and the frame numbered, joined, and
	// sorted by the two numbers.
	grid := lf.GroupBy(c("g")).MaintainOrder().
		Agg(ursus.DatetimeRanges(c("t").Min(), c("t").Max(), ursus.Every("1h")).Alias("t")).
		Explode("t").Filter(c("t").IsNotNull()).WithRowIndex("step", 0)
	want, err := grid.Join(lf.WithRowIndex("row", 0), ursus.JoinOn(c("g"), c("t")), ursus.JoinHow(ursus.JoinLeft),
		ursus.JoinNullsEqual(true)).Sort(ursus.Asc(c("step")), ursus.Asc(c("row"))).
		Select(c("t"), c("g"), c("v")).Collect(t.Context(), ursus.WithThreads(1))
	if err != nil {
		t.Fatal(err)
	}
	if want.Height() < 2*n {
		t.Fatalf("%d rows: too few steps without a row", want.Height())
	}
	for _, threads := range []int{1, 8} {
		got, err := lf.Upsample("t", ursus.Every("1h"), "g").
			Collect(t.Context(), ursus.WithThreads(threads), ursus.WithBatchSize(97))
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, want)
	}
}

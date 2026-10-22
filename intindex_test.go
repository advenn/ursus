package ursus_test

import (
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestIntegerIndexGroups is G4 of v0.6.7-scope.md (step 312): GroupByDynamic and
// Rolling over an Int32 or an Int64 index, by Polars' integer intervals, "3i". Every
// answer is Polars 1.44.1's but where a comment says otherwise.
func TestIntegerIndexGroups(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	lf := ursus.Frame(i64s("i", 0, 1, 2, 5, 6, 9), i64s("v", 1, 2, 3, 4, 5, 6), strs("g", "a", "a", "b", "a", "b", "a"))
	dyn := func(o ursus.DynamicOptions) *ursus.GroupBy { return lf.GroupByDynamic(c("i"), o) }
	every := ursus.Every("3i")
	count := ursus.Len().Cast(ursus.Int64)
	sum := c("v").Sum().Cast(ursus.Int64)
	frame := ursus.Frame
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want *ursus.LazyFrame
	}{
		{"every 3i", dyn(ursus.DynamicOptions{Every: every}).Agg(count, sum),
			frame(i64s("i", 0, 3, 6, 9), i64s("len", 3, 1, 1, 1), i64s("v", 6, 4, 5, 6))},
		{"every 2i, period 3i", dyn(ursus.DynamicOptions{Every: ursus.Every("2i"), Period: every}).Agg(count, sum),
			frame(i64s("i", 0, 2, 4, 6, 8), i64s("len", 3, 1, 2, 1, 1), i64s("v", 6, 3, 9, 5, 6))},
		{"offset 1i", dyn(ursus.DynamicOptions{Every: every, Offset: ursus.Every("1i")}).Agg(count),
			frame(i64s("i", -2, 1, 4, 7), i64s("len", 1, 2, 2, 1))},
		{"boundaries", dyn(ursus.DynamicOptions{Every: every, IncludeBoundaries: true}).Agg(count),
			frame(i64s("_lower_boundary", 0, 3, 6, 9), i64s("_upper_boundary", 3, 6, 9, 12), i64s("i", 0, 3, 6, 9),
				i64s("len", 3, 1, 1, 1))},
		{"closed both", dyn(ursus.DynamicOptions{Every: every, Closed: ursus.ClosedBoth}).Agg(count),
			frame(i64s("i", 0, 3, 6, 9), i64s("len", 3, 2, 2, 1))},
		{"label right", dyn(ursus.DynamicOptions{Every: every, Label: "right"}).Agg(count),
			frame(i64s("i", 3, 6, 9, 12), i64s("len", 3, 1, 1, 1))},
		{"start at the first row", dyn(ursus.DynamicOptions{Every: every, StartBy: "datapoint"}).Agg(count),
			frame(i64s("i", 0, 3, 6, 9), i64s("len", 3, 1, 1, 1))},
		// Floored: -5 is in the window of -6.
		{"negative", frame(i64s("i", -5, -1, 0, 4)).GroupByDynamic(c("i"), ursus.DynamicOptions{Every: every}).Agg(count),
			frame(i64s("i", -6, -3, 0, 3), i64s("len", 1, 1, 1, 1))},
		{"rolling 3i", lf.Rolling(c("i"), ursus.RollingOptions{Period: every}).Agg(count, sum),
			frame(i64s("i", 0, 1, 2, 5, 6, 9), i64s("len", 1, 2, 3, 1, 2, 1), i64s("v", 1, 3, 6, 4, 9, 6))},
		// The index first, then the key, where Polars puts the key first (§9).
		{"rolling by a key", lf.Rolling(c("i"), ursus.RollingOptions{Period: every, GroupBy: []ursus.Expr{c("g")}}).Agg(count),
			frame(i64s("i", 0, 1, 5, 9, 2, 6), strs("g", "a", "a", "a", "a", "b", "b"), i64s("len", 1, 2, 1, 1, 1, 1))},
		{"label datapoint", dyn(ursus.DynamicOptions{Every: every, Label: "datapoint"}).Agg(count),
			frame(i64s("i", 0, 5, 6, 9), i64s("len", 3, 1, 1, 1))},
		{"Having", dyn(ursus.DynamicOptions{Every: every}).Having(ursus.Len().Gt(1)).Agg(sum), frame(i64s("i", 0), i64s("v", 6))},
		{"a shorthand", lf.Select(c("i"), c("v")).GroupByDynamic(c("i"), ursus.DynamicOptions{Every: every}).First(),
			frame(i64s("i", 0, 3, 6, 9), i64s("v", 1, 4, 5, 6))},
		{"rolling over a UInt32", lf.WithColumns(c("i").Cast(ursus.Uint32)).Rolling(c("i"), ursus.RollingOptions{Period: every}).Agg(sum),
			frame(i64s("i", 0, 1, 2, 5, 6, 9), i64s("v", 1, 3, 6, 4, 9, 6)).WithColumns(c("i").Cast(ursus.Uint32))},
		{"rolling over a UInt64", lf.WithColumns(c("i").Cast(ursus.Uint64)).Rolling(c("i"), ursus.RollingOptions{Period: every}).Agg(sum),
			frame(i64s("i", 0, 1, 2, 5, 6, 9), i64s("v", 1, 3, 6, 4, 9, 6)).WithColumns(c("i").Cast(ursus.Uint64))},
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

	t.Run("an Int32 stays one", func(t *testing.T) {
		got, err := lf.WithColumns(c("i").Cast(ursus.Int32)).GroupByDynamic(c("i"), ursus.DynamicOptions{Every: every}).
			Agg(count).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if f, _ := got.Schema().ByName("i"); f.Type != ursus.Int32 {
			t.Errorf("the index is %s, want Int32", f.Type)
		}
	})

	t.Run("refused", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			q    *ursus.LazyFrame
			want string
		}{
			// Polars: expected any of the following dtypes: { Date, Datetime, Int32, Int64 }, got u32.
			{"a UInt32 index", lf.WithColumns(c("i").Cast(ursus.Uint32)).GroupByDynamic(c("i"), ursus.DynamicOptions{Every: every}).Agg(count),
				`the index column "i" is Uint32, where an index is a Date, a Datetime, an Int32 or an Int64`},
			// Polars: unsupported data type: i16 for temporal/index column.
			{"a rolling Int16 index", lf.WithColumns(c("i").Cast(ursus.Int16)).Rolling(c("i"), ursus.RollingOptions{Period: every}).Agg(count),
				`the index column "i" is Int16, where an index is a Date, a Datetime, an Int32, an Int64, a UInt32 or a UInt64`},
			{"a UInt64 past an Int64", ursus.Frame(ursus.Values("i", []uint64{0, 1 << 63})).Rolling(c("i"),
				ursus.RollingOptions{Period: every}).Agg(count), "9223372036854775808"},
			// Polars: `every` duration must be a parsed integer (i.e. use '2i', not '2d').
			{"days on an integer", dyn(ursus.DynamicOptions{Every: ursus.Every("1d")}).Agg(count),
				"every=1d is an interval of time, and the column is Int64"},
			// Polars: `every` duration may not be a parsed integer.
			{"an integer interval on a date", lf.WithColumns(c("i").Cast(ursus.Date)).GroupByDynamic(c("i"),
				ursus.DynamicOptions{Every: ursus.Every("1i")}).Agg(count), "every=1i counts an integer index, and the column is Date"},
			{"a rolling period of time", lf.Rolling(c("i"), ursus.RollingOptions{Period: ursus.Every("3h")}).Agg(count),
				"period=3h is an interval of time"},
			// Polars: cannot mix month, week, day, and sub-daily units.
			{"units mixed", dyn(ursus.DynamicOptions{Every: ursus.Every("1d1i")}).Agg(count), "the integer unit i mixes with no unit of time"},
		} {
			_, err := tc.q.Collect(ctx)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: got %v, want an error with %q", tc.name, err, tc.want)
			}
		}
	})
}

// TestIntegerBy (step 312): the rolling functions and ewms by an Int32 or an Int64,
// with Polars' integer periods and half-lives. Every answer is Polars 1.44.1's.
func TestIntegerBy(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	lf := ursus.Frame(i64s("i", 0, 1, 2, 5, 6, 9), i64s("v", 1, 2, 3, 4, 5, 6))
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want *ursus.Column
	}{
		{"rolling_sum_by", c("v").RollingSumBy(c("i"), ursus.Every("3i")).Cast(ursus.Int64), i64s("v", 1, 3, 6, 4, 9, 6)},
		{"rolling_mean_by, closed both", c("v").RollingMeanBy(c("i"), ursus.Every("3i"), ursus.WindowClosed(ursus.ClosedBoth)),
			f64s("v", 1.0, 1.5, 2.0, 3.5, 4.5, 5.5)},
		{"ewm_mean_by", c("v").EwmMeanBy(c("i"), ursus.Every("2i")),
			f64s("v", 1.0, 1.2928932188134525, 1.7928932188134525, 3.2196699141100895, 3.7411165235168156, 5.201364087934249)},
		{"by an Int32", c("v").RollingSumBy(c("i").Cast(ursus.Int32), ursus.Every("3i")).Cast(ursus.Int64),
			i64s("v", 1, 3, 6, 4, 9, 6)},
		{"by a UInt32", c("v").RollingSumBy(c("i").Cast(ursus.Uint32), ursus.Every("3i")).Cast(ursus.Int64),
			i64s("v", 1, 3, 6, 4, 9, 6)},
		{"ewm_mean_by a UInt64", c("v").EwmMeanBy(c("i").Cast(ursus.Uint64), ursus.Every("2i")),
			f64s("v", 1.0, 1.2928932188134525, 1.7928932188134525, 3.2196699141100895, 3.7411165235168156, 5.201364087934249)},
	} {
		got, err := lf.Select(tc.e).Collect(ctx)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		want, err := ursus.Frame(tc.want).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(tc.name, func(t *testing.T) { ursustest.AssertFrameEqual(t, got, want) })
	}
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want string
	}{
		{"a period of time", c("v").RollingSumBy(c("i"), ursus.Every("3d")), "period=3d is an interval of time, and the column is Int64"},
		{"an integer period by a date", c("v").RollingSumBy(c("i").Cast(ursus.Date), ursus.Every("3i")),
			"period=3i counts an integer index, and the column is Date"},
		{"a half-life of time", c("v").EwmMeanBy(c("i"), ursus.Every("2h")), "half-life=2h is an interval of time"},
		{"by a float", c("v").RollingSumBy(c("i").Cast(ursus.Float64), ursus.Every("3i")), "got Float64"},
		// Polars: unsupported data type: i16 for temporal/index column.
		{"by an Int16", c("v").EwmMeanBy(c("i").Cast(ursus.Int16), ursus.Every("2i")), "got Int16"},
	} {
		_, err := lf.Select(tc.e).Collect(ctx)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error with %q", tc.name, err, tc.want)
		}
	}
	big := ursus.Frame(ursus.Values("i", []uint64{0, 1 << 63}), i64s("v", 1, 2))
	if _, err := big.Select(c("v").RollingSumBy(c("i"), ursus.Every("3i"))).Collect(ctx); err == nil ||
		!strings.Contains(err.Error(), "past the largest Int64") {
		t.Errorf("a by of UInt64 past an Int64: got %v", err)
	}
}

// TestIntegerUpsample (step 312): Upsample over an integer index by Polars' "Ni",
// the index's type kept. Every answer is Polars 1.44.1's, with maintain_order.
func TestIntegerUpsample(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	lf := ursus.Frame(i64s("i", 0, 3, 5), i64s("v", 1, 2, 3))
	frame := ursus.Frame
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want *ursus.LazyFrame
	}{
		// 3 and 5 are off the grid of 0, 2, 4.
		{"every 2i", lf.Upsample("i", ursus.Every("2i")), frame(i64s("i", 0, 2, 4), i64s("v", 1, nil, nil))},
		{"an Int32", lf.WithColumns(c("i").Cast(ursus.Int32)).Upsample("i", ursus.Every("1i")),
			frame(i64s("i", 0, 1, 2, 3, 4, 5), i64s("v", 1, nil, nil, 2, nil, 3)).WithColumns(c("i").Cast(ursus.Int32))},
		{"a UInt32", lf.WithColumns(c("i").Cast(ursus.Uint32)).Upsample("i", ursus.Every("2i")),
			frame(i64s("i", 0, 2, 4), i64s("v", 1, nil, nil)).WithColumns(c("i").Cast(ursus.Uint32))},
		{"negative", frame(i64s("i", -3, 0), i64s("v", 1, 2)).Upsample("i", ursus.Every("2i")),
			frame(i64s("i", -3, -1), i64s("v", 1, nil))},
		{"a group's", frame(i64s("i", 0, 3, 1, 4), strs("g", "a", "a", "b", "b"), i64s("v", 1, 2, 3, 4)).Upsample("i", ursus.Every("1i"), "g"),
			frame(i64s("i", 0, 1, 2, 3, 1, 2, 3, 4), strs("g", "a", "a", "a", "a", "b", "b", "b", "b"),
				i64s("v", 1, nil, nil, 2, 3, nil, nil, 4))},
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
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		// Polars: `every` duration must be a parsed integer (i.e. use '2i', not '2d').
		{"days on an integer", lf.Upsample("i", ursus.Every("1d")), "every=1d is an interval of time, and the column is Int64"},
		// Polars: `every` duration may not be a parsed integer.
		{"an integer interval on a date", lf.WithColumns(c("i").Cast(ursus.Date)).Upsample("i", ursus.Every("1i")),
			"every=1i counts an integer index, and the column is Date"},
		{"an Int16", lf.WithColumns(c("i").Cast(ursus.Int16)).Upsample("i", ursus.Every("1i")),
			`upsample's time column is a Date, a Datetime or an integer of 32 or 64 bits, and "i" is Int16`},
	} {
		_, err := tc.q.Collect(ctx)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error with %q", tc.name, err, tc.want)
		}
	}
}

// TestIntegerIntervalMovesNoDate (step 312): "3i" counts an index, so where a date or
// an instant is moved, it is refused. Polars' truncate and offset_by take it and move
// nothing, its round panics, and its as-of tolerance reads it as nanoseconds.
func TestIntegerIntervalMovesNoDate(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	ts := []time.Time{at(2024, 1, 1, 0), at(2024, 1, 2, 0)}
	lf := ursus.Frame(ursus.Values("t", ts), strs("o", "3i", "1d"))
	three := ursus.Every("3i")
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		{"truncate", lf.Select(c("t").Dt().Truncate(three)), "truncate takes an interval of time, and 3i counts an integer index"},
		{"round", lf.Select(c("t").Dt().Round(three)), "round takes an interval of time"},
		{"offset_by", lf.Select(c("t").Dt().OffsetBy(three)), "offset_by takes an interval of time"},
		{"offset_by a row's", lf.Select(c("t").Dt().OffsetBy(c("o"))), `offset_by "3i" in row 0`},
		{"a date range", ursus.Frame(i64s("x", 1)).Select(ursus.DateRange(c("x").Cast(ursus.Date), c("x").Cast(ursus.Date), ursus.Every("1i"))),
			"takes an interval of time, and 1i counts an integer index"},
		{"a datetime range", lf.Select(ursus.DatetimeRanges(c("t"), c("t"), three)), "takes an interval of time"},
		{"an as-of tolerance", lf.JoinAsOf(lf, ursus.AsOfOn(c("t")), ursus.AsOfTolerance(three)),
			"tolerance takes an interval of time, and 3i counts an integer index"},
	} {
		_, err := tc.q.Collect(ctx)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error with %q", tc.name, err, tc.want)
		}
	}
}

// TestIntegerIndexAnswersAsADatetime (step 312): an integer index is read as the ticks
// of a Datetime(ns), so its groups answer as that Datetime's do by as many
// nanoseconds, over many batches and keys: 50,000 rows, and the dynamic groups under
// a memory limit of a fifth of their bytes too. Equals compares them, as
// AssertFrameEqual takes a second over rows this many.
func TestIntegerIndexAnswersAsADatetime(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	n, scale := 50_000, int64(1)
	if raceDetector {
		// A tenth of the rows, and the full run's limit, as
		// TestTemporalGroupsStreamTheirInput takes under the race detector.
		n, scale = 5_000, 10
	}
	is, ks, vs := make([]int64, n), make([]int64, n), make([]int64, n)
	for i := range n {
		is[i], ks[i], vs[i] = int64(i*7+i%5), int64(i%5), int64(i)
	}
	ints := ursus.Frame(ursus.Values("i", is), ursus.Values("k", ks), ursus.Values("v", vs))
	nanos := ints.WithColumns(c("i").Cast(ursus.Datetime(ursus.Nano, "")))
	in, err := ints.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fifth := in.Batch().NBytes() / 5 * scale
	k := []ursus.Expr{c("k")}
	aggs := []ursus.Expr{c("v").Sum().Alias("s"), ursus.Len().Alias("n"), c("v").Last().Alias("l")}
	same := func(name string, got, want *ursus.DataFrame) {
		t.Helper()
		ursustest.AssertSchemaEqual(t, got.Schema(), want.Schema())
		if ok, err := got.Equals(ctx, want, true); err != nil || !ok {
			t.Errorf("%s: the integer index answers otherwise than the Datetime(ns) one (%v)", name, err)
		}
	}
	for _, tc := range []struct {
		name  string
		q     func(lf *ursus.LazyFrame, every, period string) *ursus.LazyFrame
		limit int64
	}{
		{"dynamic", func(lf *ursus.LazyFrame, every, period string) *ursus.LazyFrame {
			return lf.GroupByDynamic(c("i"), ursus.DynamicOptions{Every: ursus.Every(every), Period: ursus.Every(period),
				IncludeBoundaries: true}).Agg(aggs...)
		}, fifth},
		{"dynamic by k", func(lf *ursus.LazyFrame, every, period string) *ursus.LazyFrame {
			return lf.GroupByDynamic(c("i"), ursus.DynamicOptions{Every: ursus.Every(every), Period: ursus.Every(period),
				GroupBy: k}).Agg(aggs...)
		}, fifth},
		{"rolling by k", func(lf *ursus.LazyFrame, _, period string) *ursus.LazyFrame {
			return lf.Rolling(c("i"), ursus.RollingOptions{Period: ursus.Every(period), GroupBy: k}).Agg(aggs...)
		}, 0},
	} {
		want, err := tc.q(nanos, "60ns", "180ns").WithColumns(ursus.ColRegex("^(i|_lower_boundary|_upper_boundary)$").Cast(ursus.Int64)).
			Collect(ctx, ursus.WithBatchSize(1024))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got, err := tc.q(ints, "60i", "180i").Collect(ctx, ursus.WithBatchSize(1024))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		same(tc.name, got, want)
		if tc.limit == 0 {
			continue
		}
		var st ursus.MemoryStats
		got, err = tc.q(ints, "60i", "180i").Collect(ctx, ursus.WithBatchSize(1024), ursus.WithMemoryLimit(tc.limit),
			ursus.WithMemoryStats(&st))
		if err != nil {
			t.Fatalf("%s under %d bytes: %v", tc.name, tc.limit, err)
		}
		same(tc.name, got, want)
		t.Logf("%s: a peak of %d bytes, the limit %d", tc.name, st.Peak, tc.limit)
	}
}

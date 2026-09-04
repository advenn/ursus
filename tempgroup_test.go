package ursus_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"ursus"
	"ursus/dtype"
	"ursus/internal/uerr"
	"ursus/ursustest"
)

// hourly builds a UTC Datetime index at the given wall-clock stamps.
func hourly(t testing.TB, stamps ...string) *ursus.LazyFrame {
	t.Helper()
	return ursus.Scan(tsFrame(t, "UTC", dtype.Micro, stamps...))
}

// TestGroupByDynamicMatchesTruncateGroupBy is the differential that pins the core
// against something already trusted.
//
// For the one configuration a hash group-by can express — period == every, a
// non-calendar interval, no offset, closed-left — the two must agree exactly on
// every window that contains a row. The dynamic operator additionally emits the
// EMPTY ones, which is why the comparison is against a filtered result.
func TestGroupByDynamicMatchesTruncateGroupBy(t *testing.T) {
	src := tsFrame(t, "UTC", dtype.Micro,
		"2024-01-01T00:10:00", "2024-01-01T00:40:00",
		"2024-01-01T01:05:00",
		"2024-01-01T03:30:00", "2024-01-01T03:31:00", "2024-01-01T03:59:59",
	)
	every := ursus.Every("1h")

	want, err := ursus.Scan(src).
		GroupBy(ursus.Col("ts").Dt().Truncate(every).Alias("ts")).MaintainOrder().
		Agg(ursus.Len().Alias("n"), ursus.Col("v").Sum().Alias("s")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	got, err := ursus.Scan(src).
		GroupByDynamic(ursus.Col("ts"), ursus.DynamicOptions{Every: every}).
		Agg(ursus.Len().Alias("n"), ursus.Col("v").Sum().Alias("s")).
		Filter(ursus.Col("n").Gt(uint64(0))).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, want)
}

// TestGroupByDynamicEmitsEmptyWindows is the property that makes this operator worth
// having at all: a gap in a time series becomes a zero, not a missing row.
func TestGroupByDynamicEmitsEmptyWindows(t *testing.T) {
	// 00:10 and 03:30 — hours 01 and 02 are empty.
	df, err := hourly(t, "2024-01-01T00:10:00", "2024-01-01T03:30:00").
		GroupByDynamic(ursus.Col("ts"), ursus.DynamicOptions{Every: ursus.Every("1h")}).
		Agg(ursus.Len().Alias("n")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 4 {
		t.Fatalf("%d windows, want 4 (00, 01, 02, 03) — the empty ones are missing", df.Height())
	}
	want := []uint64{1, 0, 0, 1}
	for i, w := range want {
		got, _, err := df.At[uint64](i, "n")
		if err != nil {
			t.Fatal(err)
		}
		if got != w {
			t.Errorf("window %d has n=%d, want %d", i, got, w)
		}
	}
	// A hash group-by over the same data emits two rows, which is the difference.
	hash, err := hourly(t, "2024-01-01T00:10:00", "2024-01-01T03:30:00").
		GroupBy(ursus.Col("ts").Dt().Truncate(ursus.Every("1h"))).
		Agg(ursus.Len().Alias("n")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if hash.Height() != 2 {
		t.Fatalf("the hash group-by emitted %d rows; this test's premise is stale",
			hash.Height())
	}
}

// TestGroupByDynamicOverlappingWindows: period > every means a row belongs to
// several windows, so the total count EXCEEDS the row count. That is the documented
// answer, and it is the case a per-row group id could not represent.
func TestGroupByDynamicOverlappingWindows(t *testing.T) {
	df, err := hourly(t,
		"2024-01-01T00:30:00", "2024-01-01T01:30:00",
		"2024-01-01T02:30:00", "2024-01-01T03:30:00",
	).GroupByDynamic(ursus.Col("ts"), ursus.DynamicOptions{
		Every:  ursus.Every("1h"),
		Period: ursus.Every("2h"), // each window covers two hours
	}).Agg(ursus.Len().Alias("n")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var total uint64
	for i := range df.Height() {
		n, _, err := df.At[uint64](i, "n")
		if err != nil {
			t.Fatal(err)
		}
		total += n
	}
	if total <= 4 {
		t.Errorf("total count over overlapping windows is %d, which does not exceed "+
			"the 4 input rows — the windows did not overlap", total)
	}
	// Each interior window covers exactly two of the four hourly rows.
	if n, _, _ := df.At[uint64](0, "n"); n != 2 {
		t.Errorf("the first 2h window has n=%d, want 2", n)
	}
}

// TestClosedBoundaries: all four conventions against rows sitting exactly on window
// boundaries, which is the only place they differ.
func TestClosedBoundaries(t *testing.T) {
	// Rows exactly at 00:00, 01:00 and 02:00 — every one is a boundary of an hourly
	// grid.
	f := func() *ursus.LazyFrame {
		return hourly(t, "2024-01-01T00:00:00", "2024-01-01T01:00:00",
			"2024-01-01T02:00:00")
	}
	for _, c := range []struct {
		closed ursus.Closed
		total  uint64
		note   string
	}{
		{ursus.ClosedLeft, 3, "each row starts its own window"},
		{ursus.ClosedRight, 3, "each row ends the previous window"},
		// Five, not six: the row at the very first boundary has no window before it,
		// so it is in one window where the other two are in two.
		{ursus.ClosedBoth, 5, "the interior rows are in two windows each"},
		{ursus.ClosedNone, 0, "each row is in neither"},
	} {
		t.Run(c.closed.String(), func(t *testing.T) {
			df, err := f().GroupByDynamic(ursus.Col("ts"), ursus.DynamicOptions{
				Every: ursus.Every("1h"), Closed: c.closed,
			}).Agg(ursus.Len().Alias("n")).Collect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var total uint64
			for i := range df.Height() {
				n, _, _ := df.At[uint64](i, "n")
				total += n
			}
			if total != c.total {
				t.Errorf("closed=%s total=%d, want %d (%s)",
					c.closed, total, c.total, c.note)
			}
		})
	}
}

// TestRollingIsOneGroupPerRow: the output height equals the input height, which is
// the structural difference from a dynamic group.
func TestRollingIsOneGroupPerRow(t *testing.T) {
	stamps := []string{
		"2024-01-01T00:00:00", "2024-01-01T00:30:00", "2024-01-01T01:00:00",
		"2024-01-01T05:00:00",
	}
	df, err := hourly(t, stamps...).
		Rolling(ursus.Col("ts"), ursus.RollingOptions{Period: ursus.Every("1h")}).
		Agg(ursus.Len().Alias("n")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != len(stamps) {
		t.Fatalf("%d rows, want %d — rolling emits one window per row",
			df.Height(), len(stamps))
	}
	// (t-1h, t]: row 0 sees itself; row 1 sees rows 0 and 1; row 2 sees 1 and 2
	// (row 0 is exactly 1h before, and the left end is open); row 3 sees itself.
	for i, want := range []uint64{1, 2, 2, 1} {
		got, _, err := df.At[uint64](i, "n")
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("row %d has n=%d, want %d", i, got, want)
		}
	}
}

// TestRollingMatchesAManualFilter is the oracle: each row's window is exactly the
// rows a Filter would keep, and the two share no code.
func TestRollingMatchesAManualFilter(t *testing.T) {
	stamps := []string{
		"2024-01-01T00:00:00", "2024-01-01T00:17:00", "2024-01-01T00:45:00",
		"2024-01-01T01:10:00", "2024-01-01T02:00:00", "2024-01-01T02:01:00",
	}
	// Nanosecond, so a time.Time literal in the reference Filter has the same type:
	// dtype.Promote says "temporal types promote only to themselves", so a Micro
	// column and a Nano literal have no common type at all. That is a real gap and
	// it is recorded rather than worked around anywhere but here.
	src := tsFrame(t, "UTC", dtype.Nano, stamps...)
	got, err := ursus.Scan(src).
		Rolling(ursus.Col("ts"), ursus.RollingOptions{Period: ursus.Every("1h")}).
		Agg(ursus.Len().Alias("n"), ursus.Col("v").Sum().Alias("s")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	for i, s := range stamps {
		end, err := time.Parse("2006-01-02T15:04:05", s)
		if err != nil {
			t.Fatal(err)
		}
		start := end.Add(-time.Hour)
		// The window is (end-1h, end]: strictly after the start, up to and including
		// the row's own instant.
		ref, err := ursus.Scan(src).
			Filter(ursus.Col("ts").Gt(start), ursus.Col("ts").Le(end)).
			Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		n, _, _ := got.At[uint64](i, "n")
		if int(n) != ref.Height() {
			t.Errorf("row %d (%s): rolling n=%d, filter kept %d", i, s, n, ref.Height())
		}
	}
}

// TestTemporalGroupRefusesUnsortedIndex: windows are contiguous ranges of a sorted
// index, so an unsorted one gives plausible wrong groups. It is checked rather than
// asserted — an unchecked hint is a user claim that deletes a correctness check.
func TestTemporalGroupRefusesUnsortedIndex(t *testing.T) {
	unsorted := hourly(t, "2024-01-01T02:00:00", "2024-01-01T00:00:00")
	_, err := unsorted.
		GroupByDynamic(ursus.Col("ts"), ursus.DynamicOptions{Every: ursus.Every("1h")}).
		Agg(ursus.Len()).Collect(t.Context())
	if err == nil {
		t.Fatal("an unsorted index was accepted")
	}
	if !strings.Contains(err.Error(), "not sorted") {
		t.Errorf("the message should say the index is not sorted:\n%v", err)
	}
	// And sorting first fixes it, so the refusal is actionable.
	if _, err := unsorted.Sort(ursus.Asc(ursus.Col("ts"))).
		GroupByDynamic(ursus.Col("ts"), ursus.DynamicOptions{Every: ursus.Every("1h")}).
		Agg(ursus.Len()).Collect(t.Context()); err != nil {
		t.Errorf("sorting first should work: %v", err)
	}
}

// TestRefusedDynamicOptionsAreRefused: the three unimplemented fields produce an
// error naming themselves rather than being silently dropped.
func TestRefusedDynamicOptionsAreRefused(t *testing.T) {
	for _, c := range []struct {
		name string
		o    ursus.DynamicOptions
	}{
		{"Label", ursus.DynamicOptions{Every: ursus.Every("1h"), Label: "right"}},
		{"StartBy", ursus.DynamicOptions{Every: ursus.Every("1h"), StartBy: "monday"}},
		{"IncludeBoundaries", ursus.DynamicOptions{
			Every: ursus.Every("1h"), IncludeBoundaries: true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := hourly(t, "2024-01-01T00:00:00").
				GroupByDynamic(ursus.Col("ts"), c.o).
				Agg(ursus.Len()).Collect(t.Context())
			if err == nil {
				t.Fatalf("%s was accepted and ignored", c.name)
			}
			if !errors.Is(err, uerr.ErrUnsupported) {
				t.Errorf("want an unsupported error, got %v", err)
			}
			if !strings.Contains(err.Error(), c.name) {
				t.Errorf("the message must name %s:\n%v", c.name, err)
			}
		})
	}
}

// TestTemporalGroupWithCategoricalKeys: windows are cut independently inside each
// distinct key combination.
func TestTemporalGroupWithCategoricalKeys(t *testing.T) {
	src := tsFrame(t, "UTC", dtype.Micro,
		"2024-01-01T00:10:00", "2024-01-01T00:20:00",
		"2024-01-01T01:10:00", "2024-01-01T01:20:00",
	)
	df, err := ursus.Scan(src).
		WithColumns(ursus.Col("v").Mod(int64(2)).Alias("g")).
		GroupByDynamic(ursus.Col("ts"), ursus.DynamicOptions{
			Every:   ursus.Every("1h"),
			GroupBy: []ursus.Expr{ursus.Col("g")},
		}).Agg(ursus.Len().Alias("n")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Two groups, each spanning two hours, so four windows and one row each.
	if df.Height() != 4 {
		t.Fatalf("%d windows, want 4 (2 groups x 2 hours)\n%s", df.Height(), df)
	}
	for i := range df.Height() {
		if n, _, _ := df.At[uint64](i, "n"); n != 1 {
			t.Errorf("window %d has n=%d, want 1", i, n)
		}
	}
}

// TestAllNineteenAggregatesInAWindow is the payoff of expanding windows into (row,
// group) pairs and feeding the ORDINARY accumulators: every aggregate works here on
// the day the operator lands, with no second kernel family.
func TestAllNineteenAggregatesInAWindow(t *testing.T) {
	src := tsFrame(t, "UTC", dtype.Micro,
		"2024-01-01T00:05:00", "2024-01-01T00:15:00", "2024-01-01T00:25:00",
		"2024-01-01T01:05:00", "2024-01-01T01:15:00",
	)
	df, err := ursus.Scan(src).
		WithColumns(
			ursus.Col("v").Alias("seq"),
			ursus.Col("v").Alias("opt"),
			ursus.Lit("x").Alias("pad"),
		).
		GroupByDynamic(ursus.Col("ts"), ursus.DynamicOptions{Every: ursus.Every("1h")}).
		Agg(allNineteen()...).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 2 {
		t.Fatalf("%d windows, want 2", df.Height())
	}
	if df.Width() != 1+19 {
		t.Fatalf("%d columns, want 20 (the index plus nineteen aggregates)", df.Width())
	}
}

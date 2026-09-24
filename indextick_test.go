package ursus_test

// A window interval the index column cannot represent.
//
// Nothing relates the window interval to the index's RESOLUTION. TemporalGroup.Schema
// checks that the index is temporal and stops; the resolver checks each interval's
// Err(), that every and period are positive, and that every has one component. The
// index type and the interval never meet.
//
// So a Date index — int32 days — accepts Every("1h") and asks for 24 windows per day,
// and dtype.FromTime floors every one of them to the same day number. What comes out
// depends on the Closed convention, and all four are wrong in different ways.
//
// # The invariant is DISTINCT STARTS, not coverage
//
// Coverage alone does not catch the default: under ClosedLeft every row still lands
// in some window, because the last window of each day is the one whose upper edge
// crosses into the next day. What is wrong there is the grid itself — a group-by
// returning twenty-four rows all labelled 2024-01-01, twenty-three of them empty.
//
// So the property asserted is that a grid has one row per window START. That holds
// for overlapping windows too, since Period changes a window's width and not where it
// begins.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// dateIdx is three rows over two days: 19723 is 2024-01-01.
func dateIdx(t *testing.T) *ursus.LazyFrame {
	t.Helper()
	return ursus.Scan(keyedFrame(t, "v", dtype.Date, 19723, 19723, 19724)).
		Select(ursus.Col("ts"), ursus.Col("v"))
}

// gridShape reports the output height, how many DISTINCT window starts it has, and
// how many input rows it accounts for.
func gridShape(t *testing.T, df *ursus.DataFrame) (height, distinct, covered int) {
	t.Helper()
	n, err := df.Column[uint64]("n")
	if err != nil {
		t.Fatal(err)
	}
	starts, err := df.Column[int32]("ts")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int32]bool{}
	for i := range df.Height() {
		v, _ := n.Get(i)
		covered += int(v)
		s, _ := starts.Get(i)
		seen[s] = true
	}
	return df.Height(), len(seen), covered
}

// assertFinerThanIndex fails unless err is the resolution refusal, and not some
// other error that happens to be non-nil.
func assertFinerThanIndex(t *testing.T, err error, field string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s finer than the index must be refused", field)
	}
	var e *uerr.Error
	if !errors.As(err, &e) || !errors.Is(&uerr.Error{Kind: e.Kind}, uerr.ErrType) {
		t.Fatalf("want a KindType refusal, got %v", err)
	}
	if errors.Is(err, uerr.ErrInternal) {
		t.Fatalf("a user-facing refusal, not a reported ursus bug: %v", err)
	}
	for _, want := range []string{field, "finer than the index", "Date"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should name %q: %v", want, err)
		}
	}
}

func TestGridHasOneRowPerWindowStart(t *testing.T) {
	for _, c := range []struct {
		name   string
		every  string
		offset string
		closed ursus.Closed
		finer  bool // finer than the Date index's one-day tick
	}{
		{"1d/left", "1d", "", ursus.ClosedLeft, false},
		{"1mo/left", "1mo", "", ursus.ClosedLeft, false},
		{"1h/left", "1h", "", ursus.ClosedLeft, true},
		{"1h/both", "1h", "", ursus.ClosedBoth, true},
		{"1h/none", "1h", "", ursus.ClosedNone, true},
		{"1m/left", "1m", "", ursus.ClosedLeft, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			opts := ursus.DynamicOptions{Every: ursus.Every(c.every), Closed: c.closed}
			if c.offset != "" {
				opts.Offset = ursus.Every(c.offset)
			}
			df, err := dateIdx(t).GroupByDynamic(ursus.Col("ts"), opts).
				Agg(ursus.Len().Alias("n")).Collect(t.Context())

			if c.finer {
				assertFinerThanIndex(t, err, "every")
				return
			}
			if err != nil {
				t.Fatalf("%v", err)
			}
			height, distinct, covered := gridShape(t, df)
			if height != distinct {
				t.Errorf("%d rows over %d distinct window starts; a grid has one "+
					"row per start", height, distinct)
			}
			if covered != 3 {
				t.Errorf("covered %d of 3 rows", covered)
			}
		})
	}
}

// TestOffsetFinerThanTheIndexShiftsByAWholeTick records the mildest member of the
// family, and the one the sweep above cannot state.
//
// Offset("9h") on a Date index is not inert, which is what I first assumed: the
// offset pushes the first window past the first row, the back-step then steps a
// whole DAY rather than nine hours, and the grid gains an empty leading window. The
// data lands in the same buckets either way, so no coverage or distinct-start
// assertion can see it — what is wrong is that a nine-hour shift was applied as a
// twenty-four hour one, and only the window count shows it.
func TestOffsetFinerThanTheIndexShiftsByAWholeTick(t *testing.T) {
	run := func(offset string) (int, error) {
		opts := ursus.DynamicOptions{Every: ursus.Every("1d")}
		if offset != "" {
			opts.Offset = ursus.Every(offset)
		}
		df, err := dateIdx(t).GroupByDynamic(ursus.Col("ts"), opts).
			Agg(ursus.Len().Alias("n")).Collect(t.Context())
		if err != nil {
			return 0, err
		}
		return df.Height(), nil
	}
	heights := func(offset string) int {
		h, err := run(offset)
		if err != nil {
			t.Fatalf("offset=%q: %v", offset, err)
		}
		return h
	}
	offsetErr := func(offset string) error {
		_, err := run(offset)
		return err
	}
	if plain := heights(""); plain != 2 {
		t.Errorf("no offset gives %d windows, want 2", plain)
	}
	assertFinerThanIndex(t, offsetErr("9h"), "offset")
}

// TestRollingPeriodFinerThanTheIndexIsRefused is the case the grid fixtures cannot
// reach: Rolling never consults Every, so only Period can be too fine — and the
// failure is not an empty window or a duplicated one, it is a window of the WRONG
// WIDTH, which looks entirely ordinary.
func TestRollingPeriodFinerThanTheIndexIsRefused(t *testing.T) {
	run := func(period string) ([]uint64, error) {
		df, err := dateIdx(t).
			Rolling(ursus.Col("ts"), ursus.RollingOptions{Period: ursus.Every(period)}).
			Agg(ursus.Len().Alias("n")).Collect(t.Context())
		if err != nil {
			return nil, err
		}
		s, err := df.Column[uint64]("n")
		if err != nil {
			return nil, err
		}
		out := make([]uint64, df.Height())
		for i := range df.Height() {
			out[i], _ = s.Get(i)
		}
		return out, nil
	}
	counts := func(period string) []uint64 {
		out, err := run(period)
		if err != nil {
			t.Fatalf("period=%s: %v", period, err)
		}
		return out
	}
	countsErr := func(period string) error {
		_, err := run(period)
		return err
	}

	// A day is the index's own tick and is legal.
	if got := counts("1d"); len(got) != 3 {
		t.Errorf("period=1d gives %v, want three rows", got)
	}
	// An hour used to give [2 2 1] — the same answer as a day, because it floored to
	// the same boundary. A window of the wrong WIDTH looks entirely ordinary, which
	// is why this one needed a refusal rather than an assertion about its output.
	assertFinerThanIndex(t, countsErr("1h"), "period")
}

// TestDurationIndexIsRefusedAtPlanTime. IsTemporal() admits a Duration, so the plan
// used to resolve, buffer the whole input, and only then fail — with an INTERNAL
// error telling the user to report a bug in ursus for an ordinary type mistake.
func TestDurationIndexIsRefusedAtPlanTime(t *testing.T) {
	src := keyedFrame(t, "v", dtype.Duration(dtype.Nano), 0, 1, 2)
	_, err := ursus.Scan(src).Select(ursus.Col("ts"), ursus.Col("v")).
		GroupByDynamic(ursus.Col("ts"), ursus.DynamicOptions{Every: ursus.Every("1s")}).
		Agg(ursus.Len().Alias("n")).Collect(t.Context())
	if err == nil {
		t.Fatal("a Duration is not an instant and cannot be a window index")
	}
	var e *uerr.Error
	if !errors.As(err, &e) || !errors.Is(&uerr.Error{Kind: e.Kind}, uerr.ErrType) {
		t.Fatalf("want a KindType refusal, got %v", err)
	}
	if errors.Is(err, uerr.ErrInternal) {
		t.Error("an ordinary type mistake must not ask the user to report a bug")
	}
	if !strings.Contains(err.Error(), "span rather than an instant") {
		t.Errorf("the refusal should say why a Duration cannot index: %v", err)
	}
}

// TestMixedIntervalIsCheckedOnItsSubDayPart. A pure calendar interval carries no
// nanoseconds, so the finer-than-tick comparison skips it without needing an
// IsCalendar exclusion — a mixed one does not, and is checked on the part the index
// cannot represent. Twelve hours is as unrepresentable on a Date index beside a
// month as it is alone.
func TestMixedIntervalIsCheckedOnItsSubDayPart(t *testing.T) {
	// Pure calendar: legal, and the grid is one window.
	df, err := dateIdx(t).GroupByDynamic(ursus.Col("ts"),
		ursus.DynamicOptions{Every: ursus.Every("1mo")}).
		Agg(ursus.Len().Alias("n")).Collect(t.Context())
	if err != nil {
		t.Fatalf("a month is coarser than a day and must be legal: %v", err)
	}
	if df.Height() != 1 {
		t.Errorf("every=1mo gives %d windows, want 1", df.Height())
	}

	// Mixed, with a sub-day part the index cannot hold.
	_, err = dateIdx(t).GroupByDynamic(ursus.Col("ts"),
		ursus.DynamicOptions{Every: ursus.Every("1d"), Offset: ursus.Every("1mo12h")}).
		Agg(ursus.Len().Alias("n")).Collect(t.Context())
	assertFinerThanIndex(t, err, "offset")
}

// TestCalendarIntervalOnATimeIndexIsRefused is the family's other direction: not an
// interval too fine for the index, but one whose unit the index has no way to
// measure. A Time is a wall clock with no date, so every row truncates to the same
// 1970-01-01 and one window spans the column.
//
// It is truncateOut's refusal for a different operator, in the same words — "a Time
// has no date, so it cannot be floored to a day or a month" — which is why both
// should keep saying it the same way.
func TestCalendarIntervalOnATimeIndexIsRefused(t *testing.T) {
	// Ticks inside [0, 24h), which is what a Time column holds.
	const hour = int64(time.Hour)
	src := keyedFrame(t, "v", dtype.Time(dtype.Nano), hour, 2*hour, 3*hour)
	lf := ursus.Scan(src).Select(ursus.Col("ts"), ursus.Col("v"))

	for _, every := range []string{"1mo", "1d", "1y"} {
		_, err := lf.GroupByDynamic(ursus.Col("ts"),
			ursus.DynamicOptions{Every: ursus.Every(every)}).
			Agg(ursus.Len().Alias("n")).Collect(t.Context())
		if err == nil {
			t.Errorf("every=%s on a Time index must be refused", every)
			continue
		}
		var e *uerr.Error
		if !errors.As(err, &e) || !errors.Is(&uerr.Error{Kind: e.Kind}, uerr.ErrType) {
			t.Errorf("every=%s: want a KindType refusal, got %v", every, err)
		}
		if !strings.Contains(err.Error(), "a Time has no date") {
			t.Errorf("every=%s: the refusal should say why: %v", every, err)
		}
	}

	// A sub-day interval on the same column is fine, which is exactly why the
	// index's type alone could never decide this.
	if _, err := lf.GroupByDynamic(ursus.Col("ts"),
		ursus.DynamicOptions{Every: ursus.Every("1h")}).
		Agg(ursus.Len().Alias("n")).Collect(t.Context()); err != nil {
		t.Errorf("an hour on a Time(ns) index is representable and must be legal: %v", err)
	}
}

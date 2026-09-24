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
	"testing"

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

// knownTickMismatches names each combination that answers wrongly today, with what
// it answers. Emptied by the commit that refuses an interval finer than its index.
var knownTickMismatches = map[string]string{
	"1h/left": "48 rows over 2 distinct days, 23 of every 24 empty",
	"1h/both": "48 rows, and 73 row-slots for 3 rows — each counted ~24 times",
	"1h/none": "49 rows and 0 of 3 rows covered — every row dropped",
	"1m/left": "2880 rows over 2 distinct days",
}

func TestGridHasOneRowPerWindowStart(t *testing.T) {
	for _, c := range []struct {
		name   string
		every  string
		offset string
		closed ursus.Closed
	}{
		{"1d/left", "1d", "", ursus.ClosedLeft},
		{"1mo/left", "1mo", "", ursus.ClosedLeft},
		{"1h/left", "1h", "", ursus.ClosedLeft},
		{"1h/both", "1h", "", ursus.ClosedBoth},
		{"1h/none", "1h", "", ursus.ClosedNone},
		{"1m/left", "1m", "", ursus.ClosedLeft},
	} {
		t.Run(c.name, func(t *testing.T) {
			opts := ursus.DynamicOptions{Every: ursus.Every(c.every), Closed: c.closed}
			if c.offset != "" {
				opts.Offset = ursus.Every(c.offset)
			}
			df, err := dateIdx(t).GroupByDynamic(ursus.Col("ts"), opts).
				Agg(ursus.Len().Alias("n")).Collect(t.Context())
			if err != nil {
				t.Fatalf("%v", err)
			}
			height, distinct, covered := gridShape(t, df)
			why, bad := knownTickMismatches[c.name]

			if bad {
				if height == distinct && covered == 3 {
					t.Errorf("answers correctly now — delete the entry (%s)", why)
				}
				return
			}
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
	heights := func(offset string) int {
		opts := ursus.DynamicOptions{Every: ursus.Every("1d")}
		if offset != "" {
			opts.Offset = ursus.Every(offset)
		}
		df, err := dateIdx(t).GroupByDynamic(ursus.Col("ts"), opts).
			Agg(ursus.Len().Alias("n")).Collect(t.Context())
		if err != nil {
			t.Fatalf("offset=%q: %v", offset, err)
		}
		return df.Height()
	}
	if plain, shifted := heights(""), heights("9h"); plain != 2 || shifted != 3 {
		t.Errorf("no offset gives %d windows and a 9h offset gives %d; the defect "+
			"is 2 and 3 — a sub-day offset moving the grid by a whole day",
			plain, shifted)
	}
}

// TestRollingPeriodFinerThanTheIndexIsCoarsened is the case the grid fixtures cannot
// reach: Rolling never consults Every, so only Period can be too fine — and the
// failure is not an empty window or a duplicated one, it is a window of the WRONG
// WIDTH, which looks entirely ordinary.
func TestRollingPeriodFinerThanTheIndexIsCoarsened(t *testing.T) {
	counts := func(period string) []uint64 {
		df, err := dateIdx(t).
			Rolling(ursus.Col("ts"), ursus.RollingOptions{Period: ursus.Every(period)}).
			Agg(ursus.Len().Alias("n")).Collect(t.Context())
		if err != nil {
			t.Fatalf("period=%s: %v", period, err)
		}
		s, err := df.Column[uint64]("n")
		if err != nil {
			t.Fatal(err)
		}
		out := make([]uint64, df.Height())
		for i := range df.Height() {
			out[i], _ = s.Get(i)
		}
		return out
	}

	day, hour := counts("1d"), counts("1h")
	// The defect: an hour and a day give the same answer, because an hour floors to
	// the same day boundary. [2 2 1] either way.
	if len(day) != len(hour) {
		t.Fatalf("different heights: %v and %v", day, hour)
	}
	same := true
	for i := range day {
		if day[i] != hour[i] {
			same = false
		}
	}
	if !same {
		t.Errorf("period=1h gives %v and period=1d gives %v — they differ now, so "+
			"the hour is no longer being floored to a day", hour, day)
	}
}

// TestDurationIndexIsAnInternalError: IsTemporal() admits a Duration, so the plan
// resolves, the whole input is buffered, and then ToTime refuses it with an internal
// error telling the user to report a bug in ursus.
func TestDurationIndexIsAnInternalError(t *testing.T) {
	src := keyedFrame(t, "v", dtype.Duration(dtype.Nano), 0, 1, 2)
	_, err := ursus.Scan(src).Select(ursus.Col("ts"), ursus.Col("v")).
		GroupByDynamic(ursus.Col("ts"), ursus.DynamicOptions{Every: ursus.Every("1s")}).
		Agg(ursus.Len().Alias("n")).Collect(t.Context())
	if err == nil {
		t.Fatal("a Duration is not an instant and cannot be a window index")
	}
	if !errors.Is(err, uerr.ErrInternal) {
		t.Errorf("the defect is that this is an INTERNAL error; got %v", err)
	}
}

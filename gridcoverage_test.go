package ursus_test

// Every row of a dynamic group-by lands in some window, or it is gone.
//
// Finish pushes from windows to rows rather than the reverse: it walks the grid and
// appends the rows each window covers. A row index no window covers is never
// appended, never reaches an accumulator, and never influences a cell. Nothing
// compares the number of covered rows against the input height.
//
// So a grid that stops early does not fail — it answers, with fewer rows in it. The
// operator already refuses a null index and an unsorted index, and both doc comments
// give the reason that applies here word for word: "Dropping it would change the row
// count with no error."
//
// # These cases assert the DEFECT
//
// knownGridDrops lists every combination that loses rows today, measured. The commit
// that refuses an oversized grid empties it: a listed case that starts covering
// everything, or starts being refused, fails as stale.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

// evenlySpaced builds n rows spanning exactly span, which is the cheapest fixture
// that can reach a large grid: the window count comes from span/every, not from the
// row count, so two hundred rows can ask for ten thousand windows.
func evenlySpaced(t *testing.T, n int, span time.Duration) *ursus.LazyFrame {
	t.Helper()
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	ts := make([]time.Time, n)
	vs := make([]int64, n)
	for i := range n {
		ts[i] = t0.Add(time.Duration(i) * (span / time.Duration(n-1)))
		vs[i] = 1
	}
	return ursus.Frame(ursus.Values("ts", ts), ursus.Values("v", vs))
}

// gridCoverage runs one dynamic group-by and reports how many input rows survived.
func gridCoverage(t *testing.T, lf *ursus.LazyFrame, opts ursus.DynamicOptions) (int, int) {
	t.Helper()
	df, err := lf.GroupByDynamic(ursus.Col("ts"), opts).
		Agg(ursus.Len().Alias("n")).Collect(t.Context())
	if err != nil {
		t.Fatalf("%v", err)
	}
	s, err := df.Column[uint64]("n")
	if err != nil {
		t.Fatal(err)
	}
	covered := 0
	for i := range df.Height() {
		v, _ := s.Get(i)
		covered += int(v)
	}
	return df.Height(), covered
}

// knownGridDrops names each case that loses rows today, with what it loses.
// Empty since the grid refuses rather than truncating. The three entries were
// every=1s offset=2h (142 of 200), every=1s offset=10h and every=1m offset=100h
// (0 of 200 each), measured before the repair; see step-67-as-built.md.
var knownGridDrops = map[string]string{}

func TestGridCoversEveryRow(t *testing.T) {
	const rows = 200
	const span = 3 * time.Hour

	for _, c := range []struct {
		name          string
		every, offset string
		period        string
	}{
		{"every=1h", "1h", "", ""},
		{"every=1m", "1m", "", ""},
		{"every=1s", "1s", "", ""},
		// An offset inside the old back-step cap: 1800 steps of 4096.
		{"every=1s offset=30m", "1s", "30m", ""},
		// And past it: 7200 steps wanted.
		{"every=1s offset=2h", "1s", "2h", ""},
		// Far enough past that the grid starts after the data ends.
		{"every=1s offset=10h", "1s", "10h", ""},
		{"every=1m offset=100h", "1m", "100h", ""},
		// A negative offset walks forward instead, so the back-step never runs.
		{"every=1m offset=-30m", "1m", "-30m", ""},
		// Overlapping windows cover each row more than once, so coverage is a
		// LOWER bound there — asserted separately below.
		{"every=1h period=2h", "1h", "", "2h"},
	} {
		t.Run(c.name, func(t *testing.T) {
			opts := ursus.DynamicOptions{Every: ursus.Every(c.every)}
			if c.offset != "" {
				opts.Offset = ursus.Every(c.offset)
			}
			overlapping := c.period != ""
			if overlapping {
				opts.Period = ursus.Every(c.period)
			}

			_, covered := gridCoverage(t, evenlySpaced(t, rows, span), opts)
			why, dropped := knownGridDrops[c.name]

			switch {
			case overlapping:
				if covered < rows {
					t.Errorf("covered %d of %d; overlapping windows cannot cover "+
						"FEWER rows than the grid does", covered, rows)
				}
			case dropped:
				if covered == rows {
					t.Errorf("covers all %d rows now — delete the knownGridDrops "+
						"entry (%s)", rows, why)
				}
			case covered != rows:
				t.Errorf("covered %d of %d rows; every row is inside the index "+
					"range, so every row belongs to some window", covered, rows)
			}
		})
	}
}

// TestGridTooLargeIsRefused: a grid the operator cannot build is an error, not a
// prefix of itself.
//
// Both shapes are CHEAP to refuse, which is the point of predicting the count
// instead of discovering it. Two rows a year apart with a one-second every want
// 31.5 million windows; at 24 bytes each that is 756 MiB of grid derived from two
// rows of input, and the old code built 16.7 million of them and returned.
func TestGridTooLargeIsRefused(t *testing.T) {
	lo := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	frame := func(hi time.Time) *ursus.LazyFrame {
		return ursus.Frame(
			ursus.Values("ts", []time.Time{lo, hi}),
			ursus.Values("v", []int64{1, 2}))
	}

	for _, c := range []struct {
		name   string
		hi     time.Time
		every  string
		offset string
	}{
		{"a year of seconds", lo.AddDate(1, 0, 0), "1s", ""},
		{"a day of microseconds", lo.AddDate(0, 0, 1), "1us", ""},
		// The grid walks forward from a negative offset, so this one never touches
		// the back-step and reaches the ceiling on the forward walk alone.
		{"a negative offset of a year", lo.Add(time.Hour), "1s", "-1y"},
	} {
		t.Run(c.name, func(t *testing.T) {
			opts := ursus.DynamicOptions{Every: ursus.Every(c.every)}
			if c.offset != "" {
				opts.Offset = ursus.Every(c.offset)
			}
			_, err := frame(c.hi).GroupByDynamic(ursus.Col("ts"), opts).
				Agg(ursus.Len().Alias("n")).Collect(t.Context())

			if err == nil {
				t.Fatal("a grid of tens of millions of windows must be refused")
			}
			if !errors.Is(err, ursus.ErrResource) {
				t.Errorf("want ErrResource so a caller can tell this from a bad "+
					"query, got %v", err)
			}
			// The message must be about the GRID, not the input — the input is two
			// rows, and "buffers its whole input" would be a false diagnosis.
			if !strings.Contains(err.Error(), "window grid") {
				t.Errorf("the refusal should name the grid: %v", err)
			}
			if strings.Contains(err.Error(), "buffers its whole input") {
				t.Errorf("the refusal blames the input, which is two rows: %v", err)
			}
		})
	}
}

// TestBackStepRefusesRatherThanGivingUp pins the other exit. An offset that needs
// more steps than the ceiling allows is refused; one that needs fewer is not, and
// the second half is what stops the fix from being "refuse everything".
func TestBackStepRefusesRatherThanGivingUp(t *testing.T) {
	lf := evenlySpaced(t, 200, 3*time.Hour)

	// 7200 steps: past the old 4096 cap, far inside the new ceiling, and must work.
	if _, covered := gridCoverage(t, lf, ursus.DynamicOptions{
		Every: ursus.Every("1s"), Offset: ursus.Every("2h"),
	}); covered != 200 {
		t.Errorf("covered %d of 200 with a 7200-step offset", covered)
	}

	// Past the ceiling: refused rather than truncated.
	_, err := lf.GroupByDynamic(ursus.Col("ts"), ursus.DynamicOptions{
		Every: ursus.Every("1ns"), Offset: ursus.Every("1h"),
	}).Agg(ursus.Len().Alias("n")).Collect(t.Context())
	if err == nil {
		t.Fatal("3.6e12 back-steps must be refused")
	}
	if !errors.Is(err, ursus.ErrResource) {
		t.Errorf("want ErrResource, got %v", err)
	}
}

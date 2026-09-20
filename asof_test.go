package ursus_test

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/source/memsrc"
	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/ursustest"
)

// tickFrame builds a numeric as-of fixture: an Int64 key, a group and a value.
// Numeric rather than temporal so the oracle can do arithmetic without a calendar.
func tickFrame(t testing.TB, name string, keys []int64, grp []string) *ursus.LazyFrame {
	t.Helper()
	vals := make([]int64, len(keys))
	for i := range keys {
		vals[i] = int64(i)
	}
	if grp == nil {
		return ursus.Frame(ursus.Values("k", keys), ursus.Values(name, vals))
	}
	return ursus.Frame(ursus.Values("k", keys), ursus.Values("g", grp),
		ursus.Values(name, vals))
}

// TestAsOfBackwardMatchesAManualScan is the oracle: for each left key, the LAST right
// key at or before it, found by a linear scan that shares no code with the operator.
func TestAsOfBackwardMatchesAManualScan(t *testing.T) {
	lk := []int64{0, 5, 10, 15, 20, 25, 100}
	rk := []int64{3, 5, 9, 16, 16, 21}

	got, err := tickFrame(t, "lv", lk, nil).
		JoinAsOf(tickFrame(t, "rv", rk, nil), ursus.AsOfOn(ursus.Col("k"))).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got.Height() != len(lk) {
		t.Fatalf("%d rows, want %d — an as-of join is a LEFT join", got.Height(), len(lk))
	}

	for i, k := range lk {
		// The oracle: scan every right row, keep the last one at or before k.
		want, found := -1, false
		for j, r := range rk {
			if r <= k {
				want, found = j, true
			}
		}
		v, ok, err := got.At[int64](i, "rv")
		if err != nil {
			t.Fatal(err)
		}
		if ok != found {
			t.Errorf("left key %d: matched=%v, want %v", k, ok, found)
			continue
		}
		// rv is the right row's index, so it identifies which row matched. Ties on
		// the key (16 appears twice) mean the LAST such row, which is what "the most
		// recent" has to mean when several share an instant.
		if found && v != int64(want) {
			t.Errorf("left key %d matched right row %d, want %d", k, v, want)
		}
	}
}

// TestAsOfStrategies: one fixture with right rows before, after and exactly on each
// left key, so the three strategies give three different answers.
func TestAsOfStrategies(t *testing.T) {
	lk := []int64{10, 20, 30}
	rk := []int64{8, 20, 33} // before, exact, after

	for _, c := range []struct {
		strategy ursus.AsOfStrategy
		want     []int64 // right row index per left row, -1 for no match
	}{
		// backward: 10->8(0), 20->20(1), 30->20(1)
		{ursus.AsOfBackward, []int64{0, 1, 1}},
		// forward: 10->20(1), 20->20(1), 30->33(2)
		{ursus.AsOfForward, []int64{1, 1, 2}},
		// nearest: 10->8 (d=2) beats 20 (d=10); 20->20 exact; 30->33 (d=3) beats 20 (d=10)
		{ursus.AsOfNearest, []int64{0, 1, 2}},
	} {
		t.Run(c.strategy.String(), func(t *testing.T) {
			got, err := tickFrame(t, "lv", lk, nil).
				JoinAsOf(tickFrame(t, "rv", rk, nil),
					ursus.AsOfOn(ursus.Col("k")),
					ursus.AsOfStrategyOpt(c.strategy)).
				Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatal(err)
			}
			for i, want := range c.want {
				v, ok, _ := got.At[int64](i, "rv")
				if want < 0 {
					if ok {
						t.Errorf("left %d matched row %d, want no match", lk[i], v)
					}
					continue
				}
				if !ok || v != want {
					t.Errorf("left %d matched %v (ok=%v), want row %d",
						lk[i], v, ok, want)
				}
			}
		})
	}
}

// TestAsOfAllowExactMatches: the flag flips <= to <, and it is only observable on a
// row that sits exactly on a key.
func TestAsOfAllowExactMatches(t *testing.T) {
	lk := []int64{20}
	rk := []int64{10, 20}

	for _, c := range []struct {
		allow bool
		want  int64
	}{
		{true, 1},  // the exact match at 20
		{false, 0}, // strictly before: 10
	} {
		got, err := tickFrame(t, "lv", lk, nil).
			JoinAsOf(tickFrame(t, "rv", rk, nil),
				ursus.AsOfOn(ursus.Col("k")),
				ursus.AsOfAllowExactMatches(c.allow)).
			Collect(t.Context(), ursus.WithVerify())
		if err != nil {
			t.Fatal(err)
		}
		v, ok, _ := got.At[int64](0, "rv")
		if !ok || v != c.want {
			t.Errorf("allowExact=%v matched %v (ok=%v), want row %d",
				c.allow, v, ok, c.want)
		}
	}
}

// TestAsOfByIsolatesGroups: without `by`, every left row searches one global run and
// matches the nearest row of the WRONG instrument — a plausible number and the wrong
// one. This is the test that stops a fixture passing for that reason.
func TestAsOfByIsolatesGroups(t *testing.T) {
	// Left: one row per symbol at key 100.
	left := tickFrame(t, "lv", []int64{100, 100}, []string{"AAA", "BBB"})
	// Right: AAA has a row at 1, BBB at 99. Without `by`, both left rows would take
	// the global nearest (99, the BBB row).
	right := tickFrame(t, "rv", []int64{1, 99}, []string{"AAA", "BBB"})

	got, err := left.JoinAsOf(right,
		ursus.AsOfOn(ursus.Col("k")), ursus.AsOfBy(ursus.Col("g"))).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	// AAA must match right row 0, BBB right row 1 — not both matching row 1.
	for i, want := range []int64{0, 1} {
		v, ok, _ := got.At[int64](i, "rv")
		if !ok || v != want {
			t.Errorf("row %d matched %v (ok=%v), want right row %d — a match leaked "+
				"across groups", i, v, ok, want)
		}
	}
}

// TestAsOfEmitsEveryLeftRow: it is a LEFT join, so the height is the left height
// whatever the strategy or tolerance does.
func TestAsOfEmitsEveryLeftRow(t *testing.T) {
	left := tickFrame(t, "lv", []int64{1, 2, 3, 4, 5}, nil)
	for _, c := range []struct {
		name  string
		right *ursus.LazyFrame
		opts  []ursus.AsOfOption
	}{
		{"nothing matches", tickFrame(t, "rv", []int64{100}, nil),
			[]ursus.AsOfOption{ursus.AsOfOn(ursus.Col("k"))}},
		{"empty right", tickFrame(t, "rv", nil, nil),
			[]ursus.AsOfOption{ursus.AsOfOn(ursus.Col("k"))}},
		{"forward past the end", tickFrame(t, "rv", []int64{0}, nil),
			[]ursus.AsOfOption{ursus.AsOfOn(ursus.Col("k")),
				ursus.AsOfStrategyOpt(ursus.AsOfForward)}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := left.JoinAsOf(c.right, c.opts...).
				Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatal(err)
			}
			if got.Height() != 5 {
				t.Errorf("%d rows, want 5 — every left row must survive", got.Height())
			}
		})
	}
}

// TestAsOfRefusesUnsortedInput on either side, naming which.
func TestAsOfRefusesUnsortedInput(t *testing.T) {
	sorted := tickFrame(t, "rv", []int64{1, 2, 3}, nil)
	unsorted := tickFrame(t, "lv", []int64{3, 1}, nil)

	for _, c := range []struct {
		name        string
		left, right *ursus.LazyFrame
		side        string
	}{
		{"left", unsorted, sorted, "left"},
		{"right", tickFrame(t, "lv", []int64{1, 2}, nil),
			tickFrame(t, "rv", []int64{3, 1}, nil), "right"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.left.JoinAsOf(c.right, ursus.AsOfOn(ursus.Col("k"))).
				Collect(t.Context())
			if err == nil {
				t.Fatal("an unsorted input was accepted")
			}
			if !strings.Contains(err.Error(), "not sorted") ||
				!strings.Contains(err.Error(), c.side) {
				t.Errorf("the message should name the %s side as unsorted:\n%v",
					c.side, err)
			}
		})
	}
}

// TestAsOfOptionErrors: the combinations that cannot mean anything are refused up
// front, not silently resolved.
func TestAsOfOptionErrors(t *testing.T) {
	l := tickFrame(t, "lv", []int64{1}, nil)
	r := tickFrame(t, "rv", []int64{1}, nil)
	for _, c := range []struct {
		name string
		opts []ursus.AsOfOption
	}{
		{"no key", nil},
		{"on plus left_on", []ursus.AsOfOption{
			ursus.AsOfOn(ursus.Col("k")), ursus.AsOfLeftOn(ursus.Col("k"))}},
		{"left_on without right_on", []ursus.AsOfOption{ursus.AsOfLeftOn(ursus.Col("k"))}},
		{"by plus left_by", []ursus.AsOfOption{
			ursus.AsOfOn(ursus.Col("k")), ursus.AsOfBy(ursus.Col("g")),
			ursus.AsOfLeftBy(ursus.Col("g"))}},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := l.JoinAsOf(r, c.opts...).Collect(t.Context())
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if errors.Is(err, uerr.ErrInternal) {
				t.Errorf("a bad option is not an ursus bug: %v", err)
			}
		})
	}
}

// TestAsOfToleranceBounds: a candidate further than the tolerance does not match, and
// the left row comes back null-padded rather than missing.
func TestAsOfToleranceBounds(t *testing.T) {
	// Quotes at 00:00 and 00:50; trades at 00:10 and 01:30.
	trades := tsFrame(t, "UTC", dtype.Micro, "2024-01-01T00:10:00", "2024-01-01T01:30:00")
	quotes := tsFrame(t, "UTC", dtype.Micro, "2024-01-01T00:00:00", "2024-01-01T00:50:00")

	got, err := ursus.Scan(trades).Select(ursus.Col("ts"), ursus.Col("v").Alias("tv")).
		JoinAsOf(ursus.Scan(quotes).Select(ursus.Col("ts"), ursus.Col("v").Alias("qv")),
			ursus.AsOfOn(ursus.Col("ts")),
			ursus.AsOfTolerance(ursus.Every("30m"))).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got.Height() != 2 {
		t.Fatalf("%d rows, want 2", got.Height())
	}
	// 00:10 is 10 minutes after the 00:00 quote — inside 30m.
	if v, ok, _ := got.At[int64](0, "qv"); !ok || v != 0 {
		t.Errorf("00:10 matched %v (ok=%v), want quote 0", v, ok)
	}
	// 01:30 is 40 minutes after the 00:50 quote — outside 30m.
	if _, ok, _ := got.At[int64](1, "qv"); ok {
		t.Error("01:30 matched a quote 40 minutes earlier, outside a 30m tolerance")
	}
}

// TestAsOfCalendarToleranceIsNotFixed: "1mo" is a different distance in a 29-day
// February than a fixed 30 days would be — which is the whole reason Interval has
// three fields rather than a nanosecond count.
func TestAsOfCalendarToleranceIsNotFixed(t *testing.T) {
	// A trade on 1 March, a quote on 1 February. That gap is 29 days in 2024.
	trades := tsFrame(t, "UTC", dtype.Micro, "2024-03-01T00:00:00")
	quotes := tsFrame(t, "UTC", dtype.Micro, "2024-02-01T00:00:00")

	match := func(tol ursus.Interval) bool {
		t.Helper()
		got, err := ursus.Scan(trades).Select(ursus.Col("ts"), ursus.Col("v").Alias("tv")).
			JoinAsOf(ursus.Scan(quotes).Select(ursus.Col("ts"), ursus.Col("v").Alias("qv")),
				ursus.AsOfOn(ursus.Col("ts")), ursus.AsOfTolerance(tol)).
			Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		_, ok, _ := got.At[int64](0, "qv")
		return ok
	}
	// One calendar month reaches back to 1 February exactly, so it matches.
	if !match(ursus.Every("1mo")) {
		t.Error(`Every("1mo") did not reach from 1 March to 1 February`)
	}
	// 28 fixed days does not reach 29 days back.
	if match(ursus.Every("28d")) {
		t.Error(`Every("28d") reached 29 days back`)
	}
}

// TestAsOfPromotesTemporalUnits is the canonical case, and it failed at PLAN time
// before step 15: trades and quotes written by different systems at different
// resolutions. dtype.Promote refused every non-identical temporal pair, so
// plan.keyTypes could not find a common type for the join key.
func TestAsOfPromotesTemporalUnits(t *testing.T) {
	trades := tsFrame(t, "UTC", dtype.Micro, "2024-01-01T00:10:00", "2024-01-01T00:30:00")
	quotes := tsFrame(t, "UTC", dtype.Nano, "2024-01-01T00:00:00", "2024-01-01T00:20:00")

	got, err := ursus.Scan(trades).Select(ursus.Col("ts"), ursus.Col("v").Alias("tv")).
		JoinAsOf(ursus.Scan(quotes).Select(ursus.Col("ts"), ursus.Col("v").Alias("qv")),
			ursus.AsOfOn(ursus.Col("ts"))).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("a us/ns as-of join must work: %v", err)
	}
	if got.Height() != 2 {
		t.Fatalf("%d rows, want 2", got.Height())
	}
	for i, want := range []int64{0, 1} {
		if v, ok, _ := got.At[int64](i, "qv"); !ok || v != want {
			t.Errorf("trade %d matched %v (ok=%v), want quote %d", i, v, ok, want)
		}
	}
}

// TestAsOfToleranceRefusesNumericKeys: an Interval measures time, and on a numeric
// key there is no unit to measure in. Refusing beats reinterpreting nanoseconds as a
// count of whatever the key happens to be.
func TestAsOfToleranceRefusesNumericKeys(t *testing.T) {
	l := tickFrame(t, "lv", []int64{1}, nil)
	r := tickFrame(t, "rv", []int64{1}, nil)
	_, err := l.JoinAsOf(r, ursus.AsOfOn(ursus.Col("k")),
		ursus.AsOfTolerance(ursus.Every("1h"))).Collect(t.Context())
	if err == nil {
		t.Fatal("a tolerance on an Int64 key was accepted")
	}
	if !strings.Contains(err.Error(), "tolerance") {
		t.Errorf("the message should name the tolerance:\n%v", err)
	}
}

// TestAsOfIsAPredicateBarrierOnTheRight: a right-side predicate must NOT be pushed
// below an as-of join.
//
// This is a stronger requirement than for an equi-join and the reason is worth
// stating: removing a right row does not merely delete matches, it changes which row
// is NEAREST — so a left row that matched quote A silently re-points at quote B. The
// answer is different, not smaller, and no schema check can see it.
func TestAsOfIsAPredicateBarrierOnTheRight(t *testing.T) {
	l := tickFrame(t, "lv", []int64{10, 20}, nil)
	r := tickFrame(t, "rv", []int64{1, 9, 19}, nil)

	q := func() *ursus.LazyFrame {
		return l.JoinAsOf(r, ursus.AsOfOn(ursus.Col("k"))).
			Filter(ursus.Col("rv").Lt(int64(2)))
	}
	on, err := q().Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	off, err := q().Collect(t.Context(), ursus.WithOptFlags(plan.NoFlags()))
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, on, off)

	// And the plan really does keep the filter above the join, or the comparison
	// above is only checking that the optimizer did nothing.
	txt, err := q().Explain(t.Context(), ursus.Optimized(true))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(txt, "FILTER") {
		t.Fatalf("no FILTER in the plan:\n%s", txt)
	}
	if idx := strings.Index(txt, "ASOF JOIN"); idx >= 0 &&
		strings.Index(txt, "FILTER") > idx {
		t.Errorf("the filter was pushed below the as-of join:\n%s", txt)
	}
}

// TestMergeSortedPreservesOrder: two sorted runs interleave into one sorted run, no
// row is dropped or duplicated, and ties keep the left row first.
func TestMergeSortedPreservesOrder(t *testing.T) {
	l := ursus.Frame(ursus.Values("k", []int64{1, 3, 5, 5}),
		ursus.Values("side", []string{"L", "L", "L", "L"}))
	r := ursus.Frame(ursus.Values("k", []int64{2, 5, 9}),
		ursus.Values("side", []string{"R", "R", "R"}))

	got, err := l.MergeSorted(r, "k").Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got.Height() != 7 {
		t.Fatalf("%d rows, want 7 — a merge drops nothing", got.Height())
	}
	ks := int64Col(t, got, "k")
	for i := 1; i < len(ks); i++ {
		if ks[i] < ks[i-1] {
			t.Fatalf("the result is not sorted at row %d: %v", i, ks)
		}
	}
	// Ties: the three rows at key 5 must be L, L, R — the left run first, which is
	// what makes the merge stable rather than dependent on the inputs' sizes.
	sides, err := got.Column[string]("side")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"L", "R", "L", "L", "L", "R", "R"}
	for i, w := range want {
		if got := sides.Values()[i]; got != w {
			t.Errorf("row %d side = %s, want %s (full: %v)", i, got, w, sides.Values())
		}
	}
}

// TestMergeSortedRefusesUnsorted and mismatched schemas, each with its own message.
func TestMergeSortedRefusesUnsorted(t *testing.T) {
	sorted := ursus.Frame(ursus.Values("k", []int64{1, 2}))
	unsorted := ursus.Frame(ursus.Values("k", []int64{2, 1}))

	if _, err := sorted.MergeSorted(unsorted, "k").Collect(t.Context()); err == nil {
		t.Error("an unsorted right side was accepted")
	} else if !strings.Contains(err.Error(), "not sorted") {
		t.Errorf("the message should say the side is unsorted:\n%v", err)
	}
	if _, err := unsorted.MergeSorted(sorted, "k").Collect(t.Context()); err == nil {
		t.Error("an unsorted left side was accepted")
	}

	// Different schemas: a merge interleaves rows, so both sides must match exactly.
	other := ursus.Frame(ursus.Values("k", []int64{1}), ursus.Values("x", []int64{1}))
	if _, err := sorted.MergeSorted(other, "k").Collect(t.Context()); err == nil {
		t.Error("mismatched schemas were accepted")
	}
	// And an unknown key.
	if _, err := sorted.MergeSorted(sorted, "nope").Collect(t.Context()); err == nil {
		t.Error("an unknown key column was accepted")
	}
}

// TestAsOfCalendarToleranceNeedsAnInstant. A calendar tolerance needs more than a
// temporal key: it needs a date to count months from. A Duration is a span and a Time
// is a wall clock, and for both the sink cannot build a bound at all — so the
// tolerance went silently inert and every candidate matched.
//
// truncateCalendar refuses the same thing one layer down, in the same words.
func TestAsOfCalendarToleranceNeedsAnInstant(t *testing.T) {
	for _, key := range []dtype.DataType{
		dtype.Duration(dtype.Second),
		dtype.Time(dtype.Nano),
	} {
		t.Run(key.String(), func(t *testing.T) {
			left := keyedFrame(t, "tv", key, 0)
			right := keyedFrame(t, "qv", key, 1)

			_, err := ursus.Scan(left).Select(ursus.Col("ts"), ursus.Col("tv")).
				JoinAsOf(ursus.Scan(right).Select(ursus.Col("ts"), ursus.Col("qv")),
					ursus.AsOfOn(ursus.Col("ts")),
					ursus.AsOfTolerance(ursus.Every("1mo"))).
				Collect(t.Context())
			if err == nil {
				t.Fatalf("a calendar tolerance on a %s key was accepted", key)
			}
			if !strings.Contains(err.Error(), "calendar tolerance") {
				t.Errorf("the refusal should name what it refuses: %v", err)
			}
			if errors.Is(err, uerr.ErrInternal) {
				t.Errorf("a user's option is not an ursus bug: %v", err)
			}

			// A FIXED tolerance on the same key stays legal: it is only the calendar
			// half that needs a date.
			if _, err := ursus.Scan(left).Select(ursus.Col("ts"), ursus.Col("tv")).
				JoinAsOf(ursus.Scan(right).Select(ursus.Col("ts"), ursus.Col("qv")),
					ursus.AsOfOn(ursus.Col("ts")),
					ursus.AsOfTolerance(ursus.Every("24h"))).
				Collect(t.Context()); err != nil {
				t.Errorf("a fixed tolerance on a %s key must still work: %v", key, err)
			}
		})
	}
}

// keyedFrame builds a two-column source at exact tick counts, which tsFrame cannot:
// it parses wall clocks, and these fixtures live at the edges of a type's domain.
func keyedFrame(t *testing.T, val string, dt dtype.DataType, ticks ...int64) *memsrc.Source {
	t.Helper()
	schema := dtype.MustSchema(dtype.NotNull("ts", dt), dtype.NotNull(val, dtype.Int64))
	vs := make([]int64, len(ticks))
	for i := range ticks {
		vs[i] = int64(i)
	}
	var keys *data.Column
	if dt.Physical().ID() == dtype.TypeInt32 {
		days := make([]int32, len(ticks))
		for i, v := range ticks {
			days[i] = int32(v)
		}
		keys = data.NewFixed("ts", dt, days, bitmap.AllSet(len(ticks)))
	} else {
		keys = data.NewFixed("ts", dt, ticks, bitmap.AllSet(len(ticks)))
	}
	b, err := data.NewBatch(schema, []*data.Column{keys,
		data.NewFixed(val, dtype.Int64, vs, bitmap.AllSet(len(ticks)))})
	if err != nil {
		t.Fatal(err)
	}
	src, err := memsrc.New(schema, b)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

// The tolerance used to scale the DATA up to nanoseconds — d*npt, where npt is
// 8.64e13 for a Date — so ordinary keys wrapped into a negative product that
// satisfied every bound. These are the four measured fixtures.

func TestAsOfToleranceDoesNotReachAcrossCenturies(t *testing.T) {
	ns := dtype.Datetime(dtype.Nano, "UTC")
	for _, c := range []struct {
		name        string
		key         dtype.DataType
		left, right int64
		tol         ursus.Interval
	}{
		// 106752 days * 8.64e13 wraps negative: 1970-01-01 against 2262-04-12.
		{"a Date key is measured in days", dtype.Date, 106_752, 0, ursus.Every("1h")},
		// The subtraction itself wraps: the two ends of the nanosecond domain.
		{"the two ends of the nanosecond range", ns, math.MaxInt64, math.MinInt64,
			ursus.FromDuration(1)},
		// -d leaves MinInt64 negative, which satisfies any bound at all.
		{"a difference of exactly MinInt64", ns, 0, math.MinInt64, ursus.FromDuration(1)},
		// The calendar bound cannot be represented, and that used to mean "match".
		{"a calendar bound past the end of the type", ns,
			9_222_422_400_000_000_000, 0, ursus.Every("1mo")},
	} {
		t.Run(c.name, func(t *testing.T) {
			trades := keyedFrame(t, "tv", c.key, c.left)
			quotes := keyedFrame(t, "qv", c.key, c.right)

			got, err := ursus.Scan(trades).Select(ursus.Col("ts"), ursus.Col("tv")).
				JoinAsOf(ursus.Scan(quotes).Select(ursus.Col("ts"), ursus.Col("qv")),
					ursus.AsOfOn(ursus.Col("ts")),
					ursus.AsOfStrategyOpt(ursus.AsOfNearest),
					ursus.AsOfTolerance(c.tol)).
				Collect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, ok, err := got.At[int64](0, "qv"); err != nil {
				t.Fatal(err)
			} else if ok {
				t.Error("a candidate centuries away matched")
			}
		})
	}
}

// TestAsOfNearestPrefersTheCloserNeighbour needs no tolerance at all: the tie-break
// subtracted the two distances and both subtractions could wrap.
func TestAsOfNearestPrefersTheCloserNeighbour(t *testing.T) {
	ns := dtype.Datetime(dtype.Nano, "UTC")
	trades := keyedFrame(t, "tv", ns, 0)
	quotes := keyedFrame(t, "qv", ns, math.MinInt64, 1) // 292 years back, 1ns forward

	got, err := ursus.Scan(trades).Select(ursus.Col("ts"), ursus.Col("tv")).
		JoinAsOf(ursus.Scan(quotes).Select(ursus.Col("ts"), ursus.Col("qv")),
			ursus.AsOfOn(ursus.Col("ts")),
			ursus.AsOfStrategyOpt(ursus.AsOfNearest)).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	v, ok, err := got.At[int64](0, "qv")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || v != 1 {
		t.Errorf("nearest picked qv=%v (ok=%v); the candidate one nanosecond away is "+
			"row 1, not the one 292 years back", v, ok)
	}
}

// TestAsOfSubTickToleranceMatchesOnlyExactTicks pins the floor identity: converting
// the tolerance into ticks admits exactly what scaling the data up admitted, and no
// fixture in the repo covered a tolerance that is not a whole number of key ticks.
func TestAsOfSubTickToleranceMatchesOnlyExactTicks(t *testing.T) {
	sec := dtype.Datetime(dtype.Second, "UTC")
	for _, c := range []struct {
		tol   string
		match bool
	}{
		{"1500ms", true}, // floor(1.5e9 / 1e9) = 1 tick
		{"500ms", false}, // floor(5e8 / 1e9) = 0 ticks: only an exact match
		{"1s", true},
	} {
		trades := keyedFrame(t, "tv", sec, 10)
		quotes := keyedFrame(t, "qv", sec, 9) // exactly one tick earlier

		got, err := ursus.Scan(trades).Select(ursus.Col("ts"), ursus.Col("tv")).
			JoinAsOf(ursus.Scan(quotes).Select(ursus.Col("ts"), ursus.Col("qv")),
				ursus.AsOfOn(ursus.Col("ts")),
				ursus.AsOfTolerance(ursus.Every(c.tol))).
			Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		_, ok, err := got.At[int64](0, "qv")
		if err != nil {
			t.Fatal(err)
		}
		if ok != c.match {
			t.Errorf("tolerance %s over a one-tick gap: matched=%v, want %v",
				c.tol, ok, c.match)
		}
	}
}

// TestAsOfCalendarToleranceRespectsDST: a calendar day across spring-forward is 23
// elapsed hours, not 24, so a fixed 24h reaches a quote the calendar day does not.
// Nothing asserted that the calendar branch still has a calendar.
func TestAsOfCalendarToleranceRespectsDST(t *testing.T) {
	const tz = "America/New_York"
	if _, err := time.LoadLocation(tz); err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	// 2024-03-10 is spring-forward in New York: 02:00 jumps to 03:00, so the hour
	// between them does not exist and the calendar day is 23 hours long.
	trades := tsFrame(t, tz, dtype.Second, "2024-03-10T12:00:00")

	match := func(quote string, tol ursus.Interval) bool {
		t.Helper()
		quotes := tsFrame(t, tz, dtype.Second, quote)
		got, err := ursus.Scan(trades).Select(ursus.Col("ts"), ursus.Col("v").Alias("tv")).
			JoinAsOf(ursus.Scan(quotes).Select(ursus.Col("ts"), ursus.Col("v").Alias("qv")),
				ursus.AsOfOn(ursus.Col("ts")), ursus.AsOfTolerance(tol)).
			Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		_, ok, err := got.At[int64](0, "qv")
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	// 11:30 on the 9th is 23h30m of ELAPSED time before noon on the 10th, because an
	// hour was skipped — so a fixed 24h reaches it.
	const outsideTheDay = "2024-03-09T11:30:00"
	if !match(outsideTheDay, ursus.FromDuration(24*time.Hour)) {
		t.Error("a fixed 24h did not reach a quote 23.5 elapsed hours earlier")
	}
	// But a calendar day back from noon on the 10th is noon on the 9th, and 11:30 is
	// before that. A fixed count of hours cannot express this difference.
	if match(outsideTheDay, ursus.Every("1d")) {
		t.Error("a calendar day reached past midnight-to-midnight, so the tolerance " +
			"is being measured in fixed hours rather than on a calendar")
	}
	// A quote inside the calendar day still matches, so the bound is not simply
	// refusing everything.
	if !match("2024-03-09T12:30:00", ursus.Every("1d")) {
		t.Error("a calendar day did not reach a quote 30 minutes inside it")
	}
}

// TestAsOfKeyThatDoesNotSurvivePromotionSaysSo. Both sides cast to the promoted key
// type, and that cast was NON-strict: a value the promotion cannot hold became a
// null, and the null check below it then reported "the as-of key is null at row 0"
// — with a hint to filter the nulls out — on a not-null column containing none.
// The advice was impossible to follow and the row named was innocent.
func TestAsOfKeyThatDoesNotSurvivePromotionSaysSo(t *testing.T) {
	sec := dtype.Datetime(dtype.Second, "UTC")
	ns := dtype.Datetime(dtype.Nano, "UTC")

	// 9223372037s is 2262-04-11T23:47:17Z: a fine instant in seconds, one second past
	// what nanoseconds can hold. Joining it to a nanosecond key promotes to ns.
	trades := keyedFrame(t, "tv", sec, 9_223_372_037)
	quotes := keyedFrame(t, "qv", ns, 0)

	_, err := ursus.Scan(trades).Select(ursus.Col("ts"), ursus.Col("tv")).
		JoinAsOf(ursus.Scan(quotes).Select(ursus.Col("ts"), ursus.Col("qv")),
			ursus.AsOfOn(ursus.Col("ts"))).Collect(t.Context())
	if err == nil {
		t.Fatal("an instant the promoted key type cannot hold was joined anyway")
	}
	var ue *uerr.Error
	if !errors.As(err, &ue) || ue.Op != "join_asof" {
		t.Fatalf("want a join_asof refusal, got %v", err)
	}
	// The defect in one assertion: this used to be the null message, on a column
	// with no nulls.
	if strings.Contains(ue.Msg, "null") {
		t.Errorf("a value that does not fit is not a null: %v", err)
	}
	// The cause keeps what only the cast knows.
	for _, want := range []string{"9223372037", "row 0", "overflows"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should name %q: %v", want, err)
		}
	}
}

// TestAsOfNullKeyStillSaysNull: the strict cast must not swallow the common case.
// This key is null AND needs promoting, so both checks are live on the same column.
func TestAsOfNullKeyStillSaysNull(t *testing.T) {
	sec := dtype.Datetime(dtype.Second, "UTC")
	schema := dtype.MustSchema(dtype.Of("ts", sec), dtype.NotNull("tv", dtype.Int64))
	vb := bitmap.NewBuilder(1)
	vb.Append(false)
	b, err := data.NewBatch(schema, []*data.Column{
		data.NewFixed("ts", sec, []int64{0}, vb.Finish()),
		data.NewFixed("tv", dtype.Int64, []int64{1}, bitmap.AllSet(1))})
	if err != nil {
		t.Fatal(err)
	}
	src, err := memsrc.New(schema, b)
	if err != nil {
		t.Fatal(err)
	}
	quotes := keyedFrame(t, "qv", dtype.Datetime(dtype.Nano, "UTC"), 0)

	_, err = ursus.Scan(src).Select(ursus.Col("ts"), ursus.Col("tv")).
		JoinAsOf(ursus.Scan(quotes).Select(ursus.Col("ts"), ursus.Col("qv")),
			ursus.AsOfOn(ursus.Col("ts"))).Collect(t.Context())
	var ue *uerr.Error
	if !errors.As(err, &ue) || ue.Op != "join_asof" ||
		!strings.Contains(err.Error(), "null at row 0") {
		t.Fatalf("a genuinely null key must still say so: %v", err)
	}
}

// TestAsOfNumericKeyRefusesAToleranceAtPlanTime pins the refusal that keeps
// prepareTolerance's internal error unreachable. An Interval measures time; a
// numeric key has no tick length, so NanosPerTick answers false — and the tolerance
// used to DROP that answer, treat npt <= 0 as "no bound", and match everything.
//
// It is asserted through the resolver rather than the sink because that is where the
// guarantee lives: if this refusal ever moves, the sink's Internalf is what fires,
// and it is the only thing standing between a numeric key and an inert tolerance.
func TestAsOfNumericKeyRefusesAToleranceAtPlanTime(t *testing.T) {
	trades := tickFrame(t, "tv", []int64{100}, nil)
	quotes := tickFrame(t, "qv", []int64{99}, nil)
	tol := ursus.AsOfTolerance(ursus.FromDuration(time.Hour))

	// Explain, so this is the resolver refusing rather than the operator running.
	if _, err := trades.JoinAsOf(quotes, ursus.AsOfOn(ursus.Col("k")), tol).
		Explain(t.Context()); err == nil {
		t.Error("a tolerance on an Int64 key was planned")
	}
	_, err := trades.JoinAsOf(quotes, ursus.AsOfOn(ursus.Col("k")), tol).Collect(t.Context())
	var ue *uerr.Error
	if !errors.As(err, &ue) || !errors.Is(&uerr.Error{Kind: ue.Kind}, uerr.ErrUnsupported) {
		t.Fatalf("want a KindUnsupported refusal, got %v", err)
	}
	if !strings.Contains(err.Error(), "Int64") {
		t.Errorf("the refusal should name the key type: %v", err)
	}

	// The complement: the refusal is about the tolerance, not about numeric keys.
	// Without one, an Int64 as-of join is ordinary and must still work.
	got, err := trades.JoinAsOf(quotes, ursus.AsOfOn(ursus.Col("k"))).Collect(t.Context())
	if err != nil {
		t.Fatalf("a numeric key with no tolerance must still join: %v", err)
	}
	if _, ok, err := got.At[int64](0, "qv"); err != nil || !ok {
		t.Errorf("the numeric join matched nothing (ok=%v, err=%v)", ok, err)
	}
}

// TestAsOfNearestTiesBackward. Step 63 rewrote the tie-break in unsigned distances;
// "<=" is what sends an equal pair backward, and nothing asserted it, so ">" would
// have passed every existing test.
func TestAsOfNearestTiesBackward(t *testing.T) {
	nearest := ursus.AsOfStrategyOpt(ursus.AsOfNearest)
	for _, c := range []struct {
		name  string
		quote []int64
		want  int64 // the qv of the expected row, which is its index
	}{
		{"equidistant either side", []int64{95, 105}, 0},
		// The control: without it, "always pick the backward candidate" would pass
		// the case above, and the tie assertion would mean nothing.
		{"a closer neighbour ahead still wins", []int64{90, 101}, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			trades := tickFrame(t, "tv", []int64{100}, nil)
			quotes := tickFrame(t, "qv", c.quote, nil)
			got, err := trades.JoinAsOf(quotes, ursus.AsOfOn(ursus.Col("k")), nearest).
				Collect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			v, ok, err := got.At[int64](0, "qv")
			if err != nil {
				t.Fatal(err)
			}
			if !ok || v != c.want {
				t.Errorf("nearest picked qv=%v (ok=%v), want %d — ties go backward",
					v, ok, c.want)
			}
		})
	}
}

package ursus_test

// Time arithmetic, the second producer of a Time value: a Time plus or minus a
// Duration wraps at midnight, at every unit, and a Time minus a Time is a signed
// Duration. Polars 2.1 refuses a Time beside a Duration ("failed to determine
// supertype of time and duration"), so the wrap is held to Go's time package: the
// time of day of the epoch's midnight plus the clock plus the step. The difference's
// answers are Polars', recorded in the timearith group of testdata/oracle and spelled
// in oracle_timearith_test.go since step 498.
//
// Measured before the fix (step 236):
//
//	23:00 + 2h    printed 01:00:00, sorted AFTER 14:00:00, compared UNEQUAL to 01:00
//	01:00 - 2h    printed 23:00:00, compared UNEQUAL to 23:00
//	+106751 days  gave 00:15:26.290448384 instead of 23:50:00
//
// The last one is the reason the wrap has its own kernel arm rather than a pass over
// arithNum's output: the sum had already wrapped int64 before any modulo could run.

import (
	"testing"
	"time"

	"github.com/advenn/ursus"
)

// todTicks is the time of day clock plus by lands on, in u's ticks: Go's time package
// on the epoch's day, so the wrap at midnight is the calendar's.
func todTicks(clock, by time.Duration, u ursus.TimeUnit) int64 {
	tm := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC).Add(clock).Add(by)
	tod := time.Duration(tm.Hour())*time.Hour + time.Duration(tm.Minute())*time.Minute +
		time.Duration(tm.Second())*time.Second + time.Duration(tm.Nanosecond())
	return int64(tod / u.Duration())
}

// plus is a Time column t of clock, in u's ticks, moved by by: Add for a positive
// step, Sub for a negative one.
func plus(clock, by time.Duration, u ursus.TimeUnit) *ursus.LazyFrame {
	e := ursus.Col("t").Add(by)
	if by < 0 {
		e = ursus.Col("t").Sub(-by)
	}
	return clockAt("t", u, []int64{int64(clock / u.Duration())}).Select(e.Alias("t"))
}

// TestTimeArithmeticIsTheClocks: a Time plus or minus a Duration is where the clock
// lands, at every unit, Go's time package the oracle. 106,751 whole days is the
// largest whole-day Duration there is, and at nanosecond resolution the sum overflows
// int64 for any tick past about 23:47:17, so 23:50 is the case: adding whole days must
// leave the time of day where it was. Without reducing the duration first the sum
// wrapped and the modulo answered 00:15:26.290448384, in range, plausible and wrong.
func TestTimeArithmeticIsTheClocks(t *testing.T) {
	const huge = 106_751 * 24 * time.Hour
	for _, tc := range []struct {
		name      string
		clock, by time.Duration
	}{
		{"23:00 + 2h", 23 * time.Hour, 2 * time.Hour},
		{"01:00 - 2h", time.Hour, -2 * time.Hour},
		{"12:00 + 2h", 12 * time.Hour, 2 * time.Hour},
		{"23:50 + 106751 days", 23*time.Hour + 50*time.Minute, huge},
		{"00:10 - 106751 days", 10 * time.Minute, -huge},
		{"06:00 - 30h", 6 * time.Hour, -30 * time.Hour},
	} {
		for _, u := range []ursus.TimeUnit{ursus.Second, ursus.Milli, ursus.Micro, ursus.Nano} {
			got := typedCells[int64](t, plus(tc.clock, tc.by, u).Select(ursus.Col("t").Cast(ursus.Int64)))
			if want := todTicks(tc.clock, tc.by, u); !sameCells(got, []any{want}) {
				t.Errorf("%s at %s: %v, want %d", tc.name, u, got, want)
			}
		}
	}
}

// TestTimePrintsSortsAndComparesConsistently needs all three assertions, because
// fixing any one alone leaves the defect: a genuine 01:00:00 and a rolled-over one
// rendered identically and compared unequal, so a group_by produced two groups with
// the same label. The genuine clock is Go's time package's.
func TestTimePrintsSortsAndComparesConsistently(t *testing.T) {
	rolled := plus(23*time.Hour, 2*time.Hour, ursus.Second)
	genuine := clock("t", []int64{todTicks(23*time.Hour, 2*time.Hour, ursus.Second)})

	t.Run("prints as the genuine clock", func(t *testing.T) {
		got, err := rolled.Collect(t.Context(), ursus.WithVerify())
		if err != nil {
			t.Fatal(err)
		}
		want, err := genuine.Collect(t.Context(), ursus.WithVerify())
		if err != nil {
			t.Fatal(err)
		}
		if got.String() != want.String() {
			t.Errorf("23:00 + 2h does not render as the genuine clock:\n%s\nwant\n%s", got, want)
		}
	})

	t.Run("compares equal to the genuine clock", func(t *testing.T) {
		df, err := rolled.Select(
			ursus.Col("t").Eq(ursus.Lit(todTicks(23*time.Hour, 2*time.Hour, ursus.Second)).Cast(ursus.TimeOf(ursus.Second))).
				Alias("same"),
		).Collect(t.Context(), ursus.WithVerify())
		if err != nil {
			t.Fatal(err)
		}
		v, err := df.Column[bool]("same")
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := v.Get(0); !ok || !got {
			t.Errorf("23:00 + 2h does not compare equal to the genuine clock — it renders "+
				"identically and compares unequal, so a group_by makes two groups "+
				"with one label (got %v, valid %v)", got, ok)
		}
	})

	t.Run("sorts where it prints", func(t *testing.T) {
		got := typedCells[int64](t, clock("t", []int64{at2300, 12 * 3600}).
			Select(ursus.Col("t").Add(2*time.Hour).Alias("t")).
			Sort(ursus.Asc(ursus.Col("t"))).
			Select(ursus.Col("t").Cast(ursus.Int64)))
		want := []any{todTicks(23*time.Hour, 2*time.Hour, ursus.Second), todTicks(12*time.Hour, 2*time.Hour, ursus.Second)}
		if !sameCells(got, want) {
			t.Errorf("ascending sort put the rolled-over clock after 14:00 — ordering is on "+
				"the raw tick and disagrees with what is printed: %v, want %v", got, want)
		}
	})
}

// TestTimeDifferenceStaysASignedDuration guards the SCOPE of the wrap.
//
// Time - Time yields a Duration, which is signed and unbounded by design. Wrapping it
// too would be the same mistake one type over — 01:00 minus 23:00 is minus twenty-two
// hours, not two. Go's time package subtracts the two clocks.
func TestTimeDifferenceStaysASignedDuration(t *testing.T) {
	a, b := time.Hour, 23*time.Hour
	df, err := ursus.Frame(
		ursus.Values("a", []int64{int64(a / time.Second)}),
		ursus.Values("b", []int64{int64(b / time.Second)}),
	).
		Select(
			ursus.Col("a").Cast(ursus.TimeOf(ursus.Second)).Alias("a"),
			ursus.Col("b").Cast(ursus.TimeOf(ursus.Second)).Alias("b"),
		).
		Select(ursus.Col("a").Sub(ursus.Col("b")).Alias("d")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	v, err := df.Column[int64]("d")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := v.Get(0)
	if !ok {
		t.Fatal("the difference is null")
	}
	day := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	if want := int64(day.Add(a).Sub(day.Add(b)) / time.Second); got != want {
		t.Errorf("01:00 - 23:00 = %d, want %d — a Duration is signed and must not "+
			"be wrapped into a day", got, want)
	}
}

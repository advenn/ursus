package ursus_test

// Truncate by an Interval floors the column's wall clock, swept across zones.
//
// Every("1h") on a +05:30 column means the local hour, as Every("1d") means the
// local day. The oracle walks back from each instant a minute at a time to the
// latest instant whose wall clock is on the grid — or, where the grid point fell
// into a spring-forward gap, the instant the gap ends. It knows nothing of how the
// kernel computes, only what a wall clock is. The instants straddle each zone's
// 2024 transitions, ten minutes apart; the zones include half-hour and 45-minute
// offsets and a 30-minute daylight shift.

import (
	"fmt"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// knownWallClockDefects counts the wrong instants per zone and interval.
var knownWallClockDefects = map[string]int{
	"Asia/Kolkata 1h":         37,
	"Asia/Kolkata 2h":         37,
	"Asia/Kolkata 3h":         37,
	"Asia/Kolkata 90m":        37,
	"Asia/Kathmandu 1h":       37,
	"Asia/Kathmandu 2h":       37,
	"Asia/Kathmandu 3h":       37,
	"Asia/Kathmandu 90m":      37,
	"America/New_York 2h":     43,
	"America/New_York 3h":     74,
	"America/New_York 90m":    74,
	"Australia/Lord_Howe 1h":  40,
	"Australia/Lord_Howe 2h":  74,
	"Australia/Lord_Howe 3h":  74,
	"Australia/Lord_Howe 90m": 34,
	"Pacific/Chatham 1h":      72,
	"Pacific/Chatham 2h":      74,
	"Pacific/Chatham 3h":      74,
	"Pacific/Chatham 90m":     74,
}

// wallSeconds is u's wall clock, as seconds since the epoch read as if it were UTC.
func wallSeconds(u time.Time, loc *time.Location) int64 {
	_, off := u.In(loc).Zone()
	return u.Unix() + int64(off)
}

// gridFloorOracle is the latest instant u <= t whose wall clock is a multiple of
// every, or that ends a gap a grid wall clock fell into.
func gridFloorOracle(t time.Time, loc *time.Location, every time.Duration) (time.Time, bool) {
	step := int64(every / time.Second)
	onGrid := func(w int64) bool { return ((w%step)+step)%step == 0 }
	for u := t.Truncate(time.Minute); t.Sub(u) <= every+2*time.Hour; u = u.Add(-time.Minute) {
		if onGrid(wallSeconds(u, loc)) {
			return u, true
		}
		// A gap ends at u when the wall clock jumps forward across it: the wall
		// clocks [before, after) do not exist, and a grid point among them floors to u.
		before, after := wallSeconds(u.Add(-time.Second), loc)+1, wallSeconds(u, loc)
		for w := before; w < after; w++ {
			if onGrid(w) {
				return u, true
			}
		}
	}
	return time.Time{}, false
}

// transitions are the instants in 2024 at which loc's offset changes, or one
// ordinary midnight for a zone with none.
func transitions(loc *time.Location) []time.Time {
	var out []time.Time
	u := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	_, prev := u.In(loc).Zone()
	for ; u.Year() == 2024; u = u.Add(15 * time.Minute) {
		if _, off := u.In(loc).Zone(); off != prev {
			out, prev = append(out, u), off
		}
	}
	if len(out) == 0 {
		out = append(out, time.Date(2024, 3, 1, 0, 0, 0, 0, loc))
	}
	return out
}

func TestTruncateFloorsTheWallClock(t *testing.T) {
	zones := []string{"UTC", "Asia/Kolkata", "Asia/Kathmandu", "America/New_York",
		"Australia/Lord_Howe", "Pacific/Chatham"}
	everies := []string{"15m", "1h", "2h", "3h", "90m"}
	var samples, gapsSeen int

	for _, zone := range zones {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Skipf("no tzdata for %s: %v", zone, err)
		}
		dt := dtype.Datetime(dtype.Micro, zone)
		var stamps []time.Time
		for _, tr := range transitions(loc) {
			for d := -3 * time.Hour; d <= 3*time.Hour; d += 10 * time.Minute {
				stamps = append(stamps, tr.Add(d))
			}
		}
		ticks := make([]int64, len(stamps))
		for i, s := range stamps {
			var ok bool
			if ticks[i], ok = dt.FromTime(s); !ok {
				t.Fatalf("cannot store %s", s)
			}
		}
		frame := ursus.Frame(data.NewFixed("ts", dt, ticks, bitmap.AllSet(len(ticks))))

		for _, every := range everies {
			iv, err := time.ParseDuration(every)
			if err != nil {
				t.Fatal(err)
			}
			df, err := frame.Select(ursus.Col("ts").Dt().Truncate(ursus.Every(every)).Cast(ursus.Int64)).
				Collect(t.Context())
			if err != nil {
				t.Fatalf("%s %s: %v", zone, every, err)
			}
			got, err := df.Column[int64]("ts")
			if err != nil {
				t.Fatal(err)
			}
			key := zone + " " + every
			var wrong []string
			for i, s := range stamps {
				samples++
				want, ok := gridFloorOracle(s, loc, iv)
				if !ok {
					t.Fatalf("%s: the oracle found no grid point below %s", key, s)
				}
				if wallSeconds(want, loc)%int64(iv/time.Second) != 0 {
					gapsSeen++
				}
				tick, _ := got.Get(i)
				if g, _ := dt.ToTime(tick); !g.Equal(want) {
					wrong = append(wrong, fmt.Sprintf("%s -> %s, want %s",
						s.In(loc).Format(time.RFC3339), g.In(loc).Format(time.RFC3339), want.In(loc).Format(time.RFC3339)))
				}
			}
			n, known := knownWallClockDefects[key]
			switch {
			case known && len(wrong) == 0:
				t.Errorf("%s answers correctly now; delete it from knownWallClockDefects", key)
			case known && len(wrong) != n:
				t.Errorf("%s: %d wrong, the ratchet says %d: %v", key, len(wrong), n, wrong)
			case !known && len(wrong) > 0:
				t.Errorf("%s: %d wrong: %v", key, len(wrong), wrong)
			}
		}
	}
	if samples < 1600 || gapsSeen < 20 {
		t.Fatalf("%d samples, %d gap answers — the sweep has gone vacuous", samples, gapsSeen)
	}
}

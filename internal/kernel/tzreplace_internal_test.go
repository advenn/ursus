package kernel

import (
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

// TestZoneClockReadsEveryWallClockAsTheZoneDoes: around every transition of six
// zones from 2010 to 2025, and in shuffled order so the kept period is often the
// wrong one, a zoneClock's readings of a wall clock are exactly the instants a brute
// force finds: every offset a quarter-hour multiple within fourteen hours, kept where
// the zone has it at its own instant.
//
// Apia's 2011-12-30 is a gap of a whole day; Lord Howe moves half an hour.
func TestZoneClockReadsEveryWallClockAsTheZoneDoes(t *testing.T) {
	for _, tz := range []string{"Europe/London", "America/New_York", "Australia/Lord_Howe",
		"Pacific/Apia", "America/Santiago", "Asia/Tokyo"} {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			t.Skipf("no tzdata for %s: %v", tz, err)
		}
		offset := func(s int64) int64 {
			_, off := time.Unix(s, 0).In(loc).Zone()
			return int64(off)
		}
		brute := func(w int64) []int64 {
			var at []int64
			for o := int64(-14 * 3600); o <= 14*3600; o += 900 {
				if offset(w-o) == o {
					at = append(at, w-o)
				}
			}
			slices.Sort(at)
			return at
		}

		// Wall clocks within a day and a half of each transition, and a few far away.
		rng := rand.New(rand.NewPCG(174, uint64(len(tz))))
		var walls []int64
		end := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
		for s := time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC).Unix(); s < end; {
			_, next := time.Unix(s, 0).In(loc).ZoneBounds()
			if next.IsZero() {
				break
			}
			x := next.Unix()
			for range 12 {
				walls = append(walls, x+offset(x-1)+rng.Int64N(3*86400)-3*86400/2)
			}
			for _, d := range []int64{-3600, -1800, -60, 0, 60, 1800, 3600} {
				walls = append(walls, x+offset(x-1)+d, x+offset(x)+d)
			}
			s = x
		}
		for range 50 {
			walls = append(walls, rng.Int64N(end))
		}
		rng.Shuffle(len(walls), func(i, j int) { walls[i], walls[j] = walls[j], walls[i] })

		kept, err := newZoneClock(tz)
		if err != nil {
			t.Fatal(err)
		}
		gaps, folds := 0, 0
		for _, w := range walls {
			at, n := kept.instants(w)
			want := brute(w)
			if !slices.Equal(at[:n], want) {
				t.Fatalf("%s: the wall clock %s reads at %v, want %v", tz,
					time.Unix(w, 0).UTC().Format(time.DateTime), at[:n], want)
			}
			switch n {
			case 0:
				gaps++
			case 2:
				folds++
			}
		}
		if tz != "Asia/Tokyo" && (gaps == 0 || folds == 0) {
			t.Errorf("%s: %d gaps and %d folds among %d wall clocks; the fixture missed its transitions",
				tz, gaps, folds, len(walls))
		}
	}
}

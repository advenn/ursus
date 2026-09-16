package ursus_test

// A Time is a time of day, and nothing enforced it.
//
// dtype says TypeTime is "time of day since midnight, int64 in the type's unit".
// That sentence was the whole of the enforcement. Nothing asserted it at
// construction, in the resolver, in the kernel, or in any cast, writer or spill path
// — and the consequence was not an error but a contradiction between consumers that
// are each individually plausible.
//
// This file covers the CAST producer and the boundary of the invariant.
// timearith_test.go covers the other producer.

import (
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

// clockAt builds a Time column from raw tick counts, through the documented
// tick-count escape hatch: Int64 and Time share a physical layout, so the cast is a
// relabel and the values arrive exactly as written.
func clockAt(name string, u ursus.TimeUnit, ticks []int64) *ursus.LazyFrame {
	return ursus.Frame(ursus.Values(name, ticks)).
		Select(ursus.Col(name).Cast(ursus.TimeOf(u)))
}

func clock(name string, ticks []int64) *ursus.LazyFrame {
	return clockAt(name, ursus.Second, ticks)
}

const (
	at2300 = int64(23 * 3600)
	at0100 = int64(1 * 3600)
)

// TestCastDatetimeToTimeKeepsTheTimeOfDay.
//
// Cast relabels when both sides carry the same tick length, so Datetime(s) -> Time(s)
// reinterpreted SECONDS SINCE THE EPOCH as a time of day. It printed correctly by
// accident — ToTime rebuilds the real instant and the "15:04:05" layout discards the
// date — so the only assertion that can see it is an ORDERING one across two days
// whose times of day run the other way.
//
// Measured before the fix: the cast column sorted 22:00:00 before 06:00:00, i.e. by
// date.
func TestCastDatetimeToTimeKeepsTheTimeOfDay(t *testing.T) {
	ts := []time.Time{
		time.Date(2024, 3, 15, 22, 0, 0, 0, time.UTC), // earlier day, later clock
		time.Date(2024, 3, 16, 6, 0, 0, 0, time.UTC),  // later day, earlier clock
	}
	df, err := ursus.Frame(ursus.Values("ts", ts)).
		Select(ursus.Col("ts").Cast(ursus.TimeOf(ursus.Second)).Alias("t")).
		Sort(ursus.Asc(ursus.Col("t"))).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	out := df.String()
	six, twentytwo := strings.Index(out, "06:00:00"), strings.Index(out, "22:00:00")
	if six < 0 || twentytwo < 0 {
		t.Fatalf("expected 06:00:00 and 22:00:00 in:\n%s", out)
	}
	if six > twentytwo {
		t.Errorf("sorting a Datetime cast to Time ordered by DATE, not by time of "+
			"day — the cast is a relabel, so the stored tick is still an epoch "+
			"offset:\n%s", out)
	}
}

// TestCastToTimeIsIdempotent. Normalising in Cast must be a projection: casting an
// already-normalised column again cannot move it.
func TestCastToTimeIsIdempotent(t *testing.T) {
	df, err := clock("t", []int64{at2300, at0100, 0}).
		Select(ursus.Col("t").Cast(ursus.TimeOf(ursus.Second)).Alias("t")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"23:00:00", "01:00:00", "00:00:00"} {
		if !strings.Contains(df.String(), want) {
			t.Errorf("a second cast moved %s:\n%s", want, df.String())
		}
	}
}

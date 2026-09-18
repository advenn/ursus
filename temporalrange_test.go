package ursus_test

// The front door, and the two properties that keep the overflow guard narrow.
//
// Go documents time.Time.UnixNano as UNDEFINED before 1678 or after 2262. Three
// paths called it and believed the result, so 2300-06-01 entered a frame as
// 1715-11-11T00:25:26.290448384Z — wrong before any arithmetic this step guards ever
// ran.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/uerr"
)

var beyondNanos = time.Date(2300, 6, 1, 0, 0, 0, 0, time.UTC)

// TestAnInstantBeyondNanosecondsIsNullNotWrapped: Values has no error to return, and
// the rows are the user's data rather than a bug in the caller, so the row is null.
func TestAnInstantBeyondNanosecondsIsNullNotWrapped(t *testing.T) {
	ordinary := time.Date(2000, 6, 1, 12, 0, 0, 0, time.UTC)
	df, err := ursus.Frame(ursus.Values("t", []time.Time{ordinary, beyondNanos,
		time.Date(1600, 1, 1, 0, 0, 0, 0, time.UTC)})).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	got, err := df.Column[int64]("t")
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := got.Get(0); !ok || v != ordinary.UnixNano() {
		t.Errorf("an ordinary instant came back as %d, valid=%v", v, ok)
	}
	for _, row := range []int{1, 2} {
		if v, ok := got.Get(row); ok {
			t.Errorf("row %d is valid holding %d; an instant outside Datetime(ns) must be null",
				row, v)
		}
	}
}

// TestALiteralBeyondNanosecondsIsRefused: one value the user wrote, so it is named.
func TestALiteralBeyondNanosecondsIsRefused(t *testing.T) {
	_, err := ursus.Frame(ursus.Values("k", []int64{1})).
		Select(ursus.Lit(beyondNanos).Alias("t")).
		Collect(t.Context(), ursus.WithThreads(1))

	var ue *uerr.Error
	if !errors.As(err, &ue) || !errors.Is(&uerr.Error{Kind: ue.Kind}, uerr.ErrValue) {
		t.Fatalf("want a KindValue refusal, got %v", err)
	}
	if !strings.Contains(err.Error(), "2300-06-01") {
		t.Errorf("the refusal should name the instant: %v", err)
	}
}

// TestACastBeyondNanosecondsIsRefused closes the third path: the string parser goes
// through FromTime and checked its flag all along — the flag was the lie.
func TestACastBeyondNanosecondsIsRefused(t *testing.T) {
	_, err := ursus.Frame(ursus.Values("s", []string{"2300-06-01T00:00:00Z"})).
		Select(ursus.Col("s").Cast(ursus.Datetime(ursus.Nano, "UTC")).Alias("ts")).
		Collect(t.Context(), ursus.WithThreads(1))
	if err == nil {
		t.Fatal("a year-2300 instant was accepted as Datetime(ns)")
	}
	var ue *uerr.Error
	if !errors.As(err, &ue) || errors.Is(&uerr.Error{Kind: ue.Kind}, uerr.ErrInternal) {
		t.Errorf("a user's out-of-range value reported as an ursus bug: %v", err)
	}
}

// TestACoarserUnitHoldsTheSameInstant is the other half of the same change: deriving
// ticks from Unix seconds rather than from a nanosecond count widens what fits, so
// the year 3000 is an ordinary millisecond instant.
func TestACoarserUnitHoldsTheSameInstant(t *testing.T) {
	for _, u := range []ursus.TimeUnit{ursus.Second, ursus.Milli, ursus.Micro} {
		df, err := ursus.Frame(ursus.Values("s", []string{"3000-01-01T00:00:00Z"})).
			Select(ursus.Col("s").Cast(ursus.Datetime(u, "UTC")).Dt().Year().Alias("y")).
			Collect(t.Context())
		if err != nil {
			t.Fatalf("%s: %v", u, err)
		}
		y, err := df.Column[int32]("y")
		if err != nil {
			t.Fatal(err)
		}
		if v, _ := y.Get(0); v != 3000 {
			t.Errorf("Datetime(%s) round-tripped the year 3000 as %d", u, v)
		}
	}
}

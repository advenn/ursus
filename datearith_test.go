package ursus_test

// Date arithmetic, which did not work at all.
//
// All four forms errored at Collect: Date is a signed 32-bit DAY count while every
// other temporal type is an Int64 tick count, and kernel.arithmetic dispatches on
// the OUTPUT type and reads both operands at that width. So `Date - Date` published
// Duration(s) (Int64) with Date (Int32) operands and the kernel refused.
//
// Underneath that was a unit bug the width bug was hiding: had the widths merely
// been reconciled, 19001 - 19000 would have been published as 1s rather than
// 86400s. These tests therefore assert VALUES, not types — a type-only test passes
// against the wrong answer.
//
// Nothing in the repository cast to Date before this file, and PDS-H uses Date only
// for comparisons: its TPC-H Q1 `date '1998-12-01' - interval '90' day` is
// precomputed in Go as 1998-09-02, because the arithmetic was not available.

import (
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
)

// dateFrame builds a Date column from calendar dates. ursus.Values([]time.Time)
// yields Datetime(ns, UTC), so the cast is what makes these Dates.
func dateFrame(t *testing.T, name string, stamps ...string) *ursus.LazyFrame {
	t.Helper()
	ts := make([]time.Time, len(stamps))
	for i, s := range stamps {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		ts[i] = d
	}
	return ursus.Frame(ursus.Values(name, ts)).
		Select(ursus.Col(name).Cast(ursus.Date).Alias(name))
}

// TestDateDifferenceIsSeconds. The result is a Duration, and it counts SECONDS —
// which is the half a type assertion cannot see.
func TestDateDifferenceIsSeconds(t *testing.T) {
	lf := dateFrame(t, "a", "2024-03-01", "2024-01-02", "2024-01-01").
		HStack(dateFrame(t, "b", "2024-02-01", "2024-01-01", "2024-01-02"))

	df, err := lf.Select(ursus.Col("a").Sub(ursus.Col("b")).Alias("d")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got := df.Schema().Field(0).Type; got != ursus.Duration(ursus.Second) {
		t.Fatalf("type = %s, want Duration(s)", got)
	}

	// 29 days, 1 day, -1 day. In SECONDS: a day count published as seconds would
	// give 29, 1 and -1, which is the bug the width error was masking.
	for i, want := range []int64{29 * 86400, 86400, -86400} {
		got, ok, err := df.At[int64](i, "d")
		if err != nil {
			t.Fatal(err)
		}
		if !ok || got != want {
			t.Errorf("row %d = %d seconds, want %d (%v)", i, got, want,
				time.Duration(want)*time.Second)
		}
	}
}

// TestDatePlusDurationIsANaiveDatetime. resolveTemporalArithmetic's doc has always
// said "Date ± Duration → Datetime" while the code returned Date; that mismatch was
// the width error. A sub-day duration is the case that proves the promotion is not
// cosmetic: staying a Date would have to floor it away silently.
func TestDatePlusDurationIsANaiveDatetime(t *testing.T) {
	day := ursus.Lit(int64(86400)).Cast(ursus.Duration(ursus.Second))
	halfDay := ursus.Lit(int64(43200)).Cast(ursus.Duration(ursus.Second))

	for _, c := range []struct {
		name string
		expr ursus.Expr
		want int64 // seconds since the epoch; 2024-01-02 is 1704153600
	}{
		{"plus a whole day", ursus.Col("a").Add(day), 1704240000},
		{"minus a whole day", ursus.Col("a").Sub(day), 1704067200},
		{"plus half a day", ursus.Col("a").Add(halfDay), 1704196800},
		{"duration on the left", day.Add(ursus.Col("a")), 1704240000},
	} {
		t.Run(c.name, func(t *testing.T) {
			df, err := dateFrame(t, "a", "2024-01-02").
				Select(c.expr.Alias("r")).Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatal(err)
			}
			want := ursus.Datetime(ursus.Second, "")
			if got := df.Schema().Field(0).Type; got != want {
				t.Errorf("type = %s, want %s — a date is not an instant until "+
					"someone chooses a zone, so the result is naive", got, want)
			}
			got, ok, err := df.At[int64](0, "r")
			if err != nil {
				t.Fatal(err)
			}
			if !ok || got != c.want {
				t.Errorf("got %d, want %d (%s)", got, c.want,
					time.Unix(c.want, 0).UTC().Format(time.RFC3339))
			}
		})
	}
}

// TestDatePlusHalfDayKeepsTheHalfDay is the precision claim on its own, because it
// is the entire reason the result is not a Date.
func TestDatePlusHalfDayKeepsTheHalfDay(t *testing.T) {
	halfDay := ursus.Lit(int64(43200)).Cast(ursus.Duration(ursus.Second))
	df, err := dateFrame(t, "a", "2024-01-02").Select(
		ursus.Col("a").Add(halfDay).Alias("noon"),
		ursus.Col("a").Add(halfDay).Sub(halfDay).Alias("back"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	noon, _, err := df.At[int64](0, "noon")
	if err != nil {
		t.Fatal(err)
	}
	back, _, err := df.At[int64](0, "back")
	if err != nil {
		t.Fatal(err)
	}
	if noon-back != 43200 {
		t.Errorf("noon - back = %d, want 43200 — the half day was floored away",
			noon-back)
	}
}

// TestTemporalArithmeticRefusesMixedZones is the case the existing
// TestTemporalArithmeticAndComparisonAgree is named for and never covered: it
// varies the UNIT with both sides "UTC" and never varies the ZONE.
//
// Promote refuses Datetime(us,"UTC") against Datetime(us,"") for comparison —
// "an instant against a wall clock, and reconciling them would have to invent an
// offset". Subtraction accepted it and retimed kept the LEFT operand's zone, so a
// naive wall clock was silently reinterpreted as UTC: a plausible duration, wrong
// by the offset. The same question, answered two ways.
func TestTemporalArithmeticRefusesMixedZones(t *testing.T) {
	zoned := ursus.Scan(tsFrame(t, "UTC", dtype.Micro, "2024-01-01T00:00:00"))
	naive := ursus.Datetime(ursus.Micro, "")

	lf := zoned.Select(
		ursus.Col("ts").Alias("z"),
		ursus.Col("ts").Cast(naive).Alias("n"),
	)

	sub := lf.Select(ursus.Col("z").Sub(ursus.Col("n")).Alias("d"))
	cmp := lf.Select(ursus.Col("z").Gt(ursus.Col("n")).Alias("g"))

	_, subErr := sub.CollectSchema(t.Context())
	_, cmpErr := cmp.CollectSchema(t.Context())

	if subErr == nil {
		t.Error("subtracting a naive wall clock from a zoned instant must be " +
			"refused; accepting it reinterprets the naive side as UTC")
	}
	if cmpErr == nil {
		t.Error("comparison across zones is refused by Promote; this test's " +
			"premise has changed")
	}
	// The point is not that both fail. It is that they AGREE.
	if (subErr == nil) != (cmpErr == nil) {
		t.Errorf("arithmetic and comparison disagree about mixed zones: "+
			"sub=%v cmp=%v", subErr, cmpErr)
	}
	if subErr != nil && !strings.Contains(subErr.Error(), "Datetime") {
		t.Errorf("the refusal should name both types: %v", subErr)
	}
}

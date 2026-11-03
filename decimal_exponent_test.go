package ursus_test

// Step 414: a String cast to a Decimal reads an exponent that wrapped as out of
// range, where it read 0.

import (
	"errors"
	"testing"

	"github.com/advenn/ursus"
)

// TestCastToDecimalRefusesAWrappingExponent: "1e9223372036854775807" cast to
// Decimal(38,2) was 0.00, a silent wrong answer; it is refused as out of range, as
// DuckDB 1.5.5 and Polars 1.44.1 refuse it, and "1.5e2" still reads 150.00.
func TestCastToDecimalRefusesAWrappingExponent(t *testing.T) {
	_, err := ursus.Frame(ursus.Values("s", []string{"1.5e2", "1e9223372036854775807"})).
		Select(ursus.Col("s").Cast(ursus.Decimal(38, 2))).Collect(t.Context())
	if err == nil || !errors.Is(err, ursus.ErrValue) {
		t.Fatalf("got %v, want the second value refused", err)
	}
	df, err := ursus.Frame(ursus.Values("s", []string{"1.5e2"})).
		Select(ursus.Col("s").Cast(ursus.Decimal(38, 2)).Cast(ursus.String)).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	s, err := df.Column[string]("s")
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := s.Get(0); !ok || v != "150.00" {
		t.Errorf("1.5e2 as Decimal(38,2): got %q, want 150.00", v)
	}
}

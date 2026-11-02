package ursus_test

// Step 422: a naive Datetime is written without an offset, where it ended with a
// "Z", which named a wall clock an instant in UTC.

import (
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
)

// TestANaiveDatetimeHasNoOffset: 2024-01-02 03:04:05 as a naive Datetime(us) casts to
// String and prints as 2024-01-02T03:04:05.000000, where both ended with a Z; in UTC it
// keeps its Z, and in New York its -05:00.
func TestANaiveDatetimeHasNoOffset(t *testing.T) {
	at := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, c := range []struct {
		zone, want string
	}{
		{"", "2024-01-02T03:04:05.000000"},
		{"UTC", "2024-01-02T03:04:05.000000Z"},
		{"America/New_York", "2024-01-01T22:04:05.000000-05:00"},
	} {
		lf := ursus.Frame(ursus.Values("t", []time.Time{at})).
			Select(ursus.Col("t").Cast(dtype.Datetime(dtype.Micro, "UTC")).Dt().ConvertTimeZone(c.zone))
		if c.zone == "" {
			lf = ursus.Frame(ursus.Values("t", []time.Time{at})).Select(ursus.Col("t").Cast(dtype.Datetime(dtype.Micro, "")))
		}
		df, err := lf.Collect(t.Context())
		if err != nil {
			t.Fatalf("%q: %v", c.zone, err)
		}
		text, err := df.Select(t.Context(), ursus.Col("t").Cast(ursus.String))
		if err != nil {
			t.Fatal(err)
		}
		s, _ := text.Column[string]("t")
		if v, _ := s.Get(0); v != c.want {
			t.Errorf("%q cast to String: %q, want %q", c.zone, v, c.want)
		}
		if printed := df.String(); !strings.Contains(printed, c.want) {
			t.Errorf("%q printed:\n%s\nwant it to hold %q", c.zone, printed, c.want)
		}
	}
}

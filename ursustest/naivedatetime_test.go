package ursustest_test

import (
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/ursustest"
)

// TestANaiveDatetimeIsNotUTCUnderIgnoreDTypes: AssertFrameEqual compares cells by
// their text, so with IgnoreDTypes a naive Datetime and one in UTC of the same ticks
// compared equal while a naive one was written with a Z; since step 422 a naive one
// has no offset, and the two differ, as a wall clock and an instant do. Each still
// equals itself.
func TestANaiveDatetimeIsNotUTCUnderIgnoreDTypes(t *testing.T) {
	at := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	frame := func(zone string) *ursus.DataFrame {
		df, err := ursus.Frame(ursus.Values("t", []time.Time{at})).
			Select(ursus.Col("t").Cast(dtype.Datetime(dtype.Micro, zone))).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return df
	}
	naive, utc := frame(""), frame("UTC")
	if !didFail(func(tb testing.TB) { ursustest.AssertFrameEqual(tb, naive, utc, ursustest.IgnoreDTypes()) }) {
		t.Error("a naive Datetime and one in UTC compared equal under IgnoreDTypes")
	}
	if didFail(func(tb testing.TB) { ursustest.AssertFrameEqual(tb, naive, frame(""), ursustest.IgnoreDTypes()) }) {
		t.Error("a naive Datetime did not equal itself")
	}
}

package ursus_test

// Step 174: ReplaceTimeZone. The expected answers are Polars 1.44.1's, from the
// bench's own environment:
//
//	s.dt.replace_time_zone(tz, ambiguous=..., non_existent=...).dt.convert_time_zone("UTC")

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// inUTC is ReplaceTimeZone read back in UTC, where Polars' answers were taken.
func inUTC(tz string, opts ...ursus.ZoneOption) func(ursus.DtExpr) ursus.Expr {
	return func(d ursus.DtExpr) ursus.Expr {
		return d.ReplaceTimeZone(tz, opts...).Dt().ConvertTimeZone("UTC")
	}
}

func TestReplaceTimeZoneAnswersAsPolars(t *testing.T) {
	london := dtype.Datetime(dtype.Micro, "Europe/London")
	runCalCases(t, []calCase{
		{name: "a naive clock gains a zone", dt: naiveUs,
			in:   []string{"2024-03-31T00:30:00", "null"},
			f:    inUTC("Europe/London"),
			want: []string{"2024-03-31T00:30:00Z", "null"}},
		// London's clocks go from 01:00 GMT to 02:00 BST: 01:00 and 01:30 never read.
		{name: "spring forward, null", dt: naiveUs,
			in:   []string{"2024-03-31T00:00:00", "2024-03-31T00:30:00", "2024-03-31T01:00:00", "2024-03-31T01:30:00"},
			f:    inUTC("Europe/London", ursus.NonExistentNull),
			want: []string{"2024-03-31T00:00:00Z", "2024-03-31T00:30:00Z", "null", "null"}},
		// And back from 02:00 BST to 01:00 GMT: 01:00 and 01:30 read twice.
		{name: "fall back, earliest", dt: naiveUs,
			in: []string{"2024-10-27T00:00:00", "2024-10-27T00:30:00", "2024-10-27T01:00:00",
				"2024-10-27T01:30:00", "2024-10-27T02:00:00"},
			f: inUTC("Europe/London", ursus.AmbiguousEarliest),
			want: []string{"2024-10-26T23:00:00Z", "2024-10-26T23:30:00Z", "2024-10-27T00:00:00Z",
				"2024-10-27T00:30:00Z", "2024-10-27T02:00:00Z"}},
		{name: "fall back, latest", dt: naiveUs,
			in: []string{"2024-10-27T00:00:00", "2024-10-27T00:30:00", "2024-10-27T01:00:00",
				"2024-10-27T01:30:00", "2024-10-27T02:00:00"},
			f: inUTC("Europe/London", ursus.AmbiguousLatest),
			want: []string{"2024-10-26T23:00:00Z", "2024-10-26T23:30:00Z", "2024-10-27T01:00:00Z",
				"2024-10-27T01:30:00Z", "2024-10-27T02:00:00Z"}},
		{name: "fall back, null, and spring forward, null", dt: naiveUs,
			in: []string{"2024-03-31T00:30:00", "2024-03-31T01:30:00", "2024-03-31T02:30:00",
				"2024-10-27T00:30:00", "2024-10-27T01:30:00", "2024-10-27T02:30:00", "null"},
			f: inUTC("Europe/London", ursus.AmbiguousNull, ursus.NonExistentNull),
			want: []string{"2024-03-31T00:30:00Z", "null", "2024-03-31T01:30:00Z",
				"2024-10-26T23:30:00Z", "null", "2024-10-27T02:30:00Z", "null"}},
		// Samoa crossed the date line: 2011-12-30 never began in Apia, a gap of a day.
		{name: "a day that never read", dt: naiveUs,
			in: []string{"2011-12-29T23:00:00", "2011-12-30T00:00:00", "2011-12-30T12:00:00",
				"2011-12-30T23:59:00", "2011-12-31T00:00:00"},
			f:    inUTC("Pacific/Apia", ursus.NonExistentNull),
			want: []string{"2011-12-30T09:00:00Z", "null", "null", "null", "2011-12-30T10:00:00Z"}},
		// Lord Howe Island falls back half an hour, from 02:00 to 01:30.
		{name: "half an hour twice, earliest", dt: naiveUs,
			in:   []string{"2024-04-07T01:15:00", "2024-04-07T01:30:00", "2024-04-07T01:45:00", "2024-04-07T02:00:00"},
			f:    inUTC("Australia/Lord_Howe", ursus.AmbiguousEarliest),
			want: []string{"2024-04-06T14:15:00Z", "2024-04-06T14:30:00Z", "2024-04-06T14:45:00Z", "2024-04-06T15:30:00Z"}},
		{name: "half an hour twice, latest", dt: naiveUs,
			in:   []string{"2024-04-07T01:15:00", "2024-04-07T01:30:00", "2024-04-07T01:45:00", "2024-04-07T02:00:00"},
			f:    inUTC("Australia/Lord_Howe", ursus.AmbiguousLatest),
			want: []string{"2024-04-06T14:15:00Z", "2024-04-06T15:00:00Z", "2024-04-06T15:15:00Z", "2024-04-06T15:30:00Z"}},
		{name: "a zoned clock changes zone", dt: utcUs,
			in:   []string{"2024-01-01T12:00:00Z"},
			f:    inUTC("Asia/Tokyo"),
			want: []string{"2024-01-01T03:00:00Z"}},
		// 12:00 UTC reads 21:00 in Tokyo, and stays 21:00 naive.
		{name: "a zoned clock made naive", dt: dtype.Datetime(dtype.Micro, "Asia/Tokyo"),
			in:   []string{"2024-01-01T12:00:00Z"},
			f:    func(d ursus.DtExpr) ursus.Expr { return d.ReplaceTimeZone("") },
			want: []string{"2024-01-01T21:00:00"}},
		// New York reads 01:00 twice; London reads it once, in November at UTC.
		{name: "a fold read in a zone without one", dt: dtype.Datetime(dtype.Micro, "America/New_York"),
			in: []string{"2024-11-03T04:00:00Z", "2024-11-03T05:00:00Z", "2024-11-03T06:00:00Z",
				"2024-11-03T07:00:00Z", "2024-11-03T08:00:00Z"},
			f: inUTC("Europe/London"),
			want: []string{"2024-11-03T00:00:00Z", "2024-11-03T01:00:00Z", "2024-11-03T01:00:00Z",
				"2024-11-03T02:00:00Z", "2024-11-03T03:00:00Z"}},
		// Both instants read 01:30 in London. Kept in London, they stay two; in Paris,
		// where 01:30 reads once, they become one.
		{name: "the same zone keeps a fold's readings", dt: london,
			in:   []string{"2024-10-27T00:30:00Z", "2024-10-27T01:30:00Z"},
			f:    func(d ursus.DtExpr) ursus.Expr { return d.ReplaceTimeZone("Europe/London") },
			want: []string{"2024-10-27T01:30:00+01:00", "2024-10-27T01:30:00Z"}},
		{name: "a fold's readings in another zone", dt: london,
			in:   []string{"2024-10-27T00:30:00Z", "2024-10-27T01:30:00Z"},
			f:    inUTC("Europe/Paris"),
			want: []string{"2024-10-26T23:30:00Z", "2024-10-26T23:30:00Z"}},
		{name: "before 1970", dt: naiveUs,
			in:   []string{"1900-06-01T12:00:00"},
			f:    inUTC("Europe/London"),
			want: []string{"1900-06-01T12:00:00Z"}},
		{name: "nanoseconds", dt: dtype.Datetime(dtype.Nano, ""),
			in:   []string{"2024-06-01T12:00:00.000000001"},
			f:    inUTC("America/New_York"),
			want: []string{"2024-06-01T16:00:00.000000001Z"}},

		{name: "a Date", dt: dtype.Date, in: []string{"2024-01-01"},
			f:      func(d ursus.DtExpr) ursus.Expr { return d.ReplaceTimeZone("UTC") },
			refuse: "requires a Datetime"},
		{name: "an unknown zone", dt: naiveUs, in: []string{"2024-01-01T00:00:00"},
			f:      func(d ursus.DtExpr) ursus.Expr { return d.ReplaceTimeZone("Mars/Olympus_Mons") },
			refuse: "not known"},
		{name: "an ambiguous policy that is none", dt: naiveUs, in: []string{"2024-01-01T00:00:00"},
			f:      func(d ursus.DtExpr) ursus.Expr { return d.ReplaceTimeZone("UTC", ursus.Ambiguous("nearest")) },
			refuse: "ambiguous policy"},
		{name: "a non_existent policy Polars does not take", dt: naiveUs, in: []string{"2024-01-01T00:00:00"},
			f:      func(d ursus.DtExpr) ursus.Expr { return d.ReplaceTimeZone("UTC", ursus.NonExistent("earliest")) },
			refuse: "non_existent policy"},
	})
}

// TestReplaceTimeZoneRaisesByDefault: a wall clock read twice or never refuses the
// query, as Polars' default raises, naming the clock and how to answer it.
func TestReplaceTimeZoneRaisesByDefault(t *testing.T) {
	for _, c := range []struct {
		name, in string
		want     []string
	}{
		{"never", "2024-03-31T01:30:00", []string{"does not exist", "2024-03-31", "01:30", "NonExistentNull"}},
		{"twice", "2024-10-27T01:30:00", []string{"is ambiguous", "2024-10-27", "01:30", "AmbiguousEarliest"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			lf := calColumn(t, naiveUs, "2024-06-01T00:00:00", c.in).
				Select(ursus.Col("ts").Dt().ReplaceTimeZone("Europe/London"))
			_, err := lf.Collect(t.Context())
			if err == nil {
				t.Fatal("answered; Polars raises")
			}
			if errors.Is(err, uerr.ErrInternal) {
				t.Errorf("reported as an ursus bug: %v", err)
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("the error does not name %q: %v", w, err)
				}
			}
		})
	}
}

func TestReplaceTimeZoneKeepsTheUnit(t *testing.T) {
	lf := calColumn(t, dtype.Datetime(dtype.Milli, ""), "2024-03-31T00:30:00").
		Select(ursus.Col("ts").Dt().ReplaceTimeZone("Asia/Tokyo"))
	schema, err := lf.CollectSchema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := schema.Field(0).Type, dtype.Datetime(dtype.Milli, "Asia/Tokyo"); got != want {
		t.Fatalf("the type is %s, want %s", got, want)
	}
	if got := calRender(t, lf); len(got) != 1 || got[0] != "2024-03-31T00:30:00+09:00" {
		t.Fatalf("got %v", got)
	}
}

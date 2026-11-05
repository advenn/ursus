package strftime

import (
	"testing"
	"time"
)

func TestCompileRefuses(t *testing.T) {
	for _, f := range []string{"%Q", "%Y-%", "%.4f", "%4f", "%:", "%:m"} {
		if _, err := Compile(f); err == nil {
			t.Errorf("Compile(%q) accepted it", f)
		}
	}
}

// TestParse holds what the root package's TestStrptime does not reach. That one
// parses an unpadded day and month, a month's abbreviation, a day of the year, a
// 12-hour clock, a numeric offset and the leap day, and refuses a day that does not
// exist and a value with text left over.
func TestParse(t *testing.T) {
	ok := []struct {
		format, in string
		want       Fields
	}{
		{"%Y%m%d", "20240229", Fields{Year: 2024, Month: 2, Day: 29, HasDate: true}},
		{"%B %d %Y", "february 09 2024", Fields{Year: 2024, Month: 2, Day: 9, HasDate: true}},
		{"%y-%j", "24-060", Fields{Year: 2024, Month: 2, Day: 29, HasDate: true}},
		{"%Y-%m-%dT%H:%M:%S%.f", "2024-01-02T03:04:05.5", Fields{Year: 2024, Month: 1, Day: 2, Hour: 3, Minute: 4, Second: 5, Nanos: 500_000_000, HasDate: true, HasTime: true}},
		{"%Y-%m-%dT%H:%M:%S%.f", "2024-01-02T03:04:05", Fields{Year: 2024, Month: 1, Day: 2, Hour: 3, Minute: 4, Second: 5, HasDate: true, HasTime: true}},
		{"%F %T%.3f", "2024-01-02 03:04:05.123", Fields{Year: 2024, Month: 1, Day: 2, Hour: 3, Minute: 4, Second: 5, Nanos: 123_000_000, HasDate: true, HasTime: true}},
		{"%F %H:%M%:z", "2024-01-02 03:04-01:30", Fields{Year: 2024, Month: 1, Day: 2, Hour: 3, Minute: 4, Offset: -5400, HasDate: true, HasTime: true, HasZone: true}},
		{"%F %H:%M%z", "2024-01-02 03:04Z", Fields{Year: 2024, Month: 1, Day: 2, Hour: 3, Minute: 4, HasDate: true, HasTime: true, HasZone: true}},
		{"%a %F", "Fri 2024-01-05", Fields{Year: 2024, Month: 1, Day: 5, HasDate: true}},
		{"Q1 %Y", "Q1 2024", Fields{Year: 2024, Month: 1, Day: 1, HasDate: true}},
		{"%H:%M:%S%9f", "01:02:03123456789", Fields{Hour: 1, Minute: 2, Second: 3, Nanos: 123456789, HasTime: true}},
	}
	for _, tc := range ok {
		f, err := Compile(tc.format)
		if err != nil {
			t.Fatalf("Compile(%q): %v", tc.format, err)
		}
		got, err := f.Parse(tc.in)
		if err != nil {
			t.Errorf("Parse(%q, %q): %v", tc.format, tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Parse(%q, %q) = %+v, want %+v", tc.format, tc.in, got, tc.want)
		}
	}
	bad := []struct{ format, in string }{
		{"%Y-%m-%d", "2024-13-01"},        // no such month
		{"%Y-%m-%d", " 2024-01-02"},       // leading space
		{"%Y-%m-%d", "24-01-02"},          // %Y is four digits
		{"%H:%M", "24:00"},                // no such hour
		{"%H:%M", "12:60"},                // no such minute
		{"%I %p", "13 PM"},                // not a 12-hour clock
		{"%Y-%j", "2023-366"},             // no 366th day in 2023
		{"%Y%m%d", "2024229"},             // a month of two digits ate the day
		{"%m/%d", "01/02"},                // no year
		{"%F %z", "2024-01-02 +5"},        // a half offset
		{"%Y-%m-%d%.3f", "2024-01-02.12"}, // %.3f is exactly three digits
	}
	for _, tc := range bad {
		f, err := Compile(tc.format)
		if err != nil {
			t.Fatalf("Compile(%q): %v", tc.format, err)
		}
		if got, err := f.Parse(tc.in); err == nil {
			t.Errorf("Parse(%q, %q) accepted it: %+v", tc.format, tc.in, got)
		}
	}
}

// TestFormat likewise leaves to the root package's TestStrftime a weekday's and a
// month's full names, a 12-hour clock and three digits of fraction.
func TestFormat(t *testing.T) {
	at := time.Date(2024, 2, 9, 15, 4, 5, 123456789, time.FixedZone("X", 5*3600+30*60))
	cases := []struct{ format, want string }{
		{"%e %b %y", " 9 Feb 24"},
		{"%T%.f", "15:04:05.123456789"},
		{"%T%6f", "15:04:05123456"},
		{"%j", "040"},
		{"%z %:z %Z", "+0530 +05:30 X"},
		{"100%% %F", "100% 2024-02-09"},
	}
	for _, tc := range cases {
		f, err := Compile(tc.format)
		if err != nil {
			t.Fatal(err)
		}
		got, err := f.Append(nil, at, true)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != tc.want {
			t.Errorf("%q: %q, want %q", tc.format, got, tc.want)
		}
	}
	whole := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	f, _ := Compile("%T%.f")
	if got, _ := f.Append(nil, whole, true); string(got) != "00:00:00" {
		t.Errorf("%%.f on a whole second wrote %q", got)
	}
	z, _ := Compile("%F %z")
	if _, err := z.Append(nil, whole, false); err == nil {
		t.Error("%z formatted a naive instant")
	}
}

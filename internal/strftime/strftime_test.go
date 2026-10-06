package strftime

import (
	"math/rand/v2"
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

func TestParse(t *testing.T) {
	ok := []struct {
		format, in string
		want       Fields
	}{
		{"%Y-%m-%d", "2024-02-29", Fields{Year: 2024, Month: 2, Day: 29, HasDate: true}},
		{"%d/%m/%Y", "1/2/2024", Fields{Year: 2024, Month: 2, Day: 1, HasDate: true}},
		{"%Y%m%d", "20240229", Fields{Year: 2024, Month: 2, Day: 29, HasDate: true}},
		{"%b %e, %Y", "Feb  9, 2024", Fields{Year: 2024, Month: 2, Day: 9, HasDate: true}},
		{"%B %d %Y", "february 09 2024", Fields{Year: 2024, Month: 2, Day: 9, HasDate: true}},
		{"%y-%j", "24-060", Fields{Year: 2024, Month: 2, Day: 29, HasDate: true}},
		{"%Y-%m-%dT%H:%M:%S%.f", "2024-01-02T03:04:05.5", Fields{Year: 2024, Month: 1, Day: 2, Hour: 3, Minute: 4, Second: 5, Nanos: 500_000_000, HasDate: true, HasTime: true}},
		{"%Y-%m-%dT%H:%M:%S%.f", "2024-01-02T03:04:05", Fields{Year: 2024, Month: 1, Day: 2, Hour: 3, Minute: 4, Second: 5, HasDate: true, HasTime: true}},
		{"%F %T%.3f", "2024-01-02 03:04:05.123", Fields{Year: 2024, Month: 1, Day: 2, Hour: 3, Minute: 4, Second: 5, Nanos: 123_000_000, HasDate: true, HasTime: true}},
		{"%I:%M %p", "12:30 AM", Fields{Hour: 0, Minute: 30, HasTime: true}},
		{"%I:%M %p", "12:30 PM", Fields{Hour: 12, Minute: 30, HasTime: true}},
		{"%I:%M %p", "07:05 pm", Fields{Hour: 19, Minute: 5, HasTime: true}},
		{"%F %H:%M %z", "2024-01-02 03:04 +0530", Fields{Year: 2024, Month: 1, Day: 2, Hour: 3, Minute: 4, Offset: 19800, HasDate: true, HasTime: true, HasZone: true}},
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
		{"%Y-%m-%d", "2023-02-29"},        // no such day
		{"%Y-%m-%d", "2024-13-01"},        // no such month
		{"%Y-%m-%d", "2024-01-02 "},       // something left over
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

func TestFormat(t *testing.T) {
	at := time.Date(2024, 2, 9, 15, 4, 5, 123456789, time.FixedZone("X", 5*3600+30*60))
	cases := []struct{ format, want string }{
		{"%Y-%m-%d", "2024-02-09"},
		{"%e %b %y", " 9 Feb 24"},
		{"%A, %B %d", "Friday, February 09"},
		{"%I:%M:%S %p", "03:04:05 PM"},
		{"%T%.f", "15:04:05.123456789"},
		{"%T%.3f", "15:04:05.123"},
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

// TestRoundTrip: what Append writes, Parse reads back, for random instants and
// offsets across five centuries.
func TestRoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(113, 1))
	f, err := Compile("%Y-%m-%dT%H:%M:%S%.9f%:z")
	if err != nil {
		t.Fatal(err)
	}
	for range 5000 {
		off := (r.IntN(57) - 28) * 15 * 60
		at := time.Unix(r.Int64N(16e9)-8e9, r.Int64N(1e9)).In(time.FixedZone("", off))
		s, err := f.Append(nil, at, true)
		if err != nil {
			t.Fatal(err)
		}
		p, err := f.Parse(string(s))
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		back := time.Date(p.Year, time.Month(p.Month), p.Day, p.Hour, p.Minute, p.Second, p.Nanos,
			time.FixedZone("", p.Offset))
		if !back.Equal(at) {
			t.Fatalf("%s read back as %s", s, back)
		}
	}
}

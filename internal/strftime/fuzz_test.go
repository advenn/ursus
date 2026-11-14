package strftime

import (
	"testing"
	"time"

	"github.com/advenn/ursus/internal/fuzzcheck"
)

// The strftime formats' fuzz targets (step 509, step 464 of
// context_files/v0.7.2-plan.md): Append against Go's time.Format, a directive at a
// time, and Parse against Append, each inside fuzzcheck's limits.

var limits = fuzzcheck.Limits{Wall: 5 * time.Second, Alloc: 64 << 20}

// goLayout is the time.Format layout that writes what a directive writes, and false
// where there is none: %.f, which writes nothing for a whole second where Go's .999
// trims its zeros; a year outside 0000-9999, which Append signs as chrono does; and
// %y of a year before 0, whose remainder Go writes negative.
func goLayout(it item, year int) (string, bool) {
	switch it.spec {
	case 'Y':
		return "2006", year >= 0 && year <= 9999
	case 'y':
		return "06", year >= 0
	case 'f':
		if it.digit == 0 && it.dot {
			return "", false
		}
		n := it.digit
		if n == 0 {
			n = 9
		}
		return ".000000000"[:1+n], true // without its dot where the directive has none
	case 'z':
		if it.colon {
			return "-07:00", true
		}
		return "-0700", true
	}
	layout, ok := map[byte]string{'m': "01", 'd': "02", 'e': "_2", 'j': "002", 'H': "15", 'I': "03", 'p': "PM",
		'M': "04", 'S': "05", 'b': "Jan", 'B': "January", 'a': "Mon", 'A': "Monday", 'Z': "MST"}[it.spec]
	return layout, ok
}

// formatZones are the zones the targets write instants in: none (a naive instant,
// whose %z and %Z are refused), UTC, the round trip zones of dtype's target, and two
// whose offset had seconds, Amsterdam's +00:19:32 until 1937 and Monrovia's -00:44:30
// until 1972.
var formatZones = []string{"", "UTC", "Europe/Dublin", "Africa/Casablanca", "Australia/Lord_Howe",
	"America/St_Johns", "Europe/Amsterdam", "Africa/Monrovia"}

// instant is sec and nsec in the zone zone picks, within 400,000 years of 1970, and
// whether it has a zone.
func instant(t *testing.T, sec int64, nsec uint32, zone uint8) (time.Time, bool) {
	at := time.Unix(sec%(400_000*31_556_952), int64(nsec%1_000_000_000)).UTC()
	name := formatZones[int(zone)%len(formatZones)]
	if name == "" {
		return at, false
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("no zone %s: %v", name, err)
	}
	return at.In(loc), true
}

var formatSeeds = []string{
	"%Y-%m-%d %H:%M:%S", "%F %T%.f", "%FT%T%.3f%:z", "%Y%m%d%H%M%S%9f", "%d/%m/%Y %I:%M %p", "%e %B %Y",
	"%a, %d %b %Y %H:%M:%S %z", "%A %j %y", "%D %R", "%Y-%m-%dT%H:%M:%S%.6f%z %Z", "%H:%M:%S%.9f",
	"%3f %6f %9f %f", "Q1 %Y %%", "%Y%m", "%e%d", "%H %p", "%I", "%y%m%d", "%j", "%b%Y", "%m/%d",
	"literal only", "", "%", "%Q", "%.4f", "%:", "%Y-%",
}

// FuzzAppend: any format Compile takes, written for an instant in any of formatZones
// within 400,000 years of 1970, is what Go's time.Format writes, a directive at a time,
// where goLayout has a layout for it; Append refuses only a zone's directive for a
// naive instant. And where the format names each part of the instant once and
// unambiguously (determined), Parse reads back the instant's fields from what Append
// wrote.
func FuzzAppend(f *testing.F) {
	for i, s := range formatSeeds {
		for _, sec := range []int64{0, 1_700_000_000, -1_262_305_172, 253_402_300_799, 253_402_300_800,
			-62_167_219_201, 1_635_642_000, 1_617_454_800, -2_840_140_800} {
			f.Add(s, sec, uint32(i*123_456_789), uint8(i+int(sec&7)))
		}
	}
	f.Fuzz(func(t *testing.T, format string, sec int64, nsec uint32, zone uint8) {
		fm, err := Compile(format)
		if err != nil {
			return
		}
		at, zoned := instant(t, sec, nsec, zone)
		fuzzcheck.Run(t, limits, func() error {
			out, err := fm.Append(nil, at, zoned)
			if err != nil {
				if zoned || !fm.HasZone {
					t.Fatalf("Append(%q, %v, zoned %v): %v", format, at, zoned, err)
				}
				return nil
			}
			var ref []byte
			for _, it := range fm.items {
				if it.spec == 0 {
					ref = append(ref, it.lit...)
					continue
				}
				layout, ok := goLayout(it, at.Year())
				if !ok {
					one := &Format{items: []item{it}}
					if ref, err = one.Append(ref, at, zoned); err != nil {
						t.Fatal(err)
					}
					continue
				}
				s := at.Format(layout)
				if it.spec == 'f' && !it.dot {
					s = s[1:]
				}
				ref = append(ref, s...)
			}
			if string(out) != string(ref) {
				t.Fatalf("Append(%q, %v) = %q; time.Format, a directive at a time: %q", format, at, out, ref)
			}
			if determined(fm, at) {
				got, err := fm.Parse(string(out))
				if err != nil {
					t.Fatalf("Append(%q, %v) = %q, which Parse refuses: %v", format, at, out, err)
				}
				if want := fieldsOf(fm, at, zoned); got != want {
					t.Fatalf("Append(%q, %v) = %q, which Parse reads as %+v; want %+v", format, at, out, got, want)
				}
			}
			return nil
		})
	})
}

// determined is whether a format names an instant's parts so that Parse reads back
// what Append writes for at: each part at most once, a date whole (a year with a month
// and a day, or a day of the year) or not at all, an hour on a 12-hour clock with its
// AM or PM and %p with nothing else, a year by %y only in 1970-2069, no %Z, which
// Parse refuses, and no field of a varying width, a day by %e or a year outside
// 0000-9999, followed by a digit, where Parse's reading of the longest run of digits
// takes the next field's.
func determined(fm *Format, at time.Time) bool {
	seen := map[byte]bool{}
	group := map[byte]byte{'Y': 'Y', 'y': 'Y', 'm': 'm', 'b': 'm', 'B': 'm', 'd': 'd', 'e': 'd', 'j': 'j',
		'H': 'H', 'I': 'H', 'p': 'p', 'M': 'M', 'S': 'S', 'f': 'f', 'z': 'z', 'a': 'a', 'A': 'a', 'Z': 'Z'}
	for i, it := range fm.items {
		if it.spec == 0 {
			continue
		}
		g := group[it.spec]
		if seen[g] || g == 'Z' {
			return false
		}
		seen[g] = true
		wide := it.spec == 'e' || it.spec == 'Y' && (at.Year() < 0 || at.Year() > 9999)
		if wide && i+1 < len(fm.items) && digitFirst(fm.items[i+1]) {
			return false
		}
	}
	date := seen['Y'] || seen['m'] || seen['d'] || seen['j']
	if date && !(seen['Y'] && (seen['m'] && seen['d'] && !seen['j'] || seen['j'] && !seen['m'] && !seen['d'])) {
		return false
	}
	if seen['p'] != hasSpec(fm, 'I') {
		return false
	}
	if hasSpec(fm, 'y') && (at.Year() < 1970 || at.Year() > 2069) {
		return false
	}
	return true
}

func hasSpec(fm *Format, spec byte) bool {
	for _, it := range fm.items {
		if it.spec == spec {
			return true
		}
	}
	return false
}

// digitFirst is whether what an item writes can begin with a digit.
func digitFirst(it item) bool {
	if it.spec == 0 {
		return it.lit[0] >= '0' && it.lit[0] <= '9'
	}
	switch it.spec {
	case 'b', 'B', 'a', 'A', 'p', 'z', 'Z':
		return false
	case 'f':
		return !it.dot
	}
	return true
}

// fieldsOf is what Parse reads from Append's text for at, by a determined format: the
// parts it names, an offset cut to whole minutes toward zero, as %z writes it.
func fieldsOf(fm *Format, at time.Time, zoned bool) Fields {
	var w Fields
	w.HasTime = fm.HasTime
	for _, it := range fm.items {
		switch it.spec {
		case 'Y', 'y', 'm', 'b', 'B', 'd', 'e', 'j':
			w.HasDate = true
			w.Year, w.Month, w.Day = at.Year(), int(at.Month()), at.Day()
		case 'H', 'I':
			w.Hour = at.Hour()
		case 'M':
			w.Minute = at.Minute()
		case 'S':
			w.Second = at.Second()
		case 'f':
			w.Nanos = at.Nanosecond()
			if it.digit > 0 {
				per := []int{1, 100_000_000, 10_000_000, 1_000_000, 100_000, 10_000, 1000, 100, 10, 1}[it.digit]
				w.Nanos = w.Nanos / per * per
			}
		case 'z':
			_, off := at.Zone()
			w.Offset, w.HasZone = off/60*60, true
		}
	}
	return w
}

// FuzzParse: what Parse reads from any text by any format Compile takes, Append
// writes back as text that Parse reads to the same fields.
func FuzzParse(f *testing.F) {
	texts := []string{
		"2024-01-02 03:04:05", "2024-01-02 03:04:05.5", "2024-01-02T03:04:05.123+05:30", "20240229030405123456789",
		"02/01/2024 12:30 AM", " 5 May 2024", "Fri, 05 Jan 2024 03:04:05 -0330", "Friday 060 24", "01/02/24 03:04",
		"+10000-01-02", "-0005-01-02", "+2024-01-02", "1512", "13 PM", "12", "240229", "366", "Feb2024", "Q1 2024 %",
	}
	for i, s := range formatSeeds {
		f.Add(s, texts[i%len(texts)])
	}
	f.Fuzz(func(t *testing.T, format, text string) {
		fm, err := Compile(format)
		if err != nil {
			return
		}
		fuzzcheck.Run(t, limits, func() error {
			got, err := fm.Parse(text)
			if err != nil {
				return nil
			}
			zone := time.UTC
			if got.HasZone {
				zone = time.FixedZone("", got.Offset)
			}
			at := time.Date(1970, 1, 1, got.Hour, got.Minute, got.Second, got.Nanos, zone)
			if got.HasDate {
				at = time.Date(got.Year, time.Month(got.Month), got.Day, got.Hour, got.Minute, got.Second, got.Nanos, zone)
			}
			out, err := fm.Append(nil, at, got.HasZone)
			if err != nil {
				t.Fatalf("Parse(%q, %q) = %+v, which Append refuses: %v", format, text, got, err)
			}
			if spaceDayRunsOn(fm, at, got.HasZone, out) {
				return nil
			}
			back, err := fm.Parse(string(out))
			if err != nil || back != got {
				t.Fatalf("Parse(%q, %q) = %+v, written %q, read back %+v, %v", format, text, got, out, back, err)
			}
			return nil
		})
	})
}

// spaceDayRunsOn is whether out, Append's text for at, has a day below 10 that %e wrote
// as " 1" to " 9" directly before a digit. Parse reads %e as chrono and C's strptime
// do, one space skipped and then up to two digits, so " 1" before "0" reads back as
// day 10: the text is not read back as written, by ursus or by either of them. Found
// by CI's fuzzing of step 509: "%Y%Y%y%y%e%Y%f" on "0000000000000100000".
func spaceDayRunsOn(fm *Format, at time.Time, zone bool, out []byte) bool {
	for i, it := range fm.items {
		if it.spec != 'e' {
			continue
		}
		head, err := (&Format{items: fm.items[:i+1]}).Append(nil, at, zone)
		if err != nil || len(head) >= len(out) || head[len(head)-2] != ' ' {
			continue
		}
		if c := out[len(head)]; c >= '0' && c <= '9' {
			return true
		}
	}
	return false
}

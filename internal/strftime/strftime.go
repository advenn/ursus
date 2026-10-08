// Package strftime parses and formats instants with strftime-style formats, the
// ones Polars, chrono, Python and C share: "%Y-%m-%d %H:%M:%S".
//
// # Why not Go's layouts
//
// time.Parse reads a layout by example — "2006-01-02" — so translating a strftime
// format into one would hand every literal in the format to Go's matcher too: a
// literal "1" in "Q1 %Y" reads as a month, and "Mon" as a weekday. A format here is
// compiled into directives and literals, and only the directives are fields.
//
// # What it reads
//
//	%Y year, four digits, or signed past 0000–9999 (+10000)
//	%m month 01–12     %d day 01–31     %e day, space-padded
//	%y year 00–99: 00–69 are 2000–2069 and 70–99 are 1970–1999, as chrono reads them
//	%j day of year 001–366
//	%H hour 00–23   %I hour 01–12   %p AM/PM   %M minute 00–59   %S second 00–59
//	%f fraction, 1–9 digits as written   %.f the same after a dot, optional
//	%.3f %.6f %.9f a dot and exactly 3, 6, 9 digits   %3f %6f %9f the same, no dot
//	%b %h month abbreviation   %B month name   %a weekday abbreviation   %A weekday
//	%z offset +hhmm, read with or without the colon   %:z offset +hh:mm
//	%Z zone name (formatting only)
//	%T %H:%M:%S   %D %m/%d/%y   %F %Y-%m-%d   %R %H:%M   %% a percent sign
//
// Names are English. Parsing is strict about shape: a number takes at most its
// width, a date must exist (no 30 February), and the whole input must be consumed.
// A weekday name is read and not checked against the date.
package strftime

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Format is a compiled format.
type Format struct {
	src   string
	items []item

	// What the directives can supply, for the callers that refuse a format that
	// cannot say what their type needs. HasDate is any part of a date, which is
	// what a Time cannot format; ParsesDate is a whole one — a year with a month
	// and a day, or with %j — which is what Parse needs to produce a date at all.
	HasDate, HasTime, HasZone bool
	ParsesDate                bool
	// HasZoneName is %Z, which is formatted and cannot be read back.
	HasZoneName bool
}

type item struct {
	lit   string // a literal run, when spec is 0
	spec  byte
	digit int  // a fraction's fixed digit count; 0 for as many as written
	dot   bool // the fraction follows a '.'
	colon bool // %:z
}

// String is the source format.
func (f *Format) String() string { return f.src }

// Compile parses a format.
func Compile(src string) (*Format, error) {
	f := &Format{src: src}
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			f.items = append(f.items, item{lit: lit.String()})
			lit.Reset()
		}
	}
	var year, month, day, yday bool
	add := func(it item) {
		flush()
		f.items = append(f.items, it)
		switch it.spec {
		case 'Y', 'y', 'm', 'd', 'e', 'j', 'b', 'B', 'a', 'A':
			f.HasDate = true
		case 'H', 'I', 'M', 'S', 'f', 'p':
			f.HasTime = true
		case 'z', 'Z':
			f.HasZone = true
		}
		switch it.spec {
		case 'Y', 'y':
			year = true
		case 'm', 'b', 'B':
			month = true
		case 'd', 'e':
			day = true
		case 'j':
			yday = true
		case 'Z':
			f.HasZoneName = true
		}
	}

	for i := 0; i < len(src); i++ {
		c := src[i]
		if c != '%' {
			lit.WriteByte(c)
			continue
		}
		i++
		if i >= len(src) {
			return nil, errors.New("the format ends with a lone %")
		}
		switch d := src[i]; d {
		case '%':
			lit.WriteByte('%')
		case 'Y', 'y', 'm', 'd', 'e', 'j', 'H', 'I', 'M', 'S', 'p', 'b', 'h', 'B', 'a', 'A', 'z', 'Z':
			if d == 'h' {
				d = 'b'
			}
			add(item{spec: d})
		case 'f':
			add(item{spec: 'f'})
		case '.':
			// %.f, or %.3f %.6f %.9f.
			if i+1 < len(src) && src[i+1] == 'f' {
				add(item{spec: 'f', dot: true})
				i++
				break
			}
			if i+2 < len(src) && strings.IndexByte("369", src[i+1]) >= 0 && src[i+2] == 'f' {
				add(item{spec: 'f', dot: true, digit: int(src[i+1] - '0')})
				i += 2
				break
			}
			return nil, fmt.Errorf("%%.%s is not a directive; use %%.f, %%.3f, %%.6f or %%.9f", tail(src, i+1))
		case '3', '6', '9':
			if i+1 < len(src) && src[i+1] == 'f' {
				add(item{spec: 'f', digit: int(d - '0')})
				i++
				break
			}
			return nil, fmt.Errorf("%%%c is not a directive", d)
		case ':':
			if i+1 < len(src) && src[i+1] == 'z' {
				add(item{spec: 'z', colon: true})
				i++
				break
			}
			return nil, errors.New("%: is only valid as %:z")
		case 'T':
			add(item{spec: 'H'})
			add(item{lit: ":"})
			add(item{spec: 'M'})
			add(item{lit: ":"})
			add(item{spec: 'S'})
		case 'D':
			add(item{spec: 'm'})
			add(item{lit: "/"})
			add(item{spec: 'd'})
			add(item{lit: "/"})
			add(item{spec: 'y'})
		case 'F':
			add(item{spec: 'Y'})
			add(item{lit: "-"})
			add(item{spec: 'm'})
			add(item{lit: "-"})
			add(item{spec: 'd'})
		case 'R':
			add(item{spec: 'H'})
			add(item{lit: ":"})
			add(item{spec: 'M'})
		default:
			return nil, fmt.Errorf("%%%c is not a supported directive", d)
		}
	}
	flush()
	// A weekday names no date, and %d/%m names none without a year: each passed the
	// check that asked only for "a date directive", then failed on every row, or,
	// %a alone, gave the year 0 for every one.
	f.ParsesDate = year && (month && day && !yday || yday && !month && !day)
	return f, nil
}

func tail(s string, i int) string {
	if i >= len(s) {
		return ""
	}
	return s[i:min(len(s), i+2)]
}

// Fields is what a parse found. Year, Month and Day are a calendar date when
// HasDate is set; Offset is seconds east of UTC when HasZone is.
type Fields struct {
	Year, Month, Day          int
	Hour, Minute, Second      int
	Nanos                     int
	Offset                    int
	HasDate, HasTime, HasZone bool
}

// Parse reads s with f. It reports false, with the reason, when s does not match
// the format or names a date or time that does not exist.
func (f *Format) Parse(s string) (Fields, error) {
	var (
		out                Fields
		year, yday         = 0, 0
		haveYear, haveYDay bool
		month, day         = 1, 1
		haveMonth, haveDay bool
		hour12, pm         = -1, -1
		pos                int
	)
	num := func(minW, maxW int) (int, bool) {
		n, i := 0, pos
		for i < len(s) && i-pos < maxW && s[i] >= '0' && s[i] <= '9' {
			n = n*10 + int(s[i]-'0')
			i++
		}
		if i-pos < minW {
			return 0, false
		}
		pos = i
		return n, true
	}
	name := func(names []string, short bool) (int, bool) {
		for k, full := range names {
			want := full
			if short {
				want = full[:3]
			}
			if len(s)-pos >= len(want) && strings.EqualFold(s[pos:pos+len(want)], want) {
				pos += len(want)
				return k, true
			}
		}
		return 0, false
	}
	for _, it := range f.items {
		if it.spec == 0 {
			if !strings.HasPrefix(s[pos:], it.lit) {
				return out, fmt.Errorf("expected %q at byte %d", it.lit, pos)
			}
			pos += len(it.lit)
			continue
		}
		var ok bool
		switch it.spec {
		case 'Y':
			// Four digits, or a sign and up to nine: the form Append writes for a
			// year outside 0000–9999, which Date and Datetime(ms) and (us) can hold.
			sign := 0
			if pos < len(s) && (s[pos] == '-' || s[pos] == '+') {
				sign = 1
				if s[pos] == '-' {
					sign = -1
				}
				pos++
			}
			if sign == 0 {
				year, ok = num(4, 4)
			} else {
				year, ok = num(4, 9)
				year *= sign
			}
			haveYear = true
		case 'y':
			// chrono's pivot, which Polars reads with: "99" is 1999, not 2099.
			year, ok = num(2, 2)
			if year < 70 {
				year += 2000
			} else {
				year += 1900
			}
			haveYear = true
		case 'm':
			month, ok = num(1, 2)
			haveMonth = true
		case 'd':
			day, ok = num(1, 2)
			haveDay = true
		case 'e':
			if pos < len(s) && s[pos] == ' ' {
				pos++
			}
			day, ok = num(1, 2)
			haveDay = true
		case 'j':
			yday, ok = num(1, 3)
			haveYDay = true
		case 'H':
			out.Hour, ok = num(1, 2)
		case 'I':
			hour12, ok = num(1, 2)
		case 'M':
			out.Minute, ok = num(1, 2)
		case 'S':
			out.Second, ok = num(1, 2)
		case 'p':
			var k int
			k, ok = name([]string{"AM", "PM"}, false)
			pm = k
		case 'f':
			if it.dot {
				if pos >= len(s) || s[pos] != '.' {
					// %.f is optional: no dot, no fraction.
					ok = it.digit == 0
					break
				}
				pos++
			}
			start := pos
			maxW := 9
			if it.digit > 0 {
				maxW = it.digit
			}
			var frac int
			frac, ok = num(max(it.digit, 1), maxW)
			for w := pos - start; ok && w < 9; w++ {
				frac *= 10
			}
			out.Nanos = frac
		case 'b':
			var k int
			k, ok = name(monthNames, true)
			month, haveMonth = k+1, true
		case 'B':
			var k int
			k, ok = name(monthNames, false)
			month, haveMonth = k+1, true
		case 'a':
			_, ok = name(dayNames, true)
		case 'A':
			_, ok = name(dayNames, false)
		case 'z':
			ok = parseOffset(s, &pos, it.colon, &out.Offset)
			out.HasZone = true
		case 'Z':
			return out, errors.New("%Z names a zone, which cannot be read back; use %z")
		}
		if !ok {
			return out, fmt.Errorf("%%%c does not match at byte %d", it.spec, pos)
		}
	}
	if pos != len(s) {
		return out, fmt.Errorf("%q is left over after the format", s[pos:])
	}

	if hour12 >= 0 {
		if hour12 < 1 || hour12 > 12 {
			return out, fmt.Errorf("hour %d is not on a 12-hour clock", hour12)
		}
		out.Hour = hour12 % 12
		if pm == 1 {
			out.Hour += 12
		}
	} else if pm >= 0 && out.Hour > 12 {
		return out, fmt.Errorf("hour %d with %%p", out.Hour)
	}
	if out.Hour > 23 || out.Minute > 59 || out.Second > 59 {
		return out, fmt.Errorf("%02d:%02d:%02d is not a time of day", out.Hour, out.Minute, out.Second)
	}
	out.HasTime = f.HasTime

	if haveYear || haveMonth || haveDay || haveYDay {
		out.HasDate = true
		if !haveYear {
			return out, errors.New("the format names no year")
		}
		if haveYDay {
			if haveMonth || haveDay {
				return out, errors.New("%j and a month or day both name the date")
			}
			first := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
			if yday < 1 || yday > first.AddDate(1, 0, -1).YearDay() {
				return out, fmt.Errorf("day %d is not in the year %d", yday, year)
			}
			d := first.AddDate(0, 0, yday-1)
			month, day = int(d.Month()), d.Day()
		}
		if month < 1 || month > 12 {
			return out, fmt.Errorf("month %d does not exist", month)
		}
		if day < 1 || day > daysIn(year, month) {
			return out, fmt.Errorf("%04d-%02d-%02d does not exist", year, month, day)
		}
		out.Year, out.Month, out.Day = year, month, day
	}
	return out, nil
}

func parseOffset(s string, pos *int, colon bool, out *int) bool {
	i := *pos
	if i < len(s) && (s[i] == 'Z' || s[i] == 'z') {
		*out = 0
		*pos = i + 1
		return true
	}
	if i >= len(s) || (s[i] != '+' && s[i] != '-') {
		return false
	}
	sign := 1
	if s[i] == '-' {
		sign = -1
	}
	i++
	two := func() (int, bool) {
		if i+2 > len(s) || s[i] < '0' || s[i] > '9' || s[i+1] < '0' || s[i+1] > '9' {
			return 0, false
		}
		v := int(s[i]-'0')*10 + int(s[i+1]-'0')
		i += 2
		return v, true
	}
	h, ok := two()
	if !ok {
		return false
	}
	// %:z requires the colon; %z takes it or not, as chrono and Python read it.
	if i < len(s) && s[i] == ':' {
		i++
	} else if colon {
		return false
	}
	m, ok := two()
	if !ok || h > 23 || m > 59 {
		return false
	}
	*out = sign * (h*3600 + m*60)
	*pos = i
	return true
}

func daysIn(year, month int) int {
	return time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

var monthNames = []string{"January", "February", "March", "April", "May", "June",
	"July", "August", "September", "October", "November", "December"}

var dayNames = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

// Append formats t, in its own location, onto dst. zone reports whether t's zone
// is meaningful: a naive instant has none, and %z and %Z refuse to invent one.
func (f *Format) Append(dst []byte, t time.Time, zone bool) ([]byte, error) {
	for _, it := range f.items {
		if it.spec == 0 {
			dst = append(dst, it.lit...)
			continue
		}
		switch it.spec {
		case 'Y':
			// Signed outside 0000–9999, as chrono writes it, so Parse reads it back.
			y := t.Year()
			if y < 0 {
				dst = append(dst, '-')
				y = -y
			} else if y > 9999 {
				dst = append(dst, '+')
			}
			dst = pad(dst, y, 4, '0')
		case 'y':
			dst = pad(dst, ((t.Year()%100)+100)%100, 2, '0')
		case 'm':
			dst = pad(dst, int(t.Month()), 2, '0')
		case 'd':
			dst = pad(dst, t.Day(), 2, '0')
		case 'e':
			dst = pad(dst, t.Day(), 2, ' ')
		case 'j':
			dst = pad(dst, t.YearDay(), 3, '0')
		case 'H':
			dst = pad(dst, t.Hour(), 2, '0')
		case 'I':
			h := t.Hour() % 12
			if h == 0 {
				h = 12
			}
			dst = pad(dst, h, 2, '0')
		case 'p':
			if t.Hour() < 12 {
				dst = append(dst, "AM"...)
			} else {
				dst = append(dst, "PM"...)
			}
		case 'M':
			dst = pad(dst, t.Minute(), 2, '0')
		case 'S':
			dst = pad(dst, t.Second(), 2, '0')
		case 'f':
			digits := it.digit
			if digits == 0 {
				digits = 9
			}
			if it.dot {
				if it.digit == 0 && t.Nanosecond() == 0 {
					break // %.f writes nothing for a whole second
				}
				dst = append(dst, '.')
			}
			v := t.Nanosecond()
			for range 9 - digits {
				v /= 10
			}
			dst = pad(dst, v, digits, '0')
		case 'b':
			dst = append(dst, monthNames[t.Month()-1][:3]...)
		case 'B':
			dst = append(dst, monthNames[t.Month()-1]...)
		case 'a':
			dst = append(dst, dayNames[t.Weekday()][:3]...)
		case 'A':
			dst = append(dst, dayNames[t.Weekday()]...)
		case 'z':
			if !zone {
				return dst, errors.New("%z needs a time zone, and the value has none")
			}
			_, off := t.Zone()
			sign := byte('+')
			if off < 0 {
				sign, off = '-', -off
			}
			dst = append(dst, sign)
			dst = pad(dst, off/3600, 2, '0')
			if it.colon {
				dst = append(dst, ':')
			}
			dst = pad(dst, off%3600/60, 2, '0')
		case 'Z':
			if !zone {
				return dst, errors.New("%Z needs a time zone, and the value has none")
			}
			name, _ := t.Zone()
			dst = append(dst, name...)
		}
	}
	return dst, nil
}

func pad(dst []byte, v, width int, fill byte) []byte {
	var buf [20]byte
	i := len(buf)
	for v >= 10 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	i--
	buf[i] = byte('0' + v)
	for n := len(buf) - i; n < width; n++ {
		dst = append(dst, fill)
	}
	return append(dst, buf[i:]...)
}

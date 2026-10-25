package dtype

import "time"

// Temporal text as Polars' CSV reader reads it (step 355).
//
// Polars reads a Date, a Datetime or a Time column of a CSV file, given by a schema or
// inferred by try_parse_dates, by patterns it infers: a date year first or day first,
// 2024-01-02 and 02/01/2024, its parts split by '-', '/' or '.', a month and a day of
// one digit or two; a time of day after a 'T' or a space, 03:04, 03:04:05 or
// 03:04:05.123456789, an hour of one digit or two; and after it, an offset, Z, +09:00
// or +0900, a space before it allowed. A month first, 01/02/2024 as January, is not one
// of them: Polars reads 02/01/2024 as the second of January. Each value is read by its
// own pattern here, where Polars reads a column by its first value's and refuses the
// next of another.

// TemporalTextKind is what a text reads as: a date, an instant of no zone or of one,
// or a time of day.
type TemporalTextKind uint8

const (
	NoTemporalText TemporalTextKind = iota
	DateText
	DatetimeText
	ZonedDatetimeText
	TimeText
)

// textTime is a date, a time of day or both, and an offset, as read.
type textTime struct {
	year, month, day     int
	hour, minute, second int
	nanos                int
	offset               int // seconds east of UTC
	date, clock, zone    bool
}

// ClassifyTemporalText is what s reads as by Polars' CSV patterns, or NoTemporalText.
func ClassifyTemporalText(s string) TemporalTextKind {
	t, ok := scanTemporalText(s)
	switch {
	case !ok:
		return NoTemporalText
	case !t.date:
		return TimeText
	case !t.clock:
		return DateText
	case t.zone:
		return ZonedDatetimeText
	}
	return DatetimeText
}

// ParseTemporalText reads s into a tick count of d, a Date, a Datetime or a Time, by
// Polars' CSV patterns, and else as ParseTemporal does. A Date reads a date, a Time a
// time of day, and a Datetime either a date, its midnight, or a date and a time; an
// offset makes it the instant, and none takes the wall clock as UTC, as ParseTemporal
// does. A fraction past the unit is cut, as Polars cuts one.
func ParseTemporalText(d DataType, s string) (int64, bool) {
	t, ok := scanTemporalText(s)
	if !ok {
		return ParseTemporal(d, s)
	}
	switch d.ID() {
	case TypeDate:
		if !t.date || t.clock {
			return 0, false
		}
		return d.FromTime(time.Date(t.year, time.Month(t.month), t.day, 0, 0, 0, 0, time.UTC))
	case TypeDatetime:
		if !t.date {
			return 0, false
		}
		at := time.Date(t.year, time.Month(t.month), t.day, t.hour, t.minute, t.second, t.nanos, time.UTC)
		return d.FromTime(at.Add(-time.Duration(t.offset) * time.Second))
	case TypeTime:
		if t.date {
			return 0, false
		}
		return d.FromTime(time.Date(1970, 1, 1, t.hour, t.minute, t.second, t.nanos, time.UTC))
	}
	return ParseTemporal(d, s)
}

// scanTemporalText reads s by Polars' CSV patterns: a date, a date and a time of day
// and an offset, or a time of day alone, each part in its range and the date one that
// exists.
func scanTemporalText(s string) (textTime, bool) {
	var t textTime
	i := 0
	// num reads between lo and hi digits.
	num := func(lo, hi int) (int, bool) {
		v, n := 0, 0
		for i < len(s) && n < hi && s[i] >= '0' && s[i] <= '9' {
			v = v*10 + int(s[i]-'0')
			i++
			n++
		}
		return v, n >= lo
	}
	// A date: four digits first, a year, or one or two, a day.
	start := i
	first, ok := num(1, 4)
	if ok && i < len(s) && (s[i] == '-' || s[i] == '/' || s[i] == '.') {
		sep := s[i]
		yearFirst := i-start == 4
		if !yearFirst && i-start > 2 {
			return t, false
		}
		i++
		mid, ok := num(1, 2)
		if !ok || i >= len(s) || s[i] != sep {
			return t, false
		}
		i++
		if yearFirst {
			last, ok := num(1, 2)
			if !ok {
				return t, false
			}
			t.year, t.month, t.day = first, mid, last
		} else {
			at := i
			last, ok := num(4, 4)
			if !ok || i-at != 4 {
				return t, false
			}
			t.year, t.month, t.day = last, mid, first
		}
		if t.month < 1 || t.month > 12 || t.day < 1 || t.day > daysIn(t.year, t.month) {
			return t, false
		}
		t.date = true
		if i == len(s) {
			return t, true
		}
		if s[i] != 'T' && s[i] != ' ' {
			return t, false
		}
		i++
		start = i
		if first, ok = num(1, 2); !ok {
			return t, false
		}
	} else if !ok || i-start > 2 {
		return t, false
	}
	// A time of day: the hour read, then :MM, :SS, .fraction.
	if i >= len(s) || s[i] != ':' {
		return t, false
	}
	i++
	t.hour = first
	if t.minute, ok = num(2, 2); !ok {
		return t, false
	}
	if i < len(s) && s[i] == ':' {
		i++
		if t.second, ok = num(2, 2); !ok {
			return t, false
		}
		if i < len(s) && s[i] == '.' {
			i++
			at := i
			frac, ok := num(1, 9)
			if !ok {
				return t, false
			}
			for n := i - at; n < 9; n++ {
				frac *= 10
			}
			t.nanos = frac
		}
	}
	if t.hour > 23 || t.minute > 59 || t.second > 59 {
		return t, false
	}
	t.clock = true
	// An offset, only after a date.
	if i < len(s) && t.date {
		if s[i] == ' ' && i+1 < len(s) {
			i++
		}
		switch s[i] {
		case 'Z':
			i++
		case '+', '-':
			sign := 1
			if s[i] == '-' {
				sign = -1
			}
			i++
			h, ok := num(2, 2)
			if !ok {
				return t, false
			}
			if i < len(s) && s[i] == ':' {
				i++
			}
			m, ok := num(2, 2)
			if !ok || h > 23 || m > 59 {
				return t, false
			}
			t.offset = sign * (h*3600 + m*60)
		default:
			return t, false
		}
		t.zone = true
	}
	return t, i == len(s)
}

// daysIn is the days of a month of a year.
func daysIn(year, month int) int {
	switch month {
	case 2:
		if year%4 == 0 && (year%100 != 0 || year%400 == 0) {
			return 29
		}
		return 28
	case 4, 6, 9, 11:
		return 30
	}
	return 31
}

package dtype

// AppendISO appends ticks of d as the CSV writer's default layouts write them through
// Go's time package, "2006-01-02" for a Date, "2006-01-02T15:04:05" and its unit's
// fraction for a Datetime, "15:04:05" and its fraction for a Time, the digits worked
// out directly where the time package interprets the layout a field at a time: that
// was more than half a CSV write of a million rows with an instant (step 390). ok is
// false for a year outside 0 to 9999, a Time outside a day and any other type, which
// the caller writes as it did. A zone is the caller's: d's is not read.
func AppendISO(dst []byte, d DataType, ticks int64) ([]byte, bool) {
	if d.ID() == TypeDate {
		y, m, day := civil(ticks)
		if y < 0 || y > 9999 {
			return dst, false
		}
		return appendDate(dst, y, m, day), true
	}
	if d.ID() != TypeDatetime && d.ID() != TypeTime {
		return dst, false
	}
	perSec, digits := int64(1), 0
	switch d.TimeUnit() {
	case Milli:
		perSec, digits = 1_000, 3
	case Micro:
		perSec, digits = 1_000_000, 6
	case Nano:
		perSec, digits = 1_000_000_000, 9
	}
	sec, frac := divFloor(ticks, perSec)
	days, sod := divFloor(sec, 86_400)
	if d.ID() == TypeTime {
		if days != 0 {
			return dst, false
		}
	} else {
		y, m, day := civil(days)
		if y < 0 || y > 9999 {
			return dst, false
		}
		dst = append(appendDate(dst, y, m, day), 'T')
	}
	dst = appendDigits(dst, sod/3600, 2)
	dst = appendDigits(append(dst, ':'), sod/60%60, 2)
	dst = appendDigits(append(dst, ':'), sod%60, 2)
	if digits > 0 {
		dst = appendDigits(append(dst, '.'), frac, digits)
	}
	return dst, true
}

// divFloor is a / b rounded down, and the remainder that leaves, 0 to b - 1.
func divFloor(a, b int64) (q, r int64) {
	q, r = a/b, a%b
	if r < 0 {
		q, r = q-1, r+b
	}
	return q, r
}

// civil is the proleptic Gregorian date days after 1970-01-01, by Howard Hinnant's
// days_from_civil inverse.
func civil(days int64) (y, m, d int64) {
	z := days + 719_468
	era, doe := divFloor(z, 146_097)
	yoe := (doe - doe/1460 + doe/36_524 - doe/146_096) / 365
	doy := doe - (365*yoe + yoe/4 - yoe/100)
	mp := (5*doy + 2) / 153
	d = doy - (153*mp+2)/5 + 1
	m = mp + 3
	if m > 12 {
		m -= 12
	}
	y = yoe + era*400
	if m <= 2 {
		y++
	}
	return y, m, d
}

func appendDate(dst []byte, y, m, d int64) []byte {
	dst = appendDigits(dst, y, 4)
	dst = appendDigits(append(dst, '-'), m, 2)
	return appendDigits(append(dst, '-'), d, 2)
}

// appendDigits appends v, 0 or more, as n digits with leading zeros.
func appendDigits(dst []byte, v int64, n int) []byte {
	var b [9]byte
	for i := n - 1; i >= 0; i-- {
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return append(dst, b[:n]...)
}

package expr

import (
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/strftime"
	"github.com/advenn/ursus/internal/uerr"
)

func (f CallFn) isStrptime() bool {
	return f == FnStrToDateFmt || f == FnStrToDatetimeFmt || f == FnStrToTimeFmt
}

// callLitInt64 and callLitBool read a constant argument, as callLitString does.
func callLitInt64(c *Call, i int) (int64, bool) {
	if i < 0 || i >= len(c.Args) {
		return 0, false
	}
	l, ok := c.Args[i].(*Lit)
	if !ok {
		return 0, false
	}
	v, ok := l.Value.(int64)
	return v, ok
}

// compileFormat reads a call's format argument and compiles it, refusing one that
// does not compile while the query is planned rather than on its first row.
func compileFormat(c *Call) (*strftime.Format, error) {
	src, ok := callLitString(c, 1)
	if !ok {
		return nil, uerr.Internalf("expr: %s has no format argument", c.Fn)
	}
	f, err := strftime.Compile(src)
	if err != nil {
		return nil, uerr.New(uerr.KindValue, c.Fn.String(), "the format %q: %v", src, err).
			Hint("the directives are strftime's: %%Y %%m %%d %%H %%M %%S %%.f %%z %%b %%a %%j %%p and the rest; %%%% is a percent sign")
	}
	return f, nil
}

// strptimeOut types str.to_date, str.to_datetime and str.to_time, refusing a format
// that cannot produce the type: a Date needs a date in it, a Time needs a time and
// has no date, and only a Datetime can hold the offset %z reads.
func strptimeOut(c *Call) (dtype.DataType, error) {
	f, err := compileFormat(c)
	if err != nil {
		return dtype.Null, err
	}
	refuse := func(format string, args ...any) (dtype.DataType, error) {
		return dtype.Null, uerr.New(uerr.KindValue, c.Fn.String(), format, args...)
	}
	unit := func() (dtype.TimeUnit, error) {
		u, ok := callLitInt64(c, 3)
		if !ok || u < int64(dtype.Second) || u > int64(dtype.Nano) {
			return 0, uerr.Internalf("expr: %s has no valid unit argument", c.Fn)
		}
		return dtype.TimeUnit(u), nil
	}
	if f.HasZoneName {
		return refuse("the format %q reads %%Z, a zone name, which cannot be read back; use %%z", f)
	}
	switch c.Fn {
	case FnStrToDateFmt:
		if !f.ParsesDate {
			return refuse("the format %q names no date: a Date needs %%Y with %%m and %%d, or %%j", f)
		}
		if f.HasZone {
			return refuse("the format %q reads an offset, which a Date cannot hold; parse a Datetime", f)
		}
		return dtype.Date, nil
	case FnStrToDatetimeFmt:
		if !f.ParsesDate {
			return refuse("the format %q names no date: a Datetime needs %%Y with %%m and %%d, or %%j", f)
		}
		u, err := unit()
		if err != nil {
			return dtype.Null, err
		}
		tz, _ := callLitString(c, 4)
		if tz != "" && tz != "UTC" {
			if _, err := time.LoadLocation(tz); err != nil {
				return refuse("the time zone %q is not known: %v", tz, err)
			}
		}
		return dtype.Datetime(u, tz), nil
	case FnStrToTimeFmt:
		if !f.HasTime {
			return refuse("the format %q names no time of day", f)
		}
		if f.HasDate || f.HasZone {
			return refuse("the format %q reads a date or an offset, which a Time cannot hold; parse a Datetime", f)
		}
		u, err := unit()
		if err != nil {
			return dtype.Null, err
		}
		return dtype.Time(u), nil
	}
	return dtype.Null, uerr.Internalf("expr: %s is not a strptime", c.Fn)
}

// strftimeOut types dt.strftime, refusing a format that asks for what the value
// does not have: a date from a Time, or a zone from anything but a zoned Datetime.
func strftimeOut(c *Call, in dtype.DataType) (dtype.DataType, error) {
	f, err := compileFormat(c)
	if err != nil {
		return dtype.Null, err
	}
	if in.ID() == dtype.TypeTime && f.HasDate {
		return dtype.Null, uerr.New(uerr.KindValue, c.Fn.String(),
			"the format %q asks a time of day for a date", f)
	}
	if f.HasZone && (in.ID() != dtype.TypeDatetime || in.TimeZone() == "") {
		return dtype.Null, uerr.New(uerr.KindValue, c.Fn.String(),
			"the format %q asks a %s for a time zone, and it has none", f, in).
			Hint("give a Datetime a zone first, e.g. .Cast(ursus.Datetime(ursus.Micro, \"UTC\"))")
	}
	return dtype.String, nil
}

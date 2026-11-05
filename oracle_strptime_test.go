package ursus_test

// The strptime group (step 499, from strptime_test.go's hand values): parsing and
// formatting with strftime directives, each spelled as
// bench/scripts/oracle/strptime.py asks Polars. A Time is Polars' nanoseconds.

import (
	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
)

func init() {
	c := ursus.Col
	s := c("s").Str()
	us, usUTC := dtype.Datetime(dtype.Micro, ""), dtype.Datetime(dtype.Micro, "UTC")
	parse := func(dt dtype.DataType, format string, strict bool) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			return in.df().Select(s.Strptime(dt, format, strict).Alias("v"))
		}
	}
	format := func(col, format string) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(c(col).Dt().Strftime(format).Alias("s")) }
	}
	oracleGroups["strptime"] = map[string]oracleQuery{
		"day_month_year":             parse(ursus.Date, "%d/%m/%Y", true),
		"month_names":                parse(ursus.Date, "%b %e, %Y", true),
		"day_of_year":                parse(ursus.Date, "%Y-%j", true),
		"naive_with_a_fraction":      parse(us, "%Y-%m-%d %H:%M:%S%.f", true),
		"offset_read_into_utc":       parse(usUTC, "%Y-%m-%d %H:%M %z", true),
		"offset_read_into_naive":     parse(us, "%Y-%m-%d %H:%M %z", true),
		"a_zones_wall_clock":         parse(dtype.Datetime(dtype.Micro, "America/New_York"), "%Y-%m-%d %H:%M", true),
		"digits_finer_than_the_unit": parse(dtype.Datetime(dtype.Milli, ""), "%Y-%m-%d %H:%M:%S%.6f", true),
		"twelve_hour_clock":          parse(dtype.Time(dtype.Nano), "%I:%M %p", true),
		"not_strict_is_null":         parse(ursus.Date, "%Y-%m-%d", false),
		"date_long":                  format("day", "%A, %e %B %Y"),
		"naive_twelve_hour":          format("naive", "%d/%m/%y %I:%M:%S%.3f %p"),
		"utc_has_an_offset":          format("ts", "%H:%M %z"),
		"zoned_wall_clock":           format("tokyo", "%F %T %z %Z"),
		"time":                       format("clock", "%Hh%M"),
		"composite_directives":       format("naive", "%j %a %h %y %D %R"),
		"strftime_gen": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().Select(c("t").Dt().Strftime("%Y-%m-%d %H:%M:%S%.6f").Alias("iso"), c("t").Dt().Strftime("%d/%m/%Y %I:%M %p").Alias("dmy_12h"),
				c("d").Dt().Strftime("%A %e %B %Y").Alias("long"), c("d").Dt().Strftime("%Y-%j").Alias("doy"),
				c("z").Dt().Strftime("%Y-%m-%d %H:%M:%S %z").Alias("offset"), c("tm").Dt().Strftime("%H:%M:%S%.9f").Alias("clock"))
		},
		"strptime_gen": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().Select(c("iso").Str().Strptime(us, "%Y-%m-%d %H:%M:%S%.6f", true), c("dmy_12h").Str().Strptime(us, "%d/%m/%Y %I:%M %p", true),
				c("long").Str().Strptime(ursus.Date, "%A %e %B %Y", true), c("doy").Str().Strptime(ursus.Date, "%Y-%j", true),
				c("offset").Str().Strptime(usUTC, "%Y-%m-%d %H:%M:%S %z", true), c("clock").Str().Strptime(dtype.Time(dtype.Nano), "%H:%M:%S%.9f", true))
		},
	}
	// An offset's text read into a naive Datetime is the naive Datetime of its UTC
	// wall clock here, where Polars types the column UTC from the data (feature-gaps.md
	// §9, step 356).
	oracleDifferences["strptime/offset_read_into_naive"] = oracleDifference{
		why:    "an offset read into a naive Datetime is its UTC wall clock here, naive; Polars types it UTC (feature-gaps.md §9)",
		dtypes: map[string]string{"v": "Datetime(us)"}}
}

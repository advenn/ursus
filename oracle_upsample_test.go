package ursus_test

// The upsample group (step 501, from upsample_test.go's hand answers): Polars'
// upsample, each spelled as bench/scripts/oracle/upsample.py asks Polars, which is
// asked of the frame sorted, or without its null row, where it refuses what Upsample
// takes (feature-gaps.md §9).

import (
	"time"

	"github.com/advenn/ursus"
)

func init() {
	c := ursus.Col
	up := func(every ursus.Interval, groups ...string) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Upsample("t", every, groups...) }
	}
	day := ursus.Every("1d")
	oracleGroups["upsample"] = map[string]oracleQuery{
		"daily":                   up(day),
		"every_12h":               up(ursus.Every("12h")),
		"every_12h_duration":      func(in oracleInputs) *ursus.LazyFrame { return in.df().Upsample("t", 12*time.Hour) },
		"every_2d":                up(ursus.Every("2d")),
		"a_row_between_two_steps": up(day),
		"two_rows_at_one_instant": up(day),
		"monthly_from_jan_31":     up(ursus.Every("1mo")),
		"grouped":                 up(day, "g"),
		"a_null_key":              up(day, "g"),
		"a_date":                  up(day),
		"days_in_a_zone":          up(day),
		"hours_in_a_zone":         up(ursus.Every("1h")),
		"one_row":                 up(day),
		"unsorted":                up(day),
		"a_null_instant":          up(day),
		"upsample_gen": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().Sort(ursus.Asc(c("t")), ursus.Asc(c("g")), ursus.Asc(c("v"))).Upsample("t", ursus.Every("6h"), "g")
		},
		"upsample_gen_ungrouped": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().Sort(ursus.Asc(c("t")), ursus.Asc(c("v"))).Upsample("t", ursus.Every("12h"))
		},
	}
}

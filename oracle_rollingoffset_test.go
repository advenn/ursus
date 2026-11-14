package ursus_test

// The rollingoffset group (step 500, from rollingoffset_test.go's table): a rolling
// group's Offset, each window its rows' values as a list, spelled as
// bench/scripts/oracle/rollingoffset.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	every := ursus.Every
	roll := func(o ursus.RollingOptions) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Rolling(c("t"), o).Agg(c("v").Implode()) }
	}
	period := func(p string) ursus.RollingOptions { return ursus.RollingOptions{Period: every(p)} }
	offset := func(p, o string) ursus.RollingOptions {
		return ursus.RollingOptions{Period: every(p), Offset: every(o)}
	}
	noOffset := func(p string) ursus.RollingOptions {
		return ursus.RollingOptions{Period: every(p), Offset: ursus.NoOffset()}
	}
	// 1mo_offset_m28d and 1mo_offset_m4w are asked of Polars with a period of 28d and
	// 4w (step 507): an offset as long as the period, a month measured as 28 days, ends
	// the window at its row here, where Polars 2.1 ends 1mo after -28d at Feb 29.
	oracleGroups["rollingoffset"] = map[string]oracleQuery{
		"1mo":                          roll(period("1mo")),
		"1mo_offset_m1mo":              roll(offset("1mo", "-1mo")),
		"1mo_no_offset":                roll(noOffset("1mo")),
		"1mo_offset_m2d":               roll(offset("1mo", "-2d")),
		"1mo_offset_m28d":              roll(offset("1mo", "-28d")),
		"1mo_offset_m4w":               roll(offset("1mo", "-4w")),
		"1mo_offset_m40d":              roll(offset("1mo", "-40d")),
		"3d_offset_m1d":                roll(offset("3d", "-1d")),
		"3d_offset_1d_closed_left":     roll(ursus.RollingOptions{Period: every("3d"), Offset: every("1d"), Closed: ursus.ClosedLeft}),
		"1mo_closed_both":              roll(ursus.RollingOptions{Period: every("1mo"), Closed: ursus.ClosedBoth}),
		"2i":                           roll(period("2i")),
		"2i_no_offset":                 roll(noOffset("2i")),
		"2i_offset_m3i":                roll(offset("2i", "-3i")),
		"2i_offset_1i":                 roll(offset("2i", "1i")),
		"2i_offset_m2i":                roll(offset("2i", "-2i")),
		"2i_offset_m1i":                roll(offset("2i", "-1i")),
		"5i_gen":                       roll(period("5i")),
		"5i_no_offset_gen":             roll(noOffset("5i")),
		"5i_offset_m8i_gen":            roll(offset("5i", "-8i")),
		"5i_offset_2i_closed_left_gen": roll(ursus.RollingOptions{Period: every("5i"), Offset: every("2i"), Closed: ursus.ClosedLeft}),
		"4i_closed_both_gen":           roll(ursus.RollingOptions{Period: every("4i"), Closed: ursus.ClosedBoth}),
		"4i_closed_none_gen":           roll(ursus.RollingOptions{Period: every("4i"), Closed: ursus.ClosedNone}),
	}
}

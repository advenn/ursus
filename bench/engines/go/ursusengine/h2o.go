package ursusengine

import "ursus"

// h2oQueries holds the ported h2o.ai db-benchmark queries. Answer column names
// are part of the contract: config/suites.toml names the columns the checksum
// sums, and validate.py matches them against duckdb's answer by name.
var h2oQueries = map[string]Query{
	"gb1": gb1, "gb2": gb2, "gb3": gb3, "gb4": gb4, "gb5": gb5,
	"gb6": gb6, "gb7": gb7, "gb8": gb8, "gb9": gb9, "gb10": gb10,
	"j1": j1, "j2": j2, "j3": j3, "j4": j4, "j5": j5,
}

func gb1(scan Scan) *ursus.LazyFrame {
	return scan("g1").
		GroupBy(ursus.Col("id1")).
		Agg(ursus.Col("v1").Sum().Alias("v1"))
}

func gb2(scan Scan) *ursus.LazyFrame {
	return scan("g1").
		GroupBy(ursus.Col("id1"), ursus.Col("id2")).
		Agg(ursus.Col("v1").Sum().Alias("v1"))
}

func gb3(scan Scan) *ursus.LazyFrame {
	return scan("g1").
		GroupBy(ursus.Col("id3")).
		Agg(
			ursus.Col("v1").Sum().Alias("v1"),
			ursus.Col("v3").Mean().Alias("v3"),
		)
}

func gb4(scan Scan) *ursus.LazyFrame {
	return scan("g1").
		GroupBy(ursus.Col("id4")).
		Agg(
			ursus.Col("v1").Mean().Alias("v1"),
			ursus.Col("v2").Mean().Alias("v2"),
			ursus.Col("v3").Mean().Alias("v3"),
		)
}

func gb5(scan Scan) *ursus.LazyFrame {
	return scan("g1").
		GroupBy(ursus.Col("id6")).
		Agg(
			ursus.Col("v1").Sum().Alias("v1"),
			ursus.Col("v2").Sum().Alias("v2"),
			ursus.Col("v3").Sum().Alias("v3"),
		)
}

func gb6(scan Scan) *ursus.LazyFrame {
	return scan("g1").
		GroupBy(ursus.Col("id4"), ursus.Col("id5")).
		Agg(
			ursus.Col("v3").Median().Alias("median_v3"),
			// ddof=1: the sample standard deviation, which is what SQL stddev()
			// and polars std() both default to.
			ursus.Col("v3").Std(1).Alias("sd_v3"),
		)
}

// gb7 takes the max and min in the aggregate and subtracts afterwards.
// Arithmetic between two aggregates inside a single Agg would express the same
// thing, but this form is unambiguous about what the group-by accumulates.
func gb7(scan Scan) *ursus.LazyFrame {
	return scan("g1").
		GroupBy(ursus.Col("id3")).
		Agg(
			ursus.Col("v1").Max().Alias("max_v1"),
			ursus.Col("v2").Min().Alias("min_v2"),
		).
		Select(
			ursus.Col("id3"),
			ursus.Col("max_v1").Sub(ursus.Col("min_v2")).Alias("range_v1_v2"),
		)
}

// gb8 — the two largest v3 per id6.
//
// The reference implementations sort the whole frame and take the head of each
// group. ursus has no head-within-aggregate, so this uses an ordinal window
// rank instead, which is the same query a SQL engine would run
// (row_number() OVER (PARTITION BY id6 ORDER BY v3 DESC) <= 2) and is what
// engines/sql/h2o/gb8.sql does.
func gb8(scan Scan) *ursus.LazyFrame {
	return scan("g1").
		DropNulls("v3").
		// ursus refuses a window expression inside Filter and asks for it to be
		// materialised first, so the rank becomes a real column.
		WithColumns(
			ursus.Col("v3").Rank(ursus.RankOrdinal, true).
				Over(ursus.Col("id6")).
				Alias("order_v3"),
		).
		Filter(ursus.Col("order_v3").Le(2)).
		Select(
			ursus.Col("id6"),
			ursus.Col("v3").Alias("largest2_v3"),
		)
}

// gb9 — squared Pearson correlation of v1 and v2 per (id2, id4).
//
// Computed from the raw moments because ursus has no corr aggregate. The
// closed form is exact for this data and needs one pass, where a two-pass
// covariance would need the group means first. Division by zero yields NULL in
// ursus, which is also what SQL corr() returns for a zero-variance group, so
// degenerate groups agree with the reference without a special case.
func gb9(scan Scan) *ursus.LazyFrame {
	v1 := ursus.Col("v1").Cast(ursus.Float64)
	v2 := ursus.Col("v2").Cast(ursus.Float64)

	moments := scan("g1").
		GroupBy(ursus.Col("id2"), ursus.Col("id4")).
		Agg(
			ursus.Len().Cast(ursus.Float64).Alias("n"),
			v1.Sum().Alias("sx"),
			v2.Sum().Alias("sy"),
			v1.Mul(v2).Sum().Alias("sxy"),
			v1.Mul(v1).Sum().Alias("sxx"),
			v2.Mul(v2).Sum().Alias("syy"),
		)

	n := ursus.Col("n")
	sx, sy := ursus.Col("sx"), ursus.Col("sy")
	cov := n.Mul(ursus.Col("sxy")).Sub(sx.Mul(sy))
	varX := n.Mul(ursus.Col("sxx")).Sub(sx.Mul(sx))
	varY := n.Mul(ursus.Col("syy")).Sub(sy.Mul(sy))

	return moments.Select(
		ursus.Col("id2"),
		ursus.Col("id4"),
		cov.Mul(cov).Div(varX.Mul(varY)).Alias("r2"),
	)
}

func gb10(scan Scan) *ursus.LazyFrame {
	return scan("g1").
		GroupBy(
			ursus.Col("id1"), ursus.Col("id2"), ursus.Col("id3"),
			ursus.Col("id4"), ursus.Col("id5"), ursus.Col("id6"),
		).
		Agg(
			ursus.Col("v3").Sum().Alias("v3"),
			ursus.Len().Alias("cnt"),
		)
}

func join(scan Scan, right, key string, how ursus.JoinKind) *ursus.LazyFrame {
	return scan("x").Join(
		scan(right),
		ursus.JoinOn(ursus.Col(key)),
		ursus.JoinHow(how),
	)
}

func j1(scan Scan) *ursus.LazyFrame { return join(scan, "small", "id1", ursus.JoinInner) }
func j2(scan Scan) *ursus.LazyFrame { return join(scan, "medium", "id2", ursus.JoinInner) }
func j3(scan Scan) *ursus.LazyFrame { return join(scan, "medium", "id2", ursus.JoinLeft) }
func j4(scan Scan) *ursus.LazyFrame { return join(scan, "medium", "id5", ursus.JoinInner) }
func j5(scan Scan) *ursus.LazyFrame { return join(scan, "big", "id3", ursus.JoinInner) }

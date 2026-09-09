package ursusengine

import (
	"time"

	"github.com/advenn/ursus"
)

// pdshQueries holds the ported PDS-H (TPC-H) queries. A query that is absent is
// reported as `unsupported` rather than failing the run, so the suite stays
// usable while it is being filled in.
var pdshQueries = map[string]Query{
	"q1": q1, "q2": q2, "q3": q3, "q4": q4, "q5": q5, "q6": q6,
	"q7": q7, "q8": q8, "q9": q9, "q10": q10, "q11": q11, "q12": q12,
	"q13": q13, "q14": q14, "q15": q15, "q16": q16, "q17": q17, "q18": q18,
	"q19": q19, "q20": q20, "q21": q21, "q22": q22,
}

// date builds a Date literal. Lit(time.Time) produces a Datetime, which will not
// compare against a date32 column, so the cast is not optional.
func date(year int, month time.Month, day int) ursus.Expr {
	return ursus.Lit(time.Date(year, month, day, 0, 0, 0, 0, time.UTC)).Cast(ursus.Date)
}

// revenue is `l_extendedprice * (1 - l_discount)`, the TPC-H revenue expression.
func revenue() ursus.Expr {
	return ursus.Col("l_extendedprice").Mul(ursus.Lit(1.0).Sub(ursus.Col("l_discount")))
}

// on pairs differently-named join keys.
//
// Worth knowing when reading these queries: unlike polars, ursus keeps BOTH key
// columns when they are named differently, so after joining orders to customer
// on o_custkey = c_custkey the frame has both. That removes the renaming dance
// the polars port needs, at the cost of a slightly wider intermediate.
func on(left, right string) []ursus.JoinOption {
	return []ursus.JoinOption{
		ursus.JoinLeftOn(ursus.Col(left)),
		ursus.JoinRightOn(ursus.Col(right)),
	}
}

// q1 — pricing summary report.
//
// One filtered scan of lineitem into a two-key group-by with eight aggregates.
// The arithmetic is evaluated per row before aggregation, so this measures the
// expression evaluator and the group-by accumulator loop together.
func q1(scan Scan) *ursus.LazyFrame {
	return scan("lineitem").
		Filter(ursus.Col("l_shipdate").Le(date(1998, time.September, 2))).
		GroupBy(ursus.Col("l_returnflag"), ursus.Col("l_linestatus")).
		Agg(
			ursus.Col("l_quantity").Sum().Alias("sum_qty"),
			ursus.Col("l_extendedprice").Sum().Alias("sum_base_price"),
			revenue().Sum().Alias("sum_disc_price"),
			revenue().Mul(ursus.Lit(1.0).Add(ursus.Col("l_tax"))).Sum().Alias("sum_charge"),
			ursus.Col("l_quantity").Mean().Alias("avg_qty"),
			ursus.Col("l_extendedprice").Mean().Alias("avg_price"),
			ursus.Col("l_discount").Mean().Alias("avg_disc"),
			ursus.Len().Alias("count_order"),
		).
		Sort(
			ursus.Asc(ursus.Col("l_returnflag")),
			ursus.Asc(ursus.Col("l_linestatus")),
		)
}

// q2 — minimum cost supplier.
//
// The correlated `min(ps_supplycost)` per part becomes a group-by over the same
// candidate set, joined back. That is the standard dataframe rewrite and the
// plan a SQL optimiser produces for it anyway.
func q2(scan Scan) *ursus.LazyFrame {
	europe := scan("supplier").
		Join(scan("nation"), on("s_nationkey", "n_nationkey")...).
		Join(
			scan("region").Filter(ursus.Col("r_name").Eq("EUROPE")),
			on("n_regionkey", "r_regionkey")...,
		).
		Join(scan("partsupp"), on("s_suppkey", "ps_suppkey")...)

	candidates := europe.Join(
		scan("part").Filter(
			ursus.Col("p_size").Eq(int64(15)),
			ursus.Col("p_type").Str().EndsWith("BRASS"),
		),
		on("ps_partkey", "p_partkey")...,
	)

	cheapest := candidates.
		GroupBy(ursus.Col("ps_partkey")).
		Agg(ursus.Col("ps_supplycost").Min().Alias("min_supplycost"))

	return candidates.
		Join(cheapest, ursus.JoinOn(ursus.Col("ps_partkey"))).
		Filter(ursus.Col("ps_supplycost").Eq(ursus.Col("min_supplycost"))).
		Select(
			ursus.Col("s_acctbal"), ursus.Col("s_name"), ursus.Col("n_name"),
			ursus.Col("p_partkey"), ursus.Col("p_mfgr"), ursus.Col("s_address"),
			ursus.Col("s_phone"), ursus.Col("s_comment"),
		).
		Sort(
			ursus.Desc(ursus.Col("s_acctbal")),
			ursus.Asc(ursus.Col("n_name")),
			ursus.Asc(ursus.Col("s_name")),
			ursus.Asc(ursus.Col("p_partkey")),
		).
		Head(100)
}

// q3 — shipping priority. Three-way join, group-by, top ten by revenue.
func q3(scan Scan) *ursus.LazyFrame {
	cutoff := date(1995, time.March, 15)
	return scan("customer").
		Filter(ursus.Col("c_mktsegment").Eq("BUILDING")).
		Join(
			scan("orders").Filter(ursus.Col("o_orderdate").Lt(cutoff)),
			on("c_custkey", "o_custkey")...,
		).
		Join(
			scan("lineitem").Filter(ursus.Col("l_shipdate").Gt(cutoff)),
			on("o_orderkey", "l_orderkey")...,
		).
		GroupBy(
			ursus.Col("l_orderkey"),
			ursus.Col("o_orderdate"),
			ursus.Col("o_shippriority"),
		).
		Agg(revenue().Sum().Alias("revenue")).
		Sort(
			ursus.Desc(ursus.Col("revenue")),
			ursus.Asc(ursus.Col("o_orderdate")),
		).
		Head(10)
}

// q4 — order priority checking. EXISTS becomes a semi join.
func q4(scan Scan) *ursus.LazyFrame {
	return scan("orders").
		Filter(ursus.Col("o_orderdate").IsBetween(
			date(1993, time.July, 1), date(1993, time.October, 1), ursus.ClosedLeft)).
		Join(
			scan("lineitem").Filter(
				ursus.Col("l_commitdate").Lt(ursus.Col("l_receiptdate"))),
			ursus.JoinLeftOn(ursus.Col("o_orderkey")),
			ursus.JoinRightOn(ursus.Col("l_orderkey")),
			ursus.JoinHow(ursus.JoinSemi),
		).
		GroupBy(ursus.Col("o_orderpriority")).
		Agg(ursus.Len().Alias("order_count")).
		Sort(ursus.Asc(ursus.Col("o_orderpriority")))
}

// q5 — local supplier volume. Six-way join; the `c_nationkey = s_nationkey`
// correlation is folded into a composite join key rather than a post-filter, so
// the customer must be in the same nation as the supplier.
func q5(scan Scan) *ursus.LazyFrame {
	return scan("region").
		Filter(ursus.Col("r_name").Eq("ASIA")).
		Join(scan("nation"), on("r_regionkey", "n_regionkey")...).
		Join(scan("supplier"), on("n_nationkey", "s_nationkey")...).
		Join(scan("lineitem"), on("s_suppkey", "l_suppkey")...).
		Join(scan("orders"), on("l_orderkey", "o_orderkey")...).
		Join(scan("customer"),
			ursus.JoinLeftOn(ursus.Col("o_custkey"), ursus.Col("n_nationkey")),
			ursus.JoinRightOn(ursus.Col("c_custkey"), ursus.Col("c_nationkey")),
		).
		Filter(ursus.Col("o_orderdate").IsBetween(
			date(1994, time.January, 1), date(1995, time.January, 1), ursus.ClosedLeft)).
		GroupBy(ursus.Col("n_name")).
		Agg(revenue().Sum().Alias("revenue")).
		Sort(ursus.Desc(ursus.Col("revenue")))
}

// q6 — forecasting revenue change.
//
// A single scan with a conjunctive filter and one global sum: no joins, no
// grouping, no sort. Almost pure scan-and-filter throughput, and the query where
// predicate pushdown into the Parquet reader shows up most clearly.
func q6(scan Scan) *ursus.LazyFrame {
	return scan("lineitem").
		Filter(
			ursus.Col("l_shipdate").IsBetween(
				date(1994, time.January, 1), date(1995, time.January, 1), ursus.ClosedLeft),
			ursus.Col("l_discount").IsBetween(0.05, 0.07),
			ursus.Col("l_quantity").Lt(24.0),
		).
		GroupBy().
		Agg(ursus.Col("l_extendedprice").Mul(ursus.Col("l_discount")).Sum().Alias("revenue"))
}

// q7 — volume shipping.
//
// The symmetric FRANCE/GERMANY predicate is expressed as two directed pipelines
// concatenated, which is what turns a disjunction over two nation joins into two
// ordinary equi-join chains.
func q7(scan Scan) *ursus.LazyFrame {
	nation := func(name, keyAs, nameAs string) *ursus.LazyFrame {
		return scan("nation").
			Filter(ursus.Col("n_name").Eq(name)).
			Select(
				ursus.Col("n_nationkey").Alias(keyAs),
				ursus.Col("n_name").Alias(nameAs),
			)
	}

	shipments := func(supplierNation, customerNation string) *ursus.LazyFrame {
		return scan("customer").
			Join(nation(customerNation, "cust_nationkey", "cust_nation"),
				on("c_nationkey", "cust_nationkey")...).
			Join(scan("orders"), on("c_custkey", "o_custkey")...).
			Join(scan("lineitem"), on("o_orderkey", "l_orderkey")...).
			Join(scan("supplier"), on("l_suppkey", "s_suppkey")...).
			Join(nation(supplierNation, "supp_nationkey", "supp_nation"),
				on("s_nationkey", "supp_nationkey")...).
			Select(
				ursus.Col("supp_nation"), ursus.Col("cust_nation"),
				ursus.Col("l_shipdate"), ursus.Col("l_extendedprice"),
				ursus.Col("l_discount"),
			)
	}

	return shipments("FRANCE", "GERMANY").
		Concat(shipments("GERMANY", "FRANCE")).
		Filter(ursus.Col("l_shipdate").IsBetween(
			date(1995, time.January, 1), date(1996, time.December, 31))).
		WithColumns(
			ursus.Col("l_shipdate").Dt().Year().Alias("l_year"),
			revenue().Alias("volume"),
		).
		GroupBy(
			ursus.Col("supp_nation"), ursus.Col("cust_nation"), ursus.Col("l_year"),
		).
		Agg(ursus.Col("volume").Sum().Alias("revenue")).
		Sort(
			ursus.Asc(ursus.Col("supp_nation")),
			ursus.Asc(ursus.Col("cust_nation")),
			ursus.Asc(ursus.Col("l_year")),
		)
}

// q8 — national market share. Nation is joined twice, once for the customer's
// region and once for the supplier's name, so each copy is projected to
// distinct column names first.
func q8(scan Scan) *ursus.LazyFrame {
	customerNation := scan("nation").Select(
		ursus.Col("n_nationkey").Alias("cust_nationkey"),
		ursus.Col("n_regionkey").Alias("cust_regionkey"),
	)
	supplierNation := scan("nation").Select(
		ursus.Col("n_nationkey").Alias("supp_nationkey"),
		ursus.Col("n_name").Alias("nation"),
	)

	return scan("part").
		Filter(ursus.Col("p_type").Eq("ECONOMY ANODIZED STEEL")).
		Join(scan("lineitem"), on("p_partkey", "l_partkey")...).
		Join(scan("supplier"), on("l_suppkey", "s_suppkey")...).
		Join(scan("orders"), on("l_orderkey", "o_orderkey")...).
		Filter(ursus.Col("o_orderdate").IsBetween(
			date(1995, time.January, 1), date(1996, time.December, 31))).
		Join(scan("customer"), on("o_custkey", "c_custkey")...).
		Join(customerNation, on("c_nationkey", "cust_nationkey")...).
		Join(
			scan("region").Filter(ursus.Col("r_name").Eq("AMERICA")),
			on("cust_regionkey", "r_regionkey")...,
		).
		Join(supplierNation, on("s_nationkey", "supp_nationkey")...).
		WithColumns(
			ursus.Col("o_orderdate").Dt().Year().Alias("o_year"),
			revenue().Alias("volume"),
		).
		GroupBy(ursus.Col("o_year")).
		Agg(
			ursus.When(ursus.Col("nation").Eq("BRAZIL")).
				Then(ursus.Col("volume")).
				Otherwise(0.0).
				Sum().Alias("brazil_volume"),
			ursus.Col("volume").Sum().Alias("total_volume"),
		).
		Select(
			ursus.Col("o_year"),
			ursus.Col("brazil_volume").Div(ursus.Col("total_volume")).Alias("mkt_share"),
		).
		Sort(ursus.Asc(ursus.Col("o_year")))
}

// q9 — product type profit measure. Six-way join with a composite partsupp key.
func q9(scan Scan) *ursus.LazyFrame {
	return scan("part").
		Filter(ursus.Col("p_name").Str().Contains("green", true)).
		Join(scan("lineitem"), on("p_partkey", "l_partkey")...).
		Join(scan("supplier"), on("l_suppkey", "s_suppkey")...).
		Join(scan("partsupp"),
			ursus.JoinLeftOn(ursus.Col("l_suppkey"), ursus.Col("p_partkey")),
			ursus.JoinRightOn(ursus.Col("ps_suppkey"), ursus.Col("ps_partkey")),
		).
		Join(scan("orders"), on("l_orderkey", "o_orderkey")...).
		Join(scan("nation"), on("s_nationkey", "n_nationkey")...).
		WithColumns(
			ursus.Col("n_name").Alias("nation"),
			ursus.Col("o_orderdate").Dt().Year().Alias("o_year"),
			revenue().Sub(
				ursus.Col("ps_supplycost").Mul(ursus.Col("l_quantity")),
			).Alias("amount"),
		).
		GroupBy(ursus.Col("nation"), ursus.Col("o_year")).
		Agg(ursus.Col("amount").Sum().Alias("sum_profit")).
		Sort(
			ursus.Asc(ursus.Col("nation")),
			ursus.Desc(ursus.Col("o_year")),
		)
}

// q10 — returned item reporting. Four-way join, seven-key group-by, top twenty.
func q10(scan Scan) *ursus.LazyFrame {
	return scan("customer").
		Join(
			scan("orders").Filter(ursus.Col("o_orderdate").IsBetween(
				date(1993, time.October, 1), date(1994, time.January, 1), ursus.ClosedLeft)),
			on("c_custkey", "o_custkey")...,
		).
		Join(
			scan("lineitem").Filter(ursus.Col("l_returnflag").Eq("R")),
			on("o_orderkey", "l_orderkey")...,
		).
		Join(scan("nation"), on("c_nationkey", "n_nationkey")...).
		GroupBy(
			ursus.Col("c_custkey"), ursus.Col("c_name"), ursus.Col("c_acctbal"),
			ursus.Col("c_phone"), ursus.Col("n_name"), ursus.Col("c_address"),
			ursus.Col("c_comment"),
		).
		Agg(revenue().Sum().Alias("revenue")).
		Select(
			ursus.Col("c_custkey"), ursus.Col("c_name"), ursus.Col("revenue"),
			ursus.Col("c_acctbal"), ursus.Col("n_name"), ursus.Col("c_address"),
			ursus.Col("c_phone"), ursus.Col("c_comment"),
		).
		Sort(ursus.Desc(ursus.Col("revenue"))).
		Head(20)
}

// q11 — important stock identification.
//
// The HAVING threshold is a scalar over the same input. Cross-joining the
// one-row frame keeps the whole thing in a single lazy plan rather than forcing
// an intermediate Collect just to read one number back out.
func q11(scan Scan) *ursus.LazyFrame {
	germanStock := scan("partsupp").
		Join(scan("supplier"), on("ps_suppkey", "s_suppkey")...).
		Join(
			scan("nation").Filter(ursus.Col("n_name").Eq("GERMANY")),
			on("s_nationkey", "n_nationkey")...,
		).
		WithColumns(
			ursus.Col("ps_supplycost").Mul(ursus.Col("ps_availqty")).Alias("value"),
		)

	threshold := germanStock.
		GroupBy().
		Agg(ursus.Col("value").Sum().Alias("total")).
		Select(ursus.Col("total").Mul(0.0001).Alias("threshold"))

	return germanStock.
		GroupBy(ursus.Col("ps_partkey")).
		Agg(ursus.Col("value").Sum().Alias("value")).
		Join(threshold, ursus.JoinHow(ursus.JoinCross)).
		Filter(ursus.Col("value").Gt(ursus.Col("threshold"))).
		Select(ursus.Col("ps_partkey"), ursus.Col("value")).
		Sort(ursus.Desc(ursus.Col("value")))
}

// q12 — shipping modes and order priority. Two conditional counts over one join.
func q12(scan Scan) *ursus.LazyFrame {
	urgent := ursus.Col("o_orderpriority").IsIn("1-URGENT", "2-HIGH")

	return scan("orders").
		Join(scan("lineitem"), on("o_orderkey", "l_orderkey")...).
		Filter(
			ursus.Col("l_shipmode").IsIn("MAIL", "SHIP"),
			ursus.Col("l_commitdate").Lt(ursus.Col("l_receiptdate")),
			ursus.Col("l_shipdate").Lt(ursus.Col("l_commitdate")),
			ursus.Col("l_receiptdate").IsBetween(
				date(1994, time.January, 1), date(1995, time.January, 1), ursus.ClosedLeft),
		).
		GroupBy(ursus.Col("l_shipmode")).
		Agg(
			ursus.When(urgent).Then(int64(1)).Otherwise(int64(0)).
				Sum().Alias("high_line_count"),
			ursus.When(urgent).Then(int64(0)).Otherwise(int64(1)).
				Sum().Alias("low_line_count"),
		).
		Sort(ursus.Asc(ursus.Col("l_shipmode")))
}

// q13 — customer distribution. A left join, then a group-by of a group-by.
func q13(scan Scan) *ursus.LazyFrame {
	return scan("customer").
		Join(
			// LIKE '%special%requests%': the wildcards between the words make
			// this a regex, not a substring test.
			scan("orders").Filter(
				ursus.Col("o_comment").Str().Contains("special.*requests", false).Not()),
			ursus.JoinLeftOn(ursus.Col("c_custkey")),
			ursus.JoinRightOn(ursus.Col("o_custkey")),
			ursus.JoinHow(ursus.JoinLeft),
		).
		GroupBy(ursus.Col("c_custkey")).
		Agg(ursus.Col("o_orderkey").Count().Alias("c_count")).
		GroupBy(ursus.Col("c_count")).
		Agg(ursus.Len().Alias("custdist")).
		Sort(
			ursus.Desc(ursus.Col("custdist")),
			ursus.Desc(ursus.Col("c_count")),
		)
}

// q14 — promotion effect.
func q14(scan Scan) *ursus.LazyFrame {
	return scan("lineitem").
		Filter(ursus.Col("l_shipdate").IsBetween(
			date(1995, time.September, 1), date(1995, time.October, 1), ursus.ClosedLeft)).
		Join(scan("part"), on("l_partkey", "p_partkey")...).
		GroupBy().
		Agg(
			ursus.When(ursus.Col("p_type").Str().StartsWith("PROMO")).
				Then(revenue()).
				Otherwise(0.0).
				Sum().Alias("promo"),
			revenue().Sum().Alias("total"),
		).
		Select(
			ursus.Lit(100.00).Mul(ursus.Col("promo")).Div(ursus.Col("total")).
				Alias("promo_revenue"),
		)
}

// q15 — top supplier. An aggregate view, its own maximum, and a join back.
func q15(scan Scan) *ursus.LazyFrame {
	bySupplier := scan("lineitem").
		Filter(ursus.Col("l_shipdate").IsBetween(
			date(1996, time.January, 1), date(1996, time.April, 1), ursus.ClosedLeft)).
		GroupBy(ursus.Col("l_suppkey")).
		Agg(revenue().Sum().Alias("total_revenue"))

	best := bySupplier.
		GroupBy().
		Agg(ursus.Col("total_revenue").Max().Alias("max_revenue"))

	return scan("supplier").
		Join(bySupplier, on("s_suppkey", "l_suppkey")...).
		Join(best, ursus.JoinHow(ursus.JoinCross)).
		Filter(ursus.Col("total_revenue").Eq(ursus.Col("max_revenue"))).
		Select(
			ursus.Col("s_suppkey"), ursus.Col("s_name"), ursus.Col("s_address"),
			ursus.Col("s_phone"), ursus.Col("total_revenue"),
		).
		Sort(ursus.Asc(ursus.Col("s_suppkey")))
}

// q16 — parts/supplier relationship. NOT IN becomes an anti join; the distinct
// count is an n_unique aggregate.
func q16(scan Scan) *ursus.LazyFrame {
	complained := scan("supplier").
		Filter(ursus.Col("s_comment").Str().Contains("Customer.*Complaints", false)).
		Select(ursus.Col("s_suppkey"))

	return scan("partsupp").
		Join(complained,
			ursus.JoinLeftOn(ursus.Col("ps_suppkey")),
			ursus.JoinRightOn(ursus.Col("s_suppkey")),
			ursus.JoinHow(ursus.JoinAnti),
		).
		Join(scan("part"), on("ps_partkey", "p_partkey")...).
		Filter(
			ursus.Col("p_brand").Ne("Brand#45"),
			ursus.Col("p_type").Str().StartsWith("MEDIUM POLISHED").Not(),
			ursus.Col("p_size").IsIn(int64(49), 14, 23, 45, 19, 3, 36, 9),
		).
		GroupBy(ursus.Col("p_brand"), ursus.Col("p_type"), ursus.Col("p_size")).
		Agg(ursus.Col("ps_suppkey").NUnique().Alias("supplier_cnt")).
		Sort(
			ursus.Desc(ursus.Col("supplier_cnt")),
			ursus.Asc(ursus.Col("p_brand")),
			ursus.Asc(ursus.Col("p_type")),
			ursus.Asc(ursus.Col("p_size")),
		)
}

// q17 — small-quantity-order revenue. The correlated `0.2 * avg(l_quantity)`
// per part becomes a group-by joined back onto lineitem.
func q17(scan Scan) *ursus.LazyFrame {
	parts := scan("part").Filter(
		ursus.Col("p_brand").Eq("Brand#23"),
		ursus.Col("p_container").Eq("MED BOX"),
	)

	threshold := scan("lineitem").
		Join(parts.Select(ursus.Col("p_partkey")), on("l_partkey", "p_partkey")...).
		GroupBy(ursus.Col("l_partkey")).
		Agg(ursus.Col("l_quantity").Mean().Mul(0.2).Alias("threshold"))

	return scan("lineitem").
		Join(parts, on("l_partkey", "p_partkey")...).
		Join(threshold, ursus.JoinOn(ursus.Col("l_partkey"))).
		Filter(ursus.Col("l_quantity").Lt(ursus.Col("threshold"))).
		GroupBy().
		Agg(ursus.Col("l_extendedprice").Sum().Alias("total")).
		Select(ursus.Col("total").Div(7.0).Alias("avg_yearly"))
}

// q18 — large volume customer. The IN-with-HAVING subquery becomes a semi join
// against the pre-aggregated order keys.
func q18(scan Scan) *ursus.LazyFrame {
	bulkOrders := scan("lineitem").
		GroupBy(ursus.Col("l_orderkey")).
		Agg(ursus.Col("l_quantity").Sum().Alias("order_quantity")).
		Filter(ursus.Col("order_quantity").Gt(300.0)).
		Select(ursus.Col("l_orderkey"))

	return scan("orders").
		Join(bulkOrders,
			ursus.JoinLeftOn(ursus.Col("o_orderkey")),
			ursus.JoinRightOn(ursus.Col("l_orderkey")),
			ursus.JoinHow(ursus.JoinSemi),
		).
		Join(scan("customer"), on("o_custkey", "c_custkey")...).
		Join(scan("lineitem"), on("o_orderkey", "l_orderkey")...).
		GroupBy(
			ursus.Col("c_name"), ursus.Col("c_custkey"), ursus.Col("o_orderkey"),
			ursus.Col("o_orderdate"), ursus.Col("o_totalprice"),
		).
		Agg(ursus.Col("l_quantity").Sum().Alias("col6")).
		Sort(
			ursus.Desc(ursus.Col("o_totalprice")),
			ursus.Asc(ursus.Col("o_orderdate")),
		).
		Head(100)
}

// q19 — discounted revenue.
//
// The three disjuncts all require `p_partkey = l_partkey`, so this is an
// ordinary equi join followed by a filter over the joined rows. ursus has no
// JoinWhere (non-equi join) and would not benefit from one here: the shared
// equality is the only selective part of the predicate.
func q19(scan Scan) *ursus.LazyFrame {
	bucket := func(brand string, containers []string, low float64, maxSize int64) ursus.Expr {
		return ursus.Col("p_brand").Eq(brand).
			And(ursus.Col("p_container").IsIn(containers...)).
			And(ursus.Col("l_quantity").IsBetween(low, low+10)).
			And(ursus.Col("p_size").IsBetween(int64(1), maxSize))
	}

	return scan("lineitem").
		Join(scan("part"), on("l_partkey", "p_partkey")...).
		Filter(
			ursus.Col("l_shipmode").IsIn("AIR", "AIR REG"),
			ursus.Col("l_shipinstruct").Eq("DELIVER IN PERSON"),
			bucket("Brand#12", []string{"SM CASE", "SM BOX", "SM PACK", "SM PKG"}, 1, 5).
				Or(bucket("Brand#23", []string{"MED BAG", "MED BOX", "MED PKG", "MED PACK"}, 10, 10)).
				Or(bucket("Brand#34", []string{"LG CASE", "LG BOX", "LG PACK", "LG PKG"}, 20, 15)),
		).
		GroupBy().
		Agg(revenue().Sum().Alias("revenue"))
}

// q20 — potential part promotion. Two nested IN subqueries and a correlated
// threshold: a semi join, a group-by joined back, and a final semi join.
func q20(scan Scan) *ursus.LazyFrame {
	forestParts := scan("part").
		Filter(ursus.Col("p_name").Str().StartsWith("forest")).
		Select(ursus.Col("p_partkey"))

	shipped := scan("lineitem").
		Filter(ursus.Col("l_shipdate").IsBetween(
			date(1994, time.January, 1), date(1995, time.January, 1), ursus.ClosedLeft)).
		GroupBy(ursus.Col("l_partkey"), ursus.Col("l_suppkey")).
		Agg(ursus.Col("l_quantity").Sum().Mul(0.5).Alias("threshold"))

	oversupplied := scan("partsupp").
		Join(forestParts,
			ursus.JoinLeftOn(ursus.Col("ps_partkey")),
			ursus.JoinRightOn(ursus.Col("p_partkey")),
			ursus.JoinHow(ursus.JoinSemi),
		).
		Join(shipped,
			ursus.JoinLeftOn(ursus.Col("ps_partkey"), ursus.Col("ps_suppkey")),
			ursus.JoinRightOn(ursus.Col("l_partkey"), ursus.Col("l_suppkey")),
		).
		Filter(ursus.Col("ps_availqty").Gt(ursus.Col("threshold"))).
		Select(ursus.Col("ps_suppkey")).
		Unique()

	return scan("supplier").
		Join(
			scan("nation").Filter(ursus.Col("n_name").Eq("CANADA")),
			on("s_nationkey", "n_nationkey")...,
		).
		Join(oversupplied,
			ursus.JoinLeftOn(ursus.Col("s_suppkey")),
			ursus.JoinRightOn(ursus.Col("ps_suppkey")),
			ursus.JoinHow(ursus.JoinSemi),
		).
		Select(ursus.Col("s_name"), ursus.Col("s_address")).
		Sort(ursus.Asc(ursus.Col("s_name")))
}

// q21 — suppliers who kept orders waiting.
//
// The query is two correlated subqueries: EXISTS(another supplier on this order)
// and NOT EXISTS(another *late* supplier on this order). This is now written as
// exactly that, with the non-equi predicate l2.l_suppkey <> l1.l_suppkey that
// WhereExists exists to carry.
//
// # What it replaced, and why the old form was here
//
// Until step 44 ursus had no way to push that predicate, so both subqueries were
// expressed as NUnique() over l_suppkey grouped by l_orderkey — "how many distinct
// suppliers" instead of "is there another one". That worked and was expensive: step
// 38 measured the two accumulators at 74% of this query's live heap, over about 2.9
// million groups, and named it "the cost of working around a missing feature".
//
// The comment that used to sit here said the non-equi predicate was one "no
// dataframe API can push". That was true of ursus and of polars' join_where, which
// is inner-only; it is no longer true of this one.
func q21(scan Scan) *ursus.LazyFrame {
	late := func() *ursus.LazyFrame {
		return scan("lineitem").
			Filter(ursus.Col("l_receiptdate").Gt(ursus.Col("l_commitdate")))
	}
	sameOrderOtherSupplier := []ursus.Expr{
		ursus.Col("l_orderkey").Eq(ursus.Col("l_orderkey_right")),
		ursus.Col("l_suppkey").Ne(ursus.Col("l_suppkey_right")),
	}

	return late().
		// somebody else is on this order ...
		WhereExists(scan("lineitem"), sameOrderOtherSupplier...).
		// ... and this supplier is the only late one
		WhereNotExists(late(), sameOrderOtherSupplier...).
		Join(
			scan("orders").Filter(ursus.Col("o_orderstatus").Eq("F")),
			on("l_orderkey", "o_orderkey")...,
		).
		Join(scan("supplier"), on("l_suppkey", "s_suppkey")...).
		Join(
			scan("nation").Filter(ursus.Col("n_name").Eq("SAUDI ARABIA")),
			on("s_nationkey", "n_nationkey")...,
		).
		GroupBy(ursus.Col("s_name")).
		Agg(ursus.Len().Alias("numwait")).
		Sort(
			ursus.Desc(ursus.Col("numwait")),
			ursus.Asc(ursus.Col("s_name")),
		).
		Head(100)
}

// q22 — global sales opportunity. A phone-prefix substring, a scalar average
// cross-joined in, and NOT EXISTS as an anti join.
func q22(scan Scan) *ursus.LazyFrame {
	codes := []string{"13", "31", "23", "29", "30", "18", "17"}

	customers := scan("customer").
		WithColumns(ursus.Col("c_phone").Str().Slice(0, 2).Alias("cntrycode")).
		Filter(ursus.Col("cntrycode").IsIn(codes...))

	average := customers.
		Filter(ursus.Col("c_acctbal").Gt(0.0)).
		GroupBy().
		Agg(ursus.Col("c_acctbal").Mean().Alias("avg_acctbal"))

	return customers.
		Join(average, ursus.JoinHow(ursus.JoinCross)).
		Filter(ursus.Col("c_acctbal").Gt(ursus.Col("avg_acctbal"))).
		Join(
			scan("orders").Select(ursus.Col("o_custkey")),
			ursus.JoinLeftOn(ursus.Col("c_custkey")),
			ursus.JoinRightOn(ursus.Col("o_custkey")),
			ursus.JoinHow(ursus.JoinAnti),
		).
		GroupBy(ursus.Col("cntrycode")).
		Agg(
			ursus.Len().Alias("numcust"),
			ursus.Col("c_acctbal").Sum().Alias("totacctbal"),
		).
		Sort(ursus.Asc(ursus.Col("cntrycode")))
}

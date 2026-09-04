"""polars implementations of the PDS-H (TPC-H) queries.

Each function takes a `scan(table_name) -> LazyFrame` and returns a LazyFrame
whose collected result matches the corresponding engines/sql/pdsh/q*.sql.
Column names follow the SQL, because validate.py compares against the duckdb
reference by name. Row order only matters for the LIMIT queries (2, 3, 10, 18,
21); everything else is compared order-insensitively.

Subqueries become joins, which is the only vocabulary a dataframe API has:
EXISTS is a semi join, NOT IN is an anti join, a correlated aggregate is a
group-by followed by an equi join, and a scalar threshold is a one-row frame
cross-joined in so the whole query stays a single lazy plan.

Queries absent from QUERIES report `unsupported` rather than failing the run.
"""

from __future__ import annotations

from datetime import date
from typing import Callable

import polars as pl

Scan = Callable[[str], "pl.LazyFrame"]


def _revenue() -> pl.Expr:
    """`l_extendedprice * (1 - l_discount)`, the TPC-H revenue expression."""
    return pl.col("l_extendedprice") * (1 - pl.col("l_discount"))


def q1(scan: Scan):
    return (
        scan("lineitem")
        .filter(pl.col("l_shipdate") <= date(1998, 9, 2))
        .group_by("l_returnflag", "l_linestatus")
        .agg(
            pl.sum("l_quantity").alias("sum_qty"),
            pl.sum("l_extendedprice").alias("sum_base_price"),
            _revenue().sum().alias("sum_disc_price"),
            (_revenue() * (1 + pl.col("l_tax"))).sum().alias("sum_charge"),
            pl.mean("l_quantity").alias("avg_qty"),
            pl.mean("l_extendedprice").alias("avg_price"),
            pl.mean("l_discount").alias("avg_disc"),
            pl.len().alias("count_order"),
        )
        .sort("l_returnflag", "l_linestatus")
    )


def q2(scan: Scan):
    europe = (
        scan("supplier")
        .join(scan("nation"), left_on="s_nationkey", right_on="n_nationkey")
        .join(scan("region").filter(pl.col("r_name") == "EUROPE"),
              left_on="n_regionkey", right_on="r_regionkey")
        .join(scan("partsupp"), left_on="s_suppkey", right_on="ps_suppkey")
    )
    candidates = europe.join(
        scan("part").filter(
            (pl.col("p_size") == 15) & pl.col("p_type").str.ends_with("BRASS")
        ),
        left_on="ps_partkey",
        right_on="p_partkey",
    )
    # The correlated `min(ps_supplycost)` per part, computed once and joined back.
    cheapest = candidates.group_by("ps_partkey").agg(
        pl.min("ps_supplycost").alias("min_supplycost")
    )
    return (
        candidates.join(cheapest, on="ps_partkey")
        .filter(pl.col("ps_supplycost") == pl.col("min_supplycost"))
        .select(
            "s_acctbal", "s_name", "n_name",
            pl.col("ps_partkey").alias("p_partkey"),
            "p_mfgr", "s_address", "s_phone", "s_comment",
        )
        .sort(
            ["s_acctbal", "n_name", "s_name", "p_partkey"],
            descending=[True, False, False, False],
        )
        .head(100)
    )


def q3(scan: Scan):
    cutoff = date(1995, 3, 15)
    return (
        scan("customer").filter(pl.col("c_mktsegment") == "BUILDING")
        .join(scan("orders").filter(pl.col("o_orderdate") < cutoff),
              left_on="c_custkey", right_on="o_custkey")
        .join(scan("lineitem").filter(pl.col("l_shipdate") > cutoff),
              left_on="o_orderkey", right_on="l_orderkey")
        .group_by("o_orderkey", "o_orderdate", "o_shippriority")
        .agg(_revenue().sum().alias("revenue"))
        .select(
            pl.col("o_orderkey").alias("l_orderkey"),
            "revenue", "o_orderdate", "o_shippriority",
        )
        .sort(["revenue", "o_orderdate"], descending=[True, False])
        .head(10)
    )


def q4(scan: Scan):
    return (
        scan("orders")
        .filter(pl.col("o_orderdate").is_between(
            date(1993, 7, 1), date(1993, 10, 1), closed="left"))
        # EXISTS -> semi join: keep the order, take nothing from lineitem.
        .join(
            scan("lineitem").filter(pl.col("l_commitdate") < pl.col("l_receiptdate")),
            left_on="o_orderkey", right_on="l_orderkey", how="semi",
        )
        .group_by("o_orderpriority")
        .agg(pl.len().alias("order_count"))
        .sort("o_orderpriority")
    )


def q5(scan: Scan):
    return (
        scan("region").filter(pl.col("r_name") == "ASIA")
        .join(scan("nation"), left_on="r_regionkey", right_on="n_regionkey")
        .join(scan("supplier"), left_on="n_nationkey", right_on="s_nationkey")
        .join(scan("lineitem"), left_on="s_suppkey", right_on="l_suppkey")
        .join(scan("orders"), left_on="l_orderkey", right_on="o_orderkey")
        # c_nationkey = s_nationkey is folded into the join key, so the customer
        # must be in the same nation as the supplier.
        .join(scan("customer"),
              left_on=["o_custkey", "n_nationkey"],
              right_on=["c_custkey", "c_nationkey"])
        .filter(pl.col("o_orderdate").is_between(
            date(1994, 1, 1), date(1995, 1, 1), closed="left"))
        .group_by("n_name")
        .agg(_revenue().sum().alias("revenue"))
        .sort("revenue", descending=True)
    )


def q6(scan: Scan):
    return (
        scan("lineitem")
        .filter(
            pl.col("l_shipdate").is_between(date(1994, 1, 1), date(1995, 1, 1), closed="left"),
            pl.col("l_discount").is_between(0.05, 0.07),
            pl.col("l_quantity") < 24,
        )
        .select((pl.col("l_extendedprice") * pl.col("l_discount")).sum().alias("revenue"))
    )


def q7(scan: Scan):
    nation = scan("nation").select("n_nationkey", "n_name")

    def shipments(supplier_nation: str, customer_nation: str):
        return (
            scan("customer")
            .join(nation.filter(pl.col("n_name") == customer_nation),
                  left_on="c_nationkey", right_on="n_nationkey")
            .rename({"n_name": "cust_nation"})
            .join(scan("orders"), left_on="c_custkey", right_on="o_custkey")
            .join(scan("lineitem"), left_on="o_orderkey", right_on="l_orderkey")
            .join(scan("supplier"), left_on="l_suppkey", right_on="s_suppkey")
            .join(nation.filter(pl.col("n_name") == supplier_nation),
                  left_on="s_nationkey", right_on="n_nationkey")
            .rename({"n_name": "supp_nation"})
            .select("supp_nation", "cust_nation", "l_shipdate",
                    "l_extendedprice", "l_discount")
        )

    both_directions = pl.concat(
        [shipments("FRANCE", "GERMANY"), shipments("GERMANY", "FRANCE")]
    )
    return (
        both_directions
        .filter(pl.col("l_shipdate").is_between(date(1995, 1, 1), date(1996, 12, 31)))
        .with_columns(
            pl.col("l_shipdate").dt.year().alias("l_year"),
            _revenue().alias("volume"),
        )
        .group_by("supp_nation", "cust_nation", "l_year")
        .agg(pl.sum("volume").alias("revenue"))
        .sort("supp_nation", "cust_nation", "l_year")
    )


def q8(scan: Scan):
    customer_nation = scan("nation").select("n_nationkey", "n_regionkey")
    supplier_nation = scan("nation").select(
        pl.col("n_nationkey").alias("supp_nationkey"), pl.col("n_name").alias("nation")
    )
    return (
        scan("part").filter(pl.col("p_type") == "ECONOMY ANODIZED STEEL")
        .join(scan("lineitem"), left_on="p_partkey", right_on="l_partkey")
        .join(scan("supplier"), left_on="l_suppkey", right_on="s_suppkey")
        .join(scan("orders"), left_on="l_orderkey", right_on="o_orderkey")
        .filter(pl.col("o_orderdate").is_between(date(1995, 1, 1), date(1996, 12, 31)))
        .join(scan("customer"), left_on="o_custkey", right_on="c_custkey")
        .join(customer_nation, left_on="c_nationkey", right_on="n_nationkey")
        .join(scan("region").filter(pl.col("r_name") == "AMERICA"),
              left_on="n_regionkey", right_on="r_regionkey")
        .join(supplier_nation, left_on="s_nationkey", right_on="supp_nationkey")
        .with_columns(
            pl.col("o_orderdate").dt.year().alias("o_year"),
            _revenue().alias("volume"),
        )
        .group_by("o_year")
        .agg(
            (
                pl.when(pl.col("nation") == "BRAZIL")
                .then(pl.col("volume"))
                .otherwise(0)
                .sum()
                / pl.sum("volume")
            ).alias("mkt_share")
        )
        .sort("o_year")
    )


def q9(scan: Scan):
    return (
        scan("part").filter(pl.col("p_name").str.contains("green"))
        .join(scan("lineitem"), left_on="p_partkey", right_on="l_partkey")
        .join(scan("supplier"), left_on="l_suppkey", right_on="s_suppkey")
        .join(scan("partsupp"),
              left_on=["l_suppkey", "p_partkey"],
              right_on=["ps_suppkey", "ps_partkey"])
        .join(scan("orders"), left_on="l_orderkey", right_on="o_orderkey")
        .join(scan("nation"), left_on="s_nationkey", right_on="n_nationkey")
        .with_columns(
            pl.col("n_name").alias("nation"),
            pl.col("o_orderdate").dt.year().alias("o_year"),
            (_revenue() - pl.col("ps_supplycost") * pl.col("l_quantity")).alias("amount"),
        )
        .group_by("nation", "o_year")
        .agg(pl.sum("amount").alias("sum_profit"))
        .sort(["nation", "o_year"], descending=[False, True])
    )


def q10(scan: Scan):
    return (
        scan("customer")
        .join(
            scan("orders").filter(pl.col("o_orderdate").is_between(
                date(1993, 10, 1), date(1994, 1, 1), closed="left")),
            left_on="c_custkey", right_on="o_custkey",
        )
        .join(scan("lineitem").filter(pl.col("l_returnflag") == "R"),
              left_on="o_orderkey", right_on="l_orderkey")
        .join(scan("nation"), left_on="c_nationkey", right_on="n_nationkey")
        .group_by("c_custkey", "c_name", "c_acctbal", "c_phone", "n_name",
                  "c_address", "c_comment")
        .agg(_revenue().sum().alias("revenue"))
        .select("c_custkey", "c_name", "revenue", "c_acctbal", "n_name",
                "c_address", "c_phone", "c_comment")
        .sort("revenue", descending=True)
        .head(20)
    )


def q11(scan: Scan):
    german_stock = (
        scan("partsupp")
        .join(scan("supplier"), left_on="ps_suppkey", right_on="s_suppkey")
        .join(scan("nation").filter(pl.col("n_name") == "GERMANY"),
              left_on="s_nationkey", right_on="n_nationkey")
        .with_columns((pl.col("ps_supplycost") * pl.col("ps_availqty")).alias("value"))
    )
    # The HAVING threshold is a scalar. Cross-joining the one-row frame keeps the
    # whole query in one lazy plan instead of forcing an intermediate collect.
    threshold = german_stock.select((pl.sum("value") * 0.0001).alias("threshold"))
    return (
        german_stock.group_by("ps_partkey")
        .agg(pl.sum("value").alias("value"))
        .join(threshold, how="cross")
        .filter(pl.col("value") > pl.col("threshold"))
        .select("ps_partkey", "value")
        .sort("value", descending=True)
    )


def q12(scan: Scan):
    urgent = pl.col("o_orderpriority").is_in(["1-URGENT", "2-HIGH"])
    return (
        scan("orders")
        .join(scan("lineitem"), left_on="o_orderkey", right_on="l_orderkey")
        .filter(
            pl.col("l_shipmode").is_in(["MAIL", "SHIP"]),
            pl.col("l_commitdate") < pl.col("l_receiptdate"),
            pl.col("l_shipdate") < pl.col("l_commitdate"),
            pl.col("l_receiptdate").is_between(
                date(1994, 1, 1), date(1995, 1, 1), closed="left"),
        )
        .group_by("l_shipmode")
        .agg(
            pl.when(urgent).then(1).otherwise(0).sum().alias("high_line_count"),
            pl.when(urgent).then(0).otherwise(1).sum().alias("low_line_count"),
        )
        .sort("l_shipmode")
    )


def q13(scan: Scan):
    return (
        scan("customer")
        .join(
            # LIKE '%special%requests%' — the wildcards between the words make
            # this a regex, not a substring test.
            scan("orders").filter(~pl.col("o_comment").str.contains("special.*requests")),
            left_on="c_custkey", right_on="o_custkey", how="left",
        )
        .group_by("c_custkey")
        .agg(pl.col("o_orderkey").count().alias("c_count"))
        .group_by("c_count")
        .agg(pl.len().alias("custdist"))
        .sort(["custdist", "c_count"], descending=[True, True])
    )


def q14(scan: Scan):
    return (
        scan("lineitem")
        .filter(pl.col("l_shipdate").is_between(
            date(1995, 9, 1), date(1995, 10, 1), closed="left"))
        .join(scan("part"), left_on="l_partkey", right_on="p_partkey")
        .select(
            (
                100.00
                * pl.when(pl.col("p_type").str.starts_with("PROMO"))
                .then(_revenue())
                .otherwise(0)
                .sum()
                / _revenue().sum()
            ).alias("promo_revenue")
        )
    )


def q15(scan: Scan):
    revenue = (
        scan("lineitem")
        .filter(pl.col("l_shipdate").is_between(
            date(1996, 1, 1), date(1996, 4, 1), closed="left"))
        .group_by("l_suppkey")
        .agg(_revenue().sum().alias("total_revenue"))
    )
    best = revenue.select(pl.max("total_revenue").alias("max_revenue"))
    return (
        scan("supplier")
        .join(revenue, left_on="s_suppkey", right_on="l_suppkey")
        .join(best, how="cross")
        .filter(pl.col("total_revenue") == pl.col("max_revenue"))
        .select("s_suppkey", "s_name", "s_address", "s_phone", "total_revenue")
        .sort("s_suppkey")
    )


def q16(scan: Scan):
    complained = (
        scan("supplier")
        .filter(pl.col("s_comment").str.contains("Customer.*Complaints"))
        .select("s_suppkey")
    )
    return (
        scan("partsupp")
        # NOT IN -> anti join.
        .join(complained, left_on="ps_suppkey", right_on="s_suppkey", how="anti")
        .join(scan("part"), left_on="ps_partkey", right_on="p_partkey")
        .filter(
            pl.col("p_brand") != "Brand#45",
            ~pl.col("p_type").str.starts_with("MEDIUM POLISHED"),
            pl.col("p_size").is_in([49, 14, 23, 45, 19, 3, 36, 9]),
        )
        .group_by("p_brand", "p_type", "p_size")
        .agg(pl.col("ps_suppkey").n_unique().alias("supplier_cnt"))
        .sort(
            ["supplier_cnt", "p_brand", "p_type", "p_size"],
            descending=[True, False, False, False],
        )
    )


def q17(scan: Scan):
    parts = scan("part").filter(
        (pl.col("p_brand") == "Brand#23") & (pl.col("p_container") == "MED BOX")
    )
    line = scan("lineitem")
    # The correlated `0.2 * avg(l_quantity)` per part.
    threshold = (
        line.join(parts.select("p_partkey"), left_on="l_partkey", right_on="p_partkey")
        .group_by("l_partkey")
        .agg((0.2 * pl.mean("l_quantity")).alias("threshold"))
    )
    return (
        line.join(parts, left_on="l_partkey", right_on="p_partkey")
        .join(threshold, on="l_partkey")
        .filter(pl.col("l_quantity") < pl.col("threshold"))
        .select((pl.sum("l_extendedprice") / 7.0).alias("avg_yearly"))
    )


def q18(scan: Scan):
    bulk_orders = (
        scan("lineitem")
        .group_by("l_orderkey")
        .agg(pl.sum("l_quantity").alias("order_quantity"))
        .filter(pl.col("order_quantity") > 300)
        .select("l_orderkey")
    )
    return (
        scan("orders")
        # IN (subquery with HAVING) -> semi join.
        .join(bulk_orders, left_on="o_orderkey", right_on="l_orderkey", how="semi")
        .join(scan("customer"), left_on="o_custkey", right_on="c_custkey")
        .join(scan("lineitem"), left_on="o_orderkey", right_on="l_orderkey")
        .group_by("c_name", "o_custkey", "o_orderkey", "o_orderdate", "o_totalprice")
        .agg(pl.sum("l_quantity").alias("col6"))
        .select(
            "c_name",
            pl.col("o_custkey").alias("c_custkey"),
            "o_orderkey", "o_orderdate", "o_totalprice", "col6",
        )
        .sort(["o_totalprice", "o_orderdate"], descending=[True, False])
        .head(100)
    )


def q19(scan: Scan):
    def bucket(brand: str, containers: list[str], low: int, size: int) -> pl.Expr:
        return (
            (pl.col("p_brand") == brand)
            & pl.col("p_container").is_in(containers)
            & pl.col("l_quantity").is_between(low, low + 10)
            & pl.col("p_size").is_between(1, size)
        )

    return (
        scan("lineitem")
        # The three disjuncts share `p_partkey = l_partkey`, so the join is a
        # plain equi join and the OR becomes a filter over the joined rows.
        # ursus has no JoinWhere, and this shape is what every engine ends up
        # executing anyway.
        .join(scan("part"), left_on="l_partkey", right_on="p_partkey")
        .filter(
            pl.col("l_shipmode").is_in(["AIR", "AIR REG"]),
            pl.col("l_shipinstruct") == "DELIVER IN PERSON",
            bucket("Brand#12", ["SM CASE", "SM BOX", "SM PACK", "SM PKG"], 1, 5)
            | bucket("Brand#23", ["MED BAG", "MED BOX", "MED PKG", "MED PACK"], 10, 10)
            | bucket("Brand#34", ["LG CASE", "LG BOX", "LG PACK", "LG PKG"], 20, 15),
        )
        .select(_revenue().sum().alias("revenue"))
    )


def q20(scan: Scan):
    forest_parts = (
        scan("part").filter(pl.col("p_name").str.starts_with("forest")).select("p_partkey")
    )
    shipped = (
        scan("lineitem")
        .filter(pl.col("l_shipdate").is_between(
            date(1994, 1, 1), date(1995, 1, 1), closed="left"))
        .group_by("l_partkey", "l_suppkey")
        .agg((0.5 * pl.sum("l_quantity")).alias("threshold"))
    )
    oversupplied = (
        scan("partsupp")
        .join(forest_parts, left_on="ps_partkey", right_on="p_partkey", how="semi")
        .join(shipped,
              left_on=["ps_partkey", "ps_suppkey"],
              right_on=["l_partkey", "l_suppkey"])
        .filter(pl.col("ps_availqty") > pl.col("threshold"))
        .select("ps_suppkey")
        .unique()
    )
    return (
        scan("supplier")
        .join(scan("nation").filter(pl.col("n_name") == "CANADA"),
              left_on="s_nationkey", right_on="n_nationkey")
        .join(oversupplied, left_on="s_suppkey", right_on="ps_suppkey", how="semi")
        .select("s_name", "s_address")
        .sort("s_name")
    )


def q21(scan: Scan):
    line = scan("lineitem")
    late = line.filter(pl.col("l_receiptdate") > pl.col("l_commitdate"))

    # EXISTS(another supplier on this order) and NOT EXISTS(another *late*
    # supplier on this order) are both statements about distinct supplier counts
    # per order, so two group-bys replace two correlated subqueries.
    suppliers_per_order = line.group_by("l_orderkey").agg(
        pl.col("l_suppkey").n_unique().alias("n_suppliers")
    )
    late_suppliers_per_order = late.group_by("l_orderkey").agg(
        pl.col("l_suppkey").n_unique().alias("n_late_suppliers")
    )

    return (
        late
        .join(suppliers_per_order, on="l_orderkey")
        .join(late_suppliers_per_order, on="l_orderkey")
        .filter(
            pl.col("n_suppliers") > 1,       # somebody else is on the order
            pl.col("n_late_suppliers") == 1,  # and this row is the only late one
        )
        .join(scan("orders").filter(pl.col("o_orderstatus") == "F"),
              left_on="l_orderkey", right_on="o_orderkey")
        .join(scan("supplier"), left_on="l_suppkey", right_on="s_suppkey")
        .join(scan("nation").filter(pl.col("n_name") == "SAUDI ARABIA"),
              left_on="s_nationkey", right_on="n_nationkey")
        .group_by("s_name")
        .agg(pl.len().alias("numwait"))
        .sort(["numwait", "s_name"], descending=[True, False])
        .head(100)
    )


def q22(scan: Scan):
    codes = ["13", "31", "23", "29", "30", "18", "17"]
    customers = (
        scan("customer")
        .with_columns(pl.col("c_phone").str.slice(0, 2).alias("cntrycode"))
        .filter(pl.col("cntrycode").is_in(codes))
    )
    average = (
        customers.filter(pl.col("c_acctbal") > 0.0)
        .select(pl.mean("c_acctbal").alias("avg_acctbal"))
    )
    return (
        customers.join(average, how="cross")
        .filter(pl.col("c_acctbal") > pl.col("avg_acctbal"))
        # NOT EXISTS(orders for this customer) -> anti join.
        .join(scan("orders").select("o_custkey"),
              left_on="c_custkey", right_on="o_custkey", how="anti")
        .group_by("cntrycode")
        .agg(
            pl.len().alias("numcust"),
            pl.sum("c_acctbal").alias("totacctbal"),
        )
        .sort("cntrycode")
    )


QUERIES = {
    f"q{n}": globals()[f"q{n}"]
    for n in range(1, 23)
    if f"q{n}" in globals()
}

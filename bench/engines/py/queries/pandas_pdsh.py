"""pandas implementations of the PDS-H (TPC-H) queries.

Each function takes `read(table_name) -> DataFrame` and returns a DataFrame
matching engines/sql/pdsh/q*.sql by column name.

COLUMNS declares which columns each query needs so the reader can project.
Without it pandas would be charged for materialising l_comment — a string column
on every lineitem row that no query reads — while every other engine gets
projection pushdown for free. Semi and anti joins are written with `isin`, which
is what a pandas user reaches for and avoids materialising a join just to throw
the right side away.
"""

from __future__ import annotations

from datetime import datetime
from typing import Callable

import numpy as np
import pandas as pd

Read = Callable[[str], "pd.DataFrame"]

DATE_COLUMNS = {
    "lineitem": ("l_shipdate", "l_commitdate", "l_receiptdate"),
    "orders": ("o_orderdate",),
}

COLUMNS: dict[str, dict[str, list[str]]] = {
    "q1": {"lineitem": ["l_returnflag", "l_linestatus", "l_quantity", "l_extendedprice",
                        "l_discount", "l_tax", "l_shipdate"]},
    "q2": {"part": ["p_partkey", "p_size", "p_type", "p_mfgr"],
           "supplier": ["s_suppkey", "s_nationkey", "s_acctbal", "s_name", "s_address",
                        "s_phone", "s_comment"],
           "partsupp": ["ps_partkey", "ps_suppkey", "ps_supplycost"],
           "nation": ["n_nationkey", "n_name", "n_regionkey"],
           "region": ["r_regionkey", "r_name"]},
    "q3": {"customer": ["c_custkey", "c_mktsegment"],
           "orders": ["o_orderkey", "o_custkey", "o_orderdate", "o_shippriority"],
           "lineitem": ["l_orderkey", "l_extendedprice", "l_discount", "l_shipdate"]},
    "q4": {"orders": ["o_orderkey", "o_orderdate", "o_orderpriority"],
           "lineitem": ["l_orderkey", "l_commitdate", "l_receiptdate"]},
    "q5": {"customer": ["c_custkey", "c_nationkey"],
           "orders": ["o_orderkey", "o_custkey", "o_orderdate"],
           "lineitem": ["l_orderkey", "l_suppkey", "l_extendedprice", "l_discount"],
           "supplier": ["s_suppkey", "s_nationkey"],
           "nation": ["n_nationkey", "n_name", "n_regionkey"],
           "region": ["r_regionkey", "r_name"]},
    "q6": {"lineitem": ["l_extendedprice", "l_discount", "l_shipdate", "l_quantity"]},
    "q7": {"customer": ["c_custkey", "c_nationkey"],
           "orders": ["o_orderkey", "o_custkey"],
           "lineitem": ["l_orderkey", "l_suppkey", "l_shipdate", "l_extendedprice",
                        "l_discount"],
           "supplier": ["s_suppkey", "s_nationkey"],
           "nation": ["n_nationkey", "n_name"]},
    "q8": {"part": ["p_partkey", "p_type"],
           "supplier": ["s_suppkey", "s_nationkey"],
           "lineitem": ["l_partkey", "l_suppkey", "l_orderkey", "l_extendedprice",
                        "l_discount"],
           "orders": ["o_orderkey", "o_custkey", "o_orderdate"],
           "customer": ["c_custkey", "c_nationkey"],
           "nation": ["n_nationkey", "n_name", "n_regionkey"],
           "region": ["r_regionkey", "r_name"]},
    "q9": {"part": ["p_partkey", "p_name"],
           "supplier": ["s_suppkey", "s_nationkey"],
           "lineitem": ["l_partkey", "l_suppkey", "l_orderkey", "l_extendedprice",
                        "l_discount", "l_quantity"],
           "partsupp": ["ps_partkey", "ps_suppkey", "ps_supplycost"],
           "orders": ["o_orderkey", "o_orderdate"],
           "nation": ["n_nationkey", "n_name"]},
    "q10": {"customer": ["c_custkey", "c_name", "c_acctbal", "c_phone", "c_address",
                         "c_comment", "c_nationkey"],
            "orders": ["o_orderkey", "o_custkey", "o_orderdate"],
            "lineitem": ["l_orderkey", "l_extendedprice", "l_discount", "l_returnflag"],
            "nation": ["n_nationkey", "n_name"]},
    "q11": {"partsupp": ["ps_partkey", "ps_suppkey", "ps_supplycost", "ps_availqty"],
            "supplier": ["s_suppkey", "s_nationkey"],
            "nation": ["n_nationkey", "n_name"]},
    "q12": {"orders": ["o_orderkey", "o_orderpriority"],
            "lineitem": ["l_orderkey", "l_shipmode", "l_commitdate", "l_receiptdate",
                         "l_shipdate"]},
    "q13": {"customer": ["c_custkey"],
            "orders": ["o_orderkey", "o_custkey", "o_comment"]},
    "q14": {"lineitem": ["l_partkey", "l_extendedprice", "l_discount", "l_shipdate"],
            "part": ["p_partkey", "p_type"]},
    "q15": {"lineitem": ["l_suppkey", "l_extendedprice", "l_discount", "l_shipdate"],
            "supplier": ["s_suppkey", "s_name", "s_address", "s_phone"]},
    "q16": {"partsupp": ["ps_partkey", "ps_suppkey"],
            "part": ["p_partkey", "p_brand", "p_type", "p_size"],
            "supplier": ["s_suppkey", "s_comment"]},
    "q17": {"lineitem": ["l_partkey", "l_quantity", "l_extendedprice"],
            "part": ["p_partkey", "p_brand", "p_container"]},
    "q18": {"customer": ["c_custkey", "c_name"],
            "orders": ["o_orderkey", "o_custkey", "o_orderdate", "o_totalprice"],
            "lineitem": ["l_orderkey", "l_quantity"]},
    "q19": {"lineitem": ["l_partkey", "l_quantity", "l_extendedprice", "l_discount",
                         "l_shipmode", "l_shipinstruct"],
            "part": ["p_partkey", "p_brand", "p_container", "p_size"]},
    "q20": {"supplier": ["s_suppkey", "s_name", "s_address", "s_nationkey"],
            "nation": ["n_nationkey", "n_name"],
            "partsupp": ["ps_partkey", "ps_suppkey", "ps_availqty"],
            "part": ["p_partkey", "p_name"],
            "lineitem": ["l_partkey", "l_suppkey", "l_quantity", "l_shipdate"]},
    "q21": {"supplier": ["s_suppkey", "s_name", "s_nationkey"],
            "lineitem": ["l_orderkey", "l_suppkey", "l_receiptdate", "l_commitdate"],
            "orders": ["o_orderkey", "o_orderstatus"],
            "nation": ["n_nationkey", "n_name"]},
    "q22": {"customer": ["c_custkey", "c_phone", "c_acctbal"],
            "orders": ["o_custkey"]},
}


def _revenue(frame: pd.DataFrame) -> pd.Series:
    return frame["l_extendedprice"] * (1 - frame["l_discount"])


def q1(read: Read):
    line = read("lineitem")
    line = line[line["l_shipdate"] <= datetime(1998, 9, 2)]
    disc_price = _revenue(line)
    line = line.assign(disc_price=disc_price, charge=disc_price * (1 + line["l_tax"]))
    return line.groupby(["l_returnflag", "l_linestatus"], as_index=False, sort=True).agg(
        sum_qty=("l_quantity", "sum"),
        sum_base_price=("l_extendedprice", "sum"),
        sum_disc_price=("disc_price", "sum"),
        sum_charge=("charge", "sum"),
        avg_qty=("l_quantity", "mean"),
        avg_price=("l_extendedprice", "mean"),
        avg_disc=("l_discount", "mean"),
        count_order=("l_quantity", "size"),
    )


def q2(read: Read):
    region = read("region")
    europe = region[region["r_name"] == "EUROPE"]
    candidates = (
        read("supplier")
        .merge(read("nation"), left_on="s_nationkey", right_on="n_nationkey")
        .merge(europe, left_on="n_regionkey", right_on="r_regionkey")
        .merge(read("partsupp"), left_on="s_suppkey", right_on="ps_suppkey")
    )
    part = read("part")
    brass = part[(part["p_size"] == 15) & part["p_type"].str.endswith("BRASS")]
    candidates = candidates.merge(brass, left_on="ps_partkey", right_on="p_partkey")

    cheapest = candidates.groupby("p_partkey", as_index=False).agg(
        min_supplycost=("ps_supplycost", "min")
    )
    best = candidates.merge(cheapest, on="p_partkey")
    best = best[best["ps_supplycost"] == best["min_supplycost"]]
    return (
        best[["s_acctbal", "s_name", "n_name", "p_partkey", "p_mfgr", "s_address",
              "s_phone", "s_comment"]]
        .sort_values(["s_acctbal", "n_name", "s_name", "p_partkey"],
                     ascending=[False, True, True, True])
        .head(100)
    )


def q3(read: Read):
    cutoff = datetime(1995, 3, 15)
    customer = read("customer")
    orders = read("orders")
    line = read("lineitem")

    joined = (
        customer[customer["c_mktsegment"] == "BUILDING"]
        .merge(orders[orders["o_orderdate"] < cutoff],
               left_on="c_custkey", right_on="o_custkey")
        .merge(line[line["l_shipdate"] > cutoff],
               left_on="o_orderkey", right_on="l_orderkey")
    )
    joined = joined.assign(revenue=_revenue(joined))
    return (
        joined.groupby(["l_orderkey", "o_orderdate", "o_shippriority"], as_index=False)
        .agg(revenue=("revenue", "sum"))
        [["l_orderkey", "revenue", "o_orderdate", "o_shippriority"]]
        .sort_values(["revenue", "o_orderdate"], ascending=[False, True])
        .head(10)
    )


def q4(read: Read):
    orders = read("orders")
    line = read("lineitem")
    late = line[line["l_commitdate"] < line["l_receiptdate"]]
    selected = orders[
        (orders["o_orderdate"] >= datetime(1993, 7, 1))
        & (orders["o_orderdate"] < datetime(1993, 10, 1))
        # EXISTS -> membership test; no need to materialise the join.
        & orders["o_orderkey"].isin(late["l_orderkey"])
    ]
    return (
        selected.groupby("o_orderpriority", as_index=False)
        .agg(order_count=("o_orderkey", "size"))
        .sort_values("o_orderpriority")
    )


def q5(read: Read):
    region = read("region")
    joined = (
        region[region["r_name"] == "ASIA"]
        .merge(read("nation"), left_on="r_regionkey", right_on="n_regionkey")
        .merge(read("supplier"), left_on="n_nationkey", right_on="s_nationkey")
        .merge(read("lineitem"), left_on="s_suppkey", right_on="l_suppkey")
        .merge(read("orders"), left_on="l_orderkey", right_on="o_orderkey")
        # c_nationkey = s_nationkey folded into the key: the customer must be in
        # the same nation as the supplier.
        .merge(read("customer"),
               left_on=["o_custkey", "n_nationkey"],
               right_on=["c_custkey", "c_nationkey"])
    )
    joined = joined[
        (joined["o_orderdate"] >= datetime(1994, 1, 1))
        & (joined["o_orderdate"] < datetime(1995, 1, 1))
    ]
    joined = joined.assign(revenue=_revenue(joined))
    return (
        joined.groupby("n_name", as_index=False)
        .agg(revenue=("revenue", "sum"))
        .sort_values("revenue", ascending=False)
    )


def q6(read: Read):
    line = read("lineitem")
    selected = line[
        (line["l_shipdate"] >= datetime(1994, 1, 1))
        & (line["l_shipdate"] < datetime(1995, 1, 1))
        & (line["l_discount"] >= 0.05)
        & (line["l_discount"] <= 0.07)
        & (line["l_quantity"] < 24)
    ]
    revenue = (selected["l_extendedprice"] * selected["l_discount"]).sum()
    return pd.DataFrame({"revenue": [revenue]})


def q7(read: Read):
    nation = read("nation")
    customer, orders = read("customer"), read("orders")
    line, supplier = read("lineitem"), read("supplier")

    def shipments(supplier_nation: str, customer_nation: str) -> pd.DataFrame:
        n_cust = nation[nation["n_name"] == customer_nation].rename(
            columns={"n_nationkey": "cust_nationkey", "n_name": "cust_nation"}
        )
        n_supp = nation[nation["n_name"] == supplier_nation].rename(
            columns={"n_nationkey": "supp_nationkey", "n_name": "supp_nation"}
        )
        return (
            customer.merge(n_cust, left_on="c_nationkey", right_on="cust_nationkey")
            .merge(orders, left_on="c_custkey", right_on="o_custkey")
            .merge(line, left_on="o_orderkey", right_on="l_orderkey")
            .merge(supplier, left_on="l_suppkey", right_on="s_suppkey")
            .merge(n_supp, left_on="s_nationkey", right_on="supp_nationkey")
            [["supp_nation", "cust_nation", "l_shipdate", "l_extendedprice", "l_discount"]]
        )

    both = pd.concat(
        [shipments("FRANCE", "GERMANY"), shipments("GERMANY", "FRANCE")],
        ignore_index=True,
    )
    both = both[
        (both["l_shipdate"] >= datetime(1995, 1, 1))
        & (both["l_shipdate"] <= datetime(1996, 12, 31))
    ]
    both = both.assign(l_year=both["l_shipdate"].dt.year, volume=_revenue(both))
    return (
        both.groupby(["supp_nation", "cust_nation", "l_year"], as_index=False)
        .agg(revenue=("volume", "sum"))
        .sort_values(["supp_nation", "cust_nation", "l_year"])
    )


def q8(read: Read):
    nation = read("nation")
    customer_nation = nation[["n_nationkey", "n_regionkey"]].rename(
        columns={"n_nationkey": "cust_nationkey", "n_regionkey": "cust_regionkey"}
    )
    supplier_nation = nation[["n_nationkey", "n_name"]].rename(
        columns={"n_nationkey": "supp_nationkey", "n_name": "nation"}
    )
    part, region = read("part"), read("region")

    joined = (
        part[part["p_type"] == "ECONOMY ANODIZED STEEL"]
        .merge(read("lineitem"), left_on="p_partkey", right_on="l_partkey")
        .merge(read("supplier"), left_on="l_suppkey", right_on="s_suppkey")
        .merge(read("orders"), left_on="l_orderkey", right_on="o_orderkey")
    )
    joined = joined[
        (joined["o_orderdate"] >= datetime(1995, 1, 1))
        & (joined["o_orderdate"] <= datetime(1996, 12, 31))
    ]
    joined = (
        joined.merge(read("customer"), left_on="o_custkey", right_on="c_custkey")
        .merge(customer_nation, left_on="c_nationkey", right_on="cust_nationkey")
        .merge(region[region["r_name"] == "AMERICA"],
               left_on="cust_regionkey", right_on="r_regionkey")
        .merge(supplier_nation, left_on="s_nationkey", right_on="supp_nationkey")
    )
    volume = _revenue(joined)
    joined = joined.assign(
        o_year=joined["o_orderdate"].dt.year,
        volume=volume,
        brazil_volume=np.where(joined["nation"] == "BRAZIL", volume, 0.0),
    )
    grouped = joined.groupby("o_year", as_index=False).agg(
        brazil=("brazil_volume", "sum"), total=("volume", "sum")
    )
    grouped["mkt_share"] = grouped["brazil"] / grouped["total"]
    return grouped[["o_year", "mkt_share"]].sort_values("o_year")


def q9(read: Read):
    part = read("part")
    joined = (
        part[part["p_name"].str.contains("green", regex=False)]
        .merge(read("lineitem"), left_on="p_partkey", right_on="l_partkey")
        .merge(read("supplier"), left_on="l_suppkey", right_on="s_suppkey")
        .merge(read("partsupp"),
               left_on=["l_suppkey", "p_partkey"],
               right_on=["ps_suppkey", "ps_partkey"])
        .merge(read("orders"), left_on="l_orderkey", right_on="o_orderkey")
        .merge(read("nation"), left_on="s_nationkey", right_on="n_nationkey")
    )
    joined = joined.assign(
        nation=joined["n_name"],
        o_year=joined["o_orderdate"].dt.year,
        amount=_revenue(joined) - joined["ps_supplycost"] * joined["l_quantity"],
    )
    return (
        joined.groupby(["nation", "o_year"], as_index=False)
        .agg(sum_profit=("amount", "sum"))
        .sort_values(["nation", "o_year"], ascending=[True, False])
    )


def q10(read: Read):
    orders, line = read("orders"), read("lineitem")
    joined = (
        read("customer")
        .merge(
            orders[(orders["o_orderdate"] >= datetime(1993, 10, 1))
                   & (orders["o_orderdate"] < datetime(1994, 1, 1))],
            left_on="c_custkey", right_on="o_custkey",
        )
        .merge(line[line["l_returnflag"] == "R"],
               left_on="o_orderkey", right_on="l_orderkey")
        .merge(read("nation"), left_on="c_nationkey", right_on="n_nationkey")
    )
    joined = joined.assign(revenue=_revenue(joined))
    return (
        joined.groupby(["c_custkey", "c_name", "c_acctbal", "c_phone", "n_name",
                        "c_address", "c_comment"], as_index=False)
        .agg(revenue=("revenue", "sum"))
        [["c_custkey", "c_name", "revenue", "c_acctbal", "n_name", "c_address",
          "c_phone", "c_comment"]]
        .sort_values("revenue", ascending=False)
        .head(20)
    )


def q11(read: Read):
    nation = read("nation")
    german_stock = (
        read("partsupp")
        .merge(read("supplier"), left_on="ps_suppkey", right_on="s_suppkey")
        .merge(nation[nation["n_name"] == "GERMANY"],
               left_on="s_nationkey", right_on="n_nationkey")
    )
    german_stock = german_stock.assign(
        value=german_stock["ps_supplycost"] * german_stock["ps_availqty"]
    )
    threshold = german_stock["value"].sum() * 0.0001
    grouped = german_stock.groupby("ps_partkey", as_index=False).agg(value=("value", "sum"))
    return grouped[grouped["value"] > threshold].sort_values("value", ascending=False)


def q12(read: Read):
    joined = read("orders").merge(read("lineitem"),
                                  left_on="o_orderkey", right_on="l_orderkey")
    joined = joined[
        joined["l_shipmode"].isin(["MAIL", "SHIP"])
        & (joined["l_commitdate"] < joined["l_receiptdate"])
        & (joined["l_shipdate"] < joined["l_commitdate"])
        & (joined["l_receiptdate"] >= datetime(1994, 1, 1))
        & (joined["l_receiptdate"] < datetime(1995, 1, 1))
    ]
    urgent = joined["o_orderpriority"].isin(["1-URGENT", "2-HIGH"])
    joined = joined.assign(
        high_line=np.where(urgent, 1, 0), low_line=np.where(urgent, 0, 1)
    )
    return (
        joined.groupby("l_shipmode", as_index=False)
        .agg(high_line_count=("high_line", "sum"), low_line_count=("low_line", "sum"))
        .sort_values("l_shipmode")
    )


def q13(read: Read):
    orders = read("orders")
    # LIKE '%special%requests%' — the wildcards between the words make this a
    # regex, not a substring test.
    ordinary = orders[~orders["o_comment"].str.contains("special.*requests", regex=True)]
    per_customer = (
        read("customer")
        .merge(ordinary, left_on="c_custkey", right_on="o_custkey", how="left")
        .groupby("c_custkey", as_index=False)
        .agg(c_count=("o_orderkey", "count"))
    )
    return (
        per_customer.groupby("c_count", as_index=False)
        .agg(custdist=("c_custkey", "size"))
        .sort_values(["custdist", "c_count"], ascending=[False, False])
    )


def q14(read: Read):
    line = read("lineitem")
    window = line[
        (line["l_shipdate"] >= datetime(1995, 9, 1))
        & (line["l_shipdate"] < datetime(1995, 10, 1))
    ]
    joined = window.merge(read("part"), left_on="l_partkey", right_on="p_partkey")
    revenue = _revenue(joined)
    promo = np.where(joined["p_type"].str.startswith("PROMO"), revenue, 0.0)
    return pd.DataFrame({"promo_revenue": [100.00 * promo.sum() / revenue.sum()]})


def q15(read: Read):
    line = read("lineitem")
    window = line[
        (line["l_shipdate"] >= datetime(1996, 1, 1))
        & (line["l_shipdate"] < datetime(1996, 4, 1))
    ]
    window = window.assign(revenue=_revenue(window))
    by_supplier = window.groupby("l_suppkey", as_index=False).agg(
        total_revenue=("revenue", "sum")
    )
    best = by_supplier["total_revenue"].max()
    top = by_supplier[by_supplier["total_revenue"] == best]
    return (
        read("supplier")
        .merge(top, left_on="s_suppkey", right_on="l_suppkey")
        [["s_suppkey", "s_name", "s_address", "s_phone", "total_revenue"]]
        .sort_values("s_suppkey")
    )


def q16(read: Read):
    supplier = read("supplier")
    complained = supplier[
        supplier["s_comment"].str.contains("Customer.*Complaints", regex=True)
    ]["s_suppkey"]

    partsupp = read("partsupp")
    # NOT IN -> negated membership test.
    partsupp = partsupp[~partsupp["ps_suppkey"].isin(complained)]

    joined = partsupp.merge(read("part"), left_on="ps_partkey", right_on="p_partkey")
    joined = joined[
        (joined["p_brand"] != "Brand#45")
        & ~joined["p_type"].str.startswith("MEDIUM POLISHED")
        & joined["p_size"].isin([49, 14, 23, 45, 19, 3, 36, 9])
    ]
    return (
        joined.groupby(["p_brand", "p_type", "p_size"], as_index=False)
        .agg(supplier_cnt=("ps_suppkey", "nunique"))
        .sort_values(["supplier_cnt", "p_brand", "p_type", "p_size"],
                     ascending=[False, True, True, True])
    )


def q17(read: Read):
    part = read("part")
    parts = part[(part["p_brand"] == "Brand#23") & (part["p_container"] == "MED BOX")]
    line = read("lineitem")

    relevant = line.merge(parts[["p_partkey"]], left_on="l_partkey", right_on="p_partkey")
    threshold = relevant.groupby("l_partkey", as_index=False).agg(
        threshold=("l_quantity", "mean")
    )
    threshold["threshold"] *= 0.2

    joined = relevant.merge(threshold, on="l_partkey")
    small = joined[joined["l_quantity"] < joined["threshold"]]
    return pd.DataFrame({"avg_yearly": [small["l_extendedprice"].sum() / 7.0]})


def q18(read: Read):
    line = read("lineitem")
    per_order = line.groupby("l_orderkey", as_index=False).agg(
        order_quantity=("l_quantity", "sum")
    )
    bulk = per_order[per_order["order_quantity"] > 300]["l_orderkey"]

    orders = read("orders")
    joined = (
        orders[orders["o_orderkey"].isin(bulk)]
        .merge(read("customer"), left_on="o_custkey", right_on="c_custkey")
        .merge(line, left_on="o_orderkey", right_on="l_orderkey")
    )
    return (
        joined.groupby(["c_name", "c_custkey", "o_orderkey", "o_orderdate",
                        "o_totalprice"], as_index=False)
        .agg(col6=("l_quantity", "sum"))
        .sort_values(["o_totalprice", "o_orderdate"], ascending=[False, True])
        .head(100)
    )


def q19(read: Read):
    joined = read("lineitem").merge(read("part"),
                                    left_on="l_partkey", right_on="p_partkey")

    def bucket(brand: str, containers: list[str], low: int, size: int) -> pd.Series:
        return (
            (joined["p_brand"] == brand)
            & joined["p_container"].isin(containers)
            & (joined["l_quantity"] >= low)
            & (joined["l_quantity"] <= low + 10)
            & (joined["p_size"] >= 1)
            & (joined["p_size"] <= size)
        )

    selected = joined[
        joined["l_shipmode"].isin(["AIR", "AIR REG"])
        & (joined["l_shipinstruct"] == "DELIVER IN PERSON")
        & (
            bucket("Brand#12", ["SM CASE", "SM BOX", "SM PACK", "SM PKG"], 1, 5)
            | bucket("Brand#23", ["MED BAG", "MED BOX", "MED PKG", "MED PACK"], 10, 10)
            | bucket("Brand#34", ["LG CASE", "LG BOX", "LG PACK", "LG PKG"], 20, 15)
        )
    ]
    return pd.DataFrame({"revenue": [_revenue(selected).sum()]})


def q20(read: Read):
    part = read("part")
    forest = part[part["p_name"].str.startswith("forest")]["p_partkey"]

    line = read("lineitem")
    window = line[
        (line["l_shipdate"] >= datetime(1994, 1, 1))
        & (line["l_shipdate"] < datetime(1995, 1, 1))
    ]
    shipped = window.groupby(["l_partkey", "l_suppkey"], as_index=False).agg(
        threshold=("l_quantity", "sum")
    )
    shipped["threshold"] *= 0.5

    partsupp = read("partsupp")
    candidates = partsupp[partsupp["ps_partkey"].isin(forest)].merge(
        shipped, left_on=["ps_partkey", "ps_suppkey"], right_on=["l_partkey", "l_suppkey"]
    )
    oversupplied = candidates[
        candidates["ps_availqty"] > candidates["threshold"]
    ]["ps_suppkey"].unique()

    nation = read("nation")
    supplier = read("supplier").merge(
        nation[nation["n_name"] == "CANADA"], left_on="s_nationkey", right_on="n_nationkey"
    )
    return (
        supplier[supplier["s_suppkey"].isin(oversupplied)][["s_name", "s_address"]]
        .sort_values("s_name")
    )


def q21(read: Read):
    line = read("lineitem")
    late = line[line["l_receiptdate"] > line["l_commitdate"]]

    # EXISTS(another supplier on this order) and NOT EXISTS(another *late*
    # supplier) are both statements about distinct supplier counts per order.
    suppliers = line.groupby("l_orderkey", as_index=False).agg(
        n_suppliers=("l_suppkey", "nunique")
    )
    late_suppliers = late.groupby("l_orderkey", as_index=False).agg(
        n_late_suppliers=("l_suppkey", "nunique")
    )

    candidates = late.merge(suppliers, on="l_orderkey").merge(late_suppliers, on="l_orderkey")
    candidates = candidates[
        (candidates["n_suppliers"] > 1) & (candidates["n_late_suppliers"] == 1)
    ]

    orders = read("orders")
    nation = read("nation")
    joined = (
        candidates.merge(orders[orders["o_orderstatus"] == "F"],
                         left_on="l_orderkey", right_on="o_orderkey")
        .merge(read("supplier"), left_on="l_suppkey", right_on="s_suppkey")
        .merge(nation[nation["n_name"] == "SAUDI ARABIA"],
               left_on="s_nationkey", right_on="n_nationkey")
    )
    return (
        joined.groupby("s_name", as_index=False)
        .agg(numwait=("l_orderkey", "size"))
        .sort_values(["numwait", "s_name"], ascending=[False, True])
        .head(100)
    )


def q22(read: Read):
    codes = ["13", "31", "23", "29", "30", "18", "17"]
    customer = read("customer")
    customer = customer.assign(cntrycode=customer["c_phone"].str.slice(0, 2))
    selected = customer[customer["cntrycode"].isin(codes)]

    average = selected[selected["c_acctbal"] > 0.0]["c_acctbal"].mean()
    rich = selected[selected["c_acctbal"] > average]
    # NOT EXISTS(orders for this customer) -> negated membership test.
    orderless = rich[~rich["c_custkey"].isin(read("orders")["o_custkey"])]

    return (
        orderless.groupby("cntrycode", as_index=False)
        .agg(numcust=("c_custkey", "size"), totacctbal=("c_acctbal", "sum"))
        .sort_values("cntrycode")
    )


QUERIES = {f"q{n}": globals()[f"q{n}"] for n in range(1, 23) if f"q{n}" in globals()}

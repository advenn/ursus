-- TPC-H query 18, at the validation substitution parameters.
-- Source: duckdb's `tpch` extension (tpch_queries()), which reproduces the
-- queries from the TPC-H specification verbatim. Regenerate with
--   make sql
-- Tables are referenced by bare name; each SQL engine registers views over
-- the normalised parquet (or csv) tree before running this.
SELECT
    c_name,
    c_custkey,
    o_orderkey,
    o_orderdate,
    o_totalprice,
    sum(l_quantity) AS col6
FROM
    customer,
    orders,
    lineitem
WHERE
    o_orderkey IN (
        SELECT
            l_orderkey
        FROM
            lineitem
        GROUP BY
            l_orderkey
        HAVING
            sum(l_quantity) > 300)
    AND c_custkey = o_custkey
    AND o_orderkey = l_orderkey
GROUP BY
    c_name,
    c_custkey,
    o_orderkey,
    o_orderdate,
    o_totalprice
ORDER BY
    o_totalprice DESC,
    o_orderdate
LIMIT 100;

-- TPC-H query 4, at the validation substitution parameters.
-- Source: duckdb's `tpch` extension (tpch_queries()), which reproduces the
-- queries from the TPC-H specification verbatim. Regenerate with
--   make sql
-- Tables are referenced by bare name; each SQL engine registers views over
-- the normalised parquet (or csv) tree before running this.
SELECT
    o_orderpriority,
    count(*) AS order_count
FROM
    orders
WHERE
    o_orderdate >= CAST('1993-07-01' AS date)
    AND o_orderdate < CAST('1993-10-01' AS date)
    AND EXISTS (
        SELECT
            *
        FROM
            lineitem
        WHERE
            l_orderkey = o_orderkey
            AND l_commitdate < l_receiptdate)
GROUP BY
    o_orderpriority
ORDER BY
    o_orderpriority;

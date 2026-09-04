-- TPC-H query 13, at the validation substitution parameters.
-- Source: duckdb's `tpch` extension (tpch_queries()), which reproduces the
-- queries from the TPC-H specification verbatim. Regenerate with
--   make sql
-- Tables are referenced by bare name; each SQL engine registers views over
-- the normalised parquet (or csv) tree before running this.
SELECT
    c_count,
    count(*) AS custdist
FROM (
    SELECT
        c_custkey,
        count(o_orderkey)
    FROM
        customer
    LEFT OUTER JOIN orders ON c_custkey = o_custkey
    AND o_comment NOT LIKE '%special%requests%'
GROUP BY
    c_custkey) AS c_orders (c_custkey,
        c_count)
GROUP BY
    c_count
ORDER BY
    custdist DESC,
    c_count DESC;

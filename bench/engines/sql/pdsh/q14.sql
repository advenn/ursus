-- TPC-H query 14, at the validation substitution parameters.
-- Source: duckdb's `tpch` extension (tpch_queries()), which reproduces the
-- queries from the TPC-H specification verbatim. Regenerate with
--   make sql
-- Tables are referenced by bare name; each SQL engine registers views over
-- the normalised parquet (or csv) tree before running this.
SELECT
    100.00 * sum(
        CASE WHEN p_type LIKE 'PROMO%' THEN
            l_extendedprice * (1 - l_discount)
        ELSE
            0
        END) / sum(l_extendedprice * (1 - l_discount)) AS promo_revenue
FROM
    lineitem,
    part
WHERE
    l_partkey = p_partkey
    AND l_shipdate >= date '1995-09-01'
    AND l_shipdate < CAST('1995-10-01' AS date);

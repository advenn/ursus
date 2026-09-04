-- h2o.ai db-benchmark, largest two v3 by id6.
-- Ported from h2oai/db-benchmark (groupby-duckdb.R q8). Tables are registered as
-- views named g1 / x / small / medium / big before this runs.
SELECT id6, v3 AS largest2_v3
FROM (
    SELECT id6, v3, row_number() OVER (PARTITION BY id6 ORDER BY v3 DESC) AS order_v3
    FROM g1
    WHERE v3 IS NOT NULL
) sub
WHERE order_v3 <= 2

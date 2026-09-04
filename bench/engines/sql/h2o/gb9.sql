-- h2o.ai db-benchmark, regression v1 v2 by id2 id4.
-- Ported from h2oai/db-benchmark (groupby-duckdb.R q9). Tables are registered as
-- views named g1 / x / small / medium / big before this runs.
SELECT id2, id4, pow(corr(v1, v2), 2) AS r2
FROM g1
GROUP BY id2, id4

-- h2o.ai db-benchmark, mean v1:v3 by id4.
-- Ported from h2oai/db-benchmark (groupby-duckdb.R q4). Tables are registered as
-- views named g1 / x / small / medium / big before this runs.
SELECT id4, avg(v1) AS v1, avg(v2) AS v2, avg(v3) AS v3
FROM g1
GROUP BY id4

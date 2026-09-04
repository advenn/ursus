-- h2o.ai db-benchmark, sum v1:v3 by id6.
-- Ported from h2oai/db-benchmark (groupby-duckdb.R q5). Tables are registered as
-- views named g1 / x / small / medium / big before this runs.
SELECT id6, sum(v1) AS v1, sum(v2) AS v2, sum(v3) AS v3
FROM g1
GROUP BY id6

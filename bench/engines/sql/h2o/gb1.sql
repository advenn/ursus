-- h2o.ai db-benchmark, sum v1 by id1.
-- Ported from h2oai/db-benchmark (groupby-duckdb.R q1). Tables are registered as
-- views named g1 / x / small / medium / big before this runs.
SELECT id1, sum(v1) AS v1
FROM g1
GROUP BY id1

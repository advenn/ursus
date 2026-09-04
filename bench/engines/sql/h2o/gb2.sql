-- h2o.ai db-benchmark, sum v1 by id1:id2.
-- Ported from h2oai/db-benchmark (groupby-duckdb.R q2). Tables are registered as
-- views named g1 / x / small / medium / big before this runs.
SELECT id1, id2, sum(v1) AS v1
FROM g1
GROUP BY id1, id2

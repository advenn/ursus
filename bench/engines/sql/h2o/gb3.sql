-- h2o.ai db-benchmark, sum v1 mean v3 by id3.
-- Ported from h2oai/db-benchmark (groupby-duckdb.R q3). Tables are registered as
-- views named g1 / x / small / medium / big before this runs.
SELECT id3, sum(v1) AS v1, avg(v3) AS v3
FROM g1
GROUP BY id3

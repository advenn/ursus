-- h2o.ai db-benchmark, max v1 - min v2 by id3.
-- Ported from h2oai/db-benchmark (groupby-duckdb.R q7). Tables are registered as
-- views named g1 / x / small / medium / big before this runs.
SELECT id3, max(v1) - min(v2) AS range_v1_v2
FROM g1
GROUP BY id3

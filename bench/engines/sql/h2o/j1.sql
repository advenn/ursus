-- h2o.ai db-benchmark, small inner on int.
-- Ported from h2oai/db-benchmark (join-duckdb.R q1). Tables are registered as
-- views named g1 / x / small / medium / big before this runs.
SELECT x.*, small.id4 AS small_id4, small.v2 AS v2
FROM x
INNER JOIN small USING (id1)

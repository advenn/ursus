-- h2o.ai db-benchmark, medium left on int.
-- Ported from h2oai/db-benchmark (join-duckdb.R q3). Tables are registered as
-- views named g1 / x / small / medium / big before this runs.
SELECT x.*, medium.id1 AS medium_id1, medium.id4 AS medium_id4,
       medium.id5 AS medium_id5, medium.v2 AS v2
FROM x
LEFT JOIN medium USING (id2)

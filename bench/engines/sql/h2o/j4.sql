-- h2o.ai db-benchmark, medium inner on factor.
-- Ported from h2oai/db-benchmark (join-duckdb.R q4). Tables are registered as
-- views named g1 / x / small / medium / big before this runs.
SELECT x.*, medium.id1 AS medium_id1, medium.id2 AS medium_id2,
       medium.id4 AS medium_id4, medium.v2 AS v2
FROM x
INNER JOIN medium USING (id5)

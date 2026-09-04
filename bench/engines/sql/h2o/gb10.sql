-- h2o.ai db-benchmark, sum v3 count by id1:id6.
-- Ported from h2oai/db-benchmark (groupby-duckdb.R q10). Tables are registered as
-- views named g1 / x / small / medium / big before this runs.
SELECT id1, id2, id3, id4, id5, id6, sum(v3) AS v3, count(*) AS cnt
FROM g1
GROUP BY id1, id2, id3, id4, id5, id6

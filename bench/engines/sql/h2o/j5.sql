-- h2o.ai db-benchmark, big inner on int.
-- Ported from h2oai/db-benchmark (join-duckdb.R q5). Tables are registered as
-- views named g1 / x / small / medium / big before this runs.
SELECT x.*, big.id1 AS big_id1, big.id2 AS big_id2, big.id4 AS big_id4,
       big.id5 AS big_id5, big.id6 AS big_id6, big.v2 AS v2
FROM x
INNER JOIN big USING (id3)

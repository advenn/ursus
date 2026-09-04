-- h2o.ai db-benchmark, median v3 sd v3 by id4 id5.
-- Ported from h2oai/db-benchmark (groupby-duckdb.R q6). Tables are registered as
-- views named g1 / x / small / medium / big before this runs.
SELECT id4, id5, quantile_cont(v3, 0.5) AS median_v3, stddev(v3) AS sd_v3
FROM g1
GROUP BY id4, id5

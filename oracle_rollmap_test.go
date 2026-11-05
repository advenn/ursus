package ursus_test

// The rollmap group (step 500, from rollmap_test.go's tables): rolling_rank_by and
// rolling_map, each spelled as bench/scripts/oracle/rollmap.py asks Polars; the Go
// functions are the Python ones' twins.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	x, t, g := c("x"), c("t"), c("g")
	every, min, center, closed := ursus.Every, ursus.MinSamples, ursus.RollingCenter, ursus.WindowClosed
	avg, rmin, rmax, dense := ursus.RankAverage, ursus.RankMin, ursus.RankMax, ursus.RankDense
	sel := func(e ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(e.Alias("r")) }
	}
	sum := func(w []float64, ok []bool) (float64, error) {
		s := 0.0
		for i, v := range w {
			if ok[i] {
				s += v
			}
		}
		return s, nil
	}
	length := func(w []float64, _ []bool) (float64, error) { return float64(len(w)), nil }
	greater := func(w []int64, _ []bool) (int64, error) { return max(w[0], w[1]), nil }
	three := every("3i")
	oracleGroups["rollmap"] = map[string]oracleQuery{
		"rank_by_average":         sel(x.RollingRankBy(t, three, avg)),
		"rank_by_min":             sel(x.RollingRankBy(t, three, rmin)),
		"rank_by_max":             sel(x.RollingRankBy(t, three, rmax)),
		"rank_by_dense":           sel(x.RollingRankBy(t, three, dense)),
		"rank_by_min_samples_2":   sel(x.RollingRankBy(t, three, avg, min(2))),
		"rank_by_closed_both":     sel(x.RollingRankBy(t, three, avg, closed(ursus.ClosedBoth))),
		"map_sum_3":               sel(x.RollingMap[float64, float64]("sum", ursus.Float64, 3, sum)),
		"map_sum_3_min_samples_1": sel(x.RollingMap[float64, float64]("sum", ursus.Float64, 3, sum, min(1))),
		"map_sum_3_center":        sel(x.RollingMap[float64, float64]("sum", ursus.Float64, 3, sum, center())),
		"map_len_3":               sel(x.RollingMap[float64, float64]("len", ursus.Float64, 3, length, min(1))),
		"map_max_ints":            sel(x.RollingMap[int64, int64]("max", ursus.Int64, 2, greater)),
		"map_sum_over_g_by_o": sel(x.RollingMap[float64, float64]("sum", ursus.Float64, 2, sum, min(1)).
			OverWith(ursus.WindowSpec{PartitionBy: []ursus.Expr{g}, OrderBy: []ursus.SortKey{ursus.Asc(c("o"))}})),
		"rank_by_gen": func(in oracleInputs) *ursus.LazyFrame {
			four := every("4i")
			return in.df().Select(x.RollingRankBy(t, four, avg).Alias("avg"), x.RollingRankBy(t, four, rmin).Alias("min"),
				x.RollingRankBy(t, four, rmax).Alias("max"), x.RollingRankBy(t, four, dense).Alias("dense"),
				x.RollingRankBy(t, every("6i"), avg, min(3)).Alias("avg6_3"), x.RollingRankBy(t, four, rmin, closed(ursus.ClosedBoth)).Alias("min_both"),
				x.RollingRankBy(t, every("5i"), avg).Over(g).Alias("avg5_g"))
		},
		"map_gen": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().Select(x.RollingMap[float64, float64]("sum4", ursus.Float64, 4, sum, min(2)).Alias("sum4_2"),
				x.RollingMap[float64, float64]("len3", ursus.Float64, 3, length, min(1)).Alias("len3"),
				x.RollingMap[float64, float64]("sum3c", ursus.Float64, 3, sum, min(1), center()).Alias("sum3c"))
		},
	}
}

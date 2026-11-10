package ursus_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestRollingRankByAndRollingMap is step 338: Polars' rolling_rank_by and
// rolling_map. Their answers are Polars 2.1.0's since step 500, recorded in the rollmap
// group of testdata/oracle and spelled in oracle_rollmap_test.go, which took this
// file's tables. What stays asserts the refusals, the names of two functions, a panic
// and an error in the function, and the spill past a limit.
func TestRollingRankByAndRollingMap(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	x, ti := c("x"), c("t")
	lf := ursus.Frame(i64s("t", 1, 2, 3, 5, 6, 9, 9), f64s("x", 3.0, 1.0, nil, 3.0, 5.0, 2.0, 2.0))
	sum := func(w []float64, ok []bool) (float64, error) {
		s := 0.0
		for i, v := range w {
			if ok[i] {
				s += v
			}
		}
		return s, nil
	}

	t.Run("rolling_rank_by", func(t *testing.T) {
		_, err := lf.Select(x.RollingRankBy(ti, ursus.Every("3i"), ursus.RankOrdinal)).Collect(ctx)
		if err == nil || !strings.Contains(err.Error(), "no ordinal") {
			t.Errorf("ordinal: %v", err)
		}
	})

	t.Run("rolling_map", func(t *testing.T) {
		// One function used twice is one; two of one name are refused, as two udfs are,
		// and a udf's name is theirs too.
		once := x.RollingMap[float64, float64]("once", ursus.Float64, 3, sum)
		if _, err := lf.Select(once.Alias("r"), once.Alias("s")).Collect(ctx); err != nil {
			t.Errorf("one function twice: %v", err)
		}
		for _, q := range []*ursus.LazyFrame{
			lf.Select(x.RollingMap[float64, float64]("f", ursus.Float64, 3, sum).Alias("r"),
				x.RollingMap[float64, float64]("f", ursus.Float64, 2, sum).Alias("s")),
			lf.Select(x.RollingMap[float64, float64]("f", ursus.Float64, 3, sum).Alias("r"),
				x.MapElements[float64, float64]("f", ursus.Float64, func(v float64) (float64, error) { return v, nil }).Alias("s")),
		} {
			if _, err := q.Collect(ctx); err == nil || !strings.Contains(err.Error(), "both named") {
				t.Errorf("two functions of one name: %v", err)
			}
		}
		_, err := lf.Select(x.RollingMap[float64, float64]("boom", ursus.Float64, 2,
			func([]float64, []bool) (float64, error) { panic("a bad window") }).Alias("r")).Collect(ctx)
		if err == nil || !strings.Contains(err.Error(), "a bad window") || !strings.Contains(err.Error(), "boom") {
			t.Errorf("a panic: %v", err)
		}
		_, err = lf.Select(x.RollingMap[float64, float64]("err", ursus.Float64, 2,
			func([]float64, []bool) (float64, error) { return 0, fmt.Errorf("refused") }).Alias("r")).Collect(ctx)
		if err == nil || !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), `udf "err" failed`) {
			t.Errorf("an error, naming the function and the row: %v", err)
		}
	})

	t.Run("past a memory limit", func(t *testing.T) {
		const n = 3000
		ts, v := make([]int64, n), make([]float64, n)
		for i := range n {
			ts[i], v[i] = int64(i/3), float64(i%17)
		}
		g := make([]int64, n)
		for i := range n {
			g[i] = int64(i % 50)
		}
		big := ursus.Frame(ursus.Values("t", ts), ursus.Values("g", g), ursus.Values("x", v))
		// A rolling map streams over one partition, as the rolling functions do; a rank
		// by time spills a partition at a time, and over one partition it is refused
		// by name.
		for _, e := range []ursus.Expr{x.RollingMap[float64, float64]("sum", ursus.Float64, 20, sum, ursus.MinSamples(5)).Over(),
			x.RollingRankBy(ti, ursus.Every("7i"), ursus.RankAverage).Over(c("g"))} {
			q := big.Select(e.Alias("r"))
			want := collect(t, q)
			got, stats, err := spillCollect(t, q, 16<<10, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if stats.Spills == 0 {
				t.Errorf("%s: no spill", e)
			}
			ursustest.AssertFrameEqual(t, got, want)
		}
		_, _, err := spillCollect(t, big.Select(x.RollingRankBy(ti, ursus.Every("7i"), ursus.RankAverage).Over().Alias("r")),
			16<<10, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "a rank by time") {
			t.Errorf("a rank by time over one partition past the limit: %v", err)
		}
	})
}

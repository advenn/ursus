package ursus_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestRollingRankByAndRollingMap is step 338: Polars' rolling_rank_by and
// rolling_map. Every answer is Polars 1.44.1's.
func TestRollingRankByAndRollingMap(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	x, ti := c("x"), c("t")
	lf := ursus.Frame(i64s("t", 1, 2, 3, 5, 6, 9, 9), f64s("x", 3.0, 1.0, nil, 3.0, 5.0, 2.0, 2.0))
	text := func(t *testing.T, q *ursus.LazyFrame, opts ...ursus.CollectOption) (string, string) {
		t.Helper()
		got, err := q.Collect(ctx, opts...)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := got.ToMaps()
		if err != nil {
			t.Fatal(err)
		}
		vals := make([]any, len(rows))
		for i, r := range rows {
			vals[i] = r["r"]
		}
		return fmt.Sprint(vals), got.Schema().String()
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

	t.Run("rolling_rank_by", func(t *testing.T) {
		every := ursus.Every("3i")
		for _, tc := range []struct {
			name string
			e    ursus.Expr
			want string
			typ  string
		}{
			{"average", x.RollingRankBy(ti, every, ursus.RankAverage), "[1 1 <nil> 1 2 1.5 1.5]", "Float64"},
			{"min", x.RollingRankBy(ti, every, ursus.RankMin), "[1 1 <nil> 1 2 1 1]", "Uint32"},
			{"max", x.RollingRankBy(ti, every, ursus.RankMax), "[1 1 <nil> 1 2 2 2]", "Uint32"},
			{"dense", x.RollingRankBy(ti, every, ursus.RankDense), "[1 1 <nil> 1 2 1 1]", "Uint32"},
			{"two values at least", x.RollingRankBy(ti, every, ursus.RankAverage, ursus.MinSamples(2)),
				"[<nil> 1 <nil> <nil> 2 1.5 1.5]", "Float64"},
			{"both ends closed", x.RollingRankBy(ti, every, ursus.RankAverage, ursus.WindowClosed(ursus.ClosedBoth)),
				"[1 1 <nil> 2 2 1.5 1.5]", "Float64"},
		} {
			got, sch := text(t, lf.Select(tc.e.Alias("r")))
			if got != tc.want || !strings.Contains(sch, tc.typ) {
				t.Errorf("%s: %s %s, want %s of %s", tc.name, got, sch, tc.want, tc.typ)
			}
		}
		_, err := lf.Select(x.RollingRankBy(ti, every, ursus.RankOrdinal)).Collect(ctx)
		if err == nil || !strings.Contains(err.Error(), "no ordinal") {
			t.Errorf("ordinal: %v", err)
		}
	})

	t.Run("rolling_map", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			e    ursus.Expr
			want string
		}{
			{"a sum of three", x.RollingMap[float64, float64]("sum", ursus.Float64, 3, sum), "[<nil> <nil> <nil> <nil> <nil> 10 9]"},
			{"one value at least", x.RollingMap[float64, float64]("sum", ursus.Float64, 3, sum, ursus.MinSamples(1)),
				"[3 4 4 4 8 10 9]"},
			{"centred", x.RollingMap[float64, float64]("sum", ursus.Float64, 3, sum, ursus.RollingCenter()),
				"[<nil> <nil> <nil> <nil> 10 9 <nil>]"},
			// A window holds its nulls: a length counts them.
			{"a length", x.RollingMap[float64, float64]("len", ursus.Float64, 3,
				func(w []float64, _ []bool) (float64, error) { return float64(len(w)), nil }, ursus.MinSamples(1)),
				"[1 2 3 3 3 3 3]"},
		} {
			if got, _ := text(t, lf.Select(tc.e.Alias("r"))); got != tc.want {
				t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
			}
		}
		ints := ursus.Frame(i64s("x", 1, 2, 3, 4))
		got, sch := text(t, ints.Select(x.RollingMap[int64, int64]("max", ursus.Int64, 2,
			func(w []int64, _ []bool) (int64, error) { return max(w[0], w[1]), nil }).Alias("r")))
		if got != "[<nil> 2 3 4]" || !strings.Contains(sch, "Int64") {
			t.Errorf("integers: %s %s", got, sch)
		}
		// Within each partition, in an order.
		g := ursus.Frame(strs("g", "a", "b", "a", "b", "a"), i64s("o", 5, 4, 3, 2, 1), f64s("x", 1.0, 2.0, 3.0, 4.0, 5.0))
		got, _ = text(t, g.Select(x.RollingMap[float64, float64]("sum", ursus.Float64, 2, sum, ursus.MinSamples(1)).
			OverWith(ursus.WindowSpec{PartitionBy: []ursus.Expr{c("g")}, OrderBy: []ursus.SortKey{ursus.Asc(c("o"))}}).Alias("r")))
		if got != "[4 6 8 4 5]" { // Polars'
			t.Errorf("over g by o: %s", got)
		}
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

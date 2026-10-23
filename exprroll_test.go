package ursus_test

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestRollingOfAnExpression is step 337: Expr.Rolling, an aggregate expression over
// each row's window of a rolling group, as Polars' Expr.rolling. Every answer is
// Polars 1.44.1's, but a sum of a window holding no value, null here and 0 there
// (feature-gaps §9).
func TestRollingOfAnExpression(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	x, ti := c("x"), c("t")
	every := func(p string) ursus.RollingOptions { return ursus.RollingOptions{Period: ursus.Every(p)} }
	text := func(df *ursus.DataFrame, name string) string {
		rows, err := df.ToMaps()
		if err != nil {
			t.Fatal(err)
		}
		vals := make([]any, len(rows))
		for i, r := range rows {
			vals[i] = r[name]
		}
		return fmt.Sprint(vals)
	}
	check := func(t *testing.T, lf *ursus.LazyFrame, name, want string) {
		t.Helper()
		got, err := lf.Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if s := text(got, name); s != want {
			t.Errorf("%s, want %s", s, want)
		}
	}
	lf := ursus.Frame(i64s("t", 1, 2, 3, 5, 6, 9), f64s("x", 1.0, 4.0, nil, 8.0, 3.0, 2.0),
		strs("g", "a", "b", "a", "a", "b", "b"))

	t.Run("an integer index", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			e    ursus.Expr
			want string
		}{
			{"a sum", x.Sum().Rolling(ti, every("2i")), "[1 5 4 8 11 2]"},
			{"a range", x.Max().Sub(x.Min()).Rolling(ti, every("3i")), "[0 3 3 0 5 0]"},
			{"a count", x.Count().Rolling(ti, every("3i")), "[1 2 2 1 2 1]"},
			{"the rows", ursus.Len().Rolling(ti, every("3i")), "[1 2 3 2 2 1]"},
			{"a row's values, a list", x.Rolling(ti, every("2i")), "[[1] [1 4] [4 <nil>] [8] [8 3] [2]]"},
			{"a sum of a product", x.Mul(2).Sum().Rolling(ti, every("2i")), "[2 10 8 16 22 4]"},
			{"no offset", x.Sum().Rolling(ti, ursus.RollingOptions{Period: ursus.Every("3i"), Offset: ursus.NoOffset()}),
				"[4 8 11 3 2 <nil>]"},
			{"closed at both ends", x.Sum().Rolling(ti, ursus.RollingOptions{Period: ursus.Every("3i"),
				Closed: ursus.ClosedBoth}), "[1 5 5 12 11 5]"},
			{"a first, its null", x.First().Rolling(ti, every("3i")), "[1 1 1 <nil> 8 2]"},
			{"a sort's first, nulls first", x.Sort().First().Rolling(ti, every("3i")), "[1 1 <nil> <nil> 3 2]"},
			{"over g", x.Mean().Rolling(ti, every("3i")).Over(c("g")), "[1 4 1 8 3 2]"},
		} {
			t.Run(tc.name, func(t *testing.T) { check(t, lf.Select(tc.e.Alias("r")), "r", tc.want) })
		}
		// Beside the frame's columns, named as the expression is.
		got := collect(t, lf.WithColumns(x.Sum().Rolling(ti, every("2i"))))
		if s := strings.Join(got.Columns(), ","); s != "t,x,g" || text(got, "x") != "[1 5 4 8 11 2]" {
			t.Errorf("with_columns: %s %s", s, text(got, "x"))
		}
	})

	t.Run("over a partition sorted within itself", func(t *testing.T) {
		// Polars rolls within each partition, the index sorted within each.
		p := ursus.Frame(i64s("t", 1, 5, 2, 6, 7), strs("g", "a", "a", "b", "b", "a"), f64s("x", 1.0, 2.0, 3.0, 4.0, 5.0))
		check(t, p.Select(x.Sum().Rolling(ti, every("3i")).Over(c("g")).Alias("s")), "s", "[1 2 3 4 7]")
		check(t, p.WithColumns(x.Max().Rolling(ti, every("2i")).Over(c("g"))), "x", "[1 2 3 4 5]")
		// A window ahead of its row, which a later row of another partition passes
		// before its own partition's next row reaches it: held to the end, it takes
		// that row. Polars' [3, 0, 0].
		ahead := ursus.Frame(i64s("t", 1, 10, 3), strs("g", "a", "b", "a"), f64s("x", 1.0, 2.0, 3.0))
		check(t, ahead.Select(x.Sum().Rolling(ti, ursus.RollingOptions{Period: ursus.Every("3i"), Offset: ursus.NoOffset()}).
			Over(c("g"))), "x", "[3 <nil> <nil>]")
		check(t, ahead.Select(x.Last().Rolling(ti, ursus.RollingOptions{Period: ursus.Every("3i"), Offset: ursus.NoOffset()}).
			Over(c("g"))), "x", "[3 <nil> <nil>]")
		// A tie in a partition after another partition's later row: the first row's
		// window takes it, held to the end. Polars' [3, 2, 3].
		tie := ursus.Frame(i64s("t", 1, 10, 1), strs("g", "a", "b", "a"), f64s("x", 1.0, 2.0, 3.0))
		check(t, tie.Select(x.Last().Rolling(ti, every("3i")).Over(c("g"))), "x", "[3 2 3]")
		// A batch a row, each window a segment of its own: none goes before the end.
		got, err := tie.Select(x.Last().Rolling(ti, every("3i")).Over(c("g"))).Collect(ctx, ursus.WithBatchSize(1))
		if err != nil || text(got, "x") != "[3 2 3]" {
			t.Errorf("a row a batch: %v %v", got, err)
		}
		// An aggregate with no rolling window of its own is rolled by the frame's
		// rolling group, a partition's rows put back in the frame's order.
		check(t, p.Select(x.First().Rolling(ti, every("3i")).Over(c("g"))), "x", "[1 2 3 4 2]")
		plan, err := p.Select(x.First().Rolling(ti, every("3i")).Over(c("g"))).Explain(ctx)
		if err != nil || !strings.Contains(plan, "row_order") {
			t.Errorf("the plan: %v\n%s", err, plan)
		}
	})

	t.Run("a datetime index", func(t *testing.T) {
		d := func(day, hour int) time.Time { return time.Date(2024, 1, day, hour, 0, 0, 0, time.UTC) }
		dt := ursus.Frame(ursus.Values("t", []time.Time{d(1, 0), d(1, 12), d(2, 6), d(4, 0)}), i64s("x", 1, 2, 3, 4),
			i64s("h", 1, 1, 2, 1), strs("k", "p", "p", "p", "q"))
		check(t, dt.Select(x.Sum().Rolling(ti, every("1d"))), "x", "[1 3 5 4]")
		check(t, dt.Select(x.Sum().Rolling(ti, ursus.RollingOptions{Period: ursus.Every("1d"), Offset: ursus.Every("-2d")})),
			"x", "[<nil> <nil> 1 3]")
		check(t, dt.Select(x.Filter(x.Gt(1)).Sum().Rolling(ti, every("2d"))), "x", "[<nil> 2 5 7]")
		check(t, dt.Select(x.Sum().Rolling(ti, every("3d")).Over(c("h"), c("k"))), "x", "[1 3 3 4]")
		check(t, ursus.Frame(ursus.Values("t", []int32{1, 2, 4}), f64s("x", 1.0, 2.0, 4.0)).
			Select(x.Mean().Rolling(ti, every("2i"))), "x", "[1 1.5 4]")
	})

	t.Run("in an aggregation", func(t *testing.T) {
		// Each group's rows' windows' answers, a list a group, Polars' [1, 0, 8] and
		// [4, 3, 2]: a's second window holds only a null.
		got, err := lf.GroupBy(c("g")).Agg(x.Sum().Rolling(ti, every("2i"))).Sort(ursus.Asc(c("g"))).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if s := text(got, "x"); s != "[[1 <nil> 8] [4 3 2]]" {
			t.Errorf("%s", s)
		}
	})

	t.Run("refused", func(t *testing.T) {
		unsorted := ursus.Frame(i64s("t", 3, 1, 2), i64s("x", 1, 2, 3), strs("g", "a", "a", "b"))
		// Rolled by its own window, a sum finds a window by value and needs no order:
		// Polars refuses this index.
		check(t, unsorted.Select(x.Sum().Rolling(ti, every("2i"))), "x", "[4 2 5]")
		for _, tc := range []struct {
			name string
			lf   *ursus.LazyFrame
			want string
		}{
			{"an index not sorted", unsorted.Select(x.First().Rolling(ti, every("2i"))), "is not sorted"},
			// Polars answers this one without checking, from rows out of their order.
			{"an index not sorted within its partition", unsorted.Select(x.First().Rolling(ti, every("2i")).Over(c("g"))),
				"not sorted within its partition"},
			{"in an aggregation, by the rolling group", lf.GroupBy(c("g")).Agg(x.First().Rolling(ti, every("2i"))),
				"in an aggregation"},
			{"a group-by in the options", lf.Select(x.Sum().Rolling(ti, ursus.RollingOptions{Period: ursus.Every("2i"),
				GroupBy: []ursus.Expr{c("g")}})), "from Over"},
		} {
			_, err := tc.lf.Collect(ctx)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
			}
		}
	})

	t.Run("rolled by windows as by the rolling group", func(t *testing.T) {
		// Each aggregate with a rolling window of its own is rolled by it; an offset
		// given, the same one, rolls it by the frame's rolling group. The two agree,
		// over ties, gaps, nulls and NaN, every end closed or not, and within groups.
		rng := rand.New(rand.NewPCG(337, 7))
		const n = 400
		ts, g, v, ok := make([]int64, n), make([]string, n), make([]float64, n), make([]bool, n)
		at := int64(0)
		for i := range n {
			at += int64(rng.IntN(3))
			ts[i], g[i], v[i], ok[i] = at, []string{"a", "b", "c"}[rng.IntN(3)], float64(rng.IntN(20))-5, rng.IntN(9) != 0
			if rng.IntN(40) == 0 {
				v[i] = math.NaN()
			}
		}
		r := ursus.Frame(ursus.Values("t", ts), ursus.Values("g", g), ursus.ValuesNullable("x", v, ok))
		aggs := map[string]ursus.Expr{"sum": x.Sum(), "mean": x.Mean(), "min": x.Min(), "max": x.Max(),
			"var": x.Var(1), "var0": x.Var(0), "std": x.Std(1), "median": x.Median(),
			"quantile": x.Quantile(0.3, ursus.InterpLinear), "a range": x.Max().Sub(x.Min()).Mul(2)}
		for _, closed := range []ursus.Closed{ursus.ClosedRight, ursus.ClosedLeft, ursus.ClosedBoth, ursus.ClosedNone} {
			for name, a := range aggs {
				byWin := ursus.RollingOptions{Period: ursus.Every("5i"), Closed: closed}
				byGroup := byWin
				byGroup.Offset = ursus.Every("-5i")
				for _, over := range []bool{false, true} {
					e1, e2 := a.Rolling(ti, byWin), a.Rolling(ti, byGroup)
					if over {
						e1, e2 = e1.Over(c("g")), e2.Over(c("g"))
					}
					got := collect(t, r.Select(e1.Alias("r")))
					want := collect(t, r.Select(e2.Alias("r")))
					gs, ws := rollFloats(t, got, "r"), rollFloats(t, want, "r")
					for i := range gs {
						// A sliding window's sums leave a residual a square root
						// magnifies: a std of equal values 1e-7, not 0.
						same := gs[i] == ws[i] || math.Abs(gs[i]-ws[i]) <= 1e-6*max(1, math.Abs(ws[i])) ||
							math.IsNaN(gs[i]) && math.IsNaN(ws[i])
						if !same {
							t.Fatalf("%s, %v, over %v: row %d is %v by its window, %v by the rolling group",
								name, closed, over, i, gs[i], ws[i])
						}
					}
				}
			}
		}
	})

	t.Run("past a memory limit", func(t *testing.T) {
		const n = 3000
		ts, g, v := make([]int64, n), make([]string, n), make([]float64, n)
		for i := range n {
			ts[i], g[i], v[i] = int64(i), fmt.Sprint("k", i%7), float64(i%31)
		}
		big := ursus.Frame(ursus.Values("t", ts), ursus.Values("g", g), ursus.Values("x", v))
		// Over the frame it streams, a window handed out as the index passes it; over
		// a partition, a partition's rows may come after another's later ones, so its
		// windows are held to the input's end.
		for _, tc := range []struct {
			e     ursus.Expr
			limit int64
		}{
			{x.Sum().Rolling(ti, every("20i")), 64 << 10},
			{x.Max().Sub(x.Min()).Rolling(ti, every("50i")).Over(c("g")), 2 << 20},
		} {
			q := big.Select(c("t"), tc.e.Alias("r"))
			want := collect(t, q)
			got, _, err := spillCollect(t, q, tc.limit, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			ursustest.AssertFrameEqual(t, got, want)
		}
	})
}

// rollFloats is column name of df as float64s, a null as -Inf, which no answer here is.
func rollFloats(t *testing.T, df *ursus.DataFrame, name string) []float64 {
	t.Helper()
	rows, err := df.ToMaps()
	if err != nil {
		t.Fatal(err)
	}
	out := make([]float64, len(rows))
	for i, r := range rows {
		switch v := r[name].(type) {
		case nil:
			out[i] = math.Inf(-1)
		case float64:
			out[i] = v
		case int64:
			out[i] = float64(v)
		default:
			t.Fatalf("%s: a %T", name, v)
		}
	}
	return out
}

package ursus_test

// Step 177: rolling windows by time. Their answers are Polars 2.1.0's since step 500,
// recorded in the rollingby group of testdata/oracle and spelled in
// oracle_rollingby_test.go, which took this file's tables. What stays asserts the
// refusals, the names and the plan, and holds 400 random rows to a brute force.

import (
	"math"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
)

// hoursFrame is t at the given hours of 2024-01-01, and v, null where ok is false.
func hoursFrame(hours []int, v []float64, ok []bool) *ursus.LazyFrame {
	ts := make([]time.Time, len(hours))
	for i, h := range hours {
		ts[i] = time.Date(2024, 1, 1, h, 0, 0, 0, time.UTC)
	}
	if ok == nil {
		ok = make([]bool, len(v))
		for i := range ok {
			ok[i] = true
		}
	}
	return ursus.Frame(ursus.Values("t", ts), ursus.ValuesNullable("v", v, ok))
}

func TestRollingByRefuses(t *testing.T) {
	c := ursus.Col
	frame := hoursFrame([]int{0, 1}, []float64{1, 2}, nil).
		WithColumns(c("t").Cast(dtype.Date).Alias("d"), ursus.Lit("x").Alias("s"), ursus.Lit(int64(1)).Alias("i"),
			c("t").Cast(dtype.Time(dtype.Nano)).Alias("tod"))
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want string
	}{
		{"by a string", c("v").RollingSumBy(c("s"), ursus.Every("1h")), "by a Date, a Datetime, a Time or an integer"},
		// An integer by counts by "i" (step 312); Polars: must be a parsed integer.
		{"an integer by, a period of time", c("v").RollingSumBy(c("i"), ursus.Every("1h")), "period=1h is an interval of time"},
		{"a zero period", c("v").RollingSumBy(c("t"), ursus.FromDuration(0)), "must be positive"},
		{"a negative period", c("v").RollingSumBy(c("t"), ursus.Every("-1h")), "must be positive"},
		{"an unparseable period", c("v").RollingSumBy(c("t"), ursus.Every("1x")), "not a unit"},
		{"centered", c("v").RollingSumBy(c("t"), ursus.Every("1h"), ursus.RollingCenter()), "cannot be centered"},
		{"an hour of Dates", c("v").RollingSumBy(c("d"), ursus.Every("1h")), "finer than"},
		{"a month of Times", c("v").RollingSumBy(c("tod"), ursus.Every("1mo")), "calendar interval"},
		{"a sum of strings", c("s").RollingSumBy(c("t"), ursus.Every("1h")), "not defined for String"},
		{"min_samples 0", c("v").RollingSumBy(c("t"), ursus.Every("1h"), ursus.MinSamples(0)), "at least 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertUserError(t, frame.Select(tc.e), tc.want)
		})
	}
}

// TestRollingByNamesAndRenders: named after its value; rendered as written; and two
// periods, or two closings, in one Select are two answers (the values are the
// rollingby group's names_three).
func TestRollingByNamesAndRenders(t *testing.T) {
	c := ursus.Col
	lf := hoursFrame([]int{0, 1, 2}, []float64{1, 2, 3}, nil).Select(
		c("v").RollingSumBy(c("t"), ursus.Every("1h")).Alias("one"),
		c("v").RollingSumBy(c("t"), ursus.Every("2h")).Alias("two"),
		c("v").RollingSumBy(c("t"), ursus.Every("2h"), ursus.WindowClosed(ursus.ClosedBoth)).Alias("both"),
		c("v").RollingMaxBy(c("t"), ursus.Every("1h")),
	)
	df, err := lf.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(df.Schema().Names(), ","); got != "one,two,both,v" {
		t.Fatalf("named %s", got)
	}
	plan, err := lf.Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if w := `col("v").rolling_sum_by(col("t"), 2h, closed both, min_samples 1)`; !strings.Contains(plan, w) {
		t.Errorf("the plan does not show %s:\n%s", w, plan)
	}
}

// TestRollingByAgreesWithABruteForce: random instants, unsorted with ties and nulls,
// values with nulls, in partitions, for each closing and a period, against every
// window computed here by scanning the whole partition.
func TestRollingByAgreesWithABruteForce(t *testing.T) {
	const rows = 400
	rng := rand.New(rand.NewPCG(177, 1))
	g, mins, v := make([]int64, rows), make([]int64, rows), make([]float64, rows)
	tok, vok := make([]bool, rows), make([]bool, rows)
	ts := make([]time.Time, rows)
	for i := range rows {
		g[i], mins[i], v[i] = rng.Int64N(3), rng.Int64N(240), float64(rng.IntN(100))
		ts[i] = time.Date(2024, 1, 1, 0, int(mins[i]), 0, 0, time.UTC)
		tok[i], vok[i] = rng.IntN(15) != 0, rng.IntN(8) != 0
	}
	frame := ursus.Frame(ursus.Values("g", g), ursus.ValuesNullable("t", ts, tok), ursus.ValuesNullable("v", v, vok))
	c := ursus.Col
	in := func(lowOpen, highClosed bool, from, to, x int64) bool {
		return (x > from || !lowOpen && x == from) && (x < to || highClosed && x == to)
	}
	for _, closed := range []ursus.Closed{ursus.ClosedRight, ursus.ClosedLeft, ursus.ClosedBoth, ursus.ClosedNone} {
		for _, period := range []int64{1, 17, 60} {
			lowOpen := closed == ursus.ClosedRight || closed == ursus.ClosedNone
			highClosed := closed == ursus.ClosedRight || closed == ursus.ClosedBoth
			iv := ursus.FromDuration(time.Duration(period) * time.Minute)
			opts := []ursus.WindowOption{ursus.WindowClosed(closed)}
			df, err := frame.Select(
				c("v").RollingSumBy(c("t"), iv, opts...).Over(c("g")).Alias("sum"),
				c("v").RollingMinBy(c("t"), iv, opts...).Over(c("g")).Alias("min"),
				c("v").RollingMaxBy(c("t"), iv, opts...).Over(c("g")).Alias("max"),
				c("v").RollingVarBy(c("t"), iv, opts...).Over(c("g")).Alias("var"),
			).Collect(t.Context(), ursus.WithBatchSize(64))
			if err != nil {
				t.Fatal(err)
			}
			sum, sumOK := floats175(t, df, "sum")
			mn, mnOK := floats175(t, df, "min")
			mx, mxOK := floats175(t, df, "max")
			vr, vrOK := floats175(t, df, "var")
			for i := range rows {
				var xs []float64
				if tok[i] {
					for j := range rows {
						if g[j] == g[i] && tok[j] && vok[j] && in(lowOpen, highClosed, mins[i]-period, mins[i], mins[j]) {
							xs = append(xs, v[j])
						}
					}
				}
				key := closed.String() + "/" + time.Duration(period*int64(time.Minute)).String()
				if len(xs) == 0 {
					if sumOK[i] || mnOK[i] || mxOK[i] {
						t.Fatalf("%s row %d: an empty window answered %v %v %v", key, i, sum[i], mn[i], mx[i])
					}
					continue
				}
				s, lo, hi := 0.0, math.Inf(1), math.Inf(-1)
				for _, x := range xs {
					s, lo, hi = s+x, math.Min(lo, x), math.Max(hi, x)
				}
				if !sumOK[i] || sum[i] != s || mn[i] != lo || mx[i] != hi {
					t.Fatalf("%s row %d: sum %v min %v max %v, want %v %v %v", key, i, sum[i], mn[i], mx[i], s, lo, hi)
				}
				if len(xs) < 2 {
					if vrOK[i] {
						t.Fatalf("%s row %d: var of one value %v", key, i, vr[i])
					}
					continue
				}
				m := s / float64(len(xs))
				var ss float64
				for _, x := range xs {
					ss += (x - m) * (x - m)
				}
				if want := ss / float64(len(xs)-1); math.Abs(vr[i]-want) > 1e-9*math.Max(1, want) {
					t.Fatalf("%s row %d: var %v, want %v", key, i, vr[i], want)
				}
			}
		}
	}
}

package ursus_test

// Step 177: rolling windows by time. The expected answers are Polars 1.44.1's, from
// the bench's own environment:
//
//	df.select(pl.col("v").rolling_sum_by("t", "2h", min_samples=1, closed="right"))

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

func TestRollingByAnswersAsPolars(t *testing.T) {
	c := ursus.Col
	two := ursus.Every("2h")
	frame := hoursFrame([]int{0, 1, 2, 4, 5, 9}, []float64{1, 2, 0, 4, 5, 6}, []bool{true, true, false, true, true, true})
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want string
	}{
		{"sum", c("v").RollingSumBy(c("t"), two), "1.0 3.0 2.0 4.0 9.0 6.0 "},
		{"mean", c("v").RollingMeanBy(c("t"), two), "1.0 1.5 2.0 4.0 4.5 6.0 "},
		{"min", c("v").RollingMinBy(c("t"), two), "1.0 1.0 2.0 4.0 4.0 6.0 "},
		{"max", c("v").RollingMaxBy(c("t"), two), "1.0 2.0 2.0 4.0 5.0 6.0 "},
		{"var", c("v").RollingVarBy(c("t"), two), "∅ 0.5 ∅ ∅ 0.5 ∅ "},
		{"std", c("v").RollingStdBy(c("t"), ursus.Every("3h")), "∅ 0.7071067811865476 0.7071067811865476 ∅ 0.7071067811865476 ∅ "},
		{"min_samples 2", c("v").RollingSumBy(c("t"), two, ursus.MinSamples(2)), "∅ 3.0 ∅ ∅ 9.0 ∅ "},
		{"closed right", c("v").RollingSumBy(c("t"), two, ursus.WindowClosed(ursus.ClosedRight)), "1.0 3.0 2.0 4.0 9.0 6.0 "},
		// Polars answers an empty window 0 here, under min_samples=1: [0, 1, 3, 0, 4, 0].
		{"closed left", c("v").RollingSumBy(c("t"), two, ursus.WindowClosed(ursus.ClosedLeft)), "∅ 1.0 3.0 ∅ 4.0 ∅ "},
		{"closed both", c("v").RollingSumBy(c("t"), two, ursus.WindowClosed(ursus.ClosedBoth)), "1.0 3.0 3.0 4.0 9.0 6.0 "},
		{"closed none", c("v").RollingSumBy(c("t"), two, ursus.WindowClosed(ursus.ClosedNone)), "∅ 1.0 2.0 ∅ 4.0 ∅ "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			df, err := frame.Select(tc.e).Collect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if got := rendered172(t, df, "v"); got != tc.want {
				t.Errorf("%q, want %q", got, tc.want)
			}
		})
	}

	for _, tc := range []struct {
		name  string
		frame *ursus.LazyFrame
		e     ursus.Expr
		want  string
	}{
		{"integers: an exact sum, a float mean",
			hoursFrame([]int{0, 1, 2, 4, 5, 9}, []float64{1, 2, 3, 4, 5, 6}, nil).Select(c("t"), c("v").Cast(ursus.Int64)),
			c("v").RollingMeanBy(c("t"), two), "1.0 1.5 2.5 4.0 4.5 6.0 "},
		{"by unsorted", hoursFrame([]int{2, 0, 1, 5}, []float64{1, 2, 3, 4}, nil),
			c("v").RollingSumBy(c("t"), two), "4.0 2.0 5.0 4.0 "},
		{"ties share a window, the later rows too", hoursFrame([]int{0, 0, 1}, []float64{1, 2, 3}, nil),
			c("v").RollingSumBy(c("t"), ursus.Every("1h")), "3.0 3.0 3.0 "},
		{"a null by is in no window",
			ursus.Frame(ursus.ValuesNullable("t", []time.Time{time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), {},
				time.Date(2024, 1, 1, 1, 0, 0, 0, time.UTC)}, []bool{true, false, true}), ursus.Values("v", []float64{1, 2, 3})),
			c("v").RollingSumBy(c("t"), two), "1.0 ∅ 4.0 "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			df, err := tc.frame.Select(tc.e).Collect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if got := rendered172(t, df, "v"); got != tc.want {
				t.Errorf("%q, want %q", got, tc.want)
			}
		})
	}

	// Per partition: g=[a, a, b, b], t=[0, 1, 0, 1]h, v=[1, 2, 10, 20].
	g := ursus.Values("g", []string{"a", "a", "b", "b"})
	ts := []time.Time{time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2024, 1, 1, 1, 0, 0, 0, time.UTC),
		time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2024, 1, 1, 1, 0, 0, 0, time.UTC)}
	df, err := ursus.Frame(g, ursus.Values("t", ts), ursus.Values("v", []float64{1, 2, 10, 20})).
		Select(c("v").RollingSumBy(c("t"), two).Over(c("g"))).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := rendered172(t, df, "v"); got != "1.0 3.0 10.0 30.0 " {
		t.Errorf("over g: %q", got)
	}

	// Dates, and a calendar month.
	days := func(ds ...int) []time.Time {
		out := make([]time.Time, len(ds))
		for i, d := range ds {
			out[i] = time.Date(2024, 1, d, 0, 0, 0, 0, time.UTC)
		}
		return out
	}
	dated := ursus.Frame(ursus.Values("t", days(1, 2, 3, 5)), ursus.Values("v", []float64{1, 2, 3, 4})).
		Select(c("t").Cast(dtype.Date), c("v"))
	for period, want := range map[string]string{"2d": "1.0 3.0 5.0 4.0 ", "1mo": "1.0 3.0 6.0 10.0 "} {
		df, err := dated.Select(c("v").RollingSumBy(c("t"), ursus.Every(period))).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if got := rendered172(t, df, "v"); got != want {
			t.Errorf("dates by %s: %q, want %q", period, got, want)
		}
	}

	// A calendar day across London's change of offset is not twenty-four hours.
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	zoned := ursus.Frame(ursus.Values("t", []time.Time{time.Date(2024, 3, 30, 12, 0, 0, 0, london),
		time.Date(2024, 3, 31, 12, 0, 0, 0, london)}), ursus.Values("v", []float64{1, 2})).
		Select(c("t").Cast(dtype.Datetime(dtype.Micro, "Europe/London")), c("v"))
	for _, tc := range []struct {
		name   string
		period ursus.Interval
		want   string
	}{
		{"1d", ursus.Every("1d"), "1.0 2.0 "},
		{"24h", ursus.FromDuration(24 * time.Hour), "1.0 3.0 "},
	} {
		df, err := zoned.Select(c("v").RollingSumBy(c("t"), tc.period)).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if got := rendered172(t, df, "v"); got != tc.want {
			t.Errorf("across the change, %s: %q, want %q", tc.name, got, tc.want)
		}
	}
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
// periods, or two closings, in one Select are two answers.
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
	for name, want := range map[string]string{"one": "1.0 2.0 3.0 ", "two": "1.0 3.0 5.0 ", "both": "1.0 3.0 6.0 "} {
		if got := rendered172(t, df, name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
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

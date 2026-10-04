package ursus_test

// The optimizer against a hand-computed answer, for the three defects of audit.md
// §3 that step 70 fixes and the ones found beside them.
//
// Every case runs twice — with the default rules, and with every rule off — and
// both must equal an answer worked out by hand. Comparing the two runs against each
// other is what TestPushdownSoundness does, and it is not enough on its own: a
// defect in the RESOLVER is wrong in both runs identically, and they agree. W1
// below is exactly that.

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/plan"
)

// sortedInt64s reads one Int64 column, sorted, so a case can state its answer as a
// multiset without caring about row order.
func sortedInt64s(df *ursus.DataFrame, name string) ([]int64, error) {
	col, err := df.Column[int64](name)
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, col.Len())
	for i := range col.Len() {
		v, ok := col.Get(i)
		if !ok {
			return nil, fmt.Errorf("row %d of %q is null", i, name)
		}
		out = append(out, v)
	}
	slices.Sort(out)
	return out, nil
}

// wantInts checks that column name holds exactly want, as a multiset.
func wantInts(name string, want ...int64) func(*ursus.DataFrame) error {
	return func(df *ursus.DataFrame) error {
		got, err := sortedInt64s(df, name)
		if err != nil {
			return err
		}
		if !slices.Equal(got, want) {
			return fmt.Errorf("%s = %v, want %v", name, got, want)
		}
		return nil
	}
}

// wantHeight checks only the row count.
// wantFloats is one Float64 column's values, sorted, NaN last and equal to NaN.
func wantFloats(name string, want ...float64) func(*ursus.DataFrame) error {
	return func(df *ursus.DataFrame) error {
		col, err := df.Column[float64](name)
		if err != nil {
			return err
		}
		got := make([]float64, col.Len())
		for i := range got {
			got[i], _ = col.Get(i)
		}
		key := func(v float64) float64 {
			if math.IsNaN(v) {
				return math.Inf(1)
			}
			return v
		}
		slices.SortFunc(got, func(a, b float64) int { return cmp.Compare(key(a), key(b)) })
		if !slices.EqualFunc(got, want, func(a, b float64) bool { return a == b || (a != a && b != b) }) {
			return fmt.Errorf("%s = %v, want %v", name, got, want)
		}
		return nil
	}
}

func wantHeight(n int) func(*ursus.DataFrame) error {
	return func(df *ursus.DataFrame) error {
		if df.Height() != n {
			return fmt.Errorf("%d rows, want %d", df.Height(), n)
		}
		return nil
	}
}

func byHandFrame() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.Values("x", []int64{1, 2, 3, 4, 5}),
		ursus.Values("w", []int64{100, 0, 100, 0, 100}),
		ursus.Values("y", []int64{10, 20, 30, 40, 50}),
	)
}

func doubled() func(int64) (int64, error) {
	return func(v int64) (int64, error) { return v * 2, nil }
}

// knownOptimizerDefects names each case that answers wrongly today, and in which
// mode. Emptied, entry by entry, by the commits that fix them; a listed case that
// answers correctly fails as stale, and an unlisted one that answers wrongly fails.
var knownOptimizerDefects = map[string]string{}

func TestOptimizerByHand(t *testing.T) {
	c := ursus.Col
	l := ursus.Frame(ursus.Values("k", []int64{1, 2, 3, 4}), ursus.Values("v", []int64{10, 20, 30, 40}))
	a32 := ursus.Frame(ursus.Values("k", []int32{1, 2}), ursus.Values("lv", []int64{7, 8}))
	b64 := ursus.Frame(ursus.Values("k", []int64{3, 4}), ursus.Values("lv", []int64{9, 10}))
	sok := ursus.Frame(ursus.Values("s", []string{"1", "x", "3", "4"}),
		ursus.Values("ok", []bool{true, false, true, true}),
		ursus.Values("i", []int64{1, 2, 3, 4}))
	sales := ursus.Frame(ursus.Values("price", []int64{3, 1, 5}), ursus.Values("qty", []int64{2, 1, 2}),
		ursus.Values("cost", []int64{5, 2, 12}), ursus.Values("id", []int64{1, 2, 3}))
	win := ursus.Frame(ursus.Values("g", []string{"a", "a", "b"}), ursus.Values("x", []int64{1, 2, 3}),
		ursus.Values("w", []int64{100, 100, 100}))
	nanL := ursus.Frame(ursus.Values("a", []float64{1, math.NaN()}))
	nanR := ursus.Frame(ursus.Values("b", []float64{math.NaN(), 1}))

	cases := []struct {
		name  string
		lf    *ursus.LazyFrame
		check func(*ursus.DataFrame) error
	}{
		// w = x+1 = 2..6, v = w*2 = 4..12; v > 7 keeps x = 3, 4, 5.
		{"O1 chained", byHandFrame().
			WithColumns(c("x").Add(1).Alias("w"), c("w").Mul(2).Alias("v")).
			Filter(c("v").Gt(7)), wantInts("x", 3, 4, 5)},
		// x' = x+10 = 11..15, y = x'*2 = 22..30; y > 25 keeps x' = 13, 14, 15.
		{"O1 in place", byHandFrame().
			WithColumns(c("x").Add(10).Alias("x"), c("x").Mul(2).Alias("y")).
			Filter(c("y").Gt(25)), wantInts("x", 13, 14, 15)},
		// revenue = 6, 1, 10; profit = 1, -1, -2; profit > 0 keeps id 1.
		{"O1 new name", sales.
			WithColumns(c("price").Mul(c("qty")).Alias("revenue"),
				c("revenue").Sub(c("cost")).Alias("profit")).
			Filter(c("profit").Gt(0)), wantInts("id", 1)},
		// u = 2x = 2..10, v = u+1 = 3..11; v > 7 keeps x = 4, 5. The input's own w
		// column plays u's shadowed role: it is 100 or 0, so reading it keeps others.
		{"O1 udf chain", byHandFrame().
			WithColumns(c("x").MapElements("dbl", ursus.Int64, doubled()).Alias("w"),
				c("w").Add(1).Alias("v")).
			Filter(c("v").Gt(7)), wantInts("x", 4, 5)},
		// x' = 2x = 2..10, y = x'+1 = 3..11; y > 7 keeps x' = 8, 10.
		{"O1 udf in place", byHandFrame().
			WithColumns(c("x").MapElements("dbl", ursus.Int64, doubled()).Alias("x"),
				c("x").Add(1).Alias("y")).
			Filter(c("y").Gt(7)), wantInts("x", 8, 10)},
		// The ORDER of a WithColumns matters, in the other direction too: z is
		// defined BEFORE y is redefined, so z reads the input's y = 10..50, and
		// z > 25 keeps x = 3, 4, 5. Composing in a second pass would read 2x+1.
		{"O1 control: defined before", byHandFrame().
			WithColumns(c("y").Add(1).Alias("z"), c("x").Mul(2).Alias("y")).
			Filter(c("z").Gt(25)), wantInts("x", 3, 4, 5)},
		// A Select is PARALLEL: m reads the input's x, so m = 2..10 and m > 5 keeps
		// x = 3, 4, 5, which come out as x+1 = 4, 5, 6.
		{"O1 control: select is parallel", byHandFrame().
			Select(c("x").Add(1).Alias("x"), c("x").Mul(2).Alias("m")).
			Filter(c("m").Gt(5)), wantInts("x", 4, 5, 6)},

		// 4 rows, plus the 3 with v > 15.
		{"O2 strict", l.Concat(l.Filter(c("v").Gt(15))).Select(c("k")),
			wantInts("k", 1, 2, 2, 3, 3, 4, 4)},
		// The wide child FIRST. Every other O2 case has the narrowest child in
		// front, so a fix that took its target from the first child rather than
		// from the union would pass all of them.
		{"O2 wide child first", l.Filter(c("v").Gt(15)).Concat(l).Select(c("k")),
			wantInts("k", 1, 2, 2, 3, 3, 4, 4)},
		{"O2 len", l.Concat(l.Filter(c("v").Gt(15))).GroupBy().Agg(ursus.Len().Alias("n")),
			func(df *ursus.DataFrame) error {
				n, _, err := df.At[uint64](0, "n")
				if err != nil {
					return err
				}
				if n != 7 {
					return fmt.Errorf("len = %d, want 7", n)
				}
				return nil
			}},
		// b64's k is already Int64 and a32's is not, so only a32 gets resolveUnion's
		// adaptation Project — which keeps every expression — while b64's scan
		// narrows to lv alone.
		{"O2 widening", b64.Concat(a32).Select(c("lv")),
			wantInts("lv", 7, 8, 9, 10)},
		// The same asymmetry under the diagonal mode, where it is worse: the plan
		// verifies and the physical union raises ErrInternal.
		{"O2 diagonal", ursus.Concat([]*ursus.LazyFrame{l, l.Filter(c("v").Gt(15))},
			ursus.WithConcatMode(ursus.ConcatDiagonal)).Select(c("k")),
			wantInts("k", 1, 2, 2, 3, 3, 4, 4)},

		// Only the rows the guard keeps reach the cast: "1", "3", "4", and > 1
		// keeps "3" and "4".
		{"O8 guard", sok.Filter(c("ok")).Filter(c("s").Cast(ursus.Int64).Gt(1)),
			wantInts("i", 3, 4)},
		{"O8b under unique", sok.Unique("s").Filter(c("ok")).Filter(c("s").Cast(ursus.Int64).Gt(1)),
			wantInts("i", 3, 4)},

		// A WithColumns replaces an existing name IN PLACE, keeping its position
		// (WalkWithColumns' doc). w is redefined without reading it, so projection
		// pushdown stops reading the input's w — and w then lands at the end. The
		// redefined column must not already be the last one, or moving it to the
		// end changes nothing: the first version of this case redefined y.
		{"P1 column order", byHandFrame().
			WithColumns(c("x").Add(1).Alias("w")),
			func(df *ursus.DataFrame) error {
				if got, want := strings.Join(df.Schema().Names(), ","), "x,w,y"; got != want {
					return fmt.Errorf("columns %s, want %s", got, want)
				}
				return nil
			}},

		// w = 10x, so the per-group sum is 30 for "a" and 30 for "b".
		{"W1 window", win.
			WithColumns(c("x").Mul(10).Alias("w"), c("w").Sum().Over(c("g")).Alias("s")),
			func(df *ursus.DataFrame) error {
				for i, want := range []string{"30", "30", "30"} {
					v, _, err := df.At[ursus.Int128Value](i, "s")
					if err != nil {
						return err
					}
					if v.String() != want {
						return fmt.Errorf("row %d: s = %s, want %s", i, v, want)
					}
				}
				return nil
			}},
		{"W1 window new", win.
			WithColumns(c("x").Mul(10).Alias("w2"), c("w2").Sum().Over(c("g")).Alias("s")),
			wantHeight(3)},

		// --- step 78 ---
		// O4: == is IEEE, so NaN equals nothing, NaN included. The cross-join collapse
		// made it a hash key, and the hash join groups NaN with NaN.
		{"O4 cross join and ==", nanL.Join(nanR, ursus.JoinHow(ursus.JoinCross)).
			Filter(c("a").Eq(c("b"))), wantHeight(1)},
		{"O4 JoinWhere", nanL.JoinWhere(nanR, c("a").Eq(c("b"))), wantHeight(1)},
		{"O4 WhereExists", nanL.WhereExists(nanR, c("a").Eq(c("b"))), wantFloats("a", 1)},
		{"O4 WhereNotExists", nanL.WhereNotExists(nanR, c("a").Eq(c("b"))), wantFloats("a", math.NaN())},
		// O8-join: the inner join removes the "x" row before the filter sees it, so the
		// strict cast never meets "x"; pushed into the left side, it does.
		{"O8-join inner", ursus.Frame(ursus.Values("s", []string{"1", "x"}), ursus.Values("k", []int64{1, 2})).
			Join(ursus.Frame(ursus.Values("k", []int64{1}), ursus.Values("r", []int64{9})), ursus.JoinOn(c("k"))).
			Filter(c("s").Cast(ursus.Int64).Gt(0)), wantInts("k", 1)},
		// O9: Unique("x") keeps the first of -0 and +0, which is -0, and "-0" is not
		// "0". Filtered first, the +0 row survives to be the one kept.
		{"O9 unique keeps -0", ursus.Frame(ursus.Values("x", []float64{math.Copysign(0, -1), 0}),
			ursus.Values("i", []int64{1, 2})).Unique("x").Filter(c("x").Cast(ursus.String).Eq("0")),
			wantHeight(0)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var wrong []string
			for _, mode := range []struct {
				name string
				opts []ursus.CollectOption
			}{
				{"optimized", nil},
				{"unoptimized", []ursus.CollectOption{ursus.WithOptFlags(plan.NoFlags())}},
			} {
				df, err := tc.lf.Collect(t.Context(), mode.opts...)
				if err == nil {
					err = tc.check(df)
				}
				if err != nil {
					if errors.Is(err, ursus.ErrInternal) {
						err = fmt.Errorf("ErrInternal: %w", err)
					}
					wrong = append(wrong, mode.name+": "+err.Error())
				}
			}
			why, known := knownOptimizerDefects[tc.name]
			switch {
			case known && len(wrong) == 0:
				t.Errorf("answers correctly now; delete it from knownOptimizerDefects (%s)", why)
			case known:
				t.Logf("known defect (%s): %v", why, wrong)
			case len(wrong) > 0:
				t.Errorf("%v", wrong)
			}
		})
	}
	for name := range knownOptimizerDefects {
		if !slices.ContainsFunc(cases, func(c struct {
			name  string
			lf    *ursus.LazyFrame
			check func(*ursus.DataFrame) error
		}) bool {
			return c.name == name
		}) {
			t.Errorf("knownOptimizerDefects names %q, which is not a case", name)
		}
	}
}

// TestConcatUnderLenReadsOneColumn: a parent that reads no column — Len — must not
// make a union read every column, or stack zero-column Projects to reconcile.
//
// Without pushdownUnion's empty-target guard the answer is still right — seven
// rows either way, because a zero-column batch keeps its row count — so this is
// the only place the guard shows: one scan read every column, and both children
// were wrapped in a `PROJECT []`. The rows cannot tell; the plan can.
func TestConcatUnderLenReadsOneColumn(t *testing.T) {
	c := ursus.Col
	l := ursus.Frame(ursus.Values("k", []int64{1, 2, 3, 4}), ursus.Values("v", []int64{10, 20, 30, 40}))
	p, err := l.Concat(l.Filter(c("v").Gt(15))).GroupBy().Agg(ursus.Len().Alias("n")).
		Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p, "PROJECT []") || !strings.Contains(p, "projection: [k] (1/2 cols)") {
		t.Errorf("the union's children should be asked for one column, not none:\n%s", p)
	}
}

// knownPlannerRefusalDefects names each case the planner does not refuse today.
var knownPlannerRefusalDefects = map[string]string{
	"O10 a window as the dynamic index":      "accepted by the planner, ErrInternal at Collect",
	"O11 an ordered aggregate over a window": "accepted by Explain and CollectSchema",
}

// TestPlannerRefusesWhatCollectRefuses: a plan the engine will refuse is refused by
// Explain and CollectSchema too, with the same kind — not accepted there and failed
// at Collect, and never as ErrInternal.
func TestPlannerRefusesWhatCollectRefuses(t *testing.T) {
	c := ursus.Col
	l := ursus.Frame(ursus.Values("k", []int64{1, 2}))
	r := ursus.Frame(ursus.Values("k", []int64{2, 3}))
	g := ursus.Frame(ursus.Values("g", []int64{1, 1}), ursus.Values("v", []int64{3, 4}))
	cases := []struct {
		name  string
		lf    *ursus.LazyFrame
		kind  error
		words []string
	}{
		// O5: a cross join has no keys, so there are no nulls for it to equate.
		{"O5 JoinNullsEqual on a cross join", l.Join(r, ursus.JoinHow(ursus.JoinCross), ursus.JoinNullsEqual(true)),
			ursus.ErrValue, []string{"cross"}},
		// O10: a window as the index of a dynamic group.
		{"O10 a window as the dynamic index", hourly(t, "2024-01-01T00:10:00", "2024-01-01T01:05:00").
			GroupByDynamic(c("ts").Shift(1), ursus.DynamicOptions{Every: ursus.Every("1h")}).Agg(ursus.Len()),
			ursus.ErrUnsupported, []string{"window"}},
		// O11: an aggregate over an ordered window has no order key to honour.
		{"O11 an ordered aggregate over a window", g.Select(c("v").Sum().OverWith(ursus.WindowSpec{
			PartitionBy: []ursus.Expr{c("g")}, OrderBy: []ursus.SortKey{ursus.Asc(c("v"))}})),
			ursus.ErrUnsupported, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var wrong []string
			judge := func(stage string, err error) {
				switch {
				case err == nil:
					wrong = append(wrong, stage+" accepted it")
				case !errors.Is(err, tc.kind):
					wrong = append(wrong, fmt.Sprintf("%s: a %s error, want %v: %v", stage, kindOf(err), tc.kind, err))
				default:
					for _, w := range tc.words {
						if !strings.Contains(err.Error(), w) {
							wrong = append(wrong, fmt.Sprintf("%s does not say %q: %v", stage, w, err))
						}
					}
				}
			}
			_, err := tc.lf.CollectSchema(t.Context())
			judge("CollectSchema", err)
			_, err = tc.lf.Explain(t.Context())
			judge("Explain", err)
			_, err = tc.lf.Collect(t.Context())
			judge("Collect", err)

			why, known := knownPlannerRefusalDefects[tc.name]
			switch {
			case known && len(wrong) == 0:
				t.Errorf("refused everywhere now; delete it from knownPlannerRefusalDefects (%s)", why)
			case known:
				t.Logf("known defect (%s): %v", why, wrong)
			case len(wrong) > 0:
				t.Errorf("%v", wrong)
			}
		})
	}
	for name := range knownPlannerRefusalDefects {
		if !slices.ContainsFunc(cases, func(c struct {
			name  string
			lf    *ursus.LazyFrame
			kind  error
			words []string
		}) bool {
			return c.name == name
		}) {
			t.Errorf("knownPlannerRefusalDefects names %q, which is not a case", name)
		}
	}
}

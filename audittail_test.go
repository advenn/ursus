package ursus_test

// The tail of audit.md's misleading errors (step 115), `v0.4-scope.md` item 23.
//
// Each refusal below is checked twice: that it says the right thing, and that the
// rewrite its hint recommends runs. A9's hint recommended two things that were both
// refused; that is what the second check is for.

import (
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
)

// refusedSaying collects lf's schema, which must fail with kind, saying every one of want
// and none of absent.
func refusedSaying(t *testing.T, lf *ursus.LazyFrame, kind error, want, absent []string) {
	t.Helper()
	_, err := lf.CollectSchema(t.Context())
	if !errors.Is(err, kind) {
		t.Fatalf("want a %v, got %v", kind, err)
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("does not say %q:\n%v", w, err)
		}
	}
	for _, a := range absent {
		if strings.Contains(err.Error(), a) {
			t.Errorf("says %q:\n%v", a, err)
		}
	}
}

// runs collects lf, which must succeed, and returns column name as text.
func runs(t *testing.T, lf *ursus.LazyFrame, name string) []string {
	t.Helper()
	df, err := lf.Collect(t.Context())
	if err != nil {
		t.Fatalf("the recommended rewrite fails: %v", err)
	}
	_, got := textOf(t, df, name)
	return got
}

func TestTheWindowHintMovesTheWindow(t *testing.T) {
	c := ursus.Col
	f := ursus.Frame(ursus.Values("k", []int64{1, 1, 2}), ursus.Values("x", []int64{1, 2, 4}))

	t.Run("a reduced window in Agg", func(t *testing.T) {
		// It was refused as "not an aggregate … every column must be reduced", and it
		// is reduced.
		refusedSaying(t, f.GroupBy(c("k")).Agg(c("x").Sum().Over(c("k")).Max().Alias("m")), ursus.ErrUnsupported,
			[]string{`.WithColumns(col("x").sum().over(col("k")).Alias("w"))`, `Col("w") in its place`},
			[]string{"must be reduced", `.max().Alias("w")`})
		got := runs(t, f.WithColumns(c("x").Sum().Over(c("k")).Alias("w")).
			GroupBy(c("k")).Agg(c("w").Max().Alias("m")).Sort(ursus.Asc(c("k"))), "m")
		if !slices.Equal(got, []string{"3", "4"}) {
			t.Errorf("%v", got)
		}
	})
	t.Run("a bare window function in Agg", func(t *testing.T) {
		refusedSaying(t, f.GroupBy(c("k")).Agg(c("x").CumSum(false).Max().Alias("m")), ursus.ErrUnsupported,
			[]string{`.WithColumns(col("x").cum_sum().Alias("w"))`, ".Over(the group-by keys)"},
			[]string{`.max().Alias("w")`})
		got := runs(t, f.WithColumns(c("x").CumSum(false).Over(c("k")).Alias("w")).
			GroupBy(c("k")).Agg(c("w").Max().Alias("m")).Sort(ursus.Asc(c("k"))), "m")
		if !slices.Equal(got, []string{"3", "4"}) {
			t.Errorf("%v", got)
		}
	})
	t.Run("a window in a filter", func(t *testing.T) {
		refusedSaying(t, f.Filter(c("x").Sum().Over(c("k")).Gt(3)), ursus.ErrUnsupported,
			[]string{`.WithColumns(col("x").sum().over(col("k")).Alias("w"))`}, []string{`gt(`})
		got := runs(t, f.WithColumns(c("x").Sum().Over(c("k")).Alias("w")).Filter(c("w").Gt(3)), "x")
		if !slices.Equal(got, []string{"4"}) {
			t.Errorf("%v", got)
		}
	})
}

func TestAggregateHintsSayWhatIsTrue(t *testing.T) {
	c := ursus.Col
	f := ursus.Frame(ursus.Values("k", []int64{1, 1, 2}), ursus.Values("x", []int64{1, 2, 4}))
	ordered := func(e ursus.Expr) ursus.Expr {
		return e.OverWith(ursus.WindowSpec{PartitionBy: []ursus.Expr{c("k")}, OrderBy: []ursus.SortKey{ursus.Desc(c("x"))}})
	}

	t.Run("an aggregate as a group key", func(t *testing.T) {
		refusedSaying(t, f.GroupBy(c("x").Sum()).Agg(c("x").Count()), ursus.ErrType,
			[]string{"group_by needs one value per input row"}, []string{"group_by produces"})
	})
	t.Run("Any and AllTrue name the Go methods", func(t *testing.T) {
		refusedSaying(t, f.GroupBy(c("k")).Agg(c("x").Any()), ursus.ErrType, []string{`Col("x").Gt(0).Any()`}, []string{".any()"})
		refusedSaying(t, f.GroupBy(c("k")).Agg(c("x").AllTrue()), ursus.ErrType, []string{`Col("x").Gt(0).AllTrue()`}, nil)
		got := runs(t, f.GroupBy(c("k")).Agg(c("x").Gt(1).Any().Alias("a")).Sort(ursus.Asc(c("k"))), "a")
		if !slices.Equal(got, []string{"true", "true"}) {
			t.Errorf("%v", got)
		}
	})
	t.Run("an ordered first depends on the order", func(t *testing.T) {
		// It said first was order-independent "except for first".
		refusedSaying(t, f.Select(ordered(c("x").First()).Alias("y")), ursus.ErrUnsupported,
			[]string{"first depends on the order", "sort the frame"}, []string{"order-independent", "does not depend"})
		// The recipe: the frame sorted, the window unordered. Rows come back in the
		// sorted frame's order.
		got := runs(t, f.Sort(ursus.Desc(c("x"))).Select(c("k"), c("x").First().Over(c("k")).Alias("y")), "y")
		if !slices.Equal(got, []string{"4", "2", "2"}) {
			t.Errorf("%v", got)
		}
	})
	t.Run("an ordered sum does not", func(t *testing.T) {
		refusedSaying(t, f.Select(ordered(c("x").Sum()).Alias("y")), ursus.ErrUnsupported,
			[]string{"sum does not depend on the order"}, []string{"except", "depends on the order"})
	})
}

// TestTemporalStatisticsAreRefusedWithACast: audit A13's last sentence. Mean of a
// Duration is a Duration; the rest of the temporal statistics are refused, and say
// how to reach the ticks.
func TestTemporalStatisticsAreRefusedWithACast(t *testing.T) {
	c := ursus.Col
	at := time.Date(2024, 2, 9, 0, 0, 0, 0, time.UTC)
	f := ursus.Frame(ursus.Values("ts", []time.Time{at, at.Add(48 * time.Hour)}), ursus.Values("from", []time.Time{at, at})).
		WithColumns(c("ts").Cast(ursus.Date).Alias("day"), c("ts").Sub(c("from")).Alias("d"))
	for _, tc := range []struct {
		name string
		e    ursus.Expr
	}{
		{"median of a Datetime", c("ts").Median()},
		{"quantile of a Date", c("day").Quantile(0.5, ursus.InterpLinear)},
		{"std of a Duration", c("d").Std(1)},
		{"mean of a Datetime", c("ts").Mean()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refusedSaying(t, f.GroupBy().Agg(tc.e.Alias("y")), ursus.ErrType,
				[]string{"requires a numeric operand", ".Cast(ursus.Int64) gives its ticks"}, nil)
		})
	}
	t.Run("the cast reaches the ticks", func(t *testing.T) {
		// 2024-02-09 is day 19762, and the other row is two days on.
		got := runs(t, f.GroupBy().Agg(c("day").Cast(ursus.Int64).Median().Alias("y")), "y")
		if !slices.Equal(got, []string{"19763"}) {
			t.Errorf("%v, want the median day 19763", got)
		}
	})
	t.Run("a Duration's mean is a Duration", func(t *testing.T) {
		df, err := f.GroupBy().Agg(c("d").Mean().Alias("y")).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if typ := df.Schema().Field(0).Type; typ.ID() != dtype.TypeDuration {
			t.Errorf("mean of a Duration is %s", typ)
		}
	})
}

// TestNegAndAbsOfUnsigned: audit S17. The doc said both were defined for the
// unsigned integers; Neg is refused, Abs is the identity, and the doc now says so.
func TestNegAndAbsOfUnsigned(t *testing.T) {
	c := ursus.Col
	f := ursus.Frame(ursus.Values("u", []uint8{3, 200}))
	refusedSaying(t, f.Select(c("u").Neg()), ursus.ErrType, []string{"neg()", "Cast(ursus.Int64)"}, nil)
	df, err := f.Select(c("u").Abs()).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if typ, got := textOf(t, df, "u"); typ != "Uint8" || !slices.Equal(got, []string{"3", "200"}) {
		t.Errorf("abs of Uint8: %s %v", typ, got)
	}
}

// TestUInt64ArithmeticWithAnIntLiteral: audit S14, refused until step 100, which gave
// Int128 its * // and %. It is pinned here so the row stays closed.
func TestUInt64ArithmeticWithAnIntLiteral(t *testing.T) {
	c := ursus.Col
	f := ursus.Frame(ursus.Values("u", []uint64{7, 1 << 63}))
	df, err := f.Select(c("u").Mul(2).Alias("m"), c("u").FloorDiv(2).Alias("q"), c("u").Mod(2).Alias("r")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]string{
		"m": {"14", "18446744073709551616"},
		"q": {"3", "4611686018427387904"},
		"r": {"1", "0"},
	} {
		if _, got := textOf(t, df, name); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
}

// TestADynamicGridAtTheEdgeOfTime: a row at the last instant a time.Time holds. The
// grid's step from it saturates and goes nowhere, and the guard said "every=1h does
// not advance … a bug in ursus". It is the data's doing, and every is not the cause.
func TestADynamicGridAtTheEdgeOfTime(t *testing.T) {
	c := ursus.Col
	const unixToInternal = 62135596800 // seconds from year 1 to 1970, time.Time's zero
	for _, tc := range []struct {
		name string
		secs []int64
	}{
		{"the last instant", []int64{math.MaxInt64 - unixToInternal - 9000, math.MaxInt64 - unixToInternal}},
		{"the first instant", []int64{math.MinInt64 + unixToInternal + 1, math.MinInt64 + unixToInternal + 9000}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lf := ursus.Frame(ursus.Values("i", tc.secs)).
				Select(c("i").Cast(dtype.Datetime(dtype.Second, "")).Alias("t")).
				GroupByDynamic(c("t"), ursus.DynamicOptions{Every: ursus.Every("1h"), Offset: ursus.Every("30m")}).
				Agg(c("t").Count().Alias("n"))
			df, err := lf.Collect(t.Context())
			if err == nil {
				// From Unix seconds the first instant cannot saturate a time.Time, so
				// that grid answers, and the answer must count both rows.
				_, n := textOf(t, df, "n")
				total := 0
				for _, v := range n {
					k, _ := strconv.Atoi(v)
					total += k
				}
				if total != len(tc.secs) {
					t.Errorf("the windows count %d rows, want %d: %v", total, len(tc.secs), n)
				}
			}
			if errors.Is(err, ursus.ErrInternal) || err != nil && strings.Contains(err.Error(), "every=") {
				t.Errorf("blames every, or ursus:\n%v", err)
			}
			if err != nil && !errors.Is(err, ursus.ErrValue) {
				t.Errorf("want a value error or an answer, got %v", err)
			}
			if tc.name == "the last instant" && !strings.Contains(fmtErr(err), "edge of what Go's time.Time holds") {
				t.Errorf("does not name the edge:\n%v", err)
			}
		})
	}
}

func fmtErr(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

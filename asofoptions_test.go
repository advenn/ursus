package ursus_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestAsOfToleranceAsPolars is J1 of v0.6.7-scope.md (step 315): an as-of join's
// tolerance on a numeric key, a number, and a tolerance of zero admitting only an
// exact match, an interval's too. Every answer is Polars 1.44.1's.
func TestAsOfToleranceAsPolars(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	l := ursus.Frame(i64s("k", 1, 5, 10, 20))
	r := ursus.Frame(i64s("k", 0, 4, 9, 30), strs("w", "a", "b", "c", "d"))
	lf := ursus.Frame(f64s("k", 1.0, 5.5, 10.0))
	rf := ursus.Frame(f64s("k", 0.9, 4.0, 9.95), strs("w", "a", "b", "c"))
	on := ursus.AsOfOn(c("k"))
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want []string
	}{
		{"an integer's 1", l.JoinAsOf(r, on, ursus.AsOfTolerance(1)), []string{"a", "b", "c", "null"}},
		{"an integer's 0, exact", l.JoinAsOf(r, on, ursus.AsOfTolerance(0)), []string{"null", "null", "null", "null"}},
		{"forward, 1", l.JoinAsOf(r, on, ursus.AsOfTolerance(1), ursus.AsOfStrategyOpt(ursus.AsOfForward)),
			[]string{"null", "null", "null", "null"}},
		{"nearest, 2", l.JoinAsOf(r, on, ursus.AsOfTolerance(2), ursus.AsOfStrategyOpt(ursus.AsOfNearest)),
			[]string{"a", "b", "c", "null"}},
		// A distance of integers is whole: 1.5 admits 1.
		{"an integer's 1.5", l.JoinAsOf(r, on, ursus.AsOfTolerance(1.5)), []string{"a", "b", "c", "null"}},
		{"an int64", l.JoinAsOf(r, on, ursus.AsOfTolerance(int64(1))), []string{"a", "b", "c", "null"}},
		// Inclusive: 1.0 to 0.9 is 0.1.
		{"a float's 0.1", lf.JoinAsOf(rf, on, ursus.AsOfTolerance(0.1)), []string{"a", "null", "c"}},
		{"a float's 1", lf.JoinAsOf(rf, on, ursus.AsOfTolerance(1)), []string{"a", "null", "c"}},
	} {
		df, err := tc.q.Collect(ctx)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := texts(t, df, "w"); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}

	t.Run("explained", func(t *testing.T) {
		plan, err := l.JoinAsOf(r, on, ursus.AsOfTolerance(5), ursus.AsOfCoalesce(false)).Explain(ctx)
		if err != nil || !strings.Contains(plan, "tolerance=5") || !strings.Contains(plan, "no coalesce") {
			t.Errorf("the plan does not say its tolerance and its keys: %v\n%s", err, plan)
		}
		days := ursus.Frame(ursus.Values("k", []time.Time{at(2024, 1, 1, 0)}))
		plan, err = days.JoinAsOf(days, on, ursus.AsOfTolerance(time.Duration(0))).Explain(ctx)
		if err != nil || !strings.Contains(plan, "tolerance=0s") {
			t.Errorf("the plan hides a tolerance of zero: %v\n%s", err, plan)
		}
	})

	t.Run("a zero interval is exact", func(t *testing.T) {
		ten := at(2024, 1, 1, 10)
		lt := ursus.Frame(ursus.Values("t", []time.Time{ten}))
		for _, tc := range []struct {
			name  string
			right time.Time
			tol   ursus.AsOfOption
			want  string
		}{
			{"0s, met", ten, ursus.AsOfTolerance(time.Duration(0)), "x"},
			{"0s, missed", ten.Add(-time.Minute), ursus.AsOfTolerance(time.Duration(0)), "null"},
			{"FromDuration(0), missed", ten.Add(-time.Minute), ursus.AsOfTolerance(ursus.FromDuration(0)), "null"},
			{"none, met", ten.Add(-time.Minute), nil, "x"},
		} {
			rt := ursus.Frame(ursus.Values("t", []time.Time{tc.right}), strs("w", "x"))
			opts := []ursus.AsOfOption{ursus.AsOfOn(c("t"))}
			if tc.tol != nil {
				opts = append(opts, tc.tol)
			}
			df, err := lt.JoinAsOf(rt, opts...).Collect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got := texts(t, df, "w"); got[0] != tc.want {
				t.Errorf("%s: %q, want %q", tc.name, got[0], tc.want)
			}
		}
	})

	t.Run("refused", func(t *testing.T) {
		days := ursus.Frame(ursus.Values("t", []time.Time{at(2024, 1, 1, 0)}))
		for _, tc := range []struct {
			name string
			q    *ursus.LazyFrame
			want string
		}{
			// Polars takes it, and searches as though it were 1.
			{"negative", l.JoinAsOf(r, on, ursus.AsOfTolerance(-1)), "tolerance must not be negative, got -1"},
			// Polars reads 5 on an instant as five of its ticks.
			{"a number on an instant", days.JoinAsOf(days, ursus.AsOfOn(c("t")), ursus.AsOfTolerance(5)),
				"tolerance=5 is a number, and the as-of key is Datetime(ns, UTC)"},
			// Polars panics: can only use timedelta string language with Date/Datetime/Duration/Time.
			{"time on an integer", l.JoinAsOf(r, on, ursus.AsOfTolerance(time.Second)), "tolerance is not supported on a Int64 key"},
		} {
			_, err := tc.q.Collect(ctx)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: got %v, want an error with %q", tc.name, err, tc.want)
			}
		}
	})
}

// TestAsOfCoalesceAsPolars (step 315): the ordering key merged where both sides name
// it alike, as Polars' coalesce=True; kept on both sides with AsOfCoalesce(false), or
// where the names differ, the right suffixed on a collision; the by-keys always
// merged. Every answer is Polars 1.44.1's.
func TestAsOfCoalesceAsPolars(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	l := ursus.Frame(i64s("k", 1, 5, 10, 20))
	r := ursus.Frame(i64s("k", 0, 4, 9, 30), strs("w", "a", "b", "c", "d"))
	got, err := l.JoinAsOf(r, ursus.AsOfOn(c("k")), ursus.AsOfCoalesce(false)).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want, err := ursus.Frame(i64s("k", 1, 5, 10, 20), i64s("k_right", 0, 4, 9, 9), strs("w", "a", "b", "c", "c")).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, want)

	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want []string
	}{
		{"merged by default", l.JoinAsOf(r, ursus.AsOfOn(c("k"))), []string{"k", "w"}},
		{"merged, asked", l.JoinAsOf(r, ursus.AsOfOn(c("k")), ursus.AsOfCoalesce(true)), []string{"k", "w"}},
		{"named apart", l.JoinAsOf(r.Rename(map[string]string{"k": "kk"}), ursus.AsOfLeftOn(c("k")), ursus.AsOfRightOn(c("kk"))),
			[]string{"k", "kk", "w"}},
		{"named apart, merging asked", l.JoinAsOf(r.Rename(map[string]string{"k": "kk"}), ursus.AsOfLeftOn(c("k")),
			ursus.AsOfRightOn(c("kk")), ursus.AsOfCoalesce(true)), []string{"k", "kk", "w"}},
		{"by-keys merged, named apart", l.WithColumns(ursus.Lit("a").Alias("g")).JoinAsOf(
			r.WithColumns(ursus.Lit("a").Alias("gg")).Rename(map[string]string{"k": "kk"}),
			ursus.AsOfLeftOn(c("k")), ursus.AsOfRightOn(c("kk")), ursus.AsOfLeftBy(c("g")), ursus.AsOfRightBy(c("gg"))),
			[]string{"k", "g", "kk", "w"}},
		{"by-keys merged, the key kept", l.WithColumns(ursus.Lit("a").Alias("g")).JoinAsOf(
			r.WithColumns(ursus.Lit("a").Alias("g")), ursus.AsOfOn(c("k")), ursus.AsOfBy(c("g")), ursus.AsOfCoalesce(false)),
			[]string{"k", "g", "k_right", "w"}},
	} {
		s, err := tc.q.CollectSchema(ctx)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := s.Names(); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestJoinWhereSuffix (step 315): Polars' join_where(suffix=), the predicates naming
// a colliding right column by it. Polars 1.44.1's answer.
func TestJoinWhereSuffix(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	a := ursus.Frame(i64s("a", 1, 2), i64s("x", 10, 20))
	b := ursus.Frame(i64s("b", 1, 2), i64s("x", 5, 25))
	got, err := a.JoinWhereSuffix(b, "_r", c("x").Lt(c("x_r"))).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want, err := ursus.Frame(i64s("a", 1, 2), i64s("x", 10, 20), i64s("b", 2, 2), i64s("x_r", 25, 25)).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, want, ursustest.IgnoreRowOrder())
	if _, err := a.JoinWhereSuffix(b, "", c("x").Lt(c("x_right"))).Collect(ctx); err == nil ||
		!strings.Contains(err.Error(), "the join suffix must not be empty") {
		t.Errorf("an empty suffix: %v", err)
	}
}

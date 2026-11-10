package ursus_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

// TestAsOfToleranceAsPolars is J1 of v0.6.7-scope.md (step 315): an as-of join's
// tolerance on a numeric key, a number, and a tolerance of zero admitting only an
// exact match, an interval's too. The rows are the asofoptions group's, Polars 2.1.0's
// (oracle_asofoptions_test.go); here stay the plan and the refusals, which
// feature-gaps.md §9 documents.
func TestAsOfToleranceAsPolars(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	l := ursus.Frame(i64s("k", 1, 5, 10, 20))
	r := ursus.Frame(i64s("k", 0, 4, 9, 30), strs("w", "a", "b", "c", "d"))
	on := ursus.AsOfOn(c("k"))

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
// merged. The columns are the schema's; the values the asofoptions group's.
func TestAsOfCoalesceAsPolars(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	l := ursus.Frame(i64s("k", 1, 5, 10, 20))
	r := ursus.Frame(i64s("k", 0, 4, 9, 30), strs("w", "a", "b", "c", "d"))
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want []string
	}{
		{"merged by default", l.JoinAsOf(r, ursus.AsOfOn(c("k"))), []string{"k", "w"}},
		{"merged, asked", l.JoinAsOf(r, ursus.AsOfOn(c("k")), ursus.AsOfCoalesce(true)), []string{"k", "w"}},
		{"kept apart", l.JoinAsOf(r, ursus.AsOfOn(c("k")), ursus.AsOfCoalesce(false)), []string{"k", "k_right", "w"}},
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
// a colliding right column by it; the rows are the asofoptions group's. An empty
// suffix is refused.
func TestJoinWhereSuffix(t *testing.T) {
	c := ursus.Col
	a := ursus.Frame(i64s("a", 1, 2), i64s("x", 10, 20))
	b := ursus.Frame(i64s("b", 1, 2), i64s("x", 5, 25))
	if _, err := a.JoinWhereSuffix(b, "", c("x").Lt(c("x_right"))).Collect(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "the join suffix must not be empty") {
		t.Errorf("an empty suffix: %v", err)
	}
}

package ursus_test

// Step 167: a join's built keys filter its probe side where the probe key comes
// from, below the joins between. The answer must be the one without the rule, as a
// set, and the filter must go only where it cannot drop a row the join keeps.

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/plan"
)

// noRuntimeFilters is the default optimizer without the runtime filters.
func noRuntimeFilters() plan.Flags {
	f := plan.DefaultFlags()
	f.RuntimeFilters = false
	return f
}

// runtimeSides are a fact frame (k, s, v), a middle frame (s, name) it joins on s,
// and a small dimension (k, tag) whose keys are a few of the fact's: nullable keys,
// a few duplicated.
func runtimeSides(seed uint64) (fact, middle, dim *ursus.LazyFrame) {
	rng := rand.New(rand.NewPCG(167, seed))
	const n = 5_000
	k, s, v := make([]int64, n), make([]int64, n), make([]int64, n)
	kok, sok := make([]bool, n), make([]bool, n)
	for i := range n {
		k[i], kok[i] = int64(rng.IntN(2_000)), rng.IntN(25) != 0
		s[i], sok[i] = int64(rng.IntN(50)), rng.IntN(25) != 0
		v[i] = int64(i)
	}
	ms, mok := make([]int64, 60), make([]bool, 60)
	names := make([]string, 60)
	for i := range ms {
		ms[i], mok[i], names[i] = int64(i), i%13 != 0, fmt.Sprintf("n%d", i)
	}
	dk, dok := make([]int64, 40), make([]bool, 40)
	tags := make([]string, 40)
	for i := range dk {
		dk[i], dok[i], tags[i] = int64(rng.IntN(2_000)), i%9 != 0, fmt.Sprintf("t%d", i)
	}
	fact = ursus.Frame(ursus.ValuesNullable("k", k, kok), ursus.ValuesNullable("s", s, sok), ursus.Values("v", v))
	middle = ursus.Frame(ursus.ValuesNullable("s", ms, mok), ursus.Values("name", names))
	dim = ursus.Frame(ursus.ValuesNullable("k", dk, dok), ursus.Values("tag", tags))
	return fact, middle, dim
}

func TestRuntimeFiltersKeepTheAnswer(t *testing.T) {
	c := ursus.Col
	between := []ursus.JoinKind{ursus.JoinInner, ursus.JoinLeft, ursus.JoinSemi}
	outer := []ursus.JoinKind{ursus.JoinInner, ursus.JoinSemi, ursus.JoinLeft, ursus.JoinAnti}
	// On one thread every build streams and never publishes, so a filter must keep
	// every row; on eight the builds are partitioned and publish.
	for _, threads := range []int{1, 8} {
		for _, mid := range between {
			for _, top := range outer {
				seed := uint64(threads)
				t.Run(fmt.Sprintf("%s then %s, %d threads", mid, top, threads), func(t *testing.T) {
					q := func() *ursus.LazyFrame {
						fact, middle, dim := runtimeSides(seed)
						return fact.Join(middle, ursus.JoinOn(c("s")), ursus.JoinHow(mid)).
							Join(dim, ursus.JoinOn(c("k")), ursus.JoinHow(top))
					}
					opts := []ursus.CollectOption{ursus.WithThreads(threads), ursus.WithBatchSize(256)}
					want, err := q().Collect(t.Context(), append(opts, ursus.WithOptFlags(noRuntimeFilters()))...)
					if err != nil {
						t.Fatal(err)
					}
					got, err := q().Collect(t.Context(), opts...)
					if err != nil {
						t.Fatal(err)
					}
					if !sameRows(t, got, want) {
						p, _ := q().Explain(t.Context())
						t.Fatalf("%d rows, without runtime filters %d, or they differ:\n%s", got.Height(), want.Height(), p)
					}
				})
			}
		}
	}
}

// TestRuntimeFiltersGoOnlyWhereTheyCannotDropAJoinedRow: by the plan.
func TestRuntimeFiltersGoOnlyWhereTheyCannotDropAJoinedRow(t *testing.T) {
	c := ursus.Col
	fact, middle, dim := runtimeSides(0)
	on := func(kind ursus.JoinKind) []ursus.JoinOption {
		return []ursus.JoinOption{ursus.JoinOn(c("k")), ursus.JoinHow(kind)}
	}
	narrow := ursus.Frame(ursus.Values("k", []int32{1, 2}), ursus.Values("tag", []string{"a", "b"}))
	// k is Int32 here and Int64 in the fact: the join between merges them at Int64.
	narrowFact := ursus.Frame(ursus.Values("k", []int32{1, 2, 3}), ursus.Values("w", []int64{7, 8, 9}))
	cases := []struct {
		name string
		lf   *ursus.LazyFrame
		want bool
	}{
		{"inner, through a join", fact.Join(middle, ursus.JoinOn(c("s"))).Join(dim, on(ursus.JoinInner)...), true},
		{"semi, through a join", fact.Join(middle, ursus.JoinOn(c("s"))).Join(dim, on(ursus.JoinSemi)...), true},
		{"a left join keeps its unmatched rows", fact.Join(middle, ursus.JoinOn(c("s"))).Join(dim, on(ursus.JoinLeft)...), false},
		{"an anti join keeps them", fact.Join(middle, ursus.JoinOn(c("s"))).Join(dim, on(ursus.JoinAnti)...), false},
		{"nothing between: the join's own lookups", fact.Join(dim, on(ursus.JoinInner)...), false},
		{"nulls equal", fact.Join(middle, ursus.JoinOn(c("s"))).
			Join(dim, ursus.JoinOn(c("k")), ursus.JoinNullsEqual(true)), false},
		// The probe's k is Int64 and the dimension's Int32: they meet at Int64, which
		// the probe column has, so it filters; the other way round it would not.
		{"a key of the probe's type", fact.Join(middle, ursus.JoinOn(c("s"))).Join(narrow, on(ursus.JoinInner)...), true},
		// The key comes from the null-extended side of the left join between.
		{"from a side the join between null-extends", middle.Join(fact, ursus.JoinOn(c("s")), ursus.JoinHow(ursus.JoinLeft)).
			Join(dim, on(ursus.JoinInner)...), false},
		{"through a validated join", fact.Join(middle, ursus.JoinOn(c("s")), ursus.JoinValidate(ursus.ValidateManyToMany)).
			Join(dim, on(ursus.JoinInner)...), false},
		// The join between merges an Int32 k into the fact's Int64 one. Below it, k is
		// another type, so the filter stops above that join, where it gains nothing.
		{"a key promoted at the join between", narrowFact.Join(fact, ursus.JoinOn(c("k"))).
			Join(dim, on(ursus.JoinInner)...), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := tc.lf.Explain(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(p, "RUNTIME FILTER"); got != tc.want {
				t.Errorf("runtime filter placed = %v, want %v:\n%s", got, tc.want, p)
			}
		})
	}
}

// TestARuntimeFilterNeverEntersACache: a subtree the query reads twice is shared, so
// a filter for one reader's join may not go into it.
func TestARuntimeFilterNeverEntersACache(t *testing.T) {
	c := ursus.Col
	fact, middle, dim := runtimeSides(1)
	shared := fact.Join(middle, ursus.JoinOn(c("s")))
	lf := ursus.Concat([]*ursus.LazyFrame{
		shared.Join(dim, ursus.JoinOn(c("k"))).Select(c("k"), c("v")),
		shared.Select(c("k"), c("v")),
	})
	p, err := lf.Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "CACHE") {
		t.Fatalf("the fixture shares nothing:\n%s", p)
	}
	if strings.Contains(p, "RUNTIME FILTER") {
		t.Fatalf("a runtime filter went into the shared subtree:\n%s", p)
	}
	want, err := lf.Collect(t.Context(), ursus.WithOptFlags(noRuntimeFilters()))
	if err != nil {
		t.Fatal(err)
	}
	got, err := lf.Collect(t.Context(), ursus.WithThreads(8))
	if err != nil {
		t.Fatal(err)
	}
	if !sameRows(t, got, want) {
		t.Fatalf("%d rows, want %d", got.Height(), want.Height())
	}
}

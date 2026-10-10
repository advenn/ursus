package ursus_test

// Step 165: a predicate above a join that names both sides still implies something
// about each, when every arm of its Or constrains that side alone. The implied
// predicates go below the join; the original stays above it. The answer must be the
// one with join pushdown off, for every kind of join and with nulls.

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// impliedSides are a left frame (k, a, s) and a right (k, b), keys and values
// nullable; s is text, numeric but on the left rows whose key never joins.
func impliedSides(seed uint64) (*ursus.LazyFrame, *ursus.LazyFrame) {
	rng := rand.New(rand.NewPCG(165, seed))
	const n = 600
	lk, a := make([]int64, n), make([]int64, n)
	lkok, aok := make([]bool, n), make([]bool, n)
	s := make([]string, n)
	for i := range n {
		lk[i], lkok[i] = int64(rng.IntN(40)), rng.IntN(15) != 0
		a[i], aok[i] = int64(rng.IntN(6)), rng.IntN(8) != 0
		s[i] = fmt.Sprint(rng.IntN(5))
		if lk[i] >= 30 { // no right key is 30 or more
			s[i] = "not a number"
		}
	}
	rk, b := make([]int64, n), make([]int64, n)
	rkok, bok := make([]bool, n), make([]bool, n)
	for i := range n {
		rk[i], rkok[i] = int64(rng.IntN(30)), rng.IntN(15) != 0
		b[i], bok[i] = int64(rng.IntN(6)), rng.IntN(8) != 0
	}
	left := ursus.Frame(ursus.ValuesNullable("k", lk, lkok), ursus.ValuesNullable("a", a, aok),
		ursus.Values("s", s))
	right := ursus.Frame(ursus.ValuesNullable("k", rk, rkok), ursus.ValuesNullable("b", b, bok))
	return left, right
}

func TestImpliedPredicatesKeepTheAnswer(t *testing.T) {
	c := ursus.Col
	preds := map[string]ursus.Expr{
		"q19's shape": c("a").Eq(int64(1)).And(c("b").Eq(int64(2))).
			Or(c("a").Eq(int64(3)).And(c("b").IsIn(int64(4), int64(5)))),
		"an arm with nothing on the left": c("a").Eq(int64(1)).And(c("b").Eq(int64(2))).
			Or(c("b").Eq(int64(5))),
		"one And over both sides": c("a").Gt(int64(2)).And(c("b").Lt(int64(3))),
		"nulls decide": c("a").Gt(int64(2)).And(c("b").IsNull()).
			Or(c("a").IsNull().And(c("b").Lt(int64(3)))),
		"the merged key": c("k").Eq(int64(3)).And(c("b").Eq(int64(1))).Or(c("k").Eq(int64(5))),
		// The cast fails on the left rows that never join, so it may not go below.
		"a fallible arm": c("s").Cast(ursus.Int64).Gt(int64(0)).And(c("b").Eq(int64(1))).
			Or(c("a").Eq(int64(2)).And(c("b").Eq(int64(3)))),
	}
	kinds := []ursus.JoinKind{ursus.JoinInner, ursus.JoinLeft, ursus.JoinRight, ursus.JoinFull}
	for name, pred := range preds {
		for _, kind := range kinds {
			if name == "a fallible arm" && (kind == ursus.JoinLeft || kind == ursus.JoinFull) {
				// Their unjoined left rows reach the filter above, which casts their
				// text and fails, with or without pushdown.
				continue
			}
			for seed := range uint64(3) {
				t.Run(fmt.Sprintf("%s, %s, seed %d", name, kind, seed), func(t *testing.T) {
					q := func() *ursus.LazyFrame {
						l, r := impliedSides(seed)
						return l.Join(r, ursus.JoinOn(c("k")), ursus.JoinHow(kind)).Filter(pred)
					}
					want, err := q().Collect(t.Context(), ursus.WithOptFlags(noJoinPushdown()))
					if err != nil {
						t.Fatal(err)
					}
					got, err := q().Collect(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					ursustest.AssertFrameEqual(t, got, want, ursustest.IgnoreRowOrder())
				})
			}
		}
	}
}

// TestAnOrImpliesWhatEachSideMustHold: where the implied predicates go, by the plan.
func TestAnOrImpliesWhatEachSideMustHold(t *testing.T) {
	c := ursus.Col
	l, r := impliedSides(0)
	explain := func(lf *ursus.LazyFrame) string {
		t.Helper()
		p, err := lf.Explain(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	pred := c("a").Eq(int64(1)).And(c("b").Eq(int64(2))).Or(c("a").Eq(int64(3)).And(c("b").Eq(int64(4))))
	cases := []struct {
		name       string
		kind       ursus.JoinKind
		pred       ursus.Expr
		a1, b2, s0 int // how often each condition appears in the plan
	}{
		// Above the join, and once more on each side.
		{"inner", ursus.JoinInner, pred, 2, 2, 0},
		// A left join's right side is null-extended, so nothing may go into it.
		{"left", ursus.JoinLeft, pred, 2, 1, 0},
		{"an arm with nothing on the left", ursus.JoinInner,
			c("a").Eq(int64(1)).And(c("b").Eq(int64(2))).Or(c("b").Eq(int64(4))), 1, 2, 0},
		{"a fallible arm", ursus.JoinInner,
			c("s").Cast(ursus.Int64).Gt(int64(0)).And(c("b").Eq(int64(2))).
				Or(c("a").Eq(int64(1)).And(c("b").Eq(int64(4)))), 1, 2, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := explain(l.Join(r, ursus.JoinOn(c("k")), ursus.JoinHow(tc.kind)).Filter(tc.pred))
			got := []int{
				strings.Count(p, `(col("a") == lit(1))`),
				strings.Count(p, `(col("b") == lit(2))`),
				strings.Count(p, `col("s").cast`),
			}
			if want := []int{tc.a1, tc.b2, tc.s0}; fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("a == 1, b == 2 and the cast appear %v times, want %v:\n%s", got, want, p)
			}
		})
	}
}

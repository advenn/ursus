package ursus_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestJoinMaintainOrderAsPolars is J1 of v0.6.7-scope.md (step 314): Polars'
// maintain_order, for every kind of join. The rows each kind and order give are the
// joinorder group's, Polars 2.1.0's (oracle_joinorder_test.go); here stay the
// validation's message and the plan.
func TestJoinMaintainOrderAsPolars(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	l := ursus.Frame(i64s("k", 3, 1, 2, 1, 9), strs("l", "l0", "l1", "l2", "l3", "l4"))
	r := ursus.Frame(i64s("k", 1, 3, 1, 7, 3), strs("r", "r0", "r1", "r2", "r3", "r4"))

	t.Run("a validation names the sides", func(t *testing.T) {
		dup := ursus.Frame(i64s("k", 1, 1), strs("r", "a", "b"))
		_, err := l.Join(dup, ursus.JoinOn(c("k")), ursus.JoinValidate(ursus.ValidateManyToOne),
			ursus.JoinMaintainOrder(ursus.JoinOrderRight)).Collect(ctx)
		if err == nil || !strings.Contains(err.Error(), "validate=m:1 requires unique keys on the right side") {
			t.Errorf("got %v", err)
		}
		_, err = dup.Join(l, ursus.JoinOn(c("k")), ursus.JoinValidate(ursus.ValidateOneToMany),
			ursus.JoinMaintainOrder(ursus.JoinOrderRight)).Collect(ctx)
		if err == nil || !strings.Contains(err.Error(), "validate=1:m requires unique keys on the left side") {
			t.Errorf("got %v", err)
		}
	})

	t.Run("explained and refused", func(t *testing.T) {
		plan, err := l.Join(r, ursus.JoinOn(c("k")), ursus.JoinMaintainOrder(ursus.JoinOrderRightLeft)).Explain(ctx)
		if err != nil || !strings.Contains(plan, "maintain_order right_left") {
			t.Errorf("the plan does not say its order: %v\n%s", err, plan)
		}
		_, err = l.Join(r, ursus.JoinOn(c("k")), ursus.JoinMaintainOrder(ursus.JoinOrder(9))).Collect(ctx)
		if err == nil || !strings.Contains(err.Error(), "the join order JoinOrder(9) is not one of the five") {
			t.Errorf("got %v", err)
		}
	})
}

// TestJoinOrderUnderAMemoryLimit (step 314): an ordered join whose table spills
// sorts its rows back, so it answers in the order it does in memory, for every kind
// and both orders, on one thread and on four: 4,000 rows a side of 2,000 keys, each
// side's keys in an order of their own, a fifth of each side unmatched, under 32 KiB.
// Under the race detector, one thread.
func TestJoinOrderUnderAMemoryLimit(t *testing.T) {
	ctx := t.Context()
	const n, keys = 4_000, 2_000
	threads := []int{1, 4}
	if raceDetector {
		threads = threads[:1]
	}
	lk, rk, lv, rv := make([]int64, n), make([]int64, n), make([]int64, n), make([]int64, n)
	for i := range n {
		lk[i], rk[i] = int64((i*7)%keys), int64((i*11)%keys+keys/5)
		lv[i], rv[i] = int64(i), int64(n+i)
	}
	l := ursus.Frame(ursus.Values("k", lk), ursus.Values("lv", lv))
	r := ursus.Frame(ursus.Values("k", rk), ursus.Values("rv", rv))
	for _, kind := range []ursus.JoinKind{ursus.JoinInner, ursus.JoinLeft, ursus.JoinRight, ursus.JoinFull,
		ursus.JoinSemi, ursus.JoinAnti} {
		for _, order := range []ursus.JoinOrder{ursus.JoinOrderLeft, ursus.JoinOrderRight} {
			q := l.Join(r, ursus.JoinOn(ursus.Col("k")), ursus.JoinHow(kind), ursus.JoinMaintainOrder(order))
			want, err := q.Collect(ctx, ursus.WithBatchSize(128), ursus.WithMemoryLimit(0))
			if err != nil {
				t.Fatal(err)
			}
			for _, threads := range threads {
				name := fmt.Sprintf("%s, %s, %d threads", kind, order, threads)
				var st ursus.MemoryStats
				got, err := q.Collect(ctx, ursus.WithBatchSize(128), ursus.WithThreads(threads),
					ursus.WithMemoryLimit(32<<10), ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&st))
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if st.Spills == 0 {
					t.Errorf("%s answered without spilling, so it proves nothing", name)
				}
				t.Run(name, func(t *testing.T) { ursustest.AssertFrameEqual(t, got, want) })
			}
		}
	}
}

// TestOrderedJoinLeavesTheLimitToTheJoin (step 314): the sort that puts a spilled
// ordered join's rows back holds a quarter of the limit at most, and spills the rest,
// so the join's spilled buckets have the limit to build their tables in. Holding as
// much as the query left it, it starved them: 20,000 rows a side of 3,000 keys, whose
// answer is 107,000 rows, refused under 64 KiB, "a single join key has more build rows
// than the limit".
func TestOrderedJoinLeavesTheLimitToTheJoin(t *testing.T) {
	ctx := t.Context()
	const n = 20_000
	lk, rk, lv, rv := make([]int64, n), make([]int64, n), make([]int64, n), make([]int64, n)
	for i := range n {
		lk[i], rk[i], lv[i], rv[i] = int64((i*7)%3000), int64((i*11)%3000+600), int64(i), int64(n+i)
	}
	q := ursus.Frame(ursus.Values("k", lk), ursus.Values("lv", lv)).
		Join(ursus.Frame(ursus.Values("k", rk), ursus.Values("rv", rv)), ursus.JoinOn(ursus.Col("k")),
			ursus.JoinMaintainOrder(ursus.JoinOrderLeft))
	want, err := q.Collect(ctx, ursus.WithBatchSize(256), ursus.WithMemoryLimit(0))
	if err != nil {
		t.Fatal(err)
	}
	got, err := q.Collect(ctx, ursus.WithBatchSize(256), ursus.WithThreads(1), ursus.WithMemoryLimit(64<<10),
		ursus.WithSpillDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, want)
}

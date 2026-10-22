package ursus_test

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestJoinMaintainOrderAsPolars is J1 of v0.6.7-scope.md (step 314): Polars'
// maintain_order, for every kind of join. Each row is its left and right values,
// "null" where a side is missing. Every answer is Polars 1.44.1's.
func TestJoinMaintainOrderAsPolars(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	l := ursus.Frame(i64s("k", 3, 1, 2, 1, 9), strs("l", "l0", "l1", "l2", "l3", "l4"))
	r := ursus.Frame(i64s("k", 1, 3, 1, 7, 3), strs("r", "r0", "r1", "r2", "r3", "r4"))
	leftInner := []string{"l0 r1", "l0 r4", "l1 r0", "l1 r2", "l3 r0", "l3 r2"}
	rightInner := []string{"l1 r0", "l3 r0", "l0 r1", "l1 r2", "l3 r2", "l0 r4"}
	want := map[ursus.JoinKind]map[bool][]string{
		ursus.JoinInner: {false: leftInner, true: rightInner},
		ursus.JoinLeft: {
			false: {"l0 r1", "l0 r4", "l1 r0", "l1 r2", "l2 null", "l3 r0", "l3 r2", "l4 null"},
			true:  {"l1 r0", "l3 r0", "l0 r1", "l1 r2", "l3 r2", "l0 r4", "l2 null", "l4 null"},
		},
		ursus.JoinRight: {
			false: {"l0 r1", "l0 r4", "l1 r0", "l1 r2", "l3 r0", "l3 r2", "null r3"},
			true:  {"l1 r0", "l3 r0", "l0 r1", "l1 r2", "l3 r2", "null r3", "l0 r4"},
		},
		ursus.JoinFull: {
			false: {"l0 r1", "l0 r4", "l1 r0", "l1 r2", "l2 null", "l3 r0", "l3 r2", "l4 null", "null r3"},
			true:  {"l1 r0", "l3 r0", "l0 r1", "l1 r2", "l3 r2", "null r3", "l0 r4", "l2 null", "l4 null"},
		},
	}
	orders := map[ursus.JoinOrder]bool{ursus.JoinOrderLeft: false, ursus.JoinOrderLeftRight: false,
		ursus.JoinOrderRight: true, ursus.JoinOrderRightLeft: true}
	for kind, byOrder := range want {
		for order, rightward := range orders {
			q := l.Join(r, ursus.JoinOn(c("k")), ursus.JoinHow(kind), ursus.JoinMaintainOrder(order))
			if got := pairs(t, q); !slices.Equal(got, byOrder[rightward]) {
				t.Errorf("%s, %s:\ngot  %q\nwant %q", kind, order, got, byOrder[rightward])
			}
		}
	}
	// A semi or anti join keeps the left order, either way.
	for order := range orders {
		for kind, w := range map[ursus.JoinKind][]string{ursus.JoinSemi: {"l0", "l1", "l3"}, ursus.JoinAnti: {"l2", "l4"}} {
			df, err := l.Join(r, ursus.JoinOn(c("k")), ursus.JoinHow(kind), ursus.JoinMaintainOrder(order)).Collect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got := texts(t, df, "l"); !slices.Equal(got, w) {
				t.Errorf("%s, %s: %q, want %q", kind, order, got, w)
			}
		}
	}
	// A cross join: each left row beside every right row, or each right row beside
	// every left row.
	small := func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Head(2) }
	for order, w := range map[ursus.JoinOrder][]string{
		ursus.JoinOrderLeft:  {"l0 r0", "l0 r1", "l1 r0", "l1 r1"},
		ursus.JoinOrderRight: {"l0 r0", "l1 r0", "l0 r1", "l1 r1"},
	} {
		q := small(l).Join(small(r), ursus.JoinHow(ursus.JoinCross), ursus.JoinMaintainOrder(order))
		if got := pairs(t, q); !slices.Equal(got, w) {
			t.Errorf("cross, %s: %q, want %q", order, got, w)
		}
	}

	t.Run("a right join's key", func(t *testing.T) {
		// The right key, a right join's merged key here, on every row, the unmatched
		// r3's 7 too; and a full join's, merged, the left's where a row has one.
		for order := range orders {
			df, err := l.Join(r, ursus.JoinOn(c("k")), ursus.JoinHow(ursus.JoinRight),
				ursus.JoinMaintainOrder(order)).Collect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]int64{"r0": 1, "r1": 3, "r2": 1, "r3": 7, "r4": 3}
			for i, rv := range texts(t, df, "r") {
				if k, ok, err := df.At[int64](i, "k"); err != nil || !ok || k != want[rv] {
					t.Errorf("right, %s: row %d (%s) has the key %d, %v, %v; want %d", order, i, rv, k, ok, err, want[rv])
				}
			}
			df, err = l.Join(r, ursus.JoinOn(c("k")), ursus.JoinHow(ursus.JoinFull), ursus.JoinCoalesce(true),
				ursus.JoinMaintainOrder(order)).Collect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			lkey := map[string]int64{"l0": 3, "l1": 1, "l2": 2, "l3": 1, "l4": 9}
			rs := texts(t, df, "r")
			for i, lv := range texts(t, df, "l") {
				w, ok := lkey[lv]
				if !ok {
					w = want[rs[i]]
				}
				if k, ok, err := df.At[int64](i, "k"); err != nil || !ok || k != w {
					t.Errorf("full, %s: row %d (%s %s) has the key %d, %v, %v; want %d", order, i, lv, rs[i], k, ok, err, w)
				}
			}
		}
	})

	t.Run("the key is the plan's", func(t *testing.T) {
		// Polars: -0.0, the left's, whatever the order.
		fl := ursus.Frame(f64s("k", math.Copysign(0, -1), 1.0), strs("l", "a", "b"))
		fr := ursus.Frame(f64s("k", 0.0, 1.0), strs("r", "x", "y"))
		for _, kind := range []ursus.JoinKind{ursus.JoinInner, ursus.JoinLeft, ursus.JoinFull} {
			df, err := fl.Join(fr, ursus.JoinOn(c("k")), ursus.JoinHow(kind), ursus.JoinCoalesce(true),
				ursus.JoinMaintainOrder(ursus.JoinOrderRight)).Collect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if k, ok, err := df.At[float64](0, "k"); err != nil || !ok || !math.Signbit(k) {
				t.Errorf("%s: the first key is %v, %v, %v, want -0, the left's", kind, k, ok, err)
			}
		}
	})

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

// pairs is each row of a join of frames of l and r as "l r", "null" for a null.
func pairs(t *testing.T, q *ursus.LazyFrame) []string {
	t.Helper()
	df, err := q.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ls, rs := texts(t, df, "l"), texts(t, df, "r")
	out := make([]string, len(ls))
	for i := range ls {
		out[i] = ls[i] + " " + rs[i]
	}
	return out
}

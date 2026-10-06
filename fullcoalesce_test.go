package ursus_test

// A full join merges its keys on request (step 110).
//
// audit.md J9: JoinCoalesce(true) on a full join was refused, with a hint that ursus
// had no coalesce expression to merge the keys with — ursus.Coalesce existed. A full
// join can lack either side, so its merged key is the left value where there is a
// left row and the right value where there is not.

import (
	"fmt"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/source/memsrc"
	"github.com/advenn/ursus/ursustest"
)

func TestFullJoinCoalescesItsKeys(t *testing.T) {
	c := ursus.Col
	left := ursus.Frame(
		ursus.ValuesNullable("k", []int64{1, 2, 2, 4, 0}, []bool{true, true, true, true, false}),
		ursus.Values("j", []int64{10, 20, 21, 40, 0}),
		ursus.Values("a", []string{"a1", "a2", "a2b", "a4", "anull"}),
	)
	right := ursus.Frame(
		ursus.ValuesNullable("k", []int64{2, 3, 4, 0, 5}, []bool{true, true, true, false, true}),
		ursus.Values("j", []int64{20, 30, 41, 0, 50}),
		ursus.Values("b", []string{"b2", "b3", "b4", "bnull", "b5"}),
	)
	// The oracle: the default full join keeps both key columns, the right one
	// suffixed; coalescing them by hand is what the merged key must be. rest is the
	// columns that are not keys, in the merged join's order.
	manual := func(keys, rest []string, opts []ursus.JoinOption) *ursus.LazyFrame {
		var sel []ursus.Expr
		for _, k := range keys {
			sel = append(sel, ursus.Coalesce(c(k), c(k+"_right")).Alias(k))
		}
		for _, name := range rest {
			sel = append(sel, c(name))
		}
		return left.Join(right, append(opts, ursus.JoinHow(ursus.JoinFull))...).Select(sel...)
	}

	cases := []struct {
		name       string
		keys, rest []string
		opts       []ursus.JoinOption
	}{
		{"one key", []string{"k"}, []string{"j", "a", "j_right", "b"},
			[]ursus.JoinOption{ursus.JoinOn(c("k"))}},
		{"two keys", []string{"k", "j"}, []string{"a", "b"},
			[]ursus.JoinOption{ursus.JoinOn(c("k"), c("j"))}},
		{"null keys equal", []string{"k"}, []string{"j", "a", "j_right", "b"},
			[]ursus.JoinOption{ursus.JoinOn(c("k")), ursus.JoinNullsEqual(true)}},
	}
	for _, tc := range cases {
		for _, threads := range []int{1, 4} {
			t.Run(fmt.Sprintf("%s, %d threads", tc.name, threads), func(t *testing.T) {
				want, err := manual(tc.keys, tc.rest, tc.opts).Collect(t.Context(), ursus.WithThreads(threads))
				if err != nil {
					t.Fatal(err)
				}
				q := left.Join(right, append(tc.opts, ursus.JoinHow(ursus.JoinFull), ursus.JoinCoalesce(true))...)
				got, err := q.Collect(t.Context(), ursus.WithThreads(threads))
				if err != nil {
					t.Fatal(err)
				}
				if !got.Schema().Equal(want.Schema()) {
					t.Fatalf("schema %s, want %s", got.Schema(), want.Schema())
				}
				ursustest.AssertFrameEqual(t, sortedByAll(t, got), sortedByAll(t, want))
			})
		}
	}

	t.Run("an Int32 key against an Int64 one merges in Int64", func(t *testing.T) {
		l32 := left.Select(c("k").Cast(dtype.Int32), c("a"))
		got, err := l32.Join(right.Select(c("k"), c("b")), ursus.JoinOn(c("k")),
			ursus.JoinHow(ursus.JoinFull), ursus.JoinCoalesce(true)).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		typ, keys := textOf(t, sortedByAll(t, got), "k")
		if typ != "Int64" {
			t.Errorf("the merged key is %s, want Int64", typ)
		}
		// Left 1, 2, 2, 4, null; right 2, 3, 4, null, 5: matched 2 (twice) and 4, the
		// unmatched left 1 and null, the unmatched right 3, 5 and null.
		want := []string{"∅", "∅", "1", "2", "2", "3", "4", "5"}
		if fmt.Sprint(keys) != fmt.Sprint(want) {
			t.Errorf("keys %v, want %v", keys, want)
		}
	})
}

// TestFullJoinCoalescesItsKeysWhenItSpills: the same merged key when the build side
// goes to disk, where a null-keyed build row comes back from its own bucket with
// every left column absent and its key read from the right.
func TestFullJoinCoalescesItsKeysWhenItSpills(t *testing.T) {
	left := joinFrame(t, 8000, 256, 6000, 37, true)
	right := joinFrame(t, 4000, 256, 4000, 53, true)
	q := func() *ursus.LazyFrame {
		return joinQuery(left, right, ursus.JoinFull).Select(ursus.Col("k"), ursus.Col("v"), ursus.Col("k2"), ursus.Col("v2"))
	}
	merged := func() *ursus.LazyFrame {
		return ursus.Scan(left).Select(ursus.Col("k"), ursus.Col("v")).
			Join(ursus.Scan(right).Select(ursus.Col("k").Alias("k2"), ursus.Col("v").Alias("v2")),
				ursus.JoinHow(ursus.JoinFull), ursus.JoinCoalesce(true),
				ursus.JoinLeftOn(ursus.Col("k")), ursus.JoinRightOn(ursus.Col("k2")))
	}
	want, err := q().Select(ursus.Coalesce(ursus.Col("k"), ursus.Col("k2")).Alias("k"), ursus.Col("v"), ursus.Col("v2")).
		Collect(t.Context(), ursus.WithBatchSize(256))
	if err != nil {
		t.Fatal(err)
	}
	var stats ursus.MemoryStats
	got, err := merged().Collect(t.Context(), ursus.WithBatchSize(256),
		ursus.WithMemoryLimit(32<<10), ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&stats))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Spills < 2 {
		t.Fatalf("spilled %d times, so this proves nothing", stats.Spills)
	}
	ursustest.AssertFrameEqual(t, sortedByAll(t, got), sortedByAll(t, want))
}

// TestFullJoinCoalescesTwoKeysWhenItSpills: with two keys, a build row keyed
// (null, 1) is null-keyed and spills to its own bucket, where every left column is
// absent — and its 1 must still come back, from the right. One key cannot show this:
// a null-keyed row's single key is null whichever side it is read from.
func TestFullJoinCoalescesTwoKeysWhenItSpills(t *testing.T) {
	left := joinFrame(t, 8000, 256, 6000, 37, true)
	right := joinFrame(t, 4000, 256, 4000, 53, true)
	side := func(src *memsrc.Source, suffix string) *ursus.LazyFrame {
		return ursus.Scan(src).Select(ursus.Col("k").Alias("k"+suffix),
			ursus.Col("v").Mod(int64(3)).Alias("m"+suffix), ursus.Col("v").Alias("v"+suffix))
	}
	join := func(coalesce bool) *ursus.LazyFrame {
		return side(left, "").Join(side(right, "2"),
			ursus.JoinHow(ursus.JoinFull), ursus.JoinCoalesce(coalesce),
			ursus.JoinLeftOn(ursus.Col("k"), ursus.Col("m")), ursus.JoinRightOn(ursus.Col("k2"), ursus.Col("m2")))
	}
	want, err := join(false).Select(
		ursus.Coalesce(ursus.Col("k"), ursus.Col("k2")).Alias("k"),
		ursus.Coalesce(ursus.Col("m"), ursus.Col("m2")).Alias("m"),
		ursus.Col("v"), ursus.Col("v2"),
	).Collect(t.Context(), ursus.WithBatchSize(256))
	if err != nil {
		t.Fatal(err)
	}
	var stats ursus.MemoryStats
	got, err := join(true).Collect(t.Context(), ursus.WithBatchSize(256),
		ursus.WithMemoryLimit(32<<10), ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&stats))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Spills < 2 {
		t.Fatalf("spilled %d times, so this proves nothing", stats.Spills)
	}
	ursustest.AssertFrameEqual(t, sortedByAll(t, got), sortedByAll(t, want))
}

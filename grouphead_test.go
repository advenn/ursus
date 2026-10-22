package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestGroupHeadTail is G1 of v0.6.7-scope.md (step 308): Polars' group_by().head(n)
// and tail(n), with maintain_order=True: the keys first, the groups in the order
// each first appears, each group's rows in the frame's order. Every answer is Polars
// 1.44.1's but where a comment says otherwise.
func TestGroupHeadTail(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	lf := ursus.Frame(i64s("v", 1, 2, 3, 4, 5, 6, 7), strs("k", "b", "a", "b", "a", "b", nil, "b"),
		i64s("w", 10, 20, 30, 40, 50, 60, 70))
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want *ursus.LazyFrame
	}{
		{"head 2", lf.GroupBy(c("k")).Head(2),
			ursus.Frame(strs("k", "b", "b", "a", "a", nil), i64s("v", 1, 3, 2, 4, 6), i64s("w", 10, 30, 20, 40, 60))},
		{"tail 2", lf.GroupBy(c("k")).Tail(2),
			ursus.Frame(strs("k", "b", "b", "a", "a", nil), i64s("v", 5, 7, 2, 4, 6), i64s("w", 50, 70, 20, 40, 60))},
		{"head past a group's rows", lf.GroupBy(c("k")).Head(10),
			ursus.Frame(strs("k", "b", "b", "b", "b", "a", "a", nil), i64s("v", 1, 3, 5, 7, 2, 4, 6),
				i64s("w", 10, 30, 50, 70, 20, 40, 60))},
		{"two keys", lf.WithColumns(c("v").Mod(2).Alias("j")).GroupBy(c("k"), c("j")).Head(1),
			ursus.Frame(strs("k", "b", "a", nil), i64s("j", 1, 0, 0), i64s("v", 1, 2, 6), i64s("w", 10, 20, 60))},
		// A key computed from v, named v, replaces it.
		{"a computed key", lf.GroupBy(c("v").Mod(3)).Head(1),
			ursus.Frame(i64s("v", 1, 2, 0), strs("k", "b", "a", "b"), i64s("w", 10, 20, 30))},
		{"a computed key aliased", lf.GroupBy(c("v").Mod(3).Alias("m")).Tail(1),
			ursus.Frame(i64s("m", 1, 2, 0), i64s("v", 7, 5, 6), strs("k", "b", "b", nil), i64s("w", 70, 50, 60))},
		{"no rows", lf.Head(0).GroupBy(c("k")).Head(2), ursus.Frame(strs("k"), i64s("v"), i64s("w"))},
		// Polars' head(0) is a row of nulls for each group, of how it is built.
		{"head 0", lf.GroupBy(c("k")).Head(0), ursus.Frame(strs("k"), i64s("v"), i64s("w"))},
		// No keys: the frame is one group.
		{"no keys", lf.GroupBy().Tail(2), lf.Tail(2)},
	} {
		got, err := tc.q.Collect(ctx)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		want, err := tc.want.Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(tc.name, func(t *testing.T) { ursustest.AssertFrameEqual(t, got, want) })
	}

	t.Run("refused", func(t *testing.T) {
		times := ursus.Frame(ursus.Values("t", []int64{1, 2, 3})).WithColumns(c("t").Cast(ursus.Date))
		for _, tc := range []struct {
			name string
			q    *ursus.LazyFrame
			want string
		}{
			// Polars: can't convert negative int to unsigned.
			{"a negative count", lf.GroupBy(c("k")).Head(-1), "head takes a count of rows, and -1 is negative"},
			{"a negative count's tail", lf.GroupBy(c("k")).Tail(-2), "tail takes a count of rows, and -2 is negative"},
			// Polars: 'DynamicGroupBy' object has no attribute 'head'.
			{"a dynamic group", times.GroupByDynamic(c("t"), ursus.DynamicOptions{Every: ursus.Every("2d")}).Head(1),
				"a dynamic or rolling group has no head"},
			{"an unknown key", lf.GroupBy(c("nope")).Head(1), `"nope"`},
		} {
			_, err := tc.q.Collect(ctx)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: got %v, want an error with %q", tc.name, err, tc.want)
			}
		}
	})

	t.Run("eager", func(t *testing.T) {
		df, err := lf.Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got, err := df.GroupBy(c("k")).Tail(1).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		want, err := lf.GroupBy(c("k")).Tail(1).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, want)
	})
}

// TestGroupHeadTailUnderAMemoryLimit (step 308): a group's head and tail are windows
// over the keys, a filter and a sort, each spilling past a limit, and the order holds
// there: 20,000 rows of 300 groups, in batches of 64, under 64 KiB, answer as without
// a limit.
func TestGroupHeadTailUnderAMemoryLimit(t *testing.T) {
	ctx := t.Context()
	const n = 20_000
	ks, vs := make([]int64, n), make([]int64, n)
	for i := range n {
		ks[i], vs[i] = int64((i*7)%300), int64(i)
	}
	lf := ursus.Frame(ursus.Values("k", ks), ursus.Values("v", vs))
	for name, q := range map[string]*ursus.LazyFrame{
		"head": lf.GroupBy(ursus.Col("k")).Head(3),
		"tail": lf.GroupBy(ursus.Col("k")).Tail(3),
	} {
		want, err := q.Collect(ctx, ursus.WithBatchSize(64))
		if err != nil {
			t.Fatal(err)
		}
		var st ursus.MemoryStats
		got, err := q.Collect(ctx, ursus.WithBatchSize(64), ursus.WithMemoryLimit(64<<10), ursus.WithSpillDir(t.TempDir()),
			ursus.WithMemoryStats(&st))
		if err != nil {
			t.Fatalf("%s under 64 KiB: %v", name, err)
		}
		if st.Spills == 0 {
			t.Errorf("%s answered without spilling, so it proves nothing", name)
		}
		ursustest.AssertFrameEqual(t, got, want)
		if got.Height() != 900 {
			t.Errorf("%s: %d rows, want 900", name, got.Height())
		}
	}
}

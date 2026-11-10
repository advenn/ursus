package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestUpdateAsPolars is J2 of v0.6.7-scope.md (step 316): Polars' DataFrame.update,
// other's values over the frame's in the columns both have. The rows are the update
// group's, Polars 2.1.0's (oracle_update_test.go); here stay the refusals and the
// eager form.
func TestUpdateAsPolars(t *testing.T) {
	ctx := t.Context()
	df := ursus.Frame(i64s("A", 1, 2, 3, 4), i64s("B", 400, 500, 600, 700))
	byKey := ursus.Frame(i64s("A", 3, 1, 5), i64s("B", -66, nil, -99))
	on := []string{"A"}

	t.Run("refused", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			q    *ursus.LazyFrame
			want string
		}{
			{"an unknown key", df.Update(byKey, ursus.UpdateOptions{On: []string{"nope"}}), `"nope"`},
			{"On beside LeftOn", df.Update(byKey, ursus.UpdateOptions{On: on, LeftOn: on, RightOn: on}),
				"On names the keys of both frames, so it cannot be given with LeftOn or RightOn"},
			{"keys unpaired", df.Update(byKey, ursus.UpdateOptions{LeftOn: on}), "LeftOn names 1 keys and RightOn 0"},
			{"a kind out of range", df.Update(byKey, ursus.UpdateOptions{On: on, How: 7}), "the update kind 7 is not one of the three"},
			{"no type holds both", ursus.Frame(i64s("A", 1), ursus.Values("B", []bool{true})).Update(
				ursus.Frame(i64s("A", 1)).WithColumns(ursus.ConcatList(ursus.Col("A")).Alias("B")), ursus.UpdateOptions{On: on}),
				"no type holds both"},
		} {
			_, err := tc.q.Collect(ctx)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: got %v, want an error with %q", tc.name, err, tc.want)
			}
		}
	})

	t.Run("eager", func(t *testing.T) {
		a, err := df.Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		b, err := byKey.Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got, err := a.Update(ctx, b, ursus.UpdateOptions{On: on})
		if err != nil {
			t.Fatal(err)
		}
		want, err := df.Update(byKey, ursus.UpdateOptions{On: on}).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, want)
	})
}

// TestUpdateUnderAMemoryLimit (step 316): an update is an ordered join and a column
// each, so it spills and keeps the frame's order: 20,000 rows updated from 5,000,
// in batches of 64, under 64 KiB, answer as without a limit.
func TestUpdateUnderAMemoryLimit(t *testing.T) {
	ctx := t.Context()
	const n, m = 20_000, 5_000
	ks, vs := make([]int64, n), make([]int64, n)
	for i := range n {
		ks[i], vs[i] = int64((i*7)%n), int64(i)
	}
	uk, uv := make([]int64, m), make([]int64, m)
	for i := range m {
		uk[i], uv[i] = int64((i*13)%n), int64(-i)
	}
	q := ursus.Frame(ursus.Values("k", ks), ursus.Values("v", vs)).
		Update(ursus.Frame(ursus.Values("k", uk), ursus.Values("v", uv)), ursus.UpdateOptions{On: []string{"k"}})
	want, err := q.Collect(ctx, ursus.WithBatchSize(64), ursus.WithMemoryLimit(0))
	if err != nil {
		t.Fatal(err)
	}
	var st ursus.MemoryStats
	got, err := q.Collect(ctx, ursus.WithBatchSize(64), ursus.WithMemoryLimit(64<<10), ursus.WithSpillDir(t.TempDir()),
		ursus.WithMemoryStats(&st))
	if err != nil {
		t.Fatal(err)
	}
	if st.Spills == 0 {
		t.Error("the update answered without spilling, so it proves nothing")
	}
	ursustest.AssertFrameEqual(t, got, want)
}

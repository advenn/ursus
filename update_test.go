package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestUpdateAsPolars is J2 of v0.6.7-scope.md (step 316): Polars' DataFrame.update,
// other's values over the frame's in the columns both have. Every answer is Polars
// 1.44.1's.
func TestUpdateAsPolars(t *testing.T) {
	ctx := t.Context()
	df := ursus.Frame(i64s("A", 1, 2, 3, 4), i64s("B", 400, 500, 600, 700))
	byPlace := ursus.Frame(i64s("B", -66, nil, -99), i64s("C", 5, 3, 1))
	byKey := ursus.Frame(i64s("A", 3, 1, 5), i64s("B", -66, nil, -99))
	on := []string{"A"}
	frame := ursus.Frame
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want *ursus.LazyFrame
	}{
		// C is not the frame's, so it is left out; a null leaves 500.
		{"by place", df.Update(byPlace, ursus.UpdateOptions{}), frame(i64s("A", 1, 2, 3, 4), i64s("B", -66, 500, -99, 700))},
		{"by place, nulls", df.Update(byPlace, ursus.UpdateOptions{IncludeNulls: true}),
			frame(i64s("A", 1, 2, 3, 4), i64s("B", -66, nil, -99, 700))},
		{"on A", df.Update(byKey, ursus.UpdateOptions{On: on}), frame(i64s("A", 1, 2, 3, 4), i64s("B", 400, 500, -66, 700))},
		{"on A, inner", df.Update(byKey, ursus.UpdateOptions{On: on, How: ursus.UpdateInner}),
			frame(i64s("A", 1, 3), i64s("B", 400, -66))},
		{"on A, full", df.Update(byKey, ursus.UpdateOptions{On: on, How: ursus.UpdateFull}),
			frame(i64s("A", 1, 2, 3, 4, 5), i64s("B", 400, 500, -66, 700, -99))},
		{"on A, full, nulls", df.Update(byKey, ursus.UpdateOptions{On: on, How: ursus.UpdateFull, IncludeNulls: true}),
			frame(i64s("A", 1, 2, 3, 4, 5), i64s("B", nil, 500, -66, 700, -99))},
		{"on A, full, the right order", df.Update(byKey, ursus.UpdateOptions{On: on, How: ursus.UpdateFull,
			MaintainOrder: ursus.JoinOrderRight}), frame(i64s("A", 3, 1, 5, 2, 4), i64s("B", -66, 400, -99, 500, 700))},
		// A key other holds twice gives its row twice, as a join does.
		{"a key twice in other", df.Update(frame(i64s("A", 1, 1), i64s("B", 7, 8)), ursus.UpdateOptions{On: on}),
			frame(i64s("A", 1, 1, 2, 3, 4), i64s("B", 7, 8, 500, 600, 700))},
		{"a key twice in the frame", frame(i64s("A", 1, 1, 2), i64s("B", 1, 2, 3)).Update(frame(i64s("A", 1), i64s("B", 9)),
			ursus.UpdateOptions{On: on}), frame(i64s("A", 1, 1, 2), i64s("B", 9, 9, 3))},
		// The type that holds both, Polars' supertype.
		{"a float over an integer", df.Update(frame(i64s("A", 1), f64s("B", 1.5)), ursus.UpdateOptions{On: on}),
			frame(i64s("A", 1, 2, 3, 4), f64s("B", 1.5, 500.0, 600.0, 700.0))},
		{"text over an integer", df.Update(frame(i64s("A", 1), strs("B", "x")), ursus.UpdateOptions{On: on}),
			frame(i64s("A", 1, 2, 3, 4), strs("B", "x", "500", "600", "700"))},
		{"keys named apart", df.Update(frame(i64s("AA", 2), i64s("B", 0)), ursus.UpdateOptions{LeftOn: on, RightOn: []string{"AA"}}),
			frame(i64s("A", 1, 2, 3, 4), i64s("B", 400, 0, 600, 700))},
		{"no column in common", df.Update(frame(i64s("A", 2), i64s("Z", 0)), ursus.UpdateOptions{On: on}), df},
		// A key keeps the frame's type, but where other's keys are new rows.
		{"an Int32 key, left", frame(i64s("A", 1, 2), i64s("B", 10, 10)).WithColumns(ursus.Col("A").Cast(ursus.Int32)).Update(
			frame(i64s("A", 2, 3), i64s("B", 20, 30)), ursus.UpdateOptions{On: on}),
			frame(i64s("A", 1, 2), i64s("B", 10, 20)).WithColumns(ursus.Col("A").Cast(ursus.Int32))},
		{"an Int32 key, full", frame(i64s("A", 1, 2), i64s("B", 10, 10)).WithColumns(ursus.Col("A").Cast(ursus.Int32)).Update(
			frame(i64s("A", 2, 3), i64s("B", 20, 30)), ursus.UpdateOptions{On: on, How: ursus.UpdateFull}),
			frame(i64s("A", 1, 2, 3), i64s("B", 10, 20, 30))},
		// Other shorter: the rows past it are as they were.
		{"by place, other shorter", df.Update(frame(i64s("B", 1, 2)), ursus.UpdateOptions{}),
			frame(i64s("A", 1, 2, 3, 4), i64s("B", 1, 2, 600, 700))},
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

package ursus_test

import (
	"math"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestPartitionBy is P3 of v0.6.7-scope.md (step 305): Polars' partition_by, a part
// for each distinct key in the order each first appears, a null key among them, each
// part's rows in the frame's order, with or without the key columns. Every answer is
// Polars 1.44.1's; its as_dict's key tuples are each part's Key here.
func TestPartitionBy(t *testing.T) {
	ctx := t.Context()
	df, err := ursus.Frame(
		strs("k", "b", "a", "b", nil, "a"),
		i64s("j", 1, 1, 2, 1, 1),
		i64s("v", 1, 2, 3, 4, 5),
	).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	frame := func(cols ...*ursus.Column) *ursus.DataFrame {
		t.Helper()
		f, err := ursus.Frame(cols...).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	check := func(name string, got []ursus.Partition, err error, keys, frames []*ursus.DataFrame) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(got) != len(frames) {
			t.Fatalf("%s: %d parts, want %d", name, len(got), len(frames))
		}
		for i, p := range got {
			t.Run(name, func(t *testing.T) {
				ursustest.AssertFrameEqual(t, p.Key, keys[i])
				ursustest.AssertFrameEqual(t, p.Frame, frames[i])
			})
		}
	}

	got, err := df.PartitionBy(ctx, ursus.PartitionOptions{By: []string{"k"}})
	check("by k", got, err,
		[]*ursus.DataFrame{frame(strs("k", "b")), frame(strs("k", "a")), frame(strs("k", nil))},
		[]*ursus.DataFrame{
			frame(strs("k", "b", "b"), i64s("j", 1, 2), i64s("v", 1, 3)),
			frame(strs("k", "a", "a"), i64s("j", 1, 1), i64s("v", 2, 5)),
			frame(strs("k", nil), i64s("j", 1), i64s("v", 4)),
		})
	if k, ok, err := got[0].Key.At[string](0, "k"); err != nil || !ok || k != "b" {
		t.Errorf("the first key: %q, %v, %v", k, ok, err)
	}
	if _, ok, err := got[2].Key.At[string](0, "k"); err != nil || ok {
		t.Errorf("the null key: %v, %v", ok, err)
	}

	got, err = df.PartitionBy(ctx, ursus.PartitionOptions{By: []string{"k", "j"}})
	check("by k and j", got, err,
		[]*ursus.DataFrame{frame(strs("k", "b"), i64s("j", 1)), frame(strs("k", "a"), i64s("j", 1)),
			frame(strs("k", "b"), i64s("j", 2)), frame(strs("k", nil), i64s("j", 1))},
		[]*ursus.DataFrame{
			frame(strs("k", "b"), i64s("j", 1), i64s("v", 1)),
			frame(strs("k", "a", "a"), i64s("j", 1, 1), i64s("v", 2, 5)),
			frame(strs("k", "b"), i64s("j", 2), i64s("v", 3)),
			frame(strs("k", nil), i64s("j", 1), i64s("v", 4)),
		})

	// Polars: include_key=False. The Key holds them still.
	got, err = df.PartitionBy(ctx, ursus.PartitionOptions{By: []string{"k"}, DropKey: true})
	check("without the key", got, err,
		[]*ursus.DataFrame{frame(strs("k", "b")), frame(strs("k", "a")), frame(strs("k", nil))},
		[]*ursus.DataFrame{
			frame(i64s("j", 1, 2), i64s("v", 1, 3)),
			frame(i64s("j", 1, 1), i64s("v", 2, 5)),
			frame(i64s("j", 1), i64s("v", 4)),
		})

	// Every column a key and dropped: parts of no columns, each its rows' height,
	// as Polars' are.
	got, err = df.PartitionBy(ctx, ursus.PartitionOptions{By: []string{"k", "j", "v"}, DropKey: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || got[0].Frame.Width() != 0 || got[0].Frame.Height() != 1 {
		t.Errorf("every column a dropped key: %d parts, the first %d wide and %d high",
			len(got), got[0].Frame.Width(), got[0].Frame.Height())
	}

	// -0 is 0 and NaN NaN, as a group-by keys them; the Key is the value first seen.
	floats, err := ursus.Frame(f64s("f", 0.0, math.Copysign(0, -1), math.NaN(), math.NaN(), 1.0),
		i64s("v", 1, 2, 3, 4, 5)).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err = floats.PartitionBy(ctx, ursus.PartitionOptions{By: []string{"f"}})
	check("floats", got, err,
		[]*ursus.DataFrame{frame(f64s("f", 0.0)), frame(f64s("f", math.NaN())), frame(f64s("f", 1.0))},
		[]*ursus.DataFrame{
			frame(f64s("f", 0.0, math.Copysign(0, -1)), i64s("v", 1, 2)),
			frame(f64s("f", math.NaN(), math.NaN()), i64s("v", 3, 4)),
			frame(f64s("f", 1.0), i64s("v", 5)),
		})
	if f, _, err := got[0].Key.At[float64](0, "f"); err != nil || math.Signbit(f) {
		t.Errorf("the first key is %v, %v; want the 0 first seen, not the -0 after it", f, err)
	}

	// No rows, no parts.
	empty, err := df.Head(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := empty.PartitionBy(ctx, ursus.PartitionOptions{By: []string{"k"}}); err != nil || len(got) != 0 {
		t.Errorf("no rows: %d parts, %v", len(got), err)
	}

	t.Run("refused", func(t *testing.T) {
		lists, err := ursus.Frame(strs("s", "a,b", "a")).WithColumns(ursus.Col("s").Str().Split(",").Alias("l")).
			Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name string
			df   *ursus.DataFrame
			o    ursus.PartitionOptions
			want string
		}{
			// Polars: at least one key is required in a group_by operation.
			{"no By", df, ursus.PartitionOptions{}, "PartitionBy needs a column to partition by"},
			// Polars: "nope" not found.
			{"an unknown column", df, ursus.PartitionOptions{By: []string{"nope"}}, `"nope"`},
			{"a column twice", df, ursus.PartitionOptions{By: []string{"k", "k"}}, `"k" is in By twice`},
			// Polars partitions by a List; a List is no key here (feature-gaps.md §3).
			{"a List key", lists, ursus.PartitionOptions{By: []string{"l"}}, "List(String)"},
		} {
			_, err := tc.df.PartitionBy(ctx, tc.o)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: got %v, want an error with %q", tc.name, err, tc.want)
			}
		}
	})
}

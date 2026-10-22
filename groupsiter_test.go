package ursus_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestGroupsAsFrames is tier 2 of v0.6.7-scope.md (step 321): Polars' iteration over
// a group-by and map_groups, and a frame's extend, with_columns_seq, estimated_size and
// glimpse. Every answer is Polars 1.44.1's.
func TestGroupsAsFrames(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	lf := ursus.Frame(strs("k", "a", "b", "a"), i64s("v", 1, 2, 3))
	g := lf.GroupBy(c("k")).MaintainOrder()

	var keys []string
	var heights []int
	for p, err := range g.Groups(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		k, _, err := p.Key.At[string](0, "k")
		if err != nil {
			t.Fatal(err)
		}
		keys, heights = append(keys, k), append(heights, p.Frame.Height())
	}
	if strings.Join(keys, ",") != "a,b" || heights[0] != 2 || heights[1] != 1 {
		t.Errorf("the groups are %v of %v rows, want a and b of 2 and 1", keys, heights)
	}

	// A break ends the walk: Go panics at a yield after one.
	seen := 0
	for _, err := range g.Groups(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		seen++
		break
	}
	if seen != 1 {
		t.Errorf("a break after the first group saw %d", seen)
	}

	got, err := g.MapGroups(ctx, func(df *ursus.DataFrame) (*ursus.DataFrame, error) {
		return df.Lazy().WithColumns(c("v").Sum().Cast(ursus.Int64).Alias("s")).Collect(ctx)
	})
	if err != nil {
		t.Fatal(err)
	}
	want, err := ursus.Frame(strs("k", "a", "a", "b"), i64s("v", 1, 3, 2), i64s("s", 4, 4, 2)).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, want)

	// A key computed from a column, named as it.
	var mods []int64
	for p, err := range lf.GroupBy(c("v").Mod(2).Alias("odd")).MaintainOrder().Groups(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		m, _, _ := p.Key.At[int64](0, "odd")
		mods = append(mods, m)
	}
	if len(mods) != 2 || mods[0] != 1 || mods[1] != 0 {
		t.Errorf("the computed keys are %v", mods)
	}

	// A computed key is the group's key and not its rows': each frame holds the rows
	// as they are, as Polars hands them out, though the key is named v as well.
	var rows [][]int64
	var parity []int64
	for p, err := range lf.GroupBy(c("v").Mod(2)).MaintainOrder().Groups(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(p.Frame.Columns(), ","); got != "k,v" {
			t.Errorf("a group's columns are %s, want the frame's k,v", got)
		}
		v, err := p.Frame.Column[int64]("v")
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, v.Values())
		m, _, err := p.Key.At[int64](0, "v")
		if err != nil {
			t.Fatal(err)
		}
		parity = append(parity, m)
	}
	if fmt.Sprint(rows) != "[[1 3] [2]]" || fmt.Sprint(parity) != "[1 0]" {
		t.Errorf("by v mod 2, the groups' rows are %v and keys %v, want [[1 3] [2]] and [1 0]", rows, parity)
	}

	t.Run("refused", func(t *testing.T) {
		days := ursus.Frame(i64s("t", 1, 2)).WithColumns(c("t").Cast(ursus.Date))
		for _, err := range days.GroupByDynamic(c("t"), ursus.DynamicOptions{Every: ursus.Every("1d")}).Groups(ctx) {
			if err == nil || !strings.Contains(err.Error(), "windows overlap") {
				t.Errorf("a dynamic group: %v", err)
			}
		}
		_, err := g.MapGroups(ctx, func(df *ursus.DataFrame) (*ursus.DataFrame, error) {
			if df.Height() == 1 {
				return df.Lazy().Select(c("k")).Collect(ctx)
			}
			return df, nil
		})
		if err == nil {
			t.Error("answers of two shapes were stacked")
		}
		none := ursus.Frame(strs("k"), i64s("v")).GroupBy(c("k"))
		if _, err := none.MapGroups(ctx, func(df *ursus.DataFrame) (*ursus.DataFrame, error) { return df, nil }); err == nil ||
			!strings.Contains(err.Error(), "no groups") {
			t.Errorf("no groups, as Polars refuses an empty frame: %v", err)
		}
	})

	t.Run("extend, seq, size, glimpse", func(t *testing.T) {
		a, err := ursus.Frame(i64s("x", 1, 2)).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		b, err := ursus.Frame(i64s("x", 3)).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ext, err := a.Extend(ctx, b)
		if err != nil {
			t.Fatal(err)
		}
		want, err := ursus.Frame(i64s("x", 1, 2, 3)).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, ext, want)
		seq, err := a.Lazy().WithColumnsSeq(c("x").Mul(2)).SelectSeq(c("x")).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if v, _, _ := seq.At[int64](1, "x"); v != 4 {
			t.Errorf("with_columns_seq: %d", v)
		}
		if n := ext.EstimatedSize(); n < 24 {
			t.Errorf("three Int64 values in %d bytes", n)
		}
		gl, err := ursus.Frame(f64s("price", 9.5, 3.0, nil), strs("sku", "a", "b", "c")).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		wantGlimpse := "Rows: 3\nColumns: 2\n$ price <Float64> 9.5, 3, null\n$ sku    <String> \"a\", \"b\", \"c\"\n"
		if got := gl.Glimpse(); got != wantGlimpse {
			t.Errorf("glimpse:\n%s\nwant:\n%s", got, wantGlimpse)
		}
		// Ten values at most, with no mark for more, and a name past 50 characters cut
		// to 49 and an ellipsis, as Polars' glimpse.
		long := strings.Repeat("n", 51)
		many := make([]int64, 12)
		gl, err = ursus.Frame(ursus.Values(long, many)).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		wantGlimpse = "Rows: 12\nColumns: 1\n$ " + strings.Repeat("n", 49) + "… <Int64> 0, 0, 0, 0, 0, 0, 0, 0, 0, 0\n"
		if got := gl.Glimpse(); got != wantGlimpse {
			t.Errorf("glimpse:\n%s\nwant:\n%s", got, wantGlimpse)
		}
	})
}

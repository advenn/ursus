package ursus_test

import (
	"math"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestFirstLastNonNullAndAll is G3 of v0.6.7-scope.md (step 310): Polars'
// first(ignore_nulls=True) and last(ignore_nulls=True), of an expression and a
// group-by, and group_by().all(), each column a List a group. Every answer is
// Polars 1.44.1's, with maintain_order=True.
func TestFirstLastNonNullAndAll(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	nan := math.NaN()
	lf := ursus.Frame(strs("k", "a", "a", "b", "b", "c", "a"), i64s("v", nil, 2, nil, nil, 5, 6),
		f64s("f", nan, nil, 1.0, nil, nil, 3.0), strs("s", nil, "x", "y", nil, nil, nil))
	g := func() *ursus.GroupBy { return lf.GroupBy(c("k")).MaintainOrder() }
	k := strs("k", "a", "b", "c")
	frame := ursus.Frame
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want *ursus.LazyFrame
	}{
		// NaN is a value; b has no v, c no f and no s.
		{"FirstNonNull", g().FirstNonNull(), frame(k, i64s("v", 2, nil, 5), f64s("f", nan, 1.0, nil), strs("s", "x", "y", nil))},
		{"LastNonNull", g().LastNonNull(), frame(k, i64s("v", 6, nil, 5), f64s("f", 3.0, 1.0, nil), strs("s", "x", "y", nil))},
		// Polars without ignore_nulls, unchanged.
		{"First", g().First(), frame(k, i64s("v", nil, nil, 5), f64s("f", nan, 1.0, nil), strs("s", nil, "y", nil))},
		{"in Agg", g().Agg(c("v").FirstNonNull().Alias("fv"), c("s").LastNonNull().Alias("ls")),
			frame(k, i64s("fv", 2, nil, 5), strs("ls", "x", "y", nil))},
		{"in a Select", lf.Select(c("v").FirstNonNull(), c("s").LastNonNull(), c("f").FirstNonNull()),
			frame(i64s("v", 2), strs("s", "y"), f64s("f", nan))},
		{"over a window", lf.Select(c("k"), c("v").FirstNonNull().Over(c("k")).Alias("fv"),
			c("v").LastNonNull().Over(c("k")).Alias("lv")),
			frame(strs("k", "a", "a", "b", "b", "c", "a"), i64s("fv", 2, 2, nil, nil, 5, 2), i64s("lv", 6, 6, nil, nil, 5, 6))},
		{"a column of nulls", frame(i64s("v", nil, nil)).Select(c("v").FirstNonNull()), frame(i64s("v", nil))},
		{"no rows", frame(i64s("v")).Select(c("v").LastNonNull()), frame(i64s("v", nil))},
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

	// The values a group in their order, nulls among them, as Lists; read back as
	// text, each list's elements.
	text := func(e ursus.Expr) ursus.Expr {
		return e.List().Eval(ursus.Element().Cast(ursus.String).FillNullWith("null")).List().Join(",")
	}
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want *ursus.LazyFrame
	}{
		{"All", g().All().Select(c("k"), text(c("v")), text(c("s"))),
			frame(k, ursus.Values("v", []string{"null,2,6", "null,null", "5"}), ursus.Values("s", []string{"null,x,null", "y,null", "null"}))},
		// A key computed from v, named v: v is not among the lists.
		{"All by a computed key", lf.GroupBy(c("k"), c("v").IsNull()).MaintainOrder().All().Select(c("k"), c("v"), text(c("s"))),
			frame(strs("k", "a", "a", "b", "c"), ursus.Values("v", []bool{true, false, true, false}),
				ursus.Values("s", []string{"null", "x,null", "y,null", "null"}))},
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

	t.Run("a list column", func(t *testing.T) {
		// Polars: a null list is skipped, an empty one is a value.
		lists := frame(i64s("k", 1, 1, 1), strs("s", nil, "", "1")).
			Select(c("k"), c("s").Str().Split(",").Alias("l")).
			WithColumns(ursus.When(c("l").List().Get(0).Eq("")).Then(c("l").List().Head(0)).Otherwise(c("l")).Alias("l"))
		got, err := lists.GroupBy(c("k")).Agg(c("l").FirstNonNull().List().Len().Alias("n")).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n, ok, err := got.At[uint32](0, "n"); err != nil || !ok || n != 0 {
			t.Errorf("the first list not null is %d long, %v %v; want the empty one", n, ok, err)
		}
	})

	t.Run("a dynamic group", func(t *testing.T) {
		days := frame(ursus.Values("t", []time.Time{at(2024, 1, 1, 0), at(2024, 1, 2, 0), at(2024, 1, 3, 0)}),
			i64s("v", nil, 2, nil)).WithColumns(c("t").Cast(ursus.Date))
		got, err := days.GroupByDynamic(c("t"), ursus.DynamicOptions{Every: ursus.Every("3d")}).FirstNonNull().Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if v, ok, err := got.At[int64](0, "v"); err != nil || !ok || v != 2 {
			t.Errorf("the first value not null of the window is %d, %v, %v; want 2", v, ok, err)
		}
	})
}

// TestFirstNonNullUnderAMemoryLimit (step 310): a conditional First and Last spill in
// a group-by of many keys as their plain forms do: 20,000 rows of 4,000 keys, a null
// in every third, under 64 KiB, answer as without a limit. All is not here: it is
// Implode's, whose values do not spill, refused at 64 and 256 KiB alike.
func TestFirstNonNullUnderAMemoryLimit(t *testing.T) {
	ctx := t.Context()
	const n = 20_000
	ks, vs, ok := make([]int64, n), make([]int64, n), make([]bool, n)
	for i := range n {
		ks[i], vs[i], ok[i] = int64((i*7)%4000), int64(i), i%3 != 0
	}
	g := ursus.Frame(ursus.Values("k", ks), ursus.ValuesNullable("v", vs, ok)).GroupBy(ursus.Col("k")).MaintainOrder()
	for name, q := range map[string]*ursus.LazyFrame{
		"FirstNonNull": g.FirstNonNull(),
		"LastNonNull":  g.LastNonNull(),
	} {
		limit := int64(64 << 10)
		want, err := q.Collect(ctx, ursus.WithBatchSize(64))
		if err != nil {
			t.Fatal(err)
		}
		var st ursus.MemoryStats
		got, err := q.Collect(ctx, ursus.WithBatchSize(64), ursus.WithMemoryLimit(limit), ursus.WithSpillDir(t.TempDir()),
			ursus.WithMemoryStats(&st))
		if err != nil {
			t.Fatalf("%s under %d KiB: %v", name, limit>>10, err)
		}
		if st.Spills == 0 {
			t.Errorf("%s answered without spilling, so it proves nothing", name)
		}
		ursustest.AssertFrameEqual(t, got, want)
	}
}

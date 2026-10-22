package ursus_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/ursustest"
)

func ab() (*ursus.LazyFrame, *ursus.LazyFrame) {
	a := ursus.Frame(
		ursus.Values("k", []int64{1, 2}),
		ursus.Values("v", []string{"a", "b"}),
	)
	b := ursus.Frame(
		ursus.Values("k", []int64{3}),
		ursus.Values("v", []string{"c"}),
	)
	return a, b
}

func TestConcatPreservesOrder(t *testing.T) {
	a, b := ab()
	for _, bs := range []int{1, 2, 8192} {
		df, err := ursus.Concat([]*ursus.LazyFrame{a, b, a}).
			Collect(t.Context(), ursus.WithBatchSize(bs), ursus.WithVerify())
		if err != nil {
			t.Fatalf("bs=%d: %v", bs, err)
		}
		k, err := df.Column[int64]("k")
		if err != nil {
			t.Fatal(err)
		}
		got := make([]int64, df.Height())
		for i := range df.Height() {
			got[i], _ = k.Get(i)
		}
		// Every row of the first frame before every row of the second, and a frame
		// used twice contributes twice.
		if !slices.Equal(got, []int64{1, 2, 3, 1, 2}) {
			t.Errorf("bs=%d order = %v, want [1 2 3 1 2]", bs, got)
		}
	}
}

// TestConcatRejectsMismatchedSchemas: kernel.Concat pairs columns POSITIONALLY and
// never checks the schema it is handed, deriving every name and type from batch 0.
// So the plan layer is the only thing between a type error and a spliced column.
func TestConcatRejectsMismatchedSchemas(t *testing.T) {
	a, _ := ab()
	cases := []struct {
		name  string
		other *ursus.LazyFrame
		want  string
	}{
		{"different column count",
			ursus.Frame(ursus.Values("k", []int64{9})),
			"has 1 columns"},
		{"different names",
			ursus.Frame(ursus.Values("k", []int64{9}), ursus.Values("w", []string{"z"})),
			`has "w" at position 1`},
		{"same names in a different order",
			ursus.Frame(ursus.Values("v", []string{"z"}), ursus.Values("k", []int64{9})),
			"at position 0"},
		{"types with no common type",
			ursus.Frame(ursus.Values("k", []int64{9}), ursus.Values("v", []bool{true})),
			"no common type"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ursus.Concat([]*ursus.LazyFrame{a, c.other}).Collect(t.Context())
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error should explain %q:\n%v", c.want, err)
			}
		})
	}
}

// TestConcatWidensNullability is the reason strict is not Schema.Equal.
//
// Field compares by struct equality including Nullable, which is a static property
// of the schema rather than a count of nulls actually present. Two frames differing
// only in that flag must stack — it is the ordinary result of filtering one of them.
func TestConcatWidensNullability(t *testing.T) {
	nonNull := ursus.Frame(ursus.Values("v", []int64{1, 2}))
	nullable := ursus.Frame(
		ursus.ValuesNullable("v", []int64{0, 3}, []bool{false, true}),
	)
	sA, err := nonNull.CollectSchema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sB, err := nullable.CollectSchema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if sA.Equal(sB) {
		t.Fatal("the fixture is wrong: the two schemas should differ in nullability")
	}

	df, err := ursus.Concat([]*ursus.LazyFrame{nonNull, nullable}).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("frames differing only in nullability must stack: %v", err)
	}
	if df.Height() != 4 {
		t.Errorf("height = %d, want 4", df.Height())
	}
	if !df.Schema().Field(0).Nullable {
		t.Error("the result must be nullable: one input can contribute nulls")
	}
}

// TestConcatPromotesTypes: Int32 and Int64 stack as Int64, and the narrower side is
// cast rather than reinterpreted.
func TestConcatPromotesTypes(t *testing.T) {
	small := ursus.Frame(ursus.Values("v", []int32{1, 2}))
	big := ursus.Frame(ursus.Values("v", []int64{1 << 40}))

	df, err := ursus.Concat([]*ursus.LazyFrame{small, big}).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got := df.Schema().Field(0).Type; got != ursus.Int64 {
		t.Fatalf("type = %s, want Int64", got)
	}
	v, err := df.Column[int64]("v")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := v.Get(2); got != 1<<40 {
		t.Errorf("the wide value became %d — a reinterpret rather than a cast", got)
	}
}

// TestConcatDiagonalColumnOrder: the union of the columns, with nulls in the gaps.
//
// Column order is FIRST APPEARANCE across the inputs. Anything derived from map
// iteration would make the same query produce a different schema between runs,
// which is why this asserts the order rather than just the set — and why it runs
// repeatedly.
func TestConcatDiagonalColumnOrder(t *testing.T) {
	a := ursus.Frame(ursus.Values("k", []int64{1}), ursus.Values("v", []string{"a"}))
	b := ursus.Frame(ursus.Values("x", []float64{1.5}), ursus.Values("k", []int64{2}))

	want := []string{"k", "v", "x"}
	for range 20 {
		df, err := ursus.Concat([]*ursus.LazyFrame{a, b}, ursus.WithConcatMode(ursus.ConcatDiagonal)).
			Collect(t.Context(), ursus.WithVerify())
		if err != nil {
			t.Fatal(err)
		}
		if got := df.Schema().Names(); !slices.Equal(got, want) {
			t.Fatalf("column order = %v, want %v — first appearance across the "+
				"inputs, never map order", got, want)
		}
		if df.Height() != 2 {
			t.Fatalf("height = %d, want 2", df.Height())
		}
		// A column a frame does not have is null for that frame's rows.
		if _, ok, _ := df.At[float64](0, "x"); ok {
			t.Error("frame a has no x, so its row must be null there")
		}
		if _, ok, _ := df.At[string](1, "v"); ok {
			t.Error("frame b has no v, so its row must be null there")
		}
		// And a column both have is not null anywhere.
		for i := range 2 {
			if _, ok, _ := df.At[int64](i, "k"); !ok {
				t.Errorf("k is present in both frames; row %d must not be null", i)
			}
		}
	}
}

// TestConcatDiagonalWidensNullability: a column missing from ANY input is nullable
// in the result, whatever the frames that do have it declare.
func TestConcatDiagonalWidensNullability(t *testing.T) {
	a := ursus.Frame(ursus.Values("k", []int64{1}), ursus.Values("v", []string{"a"}))
	b := ursus.Frame(ursus.Values("k", []int64{2}))

	s, err := ursus.Concat([]*ursus.LazyFrame{a, b}, ursus.WithConcatMode(ursus.ConcatDiagonal)).
		CollectSchema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	f, _ := s.ByName("v")
	if !f.Nullable {
		t.Error("v is absent from frame b, so every row b contributes is null there")
	}
	k, _ := s.ByName("k")
	if k.Nullable {
		t.Error("k is present and non-null in both frames")
	}
}

// TestConcatPushdown: both optimizer rules handle an n-ary node through their
// generic paths, and reconciliation is a Project beneath the union — so a predicate
// still reaches each scan and each scan still reads only what it needs.
func TestConcatPushdown(t *testing.T) {
	a, b := ab()
	plan, err := ursus.Concat([]*ursus.LazyFrame{a, b}).
		Filter(ursus.Col("k").Gt(int64(1))).
		Select(ursus.Col("k")).
		Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(plan, "projection: [k]") != 2 {
		t.Errorf("each input should read only k:\n%s", plan)
	}
}

func TestConcatSingleFrameIsANoOp(t *testing.T) {
	a, _ := ab()
	df, err := ursus.Concat([]*ursus.LazyFrame{a}).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 2 {
		t.Errorf("height = %d, want 2", df.Height())
	}
}

func TestHStack(t *testing.T) {
	a := ursus.Frame(ursus.Values("k", []int64{1, 2}))
	b := ursus.Frame(ursus.Values("v", []string{"x", "y"}))

	for _, bs := range []int{1, 2, 8192} {
		df, err := a.HStack(b).Collect(t.Context(), ursus.WithBatchSize(bs), ursus.WithVerify())
		if err != nil {
			t.Fatalf("bs=%d: %v", bs, err)
		}
		if got := df.Schema().Names(); !slices.Equal(got, []string{"k", "v"}) {
			t.Errorf("bs=%d columns = %v", bs, got)
		}
		if df.Height() != 2 {
			t.Errorf("bs=%d height = %d, want 2", bs, df.Height())
		}
	}

	// Heights must agree — hstack pairs rows positionally.
	_, err := a.HStack(ursus.Frame(ursus.Values("v", []string{"only one"}))).
		Collect(t.Context())
	if err == nil {
		t.Fatal("frames of different heights must be refused")
	}
	if !errors.Is(err, uerr.ErrValue) || !strings.Contains(err.Error(), "rows") {
		t.Errorf("error should name the height mismatch: %v", err)
	}

	// Names must not collide. There is no automatic suffixing: a join has a
	// principled left and right, and these inputs are peers.
	_, err = a.HStack(ursus.Frame(ursus.Values("k", []int64{9, 9}))).Collect(t.Context())
	if err == nil {
		t.Fatal("colliding column names must be refused")
	}
	if errors.Is(err, uerr.ErrInternal) {
		t.Errorf("a user mistake reported as an ursus bug: %v", err)
	}
	if !strings.Contains(err.Error(), "appears in both") {
		t.Errorf("error should name the collision: %v", err)
	}
}

// TestGoldenConcatPlan pins the adaptation, which is the part worth seeing.
//
// The frames have different columns, so resolveUnion inserts a projection above
// each that supplies the missing one as a typed null and casts the narrower type.
// That is why a column is null, visible in the plan rather than buried in an
// operator — and it is what lets projection pushdown narrow each input separately.
func TestGoldenConcatPlan(t *testing.T) {
	a := ursus.Frame(ursus.Values("k", []int32{1}), ursus.Values("v", []string{"a"}))
	b := ursus.Frame(ursus.Values("k", []int64{2}), ursus.Values("x", []float64{1.5}))
	lf := ursus.Concat([]*ursus.LazyFrame{a, b}, ursus.WithConcatMode(ursus.ConcatDiagonal))
	ursustest.AssertPlan(t, lf, "testdata/plans/concat.txt")
}

// TestConcatModes is P5 of v0.6.7-scope.md (step 307): Polars' concat modes past
// vertical and diagonal. The relaxed ones meet a column's types at Polars' supertype;
// horizontal_extend pads a shorter frame; the align ones join on the columns every
// frame has and sort by them. Every answer is Polars 1.44.1's but where a comment
// says otherwise.
func TestConcatModes(t *testing.T) {
	ctx := t.Context()
	mode := ursus.WithConcatMode
	concat := func(m ursus.ConcatMode, fs ...*ursus.LazyFrame) *ursus.LazyFrame { return ursus.Concat(fs, mode(m)) }
	frame := ursus.Frame
	ab := func(a *ursus.Column, b ...string) *ursus.LazyFrame { return frame(a, ursus.Values("b", b)) }
	at := func(y, m, d, h int) time.Time { return time.Date(y, time.Month(m), d, h, 0, 0, 0, time.UTC) }
	ids := frame(ursus.Values("id", []int64{3, 1, 2}), ursus.Values("x", []int64{30, 10, 20}))
	ys := frame(ursus.Values("id", []int64{2, 4, 1}), ursus.Values("y", []string{"b", "d", "a"}))
	zs := frame(ursus.Values("id", []int64{1, 5}), ursus.Values("z", []float64{1.5, 5.5}))
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want *ursus.LazyFrame
	}{
		{"relaxed: an Int64 beside a Float64", concat(ursus.ConcatRelaxed, ab(ursus.Values("a", []int64{3}), "z"),
			ab(ursus.Values("a", []float64{1.5}), "w")), ab(ursus.Values("a", []float64{3, 1.5}), "z", "w")},
		{"relaxed: a number beside text", concat(ursus.ConcatRelaxed, ab(ursus.Values("a", []int64{3}), "z"),
			ab(ursus.Values("a", []string{"t"}), "v")), ab(ursus.Values("a", []string{"3", "t"}), "z", "v")},
		{"relaxed: a Boolean beside a number", concat(ursus.ConcatRelaxed, ab(ursus.Values("a", []int64{3}), "z"),
			ab(ursus.Values("a", []bool{true}), "q")), ab(ursus.Values("a", []int64{3, 1}), "z", "q")},
		{"relaxed: a Date beside a Datetime", concat(ursus.ConcatRelaxed,
			frame(ursus.Values("a", []time.Time{at(2024, 1, 2, 0)})).WithColumns(ursus.Col("a").Cast(ursus.Date)),
			frame(ursus.Values("a", []time.Time{at(2024, 1, 3, 4)})).WithColumns(ursus.Col("a").Cast(ursus.Datetime(ursus.Micro, "")))),
			frame(ursus.Values("a", []time.Time{at(2024, 1, 2, 0), at(2024, 1, 3, 4)})).
				WithColumns(ursus.Col("a").Cast(ursus.Datetime(ursus.Micro, "")))},
		{"relaxed: an Int32 beside an Int64", concat(ursus.ConcatRelaxed, ab(ursus.Values("a", []int32{1, 2}), "x", "y"),
			ab(ursus.Values("a", []int64{3}), "z")), ab(ursus.Values("a", []int64{1, 2, 3}), "x", "y", "z")},
		{"diagonal relaxed", concat(ursus.ConcatDiagonalRelaxed, ab(ursus.Values("a", []int64{1}), "x"),
			frame(ursus.Values("a", []float64{1.5}), ursus.Values("c", []float64{3.5}))),
			frame(ursus.Values("a", []float64{1, 1.5}), ursus.ValuesNullable("b", []string{"x", ""}, []bool{true, false}),
				ursus.ValuesNullable("c", []float64{0, 3.5}, []bool{false, true}))},
		{"horizontal extend", concat(ursus.ConcatHorizontalExtend, frame(ursus.Values("a", []int64{1, 2, 3})),
			frame(ursus.Values("b", []string{"x", "y"}))),
			frame(ursus.Values("a", []int64{1, 2, 3}), ursus.ValuesNullable("b", []string{"x", "y", ""}, []bool{true, true, false}))},
		{"horizontal extend of no rows", concat(ursus.ConcatHorizontalExtend, frame(ursus.Values("a", []int64{1, 2})),
			frame(ursus.Values("b", []string{}))),
			frame(ursus.Values("a", []int64{1, 2}), ursus.ValuesNullable("b", []string{"", ""}, []bool{false, false}))},
		{"horizontal", concat(ursus.ConcatHorizontal, frame(ursus.Values("a", []int64{1, 2})), frame(ursus.Values("b", []string{"x", "y"}))),
			frame(ursus.Values("a", []int64{1, 2}), ursus.Values("b", []string{"x", "y"}))},
		{"align", concat(ursus.ConcatAlign, ids, ys, zs), frame(ursus.Values("id", []int64{1, 2, 3, 4, 5}),
			ursus.ValuesNullable("x", []int64{10, 20, 30, 0, 0}, []bool{true, true, true, false, false}),
			ursus.ValuesNullable("y", []string{"a", "b", "", "d", ""}, []bool{true, true, false, true, false}),
			ursus.ValuesNullable("z", []float64{1.5, 0, 0, 0, 5.5}, []bool{true, false, false, false, true}))},
		{"align inner", concat(ursus.ConcatAlignInner, ids, ys, zs), frame(ursus.Values("id", []int64{1}),
			ursus.Values("x", []int64{10}), ursus.Values("y", []string{"a"}), ursus.Values("z", []float64{1.5}))},
		{"align left", concat(ursus.ConcatAlignLeft, ids, ys, zs), frame(ursus.Values("id", []int64{1, 2, 3}),
			ursus.Values("x", []int64{10, 20, 30}), ursus.ValuesNullable("y", []string{"a", "b", ""}, []bool{true, true, false}),
			ursus.ValuesNullable("z", []float64{1.5, 0, 0}, []bool{true, false, false}))},
		{"align right", concat(ursus.ConcatAlignRight, ids, ys, zs), frame(ursus.Values("id", []int64{1, 5}),
			ursus.ValuesNullable("x", []int64{10, 0}, []bool{true, false}), ursus.ValuesNullable("y", []string{"a", ""}, []bool{true, false}),
			ursus.Values("z", []float64{1.5, 5.5}))},
		// Two keys, the second frame's in another order; the first frame's order wins.
		{"align on two columns", concat(ursus.ConcatAlign,
			frame(ursus.Values("k1", []int64{1, 1, 2}), ursus.Values("k2", []string{"a", "b", "a"}), ursus.Values("v", []int64{1, 2, 3})),
			frame(ursus.Values("k2", []string{"a", "b"}), ursus.Values("k1", []int64{1, 1}), ursus.Values("w", []int64{10, 20}))),
			frame(ursus.Values("k1", []int64{1, 1, 2}), ursus.Values("k2", []string{"a", "b", "a"}), ursus.Values("v", []int64{1, 2, 3}),
				ursus.ValuesNullable("w", []int64{10, 20, 0}, []bool{true, true, false}))},
		// Null keys match nothing and sort first, Polars' order: the second frame's
		// row, which the first has none for, first.
		{"align null keys", concat(ursus.ConcatAlign,
			frame(ursus.ValuesNullable("id", []int64{1, 0}, []bool{true, false}), ursus.Values("x", []int64{1, 2})),
			frame(ursus.ValuesNullable("id", []int64{0, 1}, []bool{false, true}), ursus.Values("y", []int64{3, 4}))),
			frame(ursus.ValuesNullable("id", []int64{0, 0, 1}, []bool{false, false, true}),
				ursus.ValuesNullable("x", []int64{0, 2, 1}, []bool{false, true, true}), ursus.ValuesNullable("y", []int64{3, 0, 4}, []bool{true, false, true}))},
		{"align, a key twice", concat(ursus.ConcatAlign,
			frame(ursus.Values("id", []int64{1, 1}), ursus.Values("x", []int64{1, 2})),
			frame(ursus.Values("id", []int64{1}), ursus.Values("y", []int64{3}))),
			frame(ursus.Values("id", []int64{1, 1}), ursus.Values("x", []int64{1, 2}), ursus.Values("y", []int64{3, 3}))},
		{"align, keys of two widths", concat(ursus.ConcatAlign,
			frame(ursus.Values("id", []int32{1, 2}), ursus.Values("x", []int64{1, 1})),
			frame(ursus.Values("id", []int64{1, 2}), ursus.Values("y", []int64{3, 4}))),
			frame(ursus.Values("id", []int64{1, 2}), ursus.Values("x", []int64{1, 1}), ursus.Values("y", []int64{3, 4}))},
		// One frame is itself, unsorted, as Polars returns it.
		{"align of one frame", concat(ursus.ConcatAlign, ids), ids},
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
			// Polars: failed to determine supertype of list[i64] and i64.
			{"relaxed: a List beside a number", concat(ursus.ConcatRelaxed,
				frame(ursus.Values("s", []string{"a,b"})).Select(ursus.Col("s").Str().Split(",").Alias("a")),
				frame(ursus.Values("a", []string{"x"})).Select(ursus.Lit(int64(1)).Alias("a"))),
				`cannot stack column "a": no supertype for List(String) and Int64`},
			// Polars: schema names differ.
			{"relaxed: columns in another order", concat(ursus.ConcatRelaxed, ab(ursus.Values("a", []int64{1}), "x"),
				frame(ursus.Values("b", []string{"y"}), ursus.Values("a", []int64{2}))), "frame 1"},
			// Polars' horizontal pads, deprecated since 1.42.1 for this, which
			// horizontal_extend keeps.
			{"horizontal of two heights", concat(ursus.ConcatHorizontal, frame(ursus.Values("a", []int64{1, 2, 3})),
				frame(ursus.Values("b", []string{"x", "y"}))), "frame 1 has 2 rows, frame 0 has 3"},
			// Polars: 'align' strategy requires at least one common column.
			{"align of no common column", concat(ursus.ConcatAlign, ids, frame(ursus.Values("q", []int64{1}))),
				"they have none in common"},
			// Polars 1.44.1 keeps the first frame's x and drops the second's; its
			// docs say a collision raises.
			{"align of a column in two frames of three", concat(ursus.ConcatAlign,
				frame(ursus.Values("k", []int64{1}), ursus.Values("x", []int64{1})),
				frame(ursus.Values("k", []int64{1}), ursus.Values("x", []int64{2})),
				frame(ursus.Values("k", []int64{1}), ursus.Values("y", []int64{3}))),
				`column "x" is in frames 0 and 1 but not in every frame`},
			{"no such mode", ursus.Concat([]*ursus.LazyFrame{ids, ids}, mode(ursus.ConcatMode(99))), "no concat mode ConcatMode(99)"},
		} {
			_, err := tc.q.Collect(ctx)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: got %v, want an error with %q", tc.name, err, tc.want)
			}
		}
	})

	t.Run("eager", func(t *testing.T) {
		var dfs []*ursus.DataFrame
		for _, lf := range []*ursus.LazyFrame{ids, ys, zs} {
			df, err := lf.Collect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			dfs = append(dfs, df)
		}
		got, err := ursus.ConcatFrames(ctx, dfs, mode(ursus.ConcatAlignLeft))
		if err != nil {
			t.Fatal(err)
		}
		want, err := concat(ursus.ConcatAlignLeft, ids, ys, zs).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, want)
		if ursus.ConcatAlign.String() != "align" || ursus.ConcatStrict.String() != "vertical" {
			t.Errorf("the names %s and %s", ursus.ConcatAlign, ursus.ConcatStrict)
		}
	})
}

// TestConcatAlignUnderAMemoryLimit (step 307): align's joins spill past a limit, where
// a join's order is not promised, and its sort by the key and each frame's rows puts
// them back: 20,000 keys of three frames, a key twice in one, under 256 KiB in
// batches of 64, answer as without a limit.
func TestConcatAlignUnderAMemoryLimit(t *testing.T) {
	ctx := t.Context()
	const n = 20_000
	frameOf := func(name string, step, dup int) *ursus.LazyFrame {
		var ids, vals []int64
		for i := 0; i < n; i += step {
			ids, vals = append(ids, int64((i*7919)%n)), append(vals, int64(i))
			if dup > 0 && i%dup == 0 {
				ids, vals = append(ids, int64((i*7919)%n)), append(vals, int64(-i))
			}
			// 7919 is prime to n, so every key but the twice-held is one.
		}
		return ursus.Frame(ursus.Values("id", ids), ursus.Values(name, vals))
	}
	q := ursus.Concat([]*ursus.LazyFrame{frameOf("x", 1, 0), frameOf("y", 2, 5), frameOf("z", 3, 0)},
		ursus.WithConcatMode(ursus.ConcatAlign))
	want, err := q.Collect(ctx, ursus.WithBatchSize(64))
	if err != nil {
		t.Fatal(err)
	}
	var st ursus.MemoryStats
	got, err := q.Collect(ctx, ursus.WithBatchSize(64), ursus.WithMemoryLimit(256<<10), ursus.WithSpillDir(t.TempDir()),
		ursus.WithMemoryStats(&st))
	if err != nil {
		t.Fatalf("under 256 KiB: %v", err)
	}
	if st.Spills == 0 {
		t.Error("answered without spilling, so it proves nothing")
	}
	ursustest.AssertFrameEqual(t, got, want)
}

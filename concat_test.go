package ursus_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"ursus"
	"ursus/internal/uerr"
	"ursus/ursustest"
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

// TestConcatDiagonal: the union of the columns, with nulls in the gaps.
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

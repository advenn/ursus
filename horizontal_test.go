package ursus_test

// Step 146: ConcatStr and Struct, the horizontal family, whose every argument is an
// operand; List().Join and Str().Join; and the bit counts.

import (
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

// texts reads column name of df as strings, "null" for a null.
func texts(t *testing.T, df *ursus.DataFrame, name string) []string {
	t.Helper()
	col, err := df.Column[string](name)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, col.Len())
	for i := range out {
		v, ok := col.Get(i)
		if !ok {
			v = "null"
		}
		out[i] = v
	}
	return out
}

func collect(t *testing.T, lf *ursus.LazyFrame) *ursus.DataFrame {
	t.Helper()
	df, err := lf.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return df
}

func people() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.ValuesNullable("first", []string{"Ada", "Alan", ""}, []bool{true, true, false}),
		ursus.Values("last", []string{"Lovelace", "Turing", "Hopper"}),
		ursus.Values("n", []int64{7, 12, 3}),
		ursus.Values("x", []float64{1.5, 2, -0.25}),
		ursus.Values("ok", []bool{true, false, true}),
		ursus.Values("day", []time.Time{
			time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC),
			time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2024, 3, 2, 0, 0, 0, 0, time.UTC),
		}),
	)
}

func TestConcatStr(t *testing.T) {
	c := ursus.Col
	df := collect(t, people().Select(
		ursus.ConcatStr(" ", c("first"), c("last")).Alias("full"),
		ursus.ConcatStr("", c("last"), ursus.Lit("#"), c("n")).Alias("tag"),
		ursus.ConcatStr("|", c("n"), c("x"), c("ok"), c("day").Cast(ursus.Date)).Alias("mixed"),
		ursus.ConcatStr(",", c("last")).Alias("alone"),
		ursus.ConcatStr("-", ursus.Lit("a"), c("last")).Alias("lit_first"),
	))
	for _, c := range []struct {
		col  string
		want []string
	}{
		// A null operand nulls its row, as Polars' default does.
		{"full", []string{"Ada Lovelace", "Alan Turing", "null"}},
		{"tag", []string{"Lovelace#7", "Turing#12", "Hopper#3"}},
		// Every operand is formatted as its Cast to String formats it.
		{"mixed", []string{"7|1.5|true|2024-02-29", "12|2|false|2024-03-01", "3|-0.25|true|2024-03-02"}},
		{"alone", []string{"Lovelace", "Turing", "Hopper"}},
		{"lit_first", []string{"a-Lovelace", "a-Turing", "a-Hopper"}},
	} {
		if got := texts(t, df, c.col); !slices.Equal(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.col, got, c.want)
		}
	}

	t.Run("named after its first expression", func(t *testing.T) {
		s, err := people().Select(ursus.ConcatStr(" ", c("last"), c("first"))).CollectSchema(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if got := s.Names(); !slices.Equal(got, []string{"last"}) {
			t.Errorf("named %v", got)
		}
	})

	t.Run("a literal first is named literal", func(t *testing.T) {
		// Named as Field names it. Named after its first column instead, WithColumns
		// would replace last with the joined text.
		df := collect(t, people().WithColumns(ursus.ConcatStr("-", ursus.Lit("a"), c("last"))))
		if got := texts(t, df, "last"); !slices.Equal(got, []string{"Lovelace", "Turing", "Hopper"}) {
			t.Errorf("last became %q", got)
		}
		if got := texts(t, df, "literal"); !slices.Equal(got, []string{"a-Lovelace", "a-Turing", "a-Hopper"}) {
			t.Errorf("literal: %q", got)
		}
		// A rename reads the call's name as expr.OutputName gives it.
		s, err := people().Select(ursus.ConcatStr("-", ursus.Lit("a"), c("last")).Suffix("_s")).
			CollectSchema(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if got := s.Names(); !slices.Equal(got, []string{"literal_s"}) {
			t.Errorf("suffixed, it is named %v", got)
		}
	})

	t.Run("over a selection of several columns", func(t *testing.T) {
		// One call per column, each named after its column.
		df := collect(t, people().Select(ursus.ConcatStr("", c("first", "last"), ursus.Lit("!"))))
		if got := texts(t, df, "last"); !slices.Equal(got, []string{"Lovelace!", "Turing!", "Hopper!"}) {
			t.Errorf("last: %q", got)
		}
		// With a literal first, every expansion is named "literal", and the
		// expansion says so (expr.OutputName).
		assertUserError(t, people().Select(ursus.ConcatStr("", ursus.Lit("!"), c("first", "last"))),
			`expands to 2 columns that would all be named "literal"`)
	})

	t.Run("filtered on, through a projection", func(t *testing.T) {
		// Predicate pushdown rewrites the filter over the operands' own columns.
		df := collect(t, people().
			WithColumns(ursus.ConcatStr(" ", c("first"), c("last")).Alias("full")).
			Filter(c("full").Eq("Alan Turing")).
			Select(c("n")))
		if df.Height() != 1 {
			t.Fatalf("%d rows, want 1", df.Height())
		}
	})

	t.Run("over aggregates", func(t *testing.T) {
		df := collect(t, ursus.Frame(ursus.Values("k", []string{"a", "b", "a"}),
			ursus.Values("v", []int64{1, 2, 3})).
			GroupBy(c("k")).Agg(ursus.ConcatStr("=", c("k").First(), c("v").Sum()).Alias("s")).
			Sort(ursus.Asc(c("s"))))
		if got := texts(t, df, "s"); !slices.Equal(got, []string{"a=4", "b=2"}) {
			t.Errorf("got %q", got)
		}
	})

	t.Run("literals only", func(t *testing.T) {
		df := collect(t, people().WithColumns(ursus.ConcatStr("+", ursus.Lit("x"), ursus.Lit(1)).Alias("k")))
		if got := texts(t, df, "k"); !slices.Equal(got, []string{"x+1", "x+1", "x+1"}) {
			t.Errorf("got %q", got)
		}
	})

	t.Run("explained", func(t *testing.T) {
		out, err := people().Select(ursus.ConcatStr("-", c("first"), c("last"))).Explain(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, `concat_str(col("first"), lit("-"), col("last"))`) {
			t.Errorf("Explain does not show the call as written:\n%s", out)
		}
	})

	t.Run("refused", func(t *testing.T) {
		assertUserError(t, people().GroupBy(c("ok")).Agg(c("n").Implode()).
			Select(ursus.ConcatStr("", c("n"))), "cannot format")
		if _, err := people().Select(ursus.ConcatStr(",")).Collect(t.Context()); err == nil {
			t.Error("ConcatStr with no expressions ran")
		}
	})
}

func TestStruct(t *testing.T) {
	c := ursus.Col
	lf := people().Select(ursus.Struct(c("last"), c("n"), ursus.Lit(1.5).Alias("w")).Alias("p"))
	s, err := lf.CollectSchema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := ursus.StructOf(ursus.Of("last", ursus.String), ursus.Of("n", ursus.Int64), ursus.Of("w", ursus.Float64))
	if got := s.Field(0).Type; got != want {
		t.Errorf("the type is %s, want %s", got, want)
	}

	df := collect(t, lf.Select(c("p").Struct().Field("last").Alias("last"),
		c("p").Struct().Field("w").Alias("w"), c("p").IsNull().Alias("null")))
	if got := texts(t, df, "last"); !slices.Equal(got, []string{"Lovelace", "Turing", "Hopper"}) {
		t.Errorf("last: %q", got)
	}
	w, _ := df.Column[float64]("w")
	nulls, _ := df.Column[bool]("null")
	for i := range 3 {
		if v, _ := w.Get(i); v != 1.5 {
			t.Errorf("row %d: the literal field is %v, want 1.5 at every row", i, v)
		}
		if v, _ := nulls.Get(i); v {
			t.Errorf("row %d: the struct is null", i)
		}
	}

	t.Run("a struct of nulls is not a null struct", func(t *testing.T) {
		df := collect(t, people().Select(ursus.Struct(c("first")).Alias("p")).
			Select(c("p").IsNull().Alias("null"), c("p").Struct().Field("first").Alias("first")))
		nulls, _ := df.Column[bool]("null")
		if v, _ := nulls.Get(2); v {
			t.Error("a struct whose one field is null came out null")
		}
		if got := texts(t, df, "first"); got[2] != "null" {
			t.Errorf("the null field reads %q", got[2])
		}
	})

	t.Run("unnested back", func(t *testing.T) {
		// The literal field is as long as the struct: read through Field, a column
		// one row long would be repeated after the fact, and unnested it is not.
		df := collect(t, people().Select(ursus.Struct(c("n"), ursus.Lit("k").Alias("tag")).Alias("p")).Unnest("p"))
		if got := df.Columns(); !slices.Equal(got, []string{"n", "tag"}) {
			t.Errorf("columns %v", got)
		}
		if got := texts(t, df, "tag"); !slices.Equal(got, []string{"k", "k", "k"}) {
			t.Errorf("tag: %q", got)
		}
	})

	t.Run("two fields of one name", func(t *testing.T) {
		assertUserError(t, people().Select(ursus.Struct(c("n"), c("x").Alias("n"))), `named "n"`)
	})
}

func TestJoinStrings(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(
		ursus.Values("k", []string{"a", "b", "a", "c", "a"}),
		ursus.ValuesNullable("s", []string{"x", "y", "", "", "z"}, []bool{true, true, false, false, true}),
	)
	t.Run("Str().Join over the column", func(t *testing.T) {
		df := collect(t, lf.GroupBy().Agg(c("s").Str().Join(", ")))
		if got := texts(t, df, "s"); !slices.Equal(got, []string{"x, y, z"}) {
			t.Errorf("got %q", got)
		}
	})
	t.Run("Str().Join per group", func(t *testing.T) {
		// c's one value is null: its join is "", not null.
		df := collect(t, lf.GroupBy(c("k")).Agg(c("s").Str().Join("-")).Sort(ursus.Asc(c("k"))))
		if got := texts(t, df, "s"); !slices.Equal(got, []string{"x-z", "y", ""}) {
			t.Errorf("got %q", got)
		}
	})
	t.Run("List().Join", func(t *testing.T) {
		lists := lf.GroupBy(c("k")).Agg(c("s").Implode()).Sort(ursus.Asc(c("k"))).
			Select(c("k"), c("s").List().Join("/").Alias("j"))
		df := collect(t, lists)
		if got := texts(t, df, "j"); !slices.Equal(got, []string{"x/z", "y", ""}) {
			t.Errorf("got %q", got)
		}
	})
	t.Run("a null list stays null", func(t *testing.T) {
		// b has no list: a left join leaves it a null one.
		lists := lf.Filter(c("k").Ne("b")).GroupBy(c("k")).Agg(c("s").Implode())
		df := collect(t, ursus.Frame(ursus.Values("k", []string{"a", "b", "c"})).
			Join(lists, ursus.JoinOn(c("k")), ursus.JoinHow(ursus.JoinLeft)).
			Sort(ursus.Asc(c("k"))).Select(c("s").List().Join("/").Alias("j")))
		if got := texts(t, df, "j"); !slices.Equal(got, []string{"x/z", "null", ""}) {
			t.Errorf("got %q", got)
		}
	})
	t.Run("not strings", func(t *testing.T) {
		assertUserError(t, ursus.Frame(ursus.Values("n", []int64{1})).GroupBy().Agg(c("n").Str().Join(",")),
			"List of String")
	})
}

func TestBitCounts(t *testing.T) {
	c := ursus.Col
	// Each row: the value, then ones, zeros, leading ones, leading zeros, trailing
	// ones, trailing zeros, in the type's own width.
	type row struct {
		v    string
		want [6]uint32
	}
	cases := []struct {
		name string
		col  *ursus.Column
		rows []row
	}{
		{"Int8", ursus.Values("v", []int8{-1, 0, 1, 64, math.MinInt8, 6}), []row{
			{"-1", [6]uint32{8, 0, 8, 0, 8, 0}},
			{"0", [6]uint32{0, 8, 0, 8, 0, 8}},
			{"1", [6]uint32{1, 7, 0, 7, 1, 0}},
			{"64", [6]uint32{1, 7, 0, 1, 0, 6}},
			{"-128", [6]uint32{1, 7, 1, 0, 0, 7}},
			{"6", [6]uint32{2, 6, 0, 5, 0, 1}},
		}},
		{"Uint16", ursus.Values("v", []uint16{math.MaxUint16, 0x00f0}), []row{
			{"65535", [6]uint32{16, 0, 16, 0, 16, 0}},
			{"240", [6]uint32{4, 12, 0, 8, 0, 4}},
		}},
		{"Int64", ursus.Values("v", []int64{1, -1, math.MinInt64, 0}), []row{
			{"1", [6]uint32{1, 63, 0, 63, 1, 0}},
			{"-1", [6]uint32{64, 0, 64, 0, 64, 0}},
			{"min", [6]uint32{1, 63, 1, 0, 0, 63}},
			{"0", [6]uint32{0, 64, 0, 64, 0, 64}},
		}},
		{"Uint64", ursus.Values("v", []uint64{math.MaxUint64, 3}), []row{
			{"max", [6]uint32{64, 0, 64, 0, 64, 0}},
			{"3", [6]uint32{2, 62, 0, 62, 2, 0}},
		}},
	}
	names := []string{"ones", "zeros", "lead1", "lead0", "trail1", "trail0"}
	for _, tc := range cases {
		v := c("v")
		df := collect(t, ursus.Frame(tc.col).Select(
			v.BitwiseCountOnes().Alias("ones"), v.BitwiseCountZeros().Alias("zeros"),
			v.BitwiseLeadingOnes().Alias("lead1"), v.BitwiseLeadingZeros().Alias("lead0"),
			v.BitwiseTrailingOnes().Alias("trail1"), v.BitwiseTrailingZeros().Alias("trail0")))
		for j, name := range names {
			col, err := df.Column[uint32](name)
			if err != nil {
				t.Fatalf("%s %s: %v", tc.name, name, err)
			}
			for i, r := range tc.rows {
				if got, _ := col.Get(i); got != r.want[j] {
					t.Errorf("%s %s of %s: %d, want %d", tc.name, name, r.v, got, r.want[j])
				}
			}
		}
	}

	t.Run("nulls", func(t *testing.T) {
		df := collect(t, ursus.Frame(ursus.ValuesNullable("v", []int32{0, 5}, []bool{false, true})).
			Select(c("v").BitwiseCountOnes()))
		col, _ := df.Column[uint32]("v")
		if _, ok := col.Get(0); ok {
			t.Error("a null counted")
		}
		if v, _ := col.Get(1); v != 2 {
			t.Errorf("5 has %d ones", v)
		}
	})
	t.Run("not an integer", func(t *testing.T) {
		for _, col := range []*ursus.Column{ursus.Values("v", []float64{1}), ursus.Values("v", []bool{true})} {
			assertUserError(t, ursus.Frame(col).Select(c("v").BitwiseCountOnes()),
				"requires an integer")
		}
	})
}

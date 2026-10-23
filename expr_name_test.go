package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestNameNamespace is step 323: Polars' name namespace, Expr.Name(), and
// struct.drop. Every answer is Polars 1.44.1's.
func TestNameNamespace(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	lf := ursus.Frame(ursus.Values("a", []int64{1, 2}), ursus.Values("b", []int64{3, 4}))
	names := func(t *testing.T, lf *ursus.LazyFrame) string {
		t.Helper()
		s, err := lf.CollectSchema(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(s.Names(), " ")
	}
	one := func(name string) *ursus.LazyFrame { return ursus.Frame(ursus.Values(name, []int64{1})) }
	for _, tc := range []struct {
		name string
		lf   *ursus.LazyFrame
		want string
	}{
		{"keep", lf.Select(c("a").Mul(2).Alias("z").Name().Keep()), "a"},
		{"keep, b first", lf.Select(c("b").Add(c("a")).Name().Keep()), "b"},
		{"keep past a literal", lf.Select(ursus.Lit(1).Add(c("b")).Name().Keep()), "b"},
		{"keep through an inner alias", lf.Select(c("a").Alias("q").Mul(2).Name().Keep()), "a"},
		{"keep, then a suffix", lf.Select(c("a").Mul(2).Alias("q").Name().Keep().Name().Suffix("_x")), "a_x"},
		{"a suffix, then keep", lf.Select(c("a").Mul(2).Name().Suffix("_x").Name().Keep()), "a"},
		{"keep of two columns", lf.Select(c("a", "b").Mul(2).Name().Keep()), "a b"},
		{"keep in an aggregation", lf.GroupBy(c("a")).Agg(c("b").Sum().Alias("q").Name().Keep()), "a b"},
		{"keep over a window", lf.Select(c("b").Sum().Over(c("a")).Alias("q").Name().Keep()), "b"},
		{"lowercase", lf.Select(c("a").Alias("AbC").Name().ToLower()), "abc"},
		{"uppercase, every script", one("Ünï").Select(ursus.All().Name().ToUpper()), "ÜNÏ"},
		{"lowercase over uppercase", lf.Select(c("a").Name().ToUpper().Name().ToLower()), "a"},
		{"replace the text, every time", one("aXa").Select(c("aXa").Name().Replace("a", "b", true)), "bXb"},
		{"replace a pattern, every match", one("aXa").Select(c("aXa").Name().Replace("[a-z]", "_", false)), "_X_"},
		{"replace by a group", lf.Select(ursus.All().Name().Replace("^(.)$", "$1$1", false)), "aa bb"},
		{"a dot is no pattern literally", one("a.b").Select(c("a.b").Name().Replace(".", "_", true)), "a_b"},
		{"prefix and suffix and map", lf.Select(c("a").Name().Prefix("p_"), c("b").Name().Suffix("_s"),
			c("a").Name().Map(strings.ToUpper)), "p_a b_s A"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := names(t, tc.lf); got != tc.want {
				t.Errorf("%q, want %q", got, tc.want)
			}
		})
	}

	// A struct of x and y, y a struct of z; its second row null.
	s := lf.Select(c("a"), ursus.Struct(c("a").Alias("x"), ursus.Struct(c("b").Alias("z")).Alias("y")).Alias("s")).
		WithColumns(ursus.When(c("a").Eq(2)).Then(ursus.Null(ursus.NullT)).Otherwise(c("s")).Alias("s"))
	fields := func(t *testing.T, lf *ursus.LazyFrame, col string) string {
		t.Helper()
		sch, err := lf.CollectSchema(ctx)
		if err != nil {
			t.Fatal(err)
		}
		field, ok := sch.ByName(col)
		if !ok {
			t.Fatalf("no column %s in %s", col, strings.Join(sch.Names(), " "))
		}
		var out []string
		for _, f := range field.Type.Fields() {
			out = append(out, f.Name)
		}
		return strings.Join(out, " ")
	}
	up := func(n string) string { return strings.ToUpper(n) }
	twice := func(n string) string { return n + n }
	for _, tc := range []struct {
		name, col string
		lf        *ursus.LazyFrame
		want      string
	}{
		// Only the struct's own fields: the nested struct's z is z still.
		{"prefix_fields", "s", s.Select(c("s").Name().PrefixFields("p_")), "p_x p_y"},
		{"suffix_fields, the column renamed apart", "s_col", s.Select(c("s").Name().SuffixFields("_1").Name().Suffix("_col")), "x_1 y_1"},
		{"map_fields", "s", s.Select(c("s").Name().MapFields(func(n string) string { return "f_" + n })), "f_x f_y"},
		{"in an aggregation", "s", s.GroupBy(c("a")).Agg(c("s").First().Name().PrefixFields("p_")), "p_x p_y"},
		{"over a window", "s", s.Select(c("s").First().Over(c("a")).Name().PrefixFields("p_")), "p_x p_y"},
		{"inside a window", "s", s.Select(c("s").Name().PrefixFields("p_").First().Over(c("a"))), "p_x p_y"},
		{"a filter on a field it named, pushed down", "s",
			s.Select(c("s").Name().PrefixFields("p_")).Filter(c("s").Struct().Field("p_x").Gt(0)), "p_x p_y"},
		// Two functions over one struct are two types, in a window too, whose
		// temporaries are shared by identity.
		{"two map_fields, the first", "u", s.Select(c("s").Name().MapFields(up).Alias("u"), c("s").Name().MapFields(twice).Alias("t")), "X Y"},
		{"two map_fields, the second", "t", s.Select(c("s").Name().MapFields(up).Alias("u"), c("s").Name().MapFields(twice).Alias("t")), "xx yy"},
		{"two map_fields inside windows", "t", s.Select(c("s").Name().MapFields(up).First().Over(c("a")).Alias("u"),
			c("s").Name().MapFields(twice).First().Over(c("a")).Alias("t")), "xx yy"},
		{"drop x", "s", s.Select(c("s").Struct().Drop("x")), "y"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fields(t, tc.lf, tc.col); got != tc.want {
				t.Errorf("the fields are %q, want %q", got, tc.want)
			}
			// The data's own fields agree with the schema's.
			df, err := tc.lf.Collect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			at := df.Schema().IndexOf(tc.col)
			if at < 0 {
				t.Fatalf("no column %s collected", tc.col)
			}
			var got []string
			for _, f := range df.ColumnAt(at).DType().Fields() {
				got = append(got, f.Name)
			}
			if strings.Join(got, " ") != tc.want {
				t.Errorf("the data's fields are %q, want %q", got, tc.want)
			}
		})
	}

	// The values: the renamed struct's x, and its null kept.
	got, err := s.Select(c("s").Name().MapFields(twice).Struct().Field("xx"), c("s").Name().MapFields(twice).IsNull().Alias("n")).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want, err := ursus.Frame(ursus.ValuesNullable("xx", []int64{1, 0}, []bool{true, false}),
		ursus.Values("n", []bool{false, true})).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, want)
	dropped, err := s.Select(c("s").Struct().Drop("x").Struct().Field("y").Struct().Field("z")).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want, err = ursus.Frame(ursus.ValuesNullable("z", []int64{3, 0}, []bool{true, false})).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, dropped, want)

	t.Run("refused", func(t *testing.T) {
		for name, tc := range map[string]struct {
			lf   *ursus.LazyFrame
			want string
		}{
			"keep of a literal":            {lf.Select(ursus.Lit(1).Alias("z").Name().Keep()), "reads no column"},
			"keep of a count":              {lf.Select(ursus.Len().Alias("n").Name().Keep()), "reads no column"},
			"a pattern that won't compile": {lf.Select(c("a").Name().Replace("(", "_", false)), "does not compile"},
			"fields of a number":           {lf.Select(c("a").Name().PrefixFields("p_")), "renames a struct's fields"},
			"two fields of one name": {s.Select(c("s").Name().MapFields(func(string) string { return "same" })),
				`names two fields "same"`},
			"a field not there": {s.Select(c("s").Struct().Drop("q")), `no field "q"`},
			"every field":       {s.Select(c("s").Struct().Drop("x", "y")), "drops every field"},
			"a number's fields": {lf.Select(c("a").Struct().Drop("x")), "Struct"},
			"a panic in a field's name": {s.Select(c("s").Name().MapFields(func(n string) string { panic("no") })),
				"panicked"},
		} {
			if _, err := tc.lf.Collect(ctx); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: %v, want an error saying %q", name, err, tc.want)
			}
		}
	})
}

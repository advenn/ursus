package ursus_test

import (
	"encoding/csv"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestEncodings is S of v0.6.6-scope.md (step 271): encode and decode, hex and
// base64. Every answer is Polars 1.44.1's.
func TestEncodings(t *testing.T) {
	c := ursus.Col
	s := c("s").Str()
	text := ursus.Frame(ursus.ValuesNullable("s", []string{"hi", "é", "", ""}, []bool{true, true, false, true}))
	if got, want := csvOf(t, text.Select(s.Encode(ursus.Hex).Alias("hex"), s.Encode(ursus.Base64).Alias("b64"))),
		"hex,b64\n6869,aGk=\nc3a9,w6k=\n,\n\"\",\"\"\n"; got != want {
		t.Errorf("encode:\ngot  %q\nwant %q", got, want)
	}
	// [b'hi', b'\xc3\xa9', None, None, None]: upper case hex reads, odd or foreign
	// digits do not, and base64 must be padded.
	hexes := ursus.Frame(ursus.ValuesNullable("s", []string{"6869", "C3A9", "", "zz", "abc"}, []bool{true, true, false, true, true}))
	b64s := ursus.Frame(ursus.ValuesNullable("s", []string{"aGk=", "w6k=", "", "!!", "aGk"}, []bool{true, true, false, true, true}))
	back := func(e ursus.Expr) ursus.Expr { return e.Cast(ursus.String).Alias("s") }
	for name, q := range map[string]*ursus.LazyFrame{
		"hex":    hexes.Select(back(s.Decode(ursus.Hex, false))),
		"base64": b64s.Select(back(s.Decode(ursus.Base64, false))),
	} {
		if got, want := csvOf(t, q), "s\nhi\né\n\n\n\n"; got != want {
			t.Errorf("decode %s:\ngot  %q\nwant %q", name, got, want)
		}
	}
	got, err := hexes.Select(s.Decode(ursus.Hex, false)).Collect(t.Context())
	if err != nil || got.Schema().Field(0).Type != ursus.Binary {
		t.Errorf("decode is Binary: %v, %v", got, err)
	}
	if _, err := hexes.Select(s.Decode(ursus.Hex, true)).Collect(t.Context()); err == nil ||
		!strings.Contains(err.Error(), `row 3 is not hex: "zz"`) {
		t.Errorf("strict decode: %v", err)
	}
	if _, err := text.Select(s.Encode("rot13")).Collect(t.Context()); err == nil {
		t.Error("an encoding that is not one should be refused")
	}
}

// TestToIntegerWith: to_integer's base, a constant or a column, its dtype and strict.
// Every answer is Polars 1.44.1's.
func TestToIntegerWith(t *testing.T) {
	c := ursus.Col
	s := c("s").Str()
	lf := ursus.Frame(
		ursus.ValuesNullable("s", []string{"ff", "10", "-1a", "z", "", " 7", "0x10"}, []bool{true, true, true, true, false, true, true}),
		ursus.Values("b", []int64{16, 2, 16, 36, 10, 10, 16}))
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		// No space, 0x or underscore reads.
		{"base 16", lf.Select(s.ToIntegerWith(16, ursus.Int64, false)), "s\n255\n16\n-26\n\n\n\n\n"},
		{"a base a row", lf.Select(s.ToIntegerWith(c("b"), ursus.Int64, false)), "s\n255\n2\n-26\n35\n\n\n\n"},
		{"Int8", ursus.Frame(ursus.Values("s", []string{"127", "128"})).Select(s.ToIntegerWith(10, ursus.Int8, false)), "s\n127\n\n"},
		{"UInt8", ursus.Frame(ursus.Values("s", []string{"-1", "200"})).Select(s.ToIntegerWith(10, ursus.Uint8, false)), "s\n\n200\n"},
		{"a sign, not an underscore", ursus.Frame(ursus.Values("s", []string{"12", "+5", "1_000"})).Select(s.ToIntegerWith(10, ursus.Int64, false)),
			"s\n12\n5\n\n"},
	} {
		if got := csvOf(t, tc.q); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
	got, err := ursus.Frame(ursus.Values("s", []string{"1"})).Select(s.ToIntegerWith(10, ursus.Uint8, false)).Collect(t.Context())
	if err != nil || got.Schema().Field(0).Type != ursus.Uint8 {
		t.Errorf("the dtype asked for: %v, %v", got, err)
	}
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		"strict":            {lf.Select(s.ToIntegerWith(16, ursus.Int64, true)), `"z", " 7", "0x10"`},
		"strict, too large": {ursus.Frame(ursus.Values("s", []string{"128"})).Select(s.ToIntegerWith(10, ursus.Int8, true)), "Int8"},
		"not an integer":    {lf.Select(s.ToIntegerWith(10, ursus.Float64, false)), "not Float64"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}

// TestReplaceNAndConcatSkipping: replace's n, and concat_str past nulls. Every answer
// is Polars 1.44.1's, but a regex replaced more than once, which Polars refuses.
func TestReplaceNAndConcatSkipping(t *testing.T) {
	c := ursus.Col
	s := c("s").Str()
	lf := ursus.Frame(ursus.ValuesNullable("s", []string{"aaa", "abab", ""}, []bool{true, true, false}))
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want string
	}{
		{"n=2", s.ReplaceN("a", "X", true, 2), "s\nXXa\nXbXb\n\n"},
		{"n=0", s.ReplaceN("a", "X", true, 0), "s\naaa\nabab\n\n"},
		{"n=-1", s.ReplaceN("a", "X", true, -1), "s\nXXX\nXbXb\n\n"},
		{"a regex, n=2", s.ReplaceN("(a)", "<$1>", false, 2), "s\n<a><a>a\n<a>b<a>b\n\n"},
		{"a regex, n=1", s.ReplaceN("a+", "X", false, 1), "s\nX\nXbab\n\n"},
	} {
		if got := csvOf(t, lf.Select(tc.e)); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
	ab := ursus.Frame(ursus.ValuesNullable("a", []string{"x", "", "z", ""}, []bool{true, false, true, false}),
		ursus.ValuesNullable("b", []int64{1, 2, 0, 0}, []bool{true, true, false, false}))
	if got, want := csvOf(t, ab.Select(ursus.ConcatStrIgnoreNulls("-", c("a"), c("b")))), "a\nx-1\n2\nz\n\"\"\n"; got != want {
		t.Errorf("concat_str, ignore_nulls:\ngot  %q\nwant %q", got, want)
	}
	if got, want := csvOf(t, ab.Select(ursus.ConcatStrIgnoreNulls("", c("b"), ursus.Lit("!")))), "b\n1!\n2!\n!\n!\n"; got != want {
		t.Errorf("concat_str with a literal:\ngot  %q\nwant %q", got, want)
	}
}

// onlyCell is a one-row, one-column answer's value, as CSV writes it, unquoted.
func onlyCell(t *testing.T, q *ursus.LazyFrame) string {
	t.Helper()
	recs, err := csv.NewReader(strings.NewReader(csvOf(t, q))).ReadAll()
	if err != nil || len(recs) != 2 || len(recs[1]) != 1 {
		t.Fatalf("not one cell: %v, %v", recs, err)
	}
	return recs[1][0]
}

const jsonDoc = `{"a": {"b": [1, 2, {"c": "x"}]}, "d": 3.5, "e": null, "f": true}`

// TestJsonPathMatch: json_path_match's paths. Every answer is Polars 1.44.1's.
func TestJsonPathMatch(t *testing.T) {
	c := ursus.Col
	s := c("s").Str()
	lf := ursus.Frame(ursus.ValuesNullable("s", []string{jsonDoc, `{"a": 1}`, "", "not json"}, []bool{true, true, false, true}))
	// Each case's four rows as CSV writes them; a quoted one holds commas of its own.
	for _, tc := range []struct {
		path string
		rows []string
	}{
		{"$.d", []string{"3.5", "", "", ""}},
		{"$.a.b[0]", []string{"1", "", "", ""}},
		{"$.a.b[2].c", []string{"x", "", "", ""}},
		{"$.a.b", []string{`"[1,2,{""c"":""x""}]"`, "", "", ""}},
		{"$.e", []string{"", "", "", ""}},
		{"$.f", []string{"true", "", "", ""}},
		{"$.a", []string{`"{""b"":[1,2,{""c"":""x""}]}"`, "1", "", ""}},
		{"$['d']", []string{"3.5", "", "", ""}},
		{"$.zz", []string{"", "", "", ""}},
		{"$.a.b[-1]", []string{`"{""c"":""x""}"`, "", "", ""}},
		{"$..c", []string{"x", "", "", ""}},
	} {
		want := "s\n" + strings.Join(tc.rows, "\n") + "\n"
		if got := csvOf(t, lf.Select(s.JsonPathMatch(tc.path))); got != want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.path, got, want)
		}
	}
	// Numbers are reprinted, an integer that fits 64 bits but -0 as written; of a
	// repeated key the last value, in the first one's place; and only what JSON must
	// is escaped.
	for _, tc := range []struct{ doc, path, want string }{
		{`[1e3, 1e15, 1e16, 1.5e-7, 0.00001, 1e-6, -0, 2.50, 1E2, 12345678901234567890, -9223372036854775809, 100000000000000000000]`, "$",
			"[1000.0,1000000000000000.0,1e+16,1.5e-7,0.00001,1e-6,-0.0,2.5,100.0,12345678901234567890,-9.223372036854776e+18,1e+20]"},
		{`{"a": 1, "b": 0, "a": 2}`, "$.a", "2"},
		{`{"a": 1, "b": 0, "a": 2}`, "$", `{"a":2,"b":0}`},
		{`{"c": "q\"x\n\u0001é/\u003c"}`, "$", `{"c":"q\"x\n\u0001é/<"}`},
		{`{"a": {"c": 2}, "c": 1}`, "$..c", "1"},
		{`{"a": [{"c": 3}, {"c": 4}], "b": {"c": 5}}`, "$..c", "3"},
	} {
		if v := onlyCell(t, one(tc.doc).Select(s.JsonPathMatch(tc.path))); v != tc.want {
			t.Errorf("%s of %s:\ngot  %s\nwant %s", tc.path, tc.doc, v, tc.want)
		}
	}
	if _, err := lf.Select(s.JsonPathMatch("a.b")).Collect(t.Context()); err == nil {
		t.Error("a path not starting at $ should be refused")
	}
}

// TestJsonDecode: json_decode with its dtype. Every answer is Polars 1.44.1's.
func TestJsonDecode(t *testing.T) {
	c := ursus.Col
	s := c("s").Str()
	objs := ursus.Frame(ursus.ValuesNullable("s", []string{`{"a": 1, "b": "x"}`, `{}`, `{"b": "y", "z": [1]}`, ""},
		[]bool{true, true, true, false}))
	ab := ursus.StructOf(ursus.Field{Name: "a", Type: ursus.Int64}, ursus.Field{Name: "b", Type: ursus.String})
	d := s.JsonDecode(ab).Alias("d")
	// [{a: 1, b: 'x'}, {a: None, b: None}, {a: None, b: 'y'}, None]
	if got, want := csvOf(t, objs.Select(d).Select(c("d").IsNull().Alias("null"),
		c("d").Struct().Field("a").Alias("a"), c("d").Struct().Field("b").Alias("b"))), "null,a,b\nfalse,1,x\nfalse,,\nfalse,,y\ntrue,,\n"; got != want {
		t.Errorf("a Struct:\ngot  %q\nwant %q", got, want)
	}
	lists := ursus.Frame(ursus.ValuesNullable("s", []string{"[1,2]", "[]", "", "[3.5]"}, []bool{true, true, false, true}))
	// [[1, 2], [], None, [3]]
	if got, want := listText4(t, lists, s.JsonDecode(ursus.List(ursus.Int64))), "n,e0,e1,e2,e3\n2,1,2,,\n0,,,,\n,,,,\n1,3,,,\n"; got != want {
		t.Errorf("a List:\ngot  %q\nwant %q", got, want)
	}
	// Numbers and Booleans as each other and as text; a number past its type's range
	// null; a scalar as a List a List of itself; of a repeated key the first value.
	for _, tc := range []struct {
		name, doc string
		dt        ursus.DataType
		want      string
	}{
		{"Int64", `[1.9, -1.9, true, 9223372036854775808, 1e30, -3]`, ursus.List(ursus.Int64), "1|-1|1|||-3"},
		{"Int8", `[300, -128]`, ursus.List(ursus.Int8), "|-128"},
		{"UInt8", `[-1, 255, 256]`, ursus.List(ursus.Uint8), "|255|"},
		{"UInt64", `[18446744073709551615, 1.5, -1.5, -0.5, false]`, ursus.List(ursus.Uint64), "18446744073709551615|1||0|0"},
		{"Float32", `[1e300, true, 2]`, ursus.List(ursus.Float32), "inf|1.0|2.0"},
		{"String", `[true, 1.50, 1e16, -0, -0.0, 1.5e-7, 12345678901234567890123, "x"]`, ursus.List(ursus.String),
			"true|1.5|10000000000000000|0|-0|0.00000015|12345678901234567890123|x"},
		{"a scalar as a List", `5`, ursus.List(ursus.Int64), "5"},
		{"scalars as Lists", `[1, [2, 3]]`, ursus.List(ursus.List(ursus.Int64)), "1|2,3"},
		{"a repeated key", `{"a": 1, "a": 2}`, ursus.StructOf(ursus.Field{Name: "a", Type: ursus.Int64}), "1"},
	} {
		e := s.JsonDecode(tc.dt)
		switch {
		case tc.dt.String() == "List(List(Int64))":
			e = e.List().Eval(ursus.Element().List().Eval(ursus.Element().Cast(ursus.String)).List().Join(","))
		case strings.HasPrefix(tc.dt.String(), "Struct"):
			e = e.Struct().Field("a").Cast(ursus.String).Implode()
		default:
			e = e.List().Eval(ursus.Element().Cast(ursus.String))
		}
		if v := onlyCell(t, one(tc.doc).Select(e.List().Eval(ursus.Element().FillNullWith("")).List().Join("|"))); v != tc.want {
			t.Errorf("%s:\ngot  %s\nwant %s", tc.name, v, tc.want)
		}
	}
	for name, tc := range map[string]struct {
		doc  string
		dt   ursus.DataType
		want string
	}{
		"not JSON":             {`[1, 2] x`, ursus.List(ursus.Int64), "row 0 is not JSON"},
		"an object as a List":  {`{"a": 1}`, ursus.List(ursus.Int64), "cannot read an object as List"},
		"an array as a Struct": {`[1]`, ab, "cannot read an array as Struct"},
		"a string as a number": {`{"a": "x"}`, ab, "cannot read a string as Int64"},
		"a number as a Bool":   {`[1]`, ursus.List(ursus.Bool), "cannot read the number 1 as Bool"},
		"an array as a String": {`[[1]]`, ursus.List(ursus.String), "cannot read an array as String"},
	} {
		if _, err := one(tc.doc).Select(s.JsonDecode(tc.dt)).Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}

// TestListEvalAndFoldOverAggregates: a list's eval over an aggregate in a select,
// which the aggregation's rewrite did not descend until step 271, and a fold over
// aggregates, built into its arithmetic before. Polars 1.44.1 answers [2, 4, 6] and
// 36.
func TestListEvalAndFoldOverAggregates(t *testing.T) {
	c, el := ursus.Col, ursus.Element
	lf := ursus.Frame(ursus.Values("a", []int64{1, 2, 3}), ursus.Values("b", []int64{10, 20, 30}))
	if got, want := csvOf(t, lf.Select(c("a").Implode().List().Eval(el().Mul(2).Cast(ursus.String)).List().Join(","))), "a\n\"2,4,6\"\n"; got != want {
		t.Errorf("implode then eval:\ngot  %q\nwant %q", got, want)
	}
	sum := func(acc, x ursus.Expr) ursus.Expr { return acc.Add(x) }
	if got, want := csvOf(t, lf.Select(ursus.Fold(0, sum, c("a").Sum(), c("b").Max()).Alias("f"))), "f\n36\n"; got != want {
		t.Errorf("a fold of aggregates:\ngot  %q\nwant %q", got, want)
	}
}

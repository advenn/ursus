package ursus_test

import (
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// Encode and decode, to_integer's base and dtype, replace's n, concat_str past nulls,
// json_path_match and json_decode, S of v0.6.6-scope.md (step 271): every answer is
// Polars 2.1.0's, recorded in the strcodec group of testdata/oracle and spelled in
// oracle_strcodec_test.go since step 499, which took this file's hand tables. What
// stays asserts the refusals, and holds a regex replaced more than once, which
// Polars refuses, to Go's regexp.

// TestEncodings: a strict decode names the row and the value; an encoding that is
// none is refused.
func TestEncodings(t *testing.T) {
	s := ursus.Col("s").Str()
	hexes := ursus.Frame(ursus.ValuesNullable("s", []string{"6869", "C3A9", "", "zz", "abc"}, []bool{true, true, false, true, true}))
	if _, err := hexes.Select(s.Decode(ursus.Hex, true)).Collect(t.Context()); err == nil ||
		!strings.Contains(err.Error(), `row 3 is not hex: "zz"`) {
		t.Errorf("strict decode: %v", err)
	}
	if _, err := hexes.Select(s.Encode("rot13")).Collect(t.Context()); err == nil {
		t.Error("an encoding that is not one should be refused")
	}
}

// TestToIntegerWith: strict parsing names the values that do not read; a value past
// the type names the type; a float type is refused.
func TestToIntegerWith(t *testing.T) {
	c := ursus.Col
	s := c("s").Str()
	lf := ursus.Frame(
		ursus.ValuesNullable("s", []string{"ff", "10", "-1a", "z", "", " 7", "0x10"}, []bool{true, true, true, true, false, true, true}),
		ursus.Values("b", []int64{16, 2, 16, 36, 10, 10, 16}))
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

// replaceFirstN is Go's regexp replacing the first n matches, each through the
// template's $1 groups: the oracle for ReplaceN of a regex with n past one, which
// Polars 2.1 refuses ("regex replacement with 'n > 1' not yet supported").
func replaceFirstN(re *regexp.Regexp, s, tmpl string, n int) string {
	var out []byte
	last := 0
	for _, m := range re.FindAllStringSubmatchIndex(s, n) {
		out = append(out, s[last:m[0]]...)
		out = re.ExpandString(out, tmpl, s, m)
		last = m[1]
	}
	return string(append(out, s[last:]...))
}

// TestReplaceNRegexMatchesGoRegexp: a regex replaced n times over 300 generated
// strings of a and b, against Go's regexp replacing its first n matches.
func TestReplaceNRegexMatchesGoRegexp(t *testing.T) {
	r := rand.New(rand.NewPCG(499, 271))
	vals := make([]string, 300)
	for i := range vals {
		b := make([]byte, r.IntN(9))
		for j := range b {
			b[j] = "ab"[r.IntN(2)]
		}
		vals[i] = string(b)
	}
	s := ursus.Col("s").Str()
	df, err := ursus.Frame(ursus.Values("s", vals)).Select(
		s.ReplaceN("(a)", "<$1>", false, 2).Alias("g2"), s.ReplaceN("(a)", "<$1>", false, 3).Alias("g3"),
		s.ReplaceN("a+", "X", false, 2).Alias("x2"), s.ReplaceN("(b)(a)", "$2$1", false, 2).Alias("swap2")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range vals {
		for _, c := range []struct {
			col, pattern, tmpl string
			n                  int
		}{{"g2", "(a)", "<$1>", 2}, {"g3", "(a)", "<$1>", 3}, {"x2", "a+", "X", 2}, {"swap2", "(b)(a)", "$2$1", 2}} {
			got, _, err := df.At[string](i, c.col)
			if err != nil {
				t.Fatal(err)
			}
			if want := replaceFirstN(regexp.MustCompile(c.pattern), v, c.tmpl, c.n); got != want {
				t.Errorf("%s of %q = %q, want %q", c.col, v, got, want)
			}
		}
	}
}

// TestJsonRefusals: a path not starting at $; a document that is not JSON, and one
// whose shape the type cannot take.
func TestJsonRefusals(t *testing.T) {
	s := ursus.Col("s").Str()
	if _, err := one(`{"a": 1}`).Select(s.JsonPathMatch("a.b")).Collect(t.Context()); err == nil {
		t.Error("a path not starting at $ should be refused")
	}
	ab := ursus.StructOf(ursus.Field{Name: "a", Type: ursus.Int64}, ursus.Field{Name: "b", Type: ursus.String})
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

package ursus_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// strPatternFixture holds a value, a pattern and a replacement a row: nulls in each,
// a regex that is not its own literal ("a+", "."), and an empty pattern.
func strPatternFixture() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.ValuesNullable("s", []string{"abc", "xbx", "hello", "", "aa", "abc", "a.c", "abc"},
			[]bool{true, true, true, false, true, true, true, true}),
		ursus.ValuesNullable("p", []string{"b", "x", "", "a", "a+", "a", ".", ""},
			[]bool{true, true, false, true, true, true, true, true}),
		ursus.ValuesNullable("v", []string{"Z", "Y", "W", "V", "", "Q", "R", "S"},
			[]bool{true, true, true, true, false, true, true, true}))
}

// TestStringPatternsAreExpressions is E5's second part (step 255): a pattern a row
// for contains, starts_with, ends_with, strip_prefix, strip_suffix and split, and a
// value a row for replace and replace_all. Every answer is Polars 1.44.1's, but a
// null value: Polars keeps the string as it was, ursus gives null (row 5's r1, ra,
// rr1 and rra, which Polars has as "aa").
func TestStringPatternsAreExpressions(t *testing.T) {
	c := ursus.Col
	s, p, v := c("s").Str(), c("p"), c("v")
	q := strPatternFixture().Select(
		s.Contains(p, false).Alias("re"), s.Contains(p, true).Alias("lit"),
		s.StartsWith(p).Alias("sw"), s.EndsWith(p).Alias("ew"),
		s.StripPrefix(p).Alias("sp"), s.StripSuffix(p).Alias("ss"),
		s.Split(p).List().Join("|").Alias("split"), s.Split(p).List().Len().Alias("nsplit"),
		s.Replace("b", v, true).Alias("r1"), s.ReplaceAll("a", v, true).Alias("ra"),
		s.Replace("[bc]", v, false).Alias("rr1"), s.ReplaceAll("[bc]", v, false).Alias("rra"))
	want := "re,lit,sw,ew,sp,ss,split,nsplit,r1,ra,rr1,rra\n" +
		"true,true,false,false,abc,abc,a|c,2,aZc,Zbc,aZc,aZZ\n" +
		"true,true,true,true,bx,xb,|b|,3,xYx,xbx,xYx,xYx\n" +
		",,,,,,,,hello,hello,hello,hello\n" +
		",,,,,,,,,,,\n" +
		"true,false,false,false,aa,aa,aa,1,,,,\n" +
		"true,true,true,false,bc,abc,|bc,2,aQc,Qbc,aQc,aQQ\n" +
		"true,true,false,false,a.c,a.c,a|c,2,a.c,R.c,a.R,a.R\n" +
		"true,true,true,true,abc,abc,a|b|c,3,aSc,Sbc,aSc,aSS\n"
	for _, size := range []int{1, 3, 8192} {
		var got bytes.Buffer
		if err := q.WriteCSV(t.Context(), &got, ursus.WithBatchSize(size)); err != nil {
			t.Fatalf("batch %d: %v", size, err)
		}
		if got.String() != want {
			t.Errorf("batch %d:\ngot  %q\nwant %q", size, got.String(), want)
		}
	}

	// A value a row is inserted as it is written, $0 and all, regex or not, as Polars
	// inserts it. Polars: a<$0>c<$0>, a<$0>cb.
	var got bytes.Buffer
	if err := ursus.Frame(ursus.Values("s", []string{"abcb"}), ursus.Values("v", []string{"<$0>"})).
		Select(c("s").Str().ReplaceAll("b", c("v"), false).Alias("all"),
			c("s").Str().Replace("b", c("v"), false).Alias("one")).
		WriteCSV(t.Context(), &got); err != nil {
		t.Fatal(err)
	}
	if want := "all,one\na<$0>c<$0>,a<$0>cb\n"; got.String() != want {
		t.Errorf("a value with $0:\ngot  %q\nwant %q", got.String(), want)
	}
}

// TestStringPatternsRefuse: an invalid regex in a row is an error naming it, and a
// pattern that is not a String is refused while the query is planned.
func TestStringPatternsRefuse(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(ursus.Values("s", []string{"a", "b"}), ursus.Values("p", []string{"a", "("}),
		ursus.Values("n", []int64{1, 2}))
	for name, tc := range map[string]struct {
		e    ursus.Expr
		want string
	}{
		"an invalid regex":    {c("s").Str().Contains(c("p"), false), `invalid regular expression "(" in row 1`},
		"an integer pattern":  {c("s").Str().StartsWith(c("n")), "requires a String pattern"},
		"an integer value":    {c("s").Str().Replace("a", c("n"), true), "requires a String value"},
		"an integer receiver": {c("n").Str().Split(c("p")), "requires a String operand"},
		"an invalid constant": {c("s").Str().Replace("(", c("p"), false), "invalid regular expression"},
	} {
		_, err := lf.Select(tc.e).Collect(t.Context())
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
	// The same invalid pattern taken as literal text matches nothing, and is no error.
	if _, err := lf.Select(c("s").Str().Contains(c("p"), true)).Collect(t.Context()); err != nil {
		t.Errorf("a literal pattern: %v", err)
	}
}

package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

func strManyFixture() *ursus.LazyFrame {
	return ursus.Frame(ursus.ValuesNullable("s", []string{"the Cat sat", "dog and cat", "", "", "catcat dogs"},
		[]bool{true, true, true, false, true}))
}

func one(v string) *ursus.LazyFrame { return ursus.Frame(ursus.Values("s", []string{v})) }

// TestManyPatterns is S of v0.6.6-scope.md (step 270): contains_any, replace_many,
// extract_many and find_many, their three ways of matching, and ASCII case-blind.
// Every answer is Polars 1.44.1's.
func TestManyPatterns(t *testing.T) {
	c := ursus.Col
	s := c("s").Str()
	lf := strManyFixture()
	join := func(e ursus.Expr) ursus.Expr {
		return e.List().Eval(ursus.Element().Cast(ursus.String)).List().Join("|")
	}
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		{"contains_any", lf.Select(s.ContainsAny([]string{"cat", "dog"}, false)), "s\nfalse\ntrue\nfalse\n\ntrue\n"},
		{"contains_any, ASCII case-blind", lf.Select(s.ContainsAny([]string{"cat", "DOG"}, true)), "s\ntrue\ntrue\nfalse\n\ntrue\n"},
		{"an empty pattern", lf.Select(s.ContainsAny([]string{""}, false)), "s\ntrue\ntrue\ntrue\n\ntrue\n"},
		{"only ASCII is case-blind", one("École").Select(s.ContainsAny([]string{"éCOLE"}, true)), "s\nfalse\n"},
		{"replace_many", lf.Select(s.ReplaceMany([]string{"cat", "dog"}, []string{"C", "D"}, false, false)),
			"s\nthe Cat sat\nD and C\n\"\"\n\nCC Ds\n"},
		{"one replacement", lf.Select(s.ReplaceMany([]string{"cat", "dog"}, []string{"X"}, false, false)),
			"s\nthe Cat sat\nX and X\n\"\"\n\nXX Xs\n"},
		{"replace_many, case-blind", lf.Select(s.ReplaceMany([]string{"CAT"}, []string{"C"}, true, false)),
			"s\nthe C sat\ndog and C\n\"\"\n\nCC dogs\n"},
		// The match ending first; of two ending together the one starting first.
		{"ends together", one("abcd").Select(s.ReplaceMany([]string{"bc", "abc"}, []string{"1", "2"}, false, false)), "s\n2d\n"},
		{"ends first", one("abcd").Select(s.ReplaceMany([]string{"ab", "a"}, []string{"1", "2"}, false, false)), "s\n2bcd\n"},
		{"leftmost, listed first", one("abcd").Select(s.ReplaceMany([]string{"ab", "a"}, []string{"1", "2"}, false, true)), "s\n1cd\n"},
		{"an empty pattern between", one("ab").Select(s.ReplaceMany([]string{"", "a"}, []string{"-", "A"}, false, false)), "s\n-a-b-\n"},
		{"extract_many", lf.Select(join(s.ExtractMany([]string{"cat", "dog"}, false, false, false))),
			"s\n\"\"\ndog|cat\n\"\"\n\ncat|cat|dog\n"},
		{"overlapping", one("aaaa").Select(join(s.ExtractMany([]string{"aa"}, false, true, false))), "s\naa|aa|aa\n"},
		{"not overlapping", one("aaaa").Select(join(s.ExtractMany([]string{"aa"}, false, false, false))), "s\naa|aa\n"},
		{"overlapping, by their ends", one("abcd").Select(join(s.ExtractMany([]string{"bc", "abc", "b"}, false, true, false))), "s\nb|abc|bc\n"},
		{"leftmost", one("abcd").Select(join(s.ExtractMany([]string{"ab", "bc", "abc"}, false, false, true))), "s\nab\n"},
		{"find_many", lf.Select(join(s.FindMany([]string{"cat", "dog"}, false, false, false))), "s\n\"\"\n0|8\n\"\"\n\n0|3|7\n"},
		{"find_many in bytes", one("éa").Select(join(s.FindMany([]string{"a"}, false, false, false))), "s\n2\n"},
	} {
		if got := csvOf(t, tc.q); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
	for name, e := range map[string]ursus.Expr{
		"replacements of another count": s.ReplaceMany([]string{"a", "b"}, []string{"1", "2", "3"}, false, false),
		"overlapping and leftmost":      s.ExtractMany([]string{"a"}, false, true, true),
	} {
		if _, err := lf.Select(e).Collect(t.Context()); err == nil {
			t.Errorf("%s should be refused", name)
		}
	}
}

// TestStringsAsStructs: extract_groups, split_exact and splitn, a Struct each, and
// split keeping its separators. Every answer is Polars 1.44.1's.
func TestStringsAsStructs(t *testing.T) {
	c := ursus.Col
	s := c("s").Str()
	f := func(e ursus.Expr, fields ...string) []ursus.Expr {
		out := make([]ursus.Expr, len(fields))
		for i, n := range fields {
			out[i] = e.Struct().Field(n).Alias(n)
		}
		return out
	}
	kv := ursus.Frame(ursus.ValuesNullable("s", []string{"a=1", "b=22", "", "x"}, []bool{true, true, false, true}))
	parts := ursus.Frame(ursus.ValuesNullable("s", []string{"a,b,c", "x", "", "a,,b", ""}, []bool{true, true, false, true, true}))
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		// {k: a, v: 1}, {k: b, v: 22}, null fields, and no match null fields.
		{"extract_groups", kv.Select(f(s.ExtractGroups(`(?<k>\w)=(?<v>\d+)`), "k", "v")...), "k,v\na,1\nb,22\n,\n,\n"},
		{"numbered groups", kv.Select(f(s.ExtractGroups(`(\w)=(\d+)`), "1", "2")...), "1,2\na,1\nb,22\n,\n,\n"},
		{"split_exact", parts.Select(f(s.SplitExact(",", 1, false), "field_0", "field_1")...),
			"field_0,field_1\na,b\nx,\n,\na,\"\"\n\"\",\n"},
		{"split_exact, inclusive", parts.Select(f(s.SplitExact(",", 1, true), "field_0", "field_1")...),
			"field_0,field_1\n\"a,\",\"b,\"\nx,\n,\n\"a,\",\",\"\n,\n"},
		{"splitn", parts.Select(f(s.SplitN(",", 2), "field_0", "field_1")...),
			"field_0,field_1\na,\"b,c\"\nx,\n,\na,\",b\"\n\"\",\n"},
		{"splitn of one", one("a,b").Select(f(s.SplitN(",", 1), "field_0")...), "field_0\n\"a,b\"\n"},
		{"splitn past the parts", one("a,b").Select(f(s.SplitN(",", 3), "field_2")...), "field_2\n\n"},
		{"split, inclusive", parts.Select(s.SplitInclusive(",").List().Join("|")),
			"s\n\"a,|b,|c\"\nx\n\n\"a,|,|b\"\n\"\"\n"},
	} {
		if got := csvOf(t, tc.q); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
	if _, err := kv.Select(s.ExtractGroups(`\w=\d`)).Collect(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "no capture group") {
		t.Errorf("a pattern of no group: %v", err)
	}
	if _, err := kv.Select(s.SplitN(",", 0)).Collect(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "at least one part") {
		t.Errorf("splitn of none: %v", err)
	}
}

// TestTitlecaseAndNormalForms: Polars' to_titlecase, and the byte lengths of four
// normal forms of a ligature, a composed é and a decomposed one.
func TestTitlecaseAndNormalForms(t *testing.T) {
	c := ursus.Col
	s := c("s").Str()
	title := ursus.Frame(ursus.Values("s", []string{"hello wORLD", "o'neil-smith x", "ÉCOLE du monde",
		"3rd place  two", "hello_world foo.bar ABC-def ǆemal  x1y"}))
	if got, want := csvOf(t, title.Select(s.ToTitlecase())),
		"s\nHello World\nO'Neil-Smith X\nÉcole Du Monde\n3Rd Place  Two\nHello_World Foo.Bar Abc-Def Ǆemal  X1Y\n"; got != want {
		t.Errorf("titlecase:\ngot  %q\nwant %q", got, want)
	}
	forms := ursus.Frame(ursus.Values("s", []string{"ﬁ", "é", "e\u0301"}))
	n := func(f ursus.UnicodeForm) ursus.Expr { return s.Normalize(f).Str().LenBytes().Alias(string(f)) }
	if got, want := csvOf(t, forms.Select(n(ursus.NFKC), n(ursus.NFD), n(ursus.NFC), n(ursus.NFKD))),
		"NFKC,NFD,NFC,NFKD\n2,3,3,2\n2,3,2,3\n2,3,2,3\n"; got != want {
		t.Errorf("normal forms:\ngot  %q\nwant %q", got, want)
	}
}

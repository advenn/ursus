package ursus_test

// The strmany group (step 499, from strmany_test.go's hand tables): contains_any,
// replace_many, extract_many and find_many; extract_groups, split_exact, splitn and
// split_inclusive; to_titlecase and the normal forms, each spelled as
// bench/scripts/oracle/strmany.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	s := c("s").Str()
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	norm := func(f ursus.UnicodeForm) ursus.Expr { return s.Normalize(f).Str().LenBytes().Alias(string(f)) }
	pats := []string{"ab", "bc", "a", "AB"}
	oracleGroups["strmany"] = map[string]oracleQuery{
		"contains_any":              sel(s.ContainsAny([]string{"cat", "dog"}, false)),
		"contains_any_ascii_ci":     sel(s.ContainsAny([]string{"cat", "DOG"}, true)),
		"empty_pattern":             sel(s.ContainsAny([]string{""}, false)),
		"only_ascii_is_ci":          sel(s.ContainsAny([]string{"éCOLE"}, true)),
		"replace_many":              sel(s.ReplaceMany([]string{"cat", "dog"}, []string{"C", "D"}, false, false)),
		"one_replacement":           sel(s.ReplaceMany([]string{"cat", "dog"}, []string{"X"}, false, false)),
		"replace_many_ascii_ci":     sel(s.ReplaceMany([]string{"CAT"}, []string{"C"}, true, false)),
		"ends_together":             sel(s.ReplaceMany([]string{"bc", "abc"}, []string{"1", "2"}, false, false)),
		"ends_first":                sel(s.ReplaceMany([]string{"ab", "a"}, []string{"1", "2"}, false, false)),
		"leftmost_listed_first":     sel(s.ReplaceMany([]string{"ab", "a"}, []string{"1", "2"}, false, true)),
		"empty_pattern_between":     sel(s.ReplaceMany([]string{"", "a"}, []string{"-", "A"}, false, false)),
		"extract_many":              sel(s.ExtractMany([]string{"cat", "dog"}, false, false, false)),
		"overlapping":               sel(s.ExtractMany([]string{"aa"}, false, true, false)),
		"not_overlapping":           sel(s.ExtractMany([]string{"aa"}, false, false, false)),
		"overlapping_by_their_ends": sel(s.ExtractMany([]string{"bc", "abc", "b"}, false, true, false)),
		"leftmost":                  sel(s.ExtractMany([]string{"ab", "bc", "abc"}, false, false, true)),
		"find_many":                 sel(s.FindMany([]string{"cat", "dog"}, false, false, false)),
		"find_many_in_bytes":        sel(s.FindMany([]string{"a"}, false, false, false)),

		"extract_groups":        sel(s.ExtractGroups(`(?<k>\w)=(?<v>\d+)`)),
		"numbered_groups":       sel(s.ExtractGroups(`(\w)=(\d+)`)),
		"split_exact":           sel(s.SplitExact(",", 1, false)),
		"split_exact_inclusive": sel(s.SplitExact(",", 1, true)),
		"splitn":                sel(s.SplitN(",", 2)),
		"splitn_of_one":         sel(s.SplitN(",", 1)),
		"splitn_past_the_parts": sel(s.SplitN(",", 3)),
		"split_inclusive":       sel(s.SplitInclusive(",")),

		"titlecase":    sel(s.ToTitlecase()),
		"normal_forms": sel(norm(ursus.NFKC), norm(ursus.NFD), norm(ursus.NFC), norm(ursus.NFKD)),

		"many_gen": sel(s.ContainsAny(pats, false).Alias("any"), s.ContainsAny(pats, true).Alias("any_ci"),
			s.ReplaceMany(pats, []string{"1", "2", "3", "4"}, false, false).Alias("rep"),
			s.ReplaceMany(pats, []string{"1", "2", "3", "4"}, false, true).Alias("rep_left"),
			s.ReplaceMany(pats, []string{"-"}, true, false).Alias("rep_ci"),
			s.ExtractMany(pats, false, false, false).Alias("ext"), s.ExtractMany(pats, false, true, false).Alias("ext_over"),
			s.ExtractMany(pats, false, false, true).Alias("ext_left"), s.FindMany(pats, false, false, false).Alias("find"),
			s.FindMany(pats, true, true, false).Alias("find_over_ci")),
		"structs_gen": sel(s.SplitExact(",", 2, false).Alias("exact"), s.SplitExact(",", 1, true).Alias("incl"),
			s.SplitN(",", 2).Alias("n2"), s.SplitInclusive(",").Alias("parts")),
		"groups_gen":    sel(s.ExtractGroups(`(?<k>[ab]+)=(?<v>\d+)`).Alias("named"), s.ExtractGroups(`([ab])(\d)`).Alias("num")),
		"titlecase_gen": sel(s.ToTitlecase().Alias("t"), norm(ursus.NFC), norm(ursus.NFD), norm(ursus.NFKC), norm(ursus.NFKD)),
	}
}

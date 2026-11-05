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
// extract_many and find_many, their three ways of matching, and ASCII case-blind;
// extract_groups, split_exact and splitn as Structs; to_titlecase and the normal
// forms. Every answer is Polars 2.1.0's, recorded in the strmany group of
// testdata/oracle and spelled in oracle_strmany_test.go since step 499, which took
// this file's hand tables; what stays asserts the refusals.
func TestManyPatterns(t *testing.T) {
	s := ursus.Col("s").Str()
	lf := strManyFixture()
	for name, e := range map[string]ursus.Expr{
		"replacements of another count": s.ReplaceMany([]string{"a", "b"}, []string{"1", "2", "3"}, false, false),
		"overlapping and leftmost":      s.ExtractMany([]string{"a"}, false, true, true),
	} {
		if _, err := lf.Select(e).Collect(t.Context()); err == nil {
			t.Errorf("%s should be refused", name)
		}
	}
	// Polars 2.1 answers a Struct of no field to both; here each is refused.
	kv := ursus.Frame(ursus.Values("s", []string{"a=1"}))
	if _, err := kv.Select(s.ExtractGroups(`\w=\d`)).Collect(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "no capture group") {
		t.Errorf("a pattern of no group: %v", err)
	}
	if _, err := kv.Select(s.SplitN(",", 0)).Collect(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "at least one part") {
		t.Errorf("splitn of none: %v", err)
	}
}

package ursus_test

import (
	"errors"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// Name().StripPrefix and StripSuffix (step 491), Polars' name.strip_prefix and
// name.strip_suffix.

func TestNameStrip(t *testing.T) {
	c := ursus.Col
	names := func(t *testing.T, lf *ursus.LazyFrame) []string {
		t.Helper()
		s, err := lf.CollectSchema(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return s.Names()
	}
	frame := func(cols ...string) *ursus.LazyFrame {
		vals := make([]*ursus.Column, len(cols))
		for i, n := range cols {
			vals[i] = ursus.Values(n, []int64{int64(i)})
		}
		return ursus.Frame(vals...)
	}
	lf := frame("a_x", "a_", "b_x", "a_a_y", "xa_")

	// Polars 2.1.0 gives these: an affix is taken off once, where the name has it,
	// and an alias before the strip is what it strips, one after it wins.
	for _, tc := range []struct {
		name string
		lf   *ursus.LazyFrame
		want []string
	}{
		{"suffix of every column", lf.Select(ursus.All().Name().StripSuffix("_x")), []string{"a", "a_", "b", "a_a_y", "xa_"}},
		{"an empty prefix", lf.Select(ursus.All().Name().StripPrefix("")), []string{"a_x", "a_", "b_x", "a_a_y", "xa_"}},
		{"an empty suffix", lf.Select(ursus.All().Name().StripSuffix("")), []string{"a_x", "a_", "b_x", "a_a_y", "xa_"}},
		{"the whole name", lf.Select(c("a_").Name().StripPrefix("a_")), []string{""}},
		{"the whole name, as a suffix", lf.Select(c("a_").Name().StripSuffix("a_")), []string{""}},
		{"an alias, then stripped", lf.Select(c("b_x").Mul(2).Alias("a_q").Name().StripPrefix("a_")), []string{"q"}},
		{"stripped, then an alias", lf.Select(c("a_x").Name().StripPrefix("a_").Alias("z")), []string{"z"}},
		{"stripped twice", lf.Select(c("a_a_y").Name().StripPrefix("a_").Name().StripPrefix("a_")), []string{"y"}},
		{"stripped, then a prefix", lf.Select(c("a_x").Name().StripPrefix("a_").Name().Prefix("p_")), []string{"p_x"}},
		{"a prefix, then stripped", lf.Select(c("a_x").Name().Prefix("a_").Name().StripPrefix("a_")), []string{"a_x"}},
		{"an aggregate", lf.Select(c("a_x").Sum().Name().StripPrefix("a_")), []string{"x"}},
		{"bytes, not letters", frame("ä_x", "äx").Select(ursus.All().Name().StripPrefix("ä")), []string{"_x", "x"}},
		{"a key and an aggregate", lf.GroupBy(c("a_x").Name().StripPrefix("a_")).Agg(c("b_x").Name().StripSuffix("_x")),
			[]string{"x", "b"}},
	} {
		if got := names(t, tc.lf); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %q, Polars %q", tc.name, got, tc.want)
		}
	}
	// Polars 2.1.0 refuses two outputs of one name: "projections contained duplicate
	// output name 'x'".
	if _, err := lf.Select(c("a_x").Name().StripPrefix("a_"), c("b_x").Name().StripPrefix("b_")).
		CollectSchema(t.Context()); !errors.Is(err, ursus.ErrSchema) {
		t.Errorf("two columns stripped to x: %v, want a schema error", err)
	}

	// The oracle: a strip undoes Prefix and Suffix, and leaves a name without the
	// affix as it is, over generated names and affixes of runes that share bytes.
	rng := rand.New(rand.NewPCG(491, 1))
	runes := []string{"a", "_", "ä", "ä́", "b"}
	word := func(n int) string {
		var b strings.Builder
		for range n {
			b.WriteString(runes[rng.IntN(len(runes))])
		}
		return b.String()
	}
	seen := map[string]bool{}
	var cols []string
	for len(cols) < 200 {
		if w := word(1 + rng.IntN(6)); !seen[w] {
			seen[w] = true
			cols = append(cols, w)
		}
	}
	wide := frame(cols...)
	for range 40 {
		affix := word(rng.IntN(4))
		for _, side := range []struct {
			name       string
			add, strip func(ursus.NameExpr) ursus.Expr
			has        func(string, string) bool
		}{
			{"prefix", func(n ursus.NameExpr) ursus.Expr { return n.Prefix(affix) },
				func(n ursus.NameExpr) ursus.Expr { return n.StripPrefix(affix) }, strings.HasPrefix},
			{"suffix", func(n ursus.NameExpr) ursus.Expr { return n.Suffix(affix) },
				func(n ursus.NameExpr) ursus.Expr { return n.StripSuffix(affix) }, strings.HasSuffix},
		} {
			back := names(t, wide.Select(side.strip(side.add(ursus.All().Name()).Name())))
			if !slices.Equal(back, cols) {
				t.Fatalf("%s %q added and stripped: %q, want %q", side.name, affix, back, cols)
			}
			var without []string
			for _, n := range cols {
				if !side.has(n, affix) {
					without = append(without, n)
				}
			}
			if len(without) == 0 {
				continue
			}
			if kept := names(t, wide.Select(side.strip(c(without...).Name()))); !slices.Equal(kept, without) {
				t.Fatalf("%s %q stripped from names without it: %q, want %q", side.name, affix, kept, without)
			}
		}
	}
}

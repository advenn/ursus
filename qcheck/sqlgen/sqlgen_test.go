package sqlgen

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/gen"
	"github.com/advenn/ursus/qcheck/model"
)

// seeds is how many generated cases the local tests translate.
const seeds = 2000

// TestEveryQueryTranslates translates generated queries, bounded as the runner
// bounds them: each must translate, none be excluded by a refusal rule (the AST
// avoids every one), and the SQL must name every column of the root frame.
func TestEveryQueryTranslates(t *testing.T) {
	for seed := uint64(1); seed <= seeds; seed++ {
		c := gen.NewCase(seed)
		q, _ := ast.Bounded(c.Query)
		s, err := Translate(q, c.TableMap())
		var ex *Excluded
		if errors.As(err, &ex) && strings.HasPrefix(ex.Rule, "R-") {
			t.Fatalf("seed %d: excluded by a refusal rule the AST avoids: %v\n%s", seed, err, c)
		}
		if err != nil && !errors.As(err, &ex) {
			t.Fatalf("seed %d: %v\n%s", seed, err, c)
		}
		if err != nil {
			continue
		}
		if q.Count {
			continue
		}
		info, err := ast.Schema(q.Root, c.TableMap())
		if err != nil {
			t.Fatal(err)
		}
		last := s.Text[strings.LastIndex(s.Text, "\nSELECT "):]
		for _, col := range info.Cols {
			if !strings.Contains(last, Quote(col.Name)) {
				t.Fatalf("seed %d: the final SELECT lacks %q: %s", seed, col.Name, last)
			}
		}
		if len(s.Nodes) == 0 {
			t.Fatalf("seed %d: no nodes to count", seed)
		}
	}
}

// TestEveryRuleFires is the rules' anti-vacuity: over the generated queries every
// rule but the ones no query can reach fires, and only rules of the table do.
func TestEveryRuleFires(t *testing.T) {
	fired := map[string]int{}
	for seed := uint64(1); seed <= seeds; seed++ {
		c := gen.NewCase(seed)
		q, _ := ast.Bounded(c.Query)
		s, err := Translate(q, c.TableMap())
		if err != nil {
			continue
		}
		for id, n := range s.Fired {
			fired[id] += n
		}
	}
	known := map[string]bool{}
	for _, r := range Rules {
		known[r.ID] = true
		switch {
		case strings.HasPrefix(r.ID, "R-"):
			// A refusal fires only on a tree the AST never makes: its test is
			// TestRefusalsAreRefused. R-SELECT-LIT is no refusal, but gen's Select
			// always keeps a column; duck's TestTranslationRules builds one.
		case Unused(r.ID):
			if fired[r.ID] > 0 {
				t.Errorf("%s is listed unused and fired %d times", r.ID, fired[r.ID])
			}
		case fired[r.ID] == 0:
			t.Errorf("%s never fired in %d cases", r.ID, seeds)
		}
	}
	for id := range fired {
		if !known[id] {
			t.Errorf("%s fired and is not in Rules", id)
		}
	}
	t.Logf("fired over %d cases: %v", seeds, fired)
}

// TestRulesNameTheirSources holds each rule to a source that names a file of the
// repository that exists, or feature-gaps' section, and to one entry per ID.
func TestRulesNameTheirSources(t *testing.T) {
	root := filepath.Join("..", "..")
	file := regexp.MustCompile(`[a-z0-9_/]+\.go\b`)
	seen := map[string]bool{}
	for _, r := range Rules {
		if seen[r.ID] {
			t.Errorf("%s is listed twice", r.ID)
		}
		seen[r.ID] = true
		if r.Source == "" || r.What == "" {
			t.Errorf("%s has no source or no translation", r.ID)
			continue
		}
		names := file.FindAllString(r.Source, -1)
		if len(names) == 0 && !strings.Contains(r.Source, "feature-gaps") && !strings.Contains(r.Source, "§") &&
			!strings.Contains(r.Source, "dtype.") && !strings.Contains(r.Source, "ast.") && !Unused(r.ID) {
			t.Errorf("%s's source names no file, doc or section: %q", r.ID, r.Source)
		}
		for _, n := range names {
			if !exists(root, n) {
				t.Errorf("%s's source names %s, which is not in the repository", r.ID, n)
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(root, "context_files", "feature-gaps.md")); err != nil ||
		!strings.Contains(string(b), "## 9. Where ursus differs") {
		t.Errorf("feature-gaps.md §9, which rules cite, is not where they say: %v", err)
	}
}

// exists is whether a file of that name or path is in the repository's ursus code.
func exists(root, name string) bool {
	for _, dir := range []string{"", "internal", "internal/kernel", "internal/expr", "dtype"} {
		if _, err := os.Stat(filepath.Join(root, dir, name)); err == nil {
			return true
		}
	}
	return false
}

// TestRefusalsAreRefused builds trees the AST's constructors refuse, by hand, each
// of a shape a refusal rule names, and holds Translate to refusing each by that
// rule: where the AST avoids a refusal, SQL never sees it.
func TestRefusalsAreRefused(t *testing.T) {
	tab := gen.Table(7, "t0", 50)
	tables := map[string]*model.Table{tab.Name: tab}
	scan := &ast.Scan{Table: tab.Name}
	key := &ast.Col{Name: tab.Cols[0].Name, T: tab.Cols[0].Type}
	sum := &ast.Agg{Op: ast.Sum, X: key, T: model.Int128}
	grouped := &ast.GroupBy{In: scan, Keys: []ast.Named{{Name: key.Name, E: key}}, Aggs: []ast.Named{{Name: "s", E: sum}}}
	str := &ast.Lit{T: model.String, V: "x"}
	cases := []struct {
		rule string
		q    *ast.Query
	}{
		{RI128Arith, &ast.Query{Root: &ast.Select{In: grouped, Cols: []ast.Named{{Name: "x",
			E: &ast.Bin{Op: ast.Add, L: &ast.Col{Name: "s", T: model.Int128}, R: &ast.Lit{T: model.Int64, V: int64(1)}, T: model.Int128}}}}}},
		{RCastStrict, &ast.Query{Root: &ast.Select{In: scan, Cols: []ast.Named{{Name: "x",
			E: &ast.Cast{X: str, To: model.Int64}}}}}},
		{RPromote, &ast.Query{Root: &ast.Select{In: scan, Cols: []ast.Named{{Name: "x",
			E: &ast.When{Cond: &ast.Lit{T: model.Bool, V: true}, Then: str, Else: key, T: model.String}}}}}},
		{RMeanBool, &ast.Query{Root: &ast.GroupBy{In: scan, Aggs: []ast.Named{{Name: "m",
			E: &ast.Agg{Op: ast.Mean, X: &ast.Lit{T: model.Bool, V: true}, T: model.Float64}}}}}},
	}
	for _, c := range cases {
		b := &builder{tables: tables, fired: map[string]int{}, names: map[ast.Frame]string{}}
		var got *Excluded
		func() {
			defer func() {
				if v := recover(); v != nil {
					got, _ = v.(*Excluded)
				}
			}()
			b.frame(c.q.Root)
		}()
		if got == nil || got.Rule != c.rule {
			t.Errorf("a tree of %s's shape: refused by %v, want %s", c.rule, got, c.rule)
		}
	}
}

// TestLiteralsKeepTheirBits holds each float literal's text to the bits it came
// from, read back by strconv, DuckDB's reader being held to strconv by
// TestDuckDBSemantics; and text with a byte a query cannot hold to its hex.
func TestLiteralsKeepTheirBits(t *testing.T) {
	re := regexp.MustCompile(`^CAST\('([^']*)' AS DOUBLE\)$`)
	for seed := uint64(1); seed <= 300; seed++ {
		tab := gen.Table(seed, "t", 300)
		for j, c := range tab.Cols {
			if c.Type != model.Float64 {
				continue
			}
			col := tab.Rec.Column(j)
			for i := range col.Len() {
				if col.IsNull(i) {
					continue
				}
				f := col.(interface{ Value(int) float64 }).Value(i)
				m := re.FindStringSubmatch(Float(f))
				if m == nil {
					t.Fatalf("%v: %s", f, Float(f))
				}
				if !sameFloat(parse(m[1]), f) {
					t.Fatalf("%v written as %s", f, Float(f))
				}
			}
		}
	}
	for _, s := range []string{"a\x00b", "日本語", "it's"} {
		lit := String(s)
		if strings.ContainsRune(lit, 0) {
			t.Errorf("%q written with a NUL: %s", s, lit)
		}
	}
}

func parse(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		panic(err)
	}
	return f
}

func sameFloat(a, b float64) bool {
	return math.Float64bits(a) == math.Float64bits(b) || math.IsNaN(a) && math.IsNaN(b)
}

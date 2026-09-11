package ursus_test

// Instruments that FAIL rather than going quiet.
//
// Every guard in this repository is a list — a list of pushdown arms, a list of
// plans, a list of golden files — and every one was written against the node set
// that existed the day it was written. A list does not fail when the code grows past
// it; it goes SILENT, and silence reads exactly like green. That is how Explode and
// Unnest went from steps 30 and 35 to step 48 with no projection-pushdown arm: every
// list simply did not mention them.
//
// The two fixes that have broken that pattern are the template for this file:
// Resolve's default arm, which enforces the function's own documented postcondition
// and so covers "every node added later"; and TestPushdownSoundness's anti-vacuity
// counter, which fails when no assertion ran. Total by construction, or loud when it
// stops checking.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/ursustest"
)

// repoFiles walks the module, calling fn for every .go file outside .git and bench.
func repoFiles(t *testing.T, fn func(path string, src []byte)) {
	t.Helper()
	root := ".."
	if _, err := os.Stat("go.mod"); err == nil {
		root = "."
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "bench", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fn(path, src)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

var citedTest = regexp.MustCompile(`\bTest[A-Z][A-Za-z0-9_]*`)

// TestEveryCitedTestExists closes the phantom-instrument class.
//
// eval.go's doc claimed "TestEvaluatorContract asserts it across the whole op ×
// dtype matrix" and no such test existed anywhere in the repository — the contract
// most likely to be violated, guarded by a sentence. It is the third instance of the
// shape: step 13 found a join test cited only by the comment naming it, and step 2
// found rejectAggregate documented as guarding Select and never called from it.
//
// This is total by construction: it reads every comment in the module rather than a
// list of the ones someone remembered.
func TestEveryCitedTestExists(t *testing.T) {
	defined := map[string]string{}
	cited := map[string]string{}
	wrapped := map[string]string{}

	repoFiles(t, func(path string, src []byte) {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
		if err != nil {
			return // not our business here; the build catches it
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
				defined[fn.Name.Name] = path
			}
		}
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				for _, loc := range citedTest.FindAllStringIndex(c.Text, -1) {
					name := c.Text[loc[0]:loc[1]]
					// A name touching the end of the line may have been WRAPPED:
					// gofmt splits a long identifier across two comment lines, and
					// reporting the first half of one is a false
					// alarm. An instrument that cries wolf gets switched off, so
					// these are recorded separately and satisfied by any defined
					// name they prefix.
					if loc[1] == len(strings.TrimRight(c.Text, " \t")) {
						if _, seen := wrapped[name]; !seen {
							wrapped[name] = path
						}
						continue
					}
					if _, seen := cited[name]; !seen {
						cited[name] = path
					}
				}
			}
		}
	})

	if len(defined) < 100 {
		t.Fatalf("found only %d test functions — the walk is not reaching the "+
			"repository, so this check has gone vacuous", len(defined))
	}

	var missing []string
	for name, where := range cited {
		// TestMain is a language convention, not a citation: a comment saying "set
		// from a TestMain" names the mechanism rather than a specific function.
		if name == "TestMain" {
			continue
		}
		if _, ok := defined[name]; !ok {
			missing = append(missing, name+" (cited in "+where+")")
		}
	}
	for name, where := range wrapped {
		if _, ok := defined[name]; ok {
			continue
		}
		var found bool
		for d := range defined {
			if strings.HasPrefix(d, name) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, name+"... (line-wrapped, cited in "+where+")")
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("a comment cites a test that does not exist: %s", m)
	}
}

// planNodeTypes parses internal/plan for every type carrying the planNode() marker.
//
// This is the half that cannot go quiet. A hand-written list of node types is
// exactly the instrument that failed for Explode and Unnest: they were added at
// steps 30 and 35, every list simply did not mention them, and the gap survived
// eighteen steps. Parsing the marker method means a twenty-third node is required
// the day it is declared.
//
// _test.go is skipped, because optimize_internal_test.go declares a stubNode that
// would otherwise demand coverage it can never have.
func planNodeTypes(t *testing.T) map[string]string {
	t.Helper()
	dir := filepath.Join(repoRoot(t), "internal", "plan")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	out := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "planNode" || fn.Recv == nil ||
				len(fn.Recv.List) == 0 {
				continue
			}
			if id := receiverIdent(fn.Recv.List[0].Type); id != "" {
				out[id] = name
			}
		}
	}
	return out
}

// receiverIdent unwraps a receiver expression to its base type name, through the
// pointer and through any type parameters.
//
// The generic case is not hypothetical: internal/kernel has an extremumNum[T] whose
// receiver is StarExpr -> IndexExpr -> Ident, and an extractor that handles only
// StarExpr -> Ident drops it SILENTLY — which is the exact failure this file exists
// to prevent, so it is worth handling here even though plan has no generic nodes
// today.
func receiverIdent(e ast.Expr) string {
	for {
		switch t := e.(type) {
		case *ast.StarExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		case *ast.Ident:
			return t.Name
		default:
			return ""
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	if _, err := os.Stat("go.mod"); err == nil {
		return "."
	}
	return ".."
}

// planCoverage is the query table. Each entry names the node types it is there to
// cover, and every entry is walked; most also pin a golden.
//
// Unnest is the one entry with no golden, and the reason is worth stating: no
// public API constructs a Struct column — only Parquet does — and ScanParquet's
// plan label embeds the file path, which under t.TempDir() changes every run. So
// the TYPE is covered by the walk while its plan text is not pinned. That is one
// named exemption carrying its justification, rather than a silent gap.
var planCoverage = []struct {
	name   string
	golden string
	build  func(t *testing.T) *ursus.LazyFrame
}{
	{"slice", "testdata/plans/slice.txt", func(t *testing.T) *ursus.LazyFrame {
		return frame().Slice(1, 2)
	}},
	{"hstack", "testdata/plans/hstack.txt", func(t *testing.T) *ursus.LazyFrame {
		return ursus.Frame(ursus.Values("k", []int64{1, 2})).
			HStack(ursus.Frame(ursus.Values("hv", []string{"x", "y"})))
	}},
	{"merge sorted", "testdata/plans/merge_sorted.txt", func(t *testing.T) *ursus.LazyFrame {
		a := ursus.Frame(ursus.Values("k", []int64{1, 3}), ursus.Values("v", []int64{10, 30}))
		b := ursus.Frame(ursus.Values("k", []int64{2, 4}), ursus.Values("v", []int64{20, 40}))
		return a.MergeSorted(b, "k")
	}},
	{"asof join", "testdata/plans/asof_join.txt", func(t *testing.T) *ursus.LazyFrame {
		l := ursus.Frame(ursus.Values("k", []int64{10, 20}), ursus.Values("lv", []int64{1, 2}))
		r := ursus.Frame(ursus.Values("k", []int64{8, 20}), ursus.Values("rv", []int64{8, 9}))
		return l.JoinAsOf(r, ursus.AsOfOn(ursus.Col("k")))
	}},
	{"explode", "testdata/plans/explode.txt", func(t *testing.T) *ursus.LazyFrame {
		return ursus.Frame(
			ursus.Values("id", []int64{1, 2}),
			ursus.Values("tags", []string{"a,b", "c"}),
		).WithColumns(ursus.Col("tags").Str().Split(",").Alias("t")).Explode("t")
	}},
	{"group by dynamic", "testdata/plans/group_by_dynamic.txt", func(t *testing.T) *ursus.LazyFrame {
		return hourly(t, "2024-01-01T00:10:00", "2024-01-01T01:30:00").
			GroupByDynamic(ursus.Col("ts"), ursus.DynamicOptions{Every: ursus.Every("1h")}).
			Agg(ursus.Len().Alias("n"))
	}},
	{"rolling", "testdata/plans/rolling.txt", func(t *testing.T) *ursus.LazyFrame {
		return hourly(t, "2024-01-01T00:10:00", "2024-01-01T01:30:00").
			Rolling(ursus.Col("ts"), ursus.RollingOptions{Period: ursus.Every("1h")}).
			Agg(ursus.Len().Alias("n"))
	}},
	{"unnest (walk only)", "", func(t *testing.T) *ursus.LazyFrame {
		return ursus.ScanParquet(structFixture(t)).Unnest("person")
	}},

	// Walk-only from here down: each of these node types is already pinned by a
	// golden belonging to the test that owns it — skeleton, conditional, topk,
	// join_left, concat, unpivot, window and the push_* family. Repeating those
	// goldens here would create a second copy to keep in step. What these entries
	// add is the INVENTORY: the type is reached by a checked-in query, so the day
	// someone deletes the test that owns it, this one still fails.
	{"scan, project, filter", "", func(t *testing.T) *ursus.LazyFrame {
		return frame().Filter(ursus.Col("qty").Gt(1)).Select(ursus.Col("id"))
	}},
	{"limit and tail", "", func(t *testing.T) *ursus.LazyFrame {
		return frame().Head(3).Tail(2)
	}},
	{"sort and reverse", "", func(t *testing.T) *ursus.LazyFrame {
		return frame().Sort(ursus.Asc(ursus.Col("qty"))).Reverse()
	}},
	{"row index", "", func(t *testing.T) *ursus.LazyFrame {
		return frame().WithRowIndex("i", 0)
	}},
	{"with columns and aggregate", "", func(t *testing.T) *ursus.LazyFrame {
		return frame().WithColumns(ursus.Col("qty").Mul(2).Alias("q2")).
			GroupBy(ursus.Col("region")).Agg(ursus.Col("q2").Sum().Alias("s"))
	}},
	{"distinct", "", func(t *testing.T) *ursus.LazyFrame {
		return frame().Unique("region")
	}},
	{"join", "", func(t *testing.T) *ursus.LazyFrame {
		r := ursus.Frame(ursus.Values("id", []int64{1, 2}), ursus.Values("tag", []string{"a", "b"}))
		return frame().Join(r, ursus.JoinOn(ursus.Col("id")))
	}},
	{"union", "", func(t *testing.T) *ursus.LazyFrame {
		return ursus.Concat([]*ursus.LazyFrame{frame(), frame()})
	}},
	{"unpivot", "", func(t *testing.T) *ursus.LazyFrame {
		return frame().Unpivot(ursus.UnpivotOptions{On: []string{"qty", "price"}})
	}},
	{"window", "", func(t *testing.T) *ursus.LazyFrame {
		return frame().Select(ursus.Col("qty").Sum().Over(ursus.Col("region")).Alias("s"))
	}},
}

// TestEveryPlanNodeIsCovered is the golden-plan inventory.
//
// It compares the node types DECLARED in internal/plan against the node types
// actually reached by a checked-in query, walking the optimized plan and matching by
// reflect.Type rather than by label.
//
// Matching on labels was the obvious design and is wrong three ways, each verified:
// "MERGE SORTED [" contains "SORT", so a MergeSorted golden would falsely satisfy
// Sort; Distinct emits either "DISTINCT [*]" or "DISTINCT [cols]"; Scan emits
// MEMORY, CSV or PARQUET depending on its source; and TemporalGroup emits ROLLING or
// GROUP_BY_DYNAMIC from one type. Reflection has none of those problems.
func TestEveryPlanNodeIsCovered(t *testing.T) {
	required := planNodeTypes(t)
	if len(required) < 20 {
		t.Fatalf("found only %d plan node types — the parse is not reaching "+
			"internal/plan, so this check has gone vacuous", len(required))
	}

	covered := map[string]bool{}
	for _, c := range planCoverage {
		t.Run(c.name, func(t *testing.T) {
			lf := c.build(t)
			if c.golden != "" {
				ursustest.AssertPlan(t, lf, c.golden)
			}

			n, err := plan.Resolve(t.Context(), lf.Plan())
			if err != nil {
				t.Fatal(err)
			}
			n, err = plan.NewOptimizer().Run(n, plan.DefaultFlags())
			if err != nil {
				t.Fatal(err)
			}
			var seen int
			plan.Walk(n, func(x plan.Node) bool {
				covered[strings.TrimPrefix(reflect.TypeOf(x).String(), "*plan.")] = true
				seen++
				return true
			})
			if seen == 0 {
				t.Errorf("%s walked no nodes — the case has gone vacuous", c.name)
			}
		})
	}

	var missing []string
	for name, file := range required {
		if !covered[name] {
			missing = append(missing, name+" (declared in internal/plan/"+file+")")
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("no checked-in query produces plan node %s — nothing exercises how "+
			"it resolves or optimizes, which is how Explode and Unnest went eighteen "+
			"steps with no projection-pushdown arm", m)
	}

	// The reverse direction: something walked that the parse did not find means the
	// enumeration missed a declaration, which would make the whole check unsound.
	for name := range covered {
		if _, ok := required[name]; !ok {
			t.Errorf("walked a %s, which planNodeTypes did not find — the marker "+
				"parse is incomplete", name)
		}
	}
}

package physical

// Every Sink.Merge is either a refusal or is tested. Enumerated, not listed.
//
// reverseSink.Merge went from step 9 to step 50 with no test of any kind — the whole
// type, not just the method — and when one was finally written the Merge turned out
// to be WRONG, not merely unverified: it prepended where it must append, so two
// sinks over [1,2] and [3,4] reversed to [2,1,4,3] instead of [4,3,2,1].
//
// It survived because nothing calls it. hashAggSink is the only concrete Sink whose
// Merge can execute in production: parallelSink has exactly one production
// constructor call, inside aggBreaker. So "is it reachable?" is the wrong question —
// six of the seven are not, and the one that was wrong would have shipped as
// silently wrong rows the day the parallel driver was pointed at a second sink type.
//
// The right question is "does anything exercise it?", and that is what this asserts.
// Static reachability is deliberately NOT attempted: both production `.Merge(` call
// sites are interface dispatches, so a grep proves nothing without whole-program
// type flow.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"
)

// mergeImpl is one Merge method found in this package's source.
type mergeImpl struct {
	recv    string
	file    string
	refusal bool
}

// findMergeImpls parses this package's own source. `go test` runs with cwd set to
// the package directory, so "." is the source tree.
func findMergeImpls(t *testing.T) []mergeImpl {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	var out []mergeImpl
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "Merge" || fn.Recv == nil || len(fn.Recv.List) == 0 {
				continue
			}
			// Match on the PARAMETER TYPE, not its name: asOfBuildSink and
			// temporalSink both declare `Merge(Sink) error` with no identifier, and
			// a name-based matcher drops them silently.
			if len(fn.Type.Params.List) != 1 {
				continue
			}
			if id, ok := fn.Type.Params.List[0].Type.(*ast.Ident); !ok || id.Name != "Sink" {
				continue
			}
			recv := mergeRecvName(fn.Recv.List[0].Type)
			if recv == "" {
				t.Errorf("%s: could not read the receiver of a Merge method — the "+
					"extractor is incomplete, which is the failure this test exists "+
					"to prevent", name)
				continue
			}
			out = append(out, mergeImpl{recv: recv, file: name, refusal: isRefusal(fn)})
		}
	}
	return out
}

// mergeRecvName unwraps a receiver to its base type name, through the pointer and
// through any type parameters. The generic case is live one package over:
// kernel's extremumNum[T] has receiver StarExpr -> IndexExpr -> Ident.
func mergeRecvName(e ast.Expr) string {
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

// isRefusal reports whether a Merge only ever returns an error.
//
// A refusal is exempt from needing a test, because there is nothing to get wrong.
// The exemption is deliberately FRAGILE: the moment a real statement appears — an
// append, an assignment, a range — this returns false and the type needs a test.
// A refusal that quietly becomes an implementation is exactly how reverseSink would
// have escaped again.
func isRefusal(fn *ast.FuncDecl) bool {
	real := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.AssignStmt, *ast.RangeStmt, *ast.ForStmt, *ast.IncDecStmt:
			real = true
		}
		return !real
	})
	return !real
}

// TestEverySinkMergeIsExercised is the inventory.
func TestEverySinkMergeIsExercised(t *testing.T) {
	impls := findMergeImpls(t)
	if len(impls) < 7 {
		t.Fatalf("found only %d Sink.Merge implementations — the parse is not "+
			"reaching this package, so the check has gone vacuous", len(impls))
	}

	// "Exercised" means: some _test.go in this package names the type AND calls
	// Merge. Matching per FILE rather than against one concatenated haystack is
	// what makes that conjunction mean anything — otherwise a type named in one
	// file and a Merge call in a different one would satisfy each other.
	//
	// It is a proxy, and a deliberately loose one. It cannot tell a thorough
	// equivalence test from a token mention. What it CAN tell is the case that
	// actually happened: a Merge no test file mentions at all.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	exercised := map[string]bool{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		// THIS file is excluded, and the exclusion is load-bearing. Its own
		// refusal test names every sink type and calls Merge, so without it the
		// inventory certifies each type by merely having listed it — which it
		// did, silently, until the tooth for this check came back green.
		if e.Name() == "mergeinventory_test.go" {
			continue
		}
		src, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		text := string(src)
		if !strings.Contains(text, ".Merge(") {
			continue
		}
		for _, m := range impls {
			if strings.Contains(text, m.recv) {
				exercised[m.recv] = true
			}
		}
	}

	var untested []string
	for _, m := range impls {
		if m.refusal || exercised[m.recv] {
			continue
		}
		untested = append(untested, m.recv+" ("+m.file+")")
	}
	sort.Strings(untested)
	for _, u := range untested {
		t.Errorf("%s has a real Merge and no test naming it — that is the "+
			"configuration reverseSink.Merge was wrong in for thirty-four steps", u)
	}
}

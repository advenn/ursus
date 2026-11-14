// Command fuzzlist names every fuzz target of the module (step 508): each
// `func FuzzX(f *testing.F)` of a package's test files, the files `go list` builds
// under the environment's GOEXPERIMENT and build tags, and nothing under bench/.
//
//	go run ./internal/gen/fuzzlist         # "package target" lines: make fuzz-list
//	go run ./internal/gen/fuzzlist -json   # fuzz.yml's matrix
//
// The JSON is a list of {"pkg", "target", "id"}: pkg as `go test` takes it, and id,
// the two joined by dashes (the root package's "root"), to name an artifact or a
// cache: two packages may each have a FuzzParse.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

type target struct {
	Pkg    string `json:"pkg"`
	Target string `json:"target"`
	ID     string `json:"id"`
}

func main() {
	asJSON := flag.Bool("json", false, "print fuzz.yml's matrix, a JSON list")
	flag.Parse()

	targets, err := list()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fuzzlist: %v\n", err)
		os.Exit(1)
	}
	if *asJSON {
		out, err := json.Marshal(targets)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fuzzlist: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(out))
		return
	}
	for _, t := range targets {
		fmt.Println(t.Pkg, t.Target)
	}
}

func list() ([]target, error) {
	root, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	out, err := exec.Command("go", "list", "-f",
		`{{.Dir}}{{range .TestGoFiles}} {{.}}{{end}}{{range .XTestGoFiles}} {{.}}{{end}}`, "./...").Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w", err)
	}
	var targets []target
	fset := token.NewFileSet()
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		dir, files, _ := strings.Cut(line, " ")
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			return nil, err
		}
		rel = filepath.ToSlash(rel)
		if rel == "bench" || strings.HasPrefix(rel, "bench/") {
			continue
		}
		pkg, stem := "./"+rel, strings.ReplaceAll(rel, "/", "-")
		if rel == "." {
			pkg, stem = ".", "root"
		}
		for _, name := range strings.Fields(files) {
			f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
			if err != nil {
				return nil, err
			}
			for _, d := range f.Decls {
				if fn, ok := d.(*ast.FuncDecl); ok && isFuzz(fn) {
					targets = append(targets, target{Pkg: pkg, Target: fn.Name.Name, ID: stem + "-" + fn.Name.Name})
				}
			}
		}
	}
	slices.SortFunc(targets, func(a, b target) int { return strings.Compare(a.ID, b.ID) })
	return targets, nil
}

// isFuzz is whether fn is a fuzz target: a function named Fuzz… taking one
// *testing.F.
func isFuzz(fn *ast.FuncDecl) bool {
	if fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Fuzz") || fn.Type.Params.NumFields() != 1 {
		return false
	}
	star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "F" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "testing"
}

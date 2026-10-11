package ursus_test

// Step 186: no exported function, method or struct field of package ursus names a
// type from one of the module's internal packages. A caller outside the module
// cannot name such a type, so an API that takes or returns one cannot be used, as
// Scan(plan.Source), FromPlan, LazyFrame.Plan, DataFrame.Batch and
// WithOptFlags(plan.Flags) could not, until this step. A public alias, Column for
// data.Column, is ursus's own name and passes; so does an option type defined over an
// internal one, which a caller only ever builds through ursus's constructors.
//
// It reads the package's source, not its types: fast, and enough, since an internal
// type can only be named through its package's import.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestThePublicAPINamesNoInternalType(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var checked, found int
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		internal := map[string]bool{} // the file's names for internal packages
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if !strings.Contains(path, "/internal/") {
				continue
			}
			local := path[strings.LastIndex(path, "/")+1:]
			if imp.Name != nil {
				local = imp.Name.Name
			}
			internal[local] = true
		}
		names := func(where string, n ast.Node) {
			ast.Inspect(n, func(x ast.Node) bool {
				sel, ok := x.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); ok && internal[id.Name] {
					found++
					t.Errorf("%s: %s names %s.%s, a type in an internal package",
						fset.Position(sel.Pos()), where, id.Name, sel.Sel.Name)
				}
				return true
			})
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if !d.Name.IsExported() || (d.Recv != nil && !exportedReceiver(d.Recv)) {
					continue
				}
				checked++
				names(d.Name.Name, d.Type)
			case *ast.GenDecl:
				for _, s := range d.Specs {
					ts, ok := s.(*ast.TypeSpec)
					if !ok || !ts.Name.IsExported() || ts.Assign.IsValid() {
						continue // an alias is ursus's own name for the type
					}
					st, ok := ts.Type.(*ast.StructType)
					if !ok {
						continue // an option type over an internal one is built by ursus
					}
					for _, fld := range st.Fields.List {
						for _, n := range fld.Names {
							if n.IsExported() {
								checked++
								names(ts.Name.Name+"."+n.Name, fld.Type)
							}
						}
					}
				}
			}
		}
	}
	if checked < 400 {
		t.Fatalf("only %d exported functions and fields read; the walk has gone vacuous", checked)
	}
}

// exportedReceiver reports whether a method's receiver is an exported type.
func exportedReceiver(r *ast.FieldList) bool {
	if r == nil || len(r.List) == 0 {
		return false
	}
	e := r.List[0].Type
	if st, ok := e.(*ast.StarExpr); ok {
		e = st.X
	}
	switch x := e.(type) {
	case *ast.IndexExpr:
		e = x.X
	case *ast.IndexListExpr:
		e = x.X
	}
	id, ok := e.(*ast.Ident)
	return ok && id.IsExported()
}

package main

import (
	"go/ast"
	"go/token"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// A hit is one counted line: a source line of a test that holds a hand-typed
// expected value.
type hit struct {
	file string // slash-separated, relative to the module's root
	fn   string // the top-level function the line is in
	line int
	lit  string // the first literal counted on the line, as written
	rule string // which expected position it is in
}

// pkgInfo is what a package's test files declare that a composite literal or a call
// in another of its files needs: the fields of each struct type, in order, and the
// parameters of each function.
type pkgInfo struct {
	structs map[string][]string   // a struct type's field names in order, by its name
	helpers map[string]helperInfo // a test file's function, by its name
}

// helperInfo is a test helper's parameters: which take a want, and which take data
// at all rather than a size, a flag, a name or a function.
type helperInfo struct {
	params   []param
	refusal  bool // it takes an error or its name says it asserts structure: no want is data
	computes bool // its name says it is a Go oracle: its arguments are its question
	reads    bool // its body reads a frame, a column or a batch, or collects a query
}

type param struct {
	name     string
	typ      ast.Expr
	variadic bool
}

// at returns the parameter an argument binds to.
func (h helperInfo) at(i int) (param, bool) {
	switch {
	case i < len(h.params):
		return h.params[i], true
	case len(h.params) > 0 && h.params[len(h.params)-1].variadic:
		return h.params[len(h.params)-1], true
	}
	return param{}, false
}

// takesWant reports whether a helper asserts its argument at i as expected data.
func (h helperInfo) takesWant(i int) bool {
	p, ok := h.at(i)
	return ok && !h.refusal && isWantName(p.name) && !structuralWantName(p.name)
}

func (h helperInfo) anyWant() bool {
	for i := range h.params {
		if h.takesWant(i) {
			return true
		}
	}
	return false
}

// nameParams are the parameter names a string argument of which is a name or a
// place, not data.
var nameParams = map[string]bool{
	"name": true, "names": true, "col": true, "column": true, "colName": true, "key": true, "field": true,
	"path": true, "dir": true, "file": true, "label": true, "what": true, "title": true, "kind": true,
	"op": true, "zone": true, "tz": true, "layout": true, "format": true, "dtype": true, "typ": true,
}

// takesData reports whether an argument at i of a constructor-like helper is data:
// a want; a variadic's values; a slice, an any, a frame or a column; a string not a
// name. Not a size, a seed or a flag (a lone integer, float or Boolean), a name, a
// function, a context or a test.
func (h helperInfo) takesData(i int) bool {
	if h.computes {
		return false
	}
	p, ok := h.at(i)
	if !ok {
		return true
	}
	if isWantName(p.name) {
		return !structuralWantName(p.name)
	}
	if p.variadic {
		return true
	}
	switch t := p.typ.(type) {
	case *ast.Ident:
		switch t.Name {
		case "string":
			return !nameParams[p.name]
		case "any":
			return true
		}
		return !basicTypes[t.Name] && t.Name != "error"
	case *ast.FuncType:
		return false
	case *ast.SelectorExpr:
		if id, ok := t.X.(*ast.Ident); ok && (id.Name == "testing" || id.Name == "context" || id.Name == "time") {
			return false
		}
	case *ast.StarExpr:
		if sel, ok := t.X.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "testing" {
				return false
			}
		}
	}
	return true
}

// oracleHelper reports whether a function's name says it computes an answer itself,
// a Go oracle: naiveQuantile, modelArgSort, bruteJoin.
func oracleHelper(name string) bool {
	l := strings.ToLower(name)
	for _, w := range []string{"naive", "model", "brute", "oracle", "reference", "simulate", "compute", "expected"} {
		if strings.HasPrefix(l, w) {
			return true
		}
	}
	// goParsedTicks, goSum: Go's own answer.
	return len(name) > 2 && strings.HasPrefix(name, "go") && unicode.IsUpper(rune(name[2]))
}

func newPkgInfo() *pkgInfo {
	return &pkgInfo{structs: map[string][]string{}, helpers: map[string]helperInfo{}}
}

// learn records f's struct types and want-taking helpers in p.
func (p *pkgInfo) learn(f *ast.File) {
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.TypeSpec:
			if st, ok := n.Type.(*ast.StructType); ok {
				if _, seen := p.structs[n.Name.Name]; !seen {
					p.structs[n.Name.Name] = fieldNames(st)
				}
			}
		case *ast.FuncDecl:
			if n.Recv != nil || strings.HasPrefix(n.Name.Name, "Test") {
				return true
			}
			if _, seen := p.helpers[n.Name.Name]; !seen {
				p.helpers[n.Name.Name] = helperOf(n.Name.Name, n.Type, n.Body)
			}
		}
		return true
	})
}

// helperOf returns a function's parameters. A helper taking an error, or named as
// asserting an error, a plan, a type or a resource, asserts structure: its wants are
// phrases, and none is counted.
func helperOf(name string, ft *ast.FuncType, body *ast.BlockStmt) helperInfo {
	h := helperInfo{refusal: structuralHelper(name), computes: oracleHelper(name),
		reads: bodyReads(body) && !countsSomething(name, ft)}
	for _, fld := range ft.Params.List {
		typ, variadic := fld.Type, false
		if el, ok := typ.(*ast.Ellipsis); ok {
			typ, variadic = el.Elt, true
		}
		if id, ok := typ.(*ast.Ident); ok && id.Name == "error" {
			h.refusal = true
		}
		if len(fld.Names) == 0 {
			h.params = append(h.params, param{typ: typ, variadic: variadic})
		}
		for _, nm := range fld.Names {
			h.params = append(h.params, param{name: nm.Name, typ: typ, variadic: variadic})
		}
	}
	return h
}

func fieldNames(st *ast.StructType) []string {
	var out []string
	for _, f := range st.Fields.List {
		if len(f.Names) == 0 {
			out = append(out, "") // embedded
			continue
		}
		for _, n := range f.Names {
			out = append(out, n.Name)
		}
	}
	return out
}

// isWantName reports whether an identifier names an expected value: want…, expect…,
// golden…, exp, or exp followed by a capital, a digit or an underscore (expX, exp2);
// not expr or explain.
func isWantName(s string) bool {
	switch {
	case strings.HasPrefix(s, "want"), strings.HasPrefix(s, "expect"), strings.HasPrefix(s, "golden"),
		strings.HasPrefix(s, "Want"), strings.HasPrefix(s, "Expect"):
		return true
	case s == "exp":
		return true
	case strings.HasPrefix(s, "exp") && len(s) > 3:
		r := rune(s[3])
		return unicode.IsUpper(r) || unicode.IsDigit(r) || r == '_'
	}
	return false
}

// structuralWantWords begin a want's name, after the want, where it expects an error,
// a plan, a type, a shape, an order or a mechanism rather than a data value:
// wantErr, wantPlan, wantType, wantHeight, wantOp.
var structuralWantWords = []string{
	"Err", "Panic", "Refus", "Reject", "Plan", "Msg", "Message", "Phrase", "Sub", "Type", "Dtype", "DType",
	"Kind", "Schema", "Height", "Width", "Len", "Size", "Count", "Spill", "Held", "Alloc", "Mem", "Log",
	"OK", "Ok", "Op", "ID", "Name", "Node", "Order", "Cols", "Columns", "Calls", "Opens", "Files", "Path",
	"Dir", "Batches", "Threads", "Workers", "Parts", "Chunks", "Goroutines", "Recovered", "Unopt", "Opt",
	"Pruned", "Pushed", "Contains", "Fail", "Refs", "Null", "Nullable", "Shape", "Kept", "Dropped",
}

// structuralWantName reports whether a want's name says it expects structure.
func structuralWantName(s string) bool {
	rest := s
	for _, p := range []string{"wanted", "want", "Want", "expected", "expect", "Expect", "golden", "exp"} {
		if r, ok := strings.CutPrefix(s, p); ok {
			rest = r
			break
		}
	}
	for _, w := range structuralWantWords {
		if tail, ok := strings.CutPrefix(rest, w); ok {
			// A word ends at a capital, a digit or the name's end: wantOpt, not wantOptional.
			if tail == "" || tail == "s" || unicode.IsUpper(rune(tail[0])) || unicode.IsDigit(rune(tail[0])) {
				return true
			}
		}
	}
	return false
}

// structuralHelper reports whether a helper's name says it asserts an error, a plan,
// a type or a resource rather than data.
func structuralHelper(name string) bool {
	l := strings.ToLower(name)
	for _, w := range []string{"err", "refus", "reject", "panic", "plan", "schema", "dtype", "type", "kind",
		"goroutine", "interned", "spill", "mem", "alloc", "contains", "saying", "phrase", "explain"} {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}

// structuralFunc reports whether a test function is left out whole: a refusal's test,
// whose literals are error phrases and the inputs refused.
func structuralFunc(name string) bool {
	return strings.Contains(name, "Refus") || strings.Contains(name, "Reject")
}

// fileScan is the walk of one file.
type fileScan struct {
	fset    *token.FileSet
	rel     string
	pkg     *pkgInfo
	imports map[string]string // the file's name for each package it imports, to its path
}

func importsOf(f *ast.File) map[string]string {
	m := map[string]string{}
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		name := path[strings.LastIndex(path, "/")+1:]
		if len(name) >= 2 && name[0] == 'v' && unicode.IsDigit(rune(name[1])) && strings.Contains(path, "/") {
			p := strings.TrimSuffix(path, "/"+name) // math/rand/v2 is rand
			name = p[strings.LastIndex(p, "/")+1:]
		}
		if imp.Name != nil {
			name = imp.Name.Name
		}
		m[name] = path
	}
	return m
}

// scanFile counts f's hand-typed expected-data lines, a Test or Fuzz function at a
// time.
func scanFile(fset *token.FileSet, rel string, f *ast.File, pkg *pkgInfo) []hit {
	s := &fileScan{fset: fset, rel: rel, pkg: pkg, imports: importsOf(f)}
	var hits []hit
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil || fd.Recv != nil {
			continue
		}
		name := fd.Name.Name
		if !strings.HasPrefix(name, "Test") && !strings.HasPrefix(name, "Fuzz") || name == "TestMain" {
			continue
		}
		if structuralFunc(name) {
			continue
		}
		fn := newFuncScan(s, name, fd.Body)
		fn.run()
		if fn.errorsOnly() {
			continue
		}
		hits = append(hits, fn.lines()...)
	}
	return hits
}

// A binding is a want: a variable, or a struct field of a case table.
type binding struct {
	name  string
	field bool
	table *ast.CompositeLit // a field's table, nil for a row outside one
	loop  *ast.RangeStmt    // a want ranged over: its uses are the loop's
}

// assign is a value assigned to a variable.
type assign struct {
	pos token.Pos
	rhs ast.Expr // nil where the variable is a range's key
	of  ast.Expr // the ranged expression, where the variable is a range's value
}

// funcScan is the walk of one test function.
type funcScan struct {
	*fileScan
	fn   string
	body *ast.BlockStmt

	parents  map[ast.Node]ast.Node
	implied  map[*ast.CompositeLit]ast.Expr // an elided composite's type, from its parent's
	assigned map[string][]assign            // each variable's assignments, by name, in order

	locals map[string]helperInfo // the function's own closures, by the variable holding each
	depth  int                   // how far varUses has followed a want's other names

	bindings  []binding
	byBinding map[binding][]hit // the lines each want holds
	direct    []hit             // lines counted where they are compared
	refs      []ref             // variables in an expected position, whose values may be expected too
	asserts   []*ast.CallExpr   // the function's assertions: t.Error… and t.Fatal… calls, comparison helpers
}

// A ref is a variable met in an expected position: assertPivot(t, …, k), want :=
// Frame(k, …). Its value is counted where every use of it is in such a position, so
// that a column of a want written beside it is not missed, and an input compared
// with itself after a round trip is not taken for one.
type ref struct {
	at      ast.Expr // a variable, or a field of a case's row: tc.x
	name    string
	field   bool
	binding *binding // the want it is in, nil where it is compared directly
	rule    string
}

func newFuncScan(s *fileScan, fn string, body *ast.BlockStmt) *funcScan {
	return &funcScan{fileScan: s, fn: fn, body: body,
		parents: map[ast.Node]ast.Node{}, implied: map[*ast.CompositeLit]ast.Expr{},
		assigned: map[string][]assign{}, byBinding: map[binding][]hit{}, locals: map[string]helperInfo{}}
}

func (s *funcScan) run() {
	// The parents, the elided composites' types and the variables' assignments first.
	var stack []ast.Node
	ast.Inspect(s.body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if len(stack) > 0 {
			s.parents[n] = stack[len(stack)-1]
		}
		stack = append(stack, n)
		switch n := n.(type) {
		case *ast.CompositeLit:
			s.noteImplied(n)
		case *ast.AssignStmt:
			s.noteAssign(n.Lhs, n.Rhs)
			for i, l := range n.Lhs {
				if id, ok := l.(*ast.Ident); ok && i < len(n.Rhs) {
					if fl, ok := n.Rhs[i].(*ast.FuncLit); ok {
						s.locals[id.Name] = helperOf(id.Name, fl.Type, fl.Body)
					}
				}
			}
		case *ast.ValueSpec:
			s.noteAssign(identExprs(n.Names), n.Values)
		case *ast.RangeStmt:
			if id, ok := n.Key.(*ast.Ident); ok {
				s.assigned[id.Name] = append(s.assigned[id.Name], assign{pos: id.Pos()})
			}
			if id, ok := n.Value.(*ast.Ident); ok {
				s.assigned[id.Name] = append(s.assigned[id.Name], assign{pos: id.Pos(), of: n.X})
			}
		}
		return true
	})
	ast.Inspect(s.body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			s.wantAssign(n, n.Lhs, n.Rhs)
		case *ast.ValueSpec:
			s.wantAssign(n, identExprs(n.Names), n.Values)
		case *ast.CompositeLit:
			s.wantFields(n)
		case *ast.RangeStmt:
			// for _, want := range []int64{10, 20}: a want a row at a time.
			if id, ok := n.Value.(*ast.Ident); ok && isWantName(id.Name) && !structuralWantName(id.Name) {
				s.record(binding{name: id.Name, loop: n}, n.X, "want ranged over")
			}
		case *ast.BinaryExpr:
			s.comparison(n)
		case *ast.CallExpr:
			s.call(n)
		}
		return true
	})
}

func identExprs(ids []*ast.Ident) []ast.Expr {
	out := make([]ast.Expr, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

func (s *funcScan) noteImplied(cl *ast.CompositeLit) {
	t := cl.Type
	if t == nil {
		t = s.implied[cl]
	}
	var key, elem ast.Expr
	switch tt := t.(type) {
	case *ast.ArrayType:
		elem = tt.Elt
	case *ast.MapType:
		key, elem = tt.Key, tt.Value
	default:
		return
	}
	set := func(e, typ ast.Expr) {
		if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AND {
			e = u.X
			if st, ok := typ.(*ast.StarExpr); ok {
				typ = st.X
			}
		}
		if c, ok := e.(*ast.CompositeLit); ok && c.Type == nil {
			s.implied[c] = typ
		}
	}
	for _, e := range cl.Elts {
		if kv, ok := e.(*ast.KeyValueExpr); ok {
			if key != nil {
				set(kv.Key, key)
			}
			set(kv.Value, elem)
			continue
		}
		set(e, elem)
	}
}

func (s *funcScan) noteAssign(lhs, rhs []ast.Expr) {
	for i, l := range lhs {
		id, ok := l.(*ast.Ident)
		if !ok || id.Name == "_" {
			continue
		}
		switch {
		case len(rhs) == len(lhs):
			s.assigned[id.Name] = append(s.assigned[id.Name], assign{pos: id.Pos(), rhs: rhs[i]})
		case len(rhs) == 1:
			s.assigned[id.Name] = append(s.assigned[id.Name], assign{pos: id.Pos(), rhs: rhs[0]})
		}
	}
}

// assignmentAt returns the assignment of name in effect at use: an if's own, else the
// last before it.
func (s *funcScan) assignmentAt(name string, use ast.Node) (assign, bool) {
	for p := s.parents[use]; p != nil; p = s.parents[p] {
		if is, ok := p.(*ast.IfStmt); ok && is.Init != nil {
			if as, ok := is.Init.(*ast.AssignStmt); ok {
				for i, l := range as.Lhs {
					if id, ok := l.(*ast.Ident); ok && id.Name == name {
						if len(as.Rhs) == len(as.Lhs) {
							return assign{pos: id.Pos(), rhs: as.Rhs[i]}, true
						}
						return assign{pos: id.Pos(), rhs: as.Rhs[0]}, true
					}
				}
			}
		}
	}
	var best assign
	found := false
	for _, a := range s.assigned[name] {
		if a.pos < use.Pos() {
			best, found = a, true
		}
	}
	return best, found
}

// structFields returns the fields of the struct type t names, if it names one.
func (s *funcScan) structFields(t ast.Expr) []string {
	switch tt := t.(type) {
	case *ast.StructType:
		return fieldNames(tt)
	case *ast.Ident:
		return s.pkg.structs[tt.Name]
	case *ast.StarExpr:
		return s.structFields(tt.X)
	case *ast.ParenExpr:
		return s.structFields(tt.X)
	}
	return nil
}

// wantAssign counts the right side of an assignment to a want: want := …, want[i] = …,
// want = append(want, …). One built a row at a time in a loop is a model's, a Go
// oracle's, and is not counted.
func (s *funcScan) wantAssign(stmt ast.Node, lhs, rhs []ast.Expr) {
	for i, l := range lhs {
		name := baseIdent(l)
		if name == "" || !isWantName(name) || structuralWantName(name) {
			continue
		}
		var r ast.Expr
		switch {
		case len(rhs) == len(lhs):
			r = rhs[i]
		case len(rhs) == 1:
			r = rhs[0]
		default:
			continue
		}
		_, indexed := l.(*ast.IndexExpr)
		if (indexed || isAppend(r)) && s.inLoop(stmt) {
			continue
		}
		_, field := l.(*ast.SelectorExpr)
		s.record(binding{name: name, field: field}, r, "want variable")
	}
}

func isAppend(e ast.Expr) bool {
	c, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	id, ok := c.Fun.(*ast.Ident)
	return ok && id.Name == "append"
}

// inLoop reports whether n is in a for or range loop's body.
func (s *funcScan) inLoop(n ast.Node) bool {
	for p := s.parents[n]; p != nil; p = s.parents[p] {
		switch p.(type) {
		case *ast.ForStmt, *ast.RangeStmt:
			return true
		case *ast.FuncLit:
			return false
		}
	}
	return false
}

// baseIdent is the name an assignment's target is: want, want[i], tc.want.
func baseIdent(e ast.Expr) string {
	for {
		switch x := e.(type) {
		case *ast.Ident:
			return x.Name
		case *ast.IndexExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		case *ast.SelectorExpr:
			return x.Sel.Name
		default:
			return ""
		}
	}
}

// wantFields counts a composite literal's want fields, keyed or by their place in the
// struct.
func (s *funcScan) wantFields(cl *ast.CompositeLit) {
	t := cl.Type
	if t == nil {
		t = s.implied[cl]
	}
	fields := s.structFields(t)
	table := s.tableOf(cl)
	for i, e := range cl.Elts {
		if kv, ok := e.(*ast.KeyValueExpr); ok {
			if id, ok := kv.Key.(*ast.Ident); ok && isWantName(id.Name) && !structuralWantName(id.Name) &&
				(fields == nil || slices.Contains(fields, id.Name)) {
				s.record(binding{name: id.Name, field: true, table: table}, kv.Value, "want field")
			}
			continue
		}
		if i < len(fields) && isWantName(fields[i]) && !structuralWantName(fields[i]) {
			s.record(binding{name: fields[i], field: true, table: table}, e, "want field")
		}
	}
}

// tableOf returns the table a row is in: the composite literal it is an element (or
// a map value) of.
func (s *funcScan) tableOf(row *ast.CompositeLit) *ast.CompositeLit {
	var e ast.Node = row
	for {
		p := s.parents[e]
		switch x := p.(type) {
		case *ast.UnaryExpr:
			e = x
			continue
		case *ast.KeyValueExpr:
			if x.Value != e {
				return nil
			}
			e = x
			continue
		case *ast.CompositeLit:
			return x
		}
		return nil
	}
}

// record counts the literals of e in its data positions under b.
func (s *funcScan) record(b binding, e ast.Expr, rule string) {
	if _, seen := s.byBinding[b]; !seen {
		s.bindings = append(s.bindings, b)
	}
	for _, l := range s.data(e, false) {
		if r, ok := refOf(l); ok {
			r.binding, r.rule = &b, rule+" through "+r.name
			s.refs = append(s.refs, r)
			continue
		}
		s.byBinding[b] = append(s.byBinding[b], s.hitsOf(l, rule)...)
	}
}

// refOf returns the ref a data position's node is, if it is a variable or a field
// rather than a literal.
func refOf(n ast.Node) (ref, bool) {
	switch x := n.(type) {
	case *ast.Ident:
		if isRef(x) {
			return ref{at: x, name: x.Name}, true
		}
	case *ast.SelectorExpr:
		return ref{at: x, name: x.Sel.Name, field: true}, true
	}
	return ref{}, false
}

// isRef reports whether a data position's identifier is a variable, not a literal.
func isRef(id *ast.Ident) bool {
	return id.Name != "true" && id.Name != "false" && id.Name != "nil"
}

func (s *funcScan) hitsOf(l ast.Node, rule string) []hit {
	start, end := s.fset.Position(l.Pos()), s.fset.Position(l.End())
	text := litText(l)
	var out []hit
	for line := start.Line; line <= end.Line; line++ {
		out = append(out, hit{file: s.rel, fn: s.fn, line: line, lit: text, rule: rule})
	}
	return out
}

func litText(l ast.Node) string {
	switch l := l.(type) {
	case *ast.BasicLit:
		v := l.Value
		if i := strings.IndexByte(v, '\n'); i >= 0 {
			v = v[:i] + "…"
		}
		if r := []rune(v); len(r) > 60 {
			v = string(r[:57]) + "…"
		}
		return v
	case *ast.Ident:
		return l.Name
	}
	return "?"
}

var basicTypes = map[string]bool{
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true, "uintptr": true,
	"float32": true, "float64": true, "complex64": true, "complex128": true,
	"string": true, "byte": true, "rune": true, "bool": true, "any": true,
}

// transparent are the package functions whose arguments are a value's own data: a
// frame's or a column's constructors, an instant's, a big number's.
var transparent = map[string]map[string]bool{
	"github.com/advenn/ursus": {"Values": true, "ValuesNullable": true, "Frame": true, "FrameOf": true,
		"FromRows": true, "TypedColumn": true, "Lit": true, "ScanCSVReader": true, "ScanNDJSONBytes": true,
		"ScanJSONBytes": true},
	"time":                         {"Date": true, "Unix": true, "UnixMilli": true, "UnixMicro": true, "Duration": true, "Month": true, "Parse": true},
	"math/big":                     {"NewInt": true, "NewRat": true, "NewFloat": true},
	"github.com/advenn/ursus/i128": {"FromInt64": true, "FromUint64": true, "FromFloat64": true, "Parse": true, "ParseDecimal": true},
	"strings":                      {"Join": true},
}

// dataMethods are the methods through whose receiver a value's data still flows.
var dataMethods = map[string]bool{"Lazy": true, "SetString": true, "UTC": true, "In": true}

// opaqueFuncs are the calls whose literal arguments are names or sizes, not data.
var opaqueFuncs = map[string]bool{
	"Col": true, "col": true, "c": true, "Cols": true, "make": true, "len": true, "cap": true, "new": true,
	"min": true, "max": true, "panic": true, "print": true, "println": true,
}

// data returns the literals in e's data positions: e itself, a composite's elements,
// a constructor's arguments, a conversion's operand, a constant expression's operands;
// not a method's arguments, a computation's, a name's, a map's key or a size's.
// inComposite says e is a composite's element, where a nil is a null value.
func (s *funcScan) data(e ast.Expr, inComposite bool) []ast.Node {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind == token.STRING && fileName(x.Value) {
			return nil
		}
		return []ast.Node{x}
	case *ast.Ident:
		if x.Name == "nil" && !inComposite {
			return nil
		}
		return []ast.Node{x} // true, false, a null; or a variable, a ref
	case *ast.ParenExpr:
		return s.data(x.X, inComposite)
	case *ast.UnaryExpr:
		return s.data(x.X, inComposite)
	case *ast.IndexExpr:
		if _, table := x.X.(*ast.CompositeLit); table {
			return s.data(x.X, inComposite) // map[string][]float32{…}[name]
		}
	case *ast.CompositeLit:
		var out []ast.Node
		for _, el := range x.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				out = append(out, s.data(kv.Value, true)...)
				continue
			}
			out = append(out, s.data(el, true)...)
		}
		return out
	case *ast.BinaryExpr:
		if constant(x) {
			return append(s.data(x.X, inComposite), s.data(x.Y, inComposite)...)
		}
		return nil
	case *ast.FuncLit:
		var out []ast.Node
		ast.Inspect(x.Body, func(n ast.Node) bool {
			if _, ok := n.(*ast.FuncLit); ok {
				return false
			}
			if r, ok := n.(*ast.ReturnStmt); ok {
				for _, res := range r.Results {
					out = append(out, s.data(res, false)...)
				}
			}
			return true
		})
		return out
	case *ast.CallExpr:
		return s.callData(x)
	case *ast.SelectorExpr:
		if _, isPkg := s.pkgOf(x.X); !isPkg {
			if _, ok := x.X.(*ast.Ident); ok {
				return []ast.Node{x} // a case's field: tc.x, a ref
			}
		}
	}
	return nil
}

func unindex(fun ast.Expr) ast.Expr {
	for {
		switch f := fun.(type) {
		case *ast.IndexExpr:
			fun = f.X
		case *ast.IndexListExpr:
			fun = f.X
		case *ast.ParenExpr:
			fun = f.X
		default:
			return fun
		}
	}
}

func (s *funcScan) callData(x *ast.CallExpr) []ast.Node {
	args := x.Args
	switch f := unindex(x.Fun).(type) {
	case *ast.ArrayType: // []byte("…")
		return s.dataArgs(args, false)
	case *ast.Ident:
		if basicTypes[f.Name] {
			return s.dataArgs(args, false)
		}
		if opaqueFuncs[f.Name] {
			return nil
		}
		if f.Name == "append" && len(args) > 0 {
			return s.dataArgs(args[1:], false)
		}
		if h, ok := s.helper(f.Name); ok {
			var out []ast.Node
			for i, a := range args {
				if h.takesData(i) {
					out = append(out, s.dataArgs([]ast.Expr{a}, false)...)
				}
			}
			return out
		}
		if oracleHelper(f.Name) {
			return nil
		}
		return s.dataArgs(args, true)
	case *ast.SelectorExpr:
		if path, isPkg := s.pkgOf(f.X); isPkg {
			if transparent[path][f.Sel.Name] {
				if path == "strings" { // strings.Join(parts, sep): the parts
					return s.dataArgs(args[:1], false)
				}
				return s.dataArgs(args, true)
			}
			return nil
		}
		if dataMethods[f.Sel.Name] {
			return s.data(f.X, false)
		}
	}
	return nil
}

// pkgOf returns the path of the package e names, if it names an import.
func (s *funcScan) pkgOf(e ast.Expr) (string, bool) {
	id, ok := e.(*ast.Ident)
	if !ok {
		return "", false
	}
	path, ok := s.imports[id.Name]
	return path, ok
}

// dataArgs returns the data literals of a call's arguments; named says a string first
// argument of several is the value's name, not its data.
func (s *funcScan) dataArgs(args []ast.Expr, named bool) []ast.Node {
	var out []ast.Node
	for i, a := range args {
		if named && i == 0 && len(args) > 1 {
			if bl, ok := a.(*ast.BasicLit); ok && bl.Kind == token.STRING {
				continue
			}
		}
		out = append(out, s.data(a, true)...) // a nil among a constructor's values is a null
	}
	return out
}

// constant reports whether a binary expression is made of literals and package
// constants alone: 1 << 40, math.MaxInt64 - 1, "a" + "b"; not n*(n+1)/2.
func constant(e ast.Expr) bool {
	ok := true
	ast.Inspect(e, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Ident:
			if n.Name != "true" && n.Name != "false" {
				ok = false
			}
		case *ast.SelectorExpr:
			return false // a package's constant: math.MaxInt64
		case *ast.CallExpr:
			if id, isID := n.Fun.(*ast.Ident); isID && basicTypes[id.Name] {
				for _, a := range n.Args {
					if !constant(a) {
						ok = false
					}
				}
				return false
			}
			ok = false
		}
		return ok
	})
	return ok
}

// fileName reports whether a string literal names a file: a test's input, not data.
func fileName(v string) bool {
	u, err := strconv.Unquote(v)
	if err != nil || strings.ContainsAny(u, " \n,") {
		return false
	}
	for _, ext := range []string{".csv", ".json", ".ndjson", ".jsonl", ".parquet", ".arrow", ".ipc", ".feather",
		".txt", ".gz", ".zst", ".bz2", ".lz4", ".db", ".sqlite", ".golden", ".go"} {
		if strings.HasSuffix(u, ext) {
			return true
		}
	}
	return false
}

// comparison counts a literal compared, by == or !=, with a value read from a frame.
// One in a failure's message is not an assertion.
func (s *funcScan) comparison(b *ast.BinaryExpr) {
	if b.Op != token.EQL && b.Op != token.NEQ {
		return
	}
	for _, pair := range [2][2]ast.Expr{{b.X, b.Y}, {b.Y, b.X}} {
		if l := compareLit(pair[1]); l != nil && s.gotish(pair[0]) && !s.inMessage(b) {
			s.direct = append(s.direct, s.hitsOf(l, "compared")...)
		}
	}
}

// inMessage reports whether n is an argument of t.Errorf, t.Logf or another of a
// test's messages.
func (s *funcScan) inMessage(n ast.Node) bool {
	for p := s.parents[n]; p != nil; p = s.parents[p] {
		switch x := p.(type) {
		case *ast.CallExpr:
			if f, ok := x.Fun.(*ast.SelectorExpr); ok && isTB(f.X) {
				return true
			}
			if f, ok := x.Fun.(*ast.SelectorExpr); ok {
				if path, isPkg := s.pkgOf(f.X); isPkg && path == "fmt" {
					return true
				}
			}
		case ast.Stmt:
			return false
		}
	}
	return false
}

// compareLit returns the literal a comparison's operand is: a number or a non-empty
// string, through parentheses, a sign and a conversion; not a Boolean.
func compareLit(e ast.Expr) *ast.BasicLit {
	for {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
			continue
		case *ast.UnaryExpr:
			e = x.X
			continue
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); ok && basicTypes[id.Name] && len(x.Args) == 1 {
				e = x.Args[0]
				continue
			}
			return nil
		case *ast.BasicLit:
			if x.Kind == token.STRING && (x.Value == `""` || x.Value == "``" || fileName(x.Value)) {
				return nil
			}
			return x
		}
		return nil
	}
}

// gotName reports whether a variable is one a test reads a frame's values into.
func gotName(n string) bool {
	switch n {
	case "got", "v", "vs", "r", "res", "result", "results", "val", "vals", "value", "values", "cell", "cells",
		"out", "row", "rows":
		return true
	}
	if strings.HasPrefix(n, "got") {
		return true
	}
	return len(n) == 2 && n[0] == 'v' && n[1] >= '0' && n[1] <= '9'
}

// readers are the methods and helpers that read a value out of a frame, a column or
// a row.
var readers = map[string]bool{
	"Get": true, "GetAny": true, "At": true, "Item": true, "Value": true, "Values": true, "ValueAt": true,
	"Int64": true, "Int32": true, "Uint64": true, "Float64": true, "Float32": true, "Bool": true, "Str": true,
	"Row": true, "Scalar": true, "Cell": true,
	// The root package's own: a column's cells as text, or as numbers.
	"cells": true, "texts": true, "numbers": true, "i64col": true, "int64Col": true, "cell": true, "item": true,
}

// shapeNames are the calls and fields whose answer is structure: a shape, a count, a
// type, an error, a plan, a resource.
var shapeNames = map[string]bool{
	"Height": true, "Width": true, "Len": true, "len": true, "cap": true, "NullCount": true, "NumRows": true,
	"NumCols": true, "NumChunks": true, "NumGoroutine": true, "Size": true, "DataType": true, "Dtype": true,
	"DType": true, "Type": true, "Schema": true, "Kind": true, "Error": true, "Explain": true, "Plan": true,
	"Name": true, "Names": true, "Columns": true, "Spills": true, "Used": true, "Peak": true, "Held": true, "Running": true,
	"Load": true, "Nullable": true, "Stats": true, "Workers": true, "Threads": true,
	"recover": true, "Unwrap": true, "Op": true,
	// A case's type or kind: tc.dt.String(), tc.kind.
	"dt": true, "typ": true, "dtype": true, "DT": true, "kind": true, "schema": true,
}

// structural reports whether e's value is structure: a shape, a count, a type, a
// name, an error, a plan, a resource; through a method chain's receivers, a
// conversion, strings.Join and fmt.Sprint, and a variable's assignment, but not a
// call's arguments, so that a read under a schema option is still a read.
func (s *funcScan) structural(e ast.Expr) bool { return s.structuralAt(e, 0) }

func (s *funcScan) structuralAt(e ast.Expr, depth int) bool {
	if depth > 3 {
		return false
	}
	switch x := e.(type) {
	case *ast.ParenExpr:
		return s.structuralAt(x.X, depth)
	case *ast.StarExpr:
		return s.structuralAt(x.X, depth)
	case *ast.UnaryExpr:
		return s.structuralAt(x.X, depth)
	case *ast.IndexExpr:
		return s.structuralAt(x.X, depth)
	case *ast.SliceExpr:
		return s.structuralAt(x.X, depth)
	case *ast.TypeAssertExpr:
		return s.structuralAt(x.X, depth)
	case *ast.BinaryExpr: // f.Name + ":" + f.Type.String()
		return s.structuralAt(x.X, depth) || s.structuralAt(x.Y, depth)
	case *ast.SelectorExpr:
		if _, isPkg := s.pkgOf(x.X); isPkg {
			return false
		}
		return shapeNames[x.Sel.Name] || s.structuralAt(x.X, depth)
	case *ast.CallExpr:
		switch f := unindex(x.Fun).(type) {
		case *ast.Ident:
			if basicTypes[f.Name] && len(x.Args) == 1 {
				return s.structuralAt(x.Args[0], depth)
			}
			if f.Name == "append" && len(x.Args) > 1 { // names = append(names, f.Name)
				return slices.ContainsFunc(x.Args[1:], func(a ast.Expr) bool { return s.structuralAt(a, depth) })
			}
			return shapeNames[f.Name]
		case *ast.SelectorExpr:
			if path, isPkg := s.pkgOf(f.X); isPkg {
				switch {
				case path == "strings" && f.Sel.Name == "Join" && len(x.Args) > 0:
					return s.structuralAt(x.Args[0], depth)
				case path == "fmt" && strings.HasPrefix(f.Sel.Name, "Sprint"):
					return slices.ContainsFunc(x.Args, func(a ast.Expr) bool { return s.structuralAt(a, depth) })
				}
				return false
			}
			return shapeNames[f.Sel.Name] || s.structuralAt(f.X, depth)
		}
	case *ast.Ident:
		if x.Name == "err" || strings.HasSuffix(x.Name, "Err") || x.Name == "plan" || x.Name == "schema" {
			return true
		}
		if a, ok := s.assignmentAt(x.Name, x); ok {
			switch {
			case a.of != nil:
				return s.structuralAt(a.of, depth+1)
			case a.rhs != nil && a.rhs != e:
				return s.structuralAt(a.rhs, depth+1)
			}
		}
	}
	return false
}

// gotish reports whether e reads a value out of a frame.
func (s *funcScan) gotish(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return s.gotish(x.X)
	case *ast.StarExpr:
		return s.gotish(x.X)
	case *ast.IndexExpr:
		return s.gotish(x.X)
	case *ast.IndexListExpr:
		return s.gotish(x.X)
	case *ast.SliceExpr:
		return s.gotish(x.X)
	case *ast.SelectorExpr:
		return !shapeNames[x.Sel.Name] && s.gotish(x.X)
	case *ast.TypeAssertExpr:
		return s.gotish(x.X)
	case *ast.Ident:
		if !gotName(x.Name) {
			return false
		}
		// A variable assigned what reads a frame; not a closure's input (a UDF's value),
		// a range's key, a constant, or a count of files, workers or bytes.
		a, ok := s.assignmentAt(x.Name, x)
		switch {
		case !ok:
			return false
		case a.of != nil:
			return !s.structural(a.of) && s.readsFrame(a.of, 0)
		case a.rhs == nil:
			return false
		}
		return !s.structural(a.rhs) && s.readsFrame(a.rhs, 0)
	case *ast.CallExpr:
		switch f := unindex(x.Fun).(type) {
		case *ast.Ident:
			if basicTypes[f.Name] {
				return len(x.Args) == 1 && s.gotish(x.Args[0])
			}
			return readers[f.Name]
		case *ast.SelectorExpr:
			if path, isPkg := s.pkgOf(f.X); isPkg {
				if path == "fmt" && strings.HasPrefix(f.Sel.Name, "Sprint") {
					return slices.ContainsFunc(x.Args, s.gotish)
				}
				return readers[f.Sel.Name] && path != "fmt"
			}
			if readers[f.Sel.Name] {
				return true
			}
			if f.Sel.Name == "String" {
				return s.gotish(f.X)
			}
		}
	}
	return false
}

// frameReaders are the methods that read a frame, a column or a batch, or run a query
// to one.
var frameReaders = map[string]bool{
	"Collect": true, "Column": true, "Series": true, "Row": true, "Rows": true, "RowWhere": true, "ItemAt": true,
	"Strings": true, "Int64s": true, "Float64s": true, "Bools": true, "MustValues": true, "Validity": true,
	"IsValid": true, "Field": true, "Struct": true, "ToSlice": true, "Slice": true, "Chunk": true, "Batch": true,
	"Next": true, "Record": true, "Read": true, "ReadAll": true, "Write": true, "Bytes": true, "Sum": true,
	"Max": true, "Min": true, "Mean": true, "Cast": true, "Eval": true, "Run": true, "Explode": true,
}

// frameTypes are the type names a helper reading a frame takes.
var frameTypes = []string{"DataFrame", "LazyFrame", "Series", "Column", "Batch", "Record", "Frame", "Array", "View"}

// readsFrame reports whether e reads a value from a frame, a column or a batch: a
// reader's call, a query collected, a helper taking a frame; through a variable it
// names, two assignments deep.
func (s *funcScan) readsFrame(e ast.Expr, depth int) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if found {
			return false
		}
		switch x := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CallExpr:
			switch f := unindex(x.Fun).(type) {
			case *ast.Ident:
				if readers[f.Name] || frameReaders[f.Name] {
					found = true
				} else if h, ok := s.helper(f.Name); ok && (h.takesFrame() || h.reads) {
					found = true
				}
			case *ast.SelectorExpr:
				if readers[f.Sel.Name] || frameReaders[f.Sel.Name] {
					found = true
				}
			}
		case *ast.Ident:
			if depth < 2 && x != e {
				if a, ok := s.assignmentAt(x.Name, x); ok {
					switch {
					case a.of != nil:
						found = s.readsFrame(a.of, depth+1)
					case a.rhs != nil && a.rhs != e:
						found = s.readsFrame(a.rhs, depth+1)
					}
				}
			}
		}
		return !found
	})
	return found
}

// countsSomething reports whether a helper answers a count, a size or a resource
// rather than a value: it returns an int, a []int or a bool, or its name says so.
func countsSomething(name string, ft *ast.FuncType) bool {
	l := strings.ToLower(name)
	for _, w := range []string{"limit", "size", "count", "rows", "batch", "held", "peak", "resumed", "worker",
		"thread", "fallback", "spill", "files", "entries", "opens", "calls"} {
		if strings.Contains(l, w) {
			return true
		}
	}
	if ft.Results == nil || len(ft.Results.List) == 0 {
		return false
	}
	switch t := ft.Results.List[0].Type.(type) {
	case *ast.Ident:
		return t.Name == "int" || t.Name == "bool"
	case *ast.ArrayType:
		id, ok := t.Elt.(*ast.Ident)
		return ok && id.Name == "int"
	}
	return false
}

// bodyReads reports whether a helper's body calls a frame's reader or collects a
// query.
func bodyReads(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			switch f := unindex(c.Fun).(type) {
			case *ast.SelectorExpr:
				found = found || readers[f.Sel.Name] || frameReaders[f.Sel.Name]
			case *ast.Ident:
				found = found || readers[f.Name]
			}
		}
		return !found
	})
	return found
}

// takesFrame reports whether a helper takes a frame, a column or a batch.
func (h helperInfo) takesFrame() bool {
	for _, p := range h.params {
		name := ""
		ast.Inspect(p.typ, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				name += id.Name + " "
			}
			return true
		})
		for _, t := range frameTypes {
			if strings.Contains(name, t) {
				return true
			}
		}
	}
	return false
}

// closureParam reports whether id names a parameter of a function literal around it.
func (s *funcScan) closureParam(id *ast.Ident) bool {
	for p := s.parents[id]; p != nil; p = s.parents[p] {
		if fl, ok := p.(*ast.FuncLit); ok {
			for _, f := range fl.Type.Params.List {
				for _, n := range f.Names {
					if n.Name == id.Name {
						return true
					}
				}
			}
		}
	}
	return false
}

// equalHelpers are the comparisons whose arguments from the index named on are
// expected.
var equalHelpers = map[string]map[string]int{
	"slices":                            {"Equal": 1, "EqualFunc": 1},
	"reflect":                           {"DeepEqual": 1},
	"bytes":                             {"Equal": 1},
	"maps":                              {"Equal": 1, "EqualFunc": 1},
	"github.com/advenn/ursus/ursustest": {"AssertFrameEqual": 2, "AssertSeriesEqual": 2},
}

// equalMethods are the methods comparing their receiver with an expected argument.
var equalMethods = map[string]bool{"Equals": true, "Equal": true, "Cmp": true}

// call counts the expected arguments of a comparison helper, or of a helper that
// takes a want; and notes the assertions.
func (s *funcScan) call(c *ast.CallExpr) {
	switch f := unindex(c.Fun).(type) {
	case *ast.SelectorExpr:
		if path, isPkg := s.pkgOf(f.X); isPkg {
			if from, ok := equalHelpers[path][f.Sel.Name]; ok {
				s.asserts = append(s.asserts, c)
				if from == 1 && len(c.Args) >= 2 && s.structural(c.Args[0]) {
					return // names, a schema's or a plan's: structure
				}
				if from == 1 && len(c.Args) >= 2 {
					if _, lit := c.Args[0].(*ast.CompositeLit); lit {
						s.directData(c.Args[0], "compared by "+f.Sel.Name)
					}
				}
				for _, a := range c.Args[min(from, len(c.Args)):] {
					if from == 1 && a != c.Args[1] { // EqualFunc's function
						continue
					}
					s.directData(a, "compared by "+f.Sel.Name)
				}
			} else if path == "github.com/advenn/ursus/ursustest" {
				s.asserts = append(s.asserts, c)
			}
			return
		}
		if isTB(f.X) {
			switch f.Sel.Name {
			case "Error", "Errorf", "Fatal", "Fatalf":
				s.asserts = append(s.asserts, c)
			}
			return
		}
		if equalMethods[f.Sel.Name] && len(c.Args) == 1 {
			s.directData(c.Args[0], "compared by "+f.Sel.Name)
		}
	case *ast.Ident:
		h, ok := s.helper(f.Name)
		if !ok || !h.anyWant() {
			return
		}
		s.asserts = append(s.asserts, c)
		for i, a := range c.Args {
			if h.takesWant(i) {
				s.directData(a, "passed as "+f.Name+"'s want")
			}
		}
	}
}

// helper returns a function the test calls by name: its own closure, or one of its
// package's test files.
func (s *funcScan) helper(name string) (helperInfo, bool) {
	if h, ok := s.locals[name]; ok {
		return h, true
	}
	h, ok := s.pkg.helpers[name]
	return h, ok
}

func (s *funcScan) directData(e ast.Expr, rule string) {
	for _, l := range s.data(e, false) {
		if r, ok := refOf(l); ok {
			r.rule = rule + " through " + r.name
			s.refs = append(s.refs, r)
			continue
		}
		s.direct = append(s.direct, s.hitsOf(l, rule)...)
	}
}

// followRefs counts the values of the variables and the case fields met in expected
// positions that are used nowhere else but there, printed or tested for being set: a
// variable's assignments, a field's values in its table's rows and its assignments.
// One step deep: a variable in such a value is not followed again.
func (s *funcScan) followRefs() {
	at := map[ast.Expr]bool{}
	for _, r := range s.refs {
		at[r.at] = true
	}
	type key struct {
		name  string
		field bool
	}
	only := map[key]bool{}
	for _, r := range s.refs {
		k := key{r.name, r.field}
		if _, done := only[k]; done {
			continue
		}
		if isWantName(r.name) || (!r.field && gotName(r.name)) {
			only[k] = false
			continue
		}
		ok := r.field || len(s.assigned[r.name]) > 0
		ast.Inspect(s.body, func(n ast.Node) bool {
			var u ast.Expr
			switch x := n.(type) {
			case *ast.Ident:
				if r.field || x.Name != r.name || s.isDefinition(x) {
					return true
				}
				u = x
			case *ast.SelectorExpr:
				if !r.field || x.Sel.Name != r.name || s.isAssignTarget(x) {
					return true
				}
				u = x
			default:
				return true
			}
			if !at[u] && s.useOf(u) != useNeutral {
				ok = false
			}
			return ok
		})
		only[k] = ok
	}
	counted := map[key]bool{}
	for _, r := range s.refs {
		k := key{r.name, r.field}
		if !only[k] || counted[k] && r.binding == nil {
			continue
		}
		counted[k] = true
		var values []ast.Expr
		if r.field {
			values = s.fieldValues(r.name)
		} else {
			for _, a := range s.assigned[r.name] {
				if a.rhs != nil && a.of == nil {
					values = append(values, a.rhs)
				}
			}
		}
		for _, v := range values {
			for _, l := range s.data(v, false) {
				if _, isRef := refOf(l); isRef {
					continue
				}
				hs := s.hitsOf(l, r.rule)
				if r.binding != nil {
					s.byBinding[*r.binding] = append(s.byBinding[*r.binding], hs...)
				} else {
					s.direct = append(s.direct, hs...)
				}
			}
		}
	}
}

// fieldValues returns the values a field is given in the function: in its rows,
// keyed or by place, and by assignment to it.
func (s *funcScan) fieldValues(name string) []ast.Expr {
	var out []ast.Expr
	ast.Inspect(s.body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CompositeLit:
			t := x.Type
			if t == nil {
				t = s.implied[x]
			}
			fields := s.structFields(t)
			for i, e := range x.Elts {
				if kv, ok := e.(*ast.KeyValueExpr); ok {
					if id, ok := kv.Key.(*ast.Ident); ok && id.Name == name && fields != nil {
						out = append(out, kv.Value)
					}
					continue
				}
				if i < len(fields) && fields[i] == name {
					out = append(out, e)
				}
			}
		case *ast.AssignStmt:
			for i, l := range x.Lhs {
				if sel, ok := l.(*ast.SelectorExpr); ok && sel.Sel.Name == name && len(x.Rhs) == len(x.Lhs) {
					out = append(out, x.Rhs[i])
				}
			}
		}
		return true
	})
	return out
}

// useKind is how a want is used: printed or tested for emptiness, as structure, or as
// data.
type useKind int

const (
	useNone useKind = iota
	useNeutral
	useStructural
	useData
)

// uses returns how b is used: data if any use compares or passes it as data,
// structural if every use is an error's, a plan's, a type's or a shape's, neutral if
// it is only printed or tested for emptiness, none if it is never read where the
// walk sees. A field's uses are its table's: the selector of the range variable over
// it.
func (s *funcScan) uses(b binding) useKind {
	scope, holder := s.tableScope(b)
	kind := useNone
	ast.Inspect(scope, func(n ast.Node) bool {
		var u ast.Expr
		switch x := n.(type) {
		case *ast.Ident:
			if b.field || x.Name != b.name || s.isDefinition(x) {
				return true
			}
			u = x
		case *ast.SelectorExpr:
			if !b.field || x.Sel.Name != b.name || s.isAssignTarget(x) {
				return true
			}
			if holder != "" {
				if id, ok := x.X.(*ast.Ident); !ok || id.Name != holder {
					return true
				}
			}
			u = x
		default:
			return true
		}
		kind = max(kind, s.useOf(u))
		return true
	})
	return kind
}

// tableScope returns where a binding's uses are: for a field of a table ranged over,
// directly or by a variable, the loop's body and the range variable's name; else the
// whole function.
func (s *funcScan) tableScope(b binding) (ast.Node, string) {
	if b.loop != nil {
		return b.loop.Body, ""
	}
	if !b.field || b.table == nil {
		return s.body, ""
	}
	var e ast.Node = b.table
	for {
		switch p := s.parents[e].(type) {
		case *ast.UnaryExpr, *ast.ParenExpr:
			e = p
			continue
		case *ast.RangeStmt:
			if id, ok := p.Value.(*ast.Ident); ok && p.X == e {
				return p.Body, id.Name
			}
		case *ast.AssignStmt, *ast.ValueSpec:
			var name string
			if as, ok := p.(*ast.AssignStmt); ok {
				for i, r := range as.Rhs {
					if r == e && i < len(as.Lhs) {
						name = baseIdent(as.Lhs[i])
					}
				}
			} else {
				vs := p.(*ast.ValueSpec)
				for i, r := range vs.Values {
					if r == e && i < len(vs.Names) {
						name = vs.Names[i].Name
					}
				}
			}
			if name == "" {
				break
			}
			var body ast.Node
			var holder string
			ast.Inspect(s.body, func(n ast.Node) bool {
				if r, ok := n.(*ast.RangeStmt); ok && body == nil {
					if id, ok := r.X.(*ast.Ident); ok && id.Name == name {
						if v, ok := r.Value.(*ast.Ident); ok {
							body, holder = r.Body, v.Name
						}
					}
				}
				return body == nil
			})
			if body != nil {
				return body, holder
			}
		}
		return s.body, ""
	}
}

// isDefinition reports whether id is a name being declared or assigned, a field's
// key, or a selector's field.
func (s *funcScan) isDefinition(id *ast.Ident) bool {
	switch p := s.parents[id].(type) {
	case *ast.SelectorExpr:
		return p.Sel == id
	case *ast.KeyValueExpr:
		return p.Key == id
	case *ast.ValueSpec:
		return slices.Contains(p.Names, id)
	}
	return s.isAssignTarget(id)
}

func (s *funcScan) isAssignTarget(e ast.Expr) bool {
	for {
		switch x := s.parents[e].(type) {
		case *ast.IndexExpr:
			if x.X != e {
				return false
			}
			e = x
			continue
		case *ast.AssignStmt:
			return slices.Contains(x.Lhs, e)
		}
		return false
	}
}

// useOf classifies one use of a want.
func (s *funcScan) useOf(u ast.Expr) useKind {
	var e ast.Node = u
	for {
		switch x := s.parents[e].(type) {
		case *ast.ParenExpr, *ast.StarExpr, *ast.UnaryExpr, *ast.SliceExpr:
			e = x
			continue
		case *ast.IndexExpr:
			if x.X == e {
				e = x
				continue
			}
			return useData
		case *ast.SelectorExpr:
			// want.Height(), want.Error(): a use of what the method answers.
			if c, ok := s.parents[x].(*ast.CallExpr); ok && c.Fun == x {
				if shapeNames[x.Sel.Name] {
					return useStructural
				}
				e = c
				continue
			}
			if shapeNames[x.Sel.Name] {
				return useStructural
			}
			return useData
		case *ast.CallExpr:
			if x.Fun == e {
				return useData
			}
			return s.argUse(x, e)
		case *ast.BinaryExpr:
			other := x.X
			if other == e {
				other = x.Y
			}
			switch x.Op {
			case token.EQL, token.NEQ, token.LSS, token.GTR, token.LEQ, token.GEQ:
				if empty(other) {
					return useNeutral
				}
				if s.structural(other) {
					return useStructural
				}
			}
			return useData
		case *ast.RangeStmt:
			// for _, w := range want: as w is used in the loop.
			if id, ok := x.Value.(*ast.Ident); ok && x.X == e && s.depth < 3 {
				return s.varUses(id.Name, x.Body)
			}
			return useData
		case *ast.AssignStmt:
			// w := want: as w is used after.
			if len(x.Lhs) == len(x.Rhs) && s.depth < 3 {
				for i, r := range x.Rhs {
					if id, ok := x.Lhs[i].(*ast.Ident); ok && r == e && id.Name != "_" {
						return s.varUses(id.Name, s.body)
					}
				}
			}
			return useData
		}
		return useData
	}
}

// varUses classifies the uses of a variable within scope, as a want's, for a want
// ranged over or given another name; a few steps deep at most.
func (s *funcScan) varUses(name string, scope ast.Node) useKind {
	s.depth++
	defer func() { s.depth-- }()
	kind := useNone
	ast.Inspect(scope, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == name && !s.isDefinition(id) {
			if _, isRange := s.parents[id].(*ast.RangeStmt); isRange && s.parents[id].(*ast.RangeStmt).Value == id {
				return true
			}
			kind = max(kind, s.useOf(id))
		}
		return true
	})
	return kind
}

// empty reports whether e is "", nil or 0: a want tested for being set.
func empty(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.BasicLit:
		return x.Value == `""` || x.Value == "``" || x.Value == "0"
	case *ast.Ident:
		return x.Name == "nil"
	}
	return false
}

// argUse classifies a want passed to a call as its argument arg.
func (s *funcScan) argUse(c *ast.CallExpr, arg ast.Node) useKind {
	switch f := unindex(c.Fun).(type) {
	case *ast.SelectorExpr:
		if path, isPkg := s.pkgOf(f.X); isPkg {
			if _, ok := equalHelpers[path][f.Sel.Name]; ok {
				// slices.Equal(got.Columns(), want): as what it is compared with.
				for _, a := range c.Args {
					if a != arg && s.structural(a) {
						return useStructural
					}
				}
				return useData
			}
			switch path {
			case "strings", "bytes", "errors", "regexp":
				return useStructural
			case "fmt":
				if strings.HasPrefix(f.Sel.Name, "Sprint") {
					return s.useOf(c)
				}
				return useNeutral
			}
			if structuralHelper(f.Sel.Name) {
				return useStructural
			}
			return useData
		}
		if isTB(f.X) {
			return useNeutral
		}
		switch f.Sel.Name {
		case "Contains", "HasPrefix", "HasSuffix", "MatchString", "Match":
			return useStructural
		}
		if structuralHelper(f.Sel.Name) {
			return useStructural
		}
	case *ast.Ident:
		if basicTypes[f.Name] {
			return s.useOf(c)
		}
		if f.Name == "len" || f.Name == "cap" {
			return useNeutral
		}
		if structuralHelper(f.Name) {
			return useStructural
		}
	}
	return useData
}

// errorsOnly reports whether every assertion of the function is on an error: each
// t.Error… or t.Fatal… sits under a condition reading an error, and no comparison
// helper is called.
func (s *funcScan) errorsOnly() bool {
	if len(s.asserts) == 0 {
		return false
	}
	for _, c := range s.asserts {
		if f, ok := c.Fun.(*ast.SelectorExpr); !ok || !isTB(f.X) || !s.underErrorCondition(c) {
			return false
		}
	}
	return true
}

func isTB(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && (id.Name == "t" || id.Name == "tb")
}

// underErrorCondition reports whether n's nearest enclosing if tests an error.
func (s *funcScan) underErrorCondition(n ast.Node) bool {
	for p := s.parents[n]; p != nil; p = s.parents[p] {
		switch x := p.(type) {
		case *ast.IfStmt:
			return mentionsError(x.Cond) || (x.Init != nil && mentionsError(x.Init))
		case *ast.FuncLit:
			return false
		}
	}
	return false
}

func mentionsError(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			found = found || x.Name == "err" || strings.HasSuffix(x.Name, "Err") || x.Name == "recover"
		case *ast.SelectorExpr:
			found = found || x.Sel.Name == "Error" || x.Sel.Name == "Is" || x.Sel.Name == "As"
		}
		return !found
	})
	return found
}

// lines returns the function's counted lines, a line once: each literal compared,
// and each want's unless all its uses are structure's.
func (s *funcScan) lines() []hit {
	seen := map[int]bool{}
	var out []hit
	add := func(hs []hit) {
		for _, h := range hs {
			if !seen[h.line] {
				seen[h.line] = true
				out = append(out, h)
			}
		}
	}
	s.followRefs()
	add(s.direct)
	for _, b := range s.bindings {
		if s.uses(b) == useStructural {
			continue
		}
		add(s.byBinding[b])
	}
	slices.SortFunc(out, func(a, b hit) int { return a.line - b.line })
	return out
}

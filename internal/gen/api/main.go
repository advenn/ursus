// Command api lists ursus's public API, a feature a line, and checks a version's
// breaks against the last tag's (step 401, C1 of v0.8-scope.md).
//
// A feature is an exported name of a public package and what a caller sees of it:
// a function's signature, a type's kind, each exported field, method and interface
// method, a constant's type and value, a variable's type, in the manner of Go's own
// cmd/api. Parameter names are left out, as a caller never sees them. A feature of the
// last tag's that the API no longer has is a break: removed, renamed, or changed in
// a way a caller can see, as a signature, a field's type or an interface's methods.
// Adding is not.
//
// api/<tag>.txt holds each tag's features, written as the tag is cut, at the commit
// tagged (`make api-tag TAG=v0.8.0`), or after from a checkout of it:
//
//	go run ./internal/gen/api -tag v0.8.0 -dir <the checkout> > api/v0.8.0.txt
//
// and `make api`, which CI runs, lists the features the newest of them has and this
// tree does not, and fails unless each is named, in backquotes, in a Breaks section
// of the CHANGELOG's unreleased releases: ursus is on ZeroVer, a stable 1.0 not
// expected yet, so a minor version may break, and every break is listed.
//
// apidiff and gorelease, which the scope named, never finish the root package: their
// correspondence check computes method sets without end, past 4.5 GB in twenty
// seconds (x/exp of 2026-10-14). The four smaller packages they compare in a second.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/importer"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

func main() {
	dir := flag.String("dir", ".", "the module to list")
	check := flag.Bool("check", false, "list the breaks since the newest api/*.txt and fail on one the CHANGELOG does not name")
	tag := flag.String("tag", "", "head the list as the API at this tag, for api/<tag>.txt")
	flag.Parse()
	feats, err := features(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "api:", err)
		os.Exit(2)
	}
	if !*check {
		w := bufio.NewWriter(os.Stdout)
		if *tag != "" {
			fmt.Fprintf(w, "# ursus's public API at %s, a feature a line: go run ./internal/gen/api in a checkout\n"+
				"# of the tag (step 401). `make api` lists what it has and the tree does not, each a break\n"+
				"# the CHANGELOG names.\n", *tag)
		}
		for _, f := range feats {
			fmt.Fprintln(w, f)
		}
		w.Flush()
		return
	}
	if err := checkBreaks(*dir, feats); err != nil {
		fmt.Fprintln(os.Stderr, "api:", err)
		os.Exit(1)
	}
}

// checkBreaks reads the newest api/<tag>.txt under dir, lists what it has and feats
// does not, and fails on one no Breaks section of the CHANGELOG's unreleased releases
// names.
func checkBreaks(dir string, feats []string) error {
	tag, old, err := newest(filepath.Join(dir, "api"))
	if err != nil {
		return err
	}
	breaks := removed(old, feats)
	added := len(removed(feats, old))
	fmt.Printf("api: %d features, %d added and %d gone since %s\n", len(feats), added, len(breaks), tag)
	log, err := os.ReadFile(filepath.Join(dir, "CHANGELOG.md"))
	if err != nil {
		return err
	}
	named := breaksNamed(string(log))
	var missing []string
	for _, b := range breaks {
		ok := slices.ContainsFunc(names(b), func(n string) bool { return named[n] })
		mark := "  "
		if !ok {
			mark, missing = "! ", append(missing, b)
		}
		fmt.Println(mark + b)
	}
	if len(missing) > 0 {
		return fmt.Errorf("%d breaks since %s that no Breaks section of an unreleased version names, marked !: "+
			"name each in backquotes, Type.Member for a member", len(missing), tag)
	}
	return nil
}

// newest returns the newest tag's features in dir, by the tags' versions.
func newest(dir string) (string, []string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "v*.txt"))
	if err != nil || len(files) == 0 {
		return "", nil, fmt.Errorf("no api/v*.txt in %s", dir)
	}
	slices.SortFunc(files, func(a, b string) int { return compareVersions(tagOf(a), tagOf(b)) })
	last := files[len(files)-1]
	b, err := os.ReadFile(last)
	if err != nil {
		return "", nil, err
	}
	var feats []string
	for l := range strings.Lines(string(b)) {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			feats = append(feats, l)
		}
	}
	return tagOf(last), feats, nil
}

func tagOf(path string) string { return strings.TrimSuffix(filepath.Base(path), ".txt") }

// compareVersions orders vMAJOR.MINOR.PATCH by number.
func compareVersions(a, b string) int {
	pa, pb := strings.Split(strings.TrimPrefix(a, "v"), "."), strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := range max(len(pa), len(pb)) {
		var x, y int
		if i < len(pa) {
			fmt.Sscan(pa[i], &x)
		}
		if i < len(pb) {
			fmt.Sscan(pb[i], &y)
		}
		if x != y {
			return x - y
		}
	}
	return 0
}

// removed returns what old has and cur does not, in old's order.
func removed(old, cur []string) []string {
	have := make(map[string]bool, len(cur))
	for _, f := range cur {
		have[f] = true
	}
	var out []string
	for _, f := range old {
		if !have[f] {
			out = append(out, f)
		}
	}
	return out
}

var (
	unreleased = regexp.MustCompile(`(?m)^## Unreleased`)
	release    = regexp.MustCompile(`(?m)^## `)
	section    = regexp.MustCompile(`(?m)^### `)
	quoted     = regexp.MustCompile("`([^`]+)`")
	word       = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?`)
)

// breaksNamed is every name, and Type.Member, in backquotes in a Breaks section of the
// CHANGELOG's unreleased versions: `WithThreads(n)` names WithThreads,
// `EngineStats.Held` names EngineStats.Held and EngineStats.
func breaksNamed(log string) map[string]bool {
	named := map[string]bool{}
	for _, at := range unreleased.FindAllStringIndex(log, -1) {
		rel := log[at[1]:]
		if next := release.FindStringIndex(rel); next != nil {
			rel = rel[:next[0]]
		}
		for _, s := range section.FindAllStringIndex(rel, -1) {
			body := rel[s[1]:]
			if !strings.HasPrefix(body, "Breaks") {
				continue
			}
			if next := section.FindStringIndex(body); next != nil {
				body = body[:next[0]]
			}
			for _, q := range quoted.FindAllStringSubmatch(body, -1) {
				for _, w := range word.FindAllString(q[1], -1) {
					named[w] = true
					if t, _, ok := strings.Cut(w, "."); ok {
						named[t] = true
					}
				}
			}
		}
	}
	return named
}

// names are what may name a feature's break in the CHANGELOG: its name; a member's
// own name, its type's, or Type.Member, as v0.6.8 named `SinkNDJSON`; a constant's or a
// variable's type, where it is this package's: the break of `ConcatMode` is its
// constants'.
func names(feat string) []string {
	_, rest, _ := strings.Cut(feat, ", ")
	kind, rest, _ := strings.Cut(rest, " ")
	ident := func(s string) string {
		s = strings.TrimLeft(s, "*[]")
		return s[:strings.IndexFunc(s+" ", func(r rune) bool {
			return r == ' ' || r == '(' || r == '[' || r == ',' || r == ')'
		})]
	}
	switch kind {
	case "method":
		// method (*T) M(...)
		recv, m, _ := strings.Cut(rest, ") ")
		t := ident(strings.TrimPrefix(recv, "("))
		return []string{t, ident(m), t + "." + ident(m)}
	case "type":
		t := ident(rest)
		if _, member, ok := strings.Cut(rest, ", "); ok && !strings.HasPrefix(member, "embedded ") {
			return []string{t, ident(member), t + "." + ident(member)}
		}
		return []string{t}
	case "const", "var":
		n, typ, _ := strings.Cut(rest, " ")
		if t := ident(typ); t != "" && !strings.Contains(t, ".") && t != "=" {
			return []string{n, t}
		}
		return []string{n}
	}
	return []string{ident(rest)}
}

// features lists the API of dir's public packages: every package of the module
// outside internal/ and docs/, but a command.
func features(dir string) ([]string, error) {
	cmd := exec.Command("go", "list", "-e", "-export", "-deps",
		"-json=ImportPath,Name,Export,Module,Error,DepOnly", "./...")
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go list: %v: %s", err, errb.String())
	}
	type pkg struct {
		ImportPath, Name, Export string
		DepOnly                  bool
		Module                   *struct{ Path string }
		Error                    *struct{ Err string }
	}
	exports := map[string]string{}
	var public []string
	dec := json.NewDecoder(&out)
	for {
		var p pkg
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		if p.Error != nil {
			return nil, fmt.Errorf("%s: %s", p.ImportPath, p.Error.Err)
		}
		exports[p.ImportPath] = p.Export
		if !p.DepOnly && p.Name != "main" && !strings.Contains(p.ImportPath, "/internal/") &&
			!strings.Contains(p.ImportPath, "/docs/") {
			public = append(public, p.ImportPath)
		}
	}
	slices.Sort(public)
	imp := importer.ForCompiler(token.NewFileSet(), "gc", func(path string) (io.ReadCloser, error) {
		f, ok := exports[path]
		if !ok || f == "" {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(f)
	})
	var feats []string
	for _, path := range public {
		p, err := imp.Import(path)
		if err != nil {
			return nil, err
		}
		feats = append(feats, packageFeatures(p)...)
	}
	slices.Sort(feats)
	return slices.Compact(feats), nil
}

func packageFeatures(p *types.Package) []string {
	w := &writer{pkg: p, alias: map[*types.TypeName]string{}, tpn: map[*types.TypeParam]string{}}
	var out []string
	emit := func(format string, args ...any) {
		out = append(out, "pkg "+p.Path()+", "+fmt.Sprintf(format, args...))
	}
	// An alias of an internal package's type is that type to a caller, under the
	// alias's name: ursus.Series was data.Series until v0.6.7, its methods data's.
	for _, name := range p.Scope().Names() {
		if o, ok := p.Scope().Lookup(name).(*types.TypeName); ok && o.Exported() && o.IsAlias() {
			if n, ok := types.Unalias(o.Type()).(*types.Named); ok && internal(n.Obj().Pkg()) {
				w.alias[n.Origin().Obj()] = name
			}
		}
	}
	for _, name := range p.Scope().Names() {
		obj := p.Scope().Lookup(name)
		if !obj.Exported() {
			continue
		}
		switch o := obj.(type) {
		case *types.Const:
			emit("const %s %s", name, w.typ(o.Type()))
			emit("const %s = %s", name, o.Val().ExactString())
		case *types.Var:
			emit("var %s %s", name, w.typ(o.Type()))
		case *types.Func:
			emit("func %s%s", name, w.sig(o.Type().(*types.Signature), true))
		case *types.TypeName:
			if !o.IsAlias() {
				named := o.Type().(*types.Named)
				w.named(emit, name, name+w.tparams(named.TypeParams(), "P"), named)
				continue
			}
			a := o.Type().(*types.Alias)
			head := name + w.tparams(a.TypeParams(), "P")
			if n, ok := types.Unalias(a).(*types.Named); ok && internal(n.Obj().Pkg()) {
				w.tparams(n.Origin().TypeParams(), "P")
				w.named(emit, name, head, n.Origin())
				continue
			}
			emit("type %s = %s", head, w.typ(types.Unalias(a)))
		}
	}
	return out
}

// internal reports whether p is one a caller cannot import.
func internal(p *types.Package) bool {
	return p != nil && (strings.Contains(p.Path(), "/internal/") || strings.HasSuffix(p.Path(), "/internal"))
}

// named emits a defined type's features under name: its kind, its exported fields or
// interface methods, and its exported methods, a pointer's too.
func (w *writer) named(emit func(string, ...any), name, head string, named *types.Named) {
	switch u := named.Underlying().(type) {
	case *types.Struct:
		emit("type %s struct", head)
		for i := range u.NumFields() {
			f := u.Field(i)
			switch {
			case f.Embedded():
				emit("type %s struct, embedded %s", name, w.typ(f.Type()))
			case f.Exported():
				emit("type %s struct, %s %s", name, f.Name(), w.typ(f.Type()))
			}
		}
	case *types.Interface:
		var ms []string
		hidden := false
		for i := range u.NumMethods() {
			m := u.Method(i)
			if !m.Exported() {
				hidden = true
				continue
			}
			ms = append(ms, m.Name())
			emit("type %s interface, %s%s", name, m.Name(), w.sig(m.Type().(*types.Signature), true))
		}
		if hidden {
			ms = append(ms, "unexported methods")
		}
		for i := range u.NumEmbeddeds() {
			emit("type %s interface, embedded %s", name, w.typ(u.EmbeddedType(i)))
		}
		emit("type %s interface { %s }", head, strings.Join(ms, ", "))
		return
	default:
		emit("type %s %s", head, w.typ(u))
	}
	// Every method a *T calls under (*T), and those a T calls under (T) as well: a
	// method moved from a pointer's receiver to a value's breaks no caller, and its
	// (*T) line stays; the other way, its (T) line goes.
	for _, recv := range []string{name, "*" + name} {
		t := types.Type(named)
		if recv != name {
			t = types.NewPointer(named)
		}
		ms := types.NewMethodSet(t)
		for i := range ms.Len() {
			if m := ms.At(i).Obj(); m.Exported() {
				emit("method (%s) %s%s", recv, m.Name(), w.sig(m.Type().(*types.Signature), true))
			}
		}
	}
}

// writer writes a type as a caller sees it: another package's names qualified by its
// name, this package's not, and a function's parameters without their names.
type writer struct {
	pkg   *types.Package
	alias map[*types.TypeName]string  // an internal type a caller knows by this package's alias
	tpn   map[*types.TypeParam]string // a type parameter's name by its place (tparams)
}

func (w *writer) qualify(p *types.Package) string {
	if p == nil || p == w.pkg {
		return ""
	}
	return p.Name()
}

func (w *writer) typ(t types.Type) string {
	switch t := t.(type) {
	case *types.Alias:
		return w.name(t.Obj(), t.TypeArgs())
	case *types.Named:
		return w.name(t.Obj(), t.TypeArgs())
	case *types.Pointer:
		return "*" + w.typ(t.Elem())
	case *types.Slice:
		return "[]" + w.typ(t.Elem())
	case *types.Array:
		return fmt.Sprintf("[%d]%s", t.Len(), w.typ(t.Elem()))
	case *types.Map:
		return "map[" + w.typ(t.Key()) + "]" + w.typ(t.Elem())
	case *types.Chan:
		switch t.Dir() {
		case types.SendOnly:
			return "chan<- " + w.typ(t.Elem())
		case types.RecvOnly:
			return "<-chan " + w.typ(t.Elem())
		}
		return "chan " + w.typ(t.Elem())
	case *types.Signature:
		return "func" + w.sig(t, false)
	case *types.Union:
		terms := make([]string, t.Len())
		for i := range t.Len() {
			terms[i] = w.typ(t.Term(i).Type())
			if t.Term(i).Tilde() {
				terms[i] = "~" + terms[i]
			}
		}
		return strings.Join(terms, " | ")
	case *types.TypeParam:
		if n, ok := w.tpn[t]; ok {
			return n
		}
		return t.Obj().Name()
	}
	return types.TypeString(t, w.qualify)
}

func (w *writer) name(obj *types.TypeName, args *types.TypeList) string {
	s := obj.Name()
	if a, ok := w.alias[obj]; ok {
		s = a
	} else if q := w.qualify(obj.Pkg()); q != "" {
		s = q + "." + s
	}
	if args.Len() > 0 {
		as := make([]string, args.Len())
		for i := range args.Len() {
			as[i] = w.typ(args.At(i))
		}
		s += "[" + strings.Join(as, ", ") + "]"
	}
	return s
}

// tparams writes a declaration's type parameters, each named by its place, prefix
// and its index: a type's P0, a function's or a method's own T0, so that renaming
// one, which no caller sees, changes no feature.
func (w *writer) tparams(tps *types.TypeParamList, prefix string) string {
	if tps.Len() == 0 {
		return ""
	}
	for i := range tps.Len() {
		w.tpn[tps.At(i)] = fmt.Sprintf("%s%d", prefix, i)
	}
	ps := make([]string, tps.Len())
	for i := range tps.Len() {
		ps[i] = w.tpn[tps.At(i)] + " " + w.typ(tps.At(i).Constraint())
	}
	return "[" + strings.Join(ps, ", ") + "]"
}

// sig writes a signature's own type parameters, where it is a declared function's or
// method's, and its parameters and results without their names.
func (w *writer) sig(s *types.Signature, own bool) string {
	var b strings.Builder
	// A method's receiver's type parameters are its type's, by their place.
	w.tparams(s.RecvTypeParams(), "P")
	if own {
		b.WriteString(w.tparams(s.TypeParams(), "T"))
	}
	b.WriteString("(")
	for i := range s.Params().Len() {
		if i > 0 {
			b.WriteString(", ")
		}
		t := s.Params().At(i).Type()
		if s.Variadic() && i == s.Params().Len()-1 {
			b.WriteString("..." + w.typ(t.(*types.Slice).Elem()))
			continue
		}
		b.WriteString(w.typ(t))
	}
	b.WriteString(")")
	switch r := s.Results(); r.Len() {
	case 0:
	case 1:
		b.WriteString(" " + w.typ(r.At(0).Type()))
	default:
		rs := make([]string, r.Len())
		for i := range r.Len() {
			rs[i] = w.typ(r.At(i).Type())
		}
		b.WriteString(" (" + strings.Join(rs, ", ") + ")")
	}
	return b.String()
}

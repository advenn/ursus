package shrink

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/model"
)

// The kinds of failure a Signature names.
const (
	KindMismatch = "mismatch" // two answers differ
	KindRefusal  = "refusal"  // one side refused where the other answered
	KindInternal = "internal" // ErrInternal, ursus's own "this is a bug in ursus"
	KindPanic    = "panic"    // a panic in ursus's code, or one that reached the caller
	KindAlloc    = "alloc"    // an allocation past fuzzcheck's bound
	KindLeak     = "leak"     // a goroutine left behind
	KindHang     = "hang"     // fuzzcheck's watchdog: it ends the process, so it is never shrunk
	KindRerun    = "rerun"    // one configuration, two answers
	KindSchema   = "schema"   // ursus's schema is not the AST's types
	KindSQL      = "sql"      // DuckDB refused the SQL: sqlgen's bug, or DuckDB's
	KindFuzz     = "fuzzcheck"
)

// Signature is how a case fails, stable enough that a shrink step keeps the same
// bug: the kind, and for a mismatch what differs, and for a differing cell the
// operation that computes its column and the column's type, followed back through
// the columns passed through or renamed, so a step that drops a rename keeps it;
// for a refusal the side that refused and its error's kind; for an internal error,
// a panic and the SQL their class: the error's first line with its numbers elided,
// or the function the panic was raised in.
type Signature struct {
	Kind  string `json:"kind"`
	What  string `json:"what,omitempty"`  // a mismatch's: compare.DiffCount, DiffColumns, DiffRows, DiffCell; a refusal's side
	Op    string `json:"op,omitempty"`    // a differing cell's column: the operation that computes it ("Min", "Add", "Scan")
	Type  string `json:"type,omitempty"`  // and its type
	Class string `json:"class,omitempty"` // a refusal's error kind; an internal error's, a panic's or the SQL's class
}

// Zero is whether s names no failure: the case passed.
func (s Signature) Zero() bool { return s.Kind == "" }

func (s Signature) String() string {
	if s.Zero() {
		return "none"
	}
	parts := []string{s.Kind}
	for _, p := range []string{s.What, s.Op, s.Type} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	out := strings.Join(parts, " ")
	if s.Class != "" {
		out += ": " + s.Class
	}
	return out
}

var (
	digits  = regexp.MustCompile(`[0-9]+`)
	hexAddr = regexp.MustCompile(`0x[0-9a-fA-F]+`)
)

// class is an error's first line, its numbers elided, so the same error over other
// rows is the same class.
func class(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	s = hexAddr.ReplaceAllString(s, "0xN")
	s = digits.ReplaceAllString(s, "N")
	if len(s) > 160 {
		s = s[:160]
	}
	return strings.TrimSpace(s)
}

// errKind is a refusal's class: the ursus error kind it is.
func errKind(err error) string {
	for _, k := range []struct {
		err  error
		name string
	}{
		{ursus.ErrSchema, "schema"}, {ursus.ErrType, "type"}, {ursus.ErrValue, "value"},
		{ursus.ErrUnsupported, "unsupported"}, {ursus.ErrIO, "io"}, {ursus.ErrResource, "resource"},
		{ursus.ErrInternal, "internal"},
	} {
		if errors.Is(err, k.err) {
			return k.name
		}
	}
	return "other"
}

// fuzzSignature is the signature of what fuzzcheck failed: its first failure.
func fuzzSignature(failure string) Signature {
	first := strings.TrimPrefix(failure, "fuzzcheck: ")
	first, _, _ = strings.Cut(first, "\n\nfuzzcheck: ")
	switch {
	case strings.HasPrefix(first, "an internal error"):
		_, rest, _ := strings.Cut(first, "\n")
		return Signature{Kind: KindInternal, Class: class(rest)}
	case strings.HasPrefix(first, "a panic"):
		return Signature{Kind: KindPanic, Class: panicSite(first)}
	case strings.HasPrefix(first, "allocated "):
		return Signature{Kind: KindAlloc}
	case strings.Contains(first, "goroutine(s) still running"):
		return Signature{Kind: KindLeak}
	}
	return Signature{Kind: KindFuzz, Class: class(first)}
}

// panicSite is the function a fuzzcheck panic failure names: "raised in F:", "in
// ursus's code, F:" or "module's, F:".
func panicSite(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	for _, marker := range []string{"raised in ", "ursus's code, ", "module's, "} {
		if _, rest, ok := strings.Cut(line, marker); ok {
			fn, _, _ := strings.Cut(rest, ":")
			fn, _, _ = strings.Cut(fn, ", answered")
			return strings.TrimSpace(fn)
		}
	}
	return class(line)
}

// producer is the operation that computes column name of f: an aggregate's or an
// expression's root operation, or "Scan" for a base table's column, followed back
// through the columns passed through or renamed.
func producer(f ast.Frame, name string, tables map[string]*model.Table) string {
	switch f := f.(type) {
	case *ast.Scan:
		return "Scan"
	case *ast.Filter:
		return producer(f.In, name, tables)
	case *ast.Sort:
		return producer(f.In, name, tables)
	case *ast.Head:
		return producer(f.In, name, tables)
	case *ast.Slice:
		return producer(f.In, name, tables)
	case *ast.Select:
		return namedProducer(f.In, f.Cols, name, tables)
	case *ast.GroupBy:
		return namedProducer(f.In, append(append([]ast.Named(nil), f.Keys...), f.Aggs...), name, tables)
	case *ast.WithColumns:
		for i := len(f.Cols) - 1; i >= 0; i-- {
			if n := f.Cols[i]; n.Name == name {
				if c, ok := n.E.(*ast.Col); ok {
					// It reads a column the ones before it may have added.
					return producer(&ast.WithColumns{In: f.In, Cols: f.Cols[:i]}, c.Name, tables)
				}
				return exprOp(n.E)
			}
		}
		return producer(f.In, name, tables)
	case *ast.Join:
		if f.How == ast.Semi || f.How == ast.Anti {
			return producer(f.L, name, tables)
		}
		l, err := ast.Schema(f.L, tables)
		if err != nil {
			return "?"
		}
		if l.Col(name) != nil {
			return producer(f.L, name, tables)
		}
		if base, ok := strings.CutSuffix(name, "_right"); ok && l.Col(base) != nil {
			return producer(f.R, base, tables)
		}
		return producer(f.R, name, tables)
	}
	return "?"
}

func namedProducer(in ast.Frame, cols []ast.Named, name string, tables map[string]*model.Table) string {
	for _, n := range cols {
		if n.Name != name {
			continue
		}
		if c, ok := n.E.(*ast.Col); ok {
			return producer(in, c.Name, tables)
		}
		return exprOp(n.E)
	}
	return "?"
}

// exprOp is an expression's root operation.
func exprOp(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Col:
		return "Col"
	case *ast.Lit:
		return "Lit"
	case *ast.Bin:
		return e.Op.String()
	case *ast.Un:
		return e.Op.String()
	case *ast.When:
		return "When"
	case *ast.FillNull:
		return "FillNull"
	case *ast.Cast:
		if e.Lossy {
			return "CastLossy"
		}
		return "Cast"
	case *ast.Agg:
		return e.Op.String()
	}
	return fmt.Sprintf("%T", e)
}

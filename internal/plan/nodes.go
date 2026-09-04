package plan

import (
	"strconv"
	"strings"

	"ursus/dtype"
	"ursus/internal/expr"
	"ursus/internal/uerr"
)

// --- Scan --------------------------------------------------------------------

// Scan reads from a Source.
//
// Projection nil means "every column"; a non-nil (possibly empty) slice is the
// exact set to produce, in that order. Projection pushdown fills it in, which is
// the entire reason Scan exists as a distinct node from Source: the optimizer
// needs somewhere to record what it worked out.
//
// Full is the source's complete schema, captured during Resolve so that later
// phases never need I/O or a context to answer a schema question.
//
// # Projection is the EMIT set, not the READ set
//
// Projection is what the scan produces, and it is what Schema reports. The
// columns the source must decode are a superset: a conjunct in Predicate that was
// classified Exact is deleted from the plan entirely, so nothing above the scan
// mentions its columns, yet the source cannot evaluate it without reading them.
//
//	Filter(c > 5).Select(a)   with c > 5 Exact
//	  =>  Scan{Projection: [a], Predicate: [c > 5]}
//
// The source reads {a, c} and emits {a}. That is not a wart to be fixed by
// widening Projection — widening it would make the scan emit a column the plan
// above does not expect, changing the output schema. It is a documented part of
// the contract; see source.ScanSpec, which says the same thing to implementors.
type Scan struct {
	Src  Source
	Full *dtype.Schema // the source's whole schema; set by Resolve

	// Projection is the exact column list to emit, in order. nil = all columns.
	Projection []string

	// Predicate holds conjuncts the source claimed it could use, and NOTHING
	// about what it guarantees. A conjunct here may be Exact (removed from the
	// plan) or Inexact (still present in a Filter above this node) — the Scan does
	// not record which, because it does not need to: whatever the source fails to
	// remove, the surviving Filter removes.
	//
	// A conjunct reaches this field only via predicatePushdown, which asks the
	// source with ClassifyPredicates first. Putting one here by hand asserts that
	// the source can honour it.
	Predicate []expr.Node

	// MaxRows is a hint that no more than this many rows will be consumed. 0 means
	// unlimited — chosen so the zero value is "read everything", since the failure
	// mode of a mistaken 0 with the opposite convention is an empty result.
	//
	// It is a HINT. A source may return more, and the Limit node that produced it
	// always remains in the plan to enforce the bound.
	MaxRows int
}

func (s *Scan) planNode()        {}
func (s *Scan) Children() []Node { return nil }

func (s *Scan) WithChildren(kids []Node) Node {
	if len(kids) != 0 {
		panic("plan: Scan takes no children")
	}
	return s
}

func (s *Scan) Schema() (*dtype.Schema, error) {
	if s.Full == nil {
		return nil, uerr.Internalf("plan: Scan.Schema called before Resolve")
	}
	if s.Projection == nil {
		return s.Full, nil
	}
	return s.Full.Select(s.Projection)
}

func (s *Scan) Label() string {
	var b strings.Builder
	b.WriteString(strings.ToUpper(s.Src.Name()))
	b.WriteString(" SCAN")
	if d := s.Src.Describe(); d != "" {
		b.WriteString(" ")
		b.WriteString(d)
	}
	return b.String()
}

// WithProjection returns a copy of the scan projecting the given columns.
func (s *Scan) WithProjection(cols []string) *Scan {
	c := *s
	c.Projection = cols
	return &c
}

// WithMaxRows returns a copy of the scan carrying a row-count hint.
func (s *Scan) WithMaxRows(n int) *Scan {
	c := *s
	c.MaxRows = n
	return &c
}

// ReadSet returns the columns the source must decode: the emit set plus every
// column referenced by a pushed-down predicate.
//
// Nothing in the plan consumes this — the scan's schema is the emit set and that
// is what the operators above see. It exists so Explain can show the truth: a
// scan rendered as "projection: [a] (1/3 cols)" while quietly reading a second
// column for its predicate is a plan that lies to whoever is debugging it.
func (s *Scan) ReadSet() []string {
	if s.Full == nil {
		return s.Projection
	}
	want := make(map[string]struct{}, s.Full.Len())
	if s.Projection == nil {
		for _, n := range s.Full.Names() {
			want[n] = struct{}{}
		}
	} else {
		for _, n := range s.Projection {
			want[n] = struct{}{}
		}
	}
	for _, p := range s.Predicate {
		for _, n := range expr.RootNames(p) {
			want[n] = struct{}{}
		}
	}
	// Source order, so the rendering is deterministic for golden files.
	out := make([]string, 0, len(want))
	for _, n := range s.Full.Names() {
		if _, ok := want[n]; ok {
			out = append(out, n)
		}
	}
	return out
}

// --- Filter ------------------------------------------------------------------

// Filter keeps rows where every predicate is true.
//
// Preds is a conjunct slice rather than a single AND tree so that predicate
// pushdown can move conjuncts independently — the common case is that some
// conjuncts can reach the scan and others cannot.
//
// # Null semantics
//
// A row is kept iff the predicate is VALID AND TRUE. A null predicate drops the
// row, matching SQL's WHERE. This is worth stating explicitly because the
// alternative (treating null as true) is a plausible-looking choice that silently
// changes results, and because `Not(p)` therefore does not partition the rows:
// a row where p is null is dropped by both `Filter(p)` and `Filter(Not(p))`.
type Filter struct {
	Input Node
	Preds []expr.Node
}

func (f *Filter) planNode()        {}
func (f *Filter) Children() []Node { return []Node{f.Input} }

func (f *Filter) WithChildren(kids []Node) Node {
	if len(kids) != 1 {
		panic("plan: Filter takes exactly one child")
	}
	c := *f
	c.Input = kids[0]
	return &c
}

// Schema is the input schema unchanged: filtering removes rows, never columns.
func (f *Filter) Schema() (*dtype.Schema, error) { return f.Input.Schema() }

func (f *Filter) Label() string {
	parts := make([]string, len(f.Preds))
	for i, p := range f.Preds {
		parts[i] = p.String()
	}
	return "FILTER [" + strings.Join(parts, " AND ") + "]"
}

// --- Project -----------------------------------------------------------------

// Project computes a new set of columns from its input.
//
// This backs Select. After Resolve, every expression produces exactly one column,
// so the output schema is one field per element of Exprs, in order.
type Project struct {
	Input Node
	Exprs []expr.Node
}

func (p *Project) planNode()        {}
func (p *Project) Children() []Node { return []Node{p.Input} }

func (p *Project) WithChildren(kids []Node) Node {
	if len(kids) != 1 {
		panic("plan: Project takes exactly one child")
	}
	c := *p
	c.Input = kids[0]
	return &c
}

func (p *Project) Schema() (*dtype.Schema, error) {
	in, err := p.Input.Schema()
	if err != nil {
		return nil, err
	}
	fields := make([]dtype.Field, len(p.Exprs))
	for i, e := range p.Exprs {
		f, err := expr.Resolve(e, in)
		if err != nil {
			return nil, uerr.Annotate(err, "select", "Project")
		}
		fields[i] = f
	}
	return dtype.NewSchema(fields...)
}

func (p *Project) Label() string {
	return "PROJECT [" + expr.StringAll(p.Exprs) + "]"
}

// --- Limit -------------------------------------------------------------------

// Limit keeps at most N rows from the start of its input.
//
// It is in the skeleton because head() is the first thing anyone types when
// exploring, and because it gives slice pushdown somewhere to land later.
type Limit struct {
	Input Node
	N     int
}

func (l *Limit) planNode()        {}
func (l *Limit) Children() []Node { return []Node{l.Input} }

func (l *Limit) WithChildren(kids []Node) Node {
	if len(kids) != 1 {
		panic("plan: Limit takes exactly one child")
	}
	c := *l
	c.Input = kids[0]
	return &c
}

func (l *Limit) Schema() (*dtype.Schema, error) { return l.Input.Schema() }
func (l *Limit) Label() string                  { return "LIMIT " + strconv.Itoa(l.N) }

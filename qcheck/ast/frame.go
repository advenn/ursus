package ast

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/advenn/ursus/qcheck/model"
)

// Frame is a typed frame operation.
type Frame interface {
	// Inputs are the frames it reads.
	Inputs() []Frame
	// Op names the operation, for the anti-vacuity counts.
	Op() string
}

// Named is an output column: E, named Name. A Col of the same name is passed
// through as it is; anything else is aliased.
type Named struct {
	Name string
	E    Expr
}

// Scan reads a generated table through ScanArrowRecords.
type Scan struct{ Table string }

// Filter keeps the rows Pred holds of.
type Filter struct {
	In   Frame
	Pred Expr
}

// Select is the columns Cols, in order.
type Select struct {
	In   Frame
	Cols []Named
}

// WithColumns adds Cols, replacing a column of the same name in place.
type WithColumns struct {
	In   Frame
	Cols []Named
}

// GroupBy aggregates by Keys, none for one row of the whole frame. Without
// MaintainOrder its groups come in no promised order (agg.go), and it runs on every
// thread.
type GroupBy struct {
	In            Frame
	Keys          []Named
	Aggs          []Named
	MaintainOrder bool
}

// SortKey is a column to sort by.
type SortKey struct {
	Col             string
	Desc, NullsLast bool
}

// Sort orders the rows, stably.
type Sort struct {
	In   Frame
	Keys []SortKey
}

// Head is the first N rows.
type Head struct {
	In Frame
	N  int
}

// Slice is Len rows from Offset; a negative Offset counts from the end, a negative
// Len is to the end (frameops.go).
type Slice struct {
	In          Frame
	Offset, Len int
}

// JoinHow is a join's kind.
type JoinHow uint8

const (
	Inner JoinHow = iota
	Left
	Semi
	Anti
)

var howNames = [...]string{"Inner", "Left", "Semi", "Anti"}

func (h JoinHow) String() string { return howNames[h] }

// Join joins L and R on one key, LeftOn of L equal to RightOn of R. With
// MaintainOrder it keeps the left frame's order, JoinOrderLeft.
type Join struct {
	L, R            Frame
	How             JoinHow
	LeftOn, RightOn string
	MaintainOrder   bool
}

func (f *Scan) Inputs() []Frame        { return nil }
func (f *Filter) Inputs() []Frame      { return []Frame{f.In} }
func (f *Select) Inputs() []Frame      { return []Frame{f.In} }
func (f *WithColumns) Inputs() []Frame { return []Frame{f.In} }
func (f *GroupBy) Inputs() []Frame     { return []Frame{f.In} }
func (f *Sort) Inputs() []Frame        { return []Frame{f.In} }
func (f *Head) Inputs() []Frame        { return []Frame{f.In} }
func (f *Slice) Inputs() []Frame       { return []Frame{f.In} }
func (f *Join) Inputs() []Frame        { return []Frame{f.L, f.R} }

func (f *Scan) Op() string        { return "Scan" }
func (f *Filter) Op() string      { return "Filter" }
func (f *Select) Op() string      { return "Select" }
func (f *WithColumns) Op() string { return "WithColumns" }
func (f *GroupBy) Op() string {
	if len(f.Keys) == 0 {
		return "GroupBy()"
	}
	return "GroupBy"
}
func (f *Sort) Op() string  { return "Sort" }
func (f *Head) Op() string  { return "Head" }
func (f *Slice) Op() string { return "Slice" }
func (f *Join) Op() string  { return "Join" + f.How.String() }

// Query is a frame and what is asked of it: the frame collected, or its Count.
type Query struct {
	Root  Frame
	Count bool
}

// Walk calls fn on f and every frame below it, inputs first.
func Walk(f Frame, fn func(Frame)) {
	for _, in := range f.Inputs() {
		Walk(in, fn)
	}
	fn(f)
}

// Ops are the operations of q, each once, in order of first appearance; Count
// among them when asked.
func (q *Query) Ops() []string {
	var out []string
	Walk(q.Root, func(f Frame) {
		if op := f.Op(); !slices.Contains(out, op) {
			out = append(out, op)
		}
	})
	if q.Count {
		out = append(out, "Count")
	}
	return out
}

// --- what a frame holds -----------------------------------------------------------

// Column is what a frame's column is.
type Column struct {
	Name string
	Type model.Type
	// Origin is the base column, "table.column", whose values it holds as they
	// were read, or "" for a computed one.
	Origin string
	// Noisy is a float Sum or Mean of a group-by that runs on every thread, whose
	// last bits depend on the thread count (feature-gaps §1, "not bit-exact"). No
	// expression reads one, so the difference never moves a row.
	Noisy bool
}

// Info is what a frame holds and promises.
type Info struct {
	Cols []Column
	// Ordered is whether its row order is promised: a scan's, kept by the
	// row-wise operations and Sort; lost by a group-by without MaintainOrder and by
	// an inner or left join without JoinMaintainOrder (join.go: "the order is not
	// promised").
	Ordered bool
	// Keys are sets of columns no two rows share: a group-by's keys. A Sort by a
	// superset of one orders the rows totally.
	Keys [][]string
	// OneRow is a keyless group-by's single row.
	OneRow bool
}

// Col is the column named name, or nil.
func (in *Info) Col(name string) *Column {
	for i := range in.Cols {
		if in.Cols[i].Name == name {
			return &in.Cols[i]
		}
	}
	return nil
}

// Schema is a frame's Info, or an error naming what is ill-formed.
func Schema(f Frame, tables map[string]*model.Table) (*Info, error) {
	switch f := f.(type) {
	case *Scan:
		t := tables[f.Table]
		if t == nil {
			return nil, fmt.Errorf("no table %q", f.Table)
		}
		in := &Info{Ordered: true}
		for _, c := range t.Cols {
			in.Cols = append(in.Cols, Column{Name: c.Name, Type: c.Type, Origin: t.Name + "." + c.Name})
		}
		return in, nil
	case *Filter:
		in, err := Schema(f.In, tables)
		if err != nil {
			return nil, err
		}
		if err := checkExpr(f.Pred, in, false); err != nil {
			return nil, fmt.Errorf("Filter: %w", err)
		}
		if f.Pred.Type() != model.Bool {
			return nil, fmt.Errorf("Filter of %s", f.Pred.Type())
		}
		return in, nil
	case *Select:
		in, err := Schema(f.In, tables)
		if err != nil {
			return nil, err
		}
		out := &Info{Ordered: in.Ordered, OneRow: in.OneRow}
		if err := project(f.Cols, in, out, false); err != nil {
			return nil, fmt.Errorf("Select: %w", err)
		}
		out.Keys = keptKeys(in.Keys, func(name string) bool {
			for _, n := range f.Cols {
				if c, ok := n.E.(*Col); ok && n.Name == name && c.Name == name {
					return true
				}
			}
			return false
		})
		return out, nil
	case *WithColumns:
		in, err := Schema(f.In, tables)
		if err != nil {
			return nil, err
		}
		// Each expression sees the columns the ones before it added or replaced
		// (agg.go: "Later expressions can reference columns that earlier ones added").
		out := &Info{Ordered: in.Ordered, OneRow: in.OneRow, Cols: slices.Clone(in.Cols)}
		for _, n := range f.Cols {
			if err := checkExpr(n.E, out, false); err != nil {
				return nil, fmt.Errorf("WithColumns: %w", err)
			}
			c := column(n, out)
			if old := out.Col(n.Name); old != nil {
				if old.Noisy {
					return nil, fmt.Errorf("WithColumns replaces noisy %q", n.Name)
				}
				*old = c
			} else {
				out.Cols = append(out.Cols, c)
			}
		}
		out.Keys = keptKeys(in.Keys, func(name string) bool {
			for _, n := range f.Cols {
				if n.Name == name {
					return false
				}
			}
			return true
		})
		return out, nil
	case *GroupBy:
		in, err := Schema(f.In, tables)
		if err != nil {
			return nil, err
		}
		// MaintainOrder is the order each group first appears in the input, so it
		// promises an order only of input in one.
		out := &Info{Ordered: f.MaintainOrder && in.Ordered || len(f.Keys) == 0, OneRow: len(f.Keys) == 0}
		for _, k := range f.Keys {
			if !k.E.Type().IsSignedInt() || k.E.Type() == model.Int128 {
				return nil, fmt.Errorf("GroupBy key %q is %s", k.Name, k.E.Type())
			}
		}
		if err := project(f.Keys, in, out, false); err != nil {
			return nil, fmt.Errorf("GroupBy keys: %w", err)
		}
		for _, a := range f.Aggs {
			agg, ok := a.E.(*Agg)
			if !ok {
				return nil, fmt.Errorf("GroupBy aggregate %q is not an aggregate", a.Name)
			}
			if err := checkExpr(a.E, in, true); err != nil {
				return nil, fmt.Errorf("GroupBy aggregate: %w", err)
			}
			if out.Col(a.Name) != nil {
				return nil, fmt.Errorf("GroupBy names %q twice", a.Name)
			}
			noisy := !f.MaintainOrder && (agg.Op == Sum || agg.Op == Mean) &&
				agg.X != nil && agg.X.Type() == model.Float64
			out.Cols = append(out.Cols, Column{Name: a.Name, Type: a.E.Type(), Noisy: noisy})
		}
		if len(f.Keys) > 0 {
			ks := make([]string, len(f.Keys))
			for i, k := range f.Keys {
				ks[i] = k.Name
			}
			out.Keys = [][]string{ks}
		}
		return out, nil
	case *Sort:
		in, err := Schema(f.In, tables)
		if err != nil {
			return nil, err
		}
		names := make([]string, len(f.Keys))
		for i, k := range f.Keys {
			c := in.Col(k.Col)
			if c == nil {
				return nil, fmt.Errorf("Sort by %q, which is not a column", k.Col)
			}
			if c.Noisy {
				return nil, fmt.Errorf("Sort by noisy %q", k.Col)
			}
			names[i] = k.Col
		}
		out := *in
		out.Ordered = in.Ordered || in.OneRow || covers(names, in.Keys)
		return &out, nil
	case *Head:
		in, err := Schema(f.In, tables)
		if err != nil {
			return nil, err
		}
		if !in.Ordered {
			return nil, fmt.Errorf("Head of rows in no promised order")
		}
		return in, nil
	case *Slice:
		in, err := Schema(f.In, tables)
		if err != nil {
			return nil, err
		}
		if !in.Ordered {
			return nil, fmt.Errorf("Slice of rows in no promised order")
		}
		return in, nil
	case *Join:
		return joinSchema(f, tables)
	}
	return nil, fmt.Errorf("unknown frame %T", f)
}

// project checks cols over in and appends them to out. A column passed through by
// its own name may be noisy: it is carried, not read.
func project(cols []Named, in, out *Info, aggs bool) error {
	for _, n := range cols {
		if c, ok := n.E.(*Col); ok && c.Name == n.Name {
			if src := in.Col(c.Name); src == nil || src.Type != c.T {
				return fmt.Errorf("passes through %q, which is not a %s column", c.Name, c.T)
			}
		} else if err := checkExpr(n.E, in, aggs); err != nil {
			return err
		}
		if out.Col(n.Name) != nil {
			return fmt.Errorf("names %q twice", n.Name)
		}
		out.Cols = append(out.Cols, column(n, in))
	}
	return nil
}

// column is the column n makes over in: a column passed through keeps its origin
// and noise.
func column(n Named, in *Info) Column {
	if c, ok := n.E.(*Col); ok {
		src := in.Col(c.Name)
		return Column{Name: n.Name, Type: c.T, Origin: src.Origin, Noisy: src.Noisy}
	}
	return Column{Name: n.Name, Type: n.E.Type()}
}

// keptKeys are the key sets whose every column passes through, as kept says.
func keptKeys(keys [][]string, kept func(string) bool) [][]string {
	var out [][]string
	for _, k := range keys {
		if !slices.ContainsFunc(k, func(name string) bool { return !kept(name) }) {
			out = append(out, k)
		}
	}
	return out
}

// covers is whether names hold every column of some key set.
func covers(names []string, keys [][]string) bool {
	for _, k := range keys {
		all := true
		for _, c := range k {
			if !slices.Contains(names, c) {
				all = false
			}
		}
		if all {
			return true
		}
	}
	return false
}

// checkExpr checks that e reads columns in has, with their types, reads no noisy
// one, and holds an aggregate only where aggs allows, at its root.
func checkExpr(e Expr, in *Info, aggs bool) error {
	if a, ok := e.(*Agg); ok {
		if !aggs {
			return fmt.Errorf("an aggregate outside a group-by")
		}
		if a.X != nil {
			if IsAgg(a.X) {
				return fmt.Errorf("an aggregate of an aggregate")
			}
			return checkExpr(a.X, in, false)
		}
		return nil
	}
	if err := retype(e); err != nil {
		return err
	}
	if c, ok := e.(*Col); ok {
		src := in.Col(c.Name)
		if src == nil {
			return fmt.Errorf("no column %q", c.Name)
		}
		if src.Type != c.T {
			return fmt.Errorf("column %q is %s, read as %s", c.Name, src.Type, c.T)
		}
		if src.Noisy {
			return fmt.Errorf("reads noisy %q", c.Name)
		}
	}
	for _, k := range e.Kids() {
		if err := checkExpr(k, in, false); err != nil {
			return err
		}
	}
	return nil
}

// retype rebuilds e's own node from its operands' types and checks it agrees.
func retype(e Expr) error {
	var again Expr
	var err error
	switch e := e.(type) {
	case *Col, *Lit:
		return nil
	case *Bin:
		again, err = NewBin(e.Op, e.L, e.R)
	case *Un:
		again, err = NewUn(e.Op, e.X)
	case *When:
		again, err = NewWhen(e.Cond, e.Then, e.Else)
	case *FillNull:
		again, err = NewFillNull(e.X, e.With)
	case *Cast:
		again, err = NewCast(e.X, e.To, e.Lossy)
	case *Agg:
		again, err = NewAgg(e.Op, e.X)
	default:
		return fmt.Errorf("unknown expression %T", e)
	}
	if err != nil {
		return err
	}
	if again.Type() != e.Type() {
		return fmt.Errorf("%s is typed %s, its operands make %s", e.Go(), e.Type(), again.Type())
	}
	return nil
}

func joinSchema(f *Join, tables map[string]*model.Table) (*Info, error) {
	l, err := Schema(f.L, tables)
	if err != nil {
		return nil, err
	}
	r, err := Schema(f.R, tables)
	if err != nil {
		return nil, err
	}
	lk, rk := l.Col(f.LeftOn), r.Col(f.RightOn)
	if lk == nil || rk == nil {
		return nil, fmt.Errorf("Join on %q = %q, not columns", f.LeftOn, f.RightOn)
	}
	if lk.Noisy || rk.Noisy {
		return nil, fmt.Errorf("Join on a noisy column")
	}
	kt, ok := JoinKeyType(lk.Type, rk.Type)
	if !ok {
		return nil, fmt.Errorf("Join on %s = %s", lk.Type, rk.Type)
	}
	for _, c := range r.Cols {
		if c.Noisy {
			return nil, fmt.Errorf("Join's right side holds noisy %q", c.Name)
		}
	}
	out := &Info{}
	switch f.How {
	case Semi, Anti:
		out.Cols = slices.Clone(l.Cols)
		out.Ordered = l.Ordered
		out.Keys = l.Keys
		out.OneRow = l.OneRow
		return out, nil
	}
	coalesce := f.LeftOn == f.RightOn
	out.Cols = slices.Clone(l.Cols)
	if coalesce {
		out.Col(f.LeftOn).Type = kt
	}
	for _, c := range r.Cols {
		if coalesce && c.Name == f.RightOn {
			continue
		}
		if l.Col(c.Name) != nil {
			c.Name += "_right"
			if out.Col(c.Name) != nil {
				return nil, fmt.Errorf("Join makes %q twice", c.Name)
			}
		}
		out.Cols = append(out.Cols, c)
	}
	out.Ordered = f.MaintainOrder && l.Ordered && r.Ordered
	return out, nil
}

// JoinKeyType is the type two join keys meet at, the coalesced key's: two integers
// at the wider, text with text, a float with a float.
func JoinKeyType(l, r model.Type) (model.Type, bool) {
	switch {
	case l == r && (l == model.String || l == model.Float64 || l.IsSignedInt()):
		return l, true
	case l.IsSignedInt() && r.IsSignedInt() && l != model.Int128 && r != model.Int128:
		t, _ := model.Promote(l, r)
		return t, true
	}
	return model.Invalid, false
}

// Check is whether q is well-typed and well-formed over tables: every node's type
// rebuilt from its leaves, every column read is there, no Head or Slice of rows in
// no promised order, no expression reading a noisy column.
func Check(q *Query, tables map[string]*model.Table) error {
	_, err := Schema(q.Root, tables)
	return err
}

// --- Go source ------------------------------------------------------------------

// Go is q as Go source over ursus's API, the tables named as Scan names them.
func (q *Query) Go() string {
	var b strings.Builder
	b.WriteString("q := ")
	writeFrame(&b, q.Root, "\t")
	b.WriteString("\n")
	if q.Count {
		b.WriteString("n, err := q.Count(ctx, opts...)")
	} else {
		b.WriteString("df, err := q.Collect(ctx, opts...)")
	}
	return b.String()
}

func writeFrame(b *strings.Builder, f Frame, indent string) {
	if s, ok := f.(*Scan); ok {
		fmt.Fprintf(b, "ursus.ScanArrowRecords(%s...)", s.Table)
		return
	}
	if j, ok := f.(*Join); ok {
		writeFrame(b, j.L, indent)
		b.WriteString(".\n" + indent + "Join(")
		writeFrame(b, j.R, indent+"\t")
		if j.LeftOn == j.RightOn {
			fmt.Fprintf(b, ", ursus.JoinOn(ursus.Col(%q))", j.LeftOn)
		} else {
			fmt.Fprintf(b, ", ursus.JoinLeftOn(ursus.Col(%q)), ursus.JoinRightOn(ursus.Col(%q))", j.LeftOn, j.RightOn)
		}
		fmt.Fprintf(b, ", ursus.JoinHow(ursus.Join%s)", j.How)
		if j.MaintainOrder {
			b.WriteString(", ursus.JoinMaintainOrder(ursus.JoinOrderLeft)")
		}
		b.WriteString(")")
		return
	}
	writeFrame(b, f.Inputs()[0], indent)
	b.WriteString(".\n" + indent)
	switch f := f.(type) {
	case *Filter:
		b.WriteString("Filter(" + f.Pred.Go() + ")")
	case *Select:
		b.WriteString("Select(" + named(f.Cols) + ")")
	case *WithColumns:
		b.WriteString("WithColumns(" + named(f.Cols) + ")")
	case *GroupBy:
		b.WriteString("GroupBy(" + named(f.Keys) + ")")
		if f.MaintainOrder {
			b.WriteString(".MaintainOrder()")
		}
		b.WriteString(".Agg(" + named(f.Aggs) + ")")
	case *Sort:
		keys := make([]string, len(f.Keys))
		for i, k := range f.Keys {
			dir := "Asc"
			if k.Desc {
				dir = "Desc"
			}
			keys[i] = fmt.Sprintf("ursus.%s(ursus.Col(%q))", dir, k.Col)
			if k.NullsLast {
				keys[i] += ".NullsLast()"
			}
		}
		b.WriteString("Sort(" + strings.Join(keys, ", ") + ")")
	case *Head:
		b.WriteString("Head(" + strconv.Itoa(f.N) + ")")
	case *Slice:
		fmt.Fprintf(b, "Slice(%d, %d)", f.Offset, f.Len)
	}
}

func named(cols []Named) string {
	parts := make([]string, len(cols))
	for i, n := range cols {
		parts[i] = n.E.Go()
		if c, ok := n.E.(*Col); !ok || c.Name != n.Name {
			parts[i] += ".Alias(" + strconv.Quote(n.Name) + ")"
		}
	}
	return strings.Join(parts, ", ")
}

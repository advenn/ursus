package shrink

import (
	"math"
	"slices"
	"unicode/utf8"

	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/model"
	"github.com/advenn/ursus/qcheck/self"
)

// Each pass tries its steps on *cur, keeping each that fails alike and costs less
// (shrinker.try), and says whether it kept any. A pass that keeps a step starts
// over from the case it made.

// passFactors takes the failing configuration to the fewest factors that still
// fail, by the self check's bisection, then to lower levels of those it keeps: four
// threads for eight, two records for more.
func (sh *shrinker) passFactors(cur **state) bool {
	if sh.k.mode == ModeSchema {
		return false
	}
	progress := false
	base := model.Reference()
	s := *cur
	if sh.k.mode != ModeDifferential && s.cfg != base {
		// DuckDB, a run or a rerun may fail at the reference itself.
		cand := s.clone()
		cand.cfg = base
		if sh.try(cur, cand, "factors") {
			return true
		}
	}
	if s.cfg.Diff(base) != 0 {
		self.Bisect(base, s.cfg, func(cfg model.Config) bool {
			if cfg == s.cfg {
				return false
			}
			cand := s.clone()
			cand.cfg = cfg
			if sh.try(cur, cand, "factors") {
				progress = true
				return true
			}
			return false
		})
	}
	for _, lower := range []func(model.Config) model.Config{
		func(c model.Config) model.Config {
			if c.Threads > 4 {
				c.Threads = 4
			}
			return c
		},
		func(c model.Config) model.Config {
			if c.Records > 2 {
				c.Records = 2
			}
			return c
		},
	} {
		s := *cur
		if cfg := lower(s.cfg); cfg != s.cfg {
			cand := s.clone()
			cand.cfg = cfg
			if sh.try(cur, cand, "factors") {
				progress = true
			}
		}
	}
	return progress
}

// passRows is ddmin on each table's rows: the table emptied, then halves and their
// complements, then finer cuts, as Zeller's delta debugging.
func (sh *shrinker) passRows(cur **state) bool {
	progress := false
	for ti := range (*cur).tables {
		base, err := (*cur).tables[ti].Held()
		if err != nil || base.Rows == 0 {
			continue
		}
		with := func(rows []int) bool {
			cand := (*cur).clone()
			cand.tables[ti] = selectRows(base, rows)
			return sh.try(cur, cand, "rows")
		}
		if with(nil) {
			progress = true
			continue
		}
		rows := make([]int, base.Rows)
		for i := range rows {
			rows[i] = i
		}
		n := 2
		for len(rows) >= 2 && !sh.spent() {
			chunks := split(rows, n)
			reduced := false
			for _, c := range chunks {
				if with(c) {
					rows, n, reduced = c, 2, true
					break
				}
			}
			if !reduced && n > 2 {
				for i := range chunks {
					comp := complement(chunks, i)
					if with(comp) {
						rows, n, reduced = comp, max(n-1, 2), true
						break
					}
				}
			}
			if reduced {
				progress = true
				continue
			}
			if n >= len(rows) {
				break
			}
			n = min(2*n, len(rows))
		}
	}
	return progress
}

// split cuts rows into n chunks as even as can be.
func split(rows []int, n int) [][]int {
	var out [][]int
	start := 0
	for i := range n {
		end := start + (len(rows)-start)/(n-i)
		if end > start {
			out = append(out, rows[start:end])
		}
		start = end
	}
	return out
}

func complement(chunks [][]int, skip int) []int {
	var out []int
	for i, c := range chunks {
		if i != skip {
			out = append(out, c...)
		}
	}
	return out
}

// selectRows is base with only rows, in order.
func selectRows(base *Table, rows []int) *Table {
	t := &Table{Name: base.Name, Seed: base.Seed, Rows: len(rows)}
	for _, c := range base.Cols {
		nc := Column{Name: c.Name, Type: c.Type, Values: make([]Value, len(rows))}
		for i, r := range rows {
			nc.Values[i] = c.Values[r]
		}
		t.Cols = append(t.Cols, nc)
	}
	return t
}

// frames are q's frames, the root first.
func frames(q *ast.Query) []ast.Frame {
	var out []ast.Frame
	ast.Walk(q.Root, func(f ast.Frame) { out = append(out, f) })
	slices.Reverse(out)
	return out
}

// replaceFrame is root with target, found by identity, replaced by repl.
func replaceFrame(root, target, repl ast.Frame) ast.Frame {
	if root == target {
		return repl
	}
	switch f := root.(type) {
	case *ast.Scan:
		return f
	case *ast.Filter:
		c := *f
		c.In = replaceFrame(f.In, target, repl)
		return &c
	case *ast.Select:
		c := *f
		c.In = replaceFrame(f.In, target, repl)
		return &c
	case *ast.WithColumns:
		c := *f
		c.In = replaceFrame(f.In, target, repl)
		return &c
	case *ast.GroupBy:
		c := *f
		c.In = replaceFrame(f.In, target, repl)
		return &c
	case *ast.Sort:
		c := *f
		c.In = replaceFrame(f.In, target, repl)
		return &c
	case *ast.Head:
		c := *f
		c.In = replaceFrame(f.In, target, repl)
		return &c
	case *ast.Slice:
		c := *f
		c.In = replaceFrame(f.In, target, repl)
		return &c
	case *ast.Join:
		c := *f
		c.L = replaceFrame(f.L, target, repl)
		c.R = replaceFrame(f.R, target, repl)
		return &c
	}
	return root
}

// withFrame is s with frame target replaced by repl.
func withFrame(s *state, target, repl ast.Frame) *state {
	c := s.clone()
	c.q = &ast.Query{Root: replaceFrame(s.q.Root, target, repl), Count: s.q.Count}
	return c
}

// passOps drops each operation, the root's first: the frame replaced by its input,
// a join by its left side or its right.
func (sh *shrinker) passOps(cur **state) bool {
	progress := false
	for again := true; again && !sh.spent(); {
		again = false
	scan:
		for _, f := range frames((*cur).q) {
			for _, in := range f.Inputs() {
				if sh.try(cur, withFrame(*cur, f, in), "ops") {
					progress, again = true, true
					break scan
				}
			}
		}
	}
	return progress
}

// passParts drops the parts of an operation: an aggregation, a key, a column, a sort
// key; and turns MaintainOrder off.
func (sh *shrinker) passParts(cur **state) bool {
	progress := false
	for again := true; again && !sh.spent(); {
		again = false
	scan:
		for _, f := range frames((*cur).q) {
			for _, repl := range parts(f) {
				if sh.try(cur, withFrame(*cur, f, repl), "parts") {
					progress, again = true, true
					break scan
				}
			}
		}
	}
	return progress
}

// parts are f with one part fewer, each.
func parts(f ast.Frame) []ast.Frame {
	var out []ast.Frame
	drop := func(ns []ast.Named, i int) []ast.Named {
		return append(append([]ast.Named(nil), ns[:i]...), ns[i+1:]...)
	}
	switch f := f.(type) {
	case *ast.GroupBy:
		for i := range f.Aggs {
			if len(f.Aggs) > 1 {
				c := *f
				c.Aggs = drop(f.Aggs, i)
				out = append(out, &c)
			}
		}
		for i := range f.Keys {
			c := *f
			c.Keys = drop(f.Keys, i)
			out = append(out, &c)
		}
		if f.MaintainOrder {
			c := *f
			c.MaintainOrder = false
			out = append(out, &c)
		}
	case *ast.Select:
		for i := range f.Cols {
			if len(f.Cols) > 1 {
				c := *f
				c.Cols = drop(f.Cols, i)
				out = append(out, &c)
			}
		}
	case *ast.WithColumns:
		for i := range f.Cols {
			if len(f.Cols) > 1 {
				c := *f
				c.Cols = drop(f.Cols, i)
				out = append(out, &c)
			}
		}
	case *ast.Sort:
		for i := range f.Keys {
			if len(f.Keys) > 1 {
				c := *f
				c.Keys = append(append([]ast.SortKey(nil), f.Keys[:i]...), f.Keys[i+1:]...)
				out = append(out, &c)
			}
		}
	case *ast.Join:
		if f.MaintainOrder {
			c := *f
			c.MaintainOrder = false
			out = append(out, &c)
		}
	}
	return out
}

// slotExprs are f's expressions, in order: a Filter's predicate, a Select's or a
// WithColumns' columns, a GroupBy's keys then its aggregations.
func slotExprs(f ast.Frame) []ast.Expr {
	var out []ast.Expr
	named := func(ns []ast.Named) {
		for _, n := range ns {
			out = append(out, n.E)
		}
	}
	switch f := f.(type) {
	case *ast.Filter:
		out = append(out, f.Pred)
	case *ast.Select:
		named(f.Cols)
	case *ast.WithColumns:
		named(f.Cols)
	case *ast.GroupBy:
		named(f.Keys)
		named(f.Aggs)
	}
	return out
}

// withSlot is f with its slot i's expression e.
func withSlot(f ast.Frame, i int, e ast.Expr) ast.Frame {
	set := func(ns []ast.Named, i int) []ast.Named {
		out := slices.Clone(ns)
		out[i].E = e
		return out
	}
	switch f := f.(type) {
	case *ast.Filter:
		return &ast.Filter{In: f.In, Pred: e}
	case *ast.Select:
		return &ast.Select{In: f.In, Cols: set(f.Cols, i)}
	case *ast.WithColumns:
		return &ast.WithColumns{In: f.In, Cols: set(f.Cols, i)}
	case *ast.GroupBy:
		c := *f
		if i < len(f.Keys) {
			c.Keys = set(f.Keys, i)
		} else {
			c.Aggs = set(f.Aggs, i-len(f.Keys))
		}
		return &c
	}
	return f
}

// slotInfo is what f's slot i is read over: its input, and in a WithColumns the
// columns the ones before it added.
func slotInfo(f ast.Frame, i int, tables map[string]*model.Table) *ast.Info {
	var in *ast.Info
	var err error
	if wc, ok := f.(*ast.WithColumns); ok && i > 0 {
		in, err = ast.Schema(&ast.WithColumns{In: wc.In, Cols: wc.Cols[:i]}, tables)
	} else {
		in, err = ast.Schema(f.Inputs()[0], tables)
	}
	if err != nil {
		return nil
	}
	return in
}

// isAggSlot is whether f's slot i must hold an aggregate at its root.
func isAggSlot(f ast.Frame, i int) bool {
	g, ok := f.(*ast.GroupBy)
	return ok && i >= len(g.Keys)
}

// positions are e's nodes as paths of operand indexes, the root first.
func positions(e ast.Expr) [][]int {
	out := [][]int{nil}
	for k, kid := range e.Kids() {
		for _, p := range positions(kid) {
			out = append(out, append([]int{k}, p...))
		}
	}
	return out
}

func exprAt(e ast.Expr, path []int) ast.Expr {
	for _, k := range path {
		e = e.Kids()[k]
	}
	return e
}

// replaceAt is e with the node at path replaced by repl, every node above it built
// again through ast's constructors and kept to its type; nil where one refuses.
func replaceAt(e ast.Expr, path []int, repl ast.Expr) ast.Expr {
	if len(path) == 0 {
		return repl
	}
	kids := slices.Clone(e.Kids())
	kids[path[0]] = replaceAt(kids[path[0]], path[1:], repl)
	if kids[path[0]] == nil {
		return nil
	}
	var out ast.Expr
	var err error
	switch e := e.(type) {
	case *ast.Bin:
		out, err = ast.NewBin(e.Op, kids[0], kids[1])
	case *ast.Un:
		out, err = ast.NewUn(e.Op, kids[0])
	case *ast.When:
		out, err = ast.NewWhen(kids[0], kids[1], kids[2])
	case *ast.FillNull:
		out, err = ast.NewFillNull(kids[0], kids[1])
	case *ast.Cast:
		out, err = ast.NewCast(kids[0], e.To, e.Lossy)
	case *ast.Agg:
		out, err = ast.NewAgg(e.Op, kids[0])
	case *ast.Fn:
		out, err = ast.NewFn(e.Op, kids[0], e.S, e.I, e.B)
	default:
		return nil
	}
	if err != nil || out.Type() != e.Type() {
		return nil
	}
	return out
}

// passExprs replaces each expression by a simpler one of its type: an operand of
// its own, a literal, a column, a simpler literal; the root's frames first, each
// expression from its root down.
func (sh *shrinker) passExprs(cur **state) bool {
	progress := false
	for again := true; again && !sh.spent(); {
		again = false
		tables, err := (*cur).tableMap()
		if err != nil {
			return progress
		}
	scan:
		for _, f := range frames((*cur).q) {
			for i, e := range slotExprs(f) {
				in := slotInfo(f, i, tables)
				for _, path := range positions(e) {
					if len(path) == 0 && isAggSlot(f, i) {
						continue
					}
					for _, repl := range simpler(exprAt(e, path), in) {
						ne := replaceAt(e, path, repl)
						if ne == nil {
							continue
						}
						if sh.try(cur, withFrame(*cur, f, withSlot(f, i, ne)), "exprs") {
							progress, again = true, true
							break scan
						}
					}
				}
			}
		}
	}
	return progress
}

// simpler are expressions of e's type simpler than e: its operands of its type,
// nearest first; a literal, or a simpler one; a column of in.
func simpler(e ast.Expr, in *ast.Info) []ast.Expr {
	t := e.Type()
	var out []ast.Expr
	queue := slices.Clone(e.Kids())
	for len(queue) > 0 {
		d := queue[0]
		queue = append(queue[1:], d.Kids()...)
		if d.Type() == t {
			out = append(out, d)
		}
	}
	if hasLit(t) {
		if l, ok := e.(*ast.Lit); ok {
			if !l.Null {
				for _, v := range simplerValues(t, litValue(l)) {
					out = append(out, newLit(t, v))
				}
			}
		} else {
			out = append(out, newLit(t, Value{}))
			if t == model.Bool {
				out = append(out, newLit(t, Value{B: true}))
			}
		}
	}
	if in != nil {
		for _, c := range in.Cols {
			if c.Type == t && !c.Noisy {
				if ec, ok := e.(*ast.Col); !ok || ec.Name != c.Name {
					out = append(out, &ast.Col{Name: c.Name, T: t})
				}
			}
		}
	}
	return out
}

// simplerValues are values of t simpler than v: toward 0, shorter text, false.
func simplerValues(t model.Type, v Value) []Value {
	if v.Null {
		return nil
	}
	if !slices.Contains(model.Base, t) && t != model.Int8 && t != model.Int16 {
		return simplerWide(t, v)
	}
	var out []Value
	switch t {
	case model.Int8, model.Int16, model.Int32, model.Int64, model.Date:
		out = append(out, Value{})
		if v.I > 1 || v.I < -1 {
			out = append(out, Value{I: v.I / 2})
			if v.I > 0 {
				out = append(out, Value{I: 1})
			} else {
				out = append(out, Value{I: -1})
			}
		}
		if v.I < 0 && v.I != math.MinInt64 {
			out = append(out, Value{I: -v.I})
		}
	case model.Float64:
		out = append(out, Value{}, Value{F: 1})
		f := v.F
		if !math.IsNaN(f) && !math.IsInf(f, 0) {
			if tr := math.Trunc(f); tr != f {
				out = append(out, Value{F: tr})
			}
			if f < -1 {
				out = append(out, Value{F: -1})
			}
			if f < 0 {
				out = append(out, Value{F: -f})
			}
		}
	case model.String:
		out = append(out, Value{})
		if n := utf8.RuneCountInString(v.S); n > 1 {
			_, size := utf8.DecodeRuneInString(v.S)
			out = append(out, Value{S: v.S[:size]})
			out = append(out, Value{S: string([]rune(v.S)[:n/2])})
		}
	case model.Bool:
		out = append(out, Value{})
	}
	return out
}

// passColumns drops each table's columns, the last first, where the query no longer
// reads them, a table's last column never; then the tables no scan reads.
func (sh *shrinker) passColumns(cur **state) bool {
	progress := false
	for ti := range (*cur).tables {
		for ci := len((*cur).tables[ti].Cols) - 1; ci >= 0; ci-- {
			t := (*cur).tables[ti]
			if len(t.Cols) <= 1 || sh.spent() {
				break
			}
			base, err := t.Held()
			if err != nil {
				break
			}
			nt := &Table{Name: base.Name, Seed: base.Seed, Rows: base.Rows,
				Cols: append(append([]Column(nil), base.Cols[:ci]...), base.Cols[ci+1:]...)}
			cand := (*cur).clone()
			cand.tables[ti] = nt
			if sh.try(cur, cand, "columns") {
				progress = true
			}
		}
	}
	scanned := map[string]bool{}
	ast.Walk((*cur).q.Root, func(f ast.Frame) {
		if s, ok := f.(*ast.Scan); ok {
			scanned[s.Table] = true
		}
	})
	for i := len((*cur).tables) - 1; i >= 0; i-- {
		if t := (*cur).tables[i]; !scanned[t.Name] {
			cand := (*cur).clone()
			cand.tables = slices.Delete(slices.Clone(cand.tables), i, i+1)
			if sh.try(cur, cand, "columns") {
				progress = true
			}
		}
	}
	return progress
}

// cellsEach is how many rows a table may hold for its values to be simplified one
// by one; past it only whole columns are.
const cellsEach = 64

// passValues simplifies each column's values: the whole column toward 0, or to one
// value, then each value toward 0, to shorter text, to the column's first value.
func (sh *shrinker) passValues(cur **state) bool {
	progress := false
	for ti := range (*cur).tables {
		for ci := range (*cur).tables[ti].Cols {
			if sh.spent() {
				return progress
			}
			base, err := (*cur).tables[ti].Held()
			if err != nil {
				break
			}
			col := base.Cols[ci]
			first := Value{Null: true}
			for _, v := range col.Values {
				if !v.Null {
					first = v
					break
				}
			}
			if first.Null {
				continue
			}
			with := func(f func(i int, v Value) Value) bool {
				t := withColumn(base, ci, f)
				if slices.EqualFunc(t.Cols[ci].Values, col.Values, sameValue) {
					// No value changed: a generated table held as rows is no step.
					return false
				}
				cand := (*cur).clone()
				cand.tables[ti] = t
				if sh.try(cur, cand, "values") {
					base = t
					col = t.Cols[ci]
					return true
				}
				return false
			}
			// The whole column at once.
			for _, whole := range []Value{{}, first} {
				if with(func(_ int, v Value) Value {
					if v.Null {
						return v
					}
					return whole
				}) {
					progress = true
				}
			}
			// Each distinct value everywhere it is, so rows that share a value, a
			// group's key, keep sharing one.
			var distinct []Value
			for _, v := range col.Values {
				if !v.Null && !slices.ContainsFunc(distinct, func(d Value) bool { return sameValue(d, v) }) {
					distinct = append(distinct, v)
				}
			}
			if len(distinct) <= cellsEach {
				for _, d := range distinct {
					for _, nv := range simplerValues(col.Type, d) {
						if with(func(_ int, v Value) Value {
							if !v.Null && sameValue(v, d) {
								return nv
							}
							return v
						}) {
							progress = true
							break
						}
					}
				}
			}
			if base.Rows > cellsEach {
				continue
			}
			for r := range base.Rows {
				v := col.Values[r]
				if v.Null {
					continue
				}
				cands := simplerValues(col.Type, v)
				for _, w := range col.Values {
					if !w.Null && !sameValue(w, v) {
						cands = append(cands, w)
						break
					}
				}
				for _, nv := range cands {
					if with(func(i int, old Value) Value {
						if i == r {
							return nv
						}
						return old
					}) {
						progress = true
						break
					}
				}
			}
		}
	}
	return progress
}

// withColumn is base with column ci's values mapped by f.
func withColumn(base *Table, ci int, f func(i int, v Value) Value) *Table {
	t := &Table{Name: base.Name, Seed: base.Seed, Rows: base.Rows, Cols: slices.Clone(base.Cols)}
	old := base.Cols[ci]
	nc := Column{Name: old.Name, Type: old.Type, Values: make([]Value, len(old.Values))}
	for i, v := range old.Values {
		nc.Values[i] = f(i, v)
	}
	t.Cols[ci] = nc
	return t
}

// sameValue is whether two values are one, a float by its bits: -0 is not +0.
func sameValue(a, b Value) bool {
	return a.Null == b.Null && a.I == b.I && a.S == b.S && a.B == b.B &&
		math.Float64bits(a.F) == math.Float64bits(b.F)
}

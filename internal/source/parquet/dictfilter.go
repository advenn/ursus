package parquet

import (
	"context"
	"encoding/binary"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
)

// String conjuncts evaluated on the dictionary (step 193, item P2 of
// v0.6.1-scope.md).
//
// `l_shipmode IN ('AIR', 'AIR REG')` compared every row's string, and the reader
// had copied every row's string to be compared: 21 to 22% of q19 and q12 in v0.6.0's
// profiles, on top of the copy. A dictionary-encoded column holds a handful of
// distinct values, so the conjunct is evaluated once per value, and a row's verdict
// is a lookup by its index:
//
//   - ClassifyPredicates reports a conjunct over one String or Binary column, built
//     of row-local nodes only, as Inexact, so the planner hands it to the reader and
//     keeps the Filter above;
//   - per row group, each such column's dictionary, plus one null, is evaluated
//     once with the engine's own evaluator (ScanSpec.Eval) into a table;
//   - a batch first marks its rows in those columns, each row's index looked up,
//     copying no string; then they copy only the rows every table keeps, and the
//     batch's other columns are read at the same rows, where their reader can, or
//     read whole and taken to them (step 377, R2 of v0.7-scope.md).
//
// The Filter above evaluates the conjunct again, on the rows that are left. A row
// dropped here fails it, and a row kept may fail another conjunct; the Filter
// decides both. So whatever the reader cannot judge it keeps: a present row of a
// PLAIN page, a column read through arrow-go, a dictionary whose evaluation errs.
// A null it judges anywhere, by evaluating the conjunct on one.
//
// That last is not hypothetical. The Filter applies its conjuncts one after
// another (step 164), so a conjunct that errs on a value only ever reaches the rows
// an earlier conjunct kept; a dictionary holds every value. An error here therefore
// turns the table off rather than failing the query.

// dictable reports whether conjunct e reads one String or Binary column of s and is
// built of nodes that compute each row from that row alone: a column, literals,
// operators, casts, conditionals and calls. An aggregate, a window or a UDF is not:
// a UDF might count the values it is shown, and would see each distinct one once.
func dictable(e expr.Node, s *dtype.Schema) (string, bool) {
	name, ok := "", true
	expr.Walk(e, func(n expr.Node) bool {
		switch t := n.(type) {
		case *expr.Col:
			if name != "" && name != t.Name {
				ok = false
			}
			name = t.Name
		case *expr.Lit, *expr.Binary, *expr.Unary, *expr.Alias, *expr.Cast, *expr.Cond, *expr.Call:
		default:
			ok = false
		}
		return ok
	})
	if !ok || name == "" {
		return "", false
	}
	i := s.IndexOf(name)
	if i < 0 {
		return "", false
	}
	switch s.Field(i).Type.ID() {
	case dtype.TypeString, dtype.TypeBinary, dtype.TypeCategorical:
		return name, true
	}
	return "", false
}

// anyDictable reports whether one of e's top-level conjuncts is dictable.
func anyDictable(e expr.Node, s *dtype.Schema) bool {
	for _, c := range conjuncts([]expr.Node{e}) {
		if _, ok := dictable(c, s); ok {
			return true
		}
	}
	return false
}

// dictFilter is a reader's conjuncts to evaluate on dictionaries, by output column.
type dictFilter struct {
	eval  func(context.Context, expr.Node, *data.Batch) (*data.Column, error)
	preds [][]expr.Node
	out   *dtype.Schema
}

// newDictFilter returns the conjuncts of preds a dictionary can judge, by output
// column, or nil when there are none or no evaluator. Under MaxRows it is nil: a
// reader stopping after so many rows read would stop short of rows produced.
func newDictFilter(preds []expr.Node, out *dtype.Schema,
	eval func(context.Context, expr.Node, *data.Batch) (*data.Column, error), maxRows int) *dictFilter {
	if eval == nil || maxRows > 0 {
		return nil
	}
	var df *dictFilter
	for _, p := range conjuncts(preds) {
		name, ok := dictable(p, out)
		if !ok {
			continue
		}
		if df == nil {
			df = &dictFilter{eval: eval, preds: make([][]expr.Node, out.Len()), out: out}
		}
		i := out.IndexOf(name)
		df.preds[i] = append(df.preds[i], p)
	}
	return df
}

// conjuncts splits each predicate's top-level Ands: the planner hands a Filter's
// predicate over whole, and `kind == "num" & code != "3"` reads two columns where
// each of its halves reads one. A row that fails a half fails the whole.
func conjuncts(preds []expr.Node) []expr.Node {
	var out []expr.Node
	var split func(e expr.Node)
	split = func(e expr.Node) {
		if b, ok := e.(*expr.Binary); ok && b.Op == expr.OpAnd {
			split(b.L)
			split(b.R)
			return
		}
		out = append(out, e)
	}
	for _, p := range preds {
		split(p)
	}
	return out
}

// marker returns chunk i when it can be marked: a column with conjuncts, read from
// its pages.
func (df *dictFilter) marker(i int, c colReader) (*byteArrayCol, bool) {
	if df == nil || len(df.preds[i]) == 0 {
		return nil, false
	}
	bc, ok := c.(*byteArrayCol)
	return bc, ok && bc.pages.pageChunk != nil
}

// table returns, for each dictionary value and then for a null, whether column
// i's conjuncts all keep it; nil when they cannot be evaluated here, which keeps
// every row. It is computed once per chunk, whose dictionary it is. A chunk with no
// dictionary still has its null's entry: a null needs no dictionary to be judged.
func (r pageStrings) table(df *dictFilter, i int, dt dtype.DataType) []bool {
	if df == nil {
		return nil // a column beside a predicate's, read at its kept rows
	}
	if r.tableDone {
		return r.keep
	}
	r.tableDone = true
	n, dictOffs, dictChars := r.dictN, r.dictOffs, r.dictChars
	if dictOffs == nil {
		n, dictOffs, dictChars = 0, []int32{0}, nil
	}
	offs := append(append([]int32(nil), dictOffs...), dictOffs[n])
	valid := bitmap.NewBuilder(n + 1)
	valid.AppendMany(true, n)
	valid.Append(false)
	f := df.out.Field(i)
	col := data.NewStringParts(f.Name, offs, dictChars, valid.Finish()).WithDType(dt)
	b, err := data.NewBatch(dtype.MustSchema(dtype.Of(f.Name, dt)), []*data.Column{col})
	if err != nil {
		return nil
	}
	keep := make([]bool, n+1)
	for j := range keep {
		keep[j] = true
	}
	pass := make([]bool, n+1)
	for _, p := range df.preds[i] {
		res, err := df.eval(context.Background(), p, b)
		if err != nil {
			return nil
		}
		sel, err := kernel.SelectionFromMask(res)
		if err != nil {
			return nil
		}
		clear(pass)
		for _, s := range sel {
			pass[s] = true
		}
		for j := range keep {
			keep[j] = keep[j] && pass[j]
		}
	}
	r.keep = keep
	return keep
}

// mark reads up to n rows of the chunk without copying a string: each row's
// dictionary index, or that it is null, or, on a PLAIN page whose bytes the next
// page reuses, a copy of its value. Where the column's table rejects a row it
// clears keep. It returns the rows read; emit then appends the kept ones.
func (r pageStrings) mark(n int, keep []bool, df *dictFilter, i int, dt dtype.DataType) (int, error) {
	if cap(r.pend) < n {
		r.pend = make([]int32, 0, n)
	}
	r.pend = r.pend[:0]
	r.poffs = append(r.poffs[:0], 0)
	r.pchars = r.pchars[:0]
	rows := 0
	for rows < n {
		if r.left == 0 {
			more, err := r.nextPage()
			if err != nil {
				return 0, err
			}
			if !more {
				break
			}
		}
		k := min(n-rows, r.left)
		present, all, err := r.levels(k)
		if err != nil {
			return 0, err
		}
		table := r.table(df, i, dt)
		if r.dict {
			ids, err := r.indices(present)
			if err != nil {
				return 0, err
			}
			vi := 0
			for j := range k {
				at := int32(-1)
				if all || r.lvl[j] == 1 {
					at = int32(ids[vi])
					vi++
				}
				r.pend = append(r.pend, at)
				if table != nil && !table[lookup(at, len(table)-1)] {
					keep[rows+j] = false
				}
			}
		} else {
			for j := range k {
				if !all && r.lvl[j] == 0 {
					r.pend = append(r.pend, -1)
					if table != nil && !table[len(table)-1] {
						keep[rows+j] = false
					}
					continue
				}
				if len(r.plain) < 4 {
					return 0, r.corrupt("a page ends inside its values")
				}
				l := int(binary.LittleEndian.Uint32(r.plain))
				if l < 0 || 4+l > len(r.plain) {
					return 0, r.corrupt("a value runs past its page")
				}
				r.pchars = append(r.pchars, r.plain[4:4+l]...)
				r.poffs = append(r.poffs, int32(len(r.pchars)))
				r.pend = append(r.pend, int32(-1-(len(r.poffs)-1)))
				r.plain = r.plain[4+l:]
			}
		}
		r.left -= k
		rows += k
	}
	// The same slack the dictionary carries, so emit's word copies stay inside.
	r.pchars = append(r.pchars, make([]byte, gatherPad)...)
	r.pchars = r.pchars[:len(r.pchars)-gatherPad]
	return rows, nil
}

// lookup is a pending row's place in a table: its index, or the null's.
func lookup(at int32, dictN int) int {
	if at < 0 {
		return dictN
	}
	return int(at)
}

// emit appends the rows mark read that keep keeps, each value copied once.
func (r pageStrings) emit(c *byteArrayCol, keep []bool) int {
	total, kept := 0, 0
	for j, at := range r.pend {
		if !keep[j] {
			continue
		}
		kept++
		switch {
		case at >= 0:
			total += int(r.dictOffs[at+1] - r.dictOffs[at])
		case at < -1:
			p := -at - 1
			total += int(r.poffs[p] - r.poffs[p-1])
		}
	}
	if c.offs == nil {
		c.offs = make([]int32, 1, kept+1)
	}
	c.offs = growExact(c.offs, kept)
	c.chars = growExact(c.chars, total+gatherPad)
	dst := c.chars[:cap(c.chars)]
	at0 := len(c.chars)
	pos := at0
	copyIn := func(src []byte, lo, hi int) {
		if hi-lo <= 16 {
			binary.LittleEndian.PutUint64(dst[pos:], binary.LittleEndian.Uint64(src[lo:]))
			binary.LittleEndian.PutUint64(dst[pos+8:], binary.LittleEndian.Uint64(src[lo+8:]))
		} else {
			copy(dst[pos:], src[lo:hi])
		}
		pos += hi - lo
	}
	var dict []byte // a PLAIN-only chunk has none, and no row refers to one
	if r.dictOffs != nil {
		dict = r.dictChars[:len(r.dictChars)+gatherPad]
	}
	plain := r.pchars[:len(r.pchars)+gatherPad]
	var word uint64
	w := 0
	for j, at := range r.pend {
		if !keep[j] {
			continue
		}
		switch {
		case at >= 0:
			copyIn(dict, int(r.dictOffs[at]), int(r.dictOffs[at+1]))
			word |= 1 << uint(w)
		case at < -1:
			p := -at - 1
			copyIn(plain, int(r.poffs[p-1]), int(r.poffs[p]))
			word |= 1 << uint(w)
		}
		c.offs = append(c.offs, int32(pos))
		if w++; w == 64 {
			c.valid.appendWord(word, 64)
			word, w = 0, 0
		}
	}
	if w > 0 {
		c.valid.appendWord(word, w)
	}
	c.chars = c.chars[:pos]
	return kept
}

// widen is how many rows to read for a batch of about batch rows, when the last
// read of rows kept kept: as many more as the filter drops, up to eight times.
// A reader that kept a fourteenth of q19's rows sent batches a fourteenth of the
// size, and every operator above paid its cost per batch fourteen times as often:
// q12's CPU fell 13% and its wall time rose 18%.
func widen(batch, rows, kept int) int {
	if kept <= 0 || rows <= 0 {
		return batch * 8
	}
	return min(batch*8, max(batch, batch*rows/kept))
}

// compact takes cols, the batch's columns other than the marked ones, to the rows
// keep keeps, sel their positions where the caller has them. rows is the rows read.
func compact(cols []*data.Column, marked []bool, keep []bool, rows int, sel []int32) (int, error) {
	if sel == nil {
		sel = make([]int32, 0, rows)
		for j, k := range keep[:rows] {
			if k {
				sel = append(sel, int32(j))
			}
		}
	}
	if len(sel) == rows {
		return rows, nil
	}
	for i, c := range cols {
		if marked[i] {
			continue
		}
		out, err := kernel.Take(c, sel)
		if err != nil {
			return 0, err
		}
		cols[i] = out
	}
	return len(sel), nil
}

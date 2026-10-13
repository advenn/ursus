package physical

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/spill"
	"github.com/advenn/ursus/internal/uerr"
)

// A window over one huge partition, in an order of its own (v0.6.4 step 235).
//
// The one-partition file (winstream.go) is read back in its own order, the input's,
// so a function that walks the partition in another order refused: an order_by. Its
// answer needs only a bounded context in THAT order, so the rows are put in it, the
// function walked over them, and the answers put back:
//
//  1. the file is read, each row stamped with its position in it, and projected to
//     what the functions read: the position, the order keys and each function's
//     child and fill;
//  2. that is sorted by the order keys, then the position, which makes the order
//     total and equal to the in-memory stable sort, whatever order runs merge in;
//  3. the functions walk the sorted stream as they walk the file, with their history,
//     lookahead or carried answer;
//  4. the answers are sorted back by the position into an answer file;
//  5. the walk over the file splices the answers in beside its rows (winStreamOp).
//
// Only the narrow projection is sorted, twice, never the rows; the file is read once
// for each distinct order and once more for the answer. The specs that share an
// order share a pass.

// sortedNeed is the need of a spec with an order of its own: its function's need,
// over the partition in that order. It reports false when the spec has no order, or
// its function needs an unbounded part of the partition even so.
func sortedNeed(sp *winSpec) (winNeed, bool) {
	if sp.acc != nil || len(sp.order) == 0 {
		return winNeed{}, false
	}
	plain := *sp
	plain.order = nil
	return streamNeed(&plain)
}

// streamsAlone reports whether a spec answers over one huge partition: in the file's
// order, or in a sorted pass.
func streamsAlone(sp *winSpec) bool {
	if _, ok := streamNeed(sp); ok {
		return true
	}
	_, ok := sortedNeed(sp)
	return ok
}

// orderKey identifies an order: its keys, each with its direction and null place.
func orderKey(order []plan.SortKey) string {
	var b strings.Builder
	for _, k := range order {
		b.WriteString(expr.Identity(k.Expr))
		if k.Descending {
			b.WriteString(" desc")
		}
		if k.NullsLast {
			b.WriteString(" nulls last")
		}
		b.WriteByte(';')
	}
	return b.String()
}

const posName = "__pos" // a row's position in the one-partition file

// sortedPass answers group, the specs that share one order, over the one-partition
// file of rows rows, and returns the answer file: the position, then each spec's
// answer, in position order.
func (s *windowSink) sortedPass(ctx context.Context, group []*winSpec, rows int64) (string, error) {
	order := group[0].order
	// The projection: the position, the order keys, each function's child and fill.
	fields := []dtype.Field{dtype.NotNull(posName, dtype.Int64)}
	keys := make([]plan.SortKey, 0, len(order)+1)
	for i, k := range order {
		f, err := k.Expr.Field(s.inSchema)
		if err != nil {
			return "", err
		}
		name := "__k" + itoa(i)
		fields = append(fields, dtype.Of(name, f.Type))
		keys = append(keys, plan.SortKey{Expr: &expr.Col{Name: name},
			Descending: k.Descending, NullsLast: k.NullsLast})
	}
	keys = append(keys, plan.SortKey{Expr: &expr.Col{Name: posName}})
	walked := make([]*winSpec, len(group))
	for j, sp := range group {
		f, err := sp.fn.Child.Field(s.inSchema)
		if err != nil {
			return "", err
		}
		ct := f.Type
		if sp.fillOf != nil {
			ct = sp.out // the column and its fill meet there, as finishOrdered casts them
		}
		c, fl := "__c"+itoa(j), "__f"+itoa(j)
		fields = append(fields, dtype.Of(c, ct))
		fn := *sp.fn
		fn.Child = &expr.Col{Name: c}
		w := *sp
		w.fn, w.order = &fn, nil
		if sp.fillOf != nil {
			fields = append(fields, dtype.Of(fl, sp.out))
			w.fillOf = &expr.Col{Name: fl}
		}
		walked[j] = &w
	}
	narrow, err := dtype.NewSchema(fields...)
	if err != nil {
		return "", err
	}
	kSch, err := keySchema(keys, narrow)
	if err != nil {
		return "", err
	}

	fwd := &sortSink{schema: narrow, keys: keys, kSch: kSch, budget: s.budget,
		mem: s.budget.Account("over"), batch: s.batch}
	defer fwd.Close()
	if err := s.project(ctx, group, narrow, fwd); err != nil {
		return "", err
	}
	sorted, err := fwd.Finish(ctx)
	if err != nil {
		return "", err
	}

	// The walk, over the sorted stream, as over the file.
	ans := []dtype.Field{dtype.NotNull(posName, dtype.Int64)}
	walkFields := append([]dtype.Field(nil), fields...)
	for _, sp := range group {
		i := s.schema.IndexOf(sp.name)
		if i < 0 {
			sorted.Close()
			return "", uerr.Internalf("physical: window column %s is not in the window's schema", sp.name)
		}
		walkFields = append(walkFields, s.schema.Field(i))
		ans = append(ans, s.schema.Field(i))
	}
	walkSch, err := dtype.NewSchema(walkFields...)
	if err != nil {
		sorted.Close()
		return "", err
	}
	ansSch, err := dtype.NewSchema(ans...)
	if err != nil {
		sorted.Close()
		return "", err
	}
	walk := &winStreamOp{schema: walkSch, inSchema: narrow, rows: rows,
		open: func() (runSource, error) { return &opRun{ctx: ctx, op: sorted}, nil }}
	for _, w := range walked {
		need, _ := streamNeed(w)
		walk.specs = append(walk.specs, &winStreamSpec{sp: w, need: need})
		walk.maxL = max(walk.maxL, need.l)
	}

	// The answers, sorted back by position.
	posKey := []plan.SortKey{{Expr: &expr.Col{Name: posName}}}
	posSch, err := keySchema(posKey, ansSch)
	if err != nil {
		walk.Close()
		return "", err
	}
	back := &sortSink{schema: ansSch, keys: posKey, kSch: posSch, budget: s.budget,
		mem: s.budget.Account("over"), batch: s.batch}
	defer back.Close()
	nar := narrow.Len()
	for {
		b, err := walk.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			walk.Close()
			return "", err
		}
		cols := append([]*data.Column{b.Column(0)}, b.Columns()[nar:]...)
		ab, err := data.NewBatch(ansSch, cols)
		if err != nil {
			walk.Close()
			return "", err
		}
		if err := back.Consume(ctx, ab); err != nil {
			walk.Close()
			return "", err
		}
	}
	if err := walk.Close(); err != nil {
		return "", err
	}
	answers, err := back.Finish(ctx)
	if err != nil {
		return "", err
	}
	return s.writeAnswers(ctx, answers, ansSch)
}

// project reads the one-partition file, stamps each row's position, and hands sink
// the projection the specs of group read.
func (s *windowSink) project(ctx context.Context, group []*winSpec, narrow *dtype.Schema, sink Sink) error {
	r, err := spill.Open(s.oneFile)
	if err != nil {
		return uerr.Wrap(err, uerr.KindIO, "over", "reading a window's rows back")
	}
	defer r.Close()
	order := group[0].order
	var pos int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		b, err := r.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return uerr.Wrap(err, uerr.KindIO, "over", "reading a window's rows back")
		}
		n := b.Rows()
		if n == 0 {
			continue
		}
		at := make([]int64, n)
		for i := range at {
			at[i] = pos + int64(i)
		}
		pos += int64(n)
		cols := []*data.Column{data.NewFixed(posName, dtype.Int64, at, bitmap.AllSet(n))}
		for _, k := range order {
			c, err := evalColumn(ctx, k.Expr, b)
			if err != nil {
				return err
			}
			cols = append(cols, c)
		}
		for _, sp := range group {
			c, err := evalColumn(ctx, sp.fn.Child, b)
			if err != nil {
				return err
			}
			if sp.fillOf != nil {
				f, err := evalColumn(ctx, sp.fillOf, b)
				if err != nil {
					return err
				}
				if c.DType() != sp.out {
					if c, err = kernel.Cast(c.Name(), sp.out, true, c); err != nil {
						return err
					}
				}
				if f.DType() != sp.out {
					if f, err = kernel.Cast(f.Name(), sp.out, true, f); err != nil {
						return err
					}
				}
				cols = append(cols, c, f)
				continue
			}
			cols = append(cols, c)
		}
		nb, err := data.NewBatch(narrow, renamed(narrow, cols))
		if err != nil {
			return err
		}
		if err := sink.Consume(ctx, nb); err != nil {
			return err
		}
	}
}

// renamed names each column as the schema's field.
func renamed(sch *dtype.Schema, cols []*data.Column) []*data.Column {
	out := make([]*data.Column, len(cols))
	for i, c := range cols {
		out[i] = c.Rename(sch.Field(i).Name)
	}
	return out
}

// writeAnswers drains op into a new file of the window's.
func (s *windowSink) writeAnswers(ctx context.Context, op Operator, sch *dtype.Schema) (path string, err error) {
	defer func() { err = errors.Join(err, op.Close()) }()
	path, err = s.newFile()
	if err != nil {
		return "", err
	}
	w, err := spill.Create(path, sch)
	if err != nil {
		return "", err
	}
	for {
		b, err := op.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			w.Close()
			return "", err
		}
		if err := w.Write(b); err != nil {
			w.Close()
			return "", uerr.Wrap(err, uerr.KindIO, "over", "writing a window's answers")
		}
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return path, nil
}

// answerReader reads a sorted pass's answer file in lockstep with the one-partition
// file: the answers for the next n rows, checked to be those rows' by position.
type answerReader struct {
	r   *spill.Reader
	cur *data.Batch
	off int
	pos int64 // the position the next answer must have
}

// take is the answers to the next n rows, a column a spec.
func (a *answerReader) take(n int) ([]*data.Column, error) {
	var parts [][]*data.Column
	for need := n; need > 0; {
		if a.cur == nil || a.off >= a.cur.Rows() {
			b, err := a.r.Next()
			if errors.Is(err, io.EOF) {
				return nil, uerr.Internalf("physical: a window's answers ended at position %d", a.pos)
			}
			if err != nil {
				return nil, uerr.Wrap(err, uerr.KindIO, "over", "reading a window's answers")
			}
			a.cur, a.off = b, 0
			continue
		}
		k := min(need, a.cur.Rows()-a.off)
		at := data.MustValues[int64](a.cur.Column(0))
		// Positions are sorted and distinct, so the two ends being right is all of
		// them being right.
		if at[a.off] != a.pos || at[a.off+k-1] != a.pos+int64(k-1) {
			return nil, uerr.Internalf("physical: a window's answers are out of step: "+
				"position %d where %d belongs", at[a.off], a.pos)
		}
		cols := make([]*data.Column, 0, a.cur.NumCols()-1)
		for _, c := range a.cur.Columns()[1:] {
			cols = append(cols, c.Slice(a.off, k))
		}
		parts = append(parts, cols)
		a.off += k
		a.pos += int64(k)
		need -= k
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	out := make([]*data.Column, len(parts[0]))
	for j := range out {
		col := make([]*data.Column, len(parts))
		for i, p := range parts {
			col[i] = p[j]
		}
		c, err := concatCols(col)
		if err != nil {
			return nil, err
		}
		out[j] = c
	}
	return out, nil
}

func (a *answerReader) Close() error {
	if a.r == nil {
		return nil
	}
	err := a.r.Close()
	a.r = nil
	return err
}

// fileOpener opens a window's spill file as a run, naming the window on an error.
func fileOpener(path string) func() (runSource, error) {
	return func() (runSource, error) {
		r, err := spill.Open(path)
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindIO, "over", "reading a window's rows back")
		}
		return &fileRun{r: r}, nil
	}
}

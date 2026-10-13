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
//
// A reversed cumulative function and a backward fill with no limit (step 236) walk
// the stream backward: over the rows in an order reversed, cum_sum reversed is
// cum_sum, and a backward fill a forward one, both carrying their last answer. The
// stream goes through a reverse sink after the forward walk, so one sort serves both
// directions, and its reverse is exact: the same rows, ties and nulls, backward.

// sortedPlan is how a spec is answered in a sorted pass, and false when it is not:
//
//   - forward, a function under an order_by of its own whose need, over the rows in
//     that order, is bounded;
//   - backward, a reversed cumulative function or a backward fill with no limit,
//     with an order or none (step 236): over the rows in the order reversed it is the
//     forward function, cum_* with Reverse cleared and the fill a forward one, and
//     those carry their last answer.
//
// The spec a pass walks is the plan's: its need is that spec's.
func sortedPlan(sp *winSpec) (walk winSpec, need winNeed, back, ok bool) {
	if sp.acc != nil {
		return winSpec{}, winNeed{}, false, false
	}
	walk = *sp
	walk.order = nil
	fn := *sp.fn
	switch {
	case fn.Fn.IsCumulative() && fn.Params.Reverse:
		fn.Params.Reverse = false
		back = true
	case fn.Fn == expr.WinBackwardFill && fn.Params.N == 0:
		fn.Fn = expr.WinForwardFill
		back = true
	case len(sp.order) == 0:
		return winSpec{}, winNeed{}, false, false // the file's order: streamNeed's
	}
	walk.fn = &fn
	need, ok = streamNeed(&walk)
	return walk, need, back, ok
}

// sortedNeed reports whether a spec is answered in a sorted pass.
func sortedNeed(sp *winSpec) bool {
	_, _, _, ok := sortedPlan(sp)
	return ok
}

// streamsAlone reports whether a spec answers over one huge partition: in the file's
// order, or in a sorted pass.
func streamsAlone(sp *winSpec) bool {
	if _, ok := streamNeed(sp); ok {
		return true
	}
	return sortedNeed(sp)
}

// orderKey identifies an order: its keys, each with its direction and null place.
// No order is the empty key: the file's order, walked backward.
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

// passStep is one spec of a pass, as walked.
type passStep struct {
	walk winSpec
	need winNeed
	back bool
}

// sortedPass answers group, the specs that share one order, over the one-partition
// file of rows rows, and returns the answer file: the position, then each spec's
// answer, in position order.
//
// The rows go to the order: sorted by its keys and the position, or, with no keys,
// the file as it is. The forward specs walk that stream; the backward ones walk it
// reversed, through a reverse sink, after the forward ones. Then everything is sorted
// back by position.
func (s *windowSink) sortedPass(ctx context.Context, group []*winSpec, rows int64) (path string, err error) {
	order := group[0].order
	steps := make([]passStep, len(group))

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
	for j, sp := range group {
		walk, need, back, _ := sortedPlan(sp)
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
		fn := *walk.fn
		fn.Child = &expr.Col{Name: c}
		walk.fn = &fn
		if sp.fillOf != nil {
			fields = append(fields, dtype.Of(fl, sp.out))
			walk.fillOf = &expr.Col{Name: fl}
		}
		steps[j] = passStep{walk: walk, need: need, back: back}
	}
	narrow, err := dtype.NewSchema(fields...)
	if err != nil {
		return "", err
	}

	// The stream in the order.
	var stream Operator
	if len(order) == 0 {
		stream = &projOp{s: s, group: group, narrow: narrow}
	} else {
		kSch, err := keySchema(keys, narrow)
		if err != nil {
			return "", err
		}
		fwd := &sortSink{schema: narrow, keys: keys, kSch: kSch, budget: s.budget,
			mem: s.budget.Account("over"), batch: s.batch}
		defer func() { err = errors.Join(err, fwd.Close()) }()
		if err := feedEach(ctx, &projOp{s: s, group: group, narrow: narrow}, fwd.Consume); err != nil {
			return "", err
		}
		if stream, err = fwd.Finish(ctx); err != nil {
			return "", err
		}
	}

	// The forward walk, then the backward one. Each spec's answer is placed where
	// the walks put it: at[j] is its column in the last walk's batches.
	at := make([]int, len(group))
	if stream, err = s.walkPass(ctx, stream, steps, false, rows, at); err != nil {
		return "", err
	}
	var rev *reverseSink
	if hasBack(steps) {
		rev = &reverseSink{schema: stream.Schema(), mem: s.budget.Account("over"), budget: s.budget}
		defer func() { err = errors.Join(err, rev.Close()) }()
		if err := feedEach(ctx, stream, rev.Consume); err != nil {
			return "", err
		}
		reversed, err := rev.Finish(ctx)
		if err != nil {
			return "", err
		}
		if stream, err = s.walkPass(ctx, reversed, steps, true, rows, at); err != nil {
			return "", err
		}
	}

	// The answers, sorted back by position.
	ans := []dtype.Field{dtype.NotNull(posName, dtype.Int64)}
	for _, sp := range group {
		i := s.schema.IndexOf(sp.name)
		if i < 0 {
			stream.Close()
			return "", uerr.Internalf("physical: window column %s is not in the window's schema", sp.name)
		}
		ans = append(ans, s.schema.Field(i))
	}
	ansSch, err := dtype.NewSchema(ans...)
	if err != nil {
		stream.Close()
		return "", err
	}
	posKey := []plan.SortKey{{Expr: &expr.Col{Name: posName}}}
	posSch, err := keySchema(posKey, ansSch)
	if err != nil {
		stream.Close()
		return "", err
	}
	back := &sortSink{schema: ansSch, keys: posKey, kSch: posSch, budget: s.budget,
		mem: s.budget.Account("over"), batch: s.batch}
	defer func() { err = errors.Join(err, back.Close()) }()
	err = feedEach(ctx, stream, func(ctx context.Context, b *data.Batch) error {
		cols := make([]*data.Column, 0, len(ans))
		cols = append(cols, b.Column(0))
		for _, i := range at {
			cols = append(cols, b.Column(i))
		}
		ab, err := data.NewBatch(ansSch, cols)
		if err != nil {
			return err
		}
		return back.Consume(ctx, ab)
	})
	if err != nil {
		return "", err
	}
	answers, err := back.Finish(ctx)
	if err != nil {
		return "", err
	}
	return s.writeAnswers(ctx, answers, ansSch)
}

// hasBack reports whether a pass walks backward.
func hasBack(steps []passStep) bool {
	for _, st := range steps {
		if st.back {
			return true
		}
	}
	return false
}

// walkPass walks in with the steps of the direction back, appending their answers
// to its rows, and records each one's column in at. With no such step, in is the
// walk.
func (s *windowSink) walkPass(ctx context.Context, in Operator, steps []passStep, back bool,
	rows int64, at []int) (Operator, error) {

	inSch := in.Schema()
	fields := append([]dtype.Field(nil), inSch.FieldSlice()...)
	walk := &winStreamOp{inSchema: inSch, rows: rows,
		open: func() (runSource, error) { return &opRun{ctx: ctx, op: in}, nil }}
	for j := range steps {
		st := &steps[j]
		if st.back != back {
			continue
		}
		i := s.schema.IndexOf(st.walk.name)
		if i < 0 {
			in.Close()
			return nil, uerr.Internalf("physical: window column %s is not in the window's schema", st.walk.name)
		}
		at[j] = len(fields)
		fields = append(fields, s.schema.Field(i))
		walk.specs = append(walk.specs, &winStreamSpec{sp: &st.walk, need: st.need})
		walk.maxL = max(walk.maxL, st.need.l)
	}
	if len(walk.specs) == 0 {
		return in, nil
	}
	sch, err := dtype.NewSchema(fields...)
	if err != nil {
		in.Close()
		return nil, err
	}
	walk.schema = sch
	return walk, nil
}

// feedEach hands every batch of op to consume, and closes op.
func feedEach(ctx context.Context, op Operator, consume func(context.Context, *data.Batch) error) (err error) {
	defer func() { err = errors.Join(err, op.Close()) }()
	for {
		b, err := op.Next(ctx)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := consume(ctx, b); err != nil {
			return err
		}
	}
}

// projOp reads the one-partition file, stamping each row's position, as the
// projection a pass's specs read: the position, the order keys, each child and fill.
type projOp struct {
	s      *windowSink
	group  []*winSpec
	narrow *dtype.Schema

	r   *spill.Reader
	pos int64
	eof bool
}

func (p *projOp) Schema() *dtype.Schema { return p.narrow }

func (p *projOp) Close() error {
	if p.r == nil {
		return nil
	}
	err := p.r.Close()
	p.r = nil
	return err
}

func (p *projOp) Next(ctx context.Context) (*data.Batch, error) {
	if p.eof {
		return nil, io.EOF
	}
	if p.r == nil {
		r, err := spill.Open(p.s.oneFile)
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindIO, "over", "reading a window's rows back")
		}
		p.r = r
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b, err := p.r.Next()
		if errors.Is(err, io.EOF) {
			p.eof = true
			return nil, io.EOF
		}
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindIO, "over", "reading a window's rows back")
		}
		if b.Rows() > 0 {
			return p.project(ctx, b)
		}
	}
}

// project is b's projection, its rows numbered on from the last batch's.
func (p *projOp) project(ctx context.Context, b *data.Batch) (*data.Batch, error) {
	n := b.Rows()
	at := make([]int64, n)
	for i := range at {
		at[i] = p.pos + int64(i)
	}
	p.pos += int64(n)
	cols := []*data.Column{data.NewFixed(posName, dtype.Int64, at, bitmap.AllSet(n))}
	for _, k := range p.group[0].order {
		c, err := evalColumn(ctx, k.Expr, b)
		if err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	for _, sp := range p.group {
		c, err := evalColumn(ctx, sp.fn.Child, b)
		if err != nil {
			return nil, err
		}
		if sp.fillOf != nil {
			f, err := evalColumn(ctx, sp.fillOf, b)
			if err != nil {
				return nil, err
			}
			if c.DType() != sp.out {
				if c, err = kernel.Cast(c.Name(), sp.out, true, c); err != nil {
					return nil, err
				}
			}
			if f.DType() != sp.out {
				if f, err = kernel.Cast(f.Name(), sp.out, true, f); err != nil {
					return nil, err
				}
			}
			cols = append(cols, c, f)
			continue
		}
		cols = append(cols, c)
	}
	return data.NewBatch(p.narrow, renamed(p.narrow, cols))
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

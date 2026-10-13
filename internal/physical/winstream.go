package physical

import (
	"context"
	"errors"
	"io"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/spill"
	"github.com/advenn/ursus/internal/uerr"
)

// A window with no partition_by, over its budget (step 197, item O2 of
// v0.6.1-scope.md).
//
// Partitioning to disk divides partitions, and a window with no partition_by is one
// partition, so it refused. It need not, for most of what is asked of one:
//
//   - an aggregate broadcast back to the rows, x - x.Mean().Over(), is one value for
//     the whole frame. Its accumulator is fed as the rows arrive, as always; the rows
//     themselves go to one spill file, and are read back with the value beside them;
//   - a function that reads the rows in order needs, for a batch, only a bounded
//     context around it: Shift by n, the n rows before or after; a rolling window of
//     N rows, the N before, and after when centred; a fill with a limit, that many.
//     A cumulative function, and a fill without a limit, carry their last answer
//     instead: it leads the next batch, which the kernel then continues from;
//   - an ewm function carries its running state, seven scalars (step 221), and
//     cum_count only a count: reversed, it counts down from the file's rows.
//
// So past the limit the input is written to one file, and read back a batch at a
// time, each function run by the ordinary kernel over the batch and its context,
// and its answer for the batch sliced out. The rows keep their order, the file's.
//
// A function with an order_by of its own is answered in a sorted pass: the rows put
// in its order, walked, and the answers put back (winsorted.go, step 235).
//
// A reversed cumulative function and a backward fill without a limit walk that
// stream backward (step 236). Rank, interpolation and a rolling window by time need
// the whole partition, or an unbounded part of it, and still refuse.

// winNeed is what an ordered function needs around a batch to answer it exactly: h
// rows before and l after, or, with carry, its own last answer.
type winNeed struct {
	h, l  int
	carry bool // a cumulative function, or a forward fill without a limit
	count bool // cum_count, which numbers on from the rows before
	ewm   bool // an ewm function, which carries its running state
	rank  bool // rank, in its own order, which carries its peer group (step 237)
	// lastPos is the position, among the rows walked, of the last that holds a value,
	// at or before each row: interpolation's (step 238).
	lastPos bool
}

// streamNeed is spec sp's need, and false when no bounded one answers it.
func streamNeed(sp *winSpec) (winNeed, bool) {
	if sp.acc != nil {
		return winNeed{}, true
	}
	fn, p := sp.fn.Fn, sp.fn.Params
	if len(sp.order) > 0 {
		return winNeed{}, false // an ordered window sorts the whole partition
	}
	switch {
	case fn == expr.WinCumCount:
		return winNeed{count: true}, true
	case fn.IsEwm():
		return winNeed{ewm: true}, true
	case fn.IsCumulative():
		return winNeed{carry: true}, !p.Reverse
	case fn == expr.WinShift:
		if p.N >= 0 {
			return winNeed{h: int(p.N)}, true
		}
		return winNeed{l: int(-p.N)}, true
	case fn == expr.WinForwardFill:
		if p.N > 0 {
			return winNeed{h: int(p.N)}, true
		}
		return winNeed{carry: true}, true
	case fn == expr.WinBackwardFill:
		return winNeed{l: int(p.N)}, p.N > 0
	case fn.IsRolling():
		if p.By {
			return winNeed{}, false
		}
		need := winNeed{h: int(p.N)}
		if p.Center {
			need.l = int(p.N)
		}
		return need, true
	}
	return winNeed{}, false
}

// streamable reports whether the sink can stream its one partition: every
// partitioning has no keys, and every function has a bounded need, in the file's
// order or its own (winsorted.go).
func (s *windowSink) streamable() bool {
	for _, p := range s.parts {
		if len(p.keys) > 0 {
			return false
		}
	}
	for i := range s.specs {
		if !streamsAlone(&s.specs[i]) {
			return false
		}
	}
	return true
}

// startOneSpill writes the rows held so far to the window's one file, and every
// later batch after them.
func (s *windowSink) startOneSpill() error {
	path, err := s.newFile()
	if err != nil {
		return err
	}
	w, err := spill.Create(path, s.inSchema)
	if err != nil {
		return err
	}
	s.oneW, s.oneFile = w, path
	for _, b := range s.rows {
		if err := w.Write(b); err != nil {
			return uerr.Wrap(err, uerr.KindIO, "over", "spilling a window's rows")
		}
	}
	s.rows, s.copy = nil, data.BufferSet{}
	for _, p := range s.parts {
		p.perRow = nil
	}
	s.budget.NoteSpill()
	s.mem.Release()
	s.stateBytes = 0
	return s.chargeOneSpill()
}

// consumeOneSpill writes a batch to the file and feeds the aggregates, whose one
// partition is group 0.
func (s *windowSink) consumeOneSpill(ctx context.Context, in *data.Batch) error {
	if err := s.oneW.Write(in); err != nil {
		return uerr.Wrap(err, uerr.KindIO, "over", "spilling a window's rows")
	}
	zeros := make([]int32, in.Rows())
	for i := range s.specs {
		sp := &s.specs[i]
		if sp.acc == nil {
			continue
		}
		col, err := evalColumn(ctx, sp.input, in)
		if err != nil {
			return err
		}
		sp.acc.Reserve(1)
		if err := sp.acc.AddBatch(zeros, col); err != nil {
			return err
		}
	}
	return s.chargeOneSpill()
}

// chargeOneSpill charges what is left in memory: the aggregates' state.
func (s *windowSink) chargeOneSpill() error {
	st := int64(0)
	for i := range s.specs {
		if s.specs[i].acc != nil {
			st += s.specs[i].acc.NBytes()
		}
	}
	s.mem.RetainBytes(st - s.stateBytes)
	s.stateBytes = st
	return s.mem.Check()
}

// finishOneSpill closes the file and returns the operator that reads it back. A spec
// with an order of its own is answered first, in a sorted pass with the others of its
// order (winsorted.go), and its answers read back beside the rows.
func (s *windowSink) finishOneSpill(ctx context.Context) (Operator, error) {
	rows := s.oneW.Rows()
	err := s.oneW.Close()
	s.oneW = nil
	if err != nil {
		return nil, uerr.Wrap(err, uerr.KindIO, "over", "spilling a window's rows")
	}
	op := &winStreamOp{schema: s.schema, inSchema: s.inSchema, open: fileOpener(s.oneFile), rows: rows}

	// The sorted passes, one for each distinct order.
	var keys []string
	groups := map[string][]int{}
	for i := range s.specs {
		if sortedNeed(&s.specs[i]) {
			k := orderKey(passOrder(&s.specs[i]))
			if _, seen := groups[k]; !seen {
				keys = append(keys, k)
			}
			groups[k] = append(groups[k], i)
		}
	}
	from := map[int][2]int{} // a sorted spec's answer file and column there
	for g, k := range keys {
		idx := groups[k]
		group := make([]*winSpec, len(idx))
		for j, i := range idx {
			group[j] = &s.specs[i]
			from[i] = [2]int{g, j}
		}
		path, err := s.sortedPass(ctx, group, rows)
		if err != nil {
			return nil, errors.Join(err, op.Close())
		}
		r, err := spill.Open(path)
		if err != nil {
			return nil, errors.Join(uerr.Wrap(err, uerr.KindIO, "over", "reading a window's answers"),
				op.Close())
		}
		op.answers = append(op.answers, &answerReader{r: r})
	}

	for i := range s.specs {
		sp := &s.specs[i]
		st := &winStreamSpec{sp: sp}
		if at, ok := from[i]; ok {
			st.sorted, st.ans, st.col = true, at[0], at[1]
		} else if sp.acc != nil {
			sp.acc.Reserve(1)
			res, err := sp.acc.Finish(sp.name, 1)
			if err != nil {
				return nil, err
			}
			st.agg = res
		} else {
			st.need, _ = streamNeed(sp)
			op.maxL = max(op.maxL, st.need.l)
		}
		op.specs = append(op.specs, st)
	}
	return op, nil
}

// winStreamSpec is one window column as the file is read back.
type winStreamSpec struct {
	sp   *winSpec
	need winNeed
	agg  *data.Column // an aggregate's one value

	hist  *data.Column     // the last h rows of the child before the batch
	histF *data.Column     // and of the fill
	last  *data.Column     // carry: the last answer that held a value, one row
	n     int64            // count: rows numbered so far
	ewm   kernel.EwmState  // ewm: the running state
	rank  kernel.RankState // rank: its position, groups and last row
	held  int64            // lastPos: the last position holding a value, or −1; walkPass sets −1

	// A spec with an order of its own is answered in a sorted pass: its answer is
	// column col of the answer file ans (winsorted.go).
	sorted   bool
	ans, col int
}

// winStreamOp reads a window's rows back, a batch at a time, with the window columns
// beside them: from the one-partition file, or a sorted pass's sorted stream.
type winStreamOp struct {
	schema   *dtype.Schema
	inSchema *dtype.Schema
	open     func() (runSource, error) // the rows, opened on the first Next
	rows     int64                     // in all: a reversed cum_count counts down from them
	specs    []*winStreamSpec
	maxL     int             // the most rows any function needs after a batch
	answers  []*answerReader // the sorted passes' answers, read beside the rows

	src   runSource
	queue []*data.Batch // read ahead: the batch to answer, then its lookahead
	eof   bool
}

func (p *winStreamOp) Schema() *dtype.Schema { return p.schema }

func (p *winStreamOp) Close() error {
	var errs []error
	if p.src != nil {
		errs = append(errs, p.src.Close())
		p.src = nil
	}
	for _, a := range p.answers {
		errs = append(errs, a.Close())
	}
	return errors.Join(errs...)
}

func (p *winStreamOp) Next(ctx context.Context) (*data.Batch, error) {
	if p.src == nil && !p.eof {
		src, err := p.open()
		if err != nil {
			return nil, err
		}
		p.src = src
	}
	// The batch to answer, and enough after it for every function's lookahead.
	for !p.eof && (len(p.queue) == 0 || p.ahead() < p.maxL) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b, err := p.src.Next()
		if errors.Is(err, io.EOF) {
			p.eof = true
			break
		}
		if err != nil {
			return nil, err
		}
		if b.Rows() > 0 {
			p.queue = append(p.queue, b)
		}
	}
	if len(p.queue) == 0 {
		return nil, io.EOF
	}
	target := p.queue[0]
	cols := append([]*data.Column(nil), target.Columns()...)
	got := make([][]*data.Column, len(p.answers))
	for i, a := range p.answers {
		c, err := a.take(target.Rows())
		if err != nil {
			return nil, err
		}
		got[i] = c
	}
	for _, st := range p.specs {
		if st.sorted {
			cols = append(cols, got[st.ans][st.col].Rename(st.sp.name))
			continue
		}
		c, err := p.answer(ctx, st, target)
		if err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	p.queue[0] = nil
	p.queue = p.queue[1:]
	return data.NewBatch(p.schema, cols)
}

// ahead is how many rows are read past the batch to answer.
func (p *winStreamOp) ahead() int {
	n := 0
	for _, b := range p.queue[1:] {
		n += b.Rows()
	}
	return n
}

// answer is one window column for the batch at the front of the queue.
func (p *winStreamOp) answer(ctx context.Context, st *winStreamSpec, target *data.Batch) (*data.Column, error) {
	n := target.Rows()
	sp := st.sp
	if st.agg != nil {
		c, err := kernel.Take(st.agg, make([]int32, n))
		if err != nil {
			return nil, err
		}
		return c.Rename(sp.name), nil
	}
	if st.need.count {
		vals := make([]uint32, n)
		for i := range vals {
			if sp.fn.Params.Reverse {
				vals[i] = uint32(p.rows - st.n - int64(i))
			} else {
				vals[i] = uint32(st.n + int64(i) + 1)
			}
		}
		st.n += int64(n)
		return data.NewFixedOwned(sp.name, dtype.Uint32, vals, bitmap.AllSet(n)), nil
	}
	child, fill, err := p.inputs(ctx, sp, target)
	if err != nil {
		return nil, err
	}
	if st.need.carry {
		return p.carried(st, child)
	}
	if st.need.ewm {
		return kernel.EwmNext(sp.fn.Fn, sp.fn.Params, sp.name, sp.out, child, &st.ewm)
	}
	if st.need.rank {
		return kernel.RankNext(sp.fn.Params.Method, sp.name, child, &st.rank)
	}
	if st.need.lastPos {
		return p.lastHeld(st, child), nil
	}
	// The context: the last h rows before, the batch, and the first l after.
	parts := []*data.Column{}
	fparts := []*data.Column{}
	before := 0
	if st.hist != nil {
		parts, fparts, before = append(parts, st.hist), append(fparts, st.histF), st.hist.Len()
	}
	parts, fparts = append(parts, child), append(fparts, fill)
	if st.need.l > 0 {
		left := st.need.l
		for _, b := range p.queue[1:] {
			if left <= 0 {
				break
			}
			c, f, err := p.inputs(ctx, sp, b)
			if err != nil {
				return nil, err
			}
			take := min(left, c.Len())
			parts = append(parts, c.Slice(0, take))
			if f != nil {
				f = f.Slice(0, take)
			}
			fparts = append(fparts, f)
			left -= take
		}
	}
	all, err := concatCols(parts)
	if err != nil {
		return nil, err
	}
	var allF *data.Column
	if fill != nil {
		if allF, err = concatCols(fparts); err != nil {
			return nil, err
		}
	}
	res, err := kernel.WinCall(sp.fn.Fn, sp.fn.Params, sp.name, sp.out, all, allF, wholeSegment(all.Len()))
	if err != nil {
		return nil, err
	}
	// Keep the last h rows of the child for the next batch.
	if st.need.h > 0 {
		keep := min(st.need.h, before+n)
		st.hist = all.Slice(before+n-keep, keep)
		if allF != nil {
			st.histF = allF.Slice(before+n-keep, keep)
		}
	}
	return res.Slice(before, n), nil
}

// carried runs a cumulative function, or a forward fill without a limit, over the
// batch with its last answer in front, and keeps the batch's last answer that holds
// a value for the next.
func (p *winStreamOp) carried(st *winStreamSpec, child *data.Column) (*data.Column, error) {
	sp := st.sp
	col := child
	lead := 0
	if st.last != nil {
		if col.DType() != st.last.DType() {
			var err error
			if col, err = kernel.Cast(col.Name(), st.last.DType(), true, col); err != nil {
				return nil, err
			}
		}
		var err error
		if col, err = concatCols([]*data.Column{st.last.Rename(col.Name()), col}); err != nil {
			return nil, err
		}
		lead = 1
	}
	res, err := kernel.WinCall(sp.fn.Fn, sp.fn.Params, sp.name, sp.out, col, nil, wholeSegment(col.Len()))
	if err != nil {
		return nil, err
	}
	res = res.Slice(lead, res.Len()-lead)
	for i := res.Len() - 1; i >= 0; i-- {
		if res.IsValid(i) {
			st.last = res.Slice(i, 1)
			break
		}
	}
	return res, nil
}

// lastHeld is, for each row of child, the position among the rows walked of the last
// one at or before it that holds a value, null before the first.
func (p *winStreamOp) lastHeld(st *winStreamSpec, child *data.Column) *data.Column {
	n := child.Len()
	vals := make([]int64, n)
	ok := bitmap.NewBuilder(n)
	for i := range n {
		if child.IsValid(i) {
			st.held = st.n + int64(i)
		}
		vals[i] = st.held
		ok.Append(st.held >= 0)
	}
	st.n += int64(n)
	return data.NewFixed(st.sp.name, dtype.Int64, vals, ok.Finish())
}

// inputs evaluates a function's child, and its fill, over one batch, cast as the
// sink's finishOrdered casts them.
func (p *winStreamOp) inputs(ctx context.Context, sp *winSpec, b *data.Batch) (*data.Column, *data.Column, error) {
	child, err := evalColumn(ctx, sp.fn.Child, b)
	if err != nil {
		return nil, nil, err
	}
	if sp.fillOf == nil {
		return child, nil, nil
	}
	fill, err := evalColumn(ctx, sp.fillOf, b)
	if err != nil {
		return nil, nil, err
	}
	if fill.DType() != sp.out {
		if fill, err = kernel.Cast(fill.Name(), sp.out, true, fill); err != nil {
			return nil, nil, err
		}
	}
	if child.DType() != sp.out {
		if child, err = kernel.Cast(child.Name(), sp.out, true, child); err != nil {
			return nil, nil, err
		}
	}
	return child, fill, nil
}

// wholeSegment is one partition of n rows in their order.
func wholeSegment(n int) kernel.Segments {
	perm := make([]int32, n)
	for i := range perm {
		perm[i] = int32(i)
	}
	return kernel.Segments{Perm: perm, Bounds: []int32{0, int32(n)}}
}

// concatCols joins columns of one type end to end.
func concatCols(cols []*data.Column) (*data.Column, error) {
	if len(cols) == 1 {
		return cols[0], nil
	}
	sch := dtype.MustSchema(dtype.Of("c", cols[0].DType()))
	bs := make([]*data.Batch, len(cols))
	for i, c := range cols {
		b, err := data.NewBatch(sch, []*data.Column{c.Rename("c")})
		if err != nil {
			return nil, err
		}
		bs[i] = b
	}
	out, err := kernel.Concat(sch, bs)
	if err != nil {
		return nil, err
	}
	return out.Column(0).Rename(cols[0].Name()), nil
}

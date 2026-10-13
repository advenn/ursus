package physical

import (
	"context"
	"errors"
	"io"
	"slices"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/uerr"
)

// A temporal group hands its answer out as it goes: with no key since step 207
// (item O2 of v0.6.2-scope.md), and with keys since step 243 (T1 of
// v0.6.4-scope.md).
//
// Step 196 streamed a temporal group's input, and it still held its answer: a
// rolling group's is a row for every input row. The index is sorted across the
// whole input, whatever the keys, so a window whose upper end the last row read has
// passed can take no more rows, in whichever bucket: a later row of its bucket is
// later still.
//
// The windows go to segments of chunk(), each with aggregates of its own, since an
// aggregate cannot hand out some of its groups and keep the rest. A segment keeps
// the largest upper end of its windows, and is handed out once it is full and the
// last row has passed that end; the per-window arrays then drop it, and wBase moves
// past it. Windows still open at a segment's end are in the next.
//
// A keyed group's windows are numbered as they are made, so the buckets' interleave,
// and its answer is per bucket, in the order the buckets appeared, each bucket's
// windows in order. So a keyed segment carries each window's bucket and number as
// two hidden columns, and a sort on them after the group puts the answer in that
// order (tempgroup.go), spilling as any sort does.

// tgSeg is a run of windows numbered from base, with their aggregates.
type tgSeg struct {
	base int32
	n    int32 // windows so far
	hi   int64 // the largest upper end of its windows, once it has one
	accs []kernel.Accumulator
}

// segSize is how many windows a segment takes.
func (s *temporalSink) segSize() int32 { return int32(s.chunk()) }

// segIndex is the place in segs of window w's segment.
func (s *temporalSink) segIndex(w int32) int {
	return int((w - s.segs[0].base) / s.segSize())
}

// openSeg gives window w, the next made, with upper end hi, its segment: the last,
// or a new one when the last is full.
func (s *temporalSink) openSeg(w int32, hi int64) error {
	last := s.segs[len(s.segs)-1]
	if w-last.base < s.segSize() {
		if last.n == 0 || hi > last.hi {
			last.hi = hi
		}
		last.n++
		return nil
	}
	accs, err := s.newAccs()
	if err != nil {
		return err
	}
	s.segs = append(s.segs, &tgSeg{base: w, n: 1, hi: hi, accs: accs})
	return nil
}

// handOut finishes, in order, the segments that are complete and whose every window
// has closed, and queues their answers in ready; at the end of the input, final,
// every one. It runs after each feed, so a segment goes as soon as its last
// window's rows are in, not at the end of the batch that closed it: by then the
// next segment's windows were held too.
func (s *temporalSink) handOut(final bool) error {
	for len(s.segs) > 0 {
		g := s.segs[0]
		last := len(s.segs) == 1
		complete := !last || final || g.n == s.segSize()
		if !complete || (!final && g.n > 0 && !s.passed(tgWin{hi: g.hi}, s.lastT)) {
			return nil
		}
		if g.n == 0 && (s.emitted || !final) {
			return nil // nothing made yet, or an input that ended on a boundary
		}
		b, err := s.segBatch(g)
		if err != nil {
			return err
		}
		s.ready = append(s.ready, b)
		s.emitted = true
		s.dropWindows(g.n)
		if !last {
			s.segs = s.segs[1:]
			continue
		}
		if final {
			s.segs = nil
			return nil
		}
		// The last segment is replaced by an empty one, to number the windows still
		// to come.
		accs, err := s.newAccs()
		if err != nil {
			return err
		}
		s.segs[0] = &tgSeg{base: g.base + g.n, accs: accs}
		return nil
	}
	return nil
}

// takeReady is the answers handOut has queued.
func (s *temporalSink) takeReady() []*data.Batch {
	out := s.ready
	s.ready = nil
	return out
}

// segBatch is segment g's answer: its windows are the first the arrays hold. A
// keyed group's carries its windows' keys, and their buckets and numbers.
func (s *temporalSink) segBatch(g *tgSeg) (*data.Batch, error) {
	n := int(g.n)
	cols := make([]*data.Column, 0, 5+len(s.keys)+len(s.specs))
	if s.boundaries {
		cols = append(cols, packTemporal("_lower_boundary", s.idxType, slices.Clone(s.wStart[:n])),
			packTemporal("_upper_boundary", s.idxType, slices.Clone(s.wEnd[:n])))
	}
	cols = append(cols, packTemporal(s.keySchema.Field(0).Name, s.idxType, slices.Clone(s.wLabel[:n])))
	if len(s.keys) > 0 {
		keys, err := s.windowKeys(s.wBkt[:n])
		if err != nil {
			return nil, err
		}
		cols = append(cols, keys.Columns()...)
	}
	for i, spec := range s.specs {
		g.accs[i].Reserve(n)
		c, err := g.accs[i].Finish(spec.name, n)
		if err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	if len(s.keys) > 0 {
		win := make([]int32, n)
		for i := range win {
			win[i] = g.base + int32(i)
		}
		cols = append(cols, data.NewFixed(s.bktName, dtype.Int32, slices.Clone(s.wBkt[:n]), bitmap.AllSet(n)),
			data.NewFixed(s.winName, dtype.Int32, win, bitmap.AllSet(n)))
	}
	return data.NewBatch(s.schema, cols)
}

// windowKeys is the key of each bucket in bkts, a row each.
func (s *temporalSink) windowKeys(bkts []int32) (*data.Batch, error) {
	srcs := s.keyRows
	if len(s.keyNew) > 0 {
		// The newest keys in one batch, made again only when a bucket has come since.
		if s.keyTail == nil || s.keyTail.Rows() != len(s.keyNew) {
			tail, err := kernel.Concat(s.keyOnly, s.keyNew)
			if err != nil {
				return nil, err
			}
			s.keyTail = tail
		}
		srcs = append(srcs[:len(srcs):len(srcs)], s.keyTail)
	}
	per := int32(s.chunk())
	refs := make([]rowRef, len(bkts))
	for i, b := range bkts {
		refs[i] = rowRef{src: b / per, row: b % per}
	}
	return gatherRows(s.keyOnly, srcs, refs)
}

// dropWindows lets the first n windows' entries go, into arrays of their own size.
func (s *temporalSink) dropWindows(n int32) {
	s.wLabel = append([]int64(nil), s.wLabel[n:]...)
	s.wFed = append([]bool(nil), s.wFed[n:]...)
	if len(s.keys) > 0 {
		s.wBkt = append([]int32(nil), s.wBkt[n:]...)
	}
	if s.boundaries {
		s.wStart = append([]int64(nil), s.wStart[n:]...)
		s.wEnd = append([]int64(nil), s.wEnd[n:]...)
	}
	s.wBase += n
}

// finishStream feeds what is queued and hands out every segment left, at the end of
// the input.
func (s *temporalSink) finishStream(ctx context.Context) ([]*data.Batch, error) {
	if !s.sorted {
		return nil, s.unsorted()
	}
	if err := s.feed(ctx); err != nil {
		return nil, err
	}
	if err := s.handOut(true); err != nil {
		return nil, err
	}
	s.parts, s.bkts, s.segs, s.keyRows, s.keyNew, s.keyTail = nil, nil, nil, nil, nil, nil
	s.mem.Release()
	return s.takeReady(), nil
}

// unsorted is the refusal of an index that went backwards.
func (s *temporalSink) unsorted() error {
	return uerr.New(uerr.KindValue, s.op(),
		"the index column is not sorted: row %d goes backwards", s.badRow).
		Hint("a temporal group-by cuts windows from a sorted index, so the rows " +
			"of a window are contiguous").
		Hint("sort first, e.g. .Sort(ursus.Asc(ursus.Col(...))) before this call")
}

// tempStreamOp runs an unkeyed temporal group: it feeds the sink its input a batch
// at a time and gives out each segment of windows as it closes.
type tempStreamOp struct {
	child Operator
	sink  *temporalSink
	out   []*data.Batch
	done  bool
}

func (o *tempStreamOp) Schema() *dtype.Schema { return o.sink.schema }

func (o *tempStreamOp) Close() error {
	return errors.Join(o.sink.Close(), o.child.Close())
}

func (o *tempStreamOp) Next(ctx context.Context) (*data.Batch, error) {
	for len(o.out) == 0 {
		if o.done {
			return nil, io.EOF
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		in, err := o.child.Next(ctx)
		if errors.Is(err, io.EOF) {
			if o.out, err = o.sink.finishStream(ctx); err != nil {
				return nil, err
			}
			o.done = true
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := o.sink.Consume(ctx, in); err != nil {
			return nil, err
		}
		if !o.sink.sorted {
			return nil, o.sink.unsorted()
		}
		o.out = o.sink.takeReady()
	}
	b := o.out[0]
	o.out = o.out[1:]
	return b, nil
}

// bucketOrderSink puts a keyed group's windows in bucket order: they come out as
// they close, the buckets' interleaved, each bucket's in order, and the answer is
// per bucket, in the order the buckets appeared. It drops the two hidden columns,
// the last, that carry each window's bucket and number.
//
// Its sort, on (bucket, window), holds what comes and spills it in sorted runs past
// the limit, then merges them. Under the limit the sort is not needed: the windows
// came in their numbers' order, so a stable counting sort on the bucket is the
// order, and one Take of what was held gives the answer, as the group gave it
// before it streamed. A sort's gather of each batch out of every segment held cost
// a keyed group in memory half again its time, and a dynamic one more than double.
type bucketOrderSink struct {
	sort *sortSink
	out  *dtype.Schema // the sort's, without the hidden columns
	bkt  int           // the bucket column
}

func (s *bucketOrderSink) Schema() *dtype.Schema { return s.out }
func (s *bucketOrderSink) Close() error          { return s.sort.Close() }

func (s *bucketOrderSink) Consume(ctx context.Context, in *data.Batch) error {
	return s.sort.Consume(ctx, in)
}

func (s *bucketOrderSink) Merge(Sink) error {
	return uerr.Internalf("physical: a temporal group's bucket order cannot be merged")
}

func (s *bucketOrderSink) Finish(ctx context.Context) (Operator, error) {
	if len(s.sort.runs) > 0 || len(s.sort.rows) == 0 {
		op, err := s.sort.Finish(ctx)
		if err != nil {
			return nil, err
		}
		return &stage{child: op, op: &renameOp{schema: s.out, drop: 2}}, nil
	}
	all, err := kernel.Concat(s.sort.schema, s.sort.rows)
	if err != nil {
		return nil, err
	}
	bkts, err := data.Values[int32](all.Column(s.bkt))
	if err != nil {
		return nil, err
	}
	at := []int32{}
	for _, b := range bkts {
		for int(b) >= len(at) {
			at = append(at, 0)
		}
		at[b]++
	}
	sum := int32(0)
	for b, n := range at {
		at[b], sum = sum, sum+n
	}
	perm := make([]int32, len(bkts))
	for i, b := range bkts {
		perm[at[b]] = int32(i)
		at[b]++
	}
	cols := make([]*data.Column, s.out.Len())
	for i := range cols {
		if cols[i], err = kernel.Take(all.Column(i), perm); err != nil {
			return nil, err
		}
	}
	out, err := data.NewBatch(s.out, cols)
	if err != nil {
		return nil, err
	}
	// The sink holds the answer now, and not what it was taken from.
	s.sort.rows, s.sort.keyB = nil, nil
	s.sort.mem.Release()
	s.sort.mem.Retain(out)
	if out.Rows() <= s.sort.chunk() {
		return newBatchOperator(s.out, out), nil
	}
	return &runOperator{schema: s.out, src: &batchRun{b: out, n: s.sort.chunk()}}, nil
}

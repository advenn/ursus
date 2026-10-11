package physical

import (
	"context"
	"errors"
	"io"
	"math"
	"slices"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/uerr"
)

// An unkeyed temporal group hands its answer out as it goes (step 207, item O2 of
// v0.6.2-scope.md).
//
// Step 196 streamed a temporal group's input, and it still held its answer: a
// rolling group's is a row for every input row. With no key there is one bucket,
// and its windows close in the order they are made: both ends of a window rise from
// one to the next, so a row past one window's upper end is past every earlier one's,
// and place closes them from the front. So every window made before the oldest
// still open is final once its rows are fed.
//
// The windows go to segments of chunk(), each with aggregates of its own, since an
// aggregate cannot hand out some of its groups and keep the rest. A segment is
// handed out once it is full and its last window has closed; the per-window arrays
// then drop it, and wBase moves past it. Windows still open at a segment's end are
// in the next. A keyed group keeps one segment of every window: its answer is per
// bucket, each bucket's windows in order, which the last row may still add to.

// tgSeg is a run of windows numbered from base, with their aggregates.
type tgSeg struct {
	base int32
	n    int32 // windows so far
	accs []kernel.Accumulator
}

// segSize is how many windows a segment takes: chunk() when the answer streams,
// every window when it does not.
func (s *temporalSink) segSize() int32 {
	if s.stream {
		return int32(s.chunk())
	}
	return math.MaxInt32
}

// segIndex is the place in segs of window w's segment.
func (s *temporalSink) segIndex(w int32) int {
	return int((w - s.segs[0].base) / s.segSize())
}

// openSeg gives window w, the next made, its segment: the last, or a new one when
// the last is full.
func (s *temporalSink) openSeg(w int32) error {
	last := s.segs[len(s.segs)-1]
	if w-last.base < s.segSize() {
		last.n++
		return nil
	}
	accs, err := s.newAccs()
	if err != nil {
		return err
	}
	s.segs = append(s.segs, &tgSeg{base: w, n: 1, accs: accs})
	return nil
}

// handOut finishes, in order, the segments that are complete and whose every window
// has closed, and queues their answers in ready; at the end of the input, final,
// every one. It runs after each feed, so a segment goes as soon as its last
// window's rows are in, not at the end of the batch that closed it: by then the
// next segment's windows were held too.
func (s *temporalSink) handOut(final bool) error {
	open := s.wBase + int32(len(s.wLabel)) // the oldest window still open
	if !final && len(s.bkts) > 0 && len(s.bkts[0].active) > 0 {
		open = s.bkts[0].active[0].id
	}
	for len(s.segs) > 0 {
		g := s.segs[0]
		last := len(s.segs) == 1
		complete := !last || final || g.n == s.segSize()
		if !complete || g.base+g.n > open {
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

// segBatch is segment g's answer: its windows are the first the arrays hold.
func (s *temporalSink) segBatch(g *tgSeg) (*data.Batch, error) {
	n := int(g.n)
	cols := make([]*data.Column, 0, 3+len(s.specs))
	if s.boundaries {
		cols = append(cols, packTemporal("_lower_boundary", s.idxType, slices.Clone(s.wStart[:n])),
			packTemporal("_upper_boundary", s.idxType, slices.Clone(s.wEnd[:n])))
	}
	cols = append(cols, packTemporal(s.keySchema.Field(0).Name, s.idxType, slices.Clone(s.wLabel[:n])))
	for i, spec := range s.specs {
		g.accs[i].Reserve(n)
		c, err := g.accs[i].Finish(spec.name, n)
		if err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	return data.NewBatch(s.schema, cols)
}

// dropWindows lets the first n windows' entries go, into arrays of their own size.
func (s *temporalSink) dropWindows(n int32) {
	s.wLabel = append([]int64(nil), s.wLabel[n:]...)
	s.wFed = append([]bool(nil), s.wFed[n:]...)
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
	s.parts, s.bkts, s.segs = nil, nil, nil
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

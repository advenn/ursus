package physical

import (
	"context"
	"errors"
	"io"
	"sync/atomic"

	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/source/memsrc"
)

// The runtime swap (step 162): v0.6 item 3's second half.
//
// # Why
//
// build_side exchanges an inner join's inputs when the right one is estimated more
// than twice the left. Its estimates are upper bounds blind to every filter, so PDS-H
// q12 builds orders' 1.5 million keys to probe the 31 thousand lineitem rows its
// filters leave: lineitem is estimated at all six million.
//
// # How
//
// The planner marks every join it could exchange, exchanged or not
// (plan.Join.SwapAtRuntime). When a marked join's build side ends, a deferred build
// has inserted no key yet: it holds the build batches and knows their rows. The join
// then reads its probe side, up to half as many rows:
//
//   - the probe side ends first: it is the smaller by the planner's own ratio, and
//     the join runs exchanged, as plan.SwapJoin rewrites it, over the two sides'
//     batches, planned as any join is;
//   - it does not: the batches read go first, and the join runs as planned.
//
// # What it costs
//
// The probe batches read are held, and charged, while they are read: at most half
// the build side's rows, and none past half the budget, the deferred build's own
// limit. A probe side is read whole either way, and an exchanged join probes with the
// batches the build side already holds.
//
// # What it keeps
//
// The answer, as a set. The rows come in the exchanged join's order, which the
// planner allows only where nothing above the join depends on its order: the same
// proof it makes before exchanging a join itself.

// swapRatio is how much smaller the probe side must be to exchange the inputs: the
// planner's own buildSideRatio.
const swapRatio = 2

// swappedJoins counts the joins exchanged at runtime, for a test to see one happen.
var swappedJoins atomic.Int64

// swapIfSmaller runs the join exchanged when its probe side turns out the smaller,
// and returns nil, having put back any probe batches it read, when it does not.
func (j *joinBreaker) swapIfSmaller(ctx context.Context) (Operator, error) {
	s, ok := j.builder.(*joinBuildSink)
	if !ok || !s.deferred || !s.spec.needBuildRows || s.nBuild == 0 {
		return nil, nil
	}
	if err := s.publishEarly(ctx); err != nil {
		return nil, err
	}
	limit := s.nBuild / swapRatio
	var (
		read []*data.Batch
		rows int
	)
	for rows <= limit && !overHalf(s) {
		b, err := j.probe.Next(ctx)
		if errors.Is(err, io.EOF) {
			return j.swapped(ctx, s, read)
		}
		if err != nil {
			return nil, err
		}
		read = append(read, b)
		s.mem.Retain(b)
		rows += b.Rows()
	}
	j.putBack(s, read)
	return nil, nil
}

// overHalf reports whether the query is past half its budget, where a deferred
// build stops waiting (catchUp) and a probe side stops being read ahead.
func overHalf(s *joinBuildSink) bool {
	return s.budget.Limit() > 0 && s.budget.Used()*2 > s.budget.Limit()
}

// putBack makes the probe batches read the first the probe side gives.
func (j *joinBreaker) putBack(s *joinBuildSink, read []*data.Batch) {
	if len(read) == 0 {
		return
	}
	j.probe = &concatOp{
		children: []Operator{newBatchOperator(s.left, read...), j.probe},
		schema:   s.left,
	}
}

// swapped plans the join exchanged over its probe side's batches, all of them, and
// its build side's.
func (j *joinBreaker) swapped(ctx context.Context, s *joinBuildSink, probe []*data.Batch) (Operator, error) {
	left, err := memsrc.New(s.left, probe...)
	if err != nil {
		return nil, err
	}
	right, err := memsrc.New(s.right, s.parts...)
	if err != nil {
		return nil, err
	}
	join := *j.swap
	join.Left = &plan.Scan{Src: left, Full: s.left}
	join.Right = &plan.Scan{Src: right, Full: s.right}
	// SwapJoin's join is a new one, unmarked, so it is not exchanged again.
	sw := plan.SwapJoin(&join)
	if sw == nil {
		// The planner found it exact over the same schemas, so this is not reached;
		// if it were, the join runs as planned.
		j.putBack(s, probe)
		return nil, nil
	}
	op, err := planPipelined(ctx, sw, j.swapOpts)
	if err != nil {
		return nil, err
	}
	// The batches are the exchanged join's now; this sink's account keeps their
	// charge until it closes, and the budget counts a buffer once however many
	// accounts hold it.
	s.parts = nil
	swappedJoins.Add(1)
	return op, nil
}

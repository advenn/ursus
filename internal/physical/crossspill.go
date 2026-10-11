package physical

import (
	"context"
	"errors"
	"io"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/spill"
	"github.com/advenn/ursus/internal/uerr"
)

// A cross join over its budget (step 195, item O4 of v0.6.1-scope.md).
//
// Radix partitioning divides the key space, and a cross join has one key, so it
// never split: past the limit it refused. It needs no partitioning, though. Every
// probe row meets every build row, so the build side can sit on disk and be read
// once per probe row: a block nested loop.
//
//   - Past the limit, the build side's resident rows are written to one spill file
//     and let go, and every later build batch is written after them (spillCross).
//   - The probe side is then joined a row at a time: each probe row against the file
//     read from its start, a block at a time (crossLoopOp). That keeps the in-memory
//     join's order, each probe row's pairs in build order, and holds one probe batch
//     and one block.
//
// Reading the file once a probe row is I/O in proportion to the output, which is the
// size of the build side for each probe row anyway, and each block read is a batch
// of output written.
//
// Only a pure cross join spills this way. A join with no keys and a residual is an
// inner join of another shape, and still refuses.

// crossSpills reports whether this sink is a cartesian product that can spill.
func (s *joinBuildSink) crossSpills() bool {
	return len(s.keys) == 0 && s.spec.kind == plan.JoinCross && len(s.spec.residual) == 0 &&
		s.spec.needBuildRows
}

// spillCross writes the build rows held so far to the cross file, lets them go, and
// sends every later build batch there.
func (s *joinBuildSink) spillCross() error {
	path, err := s.spillPath("x", 0)
	if err != nil {
		return err
	}
	w, err := spill.Create(path, s.right)
	if err != nil {
		return err
	}
	s.crossW, s.crossFile = w, path
	for _, b := range s.parts {
		if err := w.Write(b); err != nil {
			return uerr.Wrap(err, uerr.KindIO, "join", "spilling a cross join's build side")
		}
	}
	s.parts = nil
	// The per-row state goes with them: a spilled cross join counts its rows and
	// tracks nothing else.
	s.rowKey = nil
	s.mem.Release()
	s.stateBytes = 0
	s.reaccount()
	s.budget.NoteSpill()
	return nil
}

// crossProbe closes the cross file and returns the block nested loop over it.
func (s *joinBuildSink) crossProbe(probe Operator) (Operator, error) {
	s.finished = true
	err := s.crossW.Close()
	s.crossW = nil
	if err != nil {
		return nil, uerr.Wrap(err, uerr.KindIO, "join", "spilling a cross join's build side")
	}
	return &crossLoopOp{schema: s.out, layout: s.layout, probe: probe, file: s.crossFile}, nil
}

// crossLoopOp joins each probe row with every row of a spilled build side.
type crossLoopOp struct {
	schema *dtype.Schema
	layout *plan.JoinLayout
	probe  Operator
	file   string

	cur  *data.Batch   // the probe batch being joined
	row  int           // its row being joined
	rd   *spill.Reader // the build file, read from its start for that row
	done bool
}

func (p *crossLoopOp) Schema() *dtype.Schema { return p.schema }

// Close closes the file it reads; the probe side is joinBreaker's to close.
func (p *crossLoopOp) Close() error {
	if p.rd != nil {
		err := p.rd.Close()
		p.rd = nil
		return err
	}
	return nil
}

func (p *crossLoopOp) Next(ctx context.Context) (*data.Batch, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if p.done {
			return nil, io.EOF
		}
		if p.cur == nil || p.row >= p.cur.Rows() {
			in, err := p.probe.Next(ctx)
			if errors.Is(err, io.EOF) {
				p.done = true
				return nil, io.EOF
			}
			if err != nil {
				return nil, err
			}
			p.cur, p.row = in, 0
			continue
		}
		if p.rd == nil {
			rd, err := spill.Open(p.file)
			if err != nil {
				return nil, uerr.Wrap(err, uerr.KindIO, "join", "reading a cross join's build side")
			}
			p.rd = rd
		}
		block, err := p.rd.Next()
		if errors.Is(err, io.EOF) {
			// This probe row has met every build row: the next one starts over.
			if err := p.Close(); err != nil {
				return nil, uerr.Wrap(err, uerr.KindIO, "join", "reading a cross join's build side")
			}
			p.row++
			continue
		}
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindIO, "join", "reading a cross join's build side")
		}
		n := block.Rows()
		if n == 0 {
			continue
		}
		lsel, rsel := make([]int32, n), make([]int32, n)
		for i := range n {
			lsel[i], rsel[i] = int32(p.row), int32(i)
		}
		return gatherOut(p.schema, p.layout, p.cur, block, lsel, rsel, keyFromLeft)
	}
}

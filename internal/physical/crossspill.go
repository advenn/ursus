package physical

import (
	"context"
	"errors"
	"io"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
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
// A pure cross join spills this way, and since step 219 a keyless Semi or Anti join
// with a residual, a WhereExists of a predicate alone, whose probe reads the file
// until each row is decided (existsLoopOp).

// crossSpills reports whether this sink can spill its build side whole: a cartesian
// product, or a keyless Semi or Anti join with a residual, a WhereExists of a
// predicate alone (step 219).
func (s *joinBuildSink) crossSpills() bool {
	if len(s.keys) != 0 || !s.spec.needBuildRows {
		return false
	}
	switch {
	case s.spec.kind == plan.JoinCross && len(s.spec.residual) == 0:
		return true
	case s.spec.maskOnly && len(s.spec.residual) > 0:
		return true
	}
	return false
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
	if s.spec.maskOnly {
		return &existsLoopOp{schema: s.out, pairLayout: s.pairLayout, residual: s.spec.residual,
			emitMatched: s.spec.emitMatched, batch: s.spec.batchSize, probe: probe, file: s.crossFile}, nil
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

// existsLoopOp is a keyless Semi or Anti join with a residual over a spilled build
// side (v0.6.3 step 219). Such a join kept its build side whole, since it has no key
// to partition on, and refused past the budget.
//
// A probe row's verdict needs every build row only until one passes the residual. So
// a probe batch at a time, the build file is read a block at a time; each block is
// paired with the batch's rows not yet decided, a batch of pairs at a time, and the
// residual's conjuncts fold over the pairs as the in-memory probe folds them
// (residualFold): a null is not a match. Once every row is decided the file is left.
// The rows whose verdict the join wants are given out, in probe order. It holds a
// probe batch, a block and a batch of pairs.
type existsLoopOp struct {
	schema      *dtype.Schema // the probe's: a Semi or Anti join gives out its rows
	pairLayout  *plan.JoinLayout
	residual    []expr.Node
	emitMatched bool // false for Anti
	batch       int
	probe       Operator
	file        string
}

func (p *existsLoopOp) Schema() *dtype.Schema { return p.schema }

// Close closes nothing: matches closes the file it opens, and the probe side is
// joinBreaker's to close.
func (p *existsLoopOp) Close() error { return nil }

func (p *existsLoopOp) Next(ctx context.Context) (*data.Batch, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		in, err := p.probe.Next(ctx)
		if err != nil {
			return nil, err // io.EOF included
		}
		if in.Rows() == 0 {
			continue
		}
		hit, err := p.matches(ctx, in)
		if err != nil {
			return nil, err
		}
		var sel []int32
		for i, h := range hit {
			if h == p.emitMatched {
				sel = append(sel, int32(i))
			}
		}
		switch len(sel) {
		case 0:
			continue
		case in.Rows():
			return data.NewBatch(p.schema, in.Columns())
		}
		return takeBatch(p.schema, in, sel)
	}
}

// matches reports, for each row of in, whether some build row passes the residual
// with it.
func (p *existsLoopOp) matches(ctx context.Context, in *data.Batch) (_ []bool, err error) {
	n := in.Rows()
	hit := make([]bool, n)
	open := make([]int32, n) // the rows not yet decided
	for i := range open {
		open[i] = int32(i)
	}
	rd, err := spill.Open(p.file)
	if err != nil {
		return nil, uerr.Wrap(err, uerr.KindIO, "join", "reading a keyless join's build side")
	}
	defer func() { err = errors.Join(err, rd.Close()) }()
	step := max(p.batch, 1)
	lsel, rsel := make([]int32, 0, step), make([]int32, 0, step)
	for len(open) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		block, err := rd.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindIO, "join", "reading a keyless join's build side")
		}
		flush := func() error {
			if len(lsel) == 0 {
				return nil
			}
			alive, err := p.fold(ctx, in, block, lsel, rsel)
			if err != nil {
				return err
			}
			for k, ok := range alive {
				if ok {
					hit[lsel[k]] = true
				}
			}
			lsel, rsel = lsel[:0], rsel[:0]
			return nil
		}
		for _, i := range open {
			for j := range block.Rows() {
				if hit[i] {
					break // decided by a pair already folded
				}
				lsel, rsel = append(lsel, i), append(rsel, int32(j))
				if len(lsel) == step {
					if err := flush(); err != nil {
						return nil, err
					}
				}
			}
		}
		if err := flush(); err != nil {
			return nil, err
		}
		kept := open[:0]
		for _, i := range open {
			if !hit[i] {
				kept = append(kept, i)
			}
		}
		open = kept
	}
	return hit, nil
}

// fold evaluates the residual over the pairs (lsel[k], rsel[k]) and reports which pass
// every conjunct.
func (p *existsLoopOp) fold(ctx context.Context, in, block *data.Batch, lsel, rsel []int32) ([]bool, error) {
	pairs, err := gatherOut(p.pairLayout.Schema, p.pairLayout, in, block, lsel, rsel, keyFromLeft)
	if err != nil {
		return nil, err
	}
	alive := make([]bool, len(lsel))
	for k := range alive {
		alive[k] = true
	}
	for _, e := range p.residual {
		mask, err := evalColumn(ctx, e, pairs)
		if err != nil {
			return nil, err
		}
		bits, valid := mask.Bools(), mask.Validity()
		for k := range alive {
			// A null predicate is not a match, which is Filter's rule and SQL's.
			if !valid.Get(k) || !bits.Get(k) {
				alive[k] = false
			}
		}
	}
	return alive, nil
}

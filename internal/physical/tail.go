package physical

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/spill"
	"github.com/advenn/ursus/internal/uerr"
)

// tailOp keeps the last n rows.
//
// A ring of batches rather than a ring of rows: batches are immutable and already
// allocated, so dropping one from the front is free, whereas a row ring would copy
// every row twice. The cost is that it can hold up to n + (batchSize - 1) rows
// rather than exactly n — bounded either way, which is the property that matters.
//
// # Past the budget (v0.6.3 step 217)
//
// The last n rows are the answer, and n may be more than the budget. The ring is
// held as pieces, in input order: the batches it holds, always the last piece, or a
// run file batches it held went to when the query was over its budget. Pieces drop
// from the front while the rest still covers n, a dropped run's file with them, so
// the files hold about n rows. At the end the pieces are given out forward, skipping
// the rows past n at the start: a batch at a time. Until step 217 it refused.
type tailOp struct {
	child Operator
	n     int
	// limited keeps only the first take of the last n rows, fewer by as many as the
	// input fell short of n: a slice from the end (step 291).
	limited bool
	take    int

	pieces []tailPiece // in input order; only the last holds batches
	held   int         // rows across every piece
	out    Operator
	drawn  bool

	// mem is accounted even though the ring is bounded by construction. A budget
	// that only tracks the operators it expects to be large is a budget that
	// reports a number nobody can check.
	mem    *execopt.Account
	budget *execopt.Budget
	dir    string // its run files' directory, once it has spilled
	runs   int    // run files written, which names the next
}

// tailPiece is a stretch of the input: batches held, or the run file they went to.
type tailPiece struct {
	batches []*data.Batch
	run     string
	rows    int
}

func (t *tailOp) Schema() *dtype.Schema { return t.child.Schema() }

func (t *tailOp) Close() error {
	t.mem.Release()
	var errs []error
	if t.out != nil {
		errs = append(errs, t.out.Close())
	}
	errs = append(errs, t.child.Close())
	if t.dir != "" {
		if err := os.RemoveAll(t.dir); err != nil {
			errs = append(errs, uerr.Wrap(err, uerr.KindIO, "tail", "removing spill directory %s", t.dir))
		}
		t.dir = ""
	}
	return errors.Join(errs...)
}

func (t *tailOp) Next(ctx context.Context) (*data.Batch, error) {
	if !t.drawn {
		if err := t.drain(ctx); err != nil {
			return nil, err
		}
		t.drawn = true
	}
	return t.out.Next(ctx)
}

func (t *tailOp) drain(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		b, err := t.child.Next(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		t.hold(b)
		if err := t.dropFront(); err != nil {
			return err
		}

		// The ring EVICTS, so accounting is recomputed rather than accumulated.
		// What it holds is the last piece's batches.
		t.mem.Release()
		if last := t.last(); last != nil {
			for _, held := range last.batches {
				t.mem.Retain(held)
			}
		}
		if t.budget.Limit() > 0 && t.mem.Over() {
			if err := t.spillHeld(ctx); err != nil {
				return err
			}
		}
	}

	schema := t.child.Schema()
	if t.n <= 0 || t.held == 0 {
		empty, err := data.NewBatch(schema, emptyColumns(schema))
		if err != nil {
			return err
		}
		t.out = newBatchOperator(schema, empty)
		return nil
	}
	t.out = &tailOut{t: t, skip: max(t.held-t.n, 0)}
	if t.limited {
		kept := min(t.n, t.held)
		t.out = &limitOp{child: t.out, remaining: max(t.take-(t.n-kept), 0)}
	}
	return nil
}

// hold adds b to the batches held, the last piece.
func (t *tailOp) hold(b *data.Batch) {
	if n := len(t.pieces); n == 0 || t.pieces[n-1].run != "" {
		t.pieces = append(t.pieces, tailPiece{})
	}
	last := &t.pieces[len(t.pieces)-1]
	last.batches = append(last.batches, b)
	last.rows += b.Rows()
	t.held += b.Rows()
}

// last is the piece of batches held, or nil when every piece is a run.
func (t *tailOp) last() *tailPiece {
	if n := len(t.pieces); n > 0 && t.pieces[n-1].run == "" {
		return &t.pieces[n-1]
	}
	return nil
}

// dropFront drops whole runs, then whole held batches, from the front while the rest
// still covers n.
func (t *tailOp) dropFront() error {
	for len(t.pieces) > 0 {
		p := &t.pieces[0]
		if p.run != "" {
			if t.held-p.rows < t.n {
				return nil
			}
			t.held -= p.rows
			if err := os.Remove(p.run); err != nil {
				return uerr.Wrap(err, uerr.KindIO, "tail", "removing spill run %s", p.run)
			}
			t.pieces = t.pieces[1:]
			continue
		}
		for len(p.batches) > 0 && t.held-p.batches[0].Rows() >= t.n {
			t.held -= p.batches[0].Rows()
			p.rows -= p.batches[0].Rows()
			p.batches[0] = nil
			p.batches = p.batches[1:]
		}
		return nil
	}
	return nil
}

// spillHeld writes the batches held to a run file, in order, and lets them go.
func (t *tailOp) spillHeld(ctx context.Context) error {
	last := t.last()
	if last == nil || len(last.batches) == 0 {
		return nil
	}
	if t.dir == "" {
		dir, err := os.MkdirTemp(t.budget.SpillDir(), "ursus-tail-")
		if err != nil {
			return uerr.Wrap(err, uerr.KindResource, "tail", "creating a spill directory").
				Hint("choose a writable location with WithSpillDir")
		}
		t.dir = dir
	}
	path := filepath.Join(t.dir, "run"+itoa(t.runs)+".ursspill")
	t.runs++
	w, err := spill.Create(path, t.child.Schema())
	if err != nil {
		return err
	}
	for _, b := range last.batches {
		if err := ctx.Err(); err != nil {
			w.Close()
			return err
		}
		if err := w.Write(b); err != nil {
			w.Close()
			return uerr.Wrap(err, uerr.KindIO, "tail", "writing spill run %s", path)
		}
	}
	if err := w.Close(); err != nil {
		return uerr.Wrap(err, uerr.KindIO, "tail", "writing spill run %s", path)
	}
	*last = tailPiece{run: path, rows: last.rows}
	t.mem.Release()
	t.budget.NoteSpill()
	return nil
}

// tailOut gives the pieces out forward, skipping skip rows at the start.
type tailOut struct {
	t    *tailOp
	skip int
	next int           // the piece to give out next
	cur  []*data.Batch // a held piece's batches still to give out
	rd   *spill.Reader // a run being read
}

func (o *tailOut) Schema() *dtype.Schema { return o.t.child.Schema() }

func (o *tailOut) Close() error {
	if o.rd != nil {
		err := o.rd.Close()
		o.rd = nil
		return err
	}
	return nil
}

func (o *tailOut) Next(ctx context.Context) (*data.Batch, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b, err := o.read()
		if err != nil {
			return nil, err
		}
		if o.skip >= b.Rows() {
			o.skip -= b.Rows()
			continue
		}
		if o.skip > 0 {
			b = b.Slice(o.skip, b.Rows()-o.skip)
			o.skip = 0
		}
		return b, nil
	}
}

// read is the next batch of the pieces, in order, or io.EOF.
func (o *tailOut) read() (*data.Batch, error) {
	for {
		if o.rd != nil {
			b, err := o.rd.Next()
			if !errors.Is(err, io.EOF) {
				if err != nil {
					return nil, uerr.Wrap(err, uerr.KindIO, "tail", "reading a spill run")
				}
				return b, nil
			}
			if err := o.rd.Close(); err != nil {
				return nil, err
			}
			o.rd = nil
		}
		if len(o.cur) > 0 {
			b := o.cur[0]
			o.cur = o.cur[1:]
			return b, nil
		}
		if o.next >= len(o.t.pieces) {
			return nil, io.EOF
		}
		p := o.t.pieces[o.next]
		o.next++
		if p.run == "" {
			o.cur = p.batches
			continue
		}
		rd, err := spill.Open(p.run)
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindIO, "tail", "reading spill run %s", p.run)
		}
		o.rd = rd
	}
}

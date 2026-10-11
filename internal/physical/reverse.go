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
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/spill"
	"github.com/advenn/ursus/internal/uerr"
)

// reverseSink emits its input backwards.
//
// The cheapest pipeline breaker: its batches last first, each one's rows gathered
// in descending order. It is sortSink with the comparator deleted, and writing it
// that way rather than as `Sort` by a synthesised index is what keeps it O(n)
// instead of O(n log n).
//
// # Past the budget (step 206)
//
// Its input is held as pieces, in input order: the batches it holds, always the
// last piece, or a run file batches it held were written to when the query went
// over its budget. Finish gives the pieces out last to first, a run read back whole
// and then given out backwards, so the most it holds at once is one run, what it
// held when it spilled. Until step 206 it concatenated its whole input and gathered
// that backwards, two copies at once, and refused past the budget.
type reverseSink struct {
	schema *dtype.Schema
	mem    *execopt.Account
	budget *execopt.Budget
	pieces []revPiece // in input order; only the last holds batches
	dir    string     // its run files' directory, once it has spilled
	merged []string   // the directories of merged sinks' runs, now its own
	runs   int        // run files written, which names the next
}

// revPiece is a stretch of the input: batches held, or the run file they went to.
type revPiece struct {
	batches []*data.Batch
	run     string
}

func (s *reverseSink) Schema() *dtype.Schema { return s.schema }

func (s *reverseSink) Close() error {
	s.mem.Release()
	s.pieces = nil
	var errs []error
	for _, d := range append(s.merged, s.dir) {
		if d == "" {
			continue
		}
		if err := os.RemoveAll(d); err != nil {
			errs = append(errs, uerr.Wrap(err, uerr.KindIO, "reverse", "removing spill directory %s", d))
		}
	}
	s.dir, s.merged = "", nil
	return errors.Join(errs...)
}

// hold adds b to the batches held, the last piece. Retained, not copied, on the
// same grounds sortSink gives: a Batch is a schema pointer plus immutable Columns.
func (s *reverseSink) hold(b *data.Batch) {
	if n := len(s.pieces); n == 0 || s.pieces[n-1].run != "" {
		s.pieces = append(s.pieces, revPiece{})
	}
	last := &s.pieces[len(s.pieces)-1]
	last.batches = append(last.batches, b)
	s.mem.Retain(b)
}

func (s *reverseSink) Consume(ctx context.Context, in *data.Batch) error {
	if in.Rows() == 0 {
		return nil
	}
	s.hold(in)
	return s.overBudget(ctx)
}

// overBudget writes the batches held to a run when the query is over its budget.
func (s *reverseSink) overBudget(ctx context.Context) error {
	if s.budget.Limit() > 0 && s.mem.Over() {
		return s.spillHeld(ctx)
	}
	return nil
}

// spillHeld writes the batches held to a run file, in input order, and lets them go.
// They are everything the sink holds, so its account goes to nothing.
func (s *reverseSink) spillHeld(ctx context.Context) error {
	n := len(s.pieces)
	if n == 0 || s.pieces[n-1].run != "" {
		return nil
	}
	if s.dir == "" {
		dir, err := os.MkdirTemp(s.budget.SpillDir(), "ursus-reverse-")
		if err != nil {
			return uerr.Wrap(err, uerr.KindResource, "reverse", "creating a spill directory").
				Hint("choose a writable location with WithSpillDir")
		}
		s.dir = dir
	}
	path := filepath.Join(s.dir, "run"+itoa(s.runs)+".ursspill")
	s.runs++
	w, err := spill.Create(path, s.schema)
	if err != nil {
		return err
	}
	for _, b := range s.pieces[n-1].batches {
		if err := ctx.Err(); err != nil {
			w.Close()
			return err
		}
		if err := w.Write(b); err != nil {
			w.Close()
			return uerr.Wrap(err, uerr.KindIO, "reverse", "writing spill run %s", path)
		}
	}
	if err := w.Close(); err != nil {
		return uerr.Wrap(err, uerr.KindIO, "reverse", "writing spill run %s", path)
	}
	s.pieces[n-1] = revPiece{run: path}
	s.mem.Release()
	s.budget.NoteSpill()
	return nil
}

func (s *reverseSink) Merge(other Sink) error {
	o, ok := other.(*reverseSink)
	if !ok {
		return uerrInternal("reverseSink", other)
	}
	// APPEND, exactly as sortSink does, and for exactly sortSink's reason: both
	// sinks buffer their input untouched and defer the order-changing work to
	// Finish, which gives the pieces out last to first, each one backwards. So the
	// pieces must be left in INPUT order, and `other` saw a later portion of it.
	//
	// The tempting argument — "reversal turns later into earlier, so it goes in
	// FRONT" — would hold only if Consume or Merge reversed each batch. Neither
	// does, so prepending merely concatenates in the wrong order: two sinks over
	// [1,2] and [3,4] then yield [2,1,4,3] where one sink yields [4,3,2,1].
	//
	// Batches held are only ever the last piece, so a spill lets go of exactly them:
	// this sink's go to a run before the other's runs follow. The other's own held
	// batches, retained not copied, are accounted here too, the defect
	// joinBuildSink.Merge was fixed for.
	for i, p := range o.pieces {
		if p.run == "" {
			for _, b := range p.batches {
				s.hold(b)
			}
			continue
		}
		if i == 0 {
			if err := s.spillHeld(context.Background()); err != nil {
				return err
			}
		}
		s.pieces = append(s.pieces, p)
	}
	if o.dir != "" {
		s.merged = append(s.merged, o.dir)
	}
	s.merged = append(s.merged, o.merged...)
	o.pieces, o.dir, o.merged = nil, "", nil
	return s.overBudget(context.Background())
}

func (s *reverseSink) Finish(_ context.Context) (Operator, error) {
	if len(s.pieces) == 0 {
		empty, err := data.NewBatch(s.schema, emptyColumns(s.schema))
		if err != nil {
			return nil, err
		}
		return newBatchOperator(s.schema, empty), nil
	}
	return &reverseOp{sink: s, next: len(s.pieces)}, nil
}

// reverseOp gives reverseSink's pieces out last to first, each piece's batches last
// to first, and each batch backwards. The sink holds a run while it is given out
// and lets it go for the next; it closes what it holds.
type reverseOp struct {
	sink *reverseSink
	next int           // the pieces still to give out are pieces[:next]
	cur  []*data.Batch // the piece being given out; its last batch goes next
	sel  []int32
}

func (r *reverseOp) Schema() *dtype.Schema { return r.sink.schema }
func (r *reverseOp) Close() error          { return nil }

func (r *reverseOp) Next(ctx context.Context) (*data.Batch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for len(r.cur) == 0 {
		if r.next == 0 {
			return nil, io.EOF
		}
		if r.next < len(r.sink.pieces) {
			r.sink.mem.Release() // the piece given out before
		}
		r.next--
		p := &r.sink.pieces[r.next]
		if p.run == "" {
			r.cur, p.batches = p.batches, nil
			continue
		}
		batches, err := r.readRun(p.run)
		if err != nil {
			return nil, err
		}
		r.cur = batches
	}
	b := r.cur[len(r.cur)-1]
	r.cur[len(r.cur)-1] = nil
	r.cur = r.cur[:len(r.cur)-1]
	return r.backwards(b)
}

// readRun reads a run file's batches, in order, into the sink's account.
func (r *reverseOp) readRun(path string) ([]*data.Batch, error) {
	rd, err := spill.Open(path)
	if err != nil {
		return nil, uerr.Wrap(err, uerr.KindIO, "reverse", "reading spill run %s", path)
	}
	var batches []*data.Batch
	for {
		b, err := rd.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			rd.Close()
			return nil, uerr.Wrap(err, uerr.KindIO, "reverse", "reading spill run %s", path)
		}
		batches = append(batches, b)
		r.sink.mem.Retain(b)
	}
	if err := rd.Close(); err != nil {
		return nil, uerr.Wrap(err, uerr.KindIO, "reverse", "reading spill run %s", path)
	}
	return batches, nil
}

// backwards is b with its rows in descending order.
func (r *reverseOp) backwards(b *data.Batch) (*data.Batch, error) {
	n := b.Rows()
	r.sel = r.sel[:0]
	for i := n - 1; i >= 0; i-- {
		r.sel = append(r.sel, int32(i))
	}
	cols := make([]*data.Column, b.NumCols())
	for i, c := range b.Columns() {
		var err error
		if cols[i], err = kernel.Take(c, r.sel); err != nil {
			return nil, err
		}
	}
	return data.NewBatch(r.sink.schema, cols)
}

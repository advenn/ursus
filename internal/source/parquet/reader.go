package parquet

import (
	"context"
	"io"
	"sync"

	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/metadata"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// --- source.BatchSource --------------------------------------------------------

// rowsReader answers a scan of no columns from the files' footers: batches of their
// rows and no columns, no column chunk read (step 370), a file's footer read as its
// rows are reached.
type rowsReader struct {
	src  *Source
	out  *dtype.Schema
	size int
	left int // rows MaxRows leaves; 0 unlimited

	mu    sync.Mutex
	k     int   // the next file
	rows  int64 // the current file's rows not yet handed on
	ended bool  // MaxRows is reached
}

func (r *rowsReader) Schema() *dtype.Schema { return r.out }
func (r *rowsReader) Close() error          { return nil }

func (r *rowsReader) Next(ctx context.Context) (_ *data.Batch, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for r.rows == 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if r.ended || r.k >= len(r.src.opens) {
			return nil, io.EOF
		}
		f, closer, err := r.src.openFile(ctx, r.k, pages)
		if err != nil {
			return nil, err
		}
		r.rows = f.NumRows()
		closer()
		r.k++
	}
	n := int(min(r.rows, int64(r.size)))
	if r.left > 0 {
		n = min(n, r.left)
		r.left -= n
		r.ended = r.left == 0
	}
	r.rows -= int64(n)
	if r.ended {
		r.rows = 0
	}
	return data.NewBatchRows(r.out, nil, n), nil
}

type reader struct {
	src       *Source
	full      *dtype.Schema
	out       *dtype.Schema
	cols      []colPlan // what to read, in output order
	batchSize int
	remaining int // rows left to produce; 0 means unlimited
	preds     []expr.Node
	eval      func(context.Context, expr.Node, *data.Batch) (*data.Column, error)
	df        *dictFilter
	wide      int        // under df, the rows the next read takes; see widen
	pages     *scanPages // what the scan's page readers hold, told to its engine

	mu      sync.Mutex
	err     error  // a recovered panic, returned from every Next after it
	name    string // the file being read, for errors
	fileIdx int
	pf      *file.Reader
	lay     fileLayout // how pf stores the reconciled schema
	closeFn func()
	rgIndex int
	chunks  []colReader
	rgLeft  int // rows left in the current row group
	done    bool
}

func (r *reader) Schema() *dtype.Schema { return r.out }

// Close releases the chunk readers and the file.
//
// It takes the same lock Next does. Without it a worker decoding a row group races
// the driver's teardown over r.chunks and r.closeFn — latent while the engine was
// single-threaded, reachable now.
func (r *reader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	err := r.closeLocked()
	r.pages.close()
	return err
}

// closeLocked is Close for callers that already hold the lock — openNext closes the
// finished row group from inside Next.
func (r *reader) closeLocked() error {
	r.closeChunks()
	if r.closeFn != nil {
		r.closeFn()
		r.closeFn = nil
		r.pf = nil
	}
	return nil
}

func (r *reader) closeChunks() {
	for _, c := range r.chunks {
		if c != nil {
			c.close()
		}
	}
	r.chunks = nil
}

// openNext advances to the next readable row group, opening the next file when the
// current one runs out and skipping any row group the pruner rules out.
func (r *reader) openNext(ctx context.Context) error {
	for {
		if r.pf == nil {
			k, err := r.src.nextKept(ctx, r.fileIdx, r.full, r.preds, r.eval)
			if err != nil {
				return err
			}
			if r.fileIdx = k; r.fileIdx >= len(r.src.opens) {
				return io.EOF
			}
			r.name = r.src.partName(r.fileIdx)
			pf, closeFn, err := r.src.openFile(ctx, r.fileIdx, r.pages.alloc())
			if err != nil {
				return err
			}
			// Held at once, so Close releases the file even if what follows panics.
			r.pf, r.closeFn = pf, closeFn
			lay, err := r.src.layoutFor(r.full, pf.MetaData(), r.fileIdx)
			if err == nil {
				for _, p := range r.cols {
					if e := lay.unread[p.field]; e != nil {
						err = e
						break
					}
				}
			}
			if err != nil {
				closeFn()
				r.pf, r.closeFn = nil, nil
				return err
			}
			r.fileIdx++
			r.lay = lay
			r.rgIndex = -1
		}

		r.closeChunks()
		r.rgIndex++
		if r.rgIndex >= r.pf.NumRowGroups() {
			r.closeFn()
			r.closeFn, r.pf = nil, nil
			continue // next file
		}

		rgMeta := r.pf.MetaData().RowGroup(r.rgIndex)
		skip, err := r.shouldSkip(rgMeta)
		if err != nil {
			return err
		}
		r.src.countGroup(skip)
		if skip {
			continue
		}

		rg := r.pf.RowGroup(r.rgIndex)
		chunks := make([]colReader, len(r.cols))
		for i, p := range r.cols {
			c, err := r.openColumn(rg, rgMeta, p)
			if err != nil {
				return err
			}
			chunks[i] = c
		}
		r.chunks = chunks
		r.rgLeft = int(rgMeta.NumRows())
		return nil
	}
}

// colPlan is one OUTPUT column, by its index in the reconciled schema.
//
// The file leaves it is read from are NOT here, because they differ from file to
// file: each file's come from its own layout (r.lay). They used to be fixed here
// from the first file, which read every later file by the first file's positions.
type colPlan struct {
	field int // index into the reconciled schema
}

// fileView is one open file as the readers need it: its name for errors, its
// footer, and how it stores the reconciled schema.
type fileView struct {
	name   string
	pf     *file.Reader
	lay    fileLayout
	consts []*data.Column // by field, the file's partition values and path; nil for none
}

// openColumn opens the chunk readers for one output column of the current file.
func (r *reader) openColumn(rg *file.RowGroupReader,
	rgMeta *metadata.RowGroupMetaData, p colPlan) (colReader, error) {
	return openColumnIn(r.full, fileView{name: r.name, pf: r.pf, lay: r.lay, consts: r.src.constsOf(r.fileIdx - 1)},
		rg, rgMeta, p)
}

// constsOf is file k's partition values and path, by field; nil for none.
func (s *Source) constsOf(k int) []*data.Column {
	if s.consts == nil {
		return nil
	}
	return s.consts[k]
}

// openColumnIn opens the chunk readers for one output column of file fv.
//
// A struct needs one per field plus the group's own definition level, which is what
// separates "this struct is absent" from "this struct is here and its fields are
// null". Every other shape is a single leaf.
func openColumnIn(full *dtype.Schema, fv fileView, rg *file.RowGroupReader,
	rgMeta *metadata.RowGroupMetaData, p colPlan) (colReader, error) {

	f := full.Field(p.field)
	if p.field < len(fv.consts) && fv.consts[p.field] != nil {
		return &constReader{v: fv.consts[p.field]}, nil
	}
	if fv.lay.missing != nil && fv.lay.missing[p.field] {
		return &constReader{v: data.NewNull(f.Name, f.Type, 1)}, nil
	}

	// dt is passed in rather than read off f, because a struct's fields each have
	// their own type and the leaf reader is what decodes them.
	leafReader := func(ci int, dt dtype.DataType) (colReader, error) {
		if err := checkEncodings(rgMeta, ci, f.Name); err != nil {
			return nil, err
		}
		if desc := fv.pf.MetaData().Schema.Column(ci); ownable(rgMeta, ci, desc, dt) {
			pr, err := rg.GetColumnPageReader(ci)
			if err != nil {
				return nil, uerr.Wrap(err, uerr.KindIO, "scan_parquet",
					"opening column %q of %s", f.Name, fv.name)
			}
			return newOwnColReader(pr, desc, dt)
		}
		cr, err := rg.Column(ci)
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindIO, "scan_parquet",
				"opening column %q of %s", f.Name, fv.name)
		}
		return newColReader(cr, fv.pf.MetaData().Schema.Column(ci), dt)
	}

	leaves := fv.lay.leaves[p.field]
	if fv.lay.from != nil && fv.lay.from[p.field] != nil {
		// Read as the file holds it, and widened (step 348).
		c, err := leafReader(leaves[0], *fv.lay.from[p.field])
		if err != nil {
			return nil, err
		}
		return &castReader{inner: c, to: f.Type}, nil
	}
	if f.Type.ID() != dtype.TypeStruct {
		return leafReader(leaves[0], f.Type)
	}
	// Whether the struct can be null at all is THIS file's answer. The reconciled
	// field is nullable if any file's is, and a struct that is REQUIRED in this
	// file has no level of its own to read: tracking one anyway read a present
	// struct with a null first field as a null struct.
	return newStructReader(f, leaves, fv.lay.optional[p.field], leafReader)
}

// shouldSkip asks the pruner whether this row group can be ruled out.
func (r *reader) shouldSkip(rg *metadata.RowGroupMetaData) (bool, error) {
	return shouldSkipIn(r.src, r.full, r.lay, r.preds, rg)
}

// shouldSkipIn asks the pruner whether row group rg of a file laid out as lay can be
// ruled out.
func shouldSkipIn(src *Source, full *dtype.Schema, lay fileLayout, preds []expr.Node,
	rg *metadata.RowGroupMetaData) (bool, error) {
	if !src.opts.Prune || len(preds) == 0 {
		return false, nil
	}
	// The leafOf for that file: a flat column's one leaf.
	leafOf := func(name string) (int, bool) {
		i := full.IndexOf(name)
		if i < 0 || full.Field(i).Type.IsNested() || len(lay.leaves[i]) != 1 {
			return 0, false
		}
		return lay.leaves[i][0], true
	}
	return canSkipRowGroup(rg, leafOf, preds, lay.fullStats)
}

func (r *reader) Next(ctx context.Context) (_ *data.Batch, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// A panic decoding a page is the file's, and it leaves this reader's chunks in
	// no state to continue from: the error is returned from every Next after it.
	defer func() {
		if v := recover(); v != nil {
			r.err = corrupt(v, r.name)
			err = r.err
		}
	}()
	if r.err != nil {
		return nil, r.err
	}
	if r.done {
		return nil, io.EOF
	}

	want := r.batchSize
	if r.remaining > 0 && r.remaining < want {
		want = r.remaining
	}

	for {
		// This loop can go round many times without returning — a run of pruned row
		// groups, a file's worth of empty ones — and it holds the lock throughout.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if r.chunks == nil || r.rgLeft <= 0 {
			switch err := r.openNext(ctx); {
			case err == io.EOF:
				r.done = true
				return nil, io.EOF
			case err != nil:
				return nil, err
			}
			continue
		}

		n := want
		if r.df != nil {
			n = r.wide
		}
		b, rows, err := readBatch(r.chunks, r.out, min(n, r.rgLeft), r.rgIndex, r.name, r.rgLeft, r.df)
		if err != nil {
			return nil, err
		}
		r.rgLeft -= rows
		if r.df != nil {
			r.wide = widen(r.batchSize, rows, b.Rows())
		}
		if b.Rows() == 0 {
			continue // every row failed a dictionary's conjunct
		}

		if r.remaining > 0 {
			r.remaining -= rows
			if r.remaining <= 0 {
				r.done = true
			}
		}
		return b, nil
	}
}

// readBatch reads up to n rows from every chunk of one row group and builds a batch,
// returning it and the rows it holds. rgLeft is what the footer says remains of the
// row group, for the error when the chunks hold less.
func readBatch(chunks []colReader, out *dtype.Schema, n, rgIndex int, name string,
	rgLeft int, df *dictFilter) (*data.Batch, int, error) {

	rows := -1
	agree := func(got int) error {
		// Every column of a row group has the same number of rows. A disagreement
		// means the file's column chunks are inconsistent, and continuing would
		// build a ragged batch.
		if rows == -1 {
			rows = got
		} else if got != rows {
			return uerr.New(uerr.KindValue, "scan_parquet",
				"row group %d: columns disagree on row count (%d vs %d)",
				rgIndex, rows, got)
		}
		return nil
	}
	// The columns a dictionary judges are marked first, so every one has cleared
	// keep before any other column copies a row (dictfilter.go). The rest are then
	// read at the kept rows where they can be, and every other read whole and taken
	// to them.
	var keep, marked []bool
	if df != nil {
		for i, c := range chunks {
			bc, ok := df.marker(i, c)
			if !ok {
				continue
			}
			if keep == nil {
				keep, marked = make([]bool, n), make([]bool, len(chunks))
				for j := range keep {
					keep[j] = true
				}
			}
			got, err := bc.pages.mark(n, keep, df, i, bc.dt)
			if err != nil {
				return nil, 0, err
			}
			if err := agree(got); err != nil {
				return nil, 0, err
			}
			marked[i] = true
		}
	}
	// The kept rows' positions. Where none is dropped every column is read whole, as
	// it would be with no predicate, and nothing is taken.
	var sel []int32
	if keep != nil && rows > 0 {
		kept := 0
		for _, k := range keep[:rows] {
			if k {
				kept++
			}
		}
		sel = make([]int32, 0, kept)
		for j, k := range keep[:rows] {
			if k {
				sel = append(sel, int32(j))
			}
		}
	}
	dropped := keep != nil && len(sel) < rows
	for i, c := range chunks {
		if marked != nil && marked[i] {
			c.(*byteArrayCol).pages.emit(c.(*byteArrayCol), sel)
			continue
		}
		if kr, ok := c.(keptReader); ok && dropped && kr.keeps() {
			got, err := kr.readKept(keep[:rows], sel)
			if err != nil {
				return nil, 0, err
			}
			if err := agree(got); err != nil {
				return nil, 0, err
			}
			marked[i] = true // its rows are the kept ones already
			continue
		}
		got, err := c.read(n)
		if err != nil {
			return nil, 0, err
		}
		if err := agree(got); err != nil {
			return nil, 0, err
		}
	}
	if rows == 0 {
		// The footer promised rows that the column chunks do not hold. This looped
		// forever, holding the lock, and never looked at ctx.
		return nil, 0, uerr.New(uerr.KindIO, "scan_parquet",
			"row group %d of %s declares %d more rows than its column chunks hold",
			rgIndex, name, rgLeft).
			Hint("the file is corrupt or was truncated")
	}
	cols := make([]*data.Column, len(chunks))
	for i, c := range chunks {
		cols[i] = c.finish(out.Field(i).Name)
	}
	if keep != nil {
		kept, err := compact(cols, marked, keep, rows, sel)
		if err != nil {
			return nil, 0, err
		}
		if kept == 0 {
			return data.NewBatchRows(out, cols, 0), rows, nil
		}
	}
	b, err := data.NewBatch(out, cols)
	return b, rows, err
}

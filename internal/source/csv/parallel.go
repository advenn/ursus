package csv

import (
	"bytes"
	"context"
	"io"

	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// nextParallel returns the next batch of the parallel path, or (nil, nil) when the
// splitter has stopped and the serial scanner is to read the rest of the stream.
//
// Blocks parse in the background, up to twice the thread count at once, each on a
// goroutine of its own from the moment it is cut, and Next waits only for the
// oldest. So cutting new blocks overlaps parsing the ones before, and a slow block
// holds up only the batches after it. Batches are delivered in file order, cut to
// the batch size.
//
// A block's error is delivered after the blocks before it, and is the one the
// serial path would have met first. The rows of the failing block before its bad
// record are not delivered, where the serial path delivers the full batches before
// it: how many rows arrive before an error already depended on the batch size.
func (r *reader) nextParallel(ctx context.Context) (*data.Batch, error) {
	for len(r.ready) == 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for r.splitErr == nil && !r.splitEnd && len(r.window) < 2*r.threads {
			blk, ok, err := r.split.next()
			if err != nil {
				r.splitErr = uerr.Annotate(err, "scan_csv", r.src.partName(r.next-1))
				break
			}
			if !ok {
				r.splitEnd = true
				break
			}
			blk.wanted, blk.part = r.wanted, r.src.partName(r.next-1)
			r.window = append(r.window, r.parseLater(blk))
		}
		if len(r.window) > 0 {
			p := r.window[0]
			<-p.done
			r.window[0] = nil
			r.window = r.window[1:]
			if p.err != nil {
				r.failed, r.split, r.window = p.err, nil, nil
				return nil, p.err
			}
			for off := 0; off < p.b.Rows(); off += r.batchSize {
				r.ready = append(r.ready, p.b.Slice(off, min(r.batchSize, p.b.Rows()-off)))
			}
			r.row = p.blk.firstRow + p.blk.rows
			continue
		}

		// Nothing in flight: an error reading, the splitter stopped, or the stream
		// ended.
		if r.splitErr != nil {
			r.failed, r.split = r.splitErr, nil
			return nil, r.failed
		}
		if r.split.stopped {
			sc := newScannerFrom(r.split.rest(), r.src.opts)
			sc.width, sc.line = r.split.width, r.split.line
			r.sc, r.row, r.split = sc, r.split.row, nil
			return nil, nil
		}
		switch err := r.openNext(ctx); {
		case err == io.EOF:
			r.split, r.done = nil, true
			return nil, io.EOF
		case err != nil:
			return nil, err
		}
		r.split, r.splitEnd = newSplitter(r.sc, r.split.row), false
	}
	b := r.ready[0]
	r.ready[0] = nil
	r.ready = r.ready[1:]
	return b, nil
}

// parsing is one block being parsed in the background.
type parsing struct {
	blk  block
	done chan struct{}
	b    *data.Batch
	err  error
}

// parseLater starts blk's parse on a goroutine of its own. Close waits for every
// one to finish.
func (r *reader) parseLater(blk block) *parsing {
	p := &parsing{blk: blk, done: make(chan struct{})}
	r.inflight.Go(func() {
		defer close(p.done)
		defer func() {
			if v := recover(); v != nil {
				p.err = uerr.Internalf("scan_csv: parsing a block panicked: %v", v)
			}
		}()
		p.b, p.err = r.parseBlock(blk)
	})
	return p
}

// blockStart, when a test sets it, runs as each block's parse starts.
var blockStart func()

// parseBlock parses one block of whole records into a batch, with a scanner and
// builders of its own. Its rows and lines count from the block's place in the
// stream, so its errors name what the serial path's would.
func (r *reader) parseBlock(blk block) (*data.Batch, error) {
	if blockStart != nil {
		blockStart()
	}
	sc := newScannerFrom(bytes.NewReader(blk.data), r.src.opts)
	sc.width, sc.line = blk.width, blk.firstLine
	builders := make([]colBuilder, r.out.Len())
	for i, f := range r.out.All() {
		b, err := newBuilder(f.Type)
		if err != nil {
			return nil, uerr.Annotate(err, "scan_csv", r.src.desc)
		}
		builders[i] = b
	}
	row := blk.firstRow
	for sc.Next() {
		row++
		if err := r.appendTo(sc, builders, blk.wanted, row); err != nil {
			return nil, err
		}
	}
	if err := sc.Err(); err != nil {
		// Named as the serial path names a scanner's error.
		return nil, uerr.Annotate(err, "scan_csv", blk.part)
	}
	rows := row - blk.firstRow
	cols := make([]*data.Column, r.out.Len())
	for i, f := range r.out.All() {
		cols[i] = builders[i].finish(f.Name)
	}
	if err := r.checkNonNullable(cols, rows, blk.firstRow); err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return data.NewBatchRows(r.out, nil, rows), nil
	}
	return data.NewBatch(r.out, cols)
}

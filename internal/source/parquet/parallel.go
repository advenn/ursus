package parquet

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"

	"github.com/apache/arrow-go/v18/parquet/metadata"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// parallelReader decodes several row groups at once and returns their batches in
// file order, as the serial reader would.
//
// # Why
//
// The serial reader decodes every column of every row group on one goroutine, under
// one lock, and everything downstream waits on it. Step 101 measured it at 37–47% of
// the CPU of PDS-H q6 and q7 and h2o j3 — and so at roughly their whole wall time,
// with fewer than 2.3 of 8 cores ever busy. A row group is independent of every
// other: its column chunks are separate byte ranges, decoded by separate readers.
//
// # How the order is kept
//
// Next issues row groups in file order, pruning as the serial reader does, and
// starts one goroutine per row group with at most threads of them in flight. Each
// streams its batches into its own small channel, and Next reads the channels in
// the order they were issued. A row group finishes before the next one's batches are
// returned, so the rows come out exactly as the serial reader returns them.
//
// # How much it holds
//
// At most threads row groups are decoding, each at most rgBuffer batches ahead of
// the reader, plus the one batch it is building: about threads × 3 batches in
// flight. A row group is issued only when an earlier one has been read to the end.
//
// # When it is not used
//
// With one thread, or under MaxRows — a limit, where reading ahead would decode row
// groups the query never wants — Open returns the serial reader.
type parallelReader struct {
	src       *Source
	full      *dtype.Schema
	out       *dtype.Schema
	cols      []colPlan
	batchSize int
	preds     []expr.Node
	threads   int

	mu        sync.Mutex // Next's state, against Close
	err       error      // the first failure, returned from every Next after it
	fileIdx   int        // the next file to open
	cur       *sharedFile
	rgIndex   int  // the last row group of cur considered
	issuedAll bool // every row group of every file has been considered
	inflight  []*rgTask

	stop     chan struct{} // closed by Close: workers stop sending and return
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// sharedFile is a file several row-group workers read at once. Its readers are
// independent — arrow-go's RowGroup and each column's page reader read their own byte
// ranges through the file's io.ReaderAt, whose contract allows parallel ReadAt — so
// what is shared is only the footer, and the file is closed when the last user lets
// go.
type sharedFile struct {
	fileView
	closeFn func()
	refs    atomic.Int32
}

func (f *sharedFile) release() {
	if f.refs.Add(-1) == 0 {
		f.closeFn()
	}
}

// rgTask is one row group being decoded. out carries its batches, then an error or
// nothing, and is closed when the worker is done.
type rgTask struct {
	out chan rgResult
}

type rgResult struct {
	b   *data.Batch
	err error
}

// rgBuffer is how many batches a row group may decode ahead of the reader.
const rgBuffer = 2

// errClosed is what Next returns once Close has run.
var errClosed = errors.New("parquet: reader closed")

func (r *parallelReader) Schema() *dtype.Schema { return r.out }

// openFirst opens the first file, as the serial reader does inside Open, so a file
// that cannot be opened or read is refused while the query is being planned. No
// goroutine starts until the first Next.
func (r *parallelReader) openFirst(ctx context.Context) error {
	if len(r.src.opens) == 0 {
		return nil
	}
	return r.openFile(ctx)
}

// openFile opens file r.fileIdx and makes it cur.
func (r *parallelReader) openFile(ctx context.Context) error {
	name := r.src.partName(r.fileIdx)
	pf, closeFn, err := r.src.openFile(ctx, r.fileIdx)
	if err != nil {
		return err
	}
	lay, err := r.src.layoutFor(r.full, pf.MetaData().Schema, r.fileIdx)
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
		return err
	}
	f := &sharedFile{fileView: fileView{name: name, pf: pf, lay: lay}, closeFn: closeFn}
	f.refs.Store(1) // the issuer's own, dropped when it moves past the last row group
	r.cur, r.rgIndex = f, -1
	r.fileIdx++
	return nil
}

// issue starts the next row group the pruner does not rule out, and reports false
// when there is none left in any file.
func (r *parallelReader) issue(ctx context.Context) (bool, error) {
	for {
		if r.cur == nil {
			if r.fileIdx >= len(r.src.opens) {
				return false, nil
			}
			if err := r.openFile(ctx); err != nil {
				return false, err
			}
		}
		r.rgIndex++
		if r.rgIndex >= r.cur.pf.NumRowGroups() {
			r.cur.release()
			r.cur = nil
			continue
		}
		rgMeta := r.cur.pf.MetaData().RowGroup(r.rgIndex)
		skip, err := shouldSkipIn(r.src, r.full, r.cur.lay, r.preds, rgMeta)
		if err != nil {
			return false, err
		}
		r.src.countGroup(skip)
		if skip {
			continue
		}
		t := &rgTask{out: make(chan rgResult, rgBuffer)}
		r.cur.refs.Add(1)
		r.inflight = append(r.inflight, t)
		r.wg.Add(1)
		go r.decode(r.cur, r.rgIndex, rgMeta, t)
		return true, nil
	}
}

// decode reads one row group into batches and sends them, in order, on t.out.
func (r *parallelReader) decode(f *sharedFile, rgIndex int, rgMeta *metadata.RowGroupMetaData, t *rgTask) {
	defer r.wg.Done()
	defer f.release()
	defer close(t.out)

	send := func(res rgResult) bool {
		select {
		case t.out <- res:
			return true
		case <-r.stop:
			return false
		}
	}
	var chunks []colReader
	defer func() {
		for _, c := range chunks {
			if c != nil {
				c.close()
			}
		}
	}()

	// finished is set once the row group has been read to the end or its error
	// sent. A worker that stops any other way — a runtime.Goexit, which runs the
	// defers and recovers nothing — would otherwise close its channel as if it had
	// finished, and the reader would skip the rows it never sent.
	finished := false
	defer func() {
		if v := recover(); v != nil {
			send(rgResult{err: corrupt(v, f.name)})
			finished = true
		}
		if !finished {
			send(rgResult{err: uerr.Internalf(
				"parquet: decoding row group %d of %s stopped before it finished", rgIndex, f.name)})
		}
	}()

	rg := f.pf.RowGroup(rgIndex)
	chunks = make([]colReader, len(r.cols))
	for i, p := range r.cols {
		c, err := openColumnIn(r.full, f.fileView, rg, rgMeta, p)
		if err != nil {
			send(rgResult{err: err})
			finished = true
			return
		}
		chunks[i] = c
	}
	for left := int(rgMeta.NumRows()); left > 0; {
		select {
		case <-r.stop:
			finished = true
			return
		default:
		}
		b, rows, err := readBatch(chunks, r.out, min(r.batchSize, left), rgIndex, f.name, left)
		if err != nil {
			send(rgResult{err: err})
			finished = true
			return
		}
		left -= rows
		if !send(rgResult{b: b}) {
			finished = true
			return
		}
	}
	finished = true
}

func (r *parallelReader) Next(ctx context.Context) (_ *data.Batch, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	head, err := r.head(ctx)
	if head == nil || err != nil {
		return nil, err
	}
	// Waiting is done without the lock, so Close can stop the workers while Next is
	// waiting on one.
	for {
		select {
		case res, ok := <-head.out:
			r.mu.Lock()
			if !ok {
				r.inflight = r.inflight[1:]
				r.mu.Unlock()
				if head, err = r.head(ctx); head == nil || err != nil {
					return nil, err
				}
				continue
			}
			if res.err != nil && r.err == nil {
				r.err = res.err
			}
			r.mu.Unlock()
			return res.b, res.err
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-r.stop:
			return nil, errClosed
		}
	}
}

// head issues row groups up to the thread count and returns the oldest one in
// flight, or nil at the end of the input.
func (r *parallelReader) head(ctx context.Context) (_ *rgTask, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// A panic reading a footer or its row-group metadata is the file's, as it is in
	// the serial reader.
	defer func() {
		if v := recover(); v != nil {
			name := r.src.desc
			if r.cur != nil {
				name = r.cur.name
			}
			r.err = corrupt(v, name)
			err = r.err
		}
	}()
	if r.err != nil {
		return nil, r.err
	}
	select {
	case <-r.stop:
		return nil, errClosed
	default:
	}
	for !r.issuedAll && len(r.inflight) < r.threads {
		ok, err := r.issue(ctx)
		if err != nil {
			r.err = err
			return nil, err
		}
		r.issuedAll = !ok
	}
	if len(r.inflight) == 0 {
		return nil, io.EOF
	}
	return r.inflight[0], nil
}

// Close stops every worker, waits for them, and closes the files.
//
// stop is closed under the lock, and head issues only under it after checking stop,
// so no worker can start once Close has begun: a WaitGroup's Add may not race its
// Wait. The wait itself is outside the lock, because a worker can be blocked sending
// to a Next that is itself waiting for the lock.
func (r *parallelReader) Close() error {
	r.mu.Lock()
	r.stopOnce.Do(func() { close(r.stop) })
	r.mu.Unlock()
	r.wg.Wait()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur != nil {
		r.cur.release()
		r.cur = nil
	}
	r.inflight = nil
	return nil
}

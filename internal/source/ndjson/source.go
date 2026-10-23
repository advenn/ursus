package ndjson

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strconv"
	"sync"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/source"
	"github.com/advenn/ursus/internal/uerr"
)

// DefaultInferLines is how many lines a schema is inferred from, Polars'
// infer_schema_length.
const DefaultInferLines = 100

const defaultBatchSize = 1 << 16

// File is one NDJSON stream, named in Explain and in an error, and opened afresh for
// the schema and for each run. ctx is the query's, and outlives the stream, which the
// reader closes when it is done with it.
type File struct {
	Name string
	Open func(ctx context.Context) (io.ReadCloser, error)
}

// Options are a scan's: its schema, or how many lines to infer one from, and whether
// a value its column cannot take is a null rather than an error, Polars'
// ignore_errors.
type Options struct {
	Schema       *dtype.Schema // nil to infer one
	InferLines   int           // lines to infer from; 0 every line
	IgnoreErrors bool
}

// Source is NDJSON streams as one scan, read in order: of the given schema, or of the
// one the first stream's first lines infer, as Polars infers several files' from the
// first.
type Source struct {
	desc  string
	files []File
	opts  Options
	given *dtype.Schema // the given schema, every column nullable
	bad   error         // the given schema's error

	once   sync.Once
	schema *dtype.Schema
	err    error
}

// New is a source of files, described as desc in Explain. Every column is nullable,
// whatever a given schema declares: a line may leave any key out.
func New(desc string, files []File, o Options) *Source {
	s := &Source{desc: desc, files: files, opts: o}
	if o.Schema != nil {
		fields := o.Schema.FieldSlice()
		for i := range fields {
			fields[i].Nullable = true
		}
		s.given, s.bad = dtype.NewSchema(fields...)
	}
	return s
}

func (s *Source) Name() string     { return "ndjson" }
func (s *Source) Describe() string { return s.desc }
func (s *Source) Caps() plan.Caps  { return plan.Caps{Projection: true} }

// Schema is the given schema, or the one the first lines infer, read once.
func (s *Source) Schema(ctx context.Context) (*dtype.Schema, error) {
	if s.given != nil || s.bad != nil {
		return s.given, s.bad
	}
	s.once.Do(func() {
		// Recovered inside the function Do runs: a panic past it would leave the
		// Once done and every later call a source with no schema and no error.
		defer func() {
			if p := recover(); p != nil {
				s.schema, s.err = nil, uerr.FromPanic(p, "read_ndjson")
			}
		}()
		s.schema, s.err = s.inferred(ctx)
	})
	return s.schema, s.err
}

func (s *Source) inferred(ctx context.Context) (*dtype.Schema, error) {
	if len(s.files) == 0 {
		return nil, uerr.New(uerr.KindValue, "read_ndjson", "no files given")
	}
	f := s.files[0]
	rc, err := f.Open(ctx)
	if err != nil {
		return nil, uerr.Wrap(err, uerr.KindIO, "read_ndjson", "opening %s", f.Name)
	}
	defer rc.Close()
	lr := newLineReader(rc)
	var lines [][]byte
	var at []int
	for s.opts.InferLines <= 0 || len(lines) < s.opts.InferLines {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, ok, err := lr.next()
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindIO, "read_ndjson", "reading %s", f.Name)
		}
		if !ok {
			break
		}
		lines, at = append(lines, bytes.Clone(line)), append(at, lr.line)
	}
	of := ""
	if len(s.files) > 1 {
		of = f.Name
	}
	return inferSchema(lines, at, of)
}

// Open reads the streams a batch at a time, the projection's columns only.
func (s *Source) Open(ctx context.Context, spec source.ScanSpec) (source.BatchSource, error) {
	full, err := s.Schema(ctx)
	if err != nil {
		return nil, err
	}
	out := full
	if spec.Projection != nil {
		if out, err = full.Select(spec.Projection); err != nil {
			return nil, err
		}
	}
	read := make([]dtype.Field, out.Len())
	for i, f := range out.All() {
		read[i] = dtype.Field{Name: f.Name, Type: f.Type, Nullable: true}
	}
	size := spec.BatchSize
	if size <= 0 {
		size = defaultBatchSize
	}
	return &reader{src: s, out: out, read: dtype.Struct(read...), size: size, max: spec.MaxRows,
		threads: max(1, spec.Threads)}, nil
}

// natural is the type a column of t is read in, which a cast then makes t: each type
// inference gives as itself, a narrower integer as Int64, UInt64 as Int128, Float32
// as Float64, and every type JSON has no word for as its text, which the cast parses:
// a Date, a Datetime, a Time, a Decimal, an Enum.
func natural(t dtype.DataType) dtype.DataType {
	switch {
	case t.IsListLike():
		// An Array is read as a List, and the cast checks each row's width (step 341).
		return dtype.List(natural(t.Inner()))
	case t.ID() == dtype.TypeStruct:
		fs := make([]dtype.Field, len(t.Fields()))
		for i, f := range t.Fields() {
			fs[i] = dtype.Field{Name: f.Name, Type: natural(f.Type), Nullable: true}
		}
		return dtype.Struct(fs...)
	case t.IsNull(), t == dtype.Bool, t == dtype.Int64, t == dtype.Int128, t == dtype.Float64, t == dtype.String:
		return t
	case t.ID() == dtype.TypeUint64:
		return dtype.Int128
	case t.IsInteger():
		return dtype.Int64
	case t.IsFloat():
		return dtype.Float64
	}
	return dtype.String
}

// reader is one run's read of the stream: a block of lines at a time, the block cut
// at its newlines into a part a worker, the parts parsed at once, and their batches
// handed out in the stream's order.
type reader struct {
	src       *Source
	out       *dtype.Schema
	read      dtype.DataType // the out columns as a Struct
	size, max int
	threads   int

	mu      sync.Mutex
	file    int // the stream read, of the source's
	rc      io.ReadCloser
	blocks  *blockReader
	ahead   *readAhead // the stream's next block, read while the last was parsed
	line    int        // the stream's lines read, for an error's place
	emitted int
	queue   []*data.Batch
	done    bool
}

// readAhead is a block read while another is parsed.
type readAhead struct {
	block []byte
	err   error
}

func (r *reader) Schema() *dtype.Schema { return r.out }

func (r *reader) Next(ctx context.Context) (*data.Batch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for len(r.queue) == 0 {
		if r.done || r.max > 0 && r.emitted >= r.max {
			r.done = true
			return nil, io.EOF
		}
		if err := r.fill(ctx); err != nil {
			r.done = true
			return nil, err
		}
	}
	b := r.queue[0]
	r.queue = r.queue[1:]
	if r.max > 0 && r.emitted+b.Rows() > r.max {
		b = b.Slice(0, r.max-r.emitted)
	}
	r.emitted += b.Rows()
	return b, nil
}

// fill parses the next block into the queue, its parts at once.
func (r *reader) fill(ctx context.Context) error {
	if r.rc == nil {
		if r.file == len(r.src.files) {
			r.done = true
			return nil
		}
		f := r.src.files[r.file]
		rc, err := f.Open(ctx)
		if err != nil {
			return uerr.Wrap(err, uerr.KindIO, "read_ndjson", "opening %s", f.Name)
		}
		r.rc, r.blocks, r.line = rc, newBlockReader(rc, r.threads), 0
	}
	name := r.src.files[r.file].Name
	var block []byte
	var err error
	if r.ahead != nil {
		block, err = r.ahead.block, r.ahead.err
		r.ahead = nil
	} else {
		block, err = r.blocks.next()
	}
	if err != nil {
		return uerr.Wrap(err, uerr.KindIO, "read_ndjson", "reading %s", name)
	}
	if block == nil {
		err := r.rc.Close()
		r.rc, r.blocks = nil, nil
		r.file++
		if err != nil {
			return uerr.Wrap(err, uerr.KindIO, "read_ndjson", "closing %s", name)
		}
		return nil
	}
	parts := cutAtNewlines(block, r.threads)
	batches := make([]*data.Batch, len(parts))
	lines := make([]int, len(parts))
	errs := make([]error, len(parts))
	var wg sync.WaitGroup
	// The next block is read while this one is parsed. A panic in it, in a reader
	// a caller gave, is an error too.
	ahead := &readAhead{}
	wg.Go(func() {
		defer func() {
			if p := recover(); p != nil {
				ahead.block, ahead.err = nil, uerr.FromPanic(p, "read_ndjson")
			}
		}()
		ahead.block, ahead.err = r.blocks.next()
	})
	for i, part := range parts {
		wg.Go(func() {
			defer func() {
				if p := recover(); p != nil {
					errs[i] = uerr.FromPanic(p, "read_ndjson")
				}
			}()
			if ctx.Err() == nil {
				batches[i], lines[i], errs[i] = r.parse(part)
			}
			if errs[i] != nil && lines[i] > 0 {
				errs[i] = lineError{line: lines[i], err: errs[i]}
			}
		})
	}
	wg.Wait()
	r.ahead = ahead
	if err := ctx.Err(); err != nil {
		return err
	}
	for i, b := range batches {
		if le, ok := errs[i].(lineError); ok {
			return r.placed(le.err, r.line+le.line)
		} else if errs[i] != nil {
			return errs[i]
		}
		r.line += lines[i]
		for at := 0; at < b.Rows(); at += r.size {
			r.queue = append(r.queue, b.Slice(at, min(r.size, b.Rows()-at)))
		}
	}
	return nil
}

// lineError is an error on a line of a part, numbered from the part's first.
type lineError struct {
	line int
	err  error
}

func (e lineError) Error() string { return e.err.Error() }

// placed is err, an error on line of the stream read, saying where: "at line 7, key
// o.x", and the stream's name when there are several, "at line 7 of b.ndjson".
func (r *reader) placed(err error, line int) error {
	where := "line " + strconv.Itoa(line)
	if len(r.src.files) > 1 {
		where += " of " + r.src.files[r.file].Name
	}
	var ke *keyError
	if errors.As(err, &ke) {
		where += ", key " + ke.key
		err = ke.err
	}
	return uerr.Annotate(err, "read_ndjson", where)
}

// parse is part's lines as a batch, and how many lines it holds; on an error in a
// line, that line, numbered from the part's first, else 0.
func (r *reader) parse(part []byte) (*data.Batch, int, error) {
	b, err := newBuilder(r.read)
	if err != nil {
		return nil, 0, err
	}
	rows := b.(*structBuilder)
	rows.size(bytes.Count(part, []byte{'\n'}) + 1) // a row a line at most
	p := parser{ignore: r.src.opts.IgnoreErrors, given: r.src.given != nil}
	n, line, whole := 0, 0, part
	for len(part) > 0 {
		text := part
		if end := bytes.IndexByte(part, '\n'); end >= 0 {
			text, part = part[:end], part[end+1:]
		} else {
			part = nil
		}
		line++
		if blank(text) {
			continue
		}
		p.s, p.pos = text, 0
		p.space()
		if text[p.pos] != '{' {
			_, err := parseObject(text) // what the line holds instead
			return nil, line, err
		}
		if err := rows.decodeFields(&p); err != nil {
			return nil, line, err
		}
		if p.space(); p.pos != len(text) {
			return nil, line, p.fail("text after the line's object")
		}
		n++
	}
	cols := make([]*data.Column, r.out.Len())
	for i, f := range r.out.All() {
		c := rows.fields[i].finish(f.Name)
		if c.DType() != f.Type {
			read := c
			if c, err = kernel.Cast(f.Name, f.Type, !p.ignore, read); err != nil {
				if row, refused := castRefusal(read, f.Type); refused != nil {
					return nil, lineOfRow(whole, row), &keyError{key: f.Name, err: refused}
				}
				return nil, 0, uerr.Annotate(err, "read_ndjson", f.Name)
			}
		}
		cols[i] = c
	}
	return data.NewBatchRows(r.out, cols, n), line, nil
}

// castRefusal is the row of the first value of c, a column as read, that a cast to t
// refuses, and the error for it as Polars says it: "cannot parse 'x' (string) as
// Date". The error is nil when no value is found to be the one, and the cast's own
// error stands.
func castRefusal(c *data.Column, t dtype.DataType) (int, error) {
	lossy, err := kernel.Cast(c.Name(), t, false, c)
	if err != nil {
		return 0, nil
	}
	for row := range c.Len() {
		if !c.IsValid(row) || lossy.IsValid(row) {
			continue
		}
		v := value{kind: kString}
		switch {
		case c.DType().ID() == dtype.TypeList:
			v.kind = kArray
		case c.DType().ID() == dtype.TypeStruct:
			v.kind = kObject
		default:
			if c.DType().IsNumeric() {
				v.kind = kInt
			}
			text, err := kernel.Cast(c.Name(), dtype.String, false, c.Slice(row, 1))
			if err != nil {
				return 0, nil
			}
			v.text = text.Strings().Get(0)
		}
		return row, refusal(uerr.KindValue, v, t.String())
	}
	return 0, nil
}

// lineOfRow is the line of part that row was read from, numbered from the part's
// first: the row-th line that is not blank, counting from 0.
func lineOfRow(part []byte, row int) int {
	line := 0
	for len(part) > 0 {
		text := part
		if end := bytes.IndexByte(part, '\n'); end >= 0 {
			text, part = part[:end], part[end+1:]
		} else {
			part = nil
		}
		line++
		if blank(text) {
			continue
		}
		if row == 0 {
			break
		}
		row--
	}
	return line
}

// blank reports whether a line is only JSON's space, which a reader skips.
func blank(line []byte) bool {
	for _, c := range line {
		if c != ' ' && c != '\t' && c != '\r' {
			return false
		}
	}
	return true
}

func (r *reader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done = true
	r.queue = nil
	if r.rc != nil {
		err := r.rc.Close()
		r.rc = nil
		return err
	}
	return nil
}

// cutAtNewlines cuts block into at most n parts of whole lines, of about one size and
// of about 64 KiB at the least: a smaller part is not worth a worker.
func cutAtNewlines(block []byte, n int) [][]byte {
	n = max(1, min(n, len(block)>>16))
	parts := make([][]byte, 0, n)
	for ; n > 1; n-- {
		want := len(block) / n
		end := bytes.IndexByte(block[want:], '\n')
		if end < 0 {
			break
		}
		parts = append(parts, block[:want+end+1])
		block = block[want+end+1:]
	}
	if len(block) > 0 {
		parts = append(parts, block)
	}
	return parts
}

// blockReader reads a stream a block of whole lines at a time: each block ends at a
// newline, or at the stream's end, and the next starts after it. The first block is
// small, for a query that wants only the first rows, and each next one twice the last
// up to 4 MiB a worker.
type blockReader struct {
	r         io.Reader
	size, top int
	rest      []byte // a line begun at the last block's end
	eof       bool
}

func newBlockReader(r io.Reader, threads int) *blockReader {
	return &blockReader{r: r, size: 1 << 20, top: max(1, threads) << 22}
}

// next is the next block, nil at the stream's end. The block is the caller's.
func (b *blockReader) next() ([]byte, error) {
	if b.eof && len(b.rest) == 0 {
		return nil, nil
	}
	buf := make([]byte, len(b.rest), len(b.rest)+b.size)
	copy(buf, b.rest)
	b.rest = nil
	b.size = min(2*b.size, b.top)
	for !b.eof {
		if len(buf) == cap(buf) {
			if bytes.IndexByte(buf, '\n') >= 0 {
				break
			}
			buf = slices.Grow(buf, len(buf)) // a line longer than a block
		}
		n, err := b.r.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		if errors.Is(err, io.EOF) {
			b.eof = true
		} else if err != nil {
			return nil, err
		}
		if len(buf) == cap(buf) && bytes.IndexByte(buf, '\n') >= 0 {
			break
		}
	}
	if b.eof {
		return buf, nil
	}
	cut := bytes.LastIndexByte(buf, '\n') + 1
	b.rest = append([]byte(nil), buf[cut:]...)
	return buf[:cut], nil
}

// lineReader reads lines out of large blocks of a stream, skipping blank ones.
type lineReader struct {
	r          io.Reader
	buf        []byte
	start, end int
	eof        bool
	line       int // the number of the line last read, blank ones counted
}

func newLineReader(r io.Reader) *lineReader {
	return &lineReader{r: r, buf: make([]byte, 1<<20)}
}

// next is the next line that is not blank, without its newline, valid until the next
// call; false at the stream's end.
func (l *lineReader) next() ([]byte, bool, error) {
	for {
		if i := bytes.IndexByte(l.buf[l.start:l.end], '\n'); i >= 0 {
			line := l.buf[l.start : l.start+i]
			l.start += i + 1
			l.line++
			if blank(line) {
				continue
			}
			return line, true, nil
		}
		if l.eof {
			line := l.buf[l.start:l.end]
			l.start = l.end
			l.line++
			if blank(line) {
				return nil, false, nil
			}
			return line, true, nil
		}
		// Keep the line begun, and read more: into the room after it, or a buffer
		// twice as large for a line longer than the one.
		if l.start > 0 {
			l.end = copy(l.buf, l.buf[l.start:l.end])
			l.start = 0
		}
		if l.end == len(l.buf) {
			l.buf = append(l.buf, make([]byte, len(l.buf))...)
		}
		n, err := l.r.Read(l.buf[l.end:])
		l.end += n
		if errors.Is(err, io.EOF) {
			l.eof = true
		} else if err != nil {
			return nil, false, err
		}
	}
}

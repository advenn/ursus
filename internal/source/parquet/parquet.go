package parquet

import (
	"context"
	"io"
	"sync"

	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/metadata"
	"github.com/apache/arrow-go/v18/parquet/schema"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/source"
	"github.com/advenn/ursus/internal/uerr"
)

// Options configures a Parquet source.
type Options struct {
	// Prune enables row-group skipping from column statistics. On by default; the
	// switch exists so a wrong answer can be bisected to the pruner, which is the
	// one part of this package that can change which rows come back.
	Prune bool
}

// DefaultOptions enables pruning.
func DefaultOptions() Options { return Options{Prune: true} }

// Opener produces a fresh handle on the same file.
type Opener func() (parquet.ReaderAtSeeker, io.Closer, error)

// Source is one or more Parquet files read as a single table.
type Source struct {
	opens []Opener
	desc  string
	opts  Options

	once   sync.Once
	schema *dtype.Schema
	err    error

	// leaves[i] is the file LEAF column set that schema field i is read from, or nil
	// when the field cannot be read. Field index and leaf index are the same
	// number only for a flat file; see fileSchema.
	leaves [][]int
	// unread holds, per unreadable field, the refusal to return if a query asks
	// for it. Non-nil entries are the nested columns.
	unread map[int]error

	// Row groups read and skipped, across every reader this source has opened.
	//
	// Without a counter, "the pruner works" is untestable: an inexact pushdown
	// leaves the Filter in place, so a pruner that skips nothing returns exactly
	// the same rows as one that skips correctly. The only difference is how much
	// was read, and this is the only thing that can see it.
	statsMu sync.Mutex
	read    int
	skipped int
}

// RowGroupStats reports how many row groups were read and how many the pruner
// skipped, across every reader opened from this source.
func (s *Source) RowGroupStats() (read, skipped int) {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	return s.read, s.skipped
}

func (s *Source) countGroup(skipped bool) {
	s.statsMu.Lock()
	if skipped {
		s.skipped++
	} else {
		s.read++
	}
	s.statsMu.Unlock()
}

// New builds a source over a list of openers, read in order.
func New(opens []Opener, desc string, o Options) *Source {
	return &Source{opens: opens, desc: desc, opts: o}
}

// --- plan.Source ---------------------------------------------------------------

func (s *Source) Name() string     { return "parquet" }
func (s *Source) Describe() string { return s.desc }

func (s *Source) Caps() plan.Caps { return plan.Caps{Projection: true} }

// ClassifyPredicates reports every conjunct the pruner understands as INEXACT,
// and everything else as Unsupported.
//
// Inexact is not a hedge, it is the truth. Row-group statistics prove that a group
// CANNOT match; they never prove that a row does. A group that survives pruning
// still contains rows the predicate rejects, so the Filter above the scan is what
// actually produces the right answer and this only makes it read less.
//
// Claiming Exact here would delete that Filter and return every row of every
// surviving row group — silently, and only on files big enough to have more than
// one row group, which no fixture has.
func (s *Source) ClassifyPredicates(preds []expr.Node) []plan.Pushdown {
	out := make([]plan.Pushdown, len(preds))
	if !s.opts.Prune {
		return out // all Unsupported
	}
	// Background, not a caller's context: this runs inside the optimizer, which has
	// no context to pass down. It is not a hole, because Resolve already called
	// Schema with the query's context before any rule ran, so this hits the cache.
	// If it somehow did not, failing closed to Unsupported is the right answer.
	full, err := s.Schema(context.Background())
	if err != nil {
		return out
	}
	for i, p := range preds {
		if prunable(p, full) {
			out[i] = plan.Inexact
		}
	}
	return out
}

// Schema reads the footer of the first file once and caches it.
func (s *Source) Schema(ctx context.Context) (*dtype.Schema, error) {
	s.once.Do(func() {
		if err := ctx.Err(); err != nil {
			s.err = err
			return
		}
		r, closer, err := s.openFile(0)
		if err != nil {
			s.err = err
			return
		}
		defer closer()
		s.schema, s.leaves, s.unread, s.err = fileSchema(r.MetaData().Schema)
		if s.err != nil {
			s.err = uerr.Annotate(s.err, "scan_parquet", s.desc)
		}
	})
	return s.schema, s.err
}

func (s *Source) openFile(i int) (*file.Reader, func(), error) {
	ra, closer, err := s.opens[i]()
	if err != nil {
		return nil, nil, uerr.Wrap(err, uerr.KindIO, "scan_parquet",
			"opening part %d of %s", i+1, s.desc)
	}
	r, err := file.NewParquetReader(ra)
	if err != nil {
		if closer != nil {
			closer.Close()
		}
		return nil, nil, uerr.Wrap(err, uerr.KindIO, "scan_parquet",
			"reading the footer of %s", s.desc)
	}
	return r, func() {
		r.Close()
		if closer != nil {
			closer.Close()
		}
	}, nil
}

// fileSchema maps a Parquet schema to an ursus one, per TOP-LEVEL FIELD.
//
// It also returns, per field, the leaf column index to read it from, and for the
// fields that cannot be read, the error saying why.
//
// # Why it iterates fields and not columns
//
// It used to iterate sc.NumColumns(), which counts LEAVES. For a flat file the
// two coincide, which is why that worked; for a file with one struct in it they
// do not, and every later use of the index — Open's `cols`, the reader's column
// order — would be reading the wrong chunk.
//
// # Refusing the COLUMN rather than the file
//
// This used to refuse the whole file if any column was unsupported, and the
// reason given was sound: "dropping an unreadable column would give a frame that
// silently lacks data the user asked for, and the most likely reaction is not to
// notice." That guarantee is kept and the refusal is narrowed. A nested column
// stays VISIBLE in the schema with the type it actually has, and asking to read
// it fails in Open with the message it always produced. Nothing is dropped
// silently; a file with twenty flat columns and one struct is simply no longer
// unopenable, which it was.
func fileSchema(sc *schema.Schema) (*dtype.Schema, [][]int, map[int]error, error) {
	root := sc.Root()
	n := root.NumFields()

	fields := make([]dtype.Field, n)
	leaves := make([][]int, n)
	var unread map[int]error

	for i := range n {
		f := root.Field(i)
		dt, leaf, err := fieldType(sc, f)
		leaves[i] = leaf
		if err != nil {
			if unread == nil {
				unread = make(map[int]error)
			}
			unread[i] = err
		}
		fields[i] = dtype.Field{
			Name:     f.Name(),
			Type:     dt,
			Nullable: f.RepetitionType() != parquet.Repetitions.Required,
		}
	}

	out, err := dtype.NewSchema(fields...)
	return out, leaves, unread, err
}

// --- source.Openable -----------------------------------------------------------

func (s *Source) Open(ctx context.Context, spec source.ScanSpec) (source.BatchSource, error) {
	full, err := s.Schema(ctx)
	if err != nil {
		return nil, err
	}

	out := full
	if spec.Projection != nil {
		if out, err = full.Select(spec.Projection); err != nil {
			return nil, uerr.Annotate(err, "scan_parquet", s.desc)
		}
	}

	// Which file columns to open. A column outside the projection is never opened,
	// so its pages are never read or decompressed — the entire point of pushing a
	// projection into a columnar reader.
	//
	// This is also where an unreadable column is refused. fileSchema names nested
	// columns rather than rejecting the file, so the refusal has to happen at the
	// point of READING one — and only for a column this query actually wants. A
	// nil Projection means every column, so a select-all over a file with a nested
	// column still fails, which is the behaviour that keeps "nothing is silently
	// missing" true.
	var cols []colPlan
	for i := range full.Len() {
		if out.IndexOf(full.Field(i).Name) < 0 {
			continue
		}
		if err := s.unread[i]; err != nil {
			return nil, uerr.Annotate(err, "scan_parquet", s.desc)
		}
		cols = append(cols, colPlan{field: i, leaves: s.leaves[i]})
	}

	batchSize := spec.BatchSize
	if batchSize <= 0 {
		batchSize = 8192
	}

	r := &reader{
		src:       s,
		full:      full,
		out:       out,
		cols:      cols,
		batchSize: batchSize,
		remaining: spec.MaxRows,
		preds:     spec.Predicate,
		rgIndex:   -1,
	}
	if err := r.openNext(); err != nil && err != io.EOF {
		return nil, err
	}
	return r, nil
}

// --- source.BatchSource --------------------------------------------------------

type reader struct {
	src       *Source
	full      *dtype.Schema
	out       *dtype.Schema
	cols      []colPlan // what to read, in output order
	batchSize int
	remaining int // rows left to produce; 0 means unlimited
	preds     []expr.Node

	mu      sync.Mutex
	fileIdx int
	pf      *file.Reader
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
	return r.closeLocked()
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
func (r *reader) openNext() error {
	for {
		if r.pf == nil {
			if r.fileIdx >= len(r.src.opens) {
				return io.EOF
			}
			pf, closeFn, err := r.src.openFile(r.fileIdx)
			if err != nil {
				return err
			}
			r.fileIdx++
			r.pf, r.closeFn = pf, closeFn
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

// colPlan is one OUTPUT column and the file leaves it is built from.
//
// The two numbers used to be one. Every column readable before this step consumed
// exactly one leaf — a flat column obviously, a List because its single element leaf
// carries the whole thing — so the schema's field index and the file's leaf index
// happened to agree, and openNext indexed the schema with a leaf number. A struct
// with two fields ends the agreement for every column after it, and the symptom
// would have been reading the wrong column under the right name rather than an error.
type colPlan struct {
	field  int   // index into the file schema
	leaves []int // file column indexes, in field order
}

// openColumn opens the chunk readers for one output column.
//
// A struct needs one per field plus the group's own definition level, which is what
// separates "this struct is absent" from "this struct is here and its fields are
// null". Every other shape is a single leaf.
func (r *reader) openColumn(rg *file.RowGroupReader,
	rgMeta *metadata.RowGroupMetaData, p colPlan) (colReader, error) {

	f := r.full.Field(p.field)

	// dt is passed in rather than read off f, because a struct's fields each have
	// their own type and the leaf reader is what decodes them.
	leafReader := func(ci int, dt dtype.DataType) (colReader, error) {
		if err := checkEncodings(rgMeta, ci, f.Name); err != nil {
			return nil, err
		}
		cr, err := rg.Column(ci)
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindIO, "scan_parquet",
				"opening column %q", f.Name)
		}
		return newColReader(cr, r.pf.MetaData().Schema.Column(ci), dt)
	}

	if f.Type.ID() != dtype.TypeStruct {
		return leafReader(p.leaves[0], f.Type)
	}
	return newStructReader(f, p.leaves, leafReader)
}

// shouldSkip asks the pruner whether this row group can be ruled out.
func (r *reader) shouldSkip(rg *metadata.RowGroupMetaData) (bool, error) {
	if !r.src.opts.Prune || len(r.preds) == 0 {
		return false, nil
	}
	return canSkipRowGroup(rg, r.full, r.preds)
}

func (r *reader) Next(ctx context.Context) (*data.Batch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.done {
		return nil, io.EOF
	}

	want := r.batchSize
	if r.remaining > 0 && r.remaining < want {
		want = r.remaining
	}

	for {
		if r.chunks == nil || r.rgLeft <= 0 {
			switch err := r.openNext(); {
			case err == io.EOF:
				r.done = true
				return nil, io.EOF
			case err != nil:
				return nil, err
			}
			continue
		}

		n := min(want, r.rgLeft)
		rows := -1
		for _, c := range r.chunks {
			got, err := c.read(n)
			if err != nil {
				return nil, err
			}
			// Every column of a row group has the same number of rows. A disagreement
			// means the file's column chunks are inconsistent, and continuing would
			// build a ragged batch.
			if rows == -1 {
				rows = got
			} else if got != rows {
				return nil, uerr.New(uerr.KindValue, "scan_parquet",
					"row group %d: columns disagree on row count (%d vs %d)",
					r.rgIndex, rows, got)
			}
		}
		r.rgLeft -= rows
		if rows == 0 {
			continue // this row group is spent; roll on
		}

		if r.remaining > 0 {
			r.remaining -= rows
			if r.remaining <= 0 {
				r.done = true
			}
		}

		cols := make([]*data.Column, len(r.chunks))
		for i, c := range r.chunks {
			cols[i] = c.finish(r.out.Field(i).Name)
		}
		return data.NewBatch(r.out, cols)
	}
}

// --- encodings ------------------------------------------------------------------

// allowedEncodings is the set arrow-go can actually decode.
//
// # This list prevents a PANIC, not an error
//
// arrow-go's typed_encoder.go ends in `panic("unimplemented encoding ...")` at
// eleven sites, and the panic is reachable from HasNext() inside ReadBatch. A
// BYTE_ARRAY column encoded with BYTE_STREAM_SPLIT is legal Parquet 2.11 and has
// no decoder in arrow-go, so a file someone else wrote can take down the process
// of anyone who reads it.
//
// Checking the column chunk's declared encodings before touching a page turns that
// into an error naming the column and the encoding. A panic in a library is never
// the right answer to a malformed or merely newer input file.
var allowedEncodings = map[parquet.Encoding]bool{
	parquet.Encodings.Plain:                true,
	parquet.Encodings.PlainDict:            true,
	parquet.Encodings.RLE:                  true,
	parquet.Encodings.RLEDict:              true,
	parquet.Encodings.DeltaBinaryPacked:    true,
	parquet.Encodings.DeltaByteArray:       true,
	parquet.Encodings.DeltaLengthByteArray: true,
	parquet.Encodings.ByteStreamSplit:      true, // supported for FLOAT/DOUBLE only
}

func checkEncodings(rg *metadata.RowGroupMetaData, col int, name string) error {
	cc, err := rg.ColumnChunk(col)
	if err != nil {
		return uerr.Wrap(err, uerr.KindIO, "scan_parquet",
			"reading metadata for column %q", name)
	}
	for _, e := range cc.Encodings() {
		if !allowedEncodings[e] {
			return uerr.New(uerr.KindUnsupported, "scan_parquet",
				"column %q uses the %s encoding, which cannot be decoded", name, e).
				Hint("this is checked up front because the decoder panics rather " +
					"than returning an error")
		}
		// BYTE_STREAM_SPLIT is defined for every physical type since Parquet 2.11,
		// but arrow-go only implements FLOAT and DOUBLE. The others reach the panic.
		if e == parquet.Encodings.ByteStreamSplit {
			switch cc.Type() {
			case parquet.Types.Float, parquet.Types.Double:
			default:
				return uerr.New(uerr.KindUnsupported, "scan_parquet",
					"column %q uses BYTE_STREAM_SPLIT on a %s column, which cannot "+
						"be decoded", name, cc.Type()).
					Hint("only FLOAT and DOUBLE are implemented for this encoding")
			}
		}
	}
	return nil
}

// --- column reader construction --------------------------------------------------

func newColReader(cr file.ColumnChunkReader, desc *schema.Column, dt dtype.DataType) (colReader, error) {
	maxDef := desc.MaxDefinitionLevel()
	name := desc.Name()

	// A List is read by listCol, which owns the level machine; the element type
	// decides which instantiation. desc is the ELEMENT's leaf descriptor, so its
	// levels are the ones the machine needs.
	if dt.ID() == dtype.TypeList {
		return newListReader(cr, desc, dt)
	}

	switch t := cr.(type) {
	case *file.BooleanColumnChunkReader:
		return &boolCol{cr: t, maxDef: maxDef,
			out: bitmap.NewBuilder(0), valid: bitmap.NewBuilder(0)}, nil

	case *file.ByteArrayColumnChunkReader:
		if dt.ID() == dtype.TypeDecimal {
			return nil, uerr.New(uerr.KindUnsupported, "scan_parquet",
				"column %q stores a DECIMAL in a variable-length BYTE_ARRAY", name).
				Hint("ursus reads DECIMAL from INT32, INT64 and FIXED_LEN_BYTE_ARRAY")
		}
		return &byteArrayCol{cr: t, maxDef: maxDef, dt: dt, valid: bitmap.NewBuilder(0)}, nil

	case *file.Int32ColumnChunkReader:
		if dt.ID() == dtype.TypeDecimal {
			return newFixed[int32, i128.Int128](t, maxDef, dt,
				func(v int32) i128.Int128 { return i128.FromInt64(int64(v)) }), nil
		}
		return int32Reader(t, maxDef, dt, name)

	case *file.Int64ColumnChunkReader:
		if dt.ID() == dtype.TypeDecimal {
			return newFixed[int64, i128.Int128](t, maxDef, dt, i128.FromInt64), nil
		}
		switch dt.ID() {
		case dtype.TypeInt64, dtype.TypeDatetime, dtype.TypeDuration:
			// A Datetime is an int64 tick count; the logical type in the file is what
			// says at which resolution. Nothing converts.
			return newFixed[int64, int64](t, maxDef, dt, identity[int64]), nil
		case dtype.TypeTime:
			// TIME(MICROS) and TIME(NANOS) are INT64; TIME(MILLIS) is INT32 and lands
			// in int32Reader instead.
			return newFixed[int64, int64](t, maxDef, dt, identity[int64]), nil
		case dtype.TypeUint64:
			return newFixed[int64, uint64](t, maxDef, dt,
				func(v int64) uint64 { return uint64(v) }), nil
		}

	case *file.Float32ColumnChunkReader:
		return newFixed[float32, float32](t, maxDef, dt, identity[float32]), nil

	case *file.Float64ColumnChunkReader:
		return newFixed[float64, float64](t, maxDef, dt, identity[float64]), nil

	case *file.FixedLenByteArrayColumnChunkReader:
		if dt.ID() != dtype.TypeDecimal {
			return nil, uerr.New(uerr.KindUnsupported, "scan_parquet",
				"column %q is a FIXED_LEN_BYTE_ARRAY that is not a DECIMAL", name)
		}
		if n := desc.TypeLength(); n > 16 {
			return nil, uerr.New(uerr.KindUnsupported, "scan_parquet",
				"column %q is a %d-byte DECIMAL, which does not fit in 128 bits", name, n)
		}
		// The length check above is what makes the conversion infallible, so the
		// hot loop needs no error branch.
		return newFixed[parquet.FixedLenByteArray, i128.Int128](t, maxDef, dt,
			func(v parquet.FixedLenByteArray) i128.Int128 {
				d, _ := decimalFromBytes(v)
				return d
			}), nil
	}

	return nil, uerr.New(uerr.KindUnsupported, "scan_parquet",
		"column %q (%s) has no reader", name, dt)
}

// int32Reader narrows a physical INT32 to whichever width the logical type says.
// Parquet stores Int8, Int16 and Int32 identically; only the annotation separates
// them, and reading all three as Int32 would change the schema the plan resolved.
func int32Reader(t *file.Int32ColumnChunkReader, maxDef int16, dt dtype.DataType, name string) (colReader, error) {
	switch dt.ID() {
	case dtype.TypeInt8:
		return newFixed[int32, int8](t, maxDef, dt, func(v int32) int8 { return int8(v) }), nil
	case dtype.TypeInt16:
		return newFixed[int32, int16](t, maxDef, dt, func(v int32) int16 { return int16(v) }), nil
	case dtype.TypeInt32, dtype.TypeDate:
		return newFixed[int32, int32](t, maxDef, dt, identity[int32]), nil
	case dtype.TypeTime:
		// TIME(MILLIS) is the one temporal type Parquet puts on INT32, and ursus
		// stores every Time as int64 ticks — so this widens where Date, whose ursus
		// storage is also 32 bits, does not.
		return newFixed[int32, int64](t, maxDef, dt,
			func(v int32) int64 { return int64(v) }), nil
	case dtype.TypeUint8:
		return newFixed[int32, uint8](t, maxDef, dt, func(v int32) uint8 { return uint8(v) }), nil
	case dtype.TypeUint16:
		return newFixed[int32, uint16](t, maxDef, dt, func(v int32) uint16 { return uint16(v) }), nil
	case dtype.TypeUint32:
		return newFixed[int32, uint32](t, maxDef, dt, func(v int32) uint32 { return uint32(v) }), nil
	default:
		return nil, uerr.New(uerr.KindUnsupported, "scan_parquet",
			"column %q is INT32 but mapped to %s", name, dt)
	}
}

func newFixed[P any, T data.Fixed](cr batchReader[P], maxDef int16, dt dtype.DataType, conv func(P) T) colReader {
	return &fixedCol[P, T]{cr: cr, maxDef: maxDef, dt: dt, conv: conv, valid: bitmap.NewBuilder(0)}
}

func identity[T any](v T) T { return v }

// newListReader builds the reader for a List column.
//
// The level machine is listCol and is element-agnostic; all that varies is the
// accumulator underneath it, so this picks one and hands it over.
func newListReader(cr file.ColumnChunkReader, desc *schema.Column, dt dtype.DataType) (colReader, error) {
	elems, err := newListElems(cr, desc, dt.Inner())
	if err != nil {
		return nil, err
	}
	return &listCol{
		elems:  elems,
		maxDef: desc.MaxDefinitionLevel(),
		maxRep: desc.MaxRepetitionLevel(),
		dt:     dt,
		valid:  bitmap.NewBuilder(0),
	}, nil
}

// newListElems picks the accumulator for a List's element type.
//
// The refusals name what is missing rather than saying "not a list ursus can
// read": each remaining case is one more accumulator, and saying so is the
// difference between a gap and a wall.
func newListElems(cr file.ColumnChunkReader, desc *schema.Column,
	elem dtype.DataType) (listElems, error) {

	fixed := func() *bitmap.Builder { return bitmap.NewBuilder(0) }

	switch t := cr.(type) {
	case *file.ByteArrayColumnChunkReader:
		// String and Binary share the physical reader and differ only in the type
		// the child column is finished with.
		switch elem.ID() {
		case dtype.TypeString, dtype.TypeBinary:
			return &byteArrayElems{cr: t, valid: fixed()}, nil
		}

	case *file.Int32ColumnChunkReader:
		switch elem.ID() {
		case dtype.TypeInt8, dtype.TypeInt16, dtype.TypeInt32, dtype.TypeDate:
			return &fixedElems[int32, int32]{cr: t, conv: identity[int32], valid: fixed()}, nil
		}

	case *file.Int64ColumnChunkReader:
		switch elem.ID() {
		case dtype.TypeInt64, dtype.TypeDatetime, dtype.TypeDuration, dtype.TypeTime:
			return &fixedElems[int64, int64]{cr: t, conv: identity[int64], valid: fixed()}, nil
		}

	case *file.Float32ColumnChunkReader:
		if elem.ID() == dtype.TypeFloat32 {
			return &fixedElems[float32, float32]{cr: t, conv: identity[float32], valid: fixed()}, nil
		}

	case *file.Float64ColumnChunkReader:
		if elem.ID() == dtype.TypeFloat64 {
			return &fixedElems[float64, float64]{cr: t, conv: identity[float64], valid: fixed()}, nil
		}
	}
	return nil, uerr.New(uerr.KindUnsupported, "scan_parquet",
		"column %q is a list of %s, which cannot be read yet", desc.Name(), elem).
		Hint("lists of the integer, float, temporal, String and Binary types are " +
			"read; Bool, Decimal and nested elements are not")
}

package parquet

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/apache/arrow-go/v18/parquet"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/source"
	"github.com/advenn/ursus/internal/source/hive"
	"github.com/advenn/ursus/internal/uerr"
)

// Options configures a Parquet source.
type Options struct {
	// Prune enables the reader's use of pushed predicates: row-group skipping from
	// column statistics, and string conjuncts judged on each dictionary (step 193).
	// On by default; the switch exists so a wrong answer can be bisected to them,
	// the parts of this package that can change which rows come back.
	Prune bool

	// Hive is a scan's choices about its files' paths, which the scan makes
	// Partitioned from (step 347).
	Hive HiveOptions

	// Partitioned adds a hive dataset's partition columns and a column of each
	// file's path to the files' own (step 347); nil for neither.
	Partitioned *Partitioned

	// Evolve is how files whose columns differ are read as one (step 348).
	Evolve Evolution
}

// Evolution is how files whose columns differ are read as one, as Polars'
// scan_parquet reads them by its schema, missing_columns, extra_columns and
// cast_options: as Schema, or as the first file's columns where it is nil; a column a
// file lacks a null in its rows, where InsertMissing, and refused otherwise; a column
// a file has beyond them passed over, where IgnoreExtra, and refused otherwise; and an
// integer or a float of a narrower type than the schema's widened to it, where
// UpcastInts or UpcastFloats, and refused otherwise. With none set, every file has the
// same columns of the same types, as it had before step 348.
type Evolution struct {
	Schema        *dtype.Schema
	InsertMissing bool
	IgnoreExtra   bool
	UpcastInts    bool
	UpcastFloats  bool
}

func (e Evolution) set() bool {
	return e.Schema != nil || e.InsertMissing || e.IgnoreExtra || e.UpcastInts || e.UpcastFloats
}

// upcasts reports whether a column of from is read as to: an integer to one that holds
// every value of it, a Float32 to a Float64.
func (e Evolution) upcasts(from, to dtype.DataType) bool {
	switch {
	case e.UpcastInts && from.IsInteger() && to.IsInteger():
		switch {
		case from.IsSignedInteger() && to.IsUnsignedInteger():
			return false
		case from.IsUnsignedInteger() && to.IsSignedInteger():
			return to.BitWidth() > from.BitWidth()
		}
		return to.BitWidth() > from.BitWidth()
	case e.UpcastFloats:
		return from == dtype.Float32 && to == dtype.Float64
	}
	return false
}

// HiveOptions are a scan's choices about its files' paths: whether their key=value
// directories are partition columns, nil for the default, on for a directory only;
// given types of some keys; whether a key of dates or instants is text; and the name
// of a column of each file's path, "" for none.
type HiveOptions struct {
	Partitioning *bool
	Schema       *dtype.Schema
	NoDates      bool
	PathColumn   string
}

// Partitioned is what a scan adds to its files' columns: a hive dataset's partition
// keys, each file's values of them read from its path, and a column naming each file,
// Polars' include_file_paths.
type Partitioned struct {
	Parts *hive.Partitions          // nil for no partition column
	Types map[string]dtype.DataType // given types of some keys, Polars' hive_schema
	Dates bool                      // a key of dates or instants is one, Polars' try_parse_hive_dates

	PathColumn string   // the column of each file's path, "" for none
	Paths      []string // each file's path, for it
}

// DefaultOptions enables pruning.
func DefaultOptions() Options { return Options{Prune: true} }

// Opener produces a fresh handle on the same file.
//
// ctx is the query's: the one Schema and Open were given. A file that lives behind
// a network can be cancelled through it, and its handle may keep it for its reads.
type Opener func(ctx context.Context) (parquet.ReaderAtSeeker, io.Closer, error)

// Source is one or more Parquet files read as a single table.
//
// # Several files are read BY NAME
//
// Every file must have the same columns, of the same types, in any order. The
// schema is every file's footer reconciled at plan time — not the first file's,
// which is what it used to be, and every later file was then read by the first
// file's POSITIONS (audit.md §5, I1). A file with its columns in another order had
// them swapped, silently; one with other names was read as if it had the first
// file's; one with fewer columns panicked inside arrow-go.
//
// A column is nullable if it is nullable in ANY file. At read time each file's
// layout is derived again from the footer actually being read (layoutFor), so a
// file rewritten between planning and reading is read by name or refused, never
// read by a layout it no longer has.
type Source struct {
	opens []Opener
	names []string // per file, for error messages; see partName
	desc  string
	opts  Options

	once   source.Once
	schema *dtype.Schema

	// unread holds, per unreadable field of the reconciled schema, the refusal to
	// return if a query asks for it. Non-nil entries are the nested columns.
	unread map[int]error

	// files is the files' own columns, reconciled, where schema adds partition
	// columns and a path column to them (step 347). consts holds, by file and then
	// by field of schema, the one-row column of a field every row of the file
	// shares, a partition value or the file's path; nil for a field the file holds.
	files  *dtype.Schema
	consts [][]*data.Column
	fixed  []bool // by field of schema: a field consts give

	// Row groups read and skipped, across every reader this source has opened.
	//
	// Without a counter, "the pruner works" is untestable: an inexact pushdown
	// leaves the Filter in place, so a pruner that skips nothing returns exactly
	// the same rows as one that skips correctly. The only difference is how much
	// was read, and this is the only thing that can see it.
	statsMu sync.Mutex
	read    int
	skipped int

	// Files read and skipped by their partition values (step 347).
	filesRead, filesSkipped int

	// rows is every file's footer row count, summed when Schema reads them; -1
	// until it has.
	rows atomic.Int64
}

// RowGroupStats reports how many row groups were read and how many the pruner
// skipped, across every reader opened from this source.
func (s *Source) RowGroupStats() (read, skipped int) {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	return s.read, s.skipped
}

// FileStats reports how many files were read and how many their partition values
// ruled out, across every reader opened from this source.
func (s *Source) FileStats() (read, skipped int) {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	return s.filesRead, s.filesSkipped
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
	return NewNamed(opens, nil, desc, o)
}

// NewNamed is New with a name for each file, which the errors use.
func NewNamed(opens []Opener, names []string, desc string, o Options) *Source {
	s := &Source{opens: opens, names: names, desc: desc, opts: o}
	s.rows.Store(-1)
	return s
}

// EstimatedRows is the sum of the files' footer row counts, which Schema reads at
// plan time. Exact, unless a file changes between planning and reading.
func (s *Source) EstimatedRows() (int64, bool) {
	n := s.rows.Load()
	return n, n >= 0
}

// partName names file i for an error message.
func (s *Source) partName(i int) string {
	switch {
	case i < len(s.names):
		return s.names[i]
	case len(s.opens) == 1:
		return s.desc
	}
	return fmt.Sprintf("part %d of %s", i+1, s.desc)
}

// The plan.Source methods, with the files' schemas and layouts, are files.go; the
// source.BatchSource readers are reader.go; the column readers' construction, and
// the encodings it accepts, colreader.go.

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
	//
	// The plans are built in OUTPUT order, which is the order Next names the
	// chunks in. They were built in the file's order, and matched only because
	// projection pushdown happens to emit projections in source order.
	var cols []colPlan
	for i := range out.Len() {
		fi := full.IndexOf(out.Field(i).Name)
		if err := s.unread[fi]; err != nil {
			return nil, uerr.Annotate(err, "scan_parquet", s.desc)
		}
		cols = append(cols, colPlan{field: fi})
	}

	batchSize := spec.BatchSize
	if batchSize <= 0 {
		batchSize = 8192
	}
	if out.Len() == 0 {
		// The readers below decode columns; one of none and a predicate is not
		// asked of them, which planScan answers with a column (RowsOnly).
		if !s.RowsOnly(spec) {
			return nil, uerr.Internalf("scan_parquet: a scan of no columns with a predicate")
		}
		return &rowsReader{src: s, out: out, size: batchSize, left: spec.MaxRows}, nil
	}
	// Predicates arrive only as ClassifyPredicates claimed them, which Prune gates.
	df := newDictFilter(spec.Predicate, out, spec.Eval, spec.MaxRows)

	// Row groups are decoded in parallel unless there is one thread, or a limit:
	// reading ahead under MaxRows decodes row groups the query will never use. See
	// parallelReader.
	if spec.Threads > 1 && spec.MaxRows <= 0 {
		pr := &parallelReader{
			src:       s,
			full:      full,
			out:       out,
			cols:      cols,
			batchSize: batchSize,
			preds:     spec.Predicate,
			eval:      spec.Eval,
			df:        df,
			threads:   spec.Threads,
			stop:      make(chan struct{}),
			gate:      newAheadGate(spec.ReadAhead),
			pages:     newScanPages(spec.Pages),
		}
		// As the serial reader does: the first file is opened now, so one that
		// cannot be read is refused while the query is planned. A panic in its
		// footer is the file's.
		err := func() (err error) {
			defer func() {
				if v := recover(); v != nil {
					err = corrupt(v, s.partName(0))
				}
			}()
			return pr.openFirst(ctx)
		}()
		if err != nil {
			pr.pages.close()
			return nil, err
		}
		return pr, nil
	}

	r := &reader{
		src:       s,
		full:      full,
		out:       out,
		cols:      cols,
		batchSize: batchSize,
		remaining: spec.MaxRows,
		preds:     spec.Predicate,
		eval:      spec.Eval,
		df:        df,
		wide:      batchSize,
		rgIndex:   -1,
		pages:     newScanPages(spec.Pages),
	}
	// The first row group is opened here, outside Next, so it is recovered here as
	// Next recovers. On any failure the reader is not returned, so whatever it
	// opened is closed now or never.
	err = func() (err error) {
		defer func() {
			if v := recover(); v != nil {
				err = corrupt(v, r.name)
			}
		}()
		return r.openNext(ctx)
	}()
	if err != nil && err != io.EOF {
		r.closeLocked()
		r.pages.close()
		return nil, err
	}
	return r, nil
}

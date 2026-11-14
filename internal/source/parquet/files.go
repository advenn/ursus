package parquet

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/metadata"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/source"
	"github.com/advenn/ursus/internal/uerr"
)

// --- plan.Source ---------------------------------------------------------------

func (s *Source) Name() string     { return "parquet" }
func (s *Source) Describe() string { return s.desc }

func (s *Source) Caps() plan.Caps { return plan.Caps{Projection: true} }

// RowsOnly: a scan of no columns and no predicate is its files' footers' rows,
// reading no column chunk (step 370). A predicate's columns are read as any scan's.
func (s *Source) RowsOnly(spec source.ScanSpec) bool { return len(spec.Predicate) == 0 }

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
		if prunable(p, full) || s.onFixed(p, full) {
			// A conjunct of partition columns, or of the path, rules out whole
			// files, judged on their values before any is opened (keepFile).
			out[i] = plan.Inexact
		} else if anyDictable(p, full) {
			// Evaluated on a dictionary in the reader, which drops only rows that
			// fail it (step 193): the Filter above stays.
			out[i] = plan.Inexact
		}
	}
	return out
}

// Schema reads every file's footer once, reconciles them, and caches the result.
//
// Every footer, not the first: a mismatch is refused here, before any row is
// produced, rather than in the middle of a read after earlier files' rows have
// gone downstream — and nullability is only right if every file is seen. One open
// and one footer parse per file, at plan time; each is opened again to be read.
//
// A read its caller's cancellation cut short is not kept, and the next caller reads
// again (source.Once).
func (s *Source) Schema(ctx context.Context) (*dtype.Schema, error) {
	err := s.once.Do(ctx, func(ctx context.Context) (err error) {
		defer func() {
			if v := recover(); v != nil {
				s.schema, s.unread, err = nil, nil, corrupt(v, s.desc)
			}
		}()
		s.schema, s.unread, err = s.readSchemas(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.schema, nil
}

// corrupt turns a panic recovered while reading a file into an error.
//
// arrow-go's decoders panic on input they cannot parse — an index past a buffer, a
// length that is not there — and a file that makes them do that is corrupt, which
// is the file's problem: KindIO. The frame that decides is the innermost one in
// arrow-go or in ursus. If it is ursus's own, the bug is ursus's, and the result is
// ErrInternal as anywhere else; so is ursus's own assertion, an internal *Error.
func corrupt(v any, name string) *uerr.Error {
	e := uerr.Attributed(v, uerr.KindIO, "scan_parquet",
		"%s is corrupt, or uses something the decoder cannot read", name)
	var p *uerr.PanicError
	if !errors.As(e, &p) {
		return e
	}
	for _, fn := range p.Callers {
		switch {
		case strings.HasPrefix(fn, "github.com/apache/arrow-go/"):
			return e
		case strings.HasPrefix(fn, "github.com/advenn/ursus/"):
			return uerr.FromPanic(v, "scan_parquet")
		}
	}
	return e
}

func (s *Source) readSchemas(ctx context.Context) (*dtype.Schema, map[int]error, error) {
	var (
		first  *dtype.Schema
		fields []dtype.Field
		unread map[int]error
		rows   int64
	)
	for k := range s.opens {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		r, closer, err := s.openFile(ctx, k, pages)
		if err != nil {
			return nil, nil, err
		}
		rows += r.NumRows()
		fs, _, un, err := fileSchema(r.MetaData())
		closer()
		if err != nil {
			return nil, nil, uerr.Annotate(err, "scan_parquet", s.partName(k))
		}
		if k == 0 && s.opts.Evolve.Schema == nil {
			first, fields = fs, fs.FieldSlice()
			unread = un
			continue
		}
		wantName := s.partName(0)
		if k == 0 {
			// The given schema is the files' columns, each nullable where a file
			// lacks it or holds it so, as any is below (step 348).
			first, fields, wantName = s.opts.Evolve.Schema, s.opts.Evolve.Schema.FieldSlice(), "the given schema"
		}
		if err := s.fits(first, fs, wantName, s.partName(k)); err != nil {
			return nil, nil, err
		}
		for i := range fields {
			j := fs.IndexOf(fields[i].Name)
			if j < 0 {
				fields[i].Nullable = true // the file lacks it: nulls in its rows
				continue
			}
			fields[i].Nullable = fields[i].Nullable || fs.Field(j).Nullable
			if e := un[j]; e != nil && unread[i] == nil {
				if unread == nil {
					unread = make(map[int]error)
				}
				unread[i] = uerr.Annotate(e, "scan_parquet", s.partName(k))
			}
		}
	}
	files, err := dtype.NewSchema(fields...)
	if err != nil {
		return nil, nil, err
	}
	s.files = files
	out, err := s.partitioned(files)
	if err != nil {
		return nil, nil, err
	}
	for i := range s.fixed {
		delete(unread, i) // a partition value read where the file's column is not
	}
	s.rows.Store(rows)
	return out, unread, nil
}

// partitioned is files' schema with the partition columns and the path column, and
// sets each file's values of them (step 347). A partition key that names a file
// column keeps the column's place and its type, and its values replace the file's,
// as Polars reads them; the others follow the files' columns in path order, and the
// path column comes last.
func (s *Source) partitioned(files *dtype.Schema) (*dtype.Schema, error) {
	pt := s.opts.Partitioned
	if pt == nil {
		return files, nil
	}
	fields := files.FieldSlice()
	s.consts = make([][]*data.Column, len(s.opens))
	place := func(f dtype.Field, vals func(k int) *data.Column) {
		i := slices.IndexFunc(fields, func(g dtype.Field) bool { return g.Name == f.Name })
		if i < 0 {
			i = len(fields)
			fields = append(fields, f)
		} else {
			fields[i].Nullable = f.Nullable // the file's values of it are not read
		}
		for k := range s.consts {
			s.consts[k] = append(s.consts[k], make([]*data.Column, len(fields)-len(s.consts[k]))...)
			s.consts[k][i] = vals(k)
		}
	}
	if pt.Parts != nil {
		types := pt.Parts.Types(pt.Types, files, pt.Dates)
		vals, err := pt.Parts.Values(types)
		if err != nil {
			return nil, uerr.Annotate(err, "scan_parquet", s.desc)
		}
		for j, key := range pt.Parts.Keys {
			null := false
			for k := range pt.Parts.Null {
				null = null || pt.Parts.Null[k][j]
			}
			place(dtype.Field{Name: key, Type: types[j], Nullable: null}, func(k int) *data.Column { return vals[k][j] })
		}
	}
	if pt.PathColumn != "" {
		if files.IndexOf(pt.PathColumn) >= 0 || pt.Parts != nil && slices.Contains(pt.Parts.Keys, pt.PathColumn) {
			// Polars: "column name for file paths "a" conflicts with column name from file".
			return nil, uerr.New(uerr.KindSchema, "scan_parquet",
				"the file paths' column %q is a column of the files already", pt.PathColumn)
		}
		place(dtype.Field{Name: pt.PathColumn, Type: dtype.String}, func(k int) *data.Column {
			return data.NewString(pt.PathColumn, []string{pt.Paths[k]}, bitmap.AllSet(1))
		})
	}
	s.fixed = make([]bool, len(fields))
	for k := range s.consts {
		s.consts[k] = append(s.consts[k], make([]*data.Column, len(fields)-len(s.consts[k]))...)
		for i, c := range s.consts[k] {
			s.fixed[i] = s.fixed[i] || c != nil
		}
	}
	return dtype.NewSchema(fields...)
}

// onFixed reports whether p reads partition columns or the path column alone, which
// every row of a file shares.
func (s *Source) onFixed(p expr.Node, full *dtype.Schema) bool {
	names := expr.RootNames(p)
	for _, n := range names {
		if i := full.IndexOf(n); i < 0 || i >= len(s.fixed) || !s.fixed[i] {
			return false
		}
	}
	return len(names) > 0
}

// nextKept is the first file from k that keepFile keeps, len(opens) for none.
func (s *Source) nextKept(ctx context.Context, k int, full *dtype.Schema, preds []expr.Node,
	eval func(context.Context, expr.Node, *data.Batch) (*data.Column, error)) (int, error) {
	for ; k < len(s.opens); k++ {
		keep, err := s.keepFile(ctx, k, full, preds, eval)
		if err != nil || keep {
			return k, err
		}
	}
	return k, nil
}

// keepFile reports whether file k may hold a row every conjunct of preds that reads
// partition columns or the path alone keeps, judged on the file's values of them by
// eval: one that keeps no row of them rules the file out before it is opened.
func (s *Source) keepFile(ctx context.Context, k int, full *dtype.Schema, preds []expr.Node,
	eval func(context.Context, expr.Node, *data.Batch) (*data.Column, error)) (bool, error) {
	if s.consts == nil || eval == nil || !s.opts.Prune {
		return true, nil
	}
	keep := true
	for _, p := range conjuncts(preds) {
		if !s.onFixed(p, full) {
			continue
		}
		var fs []dtype.Field
		var cols []*data.Column
		for _, n := range expr.RootNames(p) {
			i := full.IndexOf(n)
			fs, cols = append(fs, full.Field(i)), append(cols, s.consts[k][i].Rename(n))
		}
		sch, err := dtype.NewSchema(fs...)
		if err != nil {
			return false, err
		}
		b, err := data.NewBatch(sch, cols)
		if err != nil {
			return false, err
		}
		got, err := eval(ctx, p, b)
		if err != nil {
			return false, err
		}
		if got.Len() != 1 || !got.IsValid(0) || !got.Bools().Get(0) {
			keep = false
			break
		}
	}
	s.statsMu.Lock()
	if keep {
		s.filesRead++
	} else {
		s.filesSkipped++
	}
	s.statsMu.Unlock()
	return keep, nil
}

// fits refuses got, a file's columns, unless it can be read as want's under the scan's
// Evolution: every column of want, or InsertMissing; no other, or IgnoreExtra; and
// each of want's type, or one it upcasts to it (step 348). With no Evolution set, it is
// sameColumns.
func (s *Source) fits(want, got *dtype.Schema, wantName, gotName string) error {
	ev := s.opts.Evolve
	if !ev.set() {
		return sameColumns(want, got, wantName, gotName)
	}
	for _, f := range want.FieldSlice() {
		j := got.IndexOf(f.Name)
		if j < 0 {
			if !ev.InsertMissing {
				return uerr.New(uerr.KindSchema, "scan_parquet", "column %q of %s is missing from %s", f.Name, wantName, gotName).
					Hint("WithInsertMissingColumns(true) reads it as nulls there, as Polars' missing_columns=\"insert\"")
			}
			continue
		}
		if g := got.Field(j).Type; !g.Equal(f.Type) && !ev.upcasts(g, f.Type) {
			// Polars: "data type mismatch for column a: incoming: Int32 != target:
			// Int64".
			e := uerr.New(uerr.KindSchema, "scan_parquet", "column %q is %s in %s, but %s in %s", f.Name, f.Type, wantName, g, gotName)
			switch {
			case (Evolution{UpcastInts: true}).upcasts(g, f.Type):
				e = e.Hint("WithUpcastIntegers(true) widens it, as Polars' integer_cast=\"upcast\"")
			case (Evolution{UpcastFloats: true}).upcasts(g, f.Type):
				e = e.Hint("WithUpcastFloats(true) widens it, as Polars' float_cast=\"upcast\"")
			}
			return e
		}
	}
	if !ev.IgnoreExtra {
		for _, g := range got.FieldSlice() {
			if want.IndexOf(g.Name) < 0 {
				// Polars: "extra column in file outside of expected schema: c".
				return uerr.New(uerr.KindSchema, "scan_parquet", "%s has a column %q that %s has not", gotName, g.Name, wantName).
					Hint("WithIgnoreExtraColumns(true) passes it over, as Polars' extra_columns=\"ignore\", or name it in WithParquetSchema")
			}
		}
	}
	return nil
}

// sameColumns refuses got unless it has exactly want's columns, by NAME, each of
// the same type. Order may differ. Nullability may differ too — it is reconciled,
// not compared — which is why this compares Field.Type and not Field.
func sameColumns(want, got *dtype.Schema, wantName, gotName string) error {
	var missing, extra []string
	for _, f := range want.FieldSlice() {
		if got.IndexOf(f.Name) < 0 {
			missing = append(missing, f.Name)
		}
	}
	for _, f := range got.FieldSlice() {
		if want.IndexOf(f.Name) < 0 {
			extra = append(extra, f.Name)
		}
	}
	remedy := func(e *uerr.Error) *uerr.Error {
		return e.Hint("several files are read as one by column NAME: every file must " +
			"have the same columns, of the same types, in any order").
			Hint("to combine files that differ, scan them separately and Concat them — " +
				"it promotes types, and ConcatDiagonal fills missing columns with nulls")
	}
	if len(missing) > 0 || len(extra) > 0 {
		e := uerr.New(uerr.KindSchema, "scan_parquet",
			"%s does not have the same columns as %s", gotName, wantName)
		if len(missing) > 0 {
			e = e.Hint("missing from %s: %s", gotName, quoteNames(missing))
		}
		if len(extra) > 0 {
			e = e.Hint("not in %s: %s", wantName, quoteNames(extra))
		}
		return remedy(e.Hint("%s: %s", wantName, want).Hint("%s: %s", gotName, got))
	}
	for _, f := range want.FieldSlice() {
		g := got.Field(got.IndexOf(f.Name))
		if !f.Type.Equal(g.Type) {
			return remedy(uerr.New(uerr.KindSchema, "scan_parquet",
				"column %q is %s in %s, but %s in %s", f.Name, f.Type, wantName, g.Type, gotName))
		}
	}
	return nil
}

func quoteNames(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = strconv.Quote(n)
	}
	return strings.Join(q, ", ")
}

// fileLayout is how ONE file stores the reconciled schema: for each of its fields,
// by reconciled index, the leaf columns to read, whether this file declares it
// optional, and the refusal if this file cannot supply it.
type fileLayout struct {
	leaves   [][]int
	optional []bool
	unread   map[int]error

	// missing marks, by field, a column the file lacks, whose rows are null; from is,
	// by field, the type the file holds a column in where it is narrower than the
	// schema's, and is read in and widened; nil where it is the schema's (step 348).
	missing []bool
	from    []*dtype.DataType

	// fullStats: the file's writer is one whose statistics always carry a null
	// count and both bounds (knownWriter), so the pruner may read their absence
	// as nothing to say rather than as a zero (step 179, audit row I23).
	fullStats bool
}

// layoutFor derives file k's layout from the footer actually being read.
//
// From that footer, and not from what was seen at plan time: a file rewritten
// between CollectSchema and Collect is then read by name, or refused. It is also
// refused if it now allows nulls in a column the plan treats as non-null, because
// the engine would be handed nulls it has promised cannot occur.
func (s *Source) layoutFor(full *dtype.Schema, md *metadata.FileMetaData, k int) (fileLayout, error) {
	fs, leaves, un, err := fileSchema(md)
	if err != nil {
		return fileLayout{}, uerr.Annotate(err, "scan_parquet", s.partName(k))
	}
	if err := s.fits(s.files, fs, "the planned schema", s.partName(k)); err != nil {
		return fileLayout{}, err
	}
	lay := fileLayout{leaves: make([][]int, full.Len()), optional: make([]bool, full.Len()),
		fullStats: knownWriter(md.GetCreatedBy())}
	for i := range full.Len() {
		if i < len(s.fixed) && s.fixed[i] {
			continue // the file's values of it are given, not read (step 347)
		}
		f := full.Field(i)
		j := fs.IndexOf(f.Name)
		if j < 0 {
			// fits let the file lack it: its rows are null (step 348).
			if lay.missing == nil {
				lay.missing = make([]bool, full.Len())
			}
			lay.missing[i], lay.optional[i] = true, true
			continue
		}
		if t := fs.Field(j).Type; !t.Equal(f.Type) {
			if lay.from == nil {
				lay.from = make([]*dtype.DataType, full.Len())
			}
			lay.from[i] = &t
		}
		g := fs.Field(j)
		if g.Nullable && !f.Nullable {
			return fileLayout{}, uerr.New(uerr.KindSchema, "scan_parquet",
				"column %q of %s allows nulls, and the query was planned for a column "+
					"that does not", f.Name, s.partName(k)).
				Hint("the file changed after the query was planned; plan it again")
		}
		lay.leaves[i], lay.optional[i] = leaves[j], g.Nullable
		if e := un[j]; e != nil {
			if lay.unread == nil {
				lay.unread = make(map[int]error)
			}
			lay.unread[i] = uerr.Annotate(e, "scan_parquet", s.partName(k))
		}
	}
	return lay, nil
}

func (s *Source) openFile(ctx context.Context, i int, mem memory.Allocator) (_ *file.Reader, _ func(), err error) {
	ra, closer, err := s.opens[i](ctx)
	if err != nil {
		return nil, nil, uerr.Wrap(err, uerr.KindIO, "scan_parquet",
			"opening %s", s.partName(i))
	}
	// A corrupt footer can panic inside arrow-go. Recovered here as well as by every
	// caller, because only here is the file still this function's to close.
	defer func() {
		if v := recover(); v != nil {
			if closer != nil {
				closer.Close()
			}
			err = corrupt(v, s.partName(i))
		}
	}()
	// The footer's schema is measured before arrow-go recurses on it (step 423).
	if ra, err = measureFooter(ra); err != nil {
		if closer != nil {
			closer.Close()
		}
		return nil, nil, uerr.Annotate(err, "scan_parquet", s.partName(i))
	}
	props := readProps(mem)
	r, err := file.NewParquetReader(ra, file.WithReadProps(props))
	if err == nil {
		if err = checkRowCounts(r.MetaData()); err == nil {
			err = checkChunks(r.MetaData())
		}
		if err != nil {
			r.Close()
		}
	}
	if err == nil {
		boundPages(props, r.MetaData())
		if m, ok := ra.(*measured); ok {
			m.watchChunks(r.MetaData())
		}
	}
	if err != nil {
		if closer != nil {
			closer.Close()
		}
		return nil, nil, uerr.Wrap(err, uerr.KindIO, "scan_parquet",
			"reading the footer of %s", s.partName(i))
	}
	return r, func() {
		r.Close()
		if closer != nil {
			closer.Close()
		}
	}, nil
}

// checkRowCounts refuses a footer whose row counts disagree (step 416): a row group's
// against each of its column chunks', which hold a value a row where the column is
// not repeated and at least one where it is, and the file's against its row groups'.
// A read takes a row group's count for its rows, and a count its sum, so a count
// short of the columns' read short, with no error, and one past them counted rows
// that are not there.
func checkRowCounts(md *metadata.FileMetaData) error {
	var total int64
	for g := range md.NumRowGroups() {
		rg := md.RowGroup(g)
		rows := rg.NumRows()
		if rows < 0 {
			return fmt.Errorf("corrupt: row group %d says %d rows", g, rows)
		}
		total += rows
		for c := range rg.NumColumns() {
			cc, err := rg.ColumnChunk(c)
			if err != nil {
				return err
			}
			vals := cc.NumValues()
			if repeated := md.Schema.Column(c).MaxRepetitionLevel() > 0; vals < rows || !repeated && vals != rows {
				return fmt.Errorf("corrupt: row group %d says %d rows, and its column %s holds %d values",
					g, rows, md.Schema.Column(c).Path(), vals)
			}
		}
	}
	if total != md.NumRows {
		return fmt.Errorf("corrupt: the file says %d rows, and its row groups hold %d", md.NumRows, total)
	}
	return nil
}

// checkChunks refuses a footer that places a column chunk outside the file (step
// 510): arrow-go reads a chunk into a slice of its declared size, made before the
// read finds the bytes missing (ReaderProperties.GetStream), so a chunk claiming
// 2^40 bytes in a file of 162 asked the runtime for a terabyte, a fatal "out of
// memory" no recover catches. A chunk begins at its first page, its dictionary's
// where it has one, as arrow-go takes it.
func checkChunks(md *metadata.FileMetaData) error {
	size := md.GetSourceFileSize()
	for g := range md.NumRowGroups() {
		rg := md.RowGroup(g)
		for c := range rg.NumColumns() {
			cc, err := rg.ColumnChunk(c)
			if err != nil {
				return err
			}
			start, n := cc.DataPageOffset(), cc.TotalCompressedSize()
			if d := cc.DictionaryPageOffset(); cc.HasDictionaryPage() && d > 0 && start > d {
				start = d
			}
			if start < 0 || n < 0 || start > size || n > size-start {
				return fmt.Errorf("corrupt: row group %d says its column %s is %d bytes from byte %d, "+
					"in a file of %d", g, md.Schema.Column(c).Path(), n, start, size)
			}
		}
	}
	return nil
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
func fileSchema(md *metadata.FileMetaData) (*dtype.Schema, [][]int, map[int]error, error) {
	sc := md.Schema
	root := sc.Root()
	n := root.NumFields()
	arrays := arraysOf(md.KeyValueMetadata())

	fields := make([]dtype.Field, n)
	leaves := make([][]int, n)
	var unread map[int]error

	for i := range n {
		f := root.Field(i)
		dt, leaf, err := fieldType(sc, f)
		switch size, ok := arrays[f.Name()]; {
		case ok && size == categorical && dt.ID() == dtype.TypeString:
			dt = dtype.Categorical
		case ok && size > 0 && dt.ID() == dtype.TypeList:
			dt = dtype.Array(dt.Inner(), size)
		}
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

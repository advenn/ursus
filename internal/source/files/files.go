// Package files reads several files of one format as one scan, in order, each file
// by a source of its own, with columns every row of a file shares after the files'
// own: a hive dataset's partition keys, and the file's path (step 360).
package files

import (
	"context"
	"io"
	"slices"
	"sync"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/source"
	"github.com/advenn/ursus/internal/source/hive"
	"github.com/advenn/ursus/internal/uerr"
)

// File is one file of the scan: its source, and its path, which names it in an error
// and is its value of the path column.
type File struct {
	Src interface {
		plan.Source
		source.Openable
	}
	Path string
}

// Shared are the columns every row of a file shares: a hive dataset's partition keys,
// of the types Types names or their values infer, Polars' hive_schema and
// try_parse_hive_dates, and the column of each file's path, Polars'
// include_file_paths.
type Shared struct {
	Parts      *hive.Partitions // nil for no partition column
	Types      map[string]dtype.DataType
	Dates      bool
	PathColumn string // "" for none
}

// Source is files read as one, of the first file's columns, every other file's read
// by name and refused where its columns differ, as Polars reads several IPC files;
// then the shared columns.
type Source struct {
	op, name, desc string
	files          []File
	with           Shared

	once   sync.Once
	schema *dtype.Schema
	own    *dtype.Schema    // the first file's columns
	shared []dtype.Field    // the shared columns, as Schema found them
	consts [][]*data.Column // by file, then shared column, one row each
	err    error
}

// New is a source of files, op naming it in errors, name and desc in Explain, with
// the shared columns with asks for.
func New(op, name, desc string, files []File, with Shared) *Source {
	return &Source{op: op, name: name, desc: desc, files: files, with: with}
}

func (s *Source) Name() string     { return s.name }
func (s *Source) Describe() string { return s.desc }
func (s *Source) Caps() plan.Caps  { return plan.Caps{Projection: true} }

// ClassifyPredicates reports a conjunct of the shared columns alone as Inexact: it
// rules out whole files, judged on their values before any is opened (keep), and the
// Filter above stays to judge the rows, as Parquet's partition keys are pruned.
func (s *Source) ClassifyPredicates(preds []expr.Node) []plan.Pushdown {
	out := make([]plan.Pushdown, len(preds))
	// Background, as Parquet's: Resolve has already read the schema, so this hits the
	// cache, and a failure fails closed to Unsupported.
	if _, err := s.Schema(context.Background()); err != nil {
		return out
	}
	for i, p := range preds {
		if s.onShared(p) {
			out[i] = plan.Inexact
		}
	}
	return out
}

// onShared reports whether p reads shared columns alone, which every row of a file
// shares.
func (s *Source) onShared(p expr.Node) bool {
	names := expr.RootNames(p)
	for _, n := range names {
		if s.sharedIndex(n) < 0 {
			return false
		}
	}
	return len(names) > 0
}

// sharedIndex is the shared column name's index, or -1.
func (s *Source) sharedIndex(name string) int {
	return slices.IndexFunc(s.shared, func(f dtype.Field) bool { return f.Name == name })
}

// keep reports whether file k may hold a row every conjunct of preds on the shared
// columns keeps, judged by eval on its values of them: a file one rules out is not
// opened. With no eval, every file is kept.
func (s *Source) keep(ctx context.Context, k int, preds []expr.Node,
	eval func(context.Context, expr.Node, *data.Batch) (*data.Column, error)) (bool, error) {
	if eval == nil {
		return true, nil
	}
	for _, p := range preds {
		if !s.onShared(p) {
			continue
		}
		var fs []dtype.Field
		var cols []*data.Column
		for _, n := range expr.RootNames(p) {
			if slices.ContainsFunc(fs, func(f dtype.Field) bool { return f.Name == n }) {
				continue
			}
			j := s.sharedIndex(n)
			fs, cols = append(fs, s.shared[j]), append(cols, s.consts[k][j].Rename(n))
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
			return false, nil
		}
	}
	return true, nil
}

// Schema is the first file's columns, then the shared ones; a shared column of a
// file column's name takes its place, as a partition key's does.
func (s *Source) Schema(ctx context.Context) (*dtype.Schema, error) {
	s.once.Do(func() {
		if len(s.files) == 0 {
			s.err = uerr.New(uerr.KindValue, s.op, "no files to read")
			return
		}
		if s.own, s.err = s.files[0].Src.Schema(ctx); s.err != nil {
			s.err = uerr.Annotate(s.err, s.op, s.files[0].Path)
			return
		}
		if s.err = s.share(); s.err != nil {
			return
		}
		fields := s.own.FieldSlice()
		for _, f := range s.shared {
			if i := slices.IndexFunc(fields, func(g dtype.Field) bool { return g.Name == f.Name }); i >= 0 {
				fields[i] = f
			} else {
				fields = append(fields, f)
			}
		}
		s.schema, s.err = dtype.NewSchema(fields...)
	})
	return s.schema, s.err
}

// share finds the shared columns and each file's values of them: the partition keys,
// of their given types, a file column's of the same name, or their values' (step
// 347's rules), and the path column, refused where a column of the files or a key has
// its name, as Polars refuses it.
func (s *Source) share() error {
	s.consts = make([][]*data.Column, len(s.files))
	if p := s.with.Parts; p != nil && len(p.Keys) > 0 {
		types := p.Types(s.with.Types, s.own, s.with.Dates)
		vals, err := p.Values(types)
		if err != nil {
			return uerr.Annotate(err, s.op, s.desc)
		}
		for j, key := range p.Keys {
			null := false
			for k := range vals {
				null = null || !vals[k][j].IsValid(0)
			}
			s.shared = append(s.shared, dtype.Field{Name: key, Type: types[j], Nullable: null})
			for k := range s.files {
				s.consts[k] = append(s.consts[k], vals[k][j])
			}
		}
	}
	if name := s.with.PathColumn; name != "" {
		if s.own.IndexOf(name) >= 0 || slices.ContainsFunc(s.shared, func(f dtype.Field) bool { return f.Name == name }) {
			// Polars: "column name for file paths "a" conflicts with column name from file".
			return uerr.New(uerr.KindSchema, s.op, "the file paths' column %q is a column of the files already", name)
		}
		s.shared = append(s.shared, dtype.Field{Name: name, Type: dtype.String})
		for k, f := range s.files {
			s.consts[k] = append(s.consts[k], data.NewString(name, []string{f.Path}, bitmap.AllSet(1)))
		}
	}
	return nil
}

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
	// read is never nil: a projection of the shared columns alone reads no file's.
	r := &reader{src: s, out: out, spec: spec, read: []string{}, shared: make([]int, out.Len()),
		reps: make([]kernel.Repeater, len(s.shared))}
	for i, f := range out.All() {
		r.shared[i] = slices.IndexFunc(s.shared, func(g dtype.Field) bool { return g.Name == f.Name })
		if r.shared[i] < 0 {
			r.read = append(r.read, f.Name)
		}
	}
	return r, nil
}

// reader reads the files in turn, each one's columns of the projection by its own
// source, and adds the shared ones.
type reader struct {
	src    *Source
	out    *dtype.Schema
	spec   source.ScanSpec
	read   []string          // the projection's file columns
	shared []int             // by output column, its shared column's index, or -1
	reps   []kernel.Repeater // by shared column, its file's value repeated

	mu      sync.Mutex
	file    int
	cur     source.BatchSource
	emitted int
	done    bool

	// On several threads, the next files are read ahead, each on a goroutine of its
	// own, as many as the threads, and their batches handed out in the files' order:
	// a dataset of ten files of 200,000 rows was read a file at a time in 82 ms,
	// where Polars, reading them at once, takes 18 (step 360).
	window []*ahead
	actx   context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// ahead is a file read on a goroutine of its own, its batches sent in order, then its
// channel closed; an error is the last thing sent.
type ahead struct {
	k  int
	ch chan aheadItem
}

type aheadItem struct {
	b   *data.Batch
	err error
}

func (r *reader) Schema() *dtype.Schema { return r.out }

func (r *reader) Next(ctx context.Context) (*data.Batch, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.spec.Threads > 1 && len(r.src.files) > 1 {
		return r.nextAhead(ctx)
	}
	for !r.done {
		if r.spec.MaxRows > 0 && r.emitted >= r.spec.MaxRows {
			break
		}
		if r.cur == nil {
			if r.file == len(r.src.files) {
				break
			}
			keep, err := r.src.keep(ctx, r.file, r.spec.Predicate, r.spec.Eval)
			if err != nil {
				r.done = true
				return nil, err
			}
			if !keep {
				r.file++
				continue
			}
			cur, err := r.open(ctx, r.file, r.emitted)
			if err != nil {
				r.done = true
				return nil, err
			}
			r.cur = cur
		}
		b, err := r.cur.Next(ctx)
		if err == io.EOF {
			err = r.cur.Close()
			r.cur = nil
			r.file++
			if err != nil {
				r.done = true
				return nil, err
			}
			continue
		}
		if err != nil {
			r.done = true
			return nil, uerr.Annotate(err, r.src.op, r.src.files[r.file].Path)
		}
		if b.Rows() == 0 {
			continue
		}
		if r.spec.MaxRows > 0 && r.emitted+b.Rows() > r.spec.MaxRows {
			b = b.Slice(0, r.spec.MaxRows-r.emitted)
		}
		r.emitted += b.Rows()
		return r.withShared(b, r.file)
	}
	r.done = true
	return nil, io.EOF
}

// nextAhead is Next on several threads: the next batch of the first file in the
// window, the window refilled as files end.
func (r *reader) nextAhead(ctx context.Context) (*data.Batch, error) {
	if r.cancel == nil {
		r.actx, r.cancel = context.WithCancel(ctx)
	}
	for !r.done {
		if r.spec.MaxRows > 0 && r.emitted >= r.spec.MaxRows {
			break
		}
		if err := r.fill(ctx); err != nil {
			r.done = true
			return nil, err
		}
		if len(r.window) == 0 {
			break
		}
		a := r.window[0]
		var it aheadItem
		var ok bool
		select {
		case it, ok = <-a.ch:
		case <-ctx.Done():
			r.done = true
			return nil, ctx.Err()
		}
		if !ok {
			r.window = r.window[1:]
			continue
		}
		if it.err != nil {
			r.done = true
			return nil, it.err
		}
		b := it.b
		if r.spec.MaxRows > 0 && r.emitted+b.Rows() > r.spec.MaxRows {
			b = b.Slice(0, r.spec.MaxRows-r.emitted)
		}
		r.emitted += b.Rows()
		return r.withShared(b, a.k)
	}
	r.done = true
	return nil, io.EOF
}

// fill starts reading the next files the predicates keep until the window holds as
// many as the threads.
func (r *reader) fill(ctx context.Context) error {
	for len(r.window) < r.spec.Threads && r.file < len(r.src.files) {
		k := r.file
		r.file++
		keep, err := r.src.keep(ctx, k, r.spec.Predicate, r.spec.Eval)
		if err != nil {
			return err
		}
		if !keep {
			continue
		}
		a := &ahead{k: k, ch: make(chan aheadItem, 4)}
		r.window = append(r.window, a)
		r.wg.Go(func() { r.readAhead(a) })
	}
	return nil
}

// readAhead reads a's file, sending its batches until it ends, fails or the scan is
// closed.
func (r *reader) readAhead(a *ahead) {
	defer close(a.ch)
	send := func(it aheadItem) bool {
		select {
		case a.ch <- it:
			return true
		case <-r.actx.Done():
			return false
		}
	}
	defer func() {
		if p := recover(); p != nil {
			send(aheadItem{err: uerr.FromPanic(p, r.src.op)})
		}
	}()
	cur, err := r.open(r.actx, a.k, 0)
	if err != nil {
		send(aheadItem{err: err})
		return
	}
	for {
		b, err := cur.Next(r.actx)
		if err == io.EOF {
			if err := cur.Close(); err != nil {
				send(aheadItem{err: err})
			}
			return
		}
		if err != nil {
			cur.Close()
			send(aheadItem{err: uerr.Annotate(err, r.src.op, r.src.files[a.k].Path)})
			return
		}
		if b.Rows() > 0 && !send(aheadItem{b: b}) {
			cur.Close()
			return
		}
	}
}

// open opens file k's source for the projection's file columns, its columns first
// checked against the first file's; emitted rows are already read of a limit.
func (r *reader) open(ctx context.Context, k, emitted int) (source.BatchSource, error) {
	f := r.src.files[k]
	if k > 0 {
		got, err := f.Src.Schema(ctx)
		if err != nil {
			return nil, uerr.Annotate(err, r.src.op, f.Path)
		}
		if err := r.src.fits(got, f.Path); err != nil {
			return nil, err
		}
	}
	spec := r.spec
	spec.Projection, spec.Predicate = r.read, nil
	if spec.MaxRows > 0 {
		spec.MaxRows -= emitted
	}
	cur, err := f.Src.Open(ctx, spec)
	if err != nil {
		return nil, uerr.Annotate(err, r.src.op, f.Path)
	}
	return cur, nil
}

// fits refuses got, a later file's columns, unless they are the first file's, in any
// order, as Polars refuses them: a column it lacks, "did not find column b"; one it
// has more, "extra column in file outside of expected schema"; and one of another
// type, "data type mismatch".
func (s *Source) fits(got *dtype.Schema, name string) error {
	for _, f := range s.own.All() {
		i := got.IndexOf(f.Name)
		if i < 0 {
			return uerr.New(uerr.KindSchema, s.op, "%s has no column %q, which %s has", name, f.Name, s.files[0].Path).
				Hint("several files are read as the first one's columns")
		}
		if t := got.Field(i).Type; t != f.Type {
			return uerr.New(uerr.KindSchema, s.op, "%s's column %q is %s, where %s's is %s", name, f.Name, t, s.files[0].Path, f.Type)
		}
	}
	for _, f := range got.All() {
		if s.own.IndexOf(f.Name) < 0 {
			return uerr.New(uerr.KindSchema, s.op, "%s has a column %q, which %s has not", name, f.Name, s.files[0].Path).
				Hint("several files are read as the first one's columns")
		}
	}
	return nil
}

// withShared is b, file k's of the projection's file columns, with the shared ones in
// their places, each its file's value repeated.
func (r *reader) withShared(b *data.Batch, k int) (*data.Batch, error) {
	if len(r.read) == r.out.Len() {
		return b, nil
	}
	consts := r.src.consts[k]
	cols := make([]*data.Column, r.out.Len())
	next := 0
	for i, j := range r.shared {
		name := r.out.Field(i).Name
		if j < 0 {
			cols[i] = b.Columns()[next]
			next++
			continue
		}
		c, err := r.reps[j].Repeat(consts[j], b.Rows())
		if err != nil {
			return nil, err
		}
		cols[i] = c.Rename(name)
	}
	return data.NewBatch(r.out, cols)
}

func (r *reader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done = true
	if r.cancel != nil {
		// The files read ahead stop at their next send, and close their sources.
		r.cancel()
		r.wg.Wait()
		r.window = nil
	}
	if r.cur != nil {
		err := r.cur.Close()
		r.cur = nil
		return err
	}
	return nil
}

package physical

import (
	"context"
	"sort"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
)

// temporalSink computes GroupByDynamic and Rolling.
//
// # One mechanism, because a sorted index makes a window a RANGE
//
// Both operators require the index sorted, and that is what collapses them into one
// algorithm: if rows are in index order then every temporal window covers a
// CONTIGUOUS RUN of them, so a window is a pair (lo, hi) rather than a set of row
// ids. The two differ only in where the pairs come from — a grid, or one per row.
//
// The payoff is that nothing about kernel.Accumulator changes. A window's rows are
// expanded into a (row, group) pair list, gathered once, and fed to the ordinary
// accumulators, so all nineteen aggregates work here on day one and the two that
// refuse to spill refuse for the same reason they do in a hash group-by.
//
// # The cost, stated rather than discovered
//
// Expansion is O(sum of window sizes), which is O(n * period/every) for a dynamic
// group and O(n * rows-per-window) for a rolling one. A seven-day mean over daily
// data is 7n. The O(n) alternative is a SLIDING accumulator, and
// kernel.Accumulator has no Remove — adding one is a second kernel family, not a
// tweak, and it is deliberately not this step.
//
// # It buffers, like windowSink and for the same reason
//
// The grid's extent comes from the observed [min, max] of the index, which is not
// known until the last row. Buffering is also what makes EMPTY windows fall out for
// free, and empty windows are the whole difference between this and
// GroupBy(truncate(ts, every)).
type temporalSink struct {
	schema    *dtype.Schema // index + keys + __aggN
	inSchema  *dtype.Schema
	keySchema *dtype.Schema // index + keys, under their output names

	index expr.Node
	keys  []expr.Node
	specs []aggSpec
	accs  []kernel.Accumulator

	rolling bool
	every   dtype.Interval
	period  dtype.Interval
	offset  dtype.Interval
	closed  expr.Closed

	idxType dtype.DataType
	// loc is the index column's timezone, resolved once. A window boundary is a
	// LOCAL instant — "every day" means local midnight — so a grid built from UTC
	// components would put every row between local and UTC midnight in the wrong
	// window, by a whole day, and never say so.
	loc   *time.Location
	mem   *execopt.Account
	batch int

	parts  []*data.Batch
	ticks  []int64 // the index column, whole stream, in input order
	nRows  int
	sorted bool // whether a violation has already been seen
	badRow int
}

func (s *temporalSink) Schema() *dtype.Schema { return s.schema }
func (s *temporalSink) Close() error          { s.mem.Release(); return nil }

// Merge is refused for the reason windowSink's is, one step further along: two sinks
// that saw different rows cannot have their window grids reconciled without knowing
// the global index order, which is exactly what a partial sink does not have.
func (s *temporalSink) Merge(Sink) error {
	return uerr.Internalf("physical: temporalSink cannot be merged")
}

func (s *temporalSink) Consume(ctx context.Context, in *data.Batch) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n := in.Rows()
	if n == 0 {
		return nil
	}
	idx, err := evalColumn(ctx, s.index, in)
	if err != nil {
		return err
	}
	ticks, err := temporalTicks(idx)
	if err != nil {
		return err
	}
	for i := range n {
		if !idx.IsValid(i) {
			// A null index belongs to no window. Dropping it would change the row
			// count with no error; assigning it one would invent an instant.
			return uerr.New(uerr.KindValue, s.op(),
				"the index column %q is null at row %d", idx.Name(), s.nRows+i).
				Hint("filter the nulls out, or fill them, before grouping by time")
		}
		if s.sorted && s.nRows+i > 0 && ticks[i] < s.lastTick() {
			s.sorted, s.badRow = false, s.nRows+i
		}
		s.ticks = append(s.ticks, ticks[i])
	}

	s.parts = append(s.parts, in)
	s.mem.Retain(in)
	s.mem.RetainBytes(int64(n) * 8)
	s.nRows += n
	return s.mem.Check()
}

func (s *temporalSink) lastTick() int64 { return s.ticks[len(s.ticks)-1] }

func (s *temporalSink) op() string {
	if s.rolling {
		return "rolling"
	}
	return "group_by_dynamic"
}

// Finish cuts the windows and aggregates each one.
func (s *temporalSink) Finish(ctx context.Context) (Operator, error) {
	if !s.sorted {
		return nil, uerr.New(uerr.KindValue, s.op(),
			"the index column is not sorted: row %d goes backwards", s.badRow).
			Hint("a temporal group-by cuts windows from a sorted index, so the rows " +
				"of a window are contiguous").
			Hint("sort first, e.g. .Sort(ursus.Asc(ursus.Col(...))) before this call")
	}

	all, err := kernel.Concat(s.inSchema, s.parts)
	if err != nil {
		return nil, err
	}

	// Bucket the rows by the categorical keys. Each bucket is already in index order,
	// because the whole input is and bucketing preserves relative order.
	buckets, reps, err := s.bucket(ctx, all)
	if err != nil {
		return nil, err
	}

	var (
		rows     []int32 // input row per (window, row) pair
		groups   []int32 // the window each pair belongs to
		starts   []int64 // the index value of each window
		bucketOf []int   // which bucket each window came from, for its key values
	)
	for b, rowsIn := range buckets {
		wins, err := s.windows(rowsIn)
		if err != nil {
			return nil, err
		}
		for _, w := range wins {
			g := int32(len(starts))
			starts = append(starts, w.start)
			bucketOf = append(bucketOf, b)
			for _, r := range rowsIn[w.lo:w.hi] {
				rows = append(rows, r)
				groups = append(groups, g)
			}
		}
	}

	nGroups := len(starts)
	if err := s.accumulate(ctx, all, rows, groups, nGroups); err != nil {
		return nil, err
	}

	cols := make([]*data.Column, 0, 1+len(s.keys)+len(s.specs))
	cols = append(cols, packTemporal(s.keySchema.Field(0).Name, s.idxType, starts))
	if len(s.keys) > 0 {
		pick := make([]int32, nGroups)
		for i, b := range bucketOf {
			pick[i] = reps[b]
		}
		keyCols, err := s.keyColumns(ctx, all)
		if err != nil {
			return nil, err
		}
		for i, c := range keyCols {
			g, err := kernel.Take(c, pick)
			if err != nil {
				return nil, err
			}
			cols = append(cols, g.Rename(s.keySchema.Field(i+1).Name))
		}
	}
	for i, spec := range s.specs {
		s.accs[i].Reserve(nGroups)
		c, err := s.accs[i].Finish(spec.name, nGroups)
		if err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}

	out, err := data.NewBatch(s.schema, cols)
	if err != nil {
		return nil, err
	}
	// The account held the input; it holds the answer now, which is the rebase
	// hashAggSink.Finish and joinBuildSink.freeze both make.
	s.parts, s.ticks = nil, nil
	s.mem.Release()
	s.mem.Retain(out)

	if out.Rows() <= s.chunk() {
		return newBatchOperator(s.schema, out), nil
	}
	return &runOperator{schema: s.schema, src: &batchRun{b: out, n: s.chunk()}}, nil
}

func (s *temporalSink) chunk() int {
	if s.batch <= 0 {
		return DefaultOptions().BatchSize
	}
	return s.batch
}

// bucket splits the row indices by categorical key, preserving index order inside
// each bucket, and returns one representative row per bucket for the key columns.
func (s *temporalSink) bucket(ctx context.Context, all *data.Batch) ([][]int32, []int32, error) {
	if len(s.keys) == 0 {
		rows := make([]int32, all.Rows())
		for i := range rows {
			rows[i] = int32(i)
		}
		return [][]int32{rows}, []int32{0}, nil
	}
	keyCols, err := s.keyColumns(ctx, all)
	if err != nil {
		return nil, nil, err
	}
	enc, err := kernel.NewGroupKeyEncoder(s.op(), keyCols)
	if err != nil {
		return nil, nil, err
	}
	ids := make(map[string]int, 16)
	var buckets [][]int32
	var reps []int32
	for i := range all.Rows() {
		k := enc.Encode(i)
		id, seen := ids[string(k)]
		if !seen {
			id = len(buckets)
			ids[string(k)] = id
			buckets = append(buckets, nil)
			reps = append(reps, int32(i))
		}
		buckets[id] = append(buckets[id], int32(i))
	}
	return buckets, reps, nil
}

func (s *temporalSink) keyColumns(ctx context.Context, all *data.Batch) ([]*data.Column, error) {
	out := make([]*data.Column, len(s.keys))
	for i, k := range s.keys {
		c, err := evalColumn(ctx, k, all)
		if err != nil {
			return nil, err
		}
		out[i] = c
	}
	return out, nil
}

// window is one output row: the index value it is labelled with, and the half-open
// range of positions WITHIN ITS BUCKET that fall in it.
type window struct {
	start  int64
	lo, hi int
}

// windows generates the window list for one bucket.
func (s *temporalSink) windows(rows []int32) ([]window, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	bts := make([]int64, len(rows))
	for i, r := range rows {
		bts[i] = s.ticks[r]
	}
	if s.rolling {
		return s.rollingWindows(bts)
	}
	return s.gridWindows(bts)
}

// gridWindows walks offset + k*every from the first row to the last.
//
// The grid is generated by REPEATED ADDITION rather than by multiplying the interval
// by k, because a calendar interval does not multiply: three months added one at a
// time from January 31st clamps at each step, and 3*1mo has no meaning that agrees
// with it.
func (s *temporalSink) gridWindows(bts []int64) ([]window, error) {
	first, err := s.toTime(bts[0])
	if err != nil {
		return nil, err
	}
	last := bts[len(bts)-1]

	start := s.offset.AddTo(s.every.TruncateTo(first))
	back := s.every.Neg()
	// A positive offset can push the first window past the first row, which would
	// leave early rows in no window at all. Step back until it does not.
	//
	// The bound is the same one the forward walk uses, because it is the same
	// quantity: how many grid points this query is asking for. It used to be 4096,
	// which is not a number anything derives — Every("1s") with Offset("2h") needs
	// 7200 steps and is an ordinary request, so 4096 stopped early and left the
	// invariant this loop exists to establish quietly broken. Exhausting it is a
	// REFUSAL now, not a shrug.
	for steps := 0; start.After(first); steps++ {
		if steps >= s.maxWindows() {
			return nil, s.gridTooLarge("stepping back from the offset to the first row")
		}
		prev := start
		if start = back.AddTo(start); !start.Before(prev) {
			return nil, uerr.Internalf(
				"physical: every=%s does not step backwards from %s", s.every, prev)
		}
	}
	// And when the LOWER end is open, a row sitting exactly on the first boundary is
	// in no window either — the grid has to reach one step further back to catch it.
	// This is the only place the boundary convention changes the grid rather than the
	// range search, and forgetting it silently drops the first row of every
	// closed-right query.
	if s.closed.LowerOpen() {
		start = back.AddTo(start)
	}

	// How many windows this will be, before building any of them.
	//
	// Only for a FIXED interval, where the count is a division: a calendar every
	// cannot be divided into a tick range, and cannot reach the ceiling anyway
	// without a span of millennia. Without this the ceiling would still be honest
	// but expensive to reach — it would build sixteen million windows and then
	// refuse, which is 384 MiB spent to say no.
	if first, err := s.fromTime(start); err == nil {
		if want, ok := s.gridPointsBetween(first, last); ok && want > int64(s.maxWindows()) {
			return nil, s.gridTooLarge("walking from the first row to the last")
		}
	}

	var out []window
	accounted := int64(0)
	for {
		lo, err := s.fromTime(start)
		if err != nil {
			return nil, err
		}
		if lo > last {
			break
		}
		if len(out) >= s.maxWindows() {
			return nil, s.gridTooLarge("walking from the first row to the last")
		}
		hi, err := s.fromTime(s.period.AddTo(start))
		if err != nil {
			return nil, err
		}
		l, h := s.rangeOf(bts, lo, hi)
		out = append(out, window{start: lo, lo: l, hi: h})

		// Account the grid as it grows, on capacity rather than length, which is the
		// stateBytes shape windowSink uses: append grows geometrically, so this is a
		// handful of retains over the whole walk rather than one per window.
		if grown := int64(cap(out)) * windowBytes; grown != accounted {
			s.mem.RetainBytes(grown - accounted)
			accounted = grown
			if err := s.mem.Check(); err != nil {
				return nil, err
			}
		}

		next := s.every.AddTo(start)
		if !next.After(start) {
			return nil, uerr.Internalf("physical: every=%s does not advance", s.every)
		}
		start = next
	}
	return out, nil
}

// windowBytes is one grid point: {int64, int, int} on a 64-bit word.
const windowBytes = 24

// defaultMaxWindows bounds a grid when no memory limit is set.
//
// It is the value the old cap already used, kept on purpose — what changes is that
// reaching it is an ERROR rather than a truncated return. The same number is
// defaultMaxRecord in the CSV scanner, used the same way: a named constant, a
// refusal, and a hint naming the knob.
//
// With a limit set, the account is what refuses first, because the grid is retained
// as it grows. This is the backstop for an unlimited query, where 16.7 million
// windows is 384 MiB of grid built from data that may be two rows long.
const defaultMaxWindows = 1 << 24

// gridPointsBetween is how many grid points span [from, to], when that is a
// division rather than a walk.
//
// Reports false for a calendar interval, whose step is not a fixed number of ticks —
// a month is not 30 days and the whole reason Interval has three fields. The caller
// then falls back to counting as it walks.
func (s *temporalSink) gridPointsBetween(from, to int64) (int64, bool) {
	if s.every.IsCalendar() {
		return 0, false
	}
	npt, ok := s.idxType.NanosPerTick()
	if !ok || npt <= 0 {
		return 0, false
	}
	step := s.every.Nanos() / npt
	if step < 1 {
		// An every finer than the index's own tick floors to the same tick for
		// every point, so the walk does not advance in index space. That is its own
		// defect and not this one's to fix; counting it as one step per tick at
		// least keeps this estimate an under-count rather than an over-count.
		step = 1
	}
	if to < from {
		return 0, true
	}
	return (to-from)/step + 1, true
}

// maxWindows is the ceiling on grid points for one query.
//
// Query-wide rather than per call: Finish generates a grid per categorical bucket,
// so a per-call cap let fifty buckets build fifty times the ceiling. s.gridPoints
// carries the running total across buckets.
func (s *temporalSink) maxWindows() int {
	return defaultMaxWindows
}

// gridTooLarge refuses a grid rather than truncating one.
//
// KindResource, because this is "a limit ursus refused to exceed rather than a
// request it could not understand" — the Kind's own doc — and because a caller can
// then detect it with errors.Is(err, ursus.ErrResource) and change a knob.
func (s *temporalSink) gridTooLarge(phase string) error {
	return uerr.New(uerr.KindResource, s.op(),
		"the window grid is larger than %s can build: %s with every=%s, offset=%s",
		s.op(), phase, s.every, s.offset).
		Hint("the grid has one point per every=%s between the first and last row, "+
			"plus one per every=%s of offset — a coarser every, or a smaller "+
			"offset, is what makes it smaller", s.every, s.every).
		Hint("this is the grid, not the input: it is derived from the index range " +
			"and the interval, so a small frame can still ask for a large one")
}

// rollingWindows gives every row a window ending at its own instant.
func (s *temporalSink) rollingWindows(bts []int64) ([]window, error) {
	back := s.period.Neg()
	out := make([]window, len(bts))
	for i, t := range bts {
		end, err := s.toTime(t)
		if err != nil {
			return nil, err
		}
		if !s.offset.IsZero() {
			end = s.offset.AddTo(end)
		}
		hi, err := s.fromTime(end)
		if err != nil {
			return nil, err
		}
		lo, err := s.fromTime(back.AddTo(end))
		if err != nil {
			return nil, err
		}
		l, h := s.rangeOf(bts, lo, hi)
		out[i] = window{start: t, lo: l, hi: h}
	}
	return out, nil
}

// rangeOf is the half-open position range of a window, by binary search.
//
// Closed picks which end is strict. This is the ONLY place the four boundary
// conventions are interpreted, so a window's membership rule cannot disagree with
// itself between the two generators.
func (s *temporalSink) rangeOf(bts []int64, lo, hi int64) (int, int) {
	l := sort.Search(len(bts), func(i int) bool {
		if s.closed.LowerOpen() {
			return bts[i] > lo
		}
		return bts[i] >= lo
	})
	h := sort.Search(len(bts), func(i int) bool {
		if s.closed.UpperClosed() {
			return bts[i] > hi
		}
		return bts[i] >= hi
	})
	if h < l {
		h = l
	}
	return l, h
}

func (s *temporalSink) toTime(tick int64) (time.Time, error) {
	v, ok := s.idxType.ToTime(tick)
	if !ok {
		return v, uerr.Internalf("physical: %s is not an instant", s.idxType)
	}
	if s.loc != nil {
		v = v.In(s.loc)
	}
	return v, nil
}

func (s *temporalSink) fromTime(t time.Time) (int64, error) {
	v, ok := s.idxType.FromTime(t)
	if !ok {
		return 0, uerr.Internalf("physical: cannot store %s", s.idxType)
	}
	return v, nil
}

// accumulate feeds the expanded (row, group) pairs to the ordinary accumulators.
func (s *temporalSink) accumulate(ctx context.Context, all *data.Batch,
	rows, groups []int32, nGroups int,
) error {
	if len(rows) == 0 {
		for i := range s.accs {
			s.accs[i].Reserve(nGroups)
		}
		return nil
	}
	src, err := takeBatch(s.inSchema, all, rows)
	if err != nil {
		return err
	}
	for i, spec := range s.specs {
		col, err := evalColumn(ctx, spec.input, src)
		if err != nil {
			return err
		}
		s.accs[i].Reserve(nGroups)
		if err := s.accs[i].AddBatch(groups, col); err != nil {
			return err
		}
	}
	return nil
}

// temporalTicks reads a temporal column's storage as int64 ticks, whatever its
// physical width. Date is Int32 days; everything else is Int64.
func temporalTicks(c *data.Column) ([]int64, error) {
	if c.DType().Physical().ID() == dtype.TypeInt32 {
		v, err := data.Values[int32](c)
		if err != nil {
			return nil, err
		}
		out := make([]int64, len(v))
		for i, x := range v {
			out[i] = int64(x)
		}
		return out, nil
	}
	return data.Values[int64](c)
}

// packTemporal stores tick values at the type's physical width.
func packTemporal(name string, dt dtype.DataType, ticks []int64) *data.Column {
	valid := bitmap.AllSet(len(ticks))
	if dt.Physical().ID() == dtype.TypeInt32 {
		v := make([]int32, len(ticks))
		for i, t := range ticks {
			v[i] = int32(t)
		}
		return data.NewFixed(name, dt, v, valid)
	}
	return data.NewFixed(name, dt, ticks, valid)
}

// --- planning ---------------------------------------------------------------------

func planTemporalGroup(ctx context.Context, t *plan.TemporalGroup, opts Options) (Operator, error) {
	child, err := planPipelined(ctx, t.Input, opts)
	if err != nil {
		return nil, err
	}
	in, err := t.Input.Schema()
	if err != nil {
		return nil, err
	}
	outSchema, err := t.Schema()
	if err != nil {
		return nil, err
	}

	// The second instantiation of extractAggs' dedup, one per temporal group. It is
	// the map that three doc comments omitted while calling the total "three".
	var specs []aggSpec
	byKey := map[string]string{}
	rewritten := make([]expr.Node, len(t.Aggs))
	compound := false
	for i, e := range t.Aggs {
		r, err := extractAggs(e, in, &specs, byKey)
		if err != nil {
			return nil, err
		}
		rewritten[i] = r
		if c, ok := r.(*expr.Col); !ok || c.Name != specs[len(specs)-1].name {
			compound = true
		}
	}

	idxField, err := expr.Resolve(t.Index, in)
	if err != nil {
		return nil, err
	}
	idxField.Nullable = false
	keyFields := []dtype.Field{idxField}
	for _, k := range t.Keys {
		f, err := expr.Resolve(k, in)
		if err != nil {
			return nil, err
		}
		keyFields = append(keyFields, f)
	}
	keySchema, err := dtype.NewSchema(keyFields...)
	if err != nil {
		return nil, err
	}

	sinkFields := append([]dtype.Field(nil), keyFields...)
	accs := make([]kernel.Accumulator, len(specs))
	for i, spec := range specs {
		cf, err := spec.input.Field(in)
		if err != nil {
			return nil, err
		}
		acc, err := kernel.NewAccumulator(spec.op, cf.Type, spec.bind, spec.params)
		if err != nil {
			return nil, err
		}
		accs[i] = acc
		specs[i].inType = cf.Type
		sinkFields = append(sinkFields, dtype.Field{
			Name:     spec.name,
			Type:     spec.bind.Out,
			Nullable: !spec.op.IsCounting(),
		})
	}
	sinkSchema, err := dtype.NewSchema(sinkFields...)
	if err != nil {
		return nil, err
	}

	def := expr.ClosedLeft
	if t.Rolling {
		// A rolling window ENDS at the row's own instant, so closing the left end
		// would drop every row from its own window. The two operators genuinely have
		// different conventions; see expr.Closed.
		def = expr.ClosedRight
	}

	sink := &temporalSink{
		schema: sinkSchema, inSchema: in, keySchema: keySchema,
		index: t.Index, keys: t.Keys, specs: specs, accs: accs,
		rolling: t.Rolling,
		every:   t.Every, period: t.Period, offset: t.Offset,
		closed:  t.Closed.Or(def),
		idxType: idxField.Type,
		loc:     temporalLocation(idxField.Type),
		mem:     opts.Budget.Account(opName(t.Rolling)),
		batch:   opts.batchSize(),
		sorted:  true,
	}

	op := Operator(&breaker{child: child, sink: sink})
	if !compound && len(specs) == len(t.Aggs) {
		return &stage{child: op, op: &renameOp{schema: outSchema}}, nil
	}
	projExprs := make([]expr.Node, 0, 1+len(t.Keys)+len(rewritten))
	for i := range keyFields {
		projExprs = append(projExprs, &expr.Col{Name: keySchema.Field(i).Name})
	}
	projExprs = append(projExprs, rewritten...)
	return &stage{child: op, op: &projectOp{schema: outSchema, exprs: projExprs}}, nil
}

// temporalLocation resolves a Datetime's zone once per query. An unloadable zone
// falls back to UTC rather than failing, which is what the .dt namespace already
// does for the same reason.
func temporalLocation(dt dtype.DataType) *time.Location {
	tz := dt.TimeZone()
	if tz == "" || tz == "UTC" {
		return nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil
	}
	return loc
}

func opName(rolling bool) string {
	if rolling {
		return "rolling"
	}
	return "group_by_dynamic"
}

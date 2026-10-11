package physical

import (
	"context"
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
// # It streams (step 196)
//
// It buffered its input, like windowSink, because the grid's extent comes from the
// observed [min, max] of the index, not known until the last row. A sorted index
// needs no extent, though: a bucket's grid starts at its first row, and each row
// makes the windows that start at or before it, EMPTY ones included, which are the
// whole difference between this and GroupBy(truncate(ts, every)). So rows are given
// to their windows as they arrive, and what is held is the windows' state, the open
// windows, and for a rolling group the rows a window yet to come reaches.
// tempstream.go has the stream.
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

	// A dynamic group's label, start and boundaries (step 187); see
	// plan.TemporalGroup.
	label      string
	startBy    string
	boundaries bool

	idxType dtype.DataType
	// loc is the index column's timezone, resolved once. A window boundary is a
	// LOCAL instant — "every day" means local midnight — so a grid built from UTC
	// components would put every row between local and UTC midnight in the wrong
	// window, by a whole day, and never say so.
	loc   *time.Location
	mem   *execopt.Account
	batch int

	// gridPoints is how many window slots this query has already built, across
	// every categorical bucket. Query-wide on purpose: Finish generates a grid per
	// bucket, so a per-call ceiling let fifty buckets build fifty times it.
	gridPoints int

	// The stream (tempstream.go): rows read, the order check, the buckets, and one
	// entry a window in the order made.
	nRows  int
	sorted bool // whether a violation has already been seen
	badRow int
	lastT  int64

	keyOnly *dtype.Schema // the key columns of keySchema
	bIDs    map[string]int32
	bkts    []*tgBucket

	wStart, wEnd []int64 // its bounds, kept only for IncludeBoundaries
	wLabel       []int64
	wFed         []bool
	parts        []*data.Batch // the batches the queued pairs and kept rows point into
	pairs        []rowRef
	pgroups      []int32
	unseen       int // pairs fed since the state was last charged
}

func (s *temporalSink) Schema() *dtype.Schema { return s.schema }
func (s *temporalSink) Close() error          { s.mem.Release(); return nil }

// Merge is refused for the reason windowSink's is, one step further along: two sinks
// that saw different rows cannot have their window grids reconciled without knowing
// the global index order, which is exactly what a partial sink does not have.
func (s *temporalSink) Merge(Sink) error {
	return uerr.Internalf("physical: temporalSink cannot be merged")
}

func (s *temporalSink) op() string {
	if s.rolling {
		return "rolling"
	}
	return "group_by_dynamic"
}

func (s *temporalSink) chunk() int {
	if s.batch <= 0 {
		return DefaultOptions().BatchSize
	}
	return s.batch
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

// gridStart is where the grid begins, as StartBy says: Every's own grid plus Offset,
// by default; the first row's own instant, with Offset ignored, as Polars ignores it;
// or, for a grid of weeks, the weekday named at or before the grid's own start, plus
// Offset. Polars truncates first and then steps back: a fortnight from Friday over a
// first row on Thursday 2024-01-04 starts on Friday 2023-12-22, the one before the
// fortnight's Monday, 2023-12-25. A weekday on any other grid was refused while the
// query was planned.
func (s *temporalSink) gridStart(first time.Time) (time.Time, error) {
	if s.startBy == "datapoint" {
		return first, nil
	}
	start := s.every.TruncateTo(first)
	if day, ok := plan.StartByWeekday(s.startBy); ok && s.every.Weekly() {
		back := (int(start.Weekday()) - int(day) + 7) % 7
		start = start.AddDate(0, 0, -back)
	}
	return s.offset.AddTo(start), nil
}

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
	if left := defaultMaxWindows - s.gridPoints; left > 0 {
		return left
	}
	return 0
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

// gridAtTheEdge is the refusal when a step of the grid does not move.
//
// The resolver has proven every positive, so every is never the cause, though the
// message used to name it, as ursus's bug. A time.Time saturates at its first and
// last instants, and a step from one of them goes nowhere. A row there is the
// data's doing: an Int64 cast to a Datetime(s) is taken as seconds unchecked, and
// one near its maximum is at the edge (step 68's record).
func (s *temporalSink) gridAtTheEdge(at time.Time) error {
	return uerr.New(uerr.KindValue, s.op(),
		"the window grid cannot step past %s, the edge of what Go's time.Time holds",
		at.UTC().Format(time.RFC3339)).
		Hint("an instant this far out usually comes from casting an integer that is not " +
			"a tick count of the type")
}

// rollingWindows gives every row a window ending at its own instant.
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

// fromTime stores a window bound in the index's type.
//
// A bound can leave the type the rows are in: the grid floors the first row, so a
// row near the earliest instant Datetime(ns) holds has a window starting before it
// — 1677-09-21 floors to 1677-09-01 by the month — and a window's end can pass the
// last. That is the data's doing, so it is KindValue, as the same floor is in
// Dt().Truncate; it was ErrInternal.
func (s *temporalSink) fromTime(t time.Time) (int64, error) {
	v, ok := s.idxType.FromTime(t)
	if !ok {
		return 0, uerr.New(uerr.KindValue, "group_by_dynamic",
			"a window bound, %s, is outside the range %s holds", t.UTC().Format(time.RFC3339Nano), s.idxType).
			Hint("a coarser unit holds a wider range: cast the index first, e.g. " +
				".Cast(ursus.Datetime(ursus.Micro, \"\"))")
	}
	return v, nil
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

	var sinkFields []dtype.Field
	if t.Boundaries {
		for _, name := range []string{"_lower_boundary", "_upper_boundary"} {
			sinkFields = append(sinkFields, dtype.Field{Name: name, Type: idxField.Type})
		}
	}
	sinkFields = append(sinkFields, keyFields...)
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
		closed: t.Closed.Or(def),
		label:  t.WindowLabel, startBy: t.StartBy, boundaries: t.Boundaries,
		idxType: idxField.Type,
		loc:     temporalLocation(idxField.Type),
		mem:     opts.Budget.Account(opName(t.Rolling)),
		batch:   opts.batchSize(),
		sorted:  true,
	}
	if sink.keyOnly, err = keyOnlySchema(keySchema); err != nil {
		return nil, err
	}

	op := Operator(&breaker{child: child, sink: sink})
	if !compound && len(specs) == len(t.Aggs) {
		return &stage{child: op, op: &renameOp{schema: outSchema}}, nil
	}
	projExprs := make([]expr.Node, 0, 3+len(t.Keys)+len(rewritten))
	if t.Boundaries {
		projExprs = append(projExprs, &expr.Col{Name: "_lower_boundary"}, &expr.Col{Name: "_upper_boundary"})
	}
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

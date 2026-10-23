package physical

import (
	"context"
	"errors"
	"slices"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/spill"
	"github.com/advenn/ursus/internal/uerr"
)

// windowSink computes `.Over()` expressions and appends them to its input.
//
// # It is hashAggSink with two changes, and both are forced
//
// The group identification, the key encoding, the accumulators and their Finish are
// all the aggregate sink's, unchanged. What differs:
//
//  1. hashAggSink's `groups []int32` is documented as "scratch, reused across
//     batches" and resliced on every Consume, because an aggregate only needs to
//     know which group a row belongs to while that row is being added. A window has
//     to write the answer back onto the row, so it keeps the group id of EVERY row.
//  2. It retains the input batches, because the output is the input plus the window
//     columns and the input has to still be there at Finish. sortSink and
//     joinBuildSink both do this and give the same justification: a Batch is a
//     schema pointer plus immutable Columns, so retaining one retains values that
//     nothing can change underneath it.
//
// Together those make it O(rows) where the aggregate sink is O(distinct keys). That
// is inherent — a window's output is as large as its input — rather than a choice.
//
// # Several partitionings in one node
//
// `Select(x.Mean().Over(g), y.Sum().Over(h))` partitions two different ways, so the
// sink holds a partitioning per DISTINCT key list and each spec points at the one it
// needs. Specs that agree share, which is the common case: every window in a query
// usually partitions the same way.
type windowSink struct {
	inSchema *dtype.Schema
	schema   *dtype.Schema

	parts []*winPartition
	specs []winSpec

	rows []*data.Batch // retained until Finish

	// mem accounts the retained input AND perRow, which is one int32 per input row
	// PER DISTINCT PARTITIONING — so two windows over different keys hold two of
	// them, and a batch walker would report the sink at half its size.
	mem        *execopt.Account
	stateBytes int64

	// copy is every distinct allocation the retained rows hold, reserved as the
	// copy Finish's Concat will make of them. Uncharged, the ledger saw half the
	// true peak, and the limit was enforced against that half.
	copy data.BufferSet

	// The spill state, idle until the query goes over its limit: extwindow.go.
	exprs       []expr.Node // the window expressions, to build fresh sinks from
	budget      *execopt.Budget
	batch       int
	level       int
	ord         string        // the input-ordinal column's name
	spillSchema *dtype.Schema // the input, plus ord
	nRows       int64         // rows stamped so far: the next row's ordinal at level 0
	spilling    bool
	dir         string
	files       int // files created so far, to name the next one
	routeSch    *dtype.Schema
	routeParts  []*partWriter
	pend        [][]int32

	// A window with no partition_by past its limit writes its rows to one file and
	// reads them back (winstream.go).
	oneW    *spill.Writer
	oneFile string
	// There, an aggregate that keeps its values, a quantile's or n_unique's, hands
	// them to disk past the limit (step 242): spill i is spec i's.
	spills []groupSpill

	// A keyed window's one partition past its limit, a level down, goes to that file
	// too, and every other key to rest (winskew.go).
	skewKey []byte
	rest    *windowSink
}

// winPartition is one distinct PARTITION BY, with its group table and the group id
// of every row seen so far.
type winPartition struct {
	keys []expr.Node
	ids  *kernel.KeyTable
	// ints numbers the partitions of one integer key in a sink at level 0, as the
	// group-by's table does, without encoding each row's key (step 336); ids is
	// then unused. A sink a level down keeps the encoded keys its skew reads.
	ints    *kernel.IntKeyTable
	scratch []int64
	perRow  []int32 // one entry per input row, in input order
}

// groups is how many partitions p has so far.
func (p *winPartition) groups() int {
	if p.ints != nil {
		return p.ints.Len()
	}
	return p.ids.Len()
}

// idBytes is what p's partition table holds.
func (p *winPartition) idBytes() int64 {
	if p.ints != nil {
		return p.ints.NBytes()
	}
	return p.ids.NBytes()
}

// intIDs numbers c's rows into gids, c the partition's one integer key.
func (p *winPartition) intIDs(c *data.Column, gids []int32) {
	if p.ints == nil {
		p.ints = kernel.NewIntKeyTable()
	}
	keys := kernel.IntKeys(c, &p.scratch)
	valid := c.Validity()
	var inserted [kernel.ManyChunk]bool
	for base := 0; base < len(keys); base += kernel.ManyChunk {
		m := min(kernel.ManyChunk, len(keys)-base)
		if valid.IsAllSet() {
			p.ints.GetOrInsertMany(keys[base:base+m], gids[base:base+m], inserted[:m])
			continue
		}
		p.ints.GetOrInsertNullable(keys[base:base+m], func(j int) bool { return !valid.Get(base + j) },
			gids[base:base+m], inserted[:m])
	}
}

// winSpec is one window to compute.
//
// Exactly one of acc and fn is set: acc for an aggregate broadcast back to rows, fn
// for an ordered function that needs the whole partition in order.
type winSpec struct {
	name string
	part int // index into windowSink.parts

	// aggregate form
	acc   kernel.Accumulator
	input expr.Node

	// ordered form
	fn     *expr.WinFn
	order  []plan.SortKey
	out    dtype.DataType
	fillOf expr.Node
}

func (s *windowSink) Schema() *dtype.Schema { return s.schema }

func (s *windowSink) Close() error {
	s.mem.Release()
	var err error
	if s.oneW != nil {
		err = s.oneW.Close()
		s.oneW = nil
	}
	err = errors.Join(err, closeSpills(s.spills))
	s.spills = nil
	if s.rest != nil {
		err = errors.Join(err, s.rest.Close())
		s.rest = nil
	}
	return errors.Join(err, s.closeSpill())
}

func (s *windowSink) Consume(ctx context.Context, in *data.Batch) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n := in.Rows()
	if n == 0 {
		return nil
	}
	if s.spilling {
		return s.route(ctx, in, s.parts[0].keys)
	}
	if s.rest != nil {
		return s.consumeSkew(ctx, in)
	}
	if s.oneW != nil {
		return s.consumeOneSpill(ctx, in)
	}
	s.rows = append(s.rows, in)
	s.mem.Retain(in)
	s.copy.AddBatch(in)

	// Assign group ids for every partitioning, keeping this batch's ids separate so
	// the accumulators can be fed with them before they are appended to the running
	// per-row record.
	batchIDs := make([][]int32, len(s.parts))
	for pi, p := range s.parts {
		gids := make([]int32, n)
		if len(p.keys) == 0 {
			// No partition keys: the whole frame is one partition, and it exists
			// even before any row arrives.
			if p.ids.Len() == 0 {
				p.ids.GetOrInsert(nil)
			}
		} else {
			cols := make([]*data.Column, len(p.keys))
			for i, k := range p.keys {
				c, err := evalColumn(ctx, k, in)
				if err != nil {
					return err
				}
				cols[i] = c
			}
			if s.level == 0 && len(cols) == 1 && kernel.IsIntKey(cols[0].DType()) &&
				(p.ints != nil || p.ids.Len() == 0) {
				p.intIDs(cols[0], gids)
			} else {
				enc, err := kernel.NewGroupKeyEncoder("over", cols)
				if err != nil {
					return err
				}
				for i := range n {
					gids[i], _ = p.ids.GetOrInsert(enc.Encode(i))
				}
			}
		}
		batchIDs[pi] = gids
		p.perRow = append(p.perRow, gids...)
	}

	for i := range s.specs {
		sp := &s.specs[i]
		if sp.acc == nil {
			continue // ordered functions run once, at Finish, over the whole frame
		}
		col, gids, err := aggInput(ctx, sp.input, in, batchIDs[sp.part])
		if err != nil {
			return err
		}
		sp.acc.Reserve(s.parts[sp.part].groups())
		if err := sp.acc.AddBatch(gids, col); err != nil {
			return err
		}
	}

	st := s.copy.Total()
	for _, p := range s.parts {
		st += int64(cap(p.perRow))*4 + p.idBytes()
	}
	for i := range s.specs {
		if s.specs[i].acc != nil {
			st += s.specs[i].acc.NBytes()
		}
	}
	s.mem.RetainBytes(st - s.stateBytes)
	s.stateBytes = st
	return s.overBudget(ctx)
}

// Merge is refused for the same reason hashAggSink's is, one step further along.
//
// Two sinks that saw different rows assigned group ids independently, so folding
// their accumulators positionally would add one partition's total to another. The
// window case is strictly harder: even given a group-id remap, the retained per-row
// ids would need remapping too, and the retained batches concatenated in the right
// order. Nothing calls it yet; refusing beats computing a wrong answer quietly.
func (s *windowSink) Merge(other Sink) error {
	if _, ok := other.(*windowSink); !ok {
		return uerr.Internalf("physical: cannot merge %T into windowSink", other)
	}
	return uerr.Internalf(
		"physical: windowSink.Merge needs a group-id remap it does not have — " +
			"two sinks assigned different partition numbering, so folding them would " +
			"attribute one partition's window value to another")
}

func (s *windowSink) Finish(ctx context.Context) (Operator, error) {
	if s.spilling {
		return s.finishSpilled(ctx)
	}
	if s.rest != nil {
		return s.finishSkew(ctx)
	}
	if s.oneW != nil {
		return s.finishOneSpill(ctx)
	}
	if len(s.rows) == 0 {
		// No row ever arrived. The result is still one column per output field, all
		// empty — a zero-column batch would leave the schema unsatisfied.
		empty, err := data.NewBatch(s.schema, emptyColumns(s.schema))
		if err != nil {
			return nil, err
		}
		return newBatchOperator(s.schema, empty), nil
	}

	// The ordered functions need every row addressable by one index, and the output
	// carries the input's columns through, so the retained batches are concatenated
	// once here. This is the peak-memory moment, as it is for sortSink.
	all, err := kernel.Concat(s.inSchema, s.rows)
	if err != nil {
		return nil, err
	}
	nRows := all.Rows()

	cols := make([]*data.Column, 0, s.inSchema.Len()+len(s.specs))
	cols = append(cols, all.Columns()...)

	for i := range s.specs {
		sp := &s.specs[i]
		var c *data.Column
		if sp.acc != nil {
			c, err = s.finishAggregate(sp)
		} else {
			c, err = s.finishOrdered(ctx, sp, all, nRows)
		}
		if err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}

	out, err := data.NewBatch(s.schema, cols)
	if err != nil {
		return nil, err
	}
	return newBatchOperator(s.schema, out), nil
}

// finishAggregate is the whole broadcast: one value per partition, gathered back
// onto the rows by their partition ids.
//
// kernel.Take does it for every type — Int128 from an integer sum, String from a
// min or max, Bool from any or all_true — because every Accumulator.Finish returns
// exactly nGroups rows indexed by ordinal with no gaps, so every id is in range by
// construction.
func (s *windowSink) finishAggregate(sp *winSpec) (*data.Column, error) {
	p := s.parts[sp.part]
	nGroups := p.groups()
	sp.acc.Reserve(nGroups)
	res, err := sp.acc.Finish(sp.name, nGroups)
	if err != nil {
		return nil, err
	}
	c, err := kernel.Take(res, p.perRow)
	if err != nil {
		return nil, err
	}
	return c.Rename(sp.name), nil
}

func (s *windowSink) finishOrdered(ctx context.Context, sp *winSpec,
	all *data.Batch, nRows int) (*data.Column, error) {

	child, err := evalColumn(ctx, sp.fn.Child, all)
	if err != nil {
		return nil, err
	}

	seg, err := s.segments(ctx, sp, all, child, nRows)
	if err != nil {
		return nil, err
	}

	var fill *data.Column
	if sp.fillOf != nil {
		if fill, err = evalColumn(ctx, sp.fillOf, all); err != nil {
			return nil, err
		}
		// The column and its fill meet at the type WinFnType chose, strictly, as a
		// When's branches do: that type holds both, so a value that fails to convert
		// is a bug, not a lossy conversion anyone asked for.
		if fill.DType() != sp.out {
			if fill, err = kernel.Cast(fill.Name(), sp.out, true, fill); err != nil {
				return nil, err
			}
		}
		if child.DType() != sp.out {
			if child, err = kernel.Cast(child.Name(), sp.out, true, child); err != nil {
				return nil, err
			}
		}
	}
	if sp.fn.Fn == expr.WinCumulativeEval {
		return cumulativeEval(ctx, sp.fn.Params, sp.name, sp.out, child, seg)
	}
	return kernel.WinCall(sp.fn.Fn, sp.fn.Params, sp.name, sp.out, child, fill, seg)
}

// segments orders the rows partition-major and, within each partition, by whatever
// the function orders by.
//
// # One sort, not one per partition
//
// The partition ids go in as the FIRST key column. NewComparator is lexicographic
// and stops at the first non-zero, so rows in different partitions never reach the
// remaining keys and rows in the same partition are compared by them alone. A single
// ArgSort therefore produces a permutation that is simultaneously partition-major
// and correctly ordered inside each partition, and the boundaries fall out of one
// linear scan.
//
// ArgSort is stable, which is what makes ties deterministic: with no order keys at
// all the rows of a partition keep their input order, and that is exactly what
// CumCount and Shift want.
//
// # Rank brings its own ordering
//
// `Col("speed").Rank(...).Over(Col("type"))` ranks by speed, not by the window's
// OrderBy. So rank sorts by its own input column and everything else sorts by the
// window's OrderBy.
func (s *windowSink) segments(ctx context.Context, sp *winSpec, all *data.Batch,
	child *data.Column, nRows int) (kernel.Segments, error) {

	p := s.parts[sp.part]
	if sp.fn.Fn != expr.WinRank && len(sp.order) == 0 {
		// No order of its own: the rows a partition at a time, each in the rows'
		// order, which a stable sort by the id alone gives and a counting sort gives in
		// two passes. The sort was a third of an interpolation's time over a million
		// rows in one partition (step 329).
		return segmentsByID(p.perRow[:nRows], max(1, p.groups())), nil
	}
	gid := data.NewFixed("__gid", dtype.Int32, p.perRow[:nRows], bitmap.AllSet(nRows))

	keyCols := []*data.Column{gid}
	specs := []kernel.SortSpec{{}}

	if sp.fn.Fn == expr.WinRank {
		keyCols = append(keyCols, child)
		specs = append(specs, kernel.SortSpec{Descending: sp.fn.Params.Descending})
	} else {
		for _, k := range sp.order {
			c, err := evalColumn(ctx, k.Expr, all)
			if err != nil {
				return kernel.Segments{}, err
			}
			keyCols = append(keyCols, c)
			specs = append(specs, kernel.SortSpec{
				Descending: k.Descending, NullsLast: k.NullsLast,
			})
		}
	}

	// Always two or more key columns — the partition id plus the rank or order
	// key — which is why this call was 60% of ArgSort's time across the engine and
	// why the radix path had to handle multiple columns to be worth anything here.
	perm, err := kernel.ArgSortColumns(keyCols, specs, nRows)
	if err != nil {
		return kernel.Segments{}, err
	}

	// Partitions are contiguous in perm, so a boundary is wherever the id changes.
	bounds := make([]int32, 0, p.groups()+1)
	bounds = append(bounds, 0)
	for j := 1; j < nRows; j++ {
		if p.perRow[perm[j]] != p.perRow[perm[j-1]] {
			bounds = append(bounds, int32(j))
		}
	}
	bounds = append(bounds, int32(nRows))
	return kernel.Segments{Perm: perm, Bounds: bounds}, nil
}

// segmentsByID is the rows a partition at a time, the partitions in id order and each
// one's rows in theirs, as ArgSortColumns of the ids alone orders them: a counting
// sort over ids, which are dense below parts. An id no row holds makes no segment.
func segmentsByID(ids []int32, parts int) kernel.Segments {
	if parts == 1 {
		perm := make([]int32, len(ids))
		for i := range perm {
			perm[i] = int32(i)
		}
		return kernel.Segments{Perm: perm, Bounds: []int32{0, int32(len(ids))}}
	}
	start := make([]int32, parts+1)
	for _, id := range ids {
		start[id+1]++
	}
	for p := range parts {
		start[p+1] += start[p]
	}
	next := slices.Clone(start[:parts])
	perm := make([]int32, len(ids))
	for i, id := range ids {
		perm[next[id]] = int32(i)
		next[id]++
	}
	bounds := make([]int32, 0, parts+1)
	bounds = append(bounds, 0)
	for p := range parts {
		if start[p+1] > start[p] {
			bounds = append(bounds, start[p+1])
		}
	}
	if len(ids) == 0 {
		bounds = append(bounds, 0)
	}
	return kernel.Segments{Perm: perm, Bounds: bounds}
}

// --- planning -------------------------------------------------------------------

// planWindow lowers a plan.Window.
//
// The child is planned through planPipelined, so everything below the window still
// runs across N workers; the sink itself is serial, which is where step 5 draws the
// line for every pipeline breaker.
func planWindow(ctx context.Context, w *plan.Window, opts Options) (Operator, error) {
	in, err := w.Input.Schema()
	if err != nil {
		return nil, err
	}
	out, err := w.Schema()
	if err != nil {
		return nil, err
	}
	child, err := planPipelined(ctx, w.Input, opts)
	if err != nil {
		return nil, err
	}

	// The ordinal is named against the OUTPUT, so it cannot collide with a window's
	// alias either: a later pass's input holds the earlier passes' columns.
	ord := ordinalName(out)
	sp, err := dtype.NewSchema(append(in.FieldSlice(), dtype.NotNull(ord, dtype.Int64))...)
	if err != nil {
		return nil, err
	}
	sink, err := newWindowSink(in, out, w.Exprs, opts.Budget.Account("over"))
	if err != nil {
		return nil, err
	}
	sink.budget, sink.batch, sink.ord, sink.spillSchema = opts.Budget, opts.batchSize(), ord, sp
	return &breaker{child: child, sink: sink}, nil
}

// newWindowSink builds a sink computing exprs over rows of schema in, producing out.
//
// planWindow calls it once; a spilling window calls it again for every partition
// file, with fresh accumulators each time.
func newWindowSink(in, out *dtype.Schema, exprs []expr.Node, mem *execopt.Account) (*windowSink, error) {
	sink := &windowSink{inSchema: in, schema: out, mem: mem, exprs: exprs}
	byKeys := map[string]int{}

	for _, e := range exprs {
		alias, ok := e.(*expr.Alias)
		if !ok {
			return nil, uerr.Internalf("physical: window expression is not aliased: %s", e)
		}
		win, ok := alias.Child.(*expr.Window)
		if !ok {
			return nil, uerr.Internalf("physical: window expression is not a window: %s", e)
		}
		if win.Mapping != expr.MapGroupsToRows {
			return nil, uerr.New(uerr.KindUnsupported, "over",
				"the %s mapping strategy is not implemented", win.Mapping).
				Hint("the default mapping gives one value per row in the original order")
		}

		// Share a partitioning between windows that partition identically, which is
		// the usual case — every window in a query tends to use the same key.
		//
		// Identically by expr.Identity, and this is the worst of the four dedup maps
		// to get wrong: what is shared is not a temporary but the partition
		// ASSIGNMENT, so a false merge buckets every row of the second window by the
		// first one's values. By the rendering alone, `k * int8(2)` and
		// `k * int64(2)` were one partitioning, and over an Int8 k the first wraps.
		key := expr.IdentityAll(win.PartitionBy)
		pi, seen := byKeys[key]
		if !seen {
			pi = len(sink.parts)
			byKeys[key] = pi
			sink.parts = append(sink.parts, &winPartition{
				keys: win.PartitionBy, ids: kernel.NewKeyTable(),
			})
		}

		spec := winSpec{name: alias.Name, part: pi}
		switch body := win.Child.(type) {
		case *expr.Agg:
			// order is deliberately NOT carried here, and the refusal below is why.
			//
			// It used to be set for both branches, in the shared initializer above,
			// and only finishOrdered ever read it — so an OrderBy over an aggregate
			// was accepted and silently dropped. That is worse than not supporting
			// it: the user asked for something and got no answer either way.
			//
			// Honouring it means an ordered aggregate, which needs a per-expression
			// order key that exists nowhere in the aggregate path. Refusing is the
			// honest state until it does.
			if len(win.OrderBy) > 0 {
				return nil, expr.OrderedAggRefusal(body.Op)
			}
			cf, err := body.Child.Field(in)
			if err != nil {
				return nil, err
			}
			bind, err := expr.ResolveAggBinding(body.Op, cf.Type)
			if err != nil {
				return nil, err
			}
			acc, err := kernel.NewAccumulator(body.Op, cf.Type, bind, body.Params)
			if err != nil {
				return nil, err
			}
			spec.acc, spec.input = acc, body.Child

		case *expr.WinFn:
			spec.order = win.OrderBy
			cf, err := body.Child.Field(in)
			if err != nil {
				return nil, err
			}
			t, err := expr.WinFnType(body, cf.Type, in)
			if err != nil {
				return nil, err
			}
			spec.fn, spec.out, spec.fillOf = body, t, body.Fill

		default:
			// Everything else is a per-row expression, which does not need a window
			// at all. Refusing names the situation instead of computing a partition
			// aggregate of something that was never aggregated.
			return nil, uerr.New(uerr.KindType, "over",
				"%s is not a window function", win.Child.String()).
				Hint("Over applies to an aggregate such as .Sum() or .Mean(), or to " +
					"an ordered function such as .Rank(...) or .CumSum(...)")
		}
		sink.specs = append(sink.specs, spec)
	}
	return sink, nil
}

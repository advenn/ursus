package physical

import (
	"context"
	"errors"
	"os"
	"strconv"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
)

// hashAggSink implements GroupBy(...).Agg(...).
//
// # Two-phase lowering
//
// An aggregate expression is not always a bare aggregate: `Col("a").Sum().Div(
// Col("a").Count())` is a Binary over two Aggs. So planning splits it in two:
//
//  1. This sink computes each DISTINCT inner Agg into a temporary column.
//  2. If any expression is more than a bare Agg, a Project on top evaluates the
//     surrounding arithmetic over those temporaries.
//
// The alternative — teaching the sink to evaluate arbitrary expressions over
// group state — would duplicate the evaluator. Reusing Project means compound
// aggregate expressions get the whole expression system for free.
//
// # Group identity
//
// Group ids are assigned in FIRST-APPEARANCE order. Keys are materialised
// incrementally: when a batch introduces new groups, exactly those rows are taken
// from the key columns and kept. Memory is therefore O(distinct keys), not
// O(input) — until the distinct keys themselves stop fitting, which is what the
// next section is about.
//
// # Spilling, by RESIDENCY FREEZE
//
// Past the memory budget the sink freezes its `ids` map. From that point a row
// whose key is already resident feeds the resident accumulators exactly as before,
// and a row with a NEW key is routed to one of sixteen partition files by a hash
// of its encoded key. At Finish the resident groups are emitted, then each
// partition is replayed through a FRESH sink and its groups emitted.
//
// The invariant that makes this work is one sentence:
//
//	A key's residency is decided at its FIRST APPEARANCE, and residency only
//	grows BEFORE the freeze — so every key is either resident for its whole life
//	or routed for its whole life, and partitionOf is a pure function of the
//	encoded key, so a routed key's rows all land in one file.
//
// Two things follow, and the second is why this shape was chosen over partitioning
// everything from the start:
//
//   - No Accumulator.Merge is ever called. positionAcc (First/Last) and
//     argExtremumAcc both require `other` to have seen a strictly LATER portion of
//     the input, and argExtremumAcc.Merge carries a position shift to compensate.
//     Here one sink sees all of a group's rows, in input order, so both are correct
//     with no shift at all — a partition-evict design would need exactly that
//     precondition and could not supply it.
//   - A query that does NOT spill produces byte-identical output to before. Roughly
//     sixty GroupBy call sites in the test suite rely on first-appearance order
//     without asking for it; partitioning unconditionally would reorder every one.
//
// What it does not bound: radix partitioning divides the KEY SPACE, never one key.
// quantile keeps every value of a group and n_unique every distinct value, so a
// single hot key is unbounded whatever the depth. See overBudget.
type hashAggSink struct {
	schema *dtype.Schema
	keys   []expr.Node
	specs  []aggSpec

	ids  map[string]int32
	accs []kernel.Accumulator

	keyParts  []*data.Batch // one per batch that introduced groups, in id order
	keySchema *dtype.Schema
	groups    []int32 // scratch, reused across batches

	// mem accounts what the sink holds: the materialised key values of every
	// distinct group, plus the accumulators' per-group state. It does NOT hold the
	// input batches — a hash aggregation is O(distinct keys), which is why it is
	// not the first operator to need spilling even though it is the second largest.
	mem      *execopt.Account
	accBytes int64

	// --- spilling ------------------------------------------------------------
	budget *execopt.Budget // for SpillDir and NoteSpill; sortSink carries the same
	batch  int             // output chunk size
	level  int             // radix depth; 0 for the sink planAggregate builds

	inSchema    *dtype.Schema // the batches Consume is handed
	spillSchema *dtype.Schema // inSchema, plus __ord when ordered

	frozen    bool  // residency is closed: a NEW key is routed, never admitted
	frozenAcc int64 // accBytes at the freeze — the baseline overBudget measures from

	dir      string // spill directory; only the level-0 sink removes it
	dirOwner bool
	prefix   string        // filename prefix, derived from the parent's file
	parts    []*partWriter // one per radix bucket, opened lazily
	files    []string      // closed partition files, in bucket order
	pend     [][]int32     // scratch: this batch's rows bound for each bucket
	keep     []int32       // scratch: this batch's rows that stay resident

	// --- MaintainOrder --------------------------------------------------------
	ordered   bool
	firstSeen []int64 // input ordinal of each resident group's first row
	nRows     int64   // rows consumed, the ordinal source at level 0
}

// aggSpec is one distinct inner aggregate: its op, its input expression, and the
// temporary name its result is published under.
type aggSpec struct {
	op     expr.AggOp
	input  expr.Node
	name   string
	bind   expr.AggBinding
	params expr.AggParams

	// inType is the accumulator's input type. planAggregate already computes it and
	// used to throw it away; a replay sink cannot rebuild its accumulators without
	// it, and rebuilding them from `op` alone would turn every Quantile(0.9) into a
	// median FOR SPILLED GROUPS ONLY — the shape step 7 and step 11 each shipped
	// once.
	inType dtype.DataType
}

// planAggregate lowers a plan.Aggregate into an operator.
//
// It returns either the bare sink (when every aggregate expression is a plain
// aggregate) or the sink wrapped in a Project (when any expression does arithmetic
// over aggregates). The Project reuses the ordinary evaluator, so `sum(a)/count()`
// needs nothing new.
func planAggregate(ctx context.Context, a *plan.Aggregate, opts Options) (Operator, error) {
	child, err := planPipelined(ctx, a.Input, opts)
	if err != nil {
		return nil, err
	}
	in, err := a.Input.Schema()
	if err != nil {
		return nil, err
	}
	outSchema, err := a.Schema()
	if err != nil {
		return nil, err
	}

	// Phase 1: pull the inner aggregates out into temporaries.
	var specs []aggSpec
	byKey := map[string]string{}
	rewritten := make([]expr.Node, len(a.Aggs))
	compound := false
	for i, e := range a.Aggs {
		r, err := extractAggs(e, in, &specs, byKey)
		if err != nil {
			return nil, err
		}
		rewritten[i] = r
		// If the rewrite is exactly a reference to the temporary, the expression was
		// a bare aggregate and no Project is needed for it.
		if c, ok := r.(*expr.Col); !ok || c.Name != specs[len(specs)-1].name {
			compound = true
		}
	}

	// The key half of the sink's own schema.
	keyFields := make([]dtype.Field, len(a.Keys))
	for i, k := range a.Keys {
		f, err := expr.Resolve(k, in)
		if err != nil {
			return nil, err
		}
		keyFields[i] = f
	}
	keySchema, err := dtype.NewSchema(keyFields...)
	if err != nil {
		return nil, err
	}

	// The sink publishes keys under their real names and aggregates under
	// temporaries, so a temporary can never collide with a user column.
	sinkFields := append([]dtype.Field(nil), keyFields...)
	for i, spec := range specs {
		cf, err := spec.input.Field(in)
		if err != nil {
			return nil, err
		}
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

	// ONE constructor, called once for the serial path and N times for the parallel
	// one. Two sinks that have to be mergeable must not be built from two literals:
	// the day they drift is the day Merge folds mismatched state.
	//
	// Accumulators are built here rather than hoisted, because each worker needs its
	// own. An unimplemented aggregate still fails at plan time, since aggBreaker
	// calls this at least once before returning.
	newSink := func() (Sink, error) {
		accs := make([]kernel.Accumulator, len(specs))
		for i, spec := range specs {
			acc, err := kernel.NewAccumulator(spec.op, spec.inType, spec.bind, spec.params)
			if err != nil {
				return nil, err
			}
			accs[i] = acc
		}
		s := &hashAggSink{
			schema:    sinkSchema,
			keys:      a.Keys,
			specs:     specs,
			ids:       make(map[string]int32),
			accs:      accs,
			keySchema: keySchema,
			mem:       opts.Budget.Account("group_by"),

			budget:   opts.Budget,
			batch:    opts.batchSize(),
			inSchema: in,
			parts:    make([]*partWriter, nParts),
			pend:     make([][]int32, nParts),
			ordered:  a.MaintainOrder,
		}
		// The spill schema is the input schema, plus the input ordinal when order is
		// being maintained. __ord never reaches the sink's OUTPUT schema, which is
		// what lets a sub-sink be the same type as its parent.
		s.spillSchema = in
		if a.MaintainOrder {
			f := append(in.FieldSlice(), dtype.NotNull(ordCol, dtype.Int64))
			sp, err := dtype.NewSchema(f...)
			if err != nil {
				return nil, err
			}
			s.spillSchema = sp
		}
		return s, nil
	}

	op, err := aggBreaker(child, newSink, aggWorkers(a, specs, opts))
	if err != nil {
		return nil, err
	}

	if !compound && len(specs) == len(a.Aggs) {
		// Every aggregate was bare, so the only remaining work is renaming the
		// temporaries to their user-facing names.
		return &stage{child: op, op: &renameOp{schema: outSchema}}, nil
	}

	// Phase 2: evaluate the surrounding arithmetic over the temporaries.
	projExprs := make([]expr.Node, 0, len(a.Keys)+len(rewritten))
	for i := range a.Keys {
		projExprs = append(projExprs, &expr.Col{Name: keySchema.Field(i).Name})
	}
	projExprs = append(projExprs, rewritten...)

	return &stage{child: op, op: &projectOp{schema: outSchema, exprs: projExprs}}, nil
}

// renameOp relabels a batch's columns to a target schema, positionally.
//
// The aggregate sink names its outputs __agg0, __agg1, … so that a temporary can
// never collide with a user column. This puts the user's names back.
type renameOp struct{ schema *dtype.Schema }

func (r *renameOp) Schema() *dtype.Schema { return r.schema }
func (r *renameOp) Close() error          { return nil }

func (r *renameOp) Apply(_ context.Context, in *data.Batch) (*data.Batch, error) {
	if in.NumCols() != r.schema.Len() {
		return nil, uerr.Internalf("physical: rename got %d columns, want %d",
			in.NumCols(), r.schema.Len())
	}
	cols := make([]*data.Column, in.NumCols())
	for i, c := range in.Columns() {
		f := r.schema.Field(i)
		cols[i] = c.Rename(f.Name).WithDType(f.Type)
	}
	return data.NewBatch(r.schema, cols)
}

// --- Sink ---------------------------------------------------------------------

func (s *hashAggSink) Schema() *dtype.Schema { return s.schema }

func (s *hashAggSink) Close() error {
	errs := []error{s.closeParts()}
	s.mem.Release()
	if s.dirOwner && s.dir != "" {
		dir := s.dir
		s.dir, s.files = "", nil
		if err := os.RemoveAll(dir); err != nil {
			errs = append(errs, uerr.Wrap(err, uerr.KindIO, "group_by",
				"removing spill directory %s", dir))
		}
	}
	return errors.Join(errs...)
}

func (s *hashAggSink) Consume(ctx context.Context, in *data.Batch) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n := in.Rows()
	if n == 0 {
		return nil
	}

	// Evaluate the key expressions for this batch.
	keyCols := make([]*data.Column, len(s.keys))
	for i, k := range s.keys {
		c, err := evalColumn(ctx, k, in)
		if err != nil {
			return err
		}
		keyCols[i] = c
	}

	// Assign group ids, remembering which rows introduced new ones.
	//
	// s.groups is DENSE OVER RESIDENT ROWS, not indexed by row. That matters once
	// the sink can freeze: AddBatch reads every slot it is given, and a routed row
	// left unwritten would inherit the PREVIOUS batch's id at that index — always in
	// range, because ids is monotone — and fold into an arbitrary resident group
	// with no panic, no length mismatch and no error. Keeping it dense makes that
	// unreachable rather than merely unlikely.
	s.groups = s.groups[:0]
	keep := s.keep[:0]
	var newRows []int32
	routed := 0

	if len(s.keys) == 0 {
		// A global aggregate: every row belongs to group 0, and group 0 exists even
		// if no row ever arrives (see Finish). No new key can ever appear, so this
		// path never routes.
		for i := range n {
			s.groups = append(s.groups, 0)
			keep = append(keep, int32(i))
		}
		if len(s.ids) == 0 {
			s.ids[""] = 0
			if s.ordered {
				s.firstSeen = append(s.firstSeen, s.ordinalOf(in, 0))
			}
		}
	} else {
		enc, err := kernel.NewGroupKeyEncoder("group_by", keyCols)
		if err != nil {
			return err
		}
		for i := range n {
			k := enc.Encode(i)
			// A map lookup on string(bytes) does not allocate in Go, so only a
			// genuinely new group pays for the key copy.
			id, seen := s.ids[string(k)]
			if !seen {
				if s.frozen {
					// Residency is closed. This row and every later row of the same key
					// go to the same file: partitionOf is a pure function of the encoded
					// key, and ids can never admit it now.
					p := partitionOf(k, s.level)
					s.pend[p] = append(s.pend[p], int32(i))
					routed++
					continue
				}
				id = int32(len(s.ids))
				s.ids[string(k)] = id
				newRows = append(newRows, int32(i))
				if s.ordered {
					s.firstSeen = append(s.firstSeen, s.ordinalOf(in, i))
				}
			}
			keep = append(keep, int32(i))
			s.groups = append(s.groups, id)
		}
	}
	s.keep = keep

	if routed > 0 {
		if err := s.route(in); err != nil {
			return err
		}
	}

	// Materialise the key values of newly-seen groups only.
	if len(newRows) > 0 {
		cols := make([]*data.Column, len(keyCols))
		for i, c := range keyCols {
			taken, err := kernel.Take(c, newRows)
			if err != nil {
				return err
			}
			cols[i] = taken.Rename(s.keySchema.Field(i).Name)
		}
		part, err := data.NewBatch(s.keySchema, cols)
		if err != nil {
			return err
		}
		s.keyParts = append(s.keyParts, part)
		s.mem.Retain(part)
	}

	// Feed the accumulators, over the RESIDENT rows only.
	//
	// Restricting the batch first is sound because an aggregate's input is a
	// row-local expression — the same argument the sort's re-keying makes for
	// evaluating a key over a gathered batch. A window inside Agg() is refused at
	// plan time, so there is no non-row-local case to worry about.
	if len(keep) > 0 {
		src := in
		if len(keep) != n {
			var err error
			if src, err = takeBatch(s.inSchema, in, keep); err != nil {
				return err
			}
		}
		nGroups := len(s.ids)
		for i, spec := range s.specs {
			// evalColumn broadcasts a literal input so it lines up with `groups`. That
			// is what makes Len() — which aggregates over a literal precisely so it
			// needs no column — work on a frame that has only key columns.
			col, err := evalColumn(ctx, spec.input, src)
			if err != nil {
				return err
			}
			s.accs[i].Reserve(nGroups)
			if err := s.accs[i].AddBatch(s.groups, col); err != nil {
				return err
			}
		}
	}

	// Accumulator state is tracked as a DELTA rather than released and re-retained,
	// so accounting stays O(groups) per batch rather than O(groups) map churn.
	var acc int64
	for _, a := range s.accs {
		acc += a.NBytes()
	}
	acc += int64(cap(s.firstSeen)) * 8
	s.mem.RetainBytes(acc - s.accBytes)
	s.accBytes = acc
	s.nRows += int64(n)

	// The freeze is tested at a BATCH BOUNDARY, so no batch is ever half-frozen.
	// The cost is that the resident set overshoots by up to one batch's distinct
	// keys; the benefit is that the invariant is visible rather than argued.
	if !s.frozen && s.budget.Limit() > 0 && s.mem.Over() && len(s.keys) > 0 {
		s.frozen, s.frozenAcc = true, acc
	}
	return s.overBudget()
}

func (s *hashAggSink) Merge(other Sink) error {
	o, ok := other.(*hashAggSink)
	if !ok {
		return uerr.Internalf("physical: cannot merge %T into hashAggSink", other)
	}
	// Two spilling sinks each hold their own partition files, which this fold would
	// silently drop; and inserting a key into a table that has already frozen breaks
	// the residency invariant. sortSink.Merge and joinBuildSink.Merge have the same
	// guard, and aggCanParallelise refuses to build workers under a memory limit at
	// all, so this is the second line of defence rather than the first.
	if s.frozen || o.frozen || len(s.files) > 0 || len(o.files) > 0 {
		return uerr.Internalf("physical: cannot merge hashAggSinks that have spilled")
	}
	// MaintainOrder is not mergeable here. firstSeen holds each group's input
	// ordinal, and two sinks that consumed INTERLEAVED portions number their rows
	// independently — worker 1's ordinal 5 may precede worker 0's ordinal 3 — so
	// there is no way to reconcile them without a global sequence the driver does
	// not carry. Refused rather than approximated; aggCanParallelise declines the
	// same case up front.
	if s.ordered || o.ordered {
		return uerr.Internalf(
			"physical: cannot merge hashAggSinks with MaintainOrder — first-appearance " +
				"ordinals are per-sink and the two saw interleaved portions of the input")
	}

	// The remap: o's group ids expressed in this sink's numbering.
	//
	// o's keys are visited in o's OWN ID ORDER rather than by ranging its map,
	// which matters for more than tidiness. Group ids are assigned by first
	// appearance and Finish emits in id order, so the serial path produces
	// first-appearance ordering as a side effect; ranging a Go map here would make
	// the merged ordering vary from run to run, turning a deterministic output into
	// a random one for every query that aggregates without MaintainOrder.
	oKeys := make([]string, len(o.ids))
	for k, oid := range o.ids {
		oKeys[oid] = k
	}
	remap := make([]int32, len(o.ids))
	var newRows []int32 // o's row index, for keys this sink has never seen
	for oid, k := range oKeys {
		id, seen := s.ids[k]
		if !seen {
			id = int32(len(s.ids))
			s.ids[k] = id
			newRows = append(newRows, int32(oid))
		}
		remap[oid] = id
	}

	// The key VALUES of the groups only o saw. This is the piece joinBuildSink.Merge
	// does not need: a join's per-key state is a row count, so its keys never have
	// to be materialised again. Here Finish concatenates keyParts and pairs the
	// result positionally with one aggregate row per group, so a group with no key
	// row shifts every key after it.
	if len(newRows) > 0 && len(s.keys) > 0 {
		keys, err := kernel.Concat(o.keySchema, o.keyParts)
		if err != nil {
			return err
		}
		cols := make([]*data.Column, keys.NumCols())
		for i := range cols {
			taken, err := kernel.Take(keys.Column(i), newRows)
			if err != nil {
				return err
			}
			cols[i] = taken.Rename(s.keySchema.Field(i).Name)
		}
		part, err := data.NewBatch(s.keySchema, cols)
		if err != nil {
			return err
		}
		s.keyParts = append(s.keyParts, part)
		s.mem.Retain(part)
	}

	for i := range s.accs {
		if err := s.accs[i].Merge(o.accs[i], remap); err != nil {
			return err
		}
	}
	s.nRows += o.nRows

	// Re-measure the accumulator state as a delta, exactly as Consume does. The
	// merge grew both the group count and the per-group state, and without this the
	// reported peak omits everything the fold pulled in.
	var acc int64
	for _, a := range s.accs {
		acc += a.NBytes()
	}
	acc += int64(cap(s.firstSeen)) * 8
	s.mem.RetainBytes(acc - s.accBytes)
	s.accBytes = acc
	return nil
}

// aggWorkers decides how many sinks a group-by gets, and is the whole safety
// argument for running one on several threads.
//
// It returns 1 — the serial breaker, byte-identical to every step before this one —
// unless BOTH gates pass. Each is a property that was checked against the code
// rather than assumed, and each declines rather than approximates.
//
// # Order
//
// Sink.Merge's contract is that `other` consumed a LATER portion of the input, and
// parallelSink's round-robin dispatcher does not satisfy it: worker 0 holds batches
// 0, N, 2N…, so worker 1's batch 1 precedes worker 0's batch N. So the aggregate
// may only run in parallel when its answer does not depend on order at all.
//
//	MaintainOrder      firstSeen holds per-sink input ordinals, and two sinks that
//	                   saw interleaved portions numbered their rows independently.
//	First/Last         name a position outright.
//	ArgMin/ArgMax      report an INDEX, and their Merge shifts by the row count of
//	                   a portion it assumes came earlier.
//
// The whole aggregate is declined rather than the individual expression, because
// one Agg list is one hash table.
//
// # Memory
//
// hashAggSink refuses to Merge once it has frozen or spilled, and N workers
// splitting one budget makes spilling likelier, not less likely — so a limit would
// turn a slow query into a failed one. Steps 10 and 12 built the serial spilling
// path and this keeps it: under a limit, nothing changes.
//
// The trade that leaves, stated plainly: without a limit, peak memory rises to
// roughly N times the group table, because each worker builds its own. That is the
// exchange being made, and the memory gate is what stops it being made behind a
// user's back.
func aggWorkers(a *plan.Aggregate, specs []aggSpec, opts Options) int {
	if opts.Threads <= 1 || a.MaintainOrder {
		return 1
	}
	if opts.Budget.Limit() > 0 {
		return 1
	}
	for _, spec := range specs {
		if spec.op.IsOrderDependent() {
			return 1
		}
	}
	return opts.Threads
}

// aggBreaker builds n sinks and wraps them in the matching driver.
//
// n == 1 returns the plain breaker rather than a one-worker parallelSink, so the
// serial path keeps exactly the operator tree it had — no goroutine, no channel,
// and nothing to explain when a profile is read.
func aggBreaker(child Operator, newSink SinkFactory, n int) (Operator, error) {
	sinks := make([]Sink, n)
	for i := range n {
		s, err := newSink()
		if err != nil {
			return nil, err
		}
		sinks[i] = s
	}
	if n == 1 {
		return &breaker{child: child, sink: sinks[0]}, nil
	}
	return newParallelSink(child, sinks), nil
}

// chunk is the output batch size, defaulting when the sink was built by hand.
func (s *hashAggSink) chunk() int {
	if s.batch <= 0 {
		return DefaultOptions().BatchSize
	}
	return s.batch
}

func (s *hashAggSink) Finish(ctx context.Context) (Operator, error) {
	if err := s.closeParts(); err != nil {
		return nil, err
	}
	resident, err := s.residentResult()
	if err != nil {
		return nil, err
	}

	s.releaseState()

	if len(s.files) == 0 {
		s.mem.Retain(resident)
		if resident.Rows() <= s.chunk() {
			// Byte-identical to before for every query that did not spill — including
			// the two empty-batch corners, since batchOperator emits a zero-row batch
			// when it is the only one and runOperator would swallow it.
			return newBatchOperator(s.schema, resident), nil
		}
		return &runOperator{schema: s.schema,
			src: &batchRun{b: resident, n: s.chunk()}}, nil
	}
	if s.ordered {
		return s.finishOrdered(ctx, resident)
	}
	s.mem.Retain(resident)
	return &aggSpillOp{schema: s.schema, parent: s, head: resident,
		n: s.chunk(), files: s.files}, nil
}

// releaseState drops the sink's claim on everything residentResult was built from.
//
// # Rebasing, and why it is not optional
//
// The key parts and the accumulator state are both dead the moment the answer
// exists, and holding all three would double-count the aggregation at exactly its
// largest moment. joinBuildSink.freeze makes the same correction for the same
// reason.
//
// # And why a SPILLING sink must go further than that
//
// A sub-sink draws on the same query-wide budget as its parent. If the parent still
// held its resident state — or its answer — while the replay ran, every sub-sink
// would start already over budget, freeze after its very first batch, and route
// almost everything one level deeper. The recursion would then need a level per
// batch rather than a level per factor of 16, and hit maxSpillDepth on data that is
// not skewed at all. That is a budget bug, not a skew one, and it is invisible from
// the error message.
func (s *hashAggSink) releaseState() {
	s.keyParts, s.accBytes = nil, 0
	s.mem.Release()
}

// residentResult builds the answer for the groups this sink holds in memory.
func (s *hashAggSink) residentResult() (*data.Batch, error) {
	nGroups := len(s.ids)

	// A GLOBAL aggregate emits exactly one row even over an empty input:
	// `count(*)` of nothing is 0, not no rows. A hash table gets this wrong by
	// default, because no row ever created a group.
	if len(s.keys) == 0 && nGroups == 0 {
		nGroups = 1
	}

	cols := make([]*data.Column, 0, len(s.keys)+len(s.specs))

	if len(s.keys) > 0 {
		if len(s.keyParts) == 0 {
			// No row ever arrived, so no group was created. Concat over zero batches
			// yields a zero-COLUMN batch, which would leave the output short of its
			// schema; the correct answer is one empty column per key.
			cols = append(cols, emptyColumns(s.keySchema)...)
		} else {
			keys, err := kernel.Concat(s.keySchema, s.keyParts)
			if err != nil {
				return nil, err
			}
			cols = append(cols, keys.Columns()...)
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

	return data.NewBatch(s.schema, cols)
}

// --- planning -----------------------------------------------------------------

// extractAggs rewrites an aggregate expression so every inner Agg is replaced by a
// reference to a temporary column, collecting the specs as it goes.
//
// Identical aggregates share one temporary, so `sum(a) / sum(a)` computes one sum.
func extractAggs(n expr.Node, in *dtype.Schema, specs *[]aggSpec, byKey map[string]string) (expr.Node, error) {
	switch t := n.(type) {
	case *expr.Agg:
		key := t.String()
		name, seen := byKey[key]
		if !seen {
			cf, err := t.Child.Field(in)
			if err != nil {
				return nil, err
			}
			bind, err := expr.ResolveAggBinding(t.Op, cf.Type)
			if err != nil {
				return nil, err
			}
			name = "__agg" + strconv.Itoa(len(*specs))
			byKey[key] = name
			*specs = append(*specs, aggSpec{
				op: t.Op, input: t.Child, name: name, bind: bind, params: t.Params,
			})
		}
		return &expr.Col{Name: name}, nil

	case *expr.Col, *expr.Lit, *expr.Err, *expr.Match:
		return n, nil

	case *expr.Binary:
		l, err := extractAggs(t.L, in, specs, byKey)
		if err != nil {
			return nil, err
		}
		r, err := extractAggs(t.R, in, specs, byKey)
		if err != nil {
			return nil, err
		}
		return &expr.Binary{Op: t.Op, L: l, R: r}, nil

	case *expr.Unary:
		c, err := extractAggs(t.Child, in, specs, byKey)
		if err != nil {
			return nil, err
		}
		// Rebuild rather than a hand-written composite literal: expr.Rebuild copies
		// every non-child field, so a node type that grows a parameter cannot lose it
		// here. Three sites used to rebuild a Unary by hand, and each would have
		// silently dropped such a field — which is why Round's decimal count went into
		// a Call rather than onto Unary.
		return expr.Rebuild(t, []expr.Node{c}), nil

	case *expr.Alias:
		c, err := extractAggs(t.Child, in, specs, byKey)
		if err != nil {
			return nil, err
		}
		return &expr.Alias{Child: c, Name: t.Name}, nil

	case *expr.Rename:
		c, err := extractAggs(t.Child, in, specs, byKey)
		if err != nil {
			return nil, err
		}
		return &expr.Rename{Child: c, Fn: t.Fn, Label: t.Label}, nil

	case *expr.Cast:
		c, err := extractAggs(t.Child, in, specs, byKey)
		if err != nil {
			return nil, err
		}
		return &expr.Cast{Child: c, To: t.To, Strict: t.Strict}, nil

	case *expr.Call:
		args := make([]expr.Node, len(t.Args))
		for i, a := range t.Args {
			r, err := extractAggs(a, in, specs, byKey)
			if err != nil {
				return nil, err
			}
			args[i] = r
		}
		return &expr.Call{Fn: t.Fn, Args: args}, nil

	case *expr.Cond:
		// Conditional aggregation — Agg(When(c).Then(Col("x").Sum()).Otherwise(0)) —
		// which is how a pivot is written. Each branch is descended independently,
		// so an aggregate in any of the three becomes its own temporary and the
		// conditional is rebuilt over the references.
		pred, err := extractAggs(t.Pred, in, specs, byKey)
		if err != nil {
			return nil, err
		}
		then, err := extractAggs(t.Then, in, specs, byKey)
		if err != nil {
			return nil, err
		}
		els, err := extractAggs(t.Else, in, specs, byKey)
		if err != nil {
			return nil, err
		}
		return &expr.Cond{Pred: pred, Then: then, Else: els}, nil

	default:
		return nil, uerr.Internalf("physical: extractAggs: unhandled node %T", n)
	}
}

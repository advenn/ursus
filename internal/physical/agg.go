package physical

import (
	"context"
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
// Past the memory budget the sink freezes its `ids` table. From that point a row
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

	ids     groupKeys
	accs    []kernel.Accumulator
	keyCh   keyChunk // Consume's scratch
	intKeys []int64  // Consume's scratch: a narrower integer key, widened

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

	spills []groupSpill // where each accumulator's values went past the budget, if any

	dir      string // spill directory; only the level-0 sink removes it
	dirOwner bool
	prefix   string        // filename prefix, derived from the parent's file
	parts    []*partWriter // one per radix bucket, opened lazily
	files    []string      // closed partition files, in bucket order
	pend     [][]int32     // scratch: this batch's rows bound for each bucket
	keep     []int32       // scratch: a frozen sink's rows of this batch that stay resident

	// --- MaintainOrder --------------------------------------------------------
	ordered   bool
	ord       string  // the ordinal column's name in spillSchema
	firstSeen []int64 // input ordinal of each resident group's first row
	nRows     int64   // rows consumed, the ordinal source at level 0

	// parallel is set on each of several workers: past the budget the sink asks the
	// driver to go serial instead of freezing (step 91).
	parallel bool
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
			ids:       newGroupKeys(keySchema),
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
			s.ord = ordinalName(in)
			f := append(in.FieldSlice(), dtype.NotNull(s.ord, dtype.Int64))
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
// renameOp gives a batch's columns the schema's names and types, in order, and drops
// the last drop columns, which the schema does not have.
type renameOp struct {
	schema *dtype.Schema
	drop   int
}

func (r *renameOp) Schema() *dtype.Schema { return r.schema }
func (r *renameOp) Close() error          { return nil }

func (r *renameOp) Apply(_ context.Context, in *data.Batch) (*data.Batch, error) {
	if in.NumCols() != r.schema.Len()+r.drop {
		return nil, uerr.Internalf("physical: rename got %d columns, want %d",
			in.NumCols(), r.schema.Len()+r.drop)
	}
	if in.NumCols() == 0 {
		// GroupBy().Agg() with nothing to aggregate: the global row, which has no
		// columns to count it by, so its height is carried, not derived.
		return data.NewBatchRows(r.schema, nil, in.Rows()), nil
	}
	cols := make([]*data.Column, r.schema.Len())
	for i := range cols {
		f := r.schema.Field(i)
		cols[i] = in.Column(i).Rename(f.Name).WithDType(f.Type)
	}
	return data.NewBatch(r.schema, cols)
}

// The Sink's Consume and its key paths are aggconsume.go; its Merge, the workers
// and the partitioned fold aggmerge.go.

func (s *hashAggSink) Finish(ctx context.Context) (Operator, error) {
	if err := s.closeParts(); err != nil {
		return nil, err
	}
	resident, err := s.residentResult(ctx)
	if err != nil {
		return nil, err
	}

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

// releaseState drops the sink's state, and its account's claim on it.
// residentResult calls it before it builds the answer, from the pieces it took.
//
// # Rebasing, and why it is not optional
//
// The key parts and the accumulator state are both dead once the answer is built
// from them, and holding all three would double-count the aggregation at exactly
// its largest moment. joinBuildSink.freeze makes the same correction for the same
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
	// The accumulators and the key table go with the account's claim on them. Kept,
	// they stayed alive until the query ended, uncounted: a median or an implode
	// holds every value of every group. firstSeen stays, for finishOrdered.
	s.keyParts, s.accBytes = nil, 0
	s.ids, s.accs = s.ids.empty(), nil
	s.mem.Release()
}

// residentResult builds the answer for the groups this sink holds in memory. It
// takes the sink's state, releases it with releaseState, and lets go of each piece
// as soon as the answer has what it needed from it. A caller closes the sink's
// partition files first: closeParts uncharges their buffers.
//
// # Why as it goes
//
// With one group per row, as h2o's gb10 has, the answer is about the input's size,
// and so is each thing it is built from: the key table, which holds every key
// encoded, and the key parts. All three were alive until the last key column was
// copied, the parts twice over, and none of the copy was charged. A six-key group-by
// of two million rows held 517 MB to assemble a 159 MB answer, against the 375 MB it
// charged, and gb10 reached a 3 GB container's cap (step 131).
//
//   - The key table answers lookups, and the answer needs none: it goes first, once
//     the group count is read.
//   - The key parts go a column at a time, with kernel.ConcatOwned, as Collect's
//     batches and a join's build side do (step 128).
//   - Each accumulator goes once its column is finished.
//
// # Why the account is released first
//
// The ledger's keys are pointers into the buffers it counts, so an account keeps
// alive what it holds. Released after the answer was built, as it used to be, the
// account kept every key part until then, whatever ConcatOwned let go of.
// finishOrdered reads firstSeen, which stays.
func (s *hashAggSink) residentResult(ctx context.Context) (*data.Batch, error) {
	setSpillsCtx(ctx, s.spills)
	nGroups := s.ids.Len()
	keyParts, accs := s.keyParts, s.accs
	s.releaseState()

	// A GLOBAL aggregate emits exactly one row even over an empty input:
	// `count(*)` of nothing is 0, not no rows. A hash table gets this wrong by
	// default, because no row ever created a group.
	if len(s.keys) == 0 && nGroups == 0 {
		nGroups = 1
	}

	cols := make([]*data.Column, 0, len(s.keys)+len(s.specs))

	if len(s.keys) > 0 {
		if len(keyParts) == 0 {
			// No row ever arrived, so no group was created. Concat over zero batches
			// yields a zero-COLUMN batch, which would leave the output short of its
			// schema; the correct answer is one empty column per key.
			cols = append(cols, emptyColumns(s.keySchema)...)
		} else {
			keys, err := kernel.ConcatOwned(s.keySchema, keyParts)
			if err != nil {
				return nil, err
			}
			cols = append(cols, keys.Columns()...)
		}
	}

	for i, spec := range s.specs {
		accs[i].Reserve(nGroups)
		c, err := accs[i].Finish(spec.name, nGroups)
		accs[i] = nil
		if err != nil {
			return nil, err
		}
		cols = append(cols, c)
		if AggColumnDone != nil {
			AggColumnDone(i)
		}
	}

	if len(cols) == 0 {
		// A global aggregate of nothing is still its one row, as Agg's doc promises;
		// NewBatch derives the height from the columns, and with none it said 0.
		return data.NewBatchRows(s.schema, nil, nGroups), nil
	}
	return data.NewBatch(s.schema, cols)
}

// AggColumnDone, when set, is called as residentResult has made aggregate i's column
// and let go of its accumulator. It is for internal/memcheck, which collects garbage
// there to read that each accumulator goes as its column is made (step 432). Nothing
// else sets it.
var AggColumnDone func(i int)

// --- planning -----------------------------------------------------------------

// extractAggs rewrites an aggregate expression so every inner Agg is replaced by a
// reference to a temporary column, collecting the specs as it goes.
//
// Identical aggregates share one temporary, so `sum(a) / sum(a)` computes one sum.
// Identical by expr.Identity: the rendering, plus each literal's type and each
// udf's id. By the rendering alone, `(i8 + int8(100)).sum()` and
// `(i8 + int64(100)).sum()` were one aggregate, and `(x*1).max()` and
// `(x*1.0).max()` were one with two types, which was ErrInternal. This function has
// two instantiations, one here per plan.Aggregate and one in tempgroup.go per
// plan.TemporalGroup, so it is two of the four dedup maps.
func extractAggs(n expr.Node, in *dtype.Schema, specs *[]aggSpec, byKey map[string]string) (expr.Node, error) {
	switch t := n.(type) {
	case *expr.Agg:
		key := expr.Identity(t)
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
		return expr.Rebuild(t, []expr.Node{c}), nil

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

	case *expr.UDF:
		// A UDF wrapping an aggregate — Agg(Col("x").Sum().MapElements(...)) — or an
		// aggregate over a UDF — Agg(Col("x").MapElements(...).Sum()). Descending
		// handles both: the inner aggregate becomes a temporary either way, and the
		// UDF is rebuilt over the reference to run as a post-aggregation projection.
		//
		// Copying the struct rather than listing fields keeps Impl, Out, Nullable,
		// Name and Kind — the user's declarations — carried across unchanged.
		c, err := extractAggs(t.Child, in, specs, byKey)
		if err != nil {
			return nil, err
		}
		u := *t
		u.Child = c
		return &u, nil

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

	case *expr.ListMap:
		// A list's eval over an aggregate, implode(x).list.eval(...): each aggregate
		// below is a temporary, and the ListMap is rebuilt over them. Its body is not
		// a child: an aggregate there is a list's.
		kids := n.Children()
		out := make([]expr.Node, len(kids))
		for i, k := range kids {
			r, err := extractAggs(k, in, specs, byKey)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return expr.Rebuild(n, out), nil

	default:
		return nil, uerr.Internalf("physical: extractAggs: unhandled node %T", n)
	}
}

// aggInput is an aggregate's input over src, with each of its rows' group: under
// Expr.Filter, only the rows every predicate is true on, a null dropping its row as
// the frame's Filter drops it (step 251). The group-by, the windows and the
// temporal groups all feed their accumulators through it.
func aggInput(ctx context.Context, input expr.Node, src *data.Batch, gids []int32) (*data.Column, []int32, error) {
	inner, preds := expr.FilterChain(input)
	col, err := evalColumn(ctx, inner, src)
	if err != nil || len(preds) == 0 {
		return col, gids, err
	}
	sel, err := passingRows(ctx, preds, src)
	if err != nil {
		return nil, nil, err
	}
	if len(sel) == src.Rows() {
		return col, gids, nil
	}
	if col, err = kernel.Take(col, sel); err != nil {
		return nil, nil, err
	}
	kept := make([]int32, len(sel))
	for j, r := range sel {
		kept[j] = gids[r]
	}
	return col, kept, nil
}

// passingRows is the rows of src, in order, that every predicate is true on.
func passingRows(ctx context.Context, preds []expr.Node, src *data.Batch) ([]int32, error) {
	var sel []int32
	for i, p := range preds {
		mask, err := evalColumn(ctx, p, src)
		if err != nil {
			return nil, err
		}
		kept, err := kernel.SelectionFromMask(mask)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			sel = kept
			continue
		}
		both := sel[:0:0]
		for a, b := 0, 0; a < len(sel) && b < len(kept); {
			switch {
			case sel[a] < kept[b]:
				a++
			case sel[a] > kept[b]:
				b++
			default:
				both = append(both, sel[a])
				a, b = a+1, b+1
			}
		}
		sel = both
	}
	return sel, nil
}

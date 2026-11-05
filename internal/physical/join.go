package physical

import (
	"context"
	"errors"
	"io"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
)

// ProbeBuilder is the BUILD half of a two-input pipeline breaker.
//
// It is a Sink — the whole Consume/Merge/Finish contract, unchanged — plus one
// method. Sink is deliberately not widened: a one-input breaker genuinely has one
// child, and making every sink carry a second would be the cost without the
// benefit.
//
// Finish and Probe are two views of one result:
//
//	Finish(ctx)  ==  Probe(ctx, an operator that yields no rows)
//
// That is not a stub. It is the join of the build side against an empty probe
// side: zero rows for Inner/Left/Semi/Anti/Cross, and every build row
// null-padded for Right/Full. Keeping Finish total rather than
// panic("use Probe") keeps Sink's contract honest and gives the suite a free
// equivalence — see TestJoinFinishEqualsProbeOfEmpty.
type ProbeBuilder interface {
	Sink

	// Probe returns the operator that streams the probe side and emits the join.
	// Called ONCE, after the last Consume, INSTEAD of Finish.
	//
	// OWNERSHIP: the returned Operator does NOT own probe. joinBreaker owns both
	// children and closes both; the returned operator's Close releases only its
	// own state. An operator that also closed probe would double-close it.
	Probe(ctx context.Context, probe Operator) (Operator, error)
}

// joinBreaker is breaker's two-input sibling.
//
// breaker cuts a linear pipeline: consume everything, Finish, delegate. A join
// cuts a DAG — the build side must reach EOF before the probe side is pulled at
// all, and then the probe side streams. That asymmetry has to live somewhere, and
// here is better than a `children []Operator` where index 0 secretly means "drain
// me" and index 1 means "stream me".
type joinBreaker struct {
	build   Operator
	probe   Operator
	builder ProbeBuilder
	out     Operator
	done    bool

	// swap is the join when it may exchange its inputs at runtime, and swapOpts the
	// options to plan the exchanged join under (joinswap.go); nil otherwise.
	swap     *plan.Join
	swapOpts Options

	// ordered: the join keeps an order, Polars' maintain_order (step 314). One that
	// fits keeps it as it goes; one whose table spilled sorts its rows back into it
	// (orderedProbe), and opts is what that sort is planned under.
	ordered bool
	opts    Options
}

func (j *joinBreaker) Schema() *dtype.Schema { return j.builder.Schema() }

func (j *joinBreaker) Next(ctx context.Context) (*data.Batch, error) {
	if !j.done {
		if err := j.drain(ctx); err != nil {
			return nil, err
		}
		j.done = true
	}
	return j.out.Next(ctx)
}

func (j *joinBreaker) drain(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		in, err := j.build.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if err := j.builder.Consume(ctx, in); err != nil {
			return err
		}
	}
	if j.swap != nil {
		out, err := j.swapIfSmaller(ctx)
		if err != nil {
			return err
		}
		if out != nil {
			j.out = out
			return nil
		}
	}
	if s, ok := j.builder.(*joinBuildSink); ok && j.ordered && s.wroteBuild() && s.crossW == nil {
		out, err := s.orderedProbe(ctx, j.probe, j.opts)
		if err != nil {
			return err
		}
		j.out = out
		return nil
	}
	out, err := j.builder.Probe(ctx, j.probe)
	if err != nil {
		return err
	}
	j.out = out
	return nil
}

// Close releases all three, unconditionally.
//
// Unlike breaker's `out` — a batchOperator with no child — this `out` WRAPS the
// probe side. If it also closed the probe, the probe would be closed twice.
// Sources are idempotent about that today, but relying on every future source
// staying idempotent is how a double-close ships. One flat rule, no conditional:
// joinBreaker owns all three, `out` owns nothing.
func (j *joinBreaker) Close() error {
	var errs []error
	if j.out != nil {
		errs = append(errs, j.out.Close())
	}
	return errors.Join(append(errs,
		j.builder.Close(), j.build.Close(), j.probe.Close())...)
}

// --- the table ------------------------------------------------------------------

// seenBytesPerKey is what one entry in the probe side's map[string]struct{} costs
// beyond the key bytes: a 16-byte string header, a tophash byte, and Go's
// load-factor slack, which keeps a map roughly a third empty.
//
// An estimate, and deliberately so. Measuring exactly would mean walking every key
// on every batch, which is the trade nuniqueAcc.NBytes documents making in the other
// direction. Being approximately right is the point; being ZERO — which is what this
// was until step 13 — is the bug.
//
// It used to cover the build side's ids map as well, at 48 bytes for the extra
// 4-byte value. That map is now a kernel.KeyTable, which owns four slices and
// reports NBytes EXACTLY, so no estimate is involved on that side any more.
const seenBytesPerKey = 44

// noKey marks a build row whose key contained a null while NullsEqual is off. It
// is in no bucket, so it matches nothing — and Right/Full still emit it unmatched.
const noKey = int32(-1)

// joinTable is what the build side produces and the probe side reads.
//
// # Why CSR and not map[string][]int32
//
// A join key repeats where a group-by key does not, so the table maps a key to a
// LIST of build rows. A map to a slice costs a slice header plus a backing array
// per distinct key: at 5M keys that is ~120 MB of headers, 5M allocations, and 5M
// pointers for the GC to trace on every cycle.
//
// Compressed sparse row instead: two flat, pointer-free slices scattered once at
// freeze. A probe is one map lookup and then a CONTIGUOUS slice, already in
// ascending build order — no chain to walk, no reversal pass.
//
// This doc used to add "it also makes the Validate check off[id+1]-off[id] > 1,
// which is O(1) per key". THAT CHECK DOES NOT EXIST and never did: right-side
// uniqueness is checked incrementally in Consume, where a duplicate is a map hit,
// and it has to be — the CSR is not built until freeze, which is after the last
// build row. A justification for a data structure should name a use the code makes.
//
// Nothing mutates it after freeze, which is what would let a future morsel
// scheduler share one table across N probe workers with no lock.
type joinTable struct {
	ids   joinKeys             // encoded key -> key id
	ints  *kernel.IntKeyParts  // integer key -> key id, in place of ids (step 159)
	words *kernel.WordKeyParts // packed integer keys -> key id, in place of ids (step 391)
	pack  *kernel.KeyPacker    // and their packer
	off   []int32              // len nKeys+1; rows[off[id]:off[id+1]] are id's build rows
	rows  []int32              // build row indices, ascending within each key
	nKeys int

	// build holds every build row, addressable by one index. nil for Semi/Anti,
	// which never emit a right column.
	build *data.Batch

	// rowKey[j] is build row j's key id, or noKey. Kept only for Right/Full, whose
	// flush walks build rows in INPUT order.
	rowKey []int32
}

// joinKeys is the probe's view of the build side's encoded keys: one KeyTable, from
// the streaming build, or several read as one, from the partitioned build (step 151).
// A partitioned build of one integer key leaves joinTable.ints instead (step 159).
type joinKeys interface {
	GetMany(keys [][]byte, ids []int32, found []bool)
	NBytes() int64
}

// --- the spec -------------------------------------------------------------------

// joinSpec is the per-kind behaviour reduced to flags, so one probe loop serves
// all seven kinds.
//
// needBuildRows's "their build memory is O(keys)" was FALSE by an O(rows) term
// until step 13: rowKey was appended for every build row and counts for every key,
// and Semi/Anti read neither — freeze hands rowKey to the table only when
// emitBuildUnmatched, and consumes counts only when needBuildRows. Four bytes a row
// of charged, retained, unread state. See tracksRows.
//
// Read the flags as identities: Right is Inner plus a build-unmatched flush, and
// Full is Left plus the same flush. That is why there is no input swap — swapping
// would put the LEFT input on the build side, so a right join of a large fact
// table against a small dimension would silently buffer the large one.
type joinSpec struct {
	kind       plan.JoinKind
	nullsEqual bool
	validate   plan.JoinValidation
	batchSize  int

	// swapped: the join runs with its inputs exchanged, its right input probing a
	// table of its left, for a right order (step 314). kind and validate are the
	// exchanged join's; a merged key is still read from the side the plan's join
	// reads it from, and an error names the plan's sides.
	swapped bool

	emitProbeUnmatched bool // Left, Full, Anti
	emitBuildUnmatched bool // Right, Full
	needBuildRows      bool // false for Semi/Anti, UNLESS they carry a residual
	maskOnly           bool // Semi/Anti emit probe columns only, at most one per row
	emitMatched        bool // false for Anti

	// residual is evaluated per candidate PAIR before the match verdict, and is
	// what makes a semi join answer "does a partner satisfying p exist" rather than
	// "does a partner exist".
	//
	// It is why needBuildRows is no longer a pure function of the kind. Reading a
	// right-hand value requires the rows to be there, so a residual semi join
	// retains its build side and its memory becomes O(build rows) like every other
	// kind's. That is a real cost and it is the price of the feature: without the
	// rows there is nothing for the predicate to look at.
	residual []expr.Node
}

// tracksRows reports whether this kind needs the per-build-row rowKey array.
//
// Only Right and Full read it — flushStep walks it — and only the needBuildRows
// kinds read counts, which freeze prefix-sums into the CSR. Semi and Anti are
// neither, so they used to build BOTH for every build row and discard them unread,
// which is what made needBuildRows's doc ("their build memory is O(keys)") false by
// an O(rows) term. The bytes were charged, so they also decided when the budget
// fired for exactly the two kinds whose memory is smallest.
func (s joinSpec) tracksRows() bool { return s.needBuildRows || s.emitBuildUnmatched }

func newJoinSpec(j *plan.Join, batchSize int) joinSpec {
	k := j.Kind
	filters := k == plan.JoinSemi || k == plan.JoinAnti
	return joinSpec{
		kind:               k,
		nullsEqual:         j.NullsEqual,
		validate:           j.Validate,
		batchSize:          batchSize,
		emitProbeUnmatched: k == plan.JoinLeft || k == plan.JoinFull || k == plan.JoinAnti,
		emitBuildUnmatched: k == plan.JoinRight || k == plan.JoinFull,
		needBuildRows:      !filters || j.HasResidual(),
		maskOnly:           filters,
		emitMatched:        k != plan.JoinAnti,
		residual:           j.Residual,
	}
}

// The build side is joinsink.go; the probe side is joinprobe.go (the loop),
// joinmatch.go (a row's matches) and joinemit.go (the output).

// --- helpers ----------------------------------------------------------------------

// emptyOperator yields no rows. It is what Finish probes against.
type emptyOperator struct{ schema *dtype.Schema }

func (e emptyOperator) Schema() *dtype.Schema                     { return e.schema }
func (e emptyOperator) Next(context.Context) (*data.Batch, error) { return nil, io.EOF }
func (e emptyOperator) Close() error                              { return nil }

// emptyBatch builds a zero-row batch with REAL payload buffers.
//
// Not emptyColumns: that uses data.NewNull, which carries no payload, and this
// batch is gathered from on every unmatched row.
func emptyBatch(s *dtype.Schema) (*data.Batch, error) {
	cols := make([]*data.Column, s.Len())
	for i, f := range s.All() {
		c, err := kernel.NullColumn(f.Name, f.Type, 0)
		if err != nil {
			return nil, err
		}
		cols[i] = c
	}
	return data.NewBatch(s, cols)
}

// evalKeys evaluates the key expressions and casts each to its comparison type.
//
// The cast is not optional. Parquet-style Int32 and Int64 keys encode to 4 and 8
// bytes with different sign biasing, so an uncast pair NEVER matches and the join
// silently returns zero rows — the worst kind of wrong answer. The plan layer has
// already proven a common type exists; this applies it.
func evalKeys(ctx context.Context, op string, keys []expr.Node, in *data.Batch, types []dtype.DataType) ([]*data.Column, error) {
	out := make([]*data.Column, len(keys))
	for i, k := range keys {
		c, err := evalColumn(ctx, k, in)
		if err != nil {
			return nil, err
		}
		if i < len(types) && c.DType() != types[i] {
			if c, err = castKey(op, c, types[i]); err != nil {
				return nil, err
			}
		}
		out[i] = c
	}
	return out, nil
}

// castKey casts a key column to the type the two sides are compared in, STRICTLY.
//
// It was non-strict, and a value the promoted type cannot hold — a Datetime(ms) in
// the year 3000, past int64 nanoseconds — became a NULL key. A null key matches
// nothing, so a left join showed the row's key as null; under NullsEqual it MATCHED
// another side's null; and on a non-nullable key column it was ErrInternal. A key
// that does not survive promotion is a value error about that value, as the as-of
// join's own key has been since it learned the same lesson. PromoteExact leaves a
// temporal unit as the one way this can fail: an integer widening cannot.
func castKey(op string, c *data.Column, to dtype.DataType) (*data.Column, error) {
	cast, err := kernel.Cast(c.Name(), to, true, c)
	if err != nil {
		// Wrapped, because a bare "cast: ..." never mentions the join that asked for
		// it, and its own hint — use a non-strict cast — is advice this caller cannot
		// take. The cause keeps the row and the value.
		return nil, uerr.Wrap(err, uerr.KindValue, op,
			"the key %q does not fit %s, the type the two sides are compared in",
			c.Name(), to).
			Hint("both sides of a join cast each key to one promoted type").
			Hint("give the two sides the same key type, or narrow the one whose " +
				"range is too wide, before joining")
	}
	return cast, nil
}

// keyValidity reports, per row, whether EVERY key column is valid.
//
// SQL's rule is that a key containing ANY null matches nothing — not "all null".
// bitmap.AndMulti is exactly that fold, and it is O(n/64) through the Words
// iterator, so honouring NullsEqual costs one pass and one branch per row rather
// than a second encoder.
func keyValidity(cols []*data.Column) bitmap.View {
	vs := make([]bitmap.View, len(cols))
	for i, c := range cols {
		vs[i] = c.Validity()
	}
	return bitmap.AndMulti(vs...)
}

func duplicateKeyErr(spec joinSpec, build bool, keys []expr.Node, row int) error {
	v, side := spec.validate, "left"
	if build {
		side = "right"
	}
	if spec.swapped {
		// The table is the plan's left input (step 314).
		v = swappedValidation(v)
		side = map[string]string{"left": "right", "right": "left"}[side]
	}
	e := uerr.New(uerr.KindValue, "join",
		"validate=%s requires unique keys on the %s side, but a key is duplicated",
		v, side)
	if row >= 0 {
		e.Hint("first duplicate at %s row %d", side, row)
	}
	if len(keys) > 0 {
		e.Hint("key: %s", expr.StringAll(keys))
	}
	e.Hint("each duplicate multiplies every matching row on the other side")
	e.Hint("de-duplicate with .Unique(...), or pass JoinValidate(ursus.ValidateManyToMany)")
	return e
}

// --- planning -----------------------------------------------------------------------

// planJoin lowers a plan.Join.
//
// The RIGHT side builds. Which input is the right one is the planner's choice
// (build_side, from the sources' row estimates), except where it marks the join
// SwapAtRuntime: then a deferred build that finds its probe side the smaller by
// half exchanges the two, by their actual rows (joinswap.go, step 162).
func planJoin(ctx context.Context, j *plan.Join, opts Options) (Operator, error) {
	left, err := planPipelined(ctx, j.Left, opts)
	if err != nil {
		return nil, err
	}
	right, err := planPipelined(ctx, j.Right, opts)
	if err != nil {
		return nil, err
	}

	layout, err := j.Layout()
	if err != nil {
		return nil, err
	}
	ls, err := j.Left.Schema()
	if err != nil {
		return nil, err
	}
	rs, err := j.Right.Schema()
	if err != nil {
		return nil, err
	}

	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultOptions().BatchSize
	}

	// The residual's namespace, computed once here rather than per probe row. Nil
	// when there is no residual, which is every join but a WhereExists.
	var pairLayout *plan.JoinLayout
	if j.HasResidual() {
		if pairLayout, err = j.PairLayout(); err != nil {
			return nil, err
		}
	}

	// A right order (step 314): the right input probes a table of the left, so the
	// rows come in its order, each one's matches in the left's, and a left or full
	// join's unmatched left rows after them, from the table's flush.
	run, probe, build := j, left, right
	if j.Order.Rightward() && j.Kind != plan.JoinSemi && j.Kind != plan.JoinAnti {
		c := *j
		c.Left, c.Right, c.LeftOn, c.RightOn = j.Right, j.Left, j.RightOn, j.LeftOn
		c.Kind, c.Validate = swappedKind(j.Kind), swappedValidation(j.Validate)
		run, probe, build = &c, right, left
		ls, rs = rs, ls
		layout = swappedLayout(layout)
	}
	spec := newJoinSpec(run, batchSize)
	spec.swapped = run != j

	// An ordered join that flushes its table's unmatched rows numbers them, so that
	// one whose table spills can flush them in their order (orderedProbe).
	var buildOrd string
	if j.Order != plan.JoinOrderNone && spec.emitBuildUnmatched {
		build, rs, buildOrd, err = withOrdinal(build, rs, append(ls.Names(), layout.Schema.Names()...), "build")
		if err != nil {
			return nil, err
		}
	}

	sink := &joinBuildSink{
		out:        layout.Schema,
		layout:     layout,
		left:       ls,
		right:      rs,
		keys:       run.RightOn,
		leftKeys:   run.LeftOn,
		spec:       spec,
		threads:    opts.Threads,
		deferred:   deferBuild(run, opts),
		pairLayout: pairLayout,
		publish:    j.Publish,
		ids:        kernel.NewKeyTable(),
		mem:        opts.Budget.Account("join"),

		budget:   opts.Budget,
		bparts:   make([]*partWriter, nBuckets),
		bfiles:   make([]string, nBuckets),
		pend:     make([][]int32, nBuckets),
		buildOrd: buildOrd,
	}
	if sink.intKeyed() {
		sink.ints = kernel.NewIntKeyTable()
	}
	br := &joinBreaker{build: build, probe: probe, builder: sink}
	if j.SwapAtRuntime && sink.deferred {
		br.swap, br.swapOpts = j, opts
	}
	if j.Order != plan.JoinOrderNone {
		br.ordered, br.opts = true, opts
	}
	return br, nil
}

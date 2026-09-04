package kernel

import (
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// Accumulator computes one aggregate across all groups.
//
// # Vectorized, not per-group
//
// There is ONE Accumulator per aggregate expression, holding per-group state in a
// slice indexed by group ordinal — not one object per group per aggregate. With a
// million groups the latter would mean a million interface values and a virtual
// call per row; here the call is per BATCH per aggregate.
//
// # Merge is not decoration
//
// Merge combines the state of two accumulators that saw disjoint subsets of the
// same input. It is what makes PARALLEL aggregation possible in v0.3: N workers
// accumulate independently, then merge. It is implemented and TESTED now, even
// though nothing calls it concurrently yet, because a Merge written later against
// forgotten invariants is a Merge that is wrong.
//
// Spilling aggregation does NOT use it, and that is a design choice rather than an
// omission. Step 12 partitions by key rather than evicting groups, so one sink sees
// every row of a group and Merge is never reached — which is what makes First,
// Last, ArgMin and ArgMax correct on a replayed partition with no position shift.
// A design that DID merge partial groups would need each accumulator's `other` to
// have seen a strictly later portion of the input, a precondition eviction cannot
// supply; argExtremumAcc.Merge carries an explicit index shift for exactly that
// reason. See hashAggSink for the invariant.
type Accumulator interface {
	// Reserve ensures per-group state exists for n groups.
	Reserve(n int)

	// AddBatch accumulates col into the groups named by groups, where groups[i] is
	// the group ordinal of row i.
	AddBatch(groups []int32, col *data.Column) error

	// Merge folds another accumulator's state into this one. Both must be for the
	// same aggregate.
	//
	// remap carries the group NUMBERING: remap[i] is this accumulator's group id
	// for other's group i, and nil means the two already agree. Two sinks that
	// consumed different portions of the input assign ids independently — the
	// first key each one sees becomes its group 0 — so without a remap the
	// positional fold below adds one group's total to another, silently and with
	// no length mismatch to catch it. That is the piece parallel aggregation
	// needs; see physical.hashAggSink.Merge for where the mapping is built.
	Merge(other Accumulator, remap []int32) error

	// Finish emits the result, one row per group.
	Finish(name string, nGroups int) (*data.Column, error)

	// NBytes reports the per-group state currently held, so a group-by can be
	// measured against a memory budget. Implementations must be O(groups): it is
	// called once per batch.
	NBytes() int64
}

// NewAccumulator builds the accumulator for an aggregate over a given input type.
//
// The binding comes from expr.ResolveAggBinding and is OBEYED, not re-derived —
// the same discipline the evaluator follows for binary operators, and for the same
// reason: a second copy of the type rules drifts, and the drift shows up as a
// column whose type does not match the schema the planner promised.
// params carries the aggregate's configuration — ddof, the quantile — and is the
// zero value for every aggregate that takes none.
func NewAccumulator(op expr.AggOp, in dtype.DataType, bind expr.AggBinding,
	params expr.AggParams) (Accumulator, error) {

	switch op {
	case expr.AggCount:
		return &countAcc{mode: countValues}, nil
	case expr.AggLen:
		return &countAcc{mode: countRows}, nil
	case expr.AggNullCount:
		return &countAcc{mode: countNulls}, nil
	case expr.AggNUnique:
		return &nuniqueAcc{}, nil

	case expr.AggFirst:
		return &positionAcc{out: bind.Out, last: false}, nil
	case expr.AggLast:
		return &positionAcc{out: bind.Out, last: true}, nil

	case expr.AggSum:
		return newSumAcc(in, bind)
	case expr.AggMean:
		return newMeanAcc(in, bind)
	case expr.AggMin:
		return newExtremum(in, bind.Out, false)
	case expr.AggMax:
		return newExtremum(in, bind.Out, true)

	case expr.AggAny, expr.AggAllTrue:
		return newBoolExtremum(op), nil

	case expr.AggVar:
		return &varAcc{ddof: params.DDof, std: false}, nil
	case expr.AggStd:
		return &varAcc{ddof: params.DDof, std: true}, nil

	case expr.AggProduct:
		return &productAcc{}, nil

	case expr.AggArgMin:
		return &argExtremumAcc{max: false}, nil
	case expr.AggArgMax:
		return &argExtremumAcc{max: true}, nil

	case expr.AggMedian:
		// The median IS the 0.5 quantile, linearly interpolated. Giving it its own
		// accumulator would mean two implementations that must agree on the
		// even-length case forever.
		return &quantileAcc{q: 0.5, interp: expr.InterpLinear}, nil
	case expr.AggQuantile:
		return &quantileAcc{q: params.Q, interp: params.Interp}, nil

	default:
		return nil, uerr.New(uerr.KindUnsupported, "agg",
			"aggregate %s is not implemented", op)
	}
}

// --- merging under a remap ----------------------------------------------------
//
// Ten accumulators share one loop shape: for each of other's groups, find the
// destination ordinal and fold. Writing the nil-remap branch ten times is how ten
// slightly different nil handlings get written, so it is written once here.

// mergeCap returns the number of groups the destination must hold to receive a
// merge of n source groups through remap.
//
// Under the identity it is just n. Under a remap it is one past the largest
// destination, which can exceed n: a merge that introduces keys the destination
// has never seen grows it.
func mergeCap(remap []int32, n int) int {
	if remap == nil {
		return n
	}
	n = min(n, len(remap))
	most := 0
	for _, d := range remap[:n] {
		most = max(most, int(d)+1)
	}
	return most
}

// mergeEach calls fn(dst, src) for every source group.
//
// n is bounded by len(remap) as well as by the caller's count, because Reserve
// over-allocates: an accumulator's slices are usually longer than the number of
// groups actually assigned, and the remap is sized to the latter.
func mergeEach(remap []int32, n int, fn func(dst, src int)) {
	if remap == nil {
		for i := range n {
			fn(i, i)
		}
		return
	}
	for i := range min(n, len(remap)) {
		fn(int(remap[i]), i)
	}
}

// --- counting ----------------------------------------------------------------

// countMode selects which rows countAcc counts.
type countMode uint8

const (
	countValues countMode = iota // Count: non-null values
	countRows                    // Len: every row
	countNulls                   // NullCount: nulls only
)

// countAcc implements Count (non-null values), Len (rows) and NullCount.
//
// The first two are SQL's COUNT(col) and COUNT(*), and they must never be aliased:
// over a column with nulls they give different answers, and which one a user meant
// is not recoverable after the fact. NullCount is their difference, and counting it
// directly rather than subtracting keeps all three on one code path.
type countAcc struct {
	mode countMode
	n    []uint64
}

func (a *countAcc) Reserve(n int) {
	for len(a.n) < n {
		a.n = append(a.n, 0)
	}
}

func (a *countAcc) AddBatch(groups []int32, col *data.Column) error {
	valid := col.Validity()
	// The mode is hoisted out of the loop: it is fixed for the accumulator's
	// lifetime, and this is the innermost loop in the engine's cheapest aggregate.
	switch a.mode {
	case countRows:
		for _, g := range groups {
			a.n[g]++
		}
	case countValues:
		for i, g := range groups {
			if valid.Get(i) {
				a.n[g]++
			}
		}
	default: // countNulls
		for i, g := range groups {
			if !valid.Get(i) {
				a.n[g]++
			}
		}
	}
	return nil
}

func (a *countAcc) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*countAcc)
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into countAcc", other)
	}
	// Three aggregates now share this type, so identical Go types no longer imply
	// the same aggregate. Merging a null count into a row count would produce a
	// number with no meaning and no symptom.
	if a.mode != o.mode {
		return uerr.Internalf("kernel: cannot merge countAcc mode %d into mode %d",
			o.mode, a.mode)
	}
	a.Reserve(mergeCap(remap, len(o.n)))
	mergeEach(remap, len(o.n), func(dst, src int) { a.n[dst] += o.n[src] })
	return nil
}

// Finish is never null: an empty or all-null group still has a well-defined count
// of zero. This is the one aggregate family with no validity bitmap.
func (a *countAcc) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	return data.NewFixed(name, dtype.Uint64, a.n[:nGroups], bitmap.AllSet(nGroups)), nil
}

// --- n_unique ----------------------------------------------------------------

// nuniqueAcc counts DISTINCT non-null values per group.
//
// Nulls are SKIPPED, so n_unique([null,null]) is 0. Polars counts null as a
// distinct value and returns 1; every other engine, and every other aggregate in
// ursus, skips nulls. Internal consistency wins: an n_unique that treated nulls
// differently from sum, mean, min, max and count would be a permanent trap.
type nuniqueAcc struct {
	sets []map[string]struct{}
}

func (a *nuniqueAcc) Reserve(n int) {
	for len(a.sets) < n {
		a.sets = append(a.sets, nil)
	}
}

func (a *nuniqueAcc) AddBatch(groups []int32, col *data.Column) error {
	// Reuse the group-key encoder, so distinctness uses ursus's grouping equality:
	// NaN equals NaN and -0.0 equals +0.0. Using IEEE equality here would make
	// n_unique disagree with group_by on the same data.
	enc, err := NewGroupKeyEncoder("n_unique", []*data.Column{col})
	if err != nil {
		return err
	}
	valid := col.Validity()
	for i, g := range groups {
		if !valid.Get(i) {
			continue
		}
		if a.sets[g] == nil {
			a.sets[g] = make(map[string]struct{}, 4)
		}
		a.sets[g][string(enc.Encode(i))] = struct{}{}
	}
	return nil
}

func (a *nuniqueAcc) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*nuniqueAcc)
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into nuniqueAcc", other)
	}
	a.Reserve(mergeCap(remap, len(o.sets)))
	mergeEach(remap, len(o.sets), func(dst, src int) {
		s := o.sets[src]
		if s == nil {
			return
		}
		if a.sets[dst] == nil {
			a.sets[dst] = make(map[string]struct{}, len(s))
		}
		for k := range s {
			a.sets[dst][k] = struct{}{}
		}
	})
	return nil
}

func (a *nuniqueAcc) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	out := make([]uint64, nGroups)
	for i := range nGroups {
		out[i] = uint64(len(a.sets[i]))
	}
	return data.NewFixed(name, dtype.Uint64, out, bitmap.AllSet(nGroups)), nil
}

// --- sum ---------------------------------------------------------------------

// sumAcc accumulates integer sums at 128 bits and float sums at 64.
//
// `seen` tracks whether a group ever received a non-null value. Without it, sum of
// an all-null group would be the accumulator's zero, which is wrong: ursus follows
// SQL and returns NULL. The distinction is between "the values summed to zero" and
// "there was nothing to sum", and once it is collapsed to 0 it cannot be recovered.
type sumAcc struct {
	in    dtype.DataType
	bind  expr.AggBinding
	isInt bool

	i    []i128.Int128
	f    []float64
	seen []bool
}

func newSumAcc(in dtype.DataType, bind expr.AggBinding) (Accumulator, error) {
	return &sumAcc{in: in, bind: bind, isInt: bind.Acc.ID() == dtype.TypeInt128}, nil
}

func (a *sumAcc) Reserve(n int) {
	for len(a.seen) < n {
		a.seen = append(a.seen, false)
		if a.isInt {
			a.i = append(a.i, i128.Zero)
		} else {
			a.f = append(a.f, 0)
		}
	}
}

func (a *sumAcc) AddBatch(groups []int32, col *data.Column) error {
	valid := col.Validity()

	if a.isInt {
		add := func(g int32, v i128.Int128) {
			a.i[g] = a.i[g].Add(v)
			a.seen[g] = true
		}
		switch {
		case col.DType().IsUnsignedInteger():
			src, err := widenUnsigned(col)
			if err != nil {
				return err
			}
			for i, g := range groups {
				if valid.Get(i) {
					add(g, i128.FromUint64(src[i]))
				}
			}
		case col.DType().Physical().ID() == dtype.TypeInt128:
			src, err := data.Values[i128.Int128](col)
			if err != nil {
				return err
			}
			for i, g := range groups {
				if valid.Get(i) {
					add(g, src[i])
				}
			}
		default:
			src, err := widenSigned(col)
			if err != nil {
				return err
			}
			for i, g := range groups {
				if valid.Get(i) {
					add(g, i128.FromInt64(src[i]))
				}
			}
		}
		return nil
	}

	src, err := widenFloat(col)
	if err != nil {
		return err
	}
	for i, g := range groups {
		if valid.Get(i) {
			a.f[g] += src[i]
			a.seen[g] = true
		}
	}
	return nil
}

func (a *sumAcc) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*sumAcc)
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into sumAcc", other)
	}
	a.Reserve(mergeCap(remap, len(o.seen)))
	mergeEach(remap, len(o.seen), func(dst, src int) {
		if !o.seen[src] {
			return
		}
		if a.isInt {
			a.i[dst] = a.i[dst].Add(o.i[src])
		} else {
			a.f[dst] += o.f[src]
		}
		a.seen[dst] = true
	})
	return nil
}

func (a *sumAcc) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	valid := seenBitmap(a.seen, nGroups)

	if a.isInt {
		return data.NewFixed(name, a.bind.Out, a.i[:nGroups], valid), nil
	}
	// Duration sums accumulate as float64 but output as an integer tick count.
	if a.bind.Out.ID() == dtype.TypeDuration {
		out := make([]int64, nGroups)
		for i := range nGroups {
			out[i] = int64(a.f[i])
		}
		return data.NewFixed(name, a.bind.Out, out, valid), nil
	}
	if a.bind.Out == dtype.Float32 {
		out := make([]float32, nGroups)
		for i := range nGroups {
			out[i] = float32(a.f[i])
		}
		return data.NewFixed(name, a.bind.Out, out, valid), nil
	}
	return data.NewFixed(name, a.bind.Out, a.f[:nGroups], valid), nil
}

// --- mean --------------------------------------------------------------------

// meanAcc divides the sum of non-null values by the COUNT of non-null values.
//
// Dividing by the group size instead would silently bias every mean downward
// whenever nulls are present, and the bias is invisible without a reference
// implementation to compare against.
//
// Mean of an all-null group is NULL, not NaN. 0/0 is the one place where ursus's
// null-versus-NaN distinction must not be allowed to blur: NaN would claim the
// mean was computed and came out undefined, when in fact there was nothing to
// compute.
type meanAcc struct {
	bind expr.AggBinding
	sum  []float64
	n    []uint64
}

func newMeanAcc(_ dtype.DataType, bind expr.AggBinding) (Accumulator, error) {
	return &meanAcc{bind: bind}, nil
}

func (a *meanAcc) Reserve(n int) {
	for len(a.n) < n {
		a.sum = append(a.sum, 0)
		a.n = append(a.n, 0)
	}
}

func (a *meanAcc) AddBatch(groups []int32, col *data.Column) error {
	src, err := widenFloat(col)
	if err != nil {
		return err
	}
	valid := col.Validity()
	for i, g := range groups {
		if valid.Get(i) {
			a.sum[g] += src[i]
			a.n[g]++
		}
	}
	return nil
}

func (a *meanAcc) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*meanAcc)
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into meanAcc", other)
	}
	// Sum and count fold independently, which is what makes the mean mergeable at
	// all: averaging two averages would weight the smaller group equally.
	a.Reserve(mergeCap(remap, len(o.n)))
	mergeEach(remap, len(o.n), func(dst, src int) {
		a.sum[dst] += o.sum[src]
		a.n[dst] += o.n[src]
	})
	return nil
}

func (a *meanAcc) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	vb := bitmap.NewBuilder(nGroups)
	out := make([]float64, nGroups)
	for i := range nGroups {
		if a.n[i] == 0 {
			vb.Append(false) // NULL, not NaN
			continue
		}
		out[i] = a.sum[i] / float64(a.n[i])
		vb.Append(true)
	}
	valid := vb.Finish()

	switch a.bind.Out {
	case dtype.Float32:
		f32 := make([]float32, nGroups)
		for i := range nGroups {
			f32[i] = float32(out[i])
		}
		return data.NewFixed(name, a.bind.Out, f32, valid), nil
	}
	if a.bind.Out.ID() == dtype.TypeDuration {
		i64 := make([]int64, nGroups)
		for i := range nGroups {
			i64[i] = int64(out[i])
		}
		return data.NewFixed(name, a.bind.Out, i64, valid), nil
	}
	return data.NewFixed(name, a.bind.Out, out, valid), nil
}

// --- min / max ---------------------------------------------------------------

// Min and Max keep the WINNING VALUE per group, in a flat typed slice.
//
// # Why four types rather than one
//
// This used to be a single implementation holding a representative single-row
// *data.Column per group, which served every type at once. The cost of that
// generality was not small: one heap-allocated Column per group, a
// map[int32]int per batch, and a Take plus a concatColumn PER GROUP PER BATCH.
// h2o gb7 — max and min over 100,000 groups — spent 93 seconds there against
// polars' 1.2, and it was 35% of every object the engine allocated.
//
// sumAcc, thirty lines above, has always used flat typed slices. These four do
// the same, at the price of one type per payload shape:
//
//	extremumNum[T]  every fixed-width numeric, and the temporal types, whose
//	                Physical() is Int32 or Int64 — the same punning data.Values
//	                already permits
//	extremumStr     String and Binary
//	extremumI128    Int128, and therefore Decimal
//	extremumBool    Bool, whose payload is a bitmap rather than a values buffer,
//	                and which is also how Any and AllTrue are implemented
//
// # The identity-value trap is still avoided, differently
//
// The step-1 docs record it: a SIMD implementation must use IfElse(valid,
// identity) with +Inf for Min and -Inf for Max, because the obvious `Masked`
// zero-fills and would make Min([3,null,5]) return 0. The old code sidestepped
// it by holding a row rather than a value. These hold a value — so the guard is
// `seen`, a per-group bool that is false until a NON-NULL value arrives. An
// all-null group never sets it and seenBitmap turns it into a null.
//
// # Comparison is the TOTAL order, not <
//
// Floats go through OrderKeyF64/F32, so NaN is one canonical bucket above
// everything and -0.0 equals +0.0 — the order order.go documents and Sort uses,
// which is what makes `min(x)` and `sort(x).first()` agree. Using `<` here would
// make max over a column containing NaN return a number.

// lessOrdered is the comparison for integers, where Go's < is already the total
// order. Floats do NOT use it; see lessFloat64.
func lessOrdered[T data.Primitive](a, b T) bool { return a < b }

func lessFloat64(a, b float64) bool { return OrderKeyF64(a) < OrderKeyF64(b) }
func lessFloat32(a, b float32) bool { return OrderKeyF32(a) < OrderKeyF32(b) }

// newExtremum builds the accumulator for Min or Max over in, publishing out.
//
// out is bind.Out rather than the physical type, which is what keeps the max of
// a Datetime a Datetime, of a Decimal a Decimal and of an Enum an Enum.
func newExtremum(in, out dtype.DataType, max bool) (Accumulator, error) {
	if in.ID() == dtype.TypeBool {
		return &extremumBool{out: out, max: max}, nil
	}
	if in.HasStringStorage() || in.ID() == dtype.TypeBinary {
		return &extremumStr{out: out, max: max}, nil
	}
	switch in.Physical().ID() {
	case dtype.TypeInt8:
		return &extremumNum[int8]{out: out, max: max, less: lessOrdered[int8]}, nil
	case dtype.TypeInt16:
		return &extremumNum[int16]{out: out, max: max, less: lessOrdered[int16]}, nil
	case dtype.TypeInt32:
		return &extremumNum[int32]{out: out, max: max, less: lessOrdered[int32]}, nil
	case dtype.TypeInt64:
		return &extremumNum[int64]{out: out, max: max, less: lessOrdered[int64]}, nil
	case dtype.TypeUint8:
		return &extremumNum[uint8]{out: out, max: max, less: lessOrdered[uint8]}, nil
	case dtype.TypeUint16:
		return &extremumNum[uint16]{out: out, max: max, less: lessOrdered[uint16]}, nil
	case dtype.TypeUint32:
		return &extremumNum[uint32]{out: out, max: max, less: lessOrdered[uint32]}, nil
	case dtype.TypeUint64:
		return &extremumNum[uint64]{out: out, max: max, less: lessOrdered[uint64]}, nil
	case dtype.TypeFloat32:
		return &extremumNum[float32]{out: out, max: max, less: lessFloat32}, nil
	case dtype.TypeFloat64:
		return &extremumNum[float64]{out: out, max: max, less: lessFloat64}, nil
	case dtype.TypeInt128:
		return &extremumI128{out: out, max: max}, nil
	default:
		return nil, uerr.New(uerr.KindUnsupported, "agg",
			"cannot take a minimum or maximum of a %s column", in)
	}
}

// --- fixed-width numerics, and the temporal types ------------------------------

type extremumNum[T data.Primitive] struct {
	out  dtype.DataType
	max  bool
	less func(a, b T) bool

	best []T
	seen []bool
}

func (a *extremumNum[T]) Reserve(n int) {
	for len(a.seen) < n {
		var zero T
		a.best = append(a.best, zero)
		a.seen = append(a.seen, false)
	}
}

// wins reports whether x should replace the current best.
func (a *extremumNum[T]) wins(x, cur T) bool {
	if a.max {
		return a.less(cur, x)
	}
	return a.less(x, cur)
}

func (a *extremumNum[T]) AddBatch(groups []int32, col *data.Column) error {
	// Once per batch, not once per group: this is the whole point of the rewrite.
	v, err := data.Values[T](col)
	if err != nil {
		return err
	}
	valid := col.Validity()
	for i, g := range groups {
		if !valid.Get(i) {
			continue // nulls are skipped, as in every other aggregate
		}
		if !a.seen[g] {
			a.best[g], a.seen[g] = v[i], true
			continue
		}
		if a.wins(v[i], a.best[g]) {
			a.best[g] = v[i]
		}
	}
	return nil
}

func (a *extremumNum[T]) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*extremumNum[T])
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into %T", other, a)
	}
	a.Reserve(mergeCap(remap, len(o.seen)))
	mergeEach(remap, len(o.seen), func(dst, src int) {
		if !o.seen[src] {
			return
		}
		if !a.seen[dst] {
			a.best[dst], a.seen[dst] = o.best[src], true
			return
		}
		if a.wins(o.best[src], a.best[dst]) {
			a.best[dst] = o.best[src]
		}
	})
	return nil
}

func (a *extremumNum[T]) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	return data.NewFixed(name, a.out, a.best[:nGroups], seenBitmap(a.seen, nGroups)), nil
}

func (a *extremumNum[T]) NBytes() int64 {
	w := int64(a.out.Physical().BitWidth() / 8)
	return int64(len(a.best))*w + int64(len(a.seen))
}

// --- strings and binary --------------------------------------------------------

type extremumStr struct {
	out  dtype.DataType
	max  bool
	best []string
	seen []bool
}

func (a *extremumStr) Reserve(n int) {
	for len(a.seen) < n {
		a.best = append(a.best, "")
		a.seen = append(a.seen, false)
	}
}

func (a *extremumStr) wins(x, cur string) bool {
	if a.max {
		return cur < x
	}
	return x < cur
}

func (a *extremumStr) AddBatch(groups []int32, col *data.Column) error {
	acc := col.Strings()
	valid := col.Validity()
	for i, g := range groups {
		if !valid.Get(i) {
			continue
		}
		// acc.Get ALIASES the batch's character buffer — its doc says so. Comparing
		// against the alias is free; STORING it would pin that buffer for the whole
		// aggregation, so every retained batch would be held alive by one winning
		// string. Clone only on the store, which happens at most once per group per
		// batch and usually far less.
		x := acc.Get(i)
		if !a.seen[g] {
			a.best[g], a.seen[g] = strings.Clone(x), true
			continue
		}
		if a.wins(x, a.best[g]) {
			a.best[g] = strings.Clone(x)
		}
	}
	return nil
}

func (a *extremumStr) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*extremumStr)
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into extremumStr", other)
	}
	a.Reserve(mergeCap(remap, len(o.seen)))
	mergeEach(remap, len(o.seen), func(dst, src int) {
		if !o.seen[src] {
			return
		}
		// o.best is already cloned, so no second copy is needed here.
		if !a.seen[dst] {
			a.best[dst], a.seen[dst] = o.best[src], true
			return
		}
		if a.wins(o.best[src], a.best[dst]) {
			a.best[dst] = o.best[src]
		}
	})
	return nil
}

func (a *extremumStr) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	col := data.NewString(name, a.best[:nGroups], seenBitmap(a.seen, nGroups))
	return col.WithDType(a.out), nil
}

func (a *extremumStr) NBytes() int64 {
	n := int64(len(a.best))*16 + int64(len(a.seen)) // string headers plus seen
	for _, s := range a.best {
		n += int64(len(s))
	}
	return n
}

// --- Int128, and therefore Decimal ---------------------------------------------

type extremumI128 struct {
	out  dtype.DataType
	max  bool
	best []i128.Int128
	seen []bool
}

func (a *extremumI128) Reserve(n int) {
	for len(a.seen) < n {
		a.best = append(a.best, i128.Zero)
		a.seen = append(a.seen, false)
	}
}

func (a *extremumI128) wins(x, cur i128.Int128) bool {
	if a.max {
		return cur.Cmp(x) < 0
	}
	return x.Cmp(cur) < 0
}

func (a *extremumI128) AddBatch(groups []int32, col *data.Column) error {
	v, err := data.Values[i128.Int128](col)
	if err != nil {
		return err
	}
	valid := col.Validity()
	for i, g := range groups {
		if !valid.Get(i) {
			continue
		}
		if !a.seen[g] {
			a.best[g], a.seen[g] = v[i], true
			continue
		}
		if a.wins(v[i], a.best[g]) {
			a.best[g] = v[i]
		}
	}
	return nil
}

func (a *extremumI128) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*extremumI128)
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into extremumI128", other)
	}
	a.Reserve(mergeCap(remap, len(o.seen)))
	mergeEach(remap, len(o.seen), func(dst, src int) {
		if !o.seen[src] {
			return
		}
		if !a.seen[dst] {
			a.best[dst], a.seen[dst] = o.best[src], true
			return
		}
		if a.wins(o.best[src], a.best[dst]) {
			a.best[dst] = o.best[src]
		}
	})
	return nil
}

func (a *extremumI128) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	return data.NewFixed(name, a.out, a.best[:nGroups], seenBitmap(a.seen, nGroups)), nil
}

func (a *extremumI128) NBytes() int64 {
	return int64(len(a.best))*16 + int64(len(a.seen))
}

// --- Bool, which is also Any and AllTrue ---------------------------------------

type extremumBool struct {
	out  dtype.DataType
	max  bool
	best []bool
	seen []bool
}

func (a *extremumBool) Reserve(n int) {
	for len(a.seen) < n {
		a.best = append(a.best, false)
		a.seen = append(a.seen, false)
	}
}

func (a *extremumBool) AddBatch(groups []int32, col *data.Column) error {
	bits := col.Bools()
	valid := col.Validity()
	for i, g := range groups {
		if !valid.Get(i) {
			continue
		}
		x := bits.Get(i)
		if !a.seen[g] {
			a.best[g], a.seen[g] = x, true
			continue
		}
		// false < true, so max saturates at true and min at false.
		if a.max && x && !a.best[g] {
			a.best[g] = true
		} else if !a.max && !x && a.best[g] {
			a.best[g] = false
		}
	}
	return nil
}

func (a *extremumBool) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*extremumBool)
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into extremumBool", other)
	}
	a.Reserve(mergeCap(remap, len(o.seen)))
	mergeEach(remap, len(o.seen), func(dst, src int) {
		if !o.seen[src] {
			return
		}
		if !a.seen[dst] {
			a.best[dst], a.seen[dst] = o.best[src], true
			return
		}
		if a.max {
			a.best[dst] = a.best[dst] || o.best[src]
		} else {
			a.best[dst] = a.best[dst] && o.best[src]
		}
	})
	return nil
}

func (a *extremumBool) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	vb := bitmap.NewBuilder(nGroups)
	for i := range nGroups {
		vb.Append(a.best[i])
	}
	return data.NewBool(name, vb.Finish(), seenBitmap(a.seen, nGroups)), nil
}

func (a *extremumBool) NBytes() int64 { return int64(len(a.best)) + int64(len(a.seen)) }

// --- first / last -------------------------------------------------------------

// positionAcc implements First and Last.
//
// POSITIONAL: it returns the first (or last) value in the group, which may itself
// be null. First is a selection, not a reduction — skipping nulls would mean
// First(a) and First(b) could come from different rows, which is precisely what
// people use them together for. DuckDB and Polars both agree on this default.
type positionAcc struct {
	out  dtype.DataType
	last bool
	rows []*data.Column
}

func (a *positionAcc) Reserve(n int) {
	for len(a.rows) < n {
		a.rows = append(a.rows, nil)
	}
}

func (a *positionAcc) AddBatch(groups []int32, col *data.Column) error {
	pick := map[int32]int{}
	for i, g := range groups {
		if _, seen := pick[g]; !seen || a.last {
			pick[g] = i
		}
	}
	for g, row := range pick {
		if a.rows[g] != nil && !a.last {
			continue // First: the earliest batch wins
		}
		c, err := Take(col, []int32{int32(row)})
		if err != nil {
			return err
		}
		a.rows[g] = c
	}
	return nil
}

func (a *positionAcc) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*positionAcc)
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into positionAcc", other)
	}
	a.Reserve(mergeCap(remap, len(o.rows)))
	mergeEach(remap, len(o.rows), func(dst, src int) {
		c := o.rows[src]
		if c == nil {
			return
		}
		// Merge assumes `other` saw a LATER slice of the input than this one. First
		// keeps what it has; Last takes the newer value.
		//
		// That assumption is why First and Last are ORDER-DEPENDENT and why the
		// parallel aggregation driver refuses to use it: its dispatcher is
		// round-robin, so worker 1's second batch precedes worker 0's Nth and
		// neither sink holds a contiguous portion. See expr.AggOp.IsOrderDependent
		// and physical.aggCanParallelise.
		if a.rows[dst] == nil || a.last {
			a.rows[dst] = c
		}
	})
	return nil
}

func (a *positionAcc) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	return assembleRows(name, a.out, a.rows[:nGroups])
}

// --- helpers -----------------------------------------------------------------

// assembleRows stitches one single-row column per group into one column, with a
// null where a group produced nothing.
func assembleRows(name string, out dtype.DataType, rows []*data.Column) (*data.Column, error) {
	n := len(rows)
	if n == 0 {
		return data.NewNull(name, out, 0), nil
	}

	parts := make([]*data.Column, 0, n)
	vb := bitmap.NewBuilder(n)
	for _, c := range rows {
		if c == nil {
			// NullColumn, not data.NewNull: this placeholder is about to be
			// concatenated, and a payload-free column cannot be read. With
			// data.NewNull here, `GroupBy(g).Agg(Col("v").Min())` failed with
			// "column has no fixed-width payload" for any group whose values were
			// all null — a plain query, reported as if it were an ursus bug.
			pad, err := NullColumn(name, out, 1)
			if err != nil {
				return nil, err
			}
			parts = append(parts, pad)
			vb.Append(false)
			continue
		}
		parts = append(parts, c)
		vb.Append(c.IsValid(0))
	}

	col, err := concatColumn(parts, n)
	if err != nil {
		return nil, err
	}
	return col.Rename(name).WithDType(out), nil
}

// seenBitmap turns per-group presence flags into a validity bitmap, dropping the
// buffer entirely when every group saw a value.
func seenBitmap(seen []bool, n int) bitmap.View {
	all := true
	vb := bitmap.NewBuilder(n)
	for i := range n {
		vb.Append(seen[i])
		all = all && seen[i]
	}
	if all {
		return bitmap.AllSet(n)
	}
	return vb.Finish()
}

func widenSigned(c *data.Column) ([]int64, error) { return widenToInt64(c) }

func widenUnsigned(c *data.Column) ([]uint64, error) { return widenToUint64(c) }

func widenFloat(c *data.Column) ([]float64, error) {
	if c.DType().Physical().ID() == dtype.TypeInt128 {
		src, err := data.Values[i128.Int128](c)
		if err != nil {
			return nil, err
		}
		out := make([]float64, len(src))
		for i, v := range src {
			out[i] = v.Float64()
		}
		return out, nil
	}
	return toFloat64(c)
}

// --- accounting ----------------------------------------------------------------
//
// NBytes reports what an accumulator is holding, so a group-by can be measured
// against a memory budget.
//
// It is on the interface rather than a type switch in the physical layer because
// per-group state is where an aggregation's memory actually goes, and an
// accumulator added later with an unbounded shape — quantile keeps every value —
// would otherwise be silently unaccounted. Every implementation below is O(groups)
// so it can be called once per batch.

// colBytes sums the payload of a per-group column slice, skipping the empty slots
// Reserve leaves behind.
func colBytes(cs []*data.Column) int64 {
	n := int64(len(cs)) * 8 // the slice of pointers
	for _, c := range cs {
		if c != nil {
			n += c.NBytes()
		}
	}
	return n
}

func (a *countAcc) NBytes() int64 { return int64(len(a.n)) * 8 }

// NBytes for n_unique approximates the map contents: 8 bytes per group for the
// slice, plus a per-entry constant covering the string header and the bucket slot.
// The key BYTES are not counted, so a group-by over long strings under-reports;
// counting them exactly would mean walking every key on every batch.
func (a *nuniqueAcc) NBytes() int64 {
	n := int64(len(a.sets)) * 8
	for _, s := range a.sets {
		n += int64(len(s)) * 48
	}
	return n
}

func (a *sumAcc) NBytes() int64 {
	return int64(len(a.i))*16 + int64(len(a.f))*8 + int64(len(a.seen))
}

func (a *meanAcc) NBytes() int64 { return int64(len(a.sum))*8 + int64(len(a.n))*8 }

func (a *positionAcc) NBytes() int64 { return colBytes(a.rows) }

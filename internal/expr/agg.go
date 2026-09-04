package expr

import (
	"strconv"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// AggOp is an aggregate function.
type AggOp uint8

const (
	AggSum AggOp = iota
	AggMean
	AggMin
	AggMax
	AggCount   // non-null values
	AggLen     // rows, including nulls
	AggNUnique // distinct non-null values
	AggFirst
	AggLast

	// APPEND ONLY below this line. IsCounting and the classifiers are written
	// against declaration order, and an op inserted mid-enum silently changes its
	// neighbours' family — the same rule CallFn and UnaryOp document.
	AggNullCount // nulls, the complement of Count
	AggAny       // Boolean OR over non-null values
	AggAllTrue   // Boolean AND over non-null values
	AggVar
	AggStd
	AggProduct
	AggArgMin // index of the minimum WITHIN the group
	AggArgMax
	AggMedian
	AggQuantile

	aggOpCount
)

var aggOpNames = [aggOpCount]string{
	AggSum: "sum", AggMean: "mean", AggMin: "min", AggMax: "max",
	AggCount: "count", AggLen: "len", AggNUnique: "n_unique",
	AggFirst: "first", AggLast: "last",
	AggNullCount: "null_count", AggAny: "any", AggAllTrue: "all_true",
	AggVar: "var", AggStd: "std", AggProduct: "product",
	AggArgMin: "arg_min", AggArgMax: "arg_max",
	AggMedian: "median", AggQuantile: "quantile",
}

func (o AggOp) String() string {
	// The `!= ""` half is the load-bearing one. aggOpNames is sized by the enum's
	// sentinel, so `int(o) < len(...)` is true for every DECLARED constant and this
	// fallback would be unreachable without it — a forgotten entry would render as
	// the EMPTY STRING, and two ops both missing names would render identically and
	// collide in the three maps that dedup on String(). dtype.TypeID.String() has
	// had the correct guard since step 1; these eight did not.
	if int(o) < len(aggOpNames) && aggOpNames[o] != "" {
		return aggOpNames[o]
	}
	return "?"
}

// IsCounting reports whether the aggregate returns a count, and is therefore
// never null and independent of the input type.
//
// ArgMin and ArgMax return an index rather than a count and are deliberately NOT
// here: a group with no non-null value has no minimum, so there is no position to
// report and the answer is null.
func (o AggOp) IsCounting() bool {
	return o == AggCount || o == AggLen || o == AggNUnique || o == AggNullCount
}

// IsOrderDependent reports whether the aggregate's ANSWER depends on the order its
// input arrived in.
//
// Every other aggregate is commutative: a sum, a count, a minimum and a quantile
// are the same however the rows are shuffled, which is what lets a group-by run on
// several workers and fold the partials together. These four are not.
//
//	First, Last     name a position outright
//	ArgMin, ArgMax  report an INDEX, and kernel.argExtremumAcc.Merge shifts the
//	                other accumulator's positions by this one's row count — which
//	                is only meaningful if `other` saw a strictly later contiguous
//	                portion
//
// The parallel aggregation driver dispatches round-robin, so no worker holds a
// contiguous portion and none of those four can be honoured. It declines the whole
// aggregate rather than the individual expression, because one query's Agg list is
// one hash table.
//
// Not the same question as IsCounting: Median and Quantile buffer every value and
// are expensive, but they sort in Finish and are perfectly order-insensitive.
func (o AggOp) IsOrderDependent() bool {
	return o == AggFirst || o == AggLast || o == AggArgMin || o == AggArgMax
}

// AggParams carries an aggregate's configuration — ddof for Var and Std, the
// quantile and its interpolation for Quantile. The zero value is correct for every
// aggregate that takes no parameters.
//
// # Why not Args []Node, the way Call does it
//
// Call holds its arguments as ordinary child nodes, and its doc explains that a
// column-valued argument "is representable in this IR and simply not implemented
// yet, which is the right way round for a constraint that may lift later". That is
// true of a regex pattern.
//
// It is not true of these. A ddof or a quantile is CONFIGURATION, not an operand:
// there is no meaning to a per-row ddof, and there never will be. Making them
// children would put nodes into Children() that RootNames, expansion and projection
// pushdown then walk for nothing.
type AggParams struct {
	DDof   uint8
	Q      float64
	Interp Interpolation
}

// args renders the parameters op actually reads. See Agg.String for why this is
// load-bearing.
func (p AggParams) args(op AggOp) string {
	switch op {
	case AggVar, AggStd:
		return strconv.FormatUint(uint64(p.DDof), 10)
	case AggQuantile:
		return strconv.FormatFloat(p.Q, 'g', -1, 64) + ", " + p.Interp.String()
	default:
		return ""
	}
}

// Agg applies an aggregate function.
//
// # Where an Agg is legal
//
// Only inside an aggregation context: GroupBy().Agg(...), or (later) a window via
// .Over(). Its defining property is that it consumes many rows and produces one,
// so it cannot appear in Select or WithColumns, where every expression must
// preserve the frame height. plan.Resolve enforces that — an Agg reaching a
// Project is rejected with an explanation rather than silently producing a
// one-row column that then fails to line up.
type Agg struct {
	Op     AggOp
	Child  Node
	Params AggParams
}

func (a *Agg) node()            {}
func (a *Agg) Children() []Node { return []Node{a.Child} }

// String renders the aggregate INCLUDING its parameters, and that is a correctness
// requirement rather than a display choice.
//
// physical.extractAggs deduplicates inner aggregates by keying a map on this
// string, so two aggregates that render identically become ONE temporary. Before
// the parameters were rendered, Agg(x.Quantile(0.5), x.Quantile(0.99)) produced
// "x.quantile()" twice, collapsed into a single computation, and returned the
// median under both names with no error anywhere.
func (a *Agg) String() string {
	return a.Child.String() + "." + a.Op.String() + "(" + a.Params.args(a.Op) + ")"
}

func (a *Agg) Field(in *dtype.Schema) (dtype.Field, error) {
	cf, err := a.Child.Field(in)
	if err != nil {
		return dtype.Field{}, err
	}
	out, err := ResolveAgg(a.Op, cf.Type)
	if err != nil {
		return dtype.Field{}, err
	}

	// Nullability is where aggregates differ most from elementwise operations.
	//
	// Counting aggregates are never null: an empty or all-null group still has a
	// well-defined count of zero.
	//
	// Every other aggregate IS nullable even over a non-nullable input, because a
	// group can contain no rows the aggregate applies to. Sum of an all-null
	// group is null, NOT zero — the SQL rule, and the one people rely on to
	// distinguish "no sales" from "sales totalling nothing".
	nullable := !a.Op.IsCounting()

	return dtype.Field{Name: OutputName(a), Type: out, Nullable: nullable}, nil
}

// AggBinding is the resolved form of an aggregate: the type it ACCUMULATES in and
// the type it OUTPUTS.
//
// The two differ, and the difference is load-bearing:
//
//   - Sum of Int8 accumulates in Int128, because a thousand Int8 values overflow
//     an Int8 immediately and a wrapped sum is a plausible wrong number.
//   - Sum of Float32 accumulates in Float64 and outputs Float32, because naive
//     float32 accumulation loses everything past ~2^24 elements. Polars has a live
//     bug from exactly this.
//   - Mean accumulates a Float64 sum and a count, and outputs a float.
//
// Returning only the output type — as an earlier draft did — leaves the
// accumulator type implicit, so the first kernel written re-derives it. That is
// the same promotion-drift failure mode expr.Binding exists to prevent for binary
// operators, and it is exactly how a Float32 mean ends up silently wrong.
type AggBinding struct {
	Acc dtype.DataType
	Out dtype.DataType
}

// ResolveAggBinding computes an aggregate's accumulator and output types.
func ResolveAggBinding(op AggOp, in dtype.DataType) (AggBinding, error) {
	switch op {
	case AggCount, AggLen, AggNUnique, AggNullCount:
		// Defined for every type, including nested ones: counting does not inspect
		// values beyond their presence.
		//
		// Uint64 rather than Uint32: having just widened Sum to 128 bits to remove a
		// silent-overflow class, leaving a counter that wraps at 4.29e9 rows would be
		// keeping the same bug in a different column.
		return AggBinding{Acc: dtype.Uint64, Out: dtype.Uint64}, nil

	case AggFirst, AggLast:
		// Positional, so the type is unchanged. Defined for every type.
		return AggBinding{Acc: in, Out: in}, nil

	case AggMin, AggMax:
		if !in.IsOrdered() {
			return AggBinding{}, uerr.New(uerr.KindType, "",
				"%s() is not defined for %s", op, in).
				Hint("only numeric, temporal, string and boolean types have an ordering")
		}
		return AggBinding{Acc: in, Out: in}, nil

	case AggSum:
		if in.ID() == dtype.TypeDuration {
			return AggBinding{Acc: in, Out: in}, nil // durations add; instants do not
		}
		if in.ID() == dtype.TypeDecimal {
			return AggBinding{}, decimalAggUnsupported(op, in)
		}
		if !in.IsNumeric() {
			return AggBinding{}, uerr.New(uerr.KindType, "",
				"sum() requires a numeric operand, got %s", in).
				Hint("use count() to count rows, or len() to include nulls")
		}
		switch {
		case in.IsInteger():
			// Every integer sum accumulates and outputs at 128 bits, following
			// DuckDB. Overflow then needs 2^63 maximum-magnitude rows, i.e. it is
			// unreachable. It also means a signed and an unsigned sum share one
			// accumulator, since Int128 holds every Uint64.
			return AggBinding{Acc: dtype.Int128, Out: dtype.Int128}, nil
		case in == dtype.Float32:
			// Accumulate wider than we output. Float32 accumulation stops making
			// progress once the running total exceeds 2^24 times the addend.
			return AggBinding{Acc: dtype.Float64, Out: dtype.Float32}, nil
		default:
			return AggBinding{Acc: dtype.Float64, Out: dtype.Float64}, nil
		}

	case AggMean:
		if in.ID() == dtype.TypeDuration {
			return AggBinding{Acc: dtype.Float64, Out: in}, nil
		}
		if in.ID() == dtype.TypeDecimal {
			return AggBinding{}, decimalAggUnsupported(op, in)
		}
		if !in.IsNumeric() {
			return AggBinding{}, uerr.New(uerr.KindType, "",
				"mean() requires a numeric operand, got %s", in)
		}
		// Always a float: the mean of two integers is rarely an integer, and
		// truncating it silently is the kind of wrong answer nobody notices.
		if in == dtype.Float32 {
			return AggBinding{Acc: dtype.Float64, Out: dtype.Float32}, nil
		}
		return AggBinding{Acc: dtype.Float64, Out: dtype.Float64}, nil

	case AggAny, AggAllTrue:
		// Boolean only. These ARE Min and Max over a Bool column — false sorts below
		// true, so max is any and min is all — and they reuse that accumulator. They
		// get their own ops rather than being sugar so that Explain says any() and
		// the error for a non-Boolean input names the function the user wrote.
		if !in.IsBool() {
			return AggBinding{}, uerr.New(uerr.KindType, "",
				"%s() requires a Boolean operand, got %s", op, in).
				Hint("compare first, e.g. Col(\"x\").Gt(0).%s()", op)
		}
		return AggBinding{Acc: dtype.Bool, Out: dtype.Bool}, nil

	case AggVar, AggStd, AggMedian, AggQuantile:
		if in.ID() == dtype.TypeDecimal {
			return AggBinding{}, decimalAggUnsupported(op, in)
		}
		if !in.IsNumeric() {
			return AggBinding{}, uerr.New(uerr.KindType, "",
				"%s() requires a numeric operand, got %s", op, in)
		}
		// Float64 throughout, including for a Float32 input, unlike sum and mean.
		// Those preserve Float32 because their result is the same KIND of quantity as
		// their input; a variance is a squared quantity and a quantile is an
		// interpolated position, so there is no input width to preserve.
		return AggBinding{Acc: dtype.Float64, Out: dtype.Float64}, nil

	case AggProduct:
		if in.ID() == dtype.TypeDecimal {
			return AggBinding{}, decimalAggUnsupported(op, in)
		}
		if !in.IsNumeric() {
			return AggBinding{}, uerr.New(uerr.KindType, "",
				"product() requires a numeric operand, got %s", in)
		}
		// Float64 even for integers, which is the one place product knowingly
		// diverges from sum.
		//
		// Sum accumulates integers at 128 bits so that overflow becomes unreachable.
		// Product cannot do the same: i128 has no multiplication, and adding one is a
		// larger piece of work than this aggregate. Float64 is exact to 2^53 and
		// approximate beyond it — a weaker guarantee than sum's, stated here rather
		// than discovered later. An integer product overflows Int64 after about
		// twenty ordinary factors anyway, so an Int64 accumulator would wrap silently
		// on inputs a Float64 still describes approximately.
		return AggBinding{Acc: dtype.Float64, Out: dtype.Float64}, nil

	case AggArgMin, AggArgMax:
		if !in.IsOrdered() {
			return AggBinding{}, uerr.New(uerr.KindType, "",
				"%s() is not defined for %s", op, in).
				Hint("only numeric, temporal, string and boolean types have an ordering")
		}
		// The POSITION within the group, so Uint64 like the counters rather than the
		// input's type. Uint64 for the same reason count is: an index that wraps at
		// 4.29e9 rows is the bug the 128-bit sum was widened to avoid.
		return AggBinding{Acc: dtype.Uint64, Out: dtype.Uint64}, nil

	default:
		return AggBinding{}, uerr.Internalf("ResolveAggBinding: unhandled op %d", op)
	}
}

// decimalAggUnsupported refuses sum and mean over a Decimal.
//
// Min, Max, First, Last, Count and NUnique all work: they select or count rather
// than compute, so the unscaled integer they carry around is still the right
// number with the right scale.
//
// Sum and mean are different. Without this the generic path would bind
// Acc: Float64 and the accumulator would ask a Decimal column — physically Int128
// — for []float64 and fail at runtime as an internal error, which is the
// plan-accepts / kernel-rejects divergence this codebase already has one scar
// from. Refusing here, at plan time, with the reason and the workaround, is the
// least-bad answer while the precision calculus for decimal aggregation is
// undecided: sum's result needs a wider precision than its input, and guessing
// which is a decision better made deliberately than by whichever branch happened
// to be reached first.
func decimalAggUnsupported(op AggOp, in dtype.DataType) error {
	return uerr.New(uerr.KindUnsupported, "",
		"%s() is not implemented for %s", op, in).
		Hint("decimal aggregation needs a precision and scale rule ursus has not " +
			"committed to yet").
		Hint("cast to Float64 first if approximate arithmetic is acceptable")
}

// ResolveAgg returns just the output type, for callers that do not run the
// accumulator.
func ResolveAgg(op AggOp, in dtype.DataType) (dtype.DataType, error) {
	b, err := ResolveAggBinding(op, in)
	if err != nil {
		return dtype.Null, err
	}
	return b.Out, nil
}

// HasAgg reports whether the tree contains an aggregate.
func HasAgg(n Node) bool {
	found := false
	Walk(n, func(x Node) bool {
		if _, ok := x.(*Agg); ok {
			found = true
			return false
		}
		return true
	})
	return found
}

// HasUnboundAgg reports whether the tree contains an aggregate that is NOT enclosed
// by a window.
//
// # Why this is separate from HasAgg
//
// `Col("x").Mean().Over(Col("g"))` contains an *Agg, so HasAgg says true — and the
// row-preserving contexts use that answer to refuse aggregates. But a window
// preserves height: the mean is computed per partition and written back to every
// row, so the expression is exactly as legal in Select as `x + 1` is. An Agg beneath
// a Window is BOUND by it, the way a bound variable is bound by its quantifier.
//
// HasAgg is left alone rather than redefined, because its other caller wants the
// literal question: IsAggregation uses it to reject `sum(a).mean()`, and an
// aggregate of a windowed aggregate is just as meaningless as an aggregate of an
// aggregate.
//
// Partition and order keys are still descended into, so `Sum(x).Over(Sum(g))` is
// refused — a partition key must be a per-row value.
func HasUnboundAgg(n Node) bool {
	var walk func(Node) bool
	walk = func(x Node) bool {
		switch t := x.(type) {
		case *Agg:
			return true
		case *Window:
			// The child is bound. The keys are not.
			for _, p := range t.PartitionBy {
				if walk(p) {
					return true
				}
			}
			for _, k := range t.OrderBy {
				if walk(k.Expr) {
					return true
				}
			}
			return false
		default:
			for _, c := range x.Children() {
				if walk(c) {
					return true
				}
			}
			return false
		}
	}
	return walk(n)
}

// IsAggregation reports whether the expression is a well-formed aggregate: it
// reduces its input to one row.
//
// An expression is an aggregation if every path from the root to a Col passes
// through an Agg. `sum(a) + 1` and `sum(a) / count()` qualify; `sum(a) + b` does
// not, because b is still a per-row value and there is no defined way to combine
// the two.
func IsAggregation(n Node) bool {
	switch t := n.(type) {
	case *Agg:
		// An aggregate of an aggregate is meaningless: the inner one has already
		// reduced the group to a single value, so `sum(a).mean()` is the mean of
		// one number. Rejecting it here rather than returning a bare true is what
		// stops it type-checking successfully (Sum→Int64, Mean→Float64) and
		// failing much later.
		return !HasAgg(t.Child)
	case *Col:
		return false
	case *Lit:
		return true // a literal broadcasts to either shape
	case *Match:
		return false
	case *Err:
		return true
	case *Window, *WinFn:
		// A window PRESERVES height, so it is not an aggregation and cannot appear
		// in GroupBy().Agg(). The generic default arm below would reach the same
		// answer by recursing into the partition keys and finding a bare Col — but
		// it would then blame that column in the error message, when the real
		// problem is the window itself.
		return false
	default:
		kids := t.Children()
		if len(kids) == 0 {
			return true
		}
		for _, c := range kids {
			if !IsAggregation(c) {
				return false
			}
		}
		return true
	}
}

// BareColumns returns the columns an expression reads OUTSIDE any aggregate.
//
// These are the columns that make an expression non-aggregating, and naming them
// is what turns "invalid aggregate expression" into a message that says which
// column is the problem and what to do about it.
func BareColumns(n Node) []string {
	var out []string
	seen := map[string]struct{}{}

	var walk func(Node, bool)
	walk = func(x Node, insideAgg bool) {
		switch t := x.(type) {
		case *Agg:
			for _, c := range t.Children() {
				walk(c, true)
			}
		case *Window:
			// Everything under a window is bound — the aggregated child by the
			// window's own reduction, the keys because they are consumed as keys
			// rather than left as per-row values. So a window contributes no bare
			// columns and never appears in the "used outside any aggregate" hint.
			for _, c := range t.Children() {
				walk(c, true)
			}
		case *Col:
			if !insideAgg {
				if _, dup := seen[t.Name]; !dup {
					seen[t.Name] = struct{}{}
					out = append(out, t.Name)
				}
			}
		default:
			for _, c := range x.Children() {
				walk(c, insideAgg)
			}
		}
	}
	walk(n, false)
	return out
}

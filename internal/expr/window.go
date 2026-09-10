package expr

import (
	"strconv"
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// OrderKey is one component of an ordering: an expression and how to sort by it.
//
// It lives here rather than in the plan package because a window carries its own
// ordering and internal/expr may not import internal/plan. plan.SortKey is an alias
// for this type, so there is one definition rather than two that must agree.
type OrderKey struct {
	Expr       Node
	Descending bool

	// NullsLast places nulls at the END OF THE OUTPUT, independent of direction.
	//
	// The zero value is therefore NULLS FIRST, which is ursus's default and matches
	// Polars (nulls_last=False). Per-key `.NullsLast()` overrides it.
	//
	// Tying null placement to direction — as SQL does, where nulls sort as the
	// largest value and so land last ascending and first descending — was
	// considered and rejected: it means `.Desc()` silently relocates the nulls,
	// which surprises people every time.
	NullsLast bool
}

func (k OrderKey) String() string {
	var b strings.Builder
	b.WriteString(k.Expr.String())
	if k.Descending {
		b.WriteString(" DESC")
	} else {
		b.WriteString(" ASC")
	}
	if k.NullsLast {
		b.WriteString(" NULLS LAST")
	} else {
		b.WriteString(" NULLS FIRST")
	}
	return b.String()
}

// MappingStrategy decides how a window's per-partition result is placed back into
// the frame.
type MappingStrategy uint8

const (
	// MapGroupsToRows returns each row's own partition value, in the original row
	// order. The default, and the only one that composes with other columns.
	MapGroupsToRows MappingStrategy = iota

	// MapExplode leaves the output in partition-sorted order instead of permuting
	// it back. Cheaper — it skips the inverse permutation — and it REORDERS the
	// frame, so it is only safe when every other column is windowed the same way.
	MapExplode

	// MapJoin aggregates each partition into a List repeated across its rows.
	// Refused, and from step 46 the refusal names the thing that does it:
	// Col("v").Implode().Over(k) IS this, under the default mapping.
	MapJoin

	mappingCount
)

var mappingNames = [mappingCount]string{
	MapGroupsToRows: "groups_to_rows", MapExplode: "explode", MapJoin: "join",
}

func (m MappingStrategy) String() string {
	// The `!= ""` half is the load-bearing one. mappingNames is sized by the enum's
	// sentinel, so `int(m) < len(...)` is true for every DECLARED constant and this
	// fallback would be unreachable without it — a forgotten entry would render as
	// the EMPTY STRING, and two ops both missing names would render identically and
	// collide in the three maps that dedup on String(). dtype.TypeID.String() has
	// had the correct guard since step 1; these eight did not.
	if int(m) < len(mappingNames) && mappingNames[m] != "" {
		return mappingNames[m]
	}
	return "?"
}

// Window computes its child within partitions and puts the answer back on every
// row — the `.Over()` of the API, and SQL's OVER clause.
//
// # Its operands are ordinary children, deliberately
//
// Node.Children's doc offers an escape hatch for exactly this situation: "A nested
// SCOPE body is deliberately not a child … so a generic walker must not rewrite
// across it." That mechanism is WRONG here, and the reason is worth stating because
// the alternative looks so plausible.
//
// It exists because a list body resolves against a DIFFERENT schema — the element
// type. A window's child resolves against the same schema as everything around it:
// `Col("x").Mean().Over(Col("g"))` resolves x and g against one input schema. There
// is no schema change to protect against.
//
// And hiding the child would break four things that presently work for free:
// RootNames would report {g} and not {x}, so projection pushdown would prune away
// the very column being aggregated; Expand would not reach a matcher inside the
// body, making All().Mean().Over(g) inexpressible; FirstErr would miss a deferred
// error; and OutputName would name the result after the partition key.
//
// So the operands stay ordinary children and the scoping lives in HasUnboundAgg
// instead. This is what design/logical.md predicted: "Spec.PartitionBy and OrderBy
// are ordinary children, so RootNames and expansion pick them up automatically."
//
// # Where a Window is legal
//
// In Select, WithColumns, Filter and Sort — the row-preserving contexts, because a
// window preserves height. NOT inside GroupBy().Agg(), which is the nested-window
// case and is refused.
type Window struct {
	Child       Node
	PartitionBy []Node
	OrderBy     []OrderKey
	Mapping     MappingStrategy
}

func (w *Window) node() {}

// Children returns the child first, then the partition keys, then the order keys.
//
// Child first matters: OutputName's generic fallback takes the first name from
// RootNames, so any other order would name a window after its partition key. There
// is an explicit OutputName case as well, and this ordering means the two agree
// rather than one masking the other.
func (w *Window) Children() []Node {
	out := make([]Node, 0, 1+len(w.PartitionBy)+len(w.OrderBy))
	out = append(out, w.Child)
	out = append(out, w.PartitionBy...)
	for _, k := range w.OrderBy {
		out = append(out, k.Expr)
	}
	return out
}

// String renders the child, the partition keys, the order keys AND the mapping.
//
// Every part is load-bearing rather than decorative, for the reason step 7 recorded
// when it fixed the same class of bug for Agg: the physical planner deduplicates
// extracted sub-expressions by keying a map on this string. Two windows that render
// identically become ONE computation. So `x.mean().over(g)` and `x.mean().over(h)`
// rendering the same would give every row the wrong partition's mean, with no error
// and no length mismatch anywhere to catch it.
func (w *Window) String() string {
	var b strings.Builder
	b.WriteString(w.Child.String())
	b.WriteString(".over(")
	for i, p := range w.PartitionBy {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(p.String())
	}
	if len(w.OrderBy) > 0 {
		b.WriteString("; order by ")
		for i, k := range w.OrderBy {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(k.String())
		}
	}
	if w.Mapping != MapGroupsToRows {
		b.WriteString("; mapping ")
		b.WriteString(w.Mapping.String())
	}
	b.WriteString(")")
	return b.String()
}

func (w *Window) Field(in *dtype.Schema) (dtype.Field, error) {
	// Both unimplemented strategies are refused HERE, at logical plan time, so
	// CollectSchema and Explain fail too. MapExplode used to be refused only in the
	// physical planner, which meant Explain happily printed a plan containing an
	// operator that could not be built — two sibling enum values with two different
	// guarantees, for no reason anyone had chosen.
	if w.Mapping == MapExplode {
		return dtype.Field{}, uerr.New(uerr.KindUnsupported, "over",
			"the explode mapping strategy is not implemented").
			Hint("it reorders the frame to partition-major order, which no operator " +
				"produces yet").
			Hint("use the default mapping to get one value per row in the original order")
	}

	// The partition keys become group keys, so they must be hashable — the same
	// constraint GroupBy applies, checked here so the message names `over`.
	for _, p := range w.PartitionBy {
		f, err := p.Field(in)
		if err != nil {
			return dtype.Field{}, err
		}
		if !f.Type.IsHashable() {
			return dtype.Field{}, uerr.New(uerr.KindType, "over",
				"cannot partition by %q of type %s", f.Name, f.Type).
				Hint("partition keys must be hashable: numeric, temporal, string or boolean")
		}
	}
	for _, k := range w.OrderBy {
		f, err := k.Expr.Field(in)
		if err != nil {
			return dtype.Field{}, err
		}
		if !f.Type.IsOrdered() {
			return dtype.Field{}, uerr.New(uerr.KindType, "over",
				"cannot order by %q of type %s", f.Name, f.Type).
				Hint("only numeric, temporal, string and boolean types have an ordering")
		}
	}

	if w.Mapping == MapJoin {
		// Still refused, and step 46 changed why rather than whether.
		//
		// It was "there is no List column layout", then "no aggregate builds one
		// yet". Both are now false, and implementing it revealed a third answer:
		// MapJoin is a SYNONYM. "Each partition's values as a list, repeated across
		// its rows" is exactly Col("v").Implode().Over(k) under the default
		// mapping — implode produces the list and the ordinary broadcast repeats
		// it. A second spelling would be a second thing to keep correct for no
		// expressive gain, and it has no coherent reading over a non-imploding
		// aggregate: Col("v").Sum() with this mapping would have to discard the sum.
		return dtype.Field{}, uerr.New(uerr.KindUnsupported, "over",
			"the join mapping strategy is not implemented").
			Hint("it is Col(...).Implode().Over(...), which does this under the " +
				"default mapping").
			Hint("use the default mapping to get one value per row")
	}

	cf, err := w.Child.Field(in)
	if err != nil {
		return dtype.Field{}, err
	}
	// The type is the child's. Nullability is too: an aggregate over a partition
	// with no non-null values is null exactly as it would be over a group, and the
	// broadcast back to rows adds nothing.
	return dtype.Field{Name: OutputName(w), Type: cf.Type, Nullable: cf.Nullable}, nil
}

// --- ordered window functions ---------------------------------------------------

// WinFnOp is a function that is only meaningful within an ordered window.
type WinFnOp uint8

const (
	WinRank WinFnOp = iota
	WinCumCount
	WinCumSum
	WinCumProd
	WinCumMin
	WinCumMax
	WinShift

	// Forward and backward fill. TWO ops rather than one plus a Reverse flag, on
	// purpose: WinFn.String() renders the op name, so the direction can never
	// collide even if the parameter rendering is imperfect. With one op the
	// direction would live ONLY in WinParams.args, which is exactly the field that
	// has been the source of every collision bug so far.
	WinForwardFill
	WinBackwardFill

	winFnCount
)

var winFnNames = [winFnCount]string{
	WinRank: "rank", WinCumCount: "cum_count", WinCumSum: "cum_sum",
	WinCumProd: "cum_prod", WinCumMin: "cum_min", WinCumMax: "cum_max",
	WinShift:       "shift",
	WinForwardFill: "forward_fill", WinBackwardFill: "backward_fill",
}

func (o WinFnOp) String() string {
	// The `!= ""` half is the load-bearing one. winFnNames is sized by the enum's
	// sentinel, so `int(o) < len(...)` is true for every DECLARED constant and this
	// fallback would be unreachable without it — a forgotten entry would render as
	// the EMPTY STRING, and two ops both missing names would render identically and
	// collide in the three maps that dedup on String(). dtype.TypeID.String() has
	// had the correct guard since step 1; these eight did not.
	if int(o) < len(winFnNames) && winFnNames[o] != "" {
		return winFnNames[o]
	}
	return "?"
}

// IsCumulative reports whether the op is a running scan over the ordered partition.
func (o WinFnOp) IsCumulative() bool { return o >= WinCumSum && o <= WinCumMax }

// RankMethod decides how Rank breaks ties.
type RankMethod uint8

const (
	// RankOrdinal gives every row a distinct rank, ties broken by input order.
	RankOrdinal RankMethod = iota
	// RankDense gives tied rows the same rank and does not skip the next value.
	RankDense
	// RankMin and RankMax give tied rows the lowest or highest of their positions.
	RankMin
	RankMax
	// RankAverage gives tied rows the mean of their positions, so it returns a
	// float where every other method returns an integer.
	RankAverage

	rankMethodCount
)

var rankNames = [rankMethodCount]string{
	RankOrdinal: "ordinal", RankDense: "dense", RankMin: "min",
	RankMax: "max", RankAverage: "average",
}

func (m RankMethod) String() string {
	// The `!= ""` half is the load-bearing one. rankNames is sized by the enum's
	// sentinel, so `int(m) < len(...)` is true for every DECLARED constant and this
	// fallback would be unreachable without it — a forgotten entry would render as
	// the EMPTY STRING, and two ops both missing names would render identically and
	// collide in the three maps that dedup on String(). dtype.TypeID.String() has
	// had the correct guard since step 1; these eight did not.
	if int(m) < len(rankNames) && rankNames[m] != "" {
		return rankNames[m]
	}
	return "?"
}

// WinParams carries an ordered function's configuration.
//
// A typed struct rather than child nodes, for the reason AggParams gives: these are
// configuration, not operands. There is no meaning to a per-row rank method.
type WinParams struct {
	Method     RankMethod
	Descending bool
	Reverse    bool
	N          int64
}

func (p WinParams) args(op WinFnOp) string {
	switch op {
	case WinRank:
		s := p.Method.String()
		if p.Descending {
			s += ", desc"
		}
		return s
	case WinShift, WinForwardFill, WinBackwardFill:
		// WITHOUT THIS ARM the fills fall to the default below and render as
		// "forward_fill()" whatever their limit — so ForwardFill(1) and
		// ForwardFill(3) would be one string, collide in the three maps that dedup on
		// String(), and one would silently take the other's answer. That is step 7's
		// quantile bug and step 8's partition-key bug, third occurrence.
		return strconv.FormatInt(p.N, 10)
	default:
		if p.Reverse {
			return "reverse"
		}
		return ""
	}
}

// WinFn is an ordered window function — rank, a running total, a shift.
//
// # Why not a Call
//
// Call{Fn, Args} already carries a function plus literal arguments, and this is the
// same shape. But Call is contractually ELEMENTWISE: evalCall runs inside a BatchOp,
// which sees one batch and may run on any worker. These functions need the whole
// partition in order, so they are pipeline breakers. Same shape, opposite contract —
// and a separate node type is what makes "only legal inside a Window" checkable.
//
// # Why not an AggOp
//
// They do not reduce. kernel.Accumulator's Finish(name, nGroups) returns one row per
// group; these return one row per INPUT row. They need a different kernel family.
//
// Fill is Shift's optional replacement for the rows shifted in from outside the
// partition. nil means null, which is the usual want.
type WinFn struct {
	Fn     WinFnOp
	Child  Node
	Fill   Node
	Params WinParams
}

func (f *WinFn) node() {}

func (f *WinFn) Children() []Node {
	if f.Fill == nil {
		return []Node{f.Child}
	}
	return []Node{f.Child, f.Fill}
}

func (f *WinFn) String() string {
	s := f.Child.String() + "." + f.Fn.String() + "(" + f.Params.args(f.Fn) + ")"
	if f.Fill != nil {
		s += ".fill(" + f.Fill.String() + ")"
	}
	return s
}

func (f *WinFn) Field(in *dtype.Schema) (dtype.Field, error) {
	cf, err := f.Child.Field(in)
	if err != nil {
		return dtype.Field{}, err
	}
	out, err := ResolveWinFn(f.Fn, f.Params, cf.Type)
	if err != nil {
		return dtype.Field{}, err
	}

	// Rank and cum_count number rows, so they are never null. Everything else can
	// be: a shift past the partition edge produces nulls even over a non-null
	// column, and a running total inherits its input's nullability.
	nullable := cf.Nullable
	switch f.Fn {
	case WinRank, WinCumCount:
		nullable = false
	case WinShift:
		nullable = true
	}
	return dtype.Field{Name: OutputName(f), Type: out, Nullable: nullable}, nil
}

// ResolveWinFn gives an ordered window function's output type.
//
// The single authority, consulted by Field for the schema and by the evaluator for
// the kernel's output type — the Binding lesson, applied a fourth time.
func ResolveWinFn(fn WinFnOp, p WinParams, in dtype.DataType) (dtype.DataType, error) {
	switch fn {
	case WinRank:
		if !in.IsOrdered() {
			return dtype.Null, uerr.New(uerr.KindType, "rank",
				"rank() is not defined for %s", in).
				Hint("only numeric, temporal, string and boolean types have an ordering")
		}
		// Average is the odd one out: the mean of two adjacent positions is not an
		// integer, and truncating it would make it indistinguishable from min.
		if p.Method == RankAverage {
			return dtype.Float64, nil
		}
		return dtype.Uint32, nil

	case WinCumCount:
		return dtype.Uint32, nil

	case WinCumSum:
		// The same binding Sum uses, so `cum_sum(x)` and `sum(x)` agree on the last
		// row rather than differing by an accumulator width.
		b, err := ResolveAggBinding(AggSum, in)
		if err != nil {
			return dtype.Null, err
		}
		return b.Out, nil

	case WinCumProd:
		b, err := ResolveAggBinding(AggProduct, in)
		if err != nil {
			return dtype.Null, err
		}
		return b.Out, nil

	case WinCumMin, WinCumMax:
		if !in.IsOrdered() {
			return dtype.Null, uerr.New(uerr.KindType, fn.String(),
				"%s() is not defined for %s", fn, in).
				Hint("only numeric, temporal, string and boolean types have an ordering")
		}
		return in, nil

	case WinShift, WinForwardFill, WinBackwardFill:
		return in, nil

	default:
		return dtype.Null, uerr.Internalf("expr: unknown window function %d", fn)
	}
}

// HasWindow reports whether the tree contains a window or an ordered window
// function.
//
// A bare WinFn counts: `Col("x").CumSum()` with no .Over() is a window over the
// whole frame, and it is normalised into one rather than being a second shape the
// planner has to know about.
func HasWindow(n Node) bool {
	found := false
	Walk(n, func(x Node) bool {
		switch x.(type) {
		case *Window, *WinFn:
			found = true
			return false
		}
		return true
	})
	return found
}

// --- generic rebuild ------------------------------------------------------------

// Rebuild returns a copy of n with its children replaced, in the order Children
// returns them.
//
// # Why this exists
//
// Rewriting an expression tree means copying every node with new children, and
// before this there were two hand-written copies of that walk — substitute, and the
// physical planner's extractAggs — with a third about to be added for windows. Each
// copy enumerates the node types and carries their non-child fields across by hand,
// which is exactly the shape of the bug step 7 hit twice: an Agg rebuilt without its
// Params silently became a different aggregate, and a Window rebuilt without its
// Mapping would silently become a different window.
//
// Copying the struct and replacing only the child fields makes that class impossible:
// a new field on any node is carried by default, and the only way to get it wrong is
// to add a child field without updating Children, which the length check catches.
//
// It PANICS on an unknown node type, for the same reason substitute did: Node is
// sealed, so reaching the default means a node type was added without teaching the
// rewriter about it, and silently dropping its children is worse than a crash.
func Rebuild(n Node, kids []Node) Node {
	if got, want := len(kids), len(n.Children()); got != want {
		panic("expr: Rebuild given " + itoa(got) + " children for a node with " + itoa(want))
	}
	switch t := n.(type) {
	case *Col, *Lit, *Err, *Match:
		return n // leaves

	case *Binary:
		c := *t
		c.L, c.R = kids[0], kids[1]
		return &c
	case *Unary:
		c := *t
		c.Child = kids[0]
		return &c
	case *Alias:
		c := *t
		c.Child = kids[0]
		return &c
	case *Cast:
		c := *t
		c.Child = kids[0]
		return &c
	case *Rename:
		c := *t
		c.Child = kids[0]
		return &c
	case *Agg:
		c := *t
		c.Child = kids[0]
		return &c
	case *Call:
		c := *t
		c.Args = append([]Node(nil), kids...)
		return &c
	case *Cond:
		c := *t
		c.Pred, c.Then, c.Else = kids[0], kids[1], kids[2]
		return &c

	case *Window:
		c := *t
		c.Child = kids[0]
		c.PartitionBy = append([]Node(nil), kids[1:1+len(t.PartitionBy)]...)
		ord := make([]OrderKey, len(t.OrderBy))
		for i, k := range t.OrderBy {
			ord[i] = OrderKey{
				Expr: kids[1+len(t.PartitionBy)+i], Descending: k.Descending,
				NullsLast: k.NullsLast,
			}
		}
		c.OrderBy = ord
		return &c
	case *WinFn:
		c := *t
		c.Child = kids[0]
		if t.Fill != nil {
			c.Fill = kids[1]
		}
		return &c

	case *UDF:
		// Copying the struct carries Impl, Out, Nullable and Name across
		// unchanged, which is the point: rebuilding must not alter what the user
		// declared, only which child it applies to.
		//
		// This arm is not optional. Rebuild is reached by substitute during
		// multi-column expansion — Col("a","b").MapElements(...) — and by
		// simplify.walk on EVERY optimizer pass, and the default arm below panics
		// rather than erroring.
		c := *t
		c.Child = kids[0]
		return &c

	default:
		panic("expr: Rebuild: unhandled node type")
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

package expr

import (
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
	// collide in the four dedup maps. dtype.TypeID.String() has
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

	// Inherits marks a window the library built for a sugar, not one the user
	// wrote: FillNull(FillMean)'s mean over the frame. Inside an enclosing Over it
	// takes that Over's partition, order and mapping (plan's extractWindows, step
	// 148), so FillNull(FillMean).Over(g) fills with each group's mean, as Polars
	// does. A window the user wrote inside another is still refused.
	Inherits bool
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
	if w.Inherits {
		// It computes what a window of the same keys computes, but an enclosing
		// Over treats the two differently, so they must not be one entry in a map.
		b.WriteString("; inherited")
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
	// An ordering over an aggregate, refused here for the reason MapExplode is: the
	// physical planner refused it alone, so Explain and CollectSchema accepted a
	// plan Collect would not run (O11). The physical check stays as a backstop.
	if agg, isAgg := w.Child.(*Agg); isAgg && len(w.OrderBy) > 0 {
		return dtype.Field{}, OrderedAggRefusal(agg.Op)
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

// The ordered window functions are winfn.go, their output types winresolve.go, and
// the generic Rebuild rebuild.go.

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

// OrderedAggRefusal refuses an ordering over an aggregate, here and in the physical
// planner's backstop.
//
// The hint depends on the aggregate. It used to call every one of them
// order-independent "except for first, last, arg_min, arg_max and implode", which
// said both things of first (audit A12).
func OrderedAggRefusal(op AggOp) *uerr.Error {
	err := uerr.New(uerr.KindUnsupported, "over",
		"an ordering is not yet honoured over an aggregate")
	if op.IsOrderDependent() {
		return err.Hint("%s depends on the order; sort the frame by the ordering first, "+
			"and drop it from the window: a partition's rows are taken in the frame's "+
			"order", op)
	}
	return err.Hint("%s does not depend on the order, so drop the ordering: the answer "+
		"is the same", op)
}

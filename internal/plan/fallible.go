package plan

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
)

// fallible reports whether p can raise an error on the values it is given — not on
// its shape, which resolution has already judged, but on the data.
//
// # Why pushdown has to know
//
// A Filter's conjuncts run in order, each on the rows the ones before it kept, so
// `Filter(Col("ok")).Filter(Col("s").Cast(Int64).Gt(1))` never casts a row `ok`
// removes: the first conjunct is the second one's guard. Predicate pushdown moves
// conjuncts below other nodes, and a conjunct that moves below one that stays runs
// first — on the rows its guard would have removed — and a join side's conjunct runs
// on rows the join itself would have removed. For a conjunct that cannot fail that is
// only a different order of the same answer. For one that can, it is an error the
// query did not have (O8b, O8-join), which is the one thing an optimizer may not add.
//
// # What can fail
//
// Conservatively, because a false "can fail" costs a missed pushdown and a false
// "cannot" costs a wrong error:
//
//   - a strict Cast that is not total (totalCast);
//   - a udf, which is the caller's code;
//   - arithmetic with a temporal operand, which refuses an overflow;
//   - dt.truncate and dt.epoch, which refuse an instant the type cannot hold.
//
// An expression this cannot type is treated as fallible.
func fallible(p expr.Node, in *dtype.Schema) bool {
	found := false
	expr.Walk(p, func(n expr.Node) bool {
		if found {
			return false
		}
		switch t := n.(type) {
		case *expr.Cast:
			if t.Strict {
				f, err := t.Child.Field(in)
				found = err != nil || !totalCast(f.Type, t.To)
			}
		case *expr.UDF:
			found = true
		case *expr.Binary:
			if t.Op.IsComparison() || t.Op.IsMissingComparison() || t.Op.IsLogical() {
				break
			}
			lf, lerr := t.L.Field(in)
			rf, rerr := t.R.Field(in)
			found = lerr != nil || rerr != nil || lf.Type.IsTemporal() || rf.Type.IsTemporal()
		case *expr.Call:
			found = t.Fn == expr.FnDtTruncate || t.Fn == expr.FnDtEpoch
		}
		return !found
	})
	return found
}

// totalCast reports whether every value of from converts to to: the same type, any
// formatting to String, an integer widening, an integer, Bool or Decimal to a float
// (which rounds and cannot overflow: Decimal and Int128 stay far below Float32's
// range), Float32 to Float64, and Bool to an integer.
func totalCast(from, to dtype.DataType) bool {
	switch {
	case from == to || from.IsNull():
		return true
	case to.ID() == dtype.TypeString:
		return from.IsNumeric() || from.IsTemporal() || from.IsBool() || from.HasStringStorage()
	case from.IsInteger() && to.IsInteger():
		p, ok := dtype.PromoteExact(from, to)
		return ok && p == to
	case (from.IsInteger() || from.IsBool() || from.ID() == dtype.TypeDecimal) && to.IsFloat():
		return true
	case from.ID() == dtype.TypeFloat32 && to.ID() == dtype.TypeFloat64:
		return true
	case from.IsBool() && to.IsInteger():
		return true
	}
	return false
}

// holdFallible keeps a fallible conjunct where it was — dest stay — unless every
// conjunct before it went where it goes. dest[i] is conjunct i's destination, and
// stay is the value meaning "above the node". A conjunct that stays guards every
// fallible conjunct after it, so those stay too, which the loop gives for free: a
// stayed conjunct's destination differs from any move.
func holdFallible(preds []expr.Node, dest []int, stay int, in *dtype.Schema) {
	for i, p := range preds {
		if dest[i] == stay || !fallible(p, in) {
			continue
		}
		for j := range i {
			if dest[j] != dest[i] {
				dest[i] = stay
				break
			}
		}
	}
}

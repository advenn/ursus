package physical

import (
	"context"

	"ursus/dtype"
	"ursus/internal/data"
	"ursus/internal/expr"
	"ursus/internal/plan"
)

// KernelFolder evaluates constant expressions for the optimizer's simplification
// rule.
//
// # Why it lives here and not in internal/plan
//
// Folding must not compute the answer a second way. `Lit(0.1).Add(Lit(0.2))` has
// to fold to exactly what the executor would produce, bit for bit, or a query
// mixing folded and unfolded values disagrees with itself — and the only way to
// guarantee that is to run the SAME evaluator. So this is Eval over a one-row
// batch with no columns, not a reimplementation of arithmetic.
//
// But Eval lives at L50 with Arrow, the allocator and GOEXPERIMENT=simd behind
// it, and internal/plan at L40 has none of those: design/logical.md §13's claim
// is that a plan can be built, resolved, optimized and explained "without an
// executor, without Arrow, and without a Parquet file". Importing kernel from
// there to fold two literals would retire that for the whole layer.
//
// So the dependency runs the way the levels already allow — physical knows about
// plan — and plan takes the folder as an interface naming no Arrow type. lazy.go
// injects it. Nothing else changes.
type KernelFolder struct{}

var _ plan.ConstEvaluator = KernelFolder{}

// Fold evaluates n and returns it as a literal.
//
// The (nil, false, nil) return means "not folded" and is the normal outcome for
// anything this cannot round-trip exactly. An error return means the same thing
// to the caller — see simplify.foldExpr for why a kernel error must never become
// a query failure — but is passed back so a genuinely internal fault is not
// disguised as a declined optimization.
func (KernelFolder) Fold(n expr.Node, _ *dtype.Schema) (expr.Node, bool, error) {
	empty, err := dtype.NewSchema()
	if err != nil {
		return nil, false, err
	}
	// One row, no columns — the shape NewBatchRows exists for. Length 1 because
	// litColumn already materialises literals at length 1 and broadcasting is a
	// property of the kernel dispatcher, so a constant expression evaluates over
	// this batch exactly as it would over a real one.
	b := data.NewBatchRows(empty, nil, 1)

	c, err := Eval(context.Background(), n, b)
	if err != nil {
		// A strict cast that cannot represent its value is the real case; division
		// by zero is not one, because the kernel answers it with +Inf or a null
		// rather than an error. Declining leaves the failure at Collect, where it
		// has always been, instead of moving it to Explain.
		//
		// Load-bearing, and against a CRASH rather than a wrong answer: Eval returns
		// a nil column with its error, so removing this check dereferences nil on
		// the next line. There is no value to fold to, which is why the refusal is
		// unconditional rather than a judgement call.
		return nil, false, nil
	}
	if c.Len() != 1 {
		return nil, false, nil
	}
	return litFromColumn(c)
}

// litFromColumn is the inverse of litColumn, and is deliberately narrower.
//
// litColumn accepts every literal a user can write, including time.Time and
// []byte. This direction accepts only what it can prove round-trips to an
// IDENTICAL column: the Go type has to pair with the dtype exactly, or the fold
// silently changes a column's physical width or its unit. The cases left out —
// temporal, Decimal, Int128, Binary — are all ones where litColumn's
// reconstruction is lossy or unit-blind, and none of them are types anyone writes
// two literals of and adds together.
//
// A refusal here is free: the expression is left exactly as the user wrote it and
// the executor evaluates it per batch, which is what happens today.
func litFromColumn(c *data.Column) (expr.Node, bool, error) {
	dt := c.DType()
	if !c.IsValid(0) {
		// A typed null. litColumn routes this to kernel.NullColumn with the same
		// dtype, so the round trip is exact for every type — including the ones
		// refused below, whose problem is the VALUE encoding rather than the type.
		return &expr.Lit{Value: nil, DT: dt}, true, nil
	}

	switch dt {
	case dtype.Bool:
		return litOf[bool](c, dt)
	case dtype.String:
		return litOf[string](c, dt)
	case dtype.Int8:
		return litOf[int8](c, dt)
	case dtype.Int16:
		return litOf[int16](c, dt)
	case dtype.Int32:
		return litOf[int32](c, dt)
	case dtype.Int64:
		return litOf[int64](c, dt)
	case dtype.Uint8:
		return litOf[uint8](c, dt)
	case dtype.Uint16:
		return litOf[uint16](c, dt)
	case dtype.Uint32:
		return litOf[uint32](c, dt)
	case dtype.Uint64:
		return litOf[uint64](c, dt)
	case dtype.Float32:
		return litOf[float32](c, dt)
	case dtype.Float64:
		return litOf[float64](c, dt)
	}
	return nil, false, nil
}

func litOf[T any](c *data.Column, dt dtype.DataType) (expr.Node, bool, error) {
	s, err := data.TypedColumn[T](c)
	if err != nil {
		return nil, false, nil
	}
	v, ok := s.Get(0)
	if !ok {
		return nil, false, nil
	}
	return &expr.Lit{Value: v, DT: dt}, true, nil
}

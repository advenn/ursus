package ursus

import (
	"context"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/uerr"
)

// This file is the escape hatch. Everything else in ursus is a closed set of
// operations the planner understands; a UDF is a Go function the planner cannot
// see into at all.
//
// # Why this is a generic METHOD
//
// Go 1.27 allows a method to declare its own type parameters, which is the reason
// this project targets 1.27. It matters here more than anywhere: MapElements has
// to name the input and output Go types to be typesafe, and it has to be a method
// to chain off an Expr.
//
// It also runs straight into 1.27's limitation — "a generic method cannot implement
// an interface method" — because the plan holds expr.Node, an interface. So the
// type parameters cannot survive into the plan. They are ERASED here: In and Out
// are captured by a closure, and what the node stores is that closure. This file is
// the only place in the engine where they are still known.

// MapElements applies a Go function to every non-null value.
//
// The per-element escape hatch. In Polars this is the slow one, because each call
// crosses into the Python interpreter; in Go it is a function call, which is what
// makes it worth offering rather than merely tolerating.
//
//	Col("celsius").MapElements[float64, float64](
//	    "to_fahrenheit", ursus.Float64,
//	    func(c float64) (float64, error) { return c*9/5 + 32, nil })
//
// # Nulls pass through
//
// fn never sees a null: a null input row is a null output row, and fn is not
// called for it. That is deliberate — the alternative hands fn a zero value it
// cannot distinguish from a real one. Use MapBatches when the null matters.
//
// # name must be unique within the query, and this is not cosmetic
//
// Three separate parts of the engine deduplicate expressions by their rendered
// form, and a Go closure has no rendered form. Two different functions sharing a
// name over the same column are one computation to the planner, and both results
// come from whichever it saw first. The name is what keeps them apart.
//
// # fn must be safe to call from several goroutines
//
// ursus parallelises a Select or Filter across runtime.NumCPU() workers by
// default, and there is no way to opt a single expression out. A closure over a
// shared map is a data race. fn must also not depend on the ORDER it is called in:
// output order is preserved, invocation order is not.
//
// # out is a declaration, and it is checked
//
// The engine reports out as the column's type from CollectSchema and Explain,
// before fn has ever run. If fn then produces something else, the evaluator fails
// the query rather than letting a wrong schema propagate.
func (e Expr) MapElements[In, Out Literal](name string, out DataType,
	fn func(In) (Out, error)) Expr {

	// No guard for an errored child: Err rides in the tree, and FirstErr walks
	// Children, so a UDF over a broken expression reports the child's error.
	if err := checkUDF(name, fn == nil, "map_elements"); err != nil {
		return wrap(&expr.Err{E: err})
	}

	impl := func(ctx context.Context, in *data.Column, outName string) (*data.Column, error) {
		src, err := udfInput[In](in, "map_elements", name)
		if err != nil {
			return nil, err
		}
		n := in.Len()
		vals := make([]Out, n)
		valid := bitmap.NewBuilder(n)
		mask := in.Validity()

		for i := range n {
			if !mask.Get(i) {
				valid.Append(false)
				continue
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			v, err := fn(src[i])
			if err != nil {
				// Naming the row and the column, as Series.MapErr does — a UDF
				// that fails on one row of a million is useless without it.
				return nil, uerr.Wrap(err, uerr.KindValue, "map_elements",
					"udf %q at row %d of column %q", name, i, in.Name())
			}
			vals[i] = v
			valid.Append(true)
		}
		return buildTyped(name, "map_elements", outName, out, vals, valid.Finish())
	}
	return e.udf(name, "map_elements", out, impl)
}

// MapBatches applies a Go function to a whole column at a time.
//
// The vectorized escape hatch, and the one to reach for first: one call per batch
// rather than one per row, and full control of the validity mask.
//
//	Col("v").MapBatches[float64, float64](
//	    "normalise", ursus.Float64,
//	    func(vals []float64, ok []bool) ([]float64, []bool, error) { ... })
//
// # The two slices are positional and the same length
//
// vals[i] is row i and ok[i] says whether it is present. A null row's vals entry
// is the zero value and must not be read. The returned pair must be the same
// length as the input; returning a nil mask means every output row is valid.
//
// vals ALIASES the engine's own buffer when the layout permits, so fn must not
// write to it. Build a new slice for the result.
//
// The notes on name, goroutine safety and out under MapElements apply here
// identically.
func (e Expr) MapBatches[In, Out Literal](name string, out DataType,
	fn func([]In, []bool) ([]Out, []bool, error)) Expr {

	if err := checkUDF(name, fn == nil, "map_batches"); err != nil {
		return wrap(&expr.Err{E: err})
	}

	impl := func(ctx context.Context, in *data.Column, outName string) (*data.Column, error) {
		src, err := udfInput[In](in, "map_batches", name)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n := in.Len()
		mask := make([]bool, n)
		v := in.Validity()
		for i := range n {
			mask[i] = v.Get(i)
		}

		vals, ok, err := fn(src, mask)
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindValue, "map_batches",
				"udf %q on column %q", name, in.Name())
		}
		if len(vals) != n {
			// Caught here as well as in evalUDF, because this message can say
			// which slice was wrong.
			return nil, uerr.New(uerr.KindValue, "map_batches",
				"udf %q returned %d values for %d rows", name, len(vals), n).
				Hint("the output slice is positional: one entry per input row")
		}
		if ok != nil && len(ok) != n {
			return nil, uerr.New(uerr.KindValue, "map_batches",
				"udf %q returned a %d-entry validity mask for %d rows",
				name, len(ok), n).
				Hint("return nil to declare every row valid")
		}

		valid := bitmap.NewBuilder(n)
		for i := range n {
			valid.Append(ok == nil || ok[i])
		}
		return buildTyped(name, "map_batches", outName, out, vals, valid.Finish())
	}
	return e.udf(name, "map_batches", out, impl)
}

// udf wraps the erased closure in a node. The one place the two methods converge.
func (e Expr) udf(name, kind string, out DataType, impl kernel.ColumnUDF) Expr {
	return wrap(&expr.UDF{
		Child: e.n,
		Out:   out,
		Name:  name,
		Kind:  kind,
		Impl:  impl,
	})
}

func checkUDF(name string, nilFn bool, kind string) error {
	if name == "" {
		return uerr.New(uerr.KindValue, kind, "a udf needs a name").
			Hint("the name distinguishes this function from every other udf in " +
				"the query; two udfs sharing one name become one computation")
	}
	if nilFn {
		return uerr.New(uerr.KindValue, kind, "udf %q has no function", name)
	}
	return nil
}

// udfInput reads a column as []In, positionally, with zero values at nulls.
//
// The fast path is the point. Column.FixedSlice hands back the payload with no
// copy and no per-row work whenever the physical layout already IS []In, which
// covers every numeric and temporal type. Falling through to Series.Get instead
// would cost an interface boxing per row — and "a per-element UDF in Go is a
// function call" is the whole claim this feature rests on.
//
// String and Bool take the slow path because neither is stored as a Go slice:
// strings are offsets plus a character buffer, bools are a packed bitmap.
func udfInput[In Literal](c *data.Column, kind, name string) ([]In, error) {
	if raw, ok := c.FixedSlice().([]In); ok {
		return raw, nil
	}
	s, err := data.TypedColumn[In](c)
	if err != nil {
		return nil, uerr.Wrap(err, uerr.KindType, kind,
			"udf %q cannot read column %q", name, c.Name())
	}
	n := c.Len()
	out := make([]In, n)
	for i := range n {
		// Get returns the zero value at a null, which is the documented contract
		// for both methods.
		out[i], _ = s.Get(i)
	}
	return out, nil
}

// buildTyped turns a []Out into a column of the type the caller DECLARED, rather
// than the one its Go type implies.
//
// valuesWith derives a dtype from the Go type — []int64 becomes Int64 — which is
// right for Values() and wrong here: a UDF may legitimately declare
// Datetime(Micro, "UTC") and return []int64, because that is the type's storage.
// So the natural column is built and then reinterpreted, and the reinterpretation
// is permitted only when the PHYSICAL layouts agree.
//
// That check is what stops `MapElements[float64, float64](…, ursus.Decimal(10,2), …)`
// — Decimal is stored as Int128, so the values would be garbage.
func buildTyped[Out Literal](udf, kind, name string, out DataType, vals []Out, valid bitmap.View) (*data.Column, error) {
	c := valuesWith(name, vals, valid)
	if c.DType() == out {
		return c, nil
	}
	if c.DType().Physical() != out.Physical() {
		return nil, uerr.New(uerr.KindType, kind,
			"udf %q declared %s but its function returns %s",
			udf, out, c.DType()).
			Hint("the declared output type must have the same storage as the Go "+
				"type the function returns; %s is stored as %s and %s as %s",
				c.DType(), c.DType().Physical(), out, out.Physical())
	}
	// An Enum's values are INDICES into its category list, so equal storage is not
	// sufficient — a returned 999 against three categories is out of range, and
	// nothing downstream would catch it.
	if out.ID() == dtype.TypeEnum {
		if err := checkEnumRange(c, out); err != nil {
			return nil, err
		}
	}
	return c.WithDType(out), nil
}

func checkEnumRange(c *data.Column, out DataType) error {
	idx, ok := c.FixedSlice().([]uint32)
	if !ok {
		return uerr.Internalf("udf: an Enum result is not stored as uint32")
	}
	n := len(out.Categories())
	valid := c.Validity()
	for i, v := range idx {
		if valid.Get(i) && int(v) >= n {
			return uerr.New(uerr.KindValue, "udf",
				"category index %d at row %d is out of range for %s", v, i, out).
				Hint("an Enum value is an index into its %d categories", n)
		}
	}
	return nil
}

// Package physical turns a logical plan into runnable operators, and expressions
// into columns.
package physical

import (
	"context"
	"regexp"
	"sync"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/uerr"
)

// Eval computes an expression over a batch, producing one column.
//
// # The contract
//
// For every resolved node n and every batch b:
//
//	Eval(ctx, n, b).DType() == n.Field(b.Schema()).Type
//
// That equality is what makes lazy schema resolution HONEST. CollectSchema
// promises a schema before any data is read; if the evaluator can produce a
// column of a different type, the promise is a lie and the failure surfaces far
// downstream as corrupted output. TestEvaluatorContract asserts it across the
// whole op × dtype matrix.
//
// Keeping the promise has one rule: the evaluator must not re-derive types. It
// asks expr.ResolveBinary for the operand and result types and obeys the answer.
// A second, independent copy of the promotion rules would drift, and the drift
// would be exactly this bug.
func Eval(ctx context.Context, n expr.Node, b *data.Batch) (*data.Column, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	switch t := n.(type) {
	case *expr.Col:
		c, ok := b.ByName(t.Name)
		if !ok {
			return nil, uerr.UnknownColumn("eval", t.Name, b.Schema().Names())
		}
		return c, nil

	case *expr.Lit:
		return litColumn(t)

	case *expr.Alias:
		c, err := Eval(ctx, t.Child, b)
		if err != nil {
			return nil, err
		}
		return c.Rename(t.Name), nil

	case *expr.Rename:
		c, err := Eval(ctx, t.Child, b)
		if err != nil {
			return nil, err
		}
		return c.Rename(t.Fn(c.Name())), nil

	case *expr.Cast:
		c, err := Eval(ctx, t.Child, b)
		if err != nil {
			return nil, err
		}
		return kernel.Cast(c.Name(), t.To, t.Strict, c)

	case *expr.Unary:
		return evalUnary(ctx, t, b)

	case *expr.Call:
		return evalCall(ctx, t, b)

	case *expr.Cond:
		return evalCond(ctx, t, b)

	case *expr.Binary:
		return evalBinary(ctx, t, b)

	case *expr.Err:
		return nil, t.E

	case *expr.Match:
		return nil, uerr.Internalf(
			"physical: expression reached the evaluator unexpanded: %s", t.String())

	case *expr.Window, *expr.WinFn:
		// A window is a pipeline breaker: it needs every row of a partition before it
		// can answer for any of them, so it cannot be evaluated against one batch.
		// Resolve lifts every window into a plan.Window node and leaves a reference
		// to a temporary behind, so reaching here means that extraction was missed —
		// an ursus bug, not a user error.
		return nil, uerr.Internalf(
			"physical: a window reached the evaluator; it should have been extracted "+
				"into a Window node: %s", n.String())

	default:
		return nil, uerr.Internalf("physical: no evaluator for %T", n)
	}
}

func evalUnary(ctx context.Context, u *expr.Unary, b *data.Batch) (*data.Column, error) {
	c, err := Eval(ctx, u.Child, b)
	if err != nil {
		return nil, err
	}
	out, err := expr.ResolveUnary(u.Op, c.DType())
	if err != nil {
		return nil, err
	}
	return kernel.Unary(u.Op, expr.OutputName(u), out, c)
}

// evalCond evaluates a conditional.
//
// Both branches are evaluated in full, for every row — there is no short-circuit,
// and there cannot be one in a columnar engine without materialising a selection
// and gathering twice, which costs more than it saves for cheap branches. That is
// only observable through side effects, and expressions have none: an arithmetic
// fault such as integer division by zero produces a NULL rather than a trap
// (scalar.go's divInt), so `When(x.Ne(0)).Then(y.Div(x)).Otherwise(0)` is safe
// even though y/x is computed at x == 0.
func evalCond(ctx context.Context, c *expr.Cond, b *data.Batch) (*data.Column, error) {
	pred, err := Eval(ctx, c.Pred, b)
	if err != nil {
		return nil, err
	}
	then, err := Eval(ctx, c.Then, b)
	if err != nil {
		return nil, err
	}
	els, err := Eval(ctx, c.Else, b)
	if err != nil {
		return nil, err
	}

	// An untyped null literal as the condition: Field admits it, meaning every row
	// takes neither branch, so it must reach the kernel as an all-null Bool rather
	// than as a Null column the mask check would reject. Cast cannot do this — a
	// data.NewNull has no payload to reinterpret.
	if pred.DType().IsNull() {
		if pred, err = kernel.NullColumn(pred.Name(), dtype.Bool, pred.Len()); err != nil {
			return nil, err
		}
	}

	// Obey the resolver rather than reproducing it, as evalBinary does.
	out, err := expr.ResolveCond(c.Then, c.Else, then.DType(), els.DType())
	if err != nil {
		return nil, err
	}
	// Strict casts: Promote only ever yields a type that holds both branches
	// exactly, so a value that fails to convert is a bug rather than a lossy
	// conversion the user asked for.
	//
	// Weak literal typing preserves that. It narrows only to a type ResolveCond has
	// already checked the literal's value fits EXACTLY, so the strict cast on the
	// length-1 literal column cannot fail. If it ever does, expr.FitsExactly and
	// kernel.narrow have diverged — which is what TestWeakFitAgreesWithCast exists
	// to prevent.
	if then.DType() != out {
		if then, err = kernel.Cast(then.Name(), out, true, then); err != nil {
			return nil, err
		}
	}
	if els.DType() != out {
		if els, err = kernel.Cast(els.Name(), out, true, els); err != nil {
			return nil, err
		}
	}

	return kernel.Select(expr.OutputName(c), pred, then, els)
}

func evalBinary(ctx context.Context, bn *expr.Binary, b *data.Batch) (*data.Column, error) {
	l, err := Eval(ctx, bn.L, b)
	if err != nil {
		return nil, err
	}
	r, err := Eval(ctx, bn.R, b)
	if err != nil {
		return nil, err
	}

	// The single source of truth for promotion. The evaluator's job is to obey it,
	// not to reproduce it.
	bind, err := expr.ResolveBinary(bn.Op, l.DType(), r.DType())
	if err != nil {
		return nil, err
	}

	if bind.NeedsCastL(l.DType()) {
		if l, err = kernel.Cast(l.Name(), bind.CastL, true, l); err != nil {
			return nil, err
		}
	}
	if bind.NeedsCastR(r.DType()) {
		if r, err = kernel.Cast(r.Name(), bind.CastR, true, r); err != nil {
			return nil, err
		}
	}

	return kernel.Binary(bn.Op, expr.OutputName(bn), bind.Out, l, r)
}

// litColumn materialises a literal as a length-1 column.
//
// Length 1 rather than a distinct Scalar type: broadcasting is then a property of
// the kernel dispatcher rather than a second code path through every operator, and
// `Col("x").Gt(5)` needs no special handling anywhere.
func litColumn(l *expr.Lit) (*data.Column, error) {
	if l.Value == nil {
		// NullColumn, not data.NewNull. NewNull carries no payload at all — "which
		// makes a typed null literal free" — and that is right for a value nobody
		// reads, but a literal is an OPERAND: it gets broadcast with Take, gathered
		// by a join, concatenated by a union. Every one of those reads the payload.
		//
		// `Select(ursus.Null(ursus.Float64))` failed with "has no fixed-width
		// payload" before this, which is the same distinction take.go records and
		// the fourth place it has bitten. The untyped-null case still costs nothing:
		// NullColumn returns data.NewNull for dtype.Null, because a column whose type
		// says there are no values has nothing to gather.
		return kernel.NullColumn("literal", l.DT, 1)
	}

	valid := bitmap.AllSet(1)
	switch v := l.Value.(type) {
	case bool:
		bits := bitmap.NewBuilder(1)
		bits.Append(v)
		return data.NewBool("literal", bits.Finish(), valid), nil
	case string:
		return data.NewString("literal", []string{v}, valid), nil
	case []byte:
		return data.NewString("literal", []string{string(v)}, valid).
			WithDType(dtype.Binary), nil
	case time.Time:
		return data.NewFixed("literal", l.DT, []int64{v.UnixNano()}, valid), nil

	case int8:
		return data.NewFixed("literal", l.DT, []int8{v}, valid), nil
	case int16:
		return data.NewFixed("literal", l.DT, []int16{v}, valid), nil
	case int32:
		return data.NewFixed("literal", l.DT, []int32{v}, valid), nil
	case int64:
		return data.NewFixed("literal", l.DT, []int64{v}, valid), nil
	case uint8:
		return data.NewFixed("literal", l.DT, []uint8{v}, valid), nil
	case uint16:
		return data.NewFixed("literal", l.DT, []uint16{v}, valid), nil
	case uint32:
		return data.NewFixed("literal", l.DT, []uint32{v}, valid), nil
	case uint64:
		return data.NewFixed("literal", l.DT, []uint64{v}, valid), nil
	case float32:
		return data.NewFixed("literal", l.DT, []float32{v}, valid), nil
	case float64:
		return data.NewFixed("literal", l.DT, []float64{v}, valid), nil

	default:
		return nil, uerr.New(uerr.KindValue, "",
			"unsupported literal type %T", l.Value)
	}
}

// buildInSet encodes is_in's literal arguments into a probe set, cast to the type
// of the column they will be compared against.
//
// Each value is materialised as a length-1 column and cast INDIVIDUALLY rather than
// as one batch, so the conversion goes through exactly the same kernel.Cast the
// rest of the engine uses and the encoding through exactly the same
// GroupKeyEncoder. Building the set any other way would be a second implementation
// of equality, and the two would eventually disagree — which for a membership test
// means rows quietly appearing or vanishing.
//
// The cast is STRICT. `Col("age").IsIn(int64(5000))` on an Int8 column is a
// question with no meaningful answer, and reporting "5000 is not representable as
// Int8" is more use than silently matching nothing.
func buildInSet(c *expr.Call, recvType dtype.DataType) (map[string]struct{}, error) {
	set := make(map[string]struct{}, len(c.Args)-1)
	for _, a := range c.Args[1:] {
		l, ok := a.(*expr.Lit)
		if !ok {
			return nil, uerr.Internalf("physical: is_in argument %s is not a literal", a)
		}
		col, err := litColumn(l)
		if err != nil {
			return nil, err
		}
		if col.DType() != recvType {
			col, err = kernel.Cast(col.Name(), recvType, true, col)
			if err != nil {
				return nil, err
			}
		}
		key, err := kernel.EncodeOne(col)
		if err != nil {
			return nil, err
		}
		set[string(key)] = struct{}{}
	}
	return set, nil
}

// callCache memoises one compiled regex per Call node.
//
// A pattern is a constant of the expression, so compiling it per batch would repeat
// identical work once per 8192 rows, and RE2 compilation costs orders of magnitude
// more than the match it enables. Keying on the node pointer is safe because plans
// are immutable — the pointer identifies the same pattern for the plan's lifetime.
//
// sync.Map rather than a plain map because parallelise hands BatchOp chains to N
// workers, so several goroutines evaluate the same Call node concurrently.
// regexp.Regexp is itself documented as safe for concurrent use.
var callCache sync.Map // *expr.Call -> compiledCall

type compiledCall struct {
	args []any
	re   *regexp.Regexp
	set  map[string]struct{} // is_in's probe set, encoded against the receiver's type
	// needle is list.contains's value, encoded against the ELEMENT type. Prepared
	// here for the reason the set is: the cast has to be strict and the encoding
	// has to match the child's, and doing it per batch would repeat both.
	needle []byte
	err    error
}

func evalCall(ctx context.Context, c *expr.Call, b *data.Batch) (*data.Column, error) {
	if len(c.Args) == 0 {
		return nil, uerr.Internalf("physical: %s has no receiver", c.Fn)
	}
	recv, err := evalColumn(ctx, c.Args[0], b)
	if err != nil {
		return nil, err
	}

	// The receiver's type is needed before compiling, because is_in encodes its
	// probe set against it. That type is fixed for the plan's lifetime, so the
	// cached entry stays valid.
	cc := compileCall(c, recv.DType())
	if cc.err != nil {
		return nil, cc.err
	}

	out, err := expr.ResolveCall(c, recv.DType())
	if err != nil {
		return nil, err
	}
	name := recv.Name()

	switch {
	case c.Fn.IsString():
		return kernel.StrCall(c.Fn, name, out, recv, cc.args, cc.re)
	case c.Fn.IsTemporal():
		return kernel.DtCall(c.Fn, name, out, recv, cc.args)
	case c.Fn == expr.FnIsIn:
		return kernel.InSet(name, recv, cc.set)
	case c.Fn.IsMath():
		return kernel.MathCall(c.Fn, name, out, recv, cc.args)
	case c.Fn.IsList():
		return kernel.ListCall(c.Fn, name, out, recv, cc.args, cc.needle)
	case c.Fn.IsStruct():
		return kernel.StructCall(c.Fn, name, recv, cc.args)
	default:
		return nil, uerr.Internalf("physical: no kernel for %s", c.Fn)
	}
}

func compileCall(c *expr.Call, recvType dtype.DataType) compiledCall {
	if v, ok := callCache.Load(c); ok {
		return v.(compiledCall)
	}
	var cc compiledCall
	cc.args, cc.err = expr.CallArgs(c)
	if cc.err == nil && c.Fn == expr.FnIsIn {
		cc.set, cc.err = buildInSet(c, recvType)
	}
	if cc.err == nil && c.Fn.IsString() {
		cc.re, cc.err = kernel.CompilePattern(c.Fn, cc.args)
	}
	if cc.err == nil && c.Fn == expr.FnListContains {
		cc.needle, cc.err = buildListNeedle(c, recvType)
	}
	callCache.Store(c, cc)
	return cc
}

// buildListNeedle encodes list.contains's value against the ELEMENT type.
//
// It is buildInSet for a single value, and strict for the same reason: comparing
// against a value the element type cannot hold is a question with no meaningful
// answer, and "5000 is not representable as Int8" is more use than silently
// matching nothing.
//
// The receiver here is the LIST, so the cast target is its Inner — getting that
// wrong would encode against List(Int8) and match nothing at all.
func buildListNeedle(c *expr.Call, recvType dtype.DataType) ([]byte, error) {
	if len(c.Args) < 2 {
		return nil, uerr.Internalf("physical: list.contains has no value argument")
	}
	l, ok := c.Args[1].(*expr.Lit)
	if !ok {
		return nil, uerr.Internalf("physical: list.contains argument %s is not a literal",
			c.Args[1])
	}
	col, err := litColumn(l)
	if err != nil {
		return nil, err
	}
	elem := recvType.Inner()
	if col.DType() != elem {
		if col, err = kernel.Cast(col.Name(), elem, true, col); err != nil {
			return nil, err
		}
	}
	return kernel.EncodeOne(col)
}

package kernel

import (
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow/memory"

	"ursus/dtype"
	"ursus/i128"
	"ursus/internal/arrowx"
	"ursus/internal/bitmap"
	"ursus/internal/data"
	"ursus/internal/expr"
	"ursus/internal/uerr"
)

// Binary applies op to two columns, producing a new column named name with type
// out (as already determined by expression type resolution).
//
// # This function owns null propagation
//
// Kernels compute values; the dispatcher decides which of those values are real.
// Centralising it here means the rule is written once and every kernel inherits it:
//
//	comparison / arithmetic  → valid_out = valid_a AND valid_b
//	missing comparison       → always valid
//	Kleene boolean           → computed by the kernel, because it is NOT va AND vb
//	partial (integer div)    → (va AND vb) AND the kernel's own ok mask
//
// Both operands must already be cast to the binding's operand type. Doing the
// casting in the evaluator rather than here keeps the dispatcher a pure function
// of physical types.
func Binary(op expr.BinaryOp, name string, out dtype.DataType, l, r *data.Column) (*data.Column, error) {
	n, err := broadcastLen(l, r)
	if err != nil {
		return nil, err
	}

	switch {
	case op.IsLogical():
		return kleene(op, name, l, r, n)
	case op.IsMissingComparison():
		return missingCompare(op, name, l, r, n)
	case op.IsComparison():
		return compare(op, name, l, r, n)
	default:
		return arithmetic(op, name, out, l, r, n)
	}
}

// broadcastLen returns the output length, allowing a length-1 column to broadcast.
//
// A literal evaluates to a length-1 column rather than to a special Scalar type.
// That keeps one code path — everything is a column — at the cost of a branch
// here, and it is what makes `Col("x").Gt(5)` work without a separate scalar
// kernel family.
func broadcastLen(l, r *data.Column) (int, error) {
	switch {
	case l.Len() == r.Len():
		return l.Len(), nil
	case l.Len() == 1:
		return r.Len(), nil
	case r.Len() == 1:
		return l.Len(), nil
	default:
		return 0, uerr.Internalf(
			"kernel: cannot combine columns of length %d and %d", l.Len(), r.Len())
	}
}

// combinedValidity is `va AND vb`, honouring broadcast.
//
// A broadcast null is total: if the scalar operand is null, every output row is
// null, which is why a length-1 invalid operand short-circuits to an all-zero mask.
func combinedValidity(l, r *data.Column, n int) bitmap.View {
	lv, rv := l.Validity(), r.Validity()
	if l.Len() == 1 && n != 1 {
		if !lv.Get(0) {
			return bitmap.Zeros(n)
		}
		lv = bitmap.AllSet(n)
	}
	if r.Len() == 1 && n != 1 {
		if !rv.Get(0) {
			return bitmap.Zeros(n)
		}
		rv = bitmap.AllSet(n)
	}
	return bitmap.And(lv, rv)
}

// --- comparison --------------------------------------------------------------

func compare(op expr.BinaryOp, name string, l, r *data.Column, n int) (*data.Column, error) {
	bits := bitmap.NewBuilder(n)

	if err := dispatchCompare(op, l, r, n, bits); err != nil {
		return nil, err
	}

	// The kernel wrote the VALUE bits. Validity is ours, and it is what makes
	// `null > 5` evaluate to null rather than to false.
	valid := combinedValidity(l, r, n)
	return data.NewBool(name, bits.Finish(), valid), nil
}

func dispatchCompare(op expr.BinaryOp, l, r *data.Column, n int, out *bitmap.Builder) error {
	p := l.DType().Physical()

	// String comparison has no numeric kernel; handle it before the numeric switch.
	if p.IsString() || p.ID() == dtype.TypeString {
		return compareStrings(op, l, r, n, out)
	}
	if p.ID() == dtype.TypeBool {
		return compareBools(op, l, r, n, out)
	}

	switch p.ID() {
	case dtype.TypeInt8:
		return cmpNum[int8](op, l, r, n, out)
	case dtype.TypeInt16:
		return cmpNum[int16](op, l, r, n, out)
	case dtype.TypeInt32:
		return cmpNum[int32](op, l, r, n, out)
	case dtype.TypeInt64:
		return cmpNum[int64](op, l, r, n, out)
	case dtype.TypeUint8:
		return cmpNum[uint8](op, l, r, n, out)
	case dtype.TypeUint16:
		return cmpNum[uint16](op, l, r, n, out)
	case dtype.TypeUint32:
		return cmpNum[uint32](op, l, r, n, out)
	case dtype.TypeUint64:
		return cmpNum[uint64](op, l, r, n, out)
	case dtype.TypeFloat32:
		return cmpNum[float32](op, l, r, n, out)
	case dtype.TypeFloat64:
		return cmpF64(op, l, r, n, out)
	case dtype.TypeInt128:
		return cmpI128(op, l, r, n, out)
	default:
		return uerr.New(uerr.KindUnsupported, "",
			"comparison is not implemented for %s", l.DType())
	}
}

// cmpF64 is the one type with hand-written SIMD kernels, so it goes through the
// dispatch vars rather than the generic scalar path.
func cmpF64(op expr.BinaryOp, l, r *data.Column, n int, out *bitmap.Builder) error {
	lv, err := data.Values[float64](l)
	if err != nil {
		return err
	}
	rv, err := data.Values[float64](r)
	if err != nil {
		return err
	}

	// The scalar-operand form is the common shape (`price > 5.0`) and the one with
	// a SIMD implementation, because broadcasting the constant once outside the
	// loop is what makes vectorising worthwhile.
	if r.Len() == 1 && n != 1 {
		s := rv[0]
		switch op {
		case expr.OpEq:
			cmpEqF64(out, lv, s)
		case expr.OpNe:
			cmpNeF64(out, lv, s)
		case expr.OpLt:
			cmpLtF64(out, lv, s)
		case expr.OpLe:
			cmpLeF64(out, lv, s)
		case expr.OpGt:
			cmpGtF64(out, lv, s)
		case expr.OpGe:
			cmpGeF64(out, lv, s)
		}
		return nil
	}
	return cmpNum[float64](op, l, r, n, out)
}

func cmpNum[T data.Primitive](op expr.BinaryOp, l, r *data.Column, n int, out *bitmap.Builder) error {
	lv, err := data.Values[T](l)
	if err != nil {
		return err
	}
	rv, err := data.Values[T](r)
	if err != nil {
		return err
	}

	// Broadcast by materialising the scalar side. Simple and correct; the float64
	// fast path above avoids it for the shape that matters.
	lv, rv = broadcastVals(lv, rv, n)

	switch op {
	case expr.OpEq:
		eqScalar(out, lv, rv)
	case expr.OpNe:
		neScalar(out, lv, rv)
	case expr.OpLt:
		ltScalar(out, lv, rv)
	case expr.OpLe:
		leScalar(out, lv, rv)
	case expr.OpGt:
		gtScalar(out, lv, rv)
	case expr.OpGe:
		geScalar(out, lv, rv)
	default:
		return uerr.Internalf("kernel: %s is not a comparison", op)
	}
	return nil
}

func broadcastVals[T data.Primitive](lv, rv []T, n int) ([]T, []T) {
	if len(lv) == 1 && n != 1 {
		v := lv[0]
		lv = make([]T, n)
		for i := range lv {
			lv[i] = v
		}
	}
	if len(rv) == 1 && n != 1 {
		v := rv[0]
		rv = make([]T, n)
		for i := range rv {
			rv[i] = v
		}
	}
	return lv, rv
}

func compareStrings(op expr.BinaryOp, l, r *data.Column, n int, out *bitmap.Builder) error {
	la, ra := l.Strings(), r.Strings()
	get := func(a data.StringAccessor, i, ln int) string {
		if ln == 1 {
			return a.Get(0)
		}
		return a.Get(i)
	}
	for i := range n {
		x, y := get(la, i, l.Len()), get(ra, i, r.Len())
		switch op {
		case expr.OpEq:
			out.Append(x == y)
		case expr.OpNe:
			out.Append(x != y)
		case expr.OpLt:
			out.Append(x < y)
		case expr.OpLe:
			out.Append(x <= y)
		case expr.OpGt:
			out.Append(x > y)
		case expr.OpGe:
			out.Append(x >= y)
		}
	}
	return nil
}

func compareBools(op expr.BinaryOp, l, r *data.Column, n int, out *bitmap.Builder) error {
	lb, rb := l.Bools(), r.Bools()
	get := func(v bitmap.View, i, ln int) bool {
		if ln == 1 {
			return v.Get(0)
		}
		return v.Get(i)
	}
	for i := range n {
		x, y := get(lb, i, l.Len()), get(rb, i, r.Len())
		switch op {
		case expr.OpEq:
			out.Append(x == y)
		case expr.OpNe:
			out.Append(x != y)
		case expr.OpLt:
			out.Append(!x && y)
		case expr.OpLe:
			out.Append(!x || y)
		case expr.OpGt:
			out.Append(x && !y)
		case expr.OpGe:
			out.Append(x || !y)
		}
	}
	return nil
}

// --- missing comparison ------------------------------------------------------

// missingCompare implements eq_missing / ne_missing: nulls are values, null equals
// null, and the result is never null.
func missingCompare(op expr.BinaryOp, name string, l, r *data.Column, n int) (*data.Column, error) {
	// Compute the ordinary comparison first, then patch the lanes involving nulls.
	inner := bitmap.NewBuilder(n)
	if err := dispatchCompare(expr.OpEq, l, r, n, inner); err != nil {
		return nil, err
	}
	eq := inner.Finish()

	lv, rv := l.Validity(), r.Validity()
	getv := func(v bitmap.View, i, ln int) bool {
		if ln == 1 {
			return v.Get(0)
		}
		return v.Get(i)
	}

	out := bitmap.NewBuilder(n)
	for i := range n {
		lok, rok := getv(lv, i, l.Len()), getv(rv, i, r.Len())
		var res bool
		switch {
		case !lok && !rok:
			res = true // null <=> null
		case lok != rok:
			res = false // null <=> value
		default:
			res = eq.Get(i)
		}
		if op == expr.OpNeMissing {
			res = !res
		}
		out.Append(res)
	}
	// No validity buffer: the answer is total by construction.
	return data.NewBool(name, out.Finish(), bitmap.AllSet(n)), nil
}

// --- Kleene ------------------------------------------------------------------

// kleene implements three-valued AND/OR/XOR.
//
// The truth tables that matter, and that `va AND vb` would get wrong:
//
//	false AND null == false   (valid, not null)
//	true  OR  null == true    (valid, not null)
//
// Getting this wrong is a silent filter bug: rows that should survive disappear.
func kleene(op expr.BinaryOp, name string, l, r *data.Column, n int) (*data.Column, error) {
	lb, rb := l.Bools(), r.Bools()
	lv, rv := l.Validity(), r.Validity()

	getb := func(v bitmap.View, i, ln int) bool {
		if ln == 1 {
			return v.Get(0)
		}
		return v.Get(i)
	}

	vals := bitmap.NewBuilder(n)
	valid := bitmap.NewBuilder(n)

	for i := range n {
		lok, rok := getb(lv, i, l.Len()), getb(rv, i, r.Len())
		// Canonicalise: Arrow does not guarantee the value bit of a null slot is
		// zero, so a null lane's payload must never be trusted.
		a := lok && getb(lb, i, l.Len())
		b := rok && getb(rb, i, r.Len())

		switch op {
		case expr.OpAnd:
			switch {
			case lok && rok:
				vals.Append(a && b)
				valid.Append(true)
			case (lok && !a) || (rok && !b):
				vals.Append(false) // false AND anything is false
				valid.Append(true)
			default:
				vals.Append(false)
				valid.Append(false)
			}
		case expr.OpOr:
			switch {
			case lok && rok:
				vals.Append(a || b)
				valid.Append(true)
			case (lok && a) || (rok && b):
				vals.Append(true) // true OR anything is true
				valid.Append(true)
			default:
				vals.Append(false)
				valid.Append(false)
			}
		case expr.OpXor:
			// XOR has no absorbing element, so a null operand always yields null.
			vals.Append(a != b)
			valid.Append(lok && rok)
		}
	}
	return data.NewBool(name, vals.Finish(), valid.Finish()), nil
}

// --- arithmetic --------------------------------------------------------------

func arithmetic(op expr.BinaryOp, name string, out dtype.DataType, l, r *data.Column, n int) (*data.Column, error) {
	p := out.Physical()
	valid := combinedValidity(l, r, n)

	switch p.ID() {
	case dtype.TypeInt8:
		return arithNum[int8](op, name, out, l, r, n, valid)
	case dtype.TypeInt16:
		return arithNum[int16](op, name, out, l, r, n, valid)
	case dtype.TypeInt32:
		return arithNum[int32](op, name, out, l, r, n, valid)
	case dtype.TypeInt64:
		return arithNum[int64](op, name, out, l, r, n, valid)
	case dtype.TypeUint8:
		return arithNum[uint8](op, name, out, l, r, n, valid)
	case dtype.TypeUint16:
		return arithNum[uint16](op, name, out, l, r, n, valid)
	case dtype.TypeUint32:
		return arithNum[uint32](op, name, out, l, r, n, valid)
	case dtype.TypeUint64:
		return arithNum[uint64](op, name, out, l, r, n, valid)
	case dtype.TypeFloat32:
		return arithFloat[float32](op, name, out, l, r, n, valid)
	case dtype.TypeFloat64:
		return arithFloat[float64](op, name, out, l, r, n, valid)
	case dtype.TypeInt128:
		return arithI128(op, name, out, l, r, n, valid)
	default:
		return nil, uerr.New(uerr.KindUnsupported, "",
			"arithmetic is not implemented for %s", out)
	}
}

// newValuesBuffer allocates a 64-byte-aligned output buffer and returns it
// together with a typed view of its contents.
//
// This is the documented route around arrow-go's sealed array.Builder interface:
// the kernel writes straight into the returned slice, and the buffer is wrapped as
// a Column with no copy. Builders would force an append-per-value API and an extra
// copy at the end.
func newValuesBuffer[T data.Fixed](n int) (*memory.Buffer, []T) {
	var z T
	buf := arrowx.NewBuffer(n * int(unsafe.Sizeof(z)))
	if n == 0 {
		return buf, nil
	}
	return buf, data.Reinterpret[T](buf.Bytes())[:n]
}

func arithNum[T Integer](op expr.BinaryOp, name string, out dtype.DataType,
	l, r *data.Column, n int, valid bitmap.View) (*data.Column, error) {

	lv, err := data.Values[T](l)
	if err != nil {
		return nil, err
	}
	rv, err := data.Values[T](r)
	if err != nil {
		return nil, err
	}
	lv, rv = broadcastVals(lv, rv, n)

	buf, dst := newValuesBuffer[T](n)

	switch op {
	case expr.OpAdd:
		addScalar(dst, lv, rv)
	case expr.OpSub:
		subScalar(dst, lv, rv)
	case expr.OpMul:
		mulScalar(dst, lv, rv)
	case expr.OpFloorDiv:
		// PARTIAL: integer division by zero panics in Go, so this kernel gets the
		// input validity and produces its own ok mask.
		ok := bitmap.NewBuilder(n)
		divIntScalar(dst, ok, lv, rv, valid)
		valid = bitmap.And(valid, ok.Finish())
	case expr.OpMod:
		ok := bitmap.NewBuilder(n)
		modIntScalar(dst, ok, lv, rv, valid)
		valid = bitmap.And(valid, ok.Finish())
	default:
		return nil, uerr.New(uerr.KindUnsupported, "",
			"operator %s is not implemented for %s", op, out)
	}
	return data.NewFixedBuffer(name, out, buf, n, valid), nil
}

func arithFloat[T ~float32 | ~float64](op expr.BinaryOp, name string, out dtype.DataType,
	l, r *data.Column, n int, valid bitmap.View) (*data.Column, error) {

	lv, err := data.Values[T](l)
	if err != nil {
		return nil, err
	}
	rv, err := data.Values[T](r)
	if err != nil {
		return nil, err
	}
	lv, rv = broadcastVals(lv, rv, n)

	buf, dst := newValuesBuffer[T](n)

	switch op {
	case expr.OpAdd:
		if d, ok := any(dst).([]float64); ok {
			arithAddF64(d, any(lv).([]float64), any(rv).([]float64))
		} else {
			addScalar(dst, lv, rv)
		}
	case expr.OpSub:
		subScalar(dst, lv, rv)
	case expr.OpMul:
		if d, ok := any(dst).([]float64); ok {
			arithMulF64(d, any(lv).([]float64), any(rv).([]float64))
		} else {
			mulScalar(dst, lv, rv)
		}
	case expr.OpDiv:
		// TOTAL for floats: IEEE division never traps, so x/0 is ±Inf and stays a
		// value rather than becoming a null. This differs from integer division on
		// purpose, and matches every other numeric library.
		divFloatScalar(dst, lv, rv)
	case expr.OpFloorDiv:
		divFloatScalar(dst, lv, rv)
		for i := range dst {
			dst[i] = T(int64(dst[i]))
		}
	case expr.OpPow:
		// Pow only ever arrives here: resolveArithmetic gives it OpDiv's binding, so
		// both operands are cast to a float before the dispatcher runs. math.Pow is
		// total, like math.Mod and IEEE division above — NaN or ±Inf, never a fault.
		powScalar(dst, lv, rv)
	case expr.OpMod:
		// resolveArithmetic has always accepted % on floats, and this arm has always
		// been missing — so Col("f64").Mod(Col("f64")) type-checked, planned, and
		// failed at execution. The same plan-accepts / kernel-rejects shape unaryArith
		// carried for abs, one function away.
		//
		// math.Mod is total, like IEEE division: mod by zero is NaN, not a trap.
		modFloatScalar(dst, lv, rv)
	default:
		return nil, uerr.New(uerr.KindUnsupported, "",
			"operator %s is not implemented for %s", op, out)
	}
	return data.NewFixedBuffer(name, out, buf, n, valid), nil
}

// cmpI128 compares Int128 columns. It cannot go through cmpNum because Int128 is a
// struct and Go's comparison operators are not defined for it; i128.Cmp is.
func cmpI128(op expr.BinaryOp, l, r *data.Column, n int, out *bitmap.Builder) error {
	lv, err := data.Values[i128.Int128](l)
	if err != nil {
		return err
	}
	rv, err := data.Values[i128.Int128](r)
	if err != nil {
		return err
	}
	get := func(v []i128.Int128, i, ln int) i128.Int128 {
		if ln == 1 {
			return v[0]
		}
		return v[i]
	}
	for i := range n {
		c := get(lv, i, l.Len()).Cmp(get(rv, i, r.Len()))
		switch op {
		case expr.OpEq:
			out.Append(c == 0)
		case expr.OpNe:
			out.Append(c != 0)
		case expr.OpLt:
			out.Append(c < 0)
		case expr.OpLe:
			out.Append(c <= 0)
		case expr.OpGt:
			out.Append(c > 0)
		case expr.OpGe:
			out.Append(c >= 0)
		}
	}
	return nil
}

// arithI128 implements Add/Sub on Int128 columns, so `sum(a) - sum(b)` works over
// an aggregated frame. Mul, division and modulo are deliberately absent: 128-bit
// multiply and divide are real algorithms, and neither appears in a query anyone
// writes over a sum. Cast to Int64 or Float64 first.
func arithI128(op expr.BinaryOp, name string, out dtype.DataType,
	l, r *data.Column, n int, valid bitmap.View) (*data.Column, error) {

	lv, err := data.Values[i128.Int128](l)
	if err != nil {
		return nil, err
	}
	rv, err := data.Values[i128.Int128](r)
	if err != nil {
		return nil, err
	}
	get := func(v []i128.Int128, i, ln int) i128.Int128 {
		if ln == 1 {
			return v[0]
		}
		return v[i]
	}

	buf, dst := newValuesBuffer[i128.Int128](n)
	for i := range n {
		a, b := get(lv, i, l.Len()), get(rv, i, r.Len())
		switch op {
		case expr.OpAdd:
			dst[i] = a.Add(b)
		case expr.OpSub:
			dst[i] = a.Sub(b)
		default:
			return nil, uerr.New(uerr.KindUnsupported, "",
				"operator %s is not implemented for Int128", op).
				Hint("cast to Int64 or Float64 first")
		}
	}
	return data.NewFixedBuffer(name, out, buf, n, valid), nil
}

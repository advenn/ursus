package kernel

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// Select is the masked merge behind When/Then/Otherwise: out[i] is a[i] where the
// mask is true and b[i] where it is false.
//
// # A null mask takes NEITHER branch
//
// This is the same rule SelectionFromMask applies for Filter — "a row survives iff
// its predicate is VALID AND TRUE" — and it is the one decision here that a reader
// might expect to go the other way. `When(x.Gt(5)).Then(a).Otherwise(b)` on a row
// where x is null produces NULL, not b. Treating a null predicate as false would
// make Otherwise silently absorb missing data, which is exactly the conflation of
// "absent" with "false" that Filter(p) and Filter(Not(p)) not partitioning exists
// to prevent.
//
// # The mask's payload bit is not trustworthy on its own
//
// Arrow does not guarantee the value bit of a null slot is zero, so every read of
// the mask is ANDed with its validity before use. dispatch.go's kleene kernel
// canonicalises for the same reason and says so.
//
// # Broadcasting is this kernel's job
//
// Eval returns whatever length a sub-expression produced, and litColumn produces
// length-1 columns; the operator-boundary broadcast in evalColumn runs afterwards,
// too late to help. So `When(c).Then(Lit(1)).Otherwise(Lit(0))` arrives here as a
// length-n mask and two length-1 branches, and this kernel repeats them — the same
// accommodation kernel.Binary makes via broadcastLen.
//
// a and b must already share a type: the evaluator casts them to the promoted type
// first, obeying the resolved binding rather than re-deriving it.
func Select(name string, mask, a, b *data.Column) (*data.Column, error) {
	if mask.DType().ID() != dtype.TypeBool {
		return nil, uerr.Internalf("kernel: select mask must be Bool, got %s", mask.DType())
	}
	if a.DType() != b.DType() {
		return nil, uerr.Internalf(
			"kernel: select branches must share a type, got %s and %s", a.DType(), b.DType())
	}

	n, err := selectLen(mask, a, b)
	if err != nil {
		return nil, err
	}

	// A payload-free null column cannot be read from. Lit(nil) becomes
	// data.NewNull, which carries no buffer at all, and every path below would
	// either error in data.Values or panic in StringAccessor.Get. NullColumn is the
	// operand-shaped null; this is the same distinction take.go records.
	if a, err = readable(a); err != nil {
		return nil, err
	}
	if b, err = readable(b); err != nil {
		return nil, err
	}

	mv, mb := mask.Validity(), mask.Bools()
	av, bv := a.Validity(), b.Validity()

	// takeA[i] reports whether row i reads from a. Rows where the mask is null read
	// from neither and are recorded as invalid up front.
	takeA := make([]bool, n)
	valid := bitmap.NewBuilder(n)
	anyNull := false
	for i := range n {
		mi := broadcastIdx(mask, i)
		switch {
		case !mv.Get(mi):
			takeA[i] = false // arbitrary; the row is null either way
			valid.Append(false)
			anyNull = true
		case mb.Get(mi):
			takeA[i] = true
			ok := av.Get(broadcastIdx(a, i))
			valid.Append(ok)
			anyNull = anyNull || !ok
		default:
			ok := bv.Get(broadcastIdx(b, i))
			valid.Append(ok)
			anyNull = anyNull || !ok
		}
	}
	outValid := valid.Finish()
	if !anyNull {
		// Drop the buffer when nothing is null, so downstream kernels take their
		// no-validity fast path. Take does the same.
		outValid = bitmap.AllSet(n)
	}

	// A row that reads from neither branch still needs SOME payload byte written,
	// because a null slot's value is unspecified but must exist. Reading a's row is
	// the cheapest choice and is never observed.
	switch {
	case a.DType().ID() == dtype.TypeBool:
		ab, bb := a.Bools(), b.Bools()
		out := bitmap.NewBuilder(n)
		for i := range n {
			if takeA[i] {
				out.Append(ab.Get(broadcastIdx(a, i)))
			} else {
				out.Append(bb.Get(broadcastIdx(b, i)))
			}
		}
		return data.NewBool(name, out.Finish(), outValid), nil

	case a.DType().HasStringStorage() || a.DType().IsString():
		aa, ba := a.Strings(), b.Strings()
		vals := make([]string, n)
		for i := range n {
			if takeA[i] {
				vals[i] = aa.Get(broadcastIdx(a, i))
			} else {
				vals[i] = ba.Get(broadcastIdx(b, i))
			}
		}
		return data.NewString(name, vals, outValid).WithDType(a.DType()), nil

	default:
		return selectFixed(name, takeA, a, b, n, outValid)
	}
}

// InSet tests each row for membership in a precomputed set of encoded values.
//
// # The set uses GROUPING equality, not IEEE equality
//
// Keys are produced by the same GroupKeyEncoder that n_unique, distinct and the
// join all use, which canonicalises floats through OrderKey: NaN equals NaN and
// -0.0 equals +0.0. An is_in built on `==` would disagree with Distinct and GroupBy
// about the same values, which is the trap nuniqueAcc's doc records for n_unique.
//
// # A null is never a member
//
// Output validity is the input's, so `null.IsIn(...)` is NULL rather than false —
// the rule every non-total unary follows, and the one that keeps null distinct from
// "absent from the set". The set itself cannot contain a null, because the public
// signature takes Go values and Go has no null.
func InSet(name string, c *data.Column, set map[string]struct{}) (*data.Column, error) {
	enc, err := NewGroupKeyEncoder("is_in", []*data.Column{c})
	if err != nil {
		return nil, err
	}
	valid := c.Validity()
	n := c.Len()
	bits := bitmap.NewBuilder(n)
	for i := range n {
		if !valid.Get(i) {
			bits.Append(false) // the value bit under a null is never read
			continue
		}
		_, ok := set[string(enc.Encode(i))]
		bits.Append(ok)
	}
	return data.NewBool(name, bits.Finish(), valid), nil
}

// EncodeOne returns the set key for the single row of a length-1 column.
//
// It exists so the probe set and the probed column are encoded by the SAME
// function. Building the set with any other encoding — even one that looks
// equivalent — would reintroduce exactly the NaN and -0.0 disagreements the shared
// encoder exists to prevent.
func EncodeOne(c *data.Column) ([]byte, error) {
	enc, err := NewGroupKeyEncoder("is_in", []*data.Column{c})
	if err != nil {
		return nil, err
	}
	// Encode aliases an internal buffer, so the caller gets a copy: this key is
	// about to be stored in a map that outlives the encoder.
	return append([]byte(nil), enc.Encode(0)...), nil
}

// selectLen resolves the output length, allowing any operand to be a length-1
// broadcast. It mirrors dispatch.go's broadcastLen, widened from two operands to
// three.
func selectLen(mask, a, b *data.Column) (int, error) {
	n := 1
	for _, c := range []*data.Column{mask, a, b} {
		if c.Len() == 1 {
			continue
		}
		if n != 1 && c.Len() != n {
			return 0, uerr.Internalf(
				"kernel: select operands have lengths %d, %d and %d",
				mask.Len(), a.Len(), b.Len())
		}
		n = c.Len()
	}
	return n, nil
}

func broadcastIdx(c *data.Column, i int) int {
	if c.Len() == 1 {
		return 0
	}
	return i
}

// readable turns a payload-free all-null column into one a kernel can gather from.
func readable(c *data.Column) (*data.Column, error) {
	if !c.IsPayloadFree() {
		return c, nil
	}
	return NullColumn(c.Name(), c.DType(), c.Len())
}

func selectFixed(name string, takeA []bool, a, b *data.Column, n int, valid bitmap.View,
) (*data.Column, error) {
	switch a.DType().Physical().ID() {
	case dtype.TypeInt8:
		return selectT[int8](name, takeA, a, b, n, valid)
	case dtype.TypeInt16:
		return selectT[int16](name, takeA, a, b, n, valid)
	case dtype.TypeInt32:
		return selectT[int32](name, takeA, a, b, n, valid)
	case dtype.TypeInt64:
		return selectT[int64](name, takeA, a, b, n, valid)
	case dtype.TypeUint8:
		return selectT[uint8](name, takeA, a, b, n, valid)
	case dtype.TypeUint16:
		return selectT[uint16](name, takeA, a, b, n, valid)
	case dtype.TypeUint32:
		return selectT[uint32](name, takeA, a, b, n, valid)
	case dtype.TypeUint64:
		return selectT[uint64](name, takeA, a, b, n, valid)
	case dtype.TypeFloat32:
		return selectT[float32](name, takeA, a, b, n, valid)
	case dtype.TypeFloat64:
		return selectT[float64](name, takeA, a, b, n, valid)
	case dtype.TypeInt128:
		return selectT[i128.Int128](name, takeA, a, b, n, valid)
	default:
		// Null on both sides is the one remaining case, and it has no payload to
		// merge: every row is null whichever branch it came from.
		if a.DType().IsNull() {
			return data.NewNull(name, a.DType(), n), nil
		}
		return nil, uerr.New(uerr.KindUnsupported, "",
			"a conditional is not implemented for %s", a.DType())
	}
}

func selectT[T data.Fixed](name string, takeA []bool, a, b *data.Column, n int, valid bitmap.View,
) (*data.Column, error) {
	av, err := data.Values[T](a)
	if err != nil {
		return nil, err
	}
	bv, err := data.Values[T](b)
	if err != nil {
		return nil, err
	}
	buf, dst := newValuesBuffer[T](n)
	for i := range n {
		if takeA[i] {
			dst[i] = av[broadcastIdx(a, i)]
		} else {
			dst[i] = bv[broadcastIdx(b, i)]
		}
	}
	return data.NewFixedBuffer(name, a.DType(), buf, n, valid), nil
}

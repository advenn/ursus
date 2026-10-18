package kernel

import (
	"encoding/binary"
	"math"
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// The binary namespace (step 274, B of v0.6.6-scope.md): Polars' .bin. A Binary
// column is stored as a String's is, offsets and bytes, so its accessor is
// Strings(), and every function here counts bytes.

// BinCall applies a binary function of one operand.
func BinCall(fn expr.CallFn, name string, out dtype.DataType, c *data.Column, args []any) (*data.Column, error) {
	if c.DType().IsNull() {
		return NullColumn(name, out, c.Len())
	}
	switch fn {
	case expr.FnBinEncode:
		enc, _ := argString(args, 0)
		return strEncode(name, c, enc)
	case expr.FnBinDecode:
		enc, _ := argString(args, 0)
		strict, _ := argBool(args, 1)
		return strDecode(name, c, enc, strict)
	case expr.FnBinSize:
		per, _ := argInt(args, 0)
		return binSize(name, c, per)
	case expr.FnBinReinterpret:
		endian, _ := argString(args, 0)
		return reinterpret(name, out, c, endian == "big")
	}
	return nil, uerr.Internalf("kernel: %s is not a binary function", fn)
}

// binSize is each value's length in bytes, a UInt32, or in units of per bytes, a
// Float64, as Polars' size: per is a power of 1024, so the quotient is exact.
func binSize(name string, c *data.Column, per int64) (*data.Column, error) {
	acc := c.Strings()
	n := c.Len()
	if per == 1 {
		lens := make([]uint32, n)
		for i := range n {
			if c.IsValid(i) {
				lens[i] = uint32(len(acc.Get(i)))
			}
		}
		return data.NewFixed(name, dtype.Uint32, lens, c.Validity()), nil
	}
	sizes := make([]float64, n)
	for i := range n {
		if c.IsValid(i) {
			sizes[i] = float64(len(acc.Get(i))) / float64(per)
		}
	}
	return data.NewFixed(name, dtype.Float64, sizes, c.Validity()), nil
}

// reinterpret reads each value's bytes as one value of out, little- or big-endian:
// Polars' reinterpret. A value of another length than out's is null, as Polars makes
// it.
func reinterpret(name string, out dtype.DataType, c *data.Column, big bool) (*data.Column, error) {
	var order binary.ByteOrder = binary.LittleEndian
	if big {
		order = binary.BigEndian
	}
	w := expr.ReinterpretWidth(out)
	acc := c.Strings()
	switch out.Physical().ID() {
	case dtype.TypeInt8:
		return reinterpretAs(name, out, c, acc, w, func(b string) int8 { return int8(b[0]) }), nil
	case dtype.TypeUint8:
		return reinterpretAs(name, out, c, acc, w, func(b string) uint8 { return b[0] }), nil
	case dtype.TypeInt16:
		return reinterpretAs(name, out, c, acc, w, func(b string) int16 { return int16(order.Uint16([]byte(b))) }), nil
	case dtype.TypeUint16:
		return reinterpretAs(name, out, c, acc, w, func(b string) uint16 { return order.Uint16([]byte(b)) }), nil
	case dtype.TypeInt32:
		return reinterpretAs(name, out, c, acc, w, func(b string) int32 { return int32(order.Uint32([]byte(b))) }), nil
	case dtype.TypeUint32:
		return reinterpretAs(name, out, c, acc, w, func(b string) uint32 { return order.Uint32([]byte(b)) }), nil
	case dtype.TypeInt64:
		return reinterpretAs(name, out, c, acc, w, func(b string) int64 { return int64(order.Uint64([]byte(b))) }), nil
	case dtype.TypeUint64:
		return reinterpretAs(name, out, c, acc, w, func(b string) uint64 { return order.Uint64([]byte(b)) }), nil
	case dtype.TypeFloat32:
		return reinterpretAs(name, out, c, acc, w, func(b string) float32 {
			return math.Float32frombits(order.Uint32([]byte(b)))
		}), nil
	case dtype.TypeFloat64:
		return reinterpretAs(name, out, c, acc, w, func(b string) float64 {
			return math.Float64frombits(order.Uint64([]byte(b)))
		}), nil
	case dtype.TypeInt128:
		return reinterpretAs(name, out, c, acc, w, func(b string) i128.Int128 {
			lo, hi := order.Uint64([]byte(b[:8])), order.Uint64([]byte(b[8:]))
			if big {
				lo, hi = hi, lo
			}
			return i128.Int128{Hi: int64(hi), Lo: lo}
		}), nil
	}
	return nil, uerr.Internalf("kernel: bin.reinterpret to %s", out)
}

func reinterpretAs[T data.Fixed](name string, out dtype.DataType, c *data.Column, acc data.StringAccessor, w int,
	read func(string) T) *data.Column {

	n := c.Len()
	res := make([]T, n)
	ok := bitmap.NewBuilder(n)
	for i := range n {
		if !c.IsValid(i) {
			ok.Append(false)
			continue
		}
		b := acc.Get(i)
		if len(b) != w {
			ok.Append(false)
			continue
		}
		res[i] = read(b)
		ok.Append(true)
	}
	return data.NewFixed(name, out, res, ok.Finish())
}

// binMatch is contains, starts_with or ends_with of a pattern a row: ops are the
// value and the pattern, bytes or text. A null pattern is null.
func binMatch(fn expr.CallFn, name string, ops []*data.Column, n int) (*data.Column, error) {
	v, p := ops[0], ops[1]
	if v.DType().IsNull() || p.DType().IsNull() {
		return NullColumn(name, dtype.Bool, n)
	}
	va, pa := v.Strings(), p.Strings()
	match := strings.Contains
	switch fn {
	case expr.FnBinStartsWith:
		match = strings.HasPrefix
	case expr.FnBinEndsWith:
		match = strings.HasSuffix
	}
	bits := bitmap.NewBuilder(n)
	ok := bitmap.NewBuilder(n)
	for i := range n {
		rv, rp := rowOf(v, i), rowOf(p, i)
		valid := v.IsValid(rv) && p.IsValid(rp)
		bits.Append(valid && match(va.Get(rv), pa.Get(rp)))
		ok.Append(valid)
	}
	return data.NewBool(name, bits.Finish(), ok.Finish()), nil
}

// binGet is each value's byte at an index a row, from the end where negative, a
// UInt8: ops are the value, the index and null_on_oob. An index past the value is
// refused, or null with null_on_oob, as Polars has it; a null index is null.
func binGet(name string, ops []*data.Column, n int) (*data.Column, error) {
	v := ops[0]
	if v.DType().IsNull() || ops[1].DType().IsNull() {
		return NullColumn(name, dtype.Uint8, n)
	}
	idx, err := asInt64(ops[1])
	if err != nil {
		return nil, err
	}
	nullOnOOB := ops[2].Bools().Get(0)
	acc := v.Strings()
	res := make([]uint8, n)
	ok := bitmap.NewBuilder(n)
	for i := range n {
		r := rowOf(v, i)
		k, kok := int64At(idx, i)
		if !v.IsValid(r) || !kok {
			ok.Append(false)
			continue
		}
		b := acc.Get(r)
		at := k
		if at < 0 {
			at += int64(len(b))
		}
		if at < 0 || at >= int64(len(b)) {
			if !nullOnOOB {
				return nil, uerr.New(uerr.KindValue, "bin.get",
					"bin.get's index %d is out of row %d's %d bytes", k, i, len(b)).
					Hint("null_on_oob makes it null")
			}
			ok.Append(false)
			continue
		}
		res[i] = b[at]
		ok.Append(true)
	}
	return data.NewFixed(name, dtype.Uint8, res, ok.Finish()), nil
}

// binSlice is each value's bytes by a slice, head or tail a row, a Binary: ops are
// the value, a, b and the mode, as expr.FnBinSlice's doc gives them. Polars' rules:
//
//   - slice: a negative offset counts from the end, and where it lands before the
//     start the length is cut by as much; a null length is the rest, and a null
//     offset is null;
//   - head and tail: n bytes from the start or the end, or where n is negative all
//     but |n| from the other end; a null n is null.
func binSlice(name string, ops []*data.Column, n int) (*data.Column, error) {
	v, a, b := ops[0], ops[1], ops[2]
	mode := ops[3].Strings().Get(0)
	if v.DType().IsNull() || a.DType().IsNull() {
		return NullColumn(name, dtype.Binary, n)
	}
	ac, err := asInt64(a)
	if err != nil {
		return nil, err
	}
	bc, err := asInt64(b)
	if err != nil {
		return nil, err
	}
	acc := v.Strings()
	res := make([]string, n)
	ok := bitmap.NewBuilder(n)
	for i := range n {
		r := rowOf(v, i)
		x, xok := int64At(ac, i)
		if !v.IsValid(r) || !xok {
			ok.Append(false)
			continue
		}
		s := acc.Get(r)
		size := int64(len(s))
		var lo, hi int64
		switch mode {
		case "head":
			lo, hi = 0, x
			if x < 0 {
				hi = size + x
			}
		case "tail":
			lo, hi = size-x, size
			if x < 0 {
				lo = -x
			}
		default:
			length, has := int64At(bc, i)
			if !has {
				length = math.MaxInt64
			}
			if length < 0 {
				return nil, uerr.New(uerr.KindValue, "bin.slice", "bin.slice's length %d at row %d is negative", length, i)
			}
			lo = x
			if lo < 0 {
				lo += size
				if lo < 0 {
					// Before the start: the length is cut by as much.
					length = max(0, length+lo)
					lo = 0
				}
			}
			hi = size
			if length < size-min(lo, size) {
				hi = lo + length
			}
		}
		lo, hi = max(0, min(lo, size)), max(0, min(hi, size))
		if hi < lo {
			hi = lo
		}
		res[i] = strings.Clone(s[lo:hi])
		ok.Append(true)
	}
	return data.NewString(name, res, ok.Finish()).WithDType(dtype.Binary), nil
}

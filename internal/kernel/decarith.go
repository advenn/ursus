package kernel

import (
	"math/big"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// decimalArith is + and - of two Decimals, exactly, into out.
//
// expr's resolveDecimal chose out: the finer operand's scale, and one integer digit
// more than the wider operand has, at most 38. Each row's operands are rescaled to
// out's scale and added in 128 bits, with every step checked; a row whose
// intermediate leaves 128 bits is redone in big.Int, because the SUM can still fit —
// 10^37 at scale 10 does not fit 128 bits, and 10^37 - 10^37 is zero. A result with
// more digits than out's precision is refused, naming the row: a Decimal past 38
// digits has no representation, and wrapping one is the silent wrong answer this
// replaces — `arithI128`'s unchecked Add, under a type one digit too narrow.
func decimalArith(op expr.BinaryOp, name string, out dtype.DataType,
	l, r *data.Column, n int, valid bitmap.View) (*data.Column, error) {

	lv, err := data.Values[i128.Int128](l)
	if err != nil {
		return nil, err
	}
	rv, err := data.Values[i128.Int128](r)
	if err != nil {
		return nil, err
	}
	at := func(v []i128.Int128, i int) i128.Int128 {
		if len(v) == 1 {
			return v[0]
		}
		return v[i]
	}
	s, prec := int(out.Scale()), int(out.Precision())
	ls, rs := s-int(l.DType().Scale()), s-int(r.DType().Scale())

	buf, dst := newValuesBuffer[i128.Int128](n)
	for i := range n {
		if !valid.Get(i) {
			continue
		}
		a, b := at(lv, i), at(rv, i)
		v, ok := decimalStep(op, a, ls, b, rs)
		if !ok || !withinDigits(v, prec) {
			if ok = decimalBig(op, a, ls, b, rs, prec, &v); !ok {
				return nil, decimalTooWide(op, out, l, r, i, a, b)
			}
		}
		dst[i] = v
	}
	return data.NewFixedBuffer(name, out, buf, n, valid), nil
}

// decimalStep is a*10^ls op b*10^rs in 128 bits, false where any step overflows.
func decimalStep(op expr.BinaryOp, a i128.Int128, ls int, b i128.Int128, rs int) (i128.Int128, bool) {
	a, ok := a.MulChecked(pow10[ls])
	if !ok {
		return a, false
	}
	b, ok = b.MulChecked(pow10[rs])
	if !ok {
		return b, false
	}
	if op == expr.OpSub {
		if b.Hi == -1<<63 && b.Lo == 0 {
			return b, false // -MinInt128 has no Int128
		}
		b = b.Neg()
	}
	v, carry := addCarry(a, b)
	return v, carry == 0
}

// decimalBig is decimalStep in big.Int, for a row whose intermediate left 128 bits,
// writing the result to v when it has at most prec digits.
func decimalBig(op expr.BinaryOp, a i128.Int128, ls int, b i128.Int128, rs int, prec int, v *i128.Int128) bool {
	big10 := func(k int) *big.Int { return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(k)), nil) }
	x, _ := new(big.Int).SetString(a.String(), 10)
	y, _ := new(big.Int).SetString(b.String(), 10)
	x.Mul(x, big10(ls))
	y.Mul(y, big10(rs))
	if op == expr.OpSub {
		x.Sub(x, y)
	} else {
		x.Add(x, y)
	}
	if new(big.Int).Abs(x).Cmp(big10(prec)) >= 0 {
		return false
	}
	r, ok := i128.Parse(x.String())
	*v = r
	return ok
}

// decimalTooWide refuses a result needing more digits than out holds.
func decimalTooWide(op expr.BinaryOp, out dtype.DataType, l, r *data.Column, row int,
	a, b i128.Int128) error {
	return uerr.New(uerr.KindValue, "arith",
		"%s %s %s at row %d needs more than the %d digits %s holds",
		dtype.FormatDecimal(a.String(), l.DType().Scale()), op,
		dtype.FormatDecimal(b.String(), r.DType().Scale()), row, out.Precision(), out).
		Hint("a Decimal holds at most %d digits; cast to Float64 for an approximate result",
			dtype.MaxDecimalPrecision)
}

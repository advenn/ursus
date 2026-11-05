package kernel

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// A Decimal rounded (step 490): Round, Floor and Ceil on its unscaled integers, and
// its Sign, exactly, into the type expr.DecimalRounded gives, Polars 2.0's.

// decMode is how decimalRound breaks the digits it drops.
type decMode uint8

const (
	decHalfAway decMode = iota // a tie away from zero, Round's default
	decHalfEven                // a tie to the even neighbour, RoundHalfToEven
	decFloor                   // toward −Inf
	decCeil                    // toward +Inf
)

// decimalRound is c, a Decimal, rounded to places decimal places by mode, at its own
// scale: 12.35 at one place is 12.40 half to even, 12.40 half away from zero. A value
// v is q·10^k + m, k the digits dropped, by Int128's floor DivMod, so m is in
// [0, 10^k); the answer is q or q+1, by m against 10^k − m, which cannot overflow
// where 2m can, at k of 38. A result past out's precision is refused, naming its row:
// at Decimal(38, s) the digit a carry needs has no room, as Polars refuses it.
func decimalRound(op, name string, out dtype.DataType, c *data.Column, places int, mode decMode) (*data.Column, error) {
	s := int(c.DType().Scale())
	if places >= s {
		// Nothing to drop: the type is the operand's, and so is every value.
		return c.Rename(name).WithDType(out), nil
	}
	v, err := data.Values[i128.Int128](c)
	if err != nil {
		return nil, err
	}
	unit, prec, valid := pow10[s-places], int(out.Precision()), c.Validity()
	buf, dst := newValuesBuffer[i128.Int128](len(v))
	for i, x := range v {
		if !valid.Get(i) {
			continue
		}
		q, m, _ := x.DivMod(unit)
		up := false
		switch mode {
		case decFloor:
		case decCeil:
			up = !m.IsZero()
		default:
			switch d := m.Cmp(unit.Sub(m)); {
			case d > 0:
				up = true
			case d == 0 && mode == decHalfAway:
				up = x.Sign() > 0 // q itself is away from zero below it
			case d == 0:
				up = q.Lo&1 == 1 // two's complement: the low bit is the parity
			}
		}
		if up {
			q = q.Add(i128.One)
		}
		r, ok := q.MulChecked(unit)
		if !ok || !withinDigits(r, prec) {
			return nil, uerr.New(uerr.KindValue, op,
				"%s of %s at row %d needs more than the %d digits %s holds",
				op, dtype.FormatDecimal(x.String(), uint8(s)), i, prec, out).
				Hint("a Decimal holds at most %d digits; cast to Float64 for an approximate result",
					dtype.MaxDecimalPrecision)
		}
		dst[i] = r
	}
	return data.NewFixedBuffer(name, out, buf, len(v), valid), nil
}

// decimalSign is −1, 0 or 1 at c's scale, 10^s unscaled, into out, which has an
// integer digit for it: Decimal(p+1, s) where c had none. Decimal(38, 38) has none to
// give, so a value of one other than 0 is refused, as Polars refuses it.
func decimalSign(name string, out dtype.DataType, c *data.Column) (*data.Column, error) {
	v, err := data.Values[i128.Int128](c)
	if err != nil {
		return nil, err
	}
	s := c.DType().Scale()
	one, valid := pow10[s], c.Validity()
	fits := withinDigits(one, int(out.Precision()))
	buf, dst := newValuesBuffer[i128.Int128](len(v))
	for i, x := range v {
		switch {
		case !valid.Get(i):
		case x.IsZero():
			dst[i] = i128.Zero
		case !fits:
			return nil, uerr.New(uerr.KindValue, "sign",
				"sign of %s at row %d is ±1, which %s cannot hold",
				dtype.FormatDecimal(x.String(), s), i, out).
				Hint("%s holds values below 1 only; cast to Float64 first for the sign as a float", out)
		case x.Sign() > 0:
			dst[i] = one
		default:
			dst[i] = one.Neg()
		}
	}
	return data.NewFixedBuffer(name, out, buf, len(v), valid), nil
}

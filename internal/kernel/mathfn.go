package kernel

import (
	"math"

	"ursus/dtype"
	"ursus/internal/data"
	"ursus/internal/expr"
	"ursus/internal/uerr"
)

// MathCall evaluates the parameterised maths family.
//
// One member today, Round. The family exists because a parameter belongs in a
// Call's argument list rather than in a field on expr.Unary — see the note on
// FnMathRound for the three hand-written Unary rebuilds that would have dropped it.
func MathCall(fn expr.CallFn, name string, out dtype.DataType,
	c *data.Column, args []any) (*data.Column, error) {

	switch fn {
	case expr.FnMathRound:
		d, err := intArg(fn, args, 0)
		if err != nil {
			return nil, err
		}
		return round(name, out, c, d)
	default:
		return nil, uerr.Internalf("kernel: no maths kernel for %s", fn)
	}
}

// round rounds to d decimal places, HALF AWAY FROM ZERO.
//
// # The tie-break is a decision, not an accident
//
// Go offers both: math.Round is half-away-from-zero, math.RoundToEven is banker's
// rounding. ursus takes half-away-from-zero, which is what Polars does and what a
// spreadsheet user expects — Round(0.5) is 1, and Round(2.5) is 3 rather than 2.
//
// # It rounds TWICE, and the second rounding is the one that surprises people
//
// Round(x, d) is math.Round(x * 10^d) / 10^d, so the scaling multiply rounds
// before math.Round ever sees the value. Round(2.675, 2) is 2.68 — not because
// 2.675 is exact (it is really 2.67499999999999982…) but because 2.675 * 100
// rounds to exactly 267.5, which then rounds away from zero to 268.
//
// So the answer is not "the decimal you wrote, rounded"; it is "the float you
// actually have, scaled and rounded". Every library that rounds binary floats
// this way lands in the same place, and the tests assert the specific values
// rather than papering over them.
func round(name string, out dtype.DataType, c *data.Column, d int) (*data.Column, error) {
	// Rounding an integer to any number of decimals is the identity, whatever the
	// width — including Int128, and including the unsigned types.
	if !out.IsFloat() {
		return c.Rename(name).WithDType(out), nil
	}

	scale := math.Pow(10, float64(d))
	src, err := toFloat64(c)
	if err != nil {
		return nil, err
	}
	dst := make([]float64, len(src))
	for i, v := range src {
		s := v * scale
		if math.IsInf(s, 0) || math.IsNaN(s) {
			// Either v was already NaN or ±Inf — both pass straight through — or the
			// scaling overflowed, which means d is larger than the value has
			// significant decimals and the value is already its own rounding.
			dst[i] = v
			continue
		}
		dst[i] = math.Round(s) / scale
	}
	return fromFloat64(name, out, dst, c.Validity(), false)
}

// intArg reads the i'th literal argument as an int.
//
// CallArgs has already established that every argument is a literal; this is the
// narrowing to a Go int, and it reports which function and which position rather
// than "expected int" so a mistake is diagnosable from the message alone.
func intArg(fn expr.CallFn, args []any, i int) (int, error) {
	if i >= len(args) {
		return 0, uerr.Internalf("kernel: %s needs argument %d, got %d", fn, i, len(args))
	}
	switch v := args[i].(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case int32:
		return int(v), nil
	default:
		return 0, uerr.New(uerr.KindType, "", "%s argument %d must be an integer, got %T",
			fn, i, args[i])
	}
}

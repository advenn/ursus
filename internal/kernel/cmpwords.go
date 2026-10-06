package kernel

import (
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// cmpWords appends a[i] op b[i] for n rows, sixty-four results to a word.
//
// # A scalar side is read, not copied
//
// A comparison with a literal arrives with that side one element long, and this
// used to copy it n times before comparing (broadcastVals): 190 MB of PDS-H q7's
// allocation at SF=1, for the date literals of one filter (step 101). A scalar on
// the right is now held in a register; one on the left swaps the operands and the
// operator, since c < x is x > c.
//
// # A word at a time
//
// The results were appended one bit per call. They are packed into a uint64 here
// and appended sixty-four at a time, LSB first, which is Arrow's bit order and the
// Builder's.
func cmpWords[T data.Primitive](out *bitmap.Builder, op expr.BinaryOp, a, b []T, n int) error {
	aScalar, bScalar := len(a) == 1 && n != 1, len(b) == 1 && n != 1
	switch {
	case aScalar && bScalar:
		// Both sides broadcast: one answer for every row.
		w := bitmap.NewBuilder(1)
		if err := cmpWords(w, op, a, b, 1); err != nil {
			return err
		}
		out.AppendMany(w.Finish().Get(0), n)
		return nil
	case aScalar:
		flipped, ok := flipComparison(op)
		if !ok {
			return uerr.Internalf("kernel: %s is not a comparison", op)
		}
		return cmpWords(out, flipped, b, a, n)
	}

	for i := 0; i < n; i += 64 {
		m := min(64, n-i)
		s := a[i : i+m]
		var w uint64
		if bScalar {
			c := b[0]
			switch op {
			case expr.OpEq:
				for j, x := range s {
					if x == c {
						w |= 1 << uint(j)
					}
				}
			case expr.OpNe:
				for j, x := range s {
					if x != c {
						w |= 1 << uint(j)
					}
				}
			case expr.OpLt:
				for j, x := range s {
					if x < c {
						w |= 1 << uint(j)
					}
				}
			case expr.OpLe:
				for j, x := range s {
					if x <= c {
						w |= 1 << uint(j)
					}
				}
			case expr.OpGt:
				for j, x := range s {
					if x > c {
						w |= 1 << uint(j)
					}
				}
			case expr.OpGe:
				for j, x := range s {
					if x >= c {
						w |= 1 << uint(j)
					}
				}
			default:
				return uerr.Internalf("kernel: %s is not a comparison", op)
			}
		} else {
			t := b[i : i+m]
			switch op {
			case expr.OpEq:
				for j, x := range s {
					if x == t[j] {
						w |= 1 << uint(j)
					}
				}
			case expr.OpNe:
				for j, x := range s {
					if x != t[j] {
						w |= 1 << uint(j)
					}
				}
			case expr.OpLt:
				for j, x := range s {
					if x < t[j] {
						w |= 1 << uint(j)
					}
				}
			case expr.OpLe:
				for j, x := range s {
					if x <= t[j] {
						w |= 1 << uint(j)
					}
				}
			case expr.OpGt:
				for j, x := range s {
					if x > t[j] {
						w |= 1 << uint(j)
					}
				}
			case expr.OpGe:
				for j, x := range s {
					if x >= t[j] {
						w |= 1 << uint(j)
					}
				}
			default:
				return uerr.Internalf("kernel: %s is not a comparison", op)
			}
		}
		out.AppendBits(w, m)
	}
	return nil
}

// flipComparison is the operator that gives the same answer with the operands
// exchanged: a < b is b > a.
func flipComparison(op expr.BinaryOp) (expr.BinaryOp, bool) {
	switch op {
	case expr.OpEq, expr.OpNe:
		return op, true
	case expr.OpLt:
		return expr.OpGt, true
	case expr.OpLe:
		return expr.OpGe, true
	case expr.OpGt:
		return expr.OpLt, true
	case expr.OpGe:
		return expr.OpLe, true
	}
	return op, false
}

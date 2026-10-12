package kernel

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/data"
)

// A narrow integer's sum and mean add in int64 (v0.6.3 step 215).
//
// An integer's sum and mean are exact: they accumulate in 128 bits. The mean widened
// every value to an Int128 first, a slice of sixteen bytes a row, and the sum to an
// int64 and then added it in 128 bits. In h2o gb4, a mean of three columns by a key of
// 100 groups, the widening alone was 15% of the CPU and the mean 25%.
//
// A value of 32 bits or fewer is under 2^32 in magnitude, so 2^30 of them sum to under
// 2^62 in an int64. So a narrow input adds into an int64 a group, read straight from
// the column, and the partials fold into the exact sums before 2^30 rows have been
// added since the last fold, and at Finish. Merge reads another's exact sum and its
// partial together, without writing it: a partitioned fold merges one worker into
// several partitions at once.

// narrowFold is how many rows the partials take before they fold into the exact sums.
// A variable, so a test can fold them often.
var narrowFold = 1 << 30

// isNarrowInt reports whether t is an integer of 32 bits or fewer. A Date, an int32
// underneath, is not: its mean is in microseconds, and a day of them overflows.
func isNarrowInt(t dtype.DataType) bool {
	switch t.ID() {
	case dtype.TypeInt8, dtype.TypeInt16, dtype.TypeInt32,
		dtype.TypeUint8, dtype.TypeUint16, dtype.TypeUint32:
		return true
	}
	return false
}

// narrowSums is a narrow integer input's partial sums, a group each, beside the exact
// ones they fold into.
type narrowSums struct {
	s    []int64
	pend int // rows added since the last fold
}

func (p *narrowSums) reserve(n int) { p.s = Extend(p.s, n, 0) }

// fold adds every partial into exact, and clears it.
func (p *narrowSums) fold(exact []i128.Int128) {
	for g, v := range p.s {
		if v != 0 {
			exact[g] = exact[g].Add(i128.FromInt64(v))
			p.s[g] = 0
		}
	}
	p.pend = 0
}

// at is group g's partial, which a merge adds to its exact sum without folding it.
func (p *narrowSums) at(g int) i128.Int128 {
	if g < len(p.s) {
		return i128.FromInt64(p.s[g])
	}
	return i128.Zero
}

// add adds col's valid values into the partials of their groups, marking seen or
// counting n for each, as the caller keeps them. It takes at most narrowFold rows at a
// time, and folds first wherever the rows since the last fold could carry a partial
// past the int64: a batch, too, may be longer than narrowFold.
func (p *narrowSums) add(exact []i128.Int128, groups []int32, col *data.Column, seen []bool, n []uint64) error {
	if col.IsPayloadFree() {
		return nil // every row is null
	}
	for lo := 0; lo < len(groups); lo += narrowFold {
		hi := min(lo+narrowFold, len(groups))
		if p.pend+hi-lo > narrowFold {
			p.fold(exact)
		}
		p.pend += hi - lo
		part := col
		if lo > 0 || hi < len(groups) {
			part = col.Slice(lo, hi-lo)
		}
		if err := p.addPart(groups[lo:hi], part, seen, n); err != nil {
			return err
		}
	}
	return nil
}

func (p *narrowSums) addPart(groups []int32, col *data.Column, seen []bool, n []uint64) error {
	switch col.DType().ID() {
	case dtype.TypeInt8:
		return addNarrow[int8](p.s, groups, col, seen, n)
	case dtype.TypeInt16:
		return addNarrow[int16](p.s, groups, col, seen, n)
	case dtype.TypeInt32:
		return addNarrow[int32](p.s, groups, col, seen, n)
	case dtype.TypeUint8:
		return addNarrow[uint8](p.s, groups, col, seen, n)
	case dtype.TypeUint16:
		return addNarrow[uint16](p.s, groups, col, seen, n)
	default:
		return addNarrow[uint32](p.s, groups, col, seen, n)
	}
}

func addNarrow[T int8 | int16 | int32 | uint8 | uint16 | uint32](s []int64, groups []int32, col *data.Column,
	seen []bool, n []uint64) error {
	vals, err := data.Values[T](col)
	if err != nil {
		return err
	}
	valid := col.Validity()
	all := valid.IsAllSet()
	for i, g := range groups {
		if all || valid.Get(i) {
			s[g] += int64(vals[i])
			if seen != nil {
				seen[g] = true
			}
			if n != nil {
				n[g]++
			}
		}
	}
	return nil
}

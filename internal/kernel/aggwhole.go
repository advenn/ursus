package kernel

import (
	"math/bits"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// WholeAdder is an Accumulator that can take a whole column into its group 0 at
// once, as a frame's aggregate of no keys feeds it: no group id a row, and the sums
// kept in locals (step 372, P4 of v0.6.8-scope.md). Whole says whether it can for
// the input it was built for; AddAll's answer is AddBatch's with every row in group
// 0, to the bit: a float's sum adds the same values in the same order.
type WholeAdder interface {
	Whole() bool
	AddAll(col *data.Column) error
}

// RowAdder is an accumulator that counts rows whatever they hold, Len's, and can be
// told their number without the column: a count of a literal evaluates nothing.
// AddRows is false where it counts by value.
type RowAdder interface {
	AddRows(n int) bool
}

// --- counts --------------------------------------------------------------------

func (a *countAcc) Whole() bool { return true }

func (a *countAcc) AddAll(col *data.Column) error {
	a.Reserve(1)
	switch a.mode {
	case countRows:
		a.n[0] += uint64(col.Len())
	case countValues:
		a.n[0] += uint64(col.Len() - col.NullCount())
	default: // countNulls
		a.n[0] += uint64(col.NullCount())
	}
	return nil
}

func (a *countAcc) AddRows(n int) bool {
	if a.mode != countRows {
		return false
	}
	a.Reserve(1)
	a.n[0] += uint64(n)
	return true
}

// --- sums and means ------------------------------------------------------------

// wholeInt reports whether t's values sum exactly through intWhole: an integer of 64
// bits or fewer, signed or not, or a Boolean; not a Date, whose mean is scaled.
func wholeInt(t dtype.DataType) bool {
	if t.ID() == dtype.TypeBool {
		return true
	}
	switch t.Physical().ID() {
	case dtype.TypeInt8, dtype.TypeInt16, dtype.TypeInt32, dtype.TypeInt64,
		dtype.TypeUint8, dtype.TypeUint16, dtype.TypeUint32, dtype.TypeUint64:
		return t.ID() != dtype.TypeDate
	}
	return false
}

// intWhole is the exact sum of col's valid values, and how many there are.
func intWhole(col *data.Column) (i128.Int128, int, error) {
	if col.IsPayloadFree() {
		return i128.Zero, 0, nil // every row is null
	}
	valid := col.Validity()
	if col.DType().ID() == dtype.TypeBool {
		t := col.Bools()
		if !valid.IsAllSet() {
			t = bitmap.And(t, valid)
		}
		return i128.FromInt64(int64(t.PopCount())), col.Len() - col.NullCount(), nil
	}
	switch col.DType().Physical().ID() {
	case dtype.TypeInt8:
		return signedWhole[int8](col, valid)
	case dtype.TypeInt16:
		return signedWhole[int16](col, valid)
	case dtype.TypeInt32:
		return signedWhole[int32](col, valid)
	case dtype.TypeInt64:
		return signedWhole[int64](col, valid)
	case dtype.TypeUint8:
		return unsignedWhole[uint8](col, valid)
	case dtype.TypeUint16:
		return unsignedWhole[uint16](col, valid)
	case dtype.TypeUint32:
		return unsignedWhole[uint32](col, valid)
	default:
		return unsignedWhole[uint64](col, valid)
	}
}

// signedWhole adds v's valid values in 128 bits by carries: the low word with its
// carry out, the high word the carries and each value's sign.
func signedWhole[T int8 | int16 | int32 | int64](col *data.Column, valid bitmap.View) (i128.Int128, int, error) {
	v, err := data.Values[T](col)
	if err != nil {
		return i128.Zero, 0, err
	}
	var lo, c uint64
	var hi int64
	if valid.IsAllSet() && len(v) < 1<<31 {
		// Each value as its high 32 bits, signed, and its low 32, apart: neither sum
		// can carry for fewer than 2^31 rows, and two adds a row with no carry
		// between them run as fast as the loads.
		var his, los [2]int64
		i := 0
		for ; i+1 < len(v); i += 2 {
			x, y := int64(v[i]), int64(v[i+1])
			his[0] += x >> 32
			los[0] += int64(uint32(x))
			his[1] += y >> 32
			los[1] += int64(uint32(y))
		}
		if i < len(v) {
			x := int64(v[i])
			his[0] += x >> 32
			los[0] += int64(uint32(x))
		}
		h, l := his[0]+his[1], uint64(los[0]+los[1])
		// h·2^32 + l, in 128 bits.
		return i128.Int128{Hi: h >> 32, Lo: uint64(h) << 32}.Add(i128.Int128{Lo: l}), len(v), nil
	}
	n := 0
	for i, x := range v {
		if valid.Get(i) {
			lo, c = bits.Add64(lo, uint64(int64(x)), 0)
			hi += int64(c) + int64(x)>>63
			n++
		}
	}
	return i128.Int128{Hi: hi, Lo: lo}, n, nil
}

func unsignedWhole[T uint8 | uint16 | uint32 | uint64](col *data.Column, valid bitmap.View) (i128.Int128, int, error) {
	v, err := data.Values[T](col)
	if err != nil {
		return i128.Zero, 0, err
	}
	var lo, c uint64
	var hi int64
	n := 0
	for i, x := range v {
		if valid.IsAllSet() || valid.Get(i) {
			lo, c = bits.Add64(lo, uint64(x), 0)
			hi += int64(c)
			n++
		}
	}
	return i128.Int128{Hi: hi, Lo: lo}, n, nil
}

// floatWhole adds col's valid values to sum, in row order, as AddBatch adds them to
// a group's, and counts them.
func floatWhole(col *data.Column, sum float64) (float64, int, error) {
	if col.IsPayloadFree() {
		return sum, 0, nil
	}
	valid := col.Validity()
	switch col.DType().Physical().ID() {
	case dtype.TypeFloat64:
		v, err := data.Values[float64](col)
		if err != nil {
			return sum, 0, err
		}
		if valid.IsAllSet() {
			for _, x := range v {
				sum += x
			}
			return sum, len(v), nil
		}
		n := 0
		for i, x := range v {
			if valid.Get(i) {
				sum += x
				n++
			}
		}
		return sum, n, nil
	default:
		v, err := widenFloat(col)
		if err != nil {
			return sum, 0, err
		}
		n := 0
		for i, x := range v {
			if valid.Get(i) {
				sum += x
				n++
			}
		}
		return sum, n, nil
	}
}

// Whole: an integer's sum of 64 bits or fewer, or a float's. A 128-bit one, a
// Decimal's or an Int128's, keeps its carry a row, through AddBatch.
func (a *sumAcc) Whole() bool {
	if a.isInt {
		return !a.wide && wholeInt(a.in)
	}
	return a.in.IsFloat() || a.in.IsInteger()
}

func (a *sumAcc) AddAll(col *data.Column) error {
	a.Reserve(1)
	if a.isInt {
		s, n, err := intWhole(col)
		if err != nil {
			return err
		}
		if n > 0 {
			a.i[0] = a.i[0].Add(s)
			a.seen[0] = true
		}
		return nil
	}
	f, n, err := floatWhole(col, a.f[0])
	if err != nil {
		return err
	}
	a.f[0] = f
	if n > 0 {
		a.seen[0] = true
	}
	return nil
}

// Whole: as sumAcc's, and not a Date's, whose mean is scaled to microseconds a row.
func (a *meanAcc) Whole() bool {
	if a.isInt {
		return !a.wide && wholeInt(a.in)
	}
	return a.in.IsFloat() || a.in.IsInteger()
}

func (a *meanAcc) AddAll(col *data.Column) error {
	a.Reserve(1)
	if a.isInt {
		s, n, err := intWhole(col)
		if err != nil {
			return err
		}
		a.i[0] = a.i[0].Add(s)
		a.n[0] += uint64(n)
		return nil
	}
	f, n, err := floatWhole(col, a.sum[0])
	if err != nil {
		return err
	}
	a.sum[0] = f
	a.n[0] += uint64(n)
	return nil
}

// --- minimum and maximum -------------------------------------------------------

// Whole: always; an integer's compared in place, a float's by its less.
func (a *extremumNum[T]) Whole() bool { return true }

func (a *extremumNum[T]) AddAll(col *data.Column) error {
	a.Reserve(1)
	if col.IsPayloadFree() {
		return nil
	}
	v, err := data.Values[T](col)
	if err != nil {
		return err
	}
	valid := col.Validity()
	best, seen := a.best[0], a.seen[0]
	if a.keyed && valid.IsAllSet() {
		switch v := any(v).(type) {
		case []float64:
			b, s := keyedExtreme(v, any(best).(float64), seen, a.max, OrderKeyF64)
			a.best[0], a.seen[0] = any(b).(T), s
			return nil
		case []float32:
			b, s := keyedExtreme(v, any(best).(float32), seen, a.max, OrderKeyF32)
			a.best[0], a.seen[0] = any(b).(T), s
			return nil
		}
	}
	if a.ordered && valid.IsAllSet() {
		// An integer's, with no null: the comparison is the type's own.
		for _, x := range v {
			switch {
			case !seen:
				best, seen = x, true
			case a.max && best < x, !a.max && x < best:
				best = x
			}
		}
	} else {
		for i, x := range v {
			switch {
			case !valid.Get(i):
			case !seen:
				best, seen = x, true
			case a.wins(x, best):
				best = x
			}
		}
	}
	a.best[0], a.seen[0] = best, seen
	return nil
}

// keyedExtreme is the least, or with max the greatest, of best and v by key, the
// first of equal keys kept, as lessFloat64's strict comparison keeps it: NaN the
// greatest, -0 and +0 alike.
func keyedExtreme[F float32 | float64, K uint32 | uint64](v []F, best F, seen, max bool, key func(F) K) (F, bool) {
	var bk K
	if seen {
		bk = key(best)
	}
	for _, x := range v {
		k := key(x)
		switch {
		case !seen:
			best, bk, seen = x, k, true
		case max && k > bk, !max && k < bk:
			best, bk = x, k
		}
	}
	return best, seen
}

// --- first and last ------------------------------------------------------------

func (a *positionAcc) Whole() bool { return true }

// AddAll keeps the first row of the first column it is given, or the last row of
// each, as AddBatch keeps a group's whatever the row holds.
func (a *positionAcc) AddAll(col *data.Column) error {
	a.Reserve(1)
	n := col.Len()
	if n == 0 || !a.last && a.at[0] != NullIndex {
		return nil
	}
	a.batch++
	row := int32(0)
	if a.last {
		row = int32(n - 1)
	}
	a.seen[0] = a.batch
	return a.place(col, []int32{row}, []int32{0})
}

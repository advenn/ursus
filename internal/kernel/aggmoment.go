package kernel

import (
	"math"
	"unsafe"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// Skew, kurtosis and the bitwise reductions (step 276, M1 of v0.6.6-scope.md).

// --- skew and kurtosis -----------------------------------------------------------

// momentAcc is skew and kurtosis: the second, third and fourth central moments of
// each group's non-null values, kept by Terriberry's online update of Welford's and
// merged by Pébay's pairwise formulas, so a group-by runs N-ways as var's does.
//
// Each group's values are shifted by its first, as varAcc's are and for the same
// reason: the moments of values whose spread is small beside their mean lose their
// low digits to the mean otherwise. A central moment does not move with a shift.
//
// Polars computes the moments in two passes, the mean first and then the mean of
// each deviation's power, so the answers agree to the last few bits, not to the bit.
type momentAcc struct {
	kurtosis bool
	fisher   bool
	bias     bool
	st       []momentState
}

type momentState struct {
	n          float64
	k          float64 // the shift: the group's first value
	mean       float64
	m2, m3, m4 float64 // sums of the deviations' powers
	seen       bool
}

func (a *momentAcc) Reserve(n int) { a.st = Extend(a.st, n, momentState{}) }

func (a *momentAcc) AddBatch(groups []int32, col *data.Column) error {
	vals, err := widenFloat(col)
	if err != nil {
		return err
	}
	valid := col.Validity()
	for i, g := range groups {
		if !valid.Get(i) {
			continue
		}
		s := &a.st[g]
		if !s.seen {
			s.k, s.seen = vals[i], true
		}
		x := vals[i] - s.k
		n1 := s.n
		s.n++
		n := s.n
		delta := x - s.mean
		dn := delta / n
		dn2 := dn * dn
		term := delta * dn * n1
		s.mean += dn
		s.m4 += term*dn2*(n*n-3*n+3) + 6*dn2*s.m2 - 4*dn*s.m3
		s.m3 += term*dn*(n-2) - 3*dn*s.m2
		s.m2 += term
	}
	return nil
}

// Merge is Pébay's: two groups' moments combine without their values.
func (a *momentAcc) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*momentAcc)
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into momentAcc", other)
	}
	a.Reserve(mergeCap(remap, len(o.st)))
	mergeEach(remap, len(o.st), func(dst, src int) {
		b, d := o.st[src], &a.st[dst]
		if !b.seen {
			return
		}
		if !d.seen {
			*d = b
			return
		}
		na, nb := d.n, b.n
		n := na + nb
		delta := b.mean + (b.k - d.k) - d.mean
		d2 := delta * delta
		m2 := d.m2 + b.m2 + d2*na*nb/n
		m3 := d.m3 + b.m3 + d2*delta*na*nb*(na-nb)/(n*n) + 3*delta*(na*b.m2-nb*d.m2)/n
		m4 := d.m4 + b.m4 + d2*d2*na*nb*(na*na-na*nb+nb*nb)/(n*n*n) +
			6*d2*(na*na*b.m2+nb*nb*d.m2)/(n*n) + 4*delta*(na*b.m3-nb*d.m3)/n
		d.mean += delta * nb / n
		d.m2, d.m3, d.m4, d.n = m2, m3, m4, n
	})
	return nil
}

// Finish is Polars' estimators, scipy's: the biased skew m3/m2^1.5 and kurtosis
// m4/m2², each moment the mean of the deviations' powers; unbiased, the skew times
// √(n(n-1))/(n-2), null below 3 values, and the kurtosis corrected, null below 4. Over
// values all alike the moments are 0 and the answer is NaN, as Polars', and a group of
// no value is null.
func (a *momentAcc) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	out := make([]float64, nGroups)
	ok := bitmap.NewBuilder(nGroups)
	for i, s := range a.st[:nGroups] {
		n := s.n
		if !s.seen || !a.bias && (!a.kurtosis && n < 3 || a.kurtosis && n < 4) {
			ok.Append(false)
			continue
		}
		m2, m3, m4 := s.m2/n, s.m3/n, s.m4/n
		var v float64
		if !a.kurtosis {
			v = m3 / math.Pow(m2, 1.5)
			if !a.bias {
				v *= math.Sqrt((n-1)*n) / (n - 2)
			}
		} else {
			v = m4 / (m2 * m2)
			if !a.bias {
				v = 3 + 1/(n-2)/(n-3)*((n*n-1)*v-3*(n-1)*(n-1))
			}
			if a.fisher {
				v -= 3
			}
		}
		out[i] = v
		ok.Append(true)
	}
	return data.NewFixed(name, dtype.Float64, out, ok.Finish()), nil
}

func (a *momentAcc) NBytes() int64 { return int64(cap(a.st)) * int64(unsafe.Sizeof(momentState{})) }

// --- bitwise and, or and xor -------------------------------------------------------

type bitOp uint8

const (
	bitAnd bitOp = iota
	bitOr
	bitXor
)

// bitAcc folds each group's non-null values by and, or or xor, in their bits, and
// gives the input's type: a group of no value is null, as Polars answers.
type bitAcc struct {
	op   bitOp
	in   dtype.DataType
	v    []uint64
	seen []bool
}

func (a *bitAcc) Reserve(n int) {
	a.v = Extend(a.v, n, 0)
	a.seen = Extend(a.seen, n, false)
}

func (a *bitAcc) fold(g int, x uint64) {
	if !a.seen[g] {
		a.v[g], a.seen[g] = x, true
		return
	}
	switch a.op {
	case bitAnd:
		a.v[g] &= x
	case bitOr:
		a.v[g] |= x
	default:
		a.v[g] ^= x
	}
}

func (a *bitAcc) AddBatch(groups []int32, col *data.Column) error {
	valid := col.Validity()
	var bits func(i int) uint64
	switch {
	case a.in.IsBool():
		b := col.Bools()
		bits = func(i int) uint64 {
			if b.Get(i) {
				return 1
			}
			return 0
		}
	case a.in.IsSignedInteger():
		v, err := widenToInt64(col)
		if err != nil {
			return err
		}
		bits = func(i int) uint64 { return uint64(v[i]) }
	default:
		v, err := widenToUint64(col)
		if err != nil {
			return err
		}
		bits = func(i int) uint64 { return v[i] }
	}
	for i, g := range groups {
		if valid.Get(i) {
			a.fold(int(g), bits(i))
		}
	}
	return nil
}

func (a *bitAcc) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*bitAcc)
	if !ok || o.op != a.op {
		return uerr.Internalf("kernel: cannot merge %T into bitAcc", other)
	}
	a.Reserve(mergeCap(remap, len(o.v)))
	mergeEach(remap, len(o.v), func(dst, src int) {
		if o.seen[src] {
			a.fold(dst, o.v[src])
		}
	})
	return nil
}

func (a *bitAcc) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	v, valid := a.v[:nGroups], seenBitmap(a.seen[:nGroups], nGroups)
	switch a.in.Physical().ID() {
	case dtype.TypeBool:
		b := bitmap.NewBuilder(nGroups)
		for _, x := range v {
			b.Append(x&1 == 1)
		}
		return data.NewBool(name, b.Finish(), valid), nil
	case dtype.TypeInt8:
		return data.NewFixed(name, a.in, narrowBits[int8](v), valid), nil
	case dtype.TypeInt16:
		return data.NewFixed(name, a.in, narrowBits[int16](v), valid), nil
	case dtype.TypeInt32:
		return data.NewFixed(name, a.in, narrowBits[int32](v), valid), nil
	case dtype.TypeInt64:
		return data.NewFixed(name, a.in, narrowBits[int64](v), valid), nil
	case dtype.TypeUint8:
		return data.NewFixed(name, a.in, narrowBits[uint8](v), valid), nil
	case dtype.TypeUint16:
		return data.NewFixed(name, a.in, narrowBits[uint16](v), valid), nil
	case dtype.TypeUint32:
		return data.NewFixed(name, a.in, narrowBits[uint32](v), valid), nil
	case dtype.TypeUint64:
		return data.NewFixed(name, a.in, append([]uint64(nil), v...), valid), nil
	}
	return nil, uerr.Internalf("kernel: bitwise reduction of %s", a.in)
}

func (a *bitAcc) NBytes() int64 { return int64(cap(a.v))*8 + int64(cap(a.seen)) }

// narrowBits is each value's low bits as T.
func narrowBits[T int8 | int16 | int32 | int64 | uint8 | uint16 | uint32](v []uint64) []T {
	out := make([]T, len(v))
	for i, x := range v {
		out[i] = T(x)
	}
	return out
}

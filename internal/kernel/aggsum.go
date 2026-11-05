package kernel

import (
	"fmt"
	"math"
	"math/big"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// --- sum ---------------------------------------------------------------------

// sumAcc accumulates integer sums at 128 bits and float sums at 64.
//
// `seen` tracks whether a group ever received a non-null value. Without it, sum of
// an all-null group would be the accumulator's zero, which is wrong: ursus follows
// SQL and returns NULL. The distinction is between "the values summed to zero" and
// "there was nothing to sum", and once it is collapsed to 0 it cannot be recovered.
//
// # A 128-bit INPUT carries
//
// The i128 package argues its wrapping Add is safe, because reaching 2^127 takes
// 2^63 rows of maximal Int64. That is true of every input of 64 bits or fewer, which
// keep the plain Add. It is false of an Int128 column, where two rows are enough —
// and wrapping can come all the way round: four values of 2^126+5 total 2^128+20,
// which wraps to 20, a number no check on the result can tell from a true 20.
//
// So a 128-bit input counts its carries: the true total is i + carry·2^128, and it
// is refused unless carry is zero. That is a statement about the TOTAL, not about
// the order the rows arrived in — a checked add that refused on the first wrap would
// accept [Max, 1, -1] in one order and refuse it in another, and a parallel merge
// does not fix the order.
type sumAcc struct {
	in     dtype.DataType
	bind   expr.AggBinding
	isInt  bool
	wide   bool // the input is 128 bits wide, so carry is kept
	narrow bool // the input is 32 bits or fewer, so it adds in int64 first

	i     []i128.Int128
	ni    narrowSums // a narrow input's partials, which fold into i
	carry []int64
	f     []float64
	seen  []bool
}

func newSumAcc(in dtype.DataType, bind expr.AggBinding) (Accumulator, error) {
	isInt := bind.Acc.ID() == dtype.TypeInt128
	return &sumAcc{in: in, bind: bind, isInt: isInt,
		wide:   isInt && in.Physical().ID() == dtype.TypeInt128,
		narrow: isInt && isNarrowInt(in)}, nil
}

func (a *sumAcc) Reserve(n int) {
	a.seen = Extend(a.seen, n, false)
	if a.isInt {
		a.i = Extend(a.i, n, i128.Zero)
		if a.wide {
			a.carry = Extend(a.carry, n, 0)
		}
		if a.narrow {
			a.ni.reserve(n)
		}
	} else {
		a.f = Extend(a.f, n, 0)
	}
}

// addCarry returns a+b wrapped to 128 bits, and the carry out of it: +1 if the
// true sum passed Max, -1 if it passed Min. The true sum is s + carry·2^128.
//
// Two operands of opposite sign cannot overflow, and two of the same sign have
// overflowed exactly when the wrapped result's sign differs from theirs.
func addCarry(a, b i128.Int128) (i128.Int128, int64) {
	s := a.Add(b)
	switch {
	case a.Hi >= 0 && b.Hi >= 0 && s.Hi < 0:
		return s, 1
	case a.Hi < 0 && b.Hi < 0 && s.Hi >= 0:
		return s, -1
	}
	return s, 0
}

func (a *sumAcc) AddBatch(groups []int32, col *data.Column) error {
	valid := col.Validity()

	if a.narrow && isNarrowInt(col.DType()) {
		return a.ni.add(a.i, groups, col, a.seen, nil)
	}
	if a.isInt {
		add := func(g int32, v i128.Int128) {
			a.i[g] = a.i[g].Add(v)
			a.seen[g] = true
		}
		switch {
		case col.DType().ID() == dtype.TypeBool:
			bits := col.Bools()
			for i, g := range groups {
				if valid.Get(i) {
					v := i128.Zero
					if bits.Get(i) {
						v = i128.FromInt64(1)
					}
					add(g, v)
				}
			}
		case col.DType().IsUnsignedInteger():
			src, err := widenUnsigned(col)
			if err != nil {
				return err
			}
			for i, g := range groups {
				if valid.Get(i) {
					add(g, i128.FromUint64(src[i]))
				}
			}
		case col.DType().Physical().ID() == dtype.TypeInt128:
			src, err := data.Values[i128.Int128](col)
			if err != nil {
				return err
			}
			for i, g := range groups {
				if valid.Get(i) {
					var c int64
					a.i[g], c = addCarry(a.i[g], src[i])
					a.carry[g] += c
					a.seen[g] = true
				}
			}
		default:
			src, err := widenSigned(col)
			if err != nil {
				return err
			}
			for i, g := range groups {
				if valid.Get(i) {
					add(g, i128.FromInt64(src[i]))
				}
			}
		}
		return nil
	}

	src, err := widenFloat(col)
	if err != nil {
		return err
	}
	for i, g := range groups {
		if valid.Get(i) {
			a.f[g] += src[i]
			a.seen[g] = true
		}
	}
	return nil
}

func (a *sumAcc) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*sumAcc)
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into sumAcc", other)
	}
	a.Reserve(mergeCap(remap, len(o.seen)))
	mergeEach(remap, len(o.seen), func(dst, src int) {
		if !o.seen[src] {
			return
		}
		switch {
		case a.wide:
			var c int64
			a.i[dst], c = addCarry(a.i[dst], o.i[src])
			a.carry[dst] += o.carry[src] + c
		case a.isInt:
			// The other's partial with its exact sum, read without folding it.
			a.i[dst] = a.i[dst].Add(o.i[src]).Add(o.ni.at(src))
		default:
			a.f[dst] += o.f[src]
		}
		a.seen[dst] = true
	})
	return nil
}

func (a *sumAcc) Finish(name string, nGroups int) (*data.Column, error) {
	if a.narrow {
		a.Reserve(nGroups)
		a.ni.fold(a.i)
	}
	a.Reserve(nGroups)
	valid := seenBitmap(a.seen, nGroups)

	if a.isInt {
		if a.wide {
			// A Decimal total must also fit its PRECISION, which is tighter than 128
			// bits: 10^38 - 1 is below 2^127, so a total can fit the accumulator and
			// still need a 39th digit.
			dec := a.bind.Out.ID() == dtype.TypeDecimal
			for g := range nGroups {
				if !a.seen[g] {
					continue // null: there is no total, and its slot is not checked
				}
				if a.carry[g] != 0 || (dec && !withinDigits(a.i[g], int(a.bind.Out.Precision()))) {
					return nil, sumOverflow(a.bind.Out, g, nGroups, a.i[g], a.carry[g])
				}
			}
		}
		if !a.bind.Out.IsTemporal() {
			return data.NewFixed(name, a.bind.Out, a.i[:nGroups], valid), nil
		}
		// A temporal output is NARROWER than its accumulator, so this is the
		// boundary i128.Int64's doc names: "where a 128-bit accumulator becomes a
		// user-visible number, and where an overflow that was impossible internally
		// must be reported rather than truncated."
		out := make([]int64, nGroups)
		for g := range nGroups {
			if !a.seen[g] {
				continue // null: there is no total to narrow
			}
			v, ok := a.i[g].Int64()
			if !ok {
				return nil, durationSumOverflow(a.bind.Out, g, nGroups, a.i[g])
			}
			out[g] = v
		}
		return data.NewFixed(name, a.bind.Out, out, valid), nil
	}
	if a.bind.Out == dtype.Float32 {
		out := make([]float32, nGroups)
		for i := range nGroups {
			out[i] = float32(a.f[i])
		}
		return data.NewFixed(name, a.bind.Out, out, valid), nil
	}
	return data.NewFixed(name, a.bind.Out, a.f[:nGroups], valid), nil
}

// durationSumOverflow refuses a group total that no longer fits the output type.
//
// Refusing rather than wrapping is what makes the aggregate agree with the
// arithmetic: Col("d").Add(Col("d")) has refused on this data since step 61, so a sum
// that quietly wrapped it would leave the two paths contradicting each other over the
// same values. The op string is "sum" so a caller can tell this refusal from an
// arithmetic one without matching on the message.
//
// The group ORDINAL is reported rather than the group's key: the key columns exist at
// the call site, but there is no value formatter outside the test helpers and
// inventing one inside an error path is a second subject. The ordinal is also not
// stable across a parallel merge or a spilled replay, which is why nothing asserts it.
func durationSumOverflow(out dtype.DataType, g, nGroups int, total i128.Int128) error {
	return uerr.New(uerr.KindValue, "sum",
		"the sum of group %d of %d is %s%s, which overflows %s",
		g, nGroups, total, out.TimeUnit(), out).
		Hint("%s holds tick counts from %s to %s", out,
			dtype.FormatTemporal(out, math.MinInt64),
			dtype.FormatTemporal(out, math.MaxInt64)).
		Hint("a coarser unit covers a wider span: cast the column with " +
			".Cast(ursus.Duration(ursus.Micro)) before summing").
		Hint("or sum the ticks themselves: .Cast(ursus.Int64).Sum() accumulates at " +
			"128 bits and returns an Int128, which cannot overflow")
}

// trueTotal is i + carry·2^128, the value a carried 128-bit sum stands for.
func trueTotal(i i128.Int128, carry int64) *big.Int {
	t, _ := new(big.Int).SetString(i.String(), 10)
	c := new(big.Int).Lsh(big.NewInt(carry), 128)
	return t.Add(t, c)
}

// sumOverflow refuses a group total of 128-bit values that its output cannot hold:
// past 128 bits for an Int128, past 38 digits for a Decimal.
func sumOverflow(out dtype.DataType, g, nGroups int, total i128.Int128, carry int64) error {
	t := trueTotal(total, carry).String()
	why := "values of 128 bits can sum past 128 bits"
	if out.ID() == dtype.TypeDecimal {
		t = dtype.FormatDecimal(t, out.Scale())
		why = fmt.Sprintf("a Decimal holds at most %d digits", dtype.MaxDecimalPrecision)
	}
	return uerr.New(uerr.KindValue, "sum",
		"the sum of group %d of %d is %s, which overflows %s", g, nGroups, t, out).
		Hint("%s; for an approximate total, sum a Float64: "+
			".Cast(ursus.Float64).Sum()", why)
}

// --- mean --------------------------------------------------------------------

// meanAcc divides the sum of non-null values by the COUNT of non-null values.
//
// Dividing by the group size instead would silently bias every mean downward
// whenever nulls are present, and the bias is invisible without a reference
// implementation to compare against.
//
// Mean of an all-null group is NULL, not NaN. 0/0 is the one place where ursus's
// null-versus-NaN distinction must not be allowed to blur: NaN would claim the
// mean was computed and came out undefined, when in fact there was nothing to
// compute.
//
// A Decimal takes the exact path too, and counts its carries as sumAcc does: its
// values are 128 bits wide, and its mean is a Float64 that exists even when the sum
// would not fit. Finish divides once — see finishDecimal.
type meanAcc struct {
	in     dtype.DataType
	bind   expr.AggBinding
	isInt  bool
	wide   bool // a 128-bit input: carry is kept
	narrow bool // an input of 32 bits or fewer: it adds in int64 first

	sum   []float64
	i     []i128.Int128
	ni    narrowSums // a narrow input's partials, which fold into i
	carry []int64
	n     []uint64
}

func newMeanAcc(in dtype.DataType, bind expr.AggBinding) (Accumulator, error) {
	isInt := bind.Acc.ID() == dtype.TypeInt128
	return &meanAcc{in: in, bind: bind, isInt: isInt,
		wide:   isInt && in.Physical().ID() == dtype.TypeInt128,
		narrow: isInt && isNarrowInt(in)}, nil
}

func (a *meanAcc) Reserve(n int) {
	a.n = Extend(a.n, n, 0)
	if a.isInt {
		a.i = Extend(a.i, n, i128.Zero)
		if a.wide {
			a.carry = Extend(a.carry, n, 0)
		}
		if a.narrow {
			a.ni.reserve(n)
		}
	} else {
		a.sum = Extend(a.sum, n, 0)
	}
}

func (a *meanAcc) AddBatch(groups []int32, col *data.Column) error {
	valid := col.Validity()
	if a.narrow && isNarrowInt(col.DType()) {
		return a.ni.add(a.i, groups, col, nil, a.n)
	}
	if a.isInt {
		// The exact path, for an output type that cannot absorb a rounding: a
		// Duration mean must equal sum // count, and the sum is exact.
		src, err := widenToInt128(col)
		if err != nil {
			return err
		}
		if a.in.ID() == dtype.TypeDate {
			// A Date's mean is a Datetime(us), in microseconds (step 173). A day's
			// worth of them times any int32 of days fits 128 bits.
			day := i128.FromInt64(microsPerDay)
			for i := range src {
				src[i], _ = src[i].MulChecked(day)
			}
		}
		for i, g := range groups {
			if valid.Get(i) {
				if a.wide {
					var c int64
					a.i[g], c = addCarry(a.i[g], src[i])
					a.carry[g] += c
				} else {
					a.i[g] = a.i[g].Add(src[i])
				}
				a.n[g]++
			}
		}
		return nil
	}

	src, err := widenFloat(col)
	if err != nil {
		return err
	}
	for i, g := range groups {
		if valid.Get(i) {
			a.sum[g] += src[i]
			a.n[g]++
		}
	}
	return nil
}

func (a *meanAcc) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*meanAcc)
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into meanAcc", other)
	}
	// Sum and count fold independently, which is what makes the mean mergeable at
	// all: averaging two averages would weight the smaller group equally.
	a.Reserve(mergeCap(remap, len(o.n)))
	mergeEach(remap, len(o.n), func(dst, src int) {
		switch {
		case a.wide:
			var c int64
			a.i[dst], c = addCarry(a.i[dst], o.i[src])
			a.carry[dst] += o.carry[src] + c
		case a.isInt:
			// The other's partial with its exact sum, read without folding it.
			a.i[dst] = a.i[dst].Add(o.i[src]).Add(o.ni.at(src))
		default:
			a.sum[dst] += o.sum[src]
		}
		a.n[dst] += o.n[src]
	})
	return nil
}

func (a *meanAcc) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	if a.narrow {
		a.ni.fold(a.i)
	}
	// An exact sum with a float output — a Decimal's, or an integer's — is divided
	// once, to the nearest double. An integer is a Decimal of scale 0.
	if a.in.ID() == dtype.TypeDecimal || (a.isInt && a.bind.Out.IsFloat()) {
		return a.finishDecimal(name, nGroups), nil
	}
	if a.isInt {
		return a.finishExact(name, nGroups)
	}

	vb := bitmap.NewBuilder(nGroups)
	out := make([]float64, nGroups)
	for i := range nGroups {
		if a.n[i] == 0 {
			vb.Append(false) // NULL, not NaN
			continue
		}
		out[i] = a.sum[i] / float64(a.n[i])
		vb.Append(true)
	}
	valid := vb.Finish()

	switch a.bind.Out {
	case dtype.Float32:
		f32 := make([]float32, nGroups)
		for i := range nGroups {
			f32[i] = float32(out[i])
		}
		return data.NewFixed(name, a.bind.Out, f32, valid), nil
	}
	return data.NewFixed(name, a.bind.Out, out, valid), nil
}

// finishExact divides an exact 128-bit sum by the count.
//
// It never refuses, and that is a theorem rather than a hope: for a non-empty group
// min <= mean <= max, and truncating a value inside [min, max] toward zero leaves it
// inside — so the mean of int64 values is always an int64. The !fits arm is a bug
// detector, not a user-facing refusal.
//
// It TRUNCATES toward zero, which is what Duration // Int64 does ("integer-truncating
// division") and what .dt.Total* does. Flooring is right for an INSTANT —
// rescaleTemporal's doc explains why — and wrong for a span: a Duration has a true
// origin at zero, so nothing collapses asymmetrically around it, and the symmetry
// that matters is mean(-x) == -mean(x).
func (a *meanAcc) finishExact(name string, nGroups int) (*data.Column, error) {
	vb := bitmap.NewBuilder(nGroups)
	out := make([]int64, nGroups)
	for g := range nGroups {
		if a.n[g] == 0 {
			vb.Append(false) // NULL, not NaN
			continue
		}
		q, _, ok := a.i[g].DivUint64(a.n[g])
		if !ok {
			return nil, uerr.Internalf("kernel: mean divided group %d by a zero count", g)
		}
		v, fits := q.Int64()
		if !fits {
			return nil, uerr.Internalf(
				"kernel: the mean of %s values is %s, which left int64", a.bind.Out, q)
		}
		out[g] = v
		vb.Append(true)
	}
	return data.NewFixed(name, a.bind.Out, out, vb.Finish()), nil
}

// finishDecimal is the mean of a Decimal: the exact sum divided by count * 10^scale,
// rounded ONCE, to the double nearest the true mean.
//
// Once matters. Converting the sum to a float and then dividing rounds twice, and
// can land a ulp away. So the fast path is the one where a single IEEE division is
// the whole computation: the sum below 2^53, and count * 10^scale an exact integer
// below 2^53, both exact doubles. Everything else — a big sum, a carry, a big count
// — divides exact rationals and rounds that.
func (a *meanAcc) finishDecimal(name string, nGroups int) *data.Column {
	scale := int(a.in.Scale())
	vb := bitmap.NewBuilder(nGroups)
	out := make([]float64, nGroups)
	for g := range nGroups {
		if a.n[g] == 0 {
			vb.Append(false) // NULL, not NaN
			continue
		}
		vb.Append(true)
		// The carry is kept only for a 128-bit input; an Int64 sum cannot leave
		// Int128 in fewer than 2^64 rows.
		var carry int64
		if a.wide {
			carry = a.carry[g]
		}
		if scale <= 22 && carry == 0 {
			d := float64(a.n[g]) * math.Pow10(scale)
			if x, ok := a.i[g].Int64(); ok && x >= -(1<<53) && x <= 1<<53 && d <= 1<<52 {
				out[g] = float64(x) / d
				continue
			}
		}
		den := new(big.Int).Mul(new(big.Int).SetUint64(a.n[g]),
			new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil))
		out[g], _ = new(big.Rat).SetFrac(trueTotal(a.i[g], carry), den).Float64()
	}
	return data.NewFixed(name, a.bind.Out, out, vb.Finish())
}

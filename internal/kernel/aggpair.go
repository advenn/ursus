package kernel

import (
	"math"
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// The aggregates of two inputs (step 175). Each reads expr.PairOf's struct: its
// first field and its second, a row counting only where the struct and the fields
// it reads are non-null.

// pairFields is a paired aggregate's two inputs.
func pairFields(col *data.Column) (a, b *data.Column, err error) {
	fs := col.Fields()
	if col.DType().ID() != dtype.TypeStruct || len(fs) != 2 {
		return nil, nil, uerr.Internalf("kernel: a paired aggregate over %s", col.DType())
	}
	return fs[0], fs[1], nil
}

// --- corr and cov --------------------------------------------------------------

// coMomentAcc is Corr's and Cov's state: per group, the pairs where both values
// are non-null, their means, and their co-moment, the sum of (x−x̄)(y−ȳ); and for
// Corr, each one's second moment as well.
//
// The update is Welford's, a pair at a time, as varAcc's is, and two groups' states
// merge by Chan's formulas, so Corr and Cov run on every worker, as Var does.
//
// Each group's values are shifted by its first pair, kx and ky, before they are
// accumulated, and its means are of the shifted values. Welford's alone lost eight
// digits of a correlation over values near 1e9 that vary by a few units, as
// epoch seconds do: the mean of 1e9+1, 1e9+2 and 1e9+4 rounds in its eighth
// significant digit of the spread. Shifted, the values are the spread itself.
type coMomentAcc struct {
	corr bool
	ddof uint8

	n           []uint64
	kx, ky      []float64 // the shift: the group's first pair
	mx, my, cxy []float64
	m2x, m2y    []float64 // Corr's only
}

func (a *coMomentAcc) Reserve(n int) {
	a.n = Extend(a.n, n, 0)
	a.kx = Extend(a.kx, n, 0)
	a.ky = Extend(a.ky, n, 0)
	a.mx = Extend(a.mx, n, 0)
	a.my = Extend(a.my, n, 0)
	a.cxy = Extend(a.cxy, n, 0)
	if a.corr {
		a.m2x = Extend(a.m2x, n, 0)
		a.m2y = Extend(a.m2y, n, 0)
	}
}

func (a *coMomentAcc) AddBatch(groups []int32, col *data.Column) error {
	fx, fy, err := pairFields(col)
	if err != nil {
		return err
	}
	xs, err := widenFloat(fx)
	if err != nil {
		return err
	}
	ys, err := widenFloat(fy)
	if err != nil {
		return err
	}
	sv, xv, yv := col.Validity(), fx.Validity(), fy.Validity()
	for i, g := range groups {
		if !sv.Get(i) || !xv.Get(i) || !yv.Get(i) {
			continue
		}
		if a.n[g] == 0 {
			a.kx[g], a.ky[g] = xs[i], ys[i]
		}
		x, y := xs[i]-a.kx[g], ys[i]-a.ky[g]
		a.n[g]++
		n := float64(a.n[g])
		dx, dy := x-a.mx[g], y-a.my[g]
		a.mx[g] += dx / n
		a.my[g] += dy / n
		// The old dx and the new mean, which is the update's exact form.
		a.cxy[g] += dx * (y - a.my[g])
		if a.corr {
			a.m2x[g] += dx * (x - a.mx[g])
			a.m2y[g] += dy * (y - a.my[g])
		}
	}
	return nil
}

// Merge is Chan's update for the co-moment, and varAcc's for the second moments.
func (a *coMomentAcc) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*coMomentAcc)
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into coMomentAcc", other)
	}
	if a.corr != o.corr || a.ddof != o.ddof {
		return uerr.Internalf("kernel: cannot merge corr/cov accumulators with different parameters")
	}
	a.Reserve(mergeCap(remap, len(o.n)))
	mergeEach(remap, len(o.n), func(dst, src int) {
		nb := o.n[src]
		if nb == 0 {
			return
		}
		na := a.n[dst]
		if na == 0 {
			a.n[dst], a.mx[dst], a.my[dst], a.cxy[dst] = nb, o.mx[src], o.my[src], o.cxy[src]
			a.kx[dst], a.ky[dst] = o.kx[src], o.ky[src]
			if a.corr {
				a.m2x[dst], a.m2y[dst] = o.m2x[src], o.m2y[src]
			}
			return
		}
		fa, fb := float64(na), float64(nb)
		total := fa + fb
		// o's means, moved to this group's shift.
		dx := o.mx[src] + (o.kx[src] - a.kx[dst]) - a.mx[dst]
		dy := o.my[src] + (o.ky[src] - a.ky[dst]) - a.my[dst]
		w := fa * fb / total
		a.cxy[dst] += o.cxy[src] + dx*dy*w
		if a.corr {
			a.m2x[dst] += o.m2x[src] + dx*dx*w
			a.m2y[dst] += o.m2y[src] + dy*dy*w
		}
		a.mx[dst] += dx * fb / total
		a.my[dst] += dy * fb / total
		a.n[dst] = na + nb
	})
	return nil
}

// Finish answers as Polars does. A correlation is never null: where it is undefined,
// with no pairs, one, or a variable that does not vary, it is 0/0, NaN. A covariance
// of ddof or fewer pairs is null, as Var is.
func (a *coMomentAcc) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	out := make([]float64, nGroups)
	if a.corr {
		for i := range nGroups {
			// Each moment rooted first, so their product cannot overflow where the
			// answer, at most 1, is well within range.
			out[i] = a.cxy[i] / (math.Sqrt(a.m2x[i]) * math.Sqrt(a.m2y[i]))
		}
		return data.NewFixed(name, dtype.Float64, out, bitmap.AllSet(nGroups)), nil
	}
	seen := make([]bool, nGroups)
	for i := range nGroups {
		if a.n[i] <= uint64(a.ddof) {
			continue
		}
		out[i] = a.cxy[i] / float64(a.n[i]-uint64(a.ddof))
		seen[i] = true
	}
	return data.NewFixed(name, dtype.Float64, out, seenBitmap(seen, nGroups)), nil
}

func (a *coMomentAcc) NBytes() int64 {
	return int64(cap(a.n)+cap(a.kx)+cap(a.ky)+cap(a.mx)+cap(a.my)+cap(a.cxy)+cap(a.m2x)+cap(a.m2y)) * 8
}

// --- min_by and max_by ---------------------------------------------------------

// byAcc is MinBy's and MaxBy's state: per group, the value at the row whose second
// field is the least, or the greatest, so far. The values are kept as positionAcc
// keeps First's and Last's, a part a batch with a row index a group, superseded
// rows compacted away. Beside each group's row is its key, typed (byKeys).
//
// The order is the total one Min, Max and Sort use, so NaN is the greatest value:
// MaxBy over a NaN answers the NaN's row, where Polars skips it, and
// v.MaxBy(by) is always v at by.ArgMax(). A tie keeps the earlier row, so the
// accumulator is order-dependent (expr.AggOp.IsBy).
type byAcc struct {
	max  bool
	keys byOrder
	rows positionAcc

	// cand[g] is group g's best row of the batch seen[g] names.
	cand  []int32
	seen  []int32
	batch int32
}

func newByAcc(in dtype.DataType, out dtype.DataType, max bool) (Accumulator, error) {
	fs := in.Fields()
	if in.ID() != dtype.TypeStruct || len(fs) != 2 {
		return nil, uerr.Internalf("kernel: min_by/max_by over %s", in)
	}
	keys, err := newByOrder(fs[1].Type)
	if err != nil {
		return nil, err
	}
	return &byAcc{max: max, keys: keys, rows: positionAcc{out: out}}, nil
}

func (a *byAcc) Reserve(n int) {
	a.rows.Reserve(n)
	a.keys.reserve(n)
	a.cand = Extend(a.cand, n, 0)
	a.seen = Extend(a.seen, n, 0)
}

// better reports whether c, a candidate's comparison with the incumbent, wins.
// Strictly, so a tie keeps the earlier row.
func (a *byAcc) better(c int) bool {
	if a.max {
		return c > 0
	}
	return c < 0
}

func (a *byAcc) AddBatch(groups []int32, col *data.Column) error {
	value, by, err := pairFields(col)
	if err != nil {
		return err
	}
	if err := a.keys.load(by); err != nil {
		return err
	}
	a.batch++
	sv, bv := col.Validity(), by.Validity()
	var touched []int32
	for i, g := range groups {
		if !sv.Get(i) || !bv.Get(i) {
			continue
		}
		if a.seen[g] != a.batch {
			a.seen[g], a.cand[g] = a.batch, int32(i)
			touched = append(touched, g)
		} else if a.better(a.keys.rows(i, int(a.cand[g]))) {
			a.cand[g] = int32(i)
		}
	}
	var pick, who []int32
	for _, g := range touched {
		i := int(a.cand[g])
		if a.rows.at[g] == NullIndex || a.better(a.keys.vsKept(i, g)) {
			a.keys.keep(i, g)
			pick, who = append(pick, int32(i)), append(who, g)
		}
	}
	return a.rows.place(value, pick, who)
}

// Merge folds in another accumulator that saw a LATER portion of the input, which
// is what a tie keeping this one's row relies on.
func (a *byAcc) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*byAcc)
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into byAcc", other)
	}
	if a.max != o.max {
		return uerr.Internalf("kernel: cannot merge min_by and max_by")
	}
	shift := a.rows.adopt(&o.rows)
	a.Reserve(mergeCap(remap, len(o.rows.at)))
	var ferr error
	mergeEach(remap, len(o.rows.at), func(dst, src int) {
		r := o.rows.at[src]
		if r == NullIndex || ferr != nil {
			return
		}
		if a.rows.at[dst] == NullIndex {
			a.rows.live++
		} else {
			c, err := a.keys.merged(o.keys, src, dst)
			if err != nil {
				ferr = err
				return
			}
			a.rows.dead++
			if !a.better(c) {
				return // this one's row stays; o's is never read
			}
		}
		a.rows.at[dst] = r + shift
		a.keys.adopt(o.keys, src, dst)
	})
	return ferr
}

func (a *byAcc) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	return a.rows.Finish(name, nGroups)
}

func (a *byAcc) NBytes() int64 {
	return a.rows.NBytes() + a.keys.nbytes() + int64(cap(a.cand)+cap(a.seen))*4
}

// byOrder holds MinBy's keys: a batch's, compared with each other, and each group's
// kept one, compared with the batch's and with another accumulator's.
type byOrder interface {
	load(by *data.Column) error
	rows(i, j int) int         // batch row i against batch row j
	vsKept(i int, g int32) int // batch row i against group g's kept key
	keep(i int, g int32)       // group g keeps batch row i's key
	merged(o byOrder, src, dst int) (int, error)
	adopt(o byOrder, src, dst int)
	reserve(n int)
	nbytes() int64
}

// newByOrder chooses the keys for a by column of type t, in the order
// valueComparator asks: an order key for a Boolean, an integer, a float or an
// instant; the text for a string; the value for an Int128 or a Decimal.
func newByOrder(t dtype.DataType) (byOrder, error) {
	switch {
	case t.ID() == dtype.TypeBool:
		return &byKeys[uint64]{cmp: cmpOrdered[uint64], read: orderKeys}, nil
	case t.HasStringStorage():
		return &byKeys[string]{cmp: strings.Compare, read: textKeys, own: strings.Clone, size: 16}, nil
	case t.Physical().ID() == dtype.TypeInt128:
		return &byKeys[i128.Int128]{cmp: i128.Int128.Cmp, read: data.Values[i128.Int128], size: 16}, nil
	}
	switch t.Physical().ID() {
	case dtype.TypeInt8, dtype.TypeInt16, dtype.TypeInt32, dtype.TypeInt64,
		dtype.TypeUint8, dtype.TypeUint16, dtype.TypeUint32, dtype.TypeUint64,
		dtype.TypeFloat32, dtype.TypeFloat64:
		return &byKeys[uint64]{cmp: cmpOrdered[uint64], read: orderKeys}, nil
	}
	return nil, uerr.Internalf("kernel: min_by/max_by cannot order a %s", t)
}

func cmpOrdered[T uint64](a, b T) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func orderKeys(c *data.Column) ([]uint64, error) {
	keys, ok, err := orderKey(c)
	if err == nil && !ok {
		err = uerr.Internalf("kernel: no order key for %s", c.DType())
	}
	return keys, err
}

func textKeys(c *data.Column) ([]string, error) {
	acc := c.Strings()
	out := make([]string, c.Len())
	for i := range out {
		out[i] = acc.Get(i)
	}
	return out, nil
}

// byKeys is byOrder over keys of one Go type.
type byKeys[T any] struct {
	cmp  func(a, b T) int
	read func(*data.Column) ([]T, error)
	own  func(T) T // a key kept past its batch: a string, which may share the batch's bytes, is cloned
	size int64     // bytes a key holds, for NBytes; 8 if zero

	batch []T
	kept  []T
}

func (k *byKeys[T]) load(by *data.Column) (err error) {
	k.batch, err = k.read(by)
	return err
}

func (k *byKeys[T]) rows(i, j int) int         { return k.cmp(k.batch[i], k.batch[j]) }
func (k *byKeys[T]) vsKept(i int, g int32) int { return k.cmp(k.batch[i], k.kept[g]) }

func (k *byKeys[T]) keep(i int, g int32) {
	v := k.batch[i]
	if k.own != nil {
		v = k.own(v)
	}
	k.kept[g] = v
}

func (k *byKeys[T]) merged(o byOrder, src, dst int) (int, error) {
	ok, isK := o.(*byKeys[T])
	if !isK {
		return 0, uerr.Internalf("kernel: cannot merge %T into %T", o, k)
	}
	return k.cmp(ok.kept[src], k.kept[dst]), nil
}

func (k *byKeys[T]) adopt(o byOrder, src, dst int) { k.kept[dst] = o.(*byKeys[T]).kept[src] }

func (k *byKeys[T]) reserve(n int) {
	var zero T
	k.kept = Extend(k.kept, n, zero)
}

func (k *byKeys[T]) nbytes() int64 {
	size := k.size
	if size == 0 {
		size = 8
	}
	return int64(cap(k.kept)) * size
}

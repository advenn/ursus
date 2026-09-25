package kernel_test

// Every integer -> integer cast is exact or refused, for every pair of widths.
//
// castTo widened every numeric cast through float64 "as the common currency", on
// the argument that the result was "exact for every remaining pair ... any value
// large enough to round is caught by the range check in narrow". It is not caught:
// narrow checks the value AFTER it has been rounded, against itself. So Int64 and
// Uint64 above 2^53 came back rounded, and MaxInt64 cast to Uint64 came back as
// 2^63 — one MORE than the input, which a range check on the output cannot see
// because 2^63 fits a Uint64.
//
// The pairs are derived rather than listed, and the values are every integer type's
// own boundaries plus 2^53 ± 1, so each pair is tried at each edge that any width
// has. The oracle is math/big, which shares no code with the kernel.

import (
	"errors"
	"math/big"
	"slices"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/uerr"
)

// integerTypes is every integer type a column can hold, derived from the TypeID
// enumeration. Int128 is added by hand because castTarget has no sample for it — the
// same gap expectedUnsampled records.
func integerTypes(t *testing.T) []dtype.DataType {
	t.Helper()
	var out []dtype.DataType
	for id := range int(dtype.TypeIDCount) {
		if dt, ok := castTarget(dtype.TypeID(id)); ok && dt.IsInteger() {
			out = append(out, dt)
		}
	}
	if !slices.Contains(out, dtype.Int128) {
		out = append(out, dtype.Int128)
	}
	if len(out) != 9 {
		t.Fatalf("%d integer types, want 9 — Int8..Int64, Uint8..Uint64, Int128", len(out))
	}
	return out
}

// intRange is the closed range of an integer type, from its width and sign.
func intRange(dt dtype.DataType) (lo, hi *big.Int) {
	w := uint(dt.BitWidth())
	one := big.NewInt(1)
	if dt.IsSignedInteger() {
		half := new(big.Int).Lsh(one, w-1)
		return new(big.Int).Neg(half), new(big.Int).Sub(half, one)
	}
	return big.NewInt(0), new(big.Int).Sub(new(big.Int).Lsh(one, w), one)
}

func inRange(v *big.Int, dt dtype.DataType) bool {
	lo, hi := intRange(dt)
	return v.Cmp(lo) >= 0 && v.Cmp(hi) <= 0
}

// edges is every boundary any integer type has — its min and max and one past each
// — plus 2^53 ± 1, where a float64 stops holding every integer, and 0 and ±1.
func edges(types []dtype.DataType) []*big.Int {
	var out []*big.Int
	add := func(v *big.Int) {
		for _, o := range out {
			if o.Cmp(v) == 0 {
				return
			}
		}
		out = append(out, v)
	}
	one := big.NewInt(1)
	for _, dt := range types {
		lo, hi := intRange(dt)
		add(lo)
		add(hi)
		add(new(big.Int).Sub(lo, one))
		add(new(big.Int).Add(hi, one))
	}
	f53 := new(big.Int).Lsh(one, 53)
	for _, v := range []*big.Int{
		new(big.Int).Add(f53, one), new(big.Int).Sub(f53, one),
		new(big.Int).Neg(new(big.Int).Add(f53, one)),
		big.NewInt(0), big.NewInt(1), big.NewInt(-1),
	} {
		add(v)
	}
	return out
}

// intColumn builds a column of type dt holding vals, every one of which is in range.
func intColumn(t *testing.T, dt dtype.DataType, vals []*big.Int) *data.Column {
	t.Helper()
	n := len(vals)
	v := bitmap.AllSet(n)
	switch dt.ID() {
	case dtype.TypeInt8:
		return data.NewFixed("c", dt, mapTo(vals, func(x *big.Int) int8 { return int8(x.Int64()) }), v)
	case dtype.TypeInt16:
		return data.NewFixed("c", dt, mapTo(vals, func(x *big.Int) int16 { return int16(x.Int64()) }), v)
	case dtype.TypeInt32:
		return data.NewFixed("c", dt, mapTo(vals, func(x *big.Int) int32 { return int32(x.Int64()) }), v)
	case dtype.TypeInt64:
		return data.NewFixed("c", dt, mapTo(vals, func(x *big.Int) int64 { return x.Int64() }), v)
	case dtype.TypeUint8:
		return data.NewFixed("c", dt, mapTo(vals, func(x *big.Int) uint8 { return uint8(x.Uint64()) }), v)
	case dtype.TypeUint16:
		return data.NewFixed("c", dt, mapTo(vals, func(x *big.Int) uint16 { return uint16(x.Uint64()) }), v)
	case dtype.TypeUint32:
		return data.NewFixed("c", dt, mapTo(vals, func(x *big.Int) uint32 { return uint32(x.Uint64()) }), v)
	case dtype.TypeUint64:
		return data.NewFixed("c", dt, mapTo(vals, func(x *big.Int) uint64 { return x.Uint64() }), v)
	case dtype.TypeInt128:
		return data.NewFixed("c", dt, mapTo(vals, func(x *big.Int) i128.Int128 {
			r, ok := i128.Parse(x.String())
			if !ok {
				t.Fatalf("i128.Parse(%s)", x)
			}
			return r
		}), v)
	}
	t.Fatalf("no column builder for %s", dt)
	return nil
}

func mapTo[T any](vals []*big.Int, f func(*big.Int) T) []T {
	out := make([]T, len(vals))
	for i, x := range vals {
		out[i] = f(x)
	}
	return out
}

// intValues reads an integer column back as big.Ints, nil for a null.
func intValues(t *testing.T, c *data.Column) []*big.Int {
	t.Helper()
	valid := c.Validity()
	out := make([]*big.Int, c.Len())
	read := func(i int, x *big.Int) {
		if valid.Get(i) {
			out[i] = x
		}
	}
	var err error
	switch c.DType().Physical().ID() {
	case dtype.TypeInt8:
		err = readInts[int8](c, func(i int, x int8) { read(i, big.NewInt(int64(x))) })
	case dtype.TypeInt16:
		err = readInts[int16](c, func(i int, x int16) { read(i, big.NewInt(int64(x))) })
	case dtype.TypeInt32:
		err = readInts[int32](c, func(i int, x int32) { read(i, big.NewInt(int64(x))) })
	case dtype.TypeInt64:
		err = readInts[int64](c, func(i int, x int64) { read(i, big.NewInt(x)) })
	case dtype.TypeUint8:
		err = readInts[uint8](c, func(i int, x uint8) { read(i, new(big.Int).SetUint64(uint64(x))) })
	case dtype.TypeUint16:
		err = readInts[uint16](c, func(i int, x uint16) { read(i, new(big.Int).SetUint64(uint64(x))) })
	case dtype.TypeUint32:
		err = readInts[uint32](c, func(i int, x uint32) { read(i, new(big.Int).SetUint64(uint64(x))) })
	case dtype.TypeUint64:
		err = readInts[uint64](c, func(i int, x uint64) { read(i, new(big.Int).SetUint64(x)) })
	case dtype.TypeInt128:
		err = readInts[i128.Int128](c, func(i int, x i128.Int128) {
			b, _ := new(big.Int).SetString(x.String(), 10)
			read(i, b)
		})
	default:
		t.Fatalf("cannot read %s back", c.DType())
	}
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func readInts[T data.Fixed](c *data.Column, f func(int, T)) error {
	vals, err := data.Values[T](c)
	if err != nil {
		return err
	}
	for i, x := range vals {
		f(i, x)
	}
	return nil
}

// knownInexactIntCasts names each pair that answers wrongly today, with what it
// answers. Emptied by the commit that makes integer casts exact.
var knownInexactIntCasts = map[string]string{
	"Int64->Uint64":  "MaxInt64 becomes 2^63; 2^53+1 becomes 2^53",
	"Uint64->Int64":  "MaxInt64 refused, or null under CastLossy; 2^53+1 becomes 2^53",
	"Int128->Int64":  "MaxInt64 refused; ±(2^53+1) become ±2^53",
	"Int128->Uint64": "MaxInt64 becomes 2^63; everything above it refused; 2^53+1 becomes 2^53",
}

func TestIntegerCastsAreExactOrRefused(t *testing.T) {
	types := integerTypes(t)
	all := edges(types)
	var pairs, values int

	for _, from := range types {
		var src []*big.Int
		for _, v := range all {
			if inRange(v, from) {
				src = append(src, v)
			}
		}
		col := intColumn(t, from, src)

		for _, to := range types {
			pairs++
			name := from.String() + "->" + to.String()
			var wrong []string

			// Lossy, the whole column at once: every value that fits comes back
			// EXACTLY, and every value that does not comes back null.
			got, err := kernel.Cast("c", to, false, col)
			if err != nil {
				t.Errorf("%s: CastLossy refused: %v", name, err)
				continue
			}
			for i, g := range intValues(t, got) {
				values++
				want := src[i]
				if !inRange(want, to) {
					want = nil
				}
				if (g == nil) != (want == nil) || (g != nil && g.Cmp(want) != 0) {
					wrong = append(wrong, src[i].String()+" -> "+render(g))
				}
			}

			// Strict, one value at a time: an unrepresentable value is refused
			// with ErrValue, never converted to something else.
			for _, v := range src {
				one := intColumn(t, from, []*big.Int{v})
				got, err := kernel.Cast("c", to, true, one)
				if inRange(v, to) {
					if err != nil {
						wrong = append(wrong, v.String()+" -> refused")
					}
					continue
				}
				if err == nil {
					wrong = append(wrong, v.String()+" -> "+render(intValues(t, got)[0])+" (strict)")
				} else if !errors.Is(err, uerr.ErrValue) {
					t.Errorf("%s: %s refused with the wrong kind: %v", name, v, err)
				}
			}

			why, known := knownInexactIntCasts[name]
			switch {
			case known && len(wrong) == 0:
				t.Errorf("%s is exact now; delete it from knownInexactIntCasts", name)
			case known:
				t.Logf("%s known inexact (%s): %v", name, why, wrong)
			case len(wrong) > 0:
				t.Errorf("%s: %v", name, wrong)
			}
		}
	}
	if pairs != 81 || values < 500 {
		t.Fatalf("%d pairs and %d values — the sweep has gone vacuous", pairs, values)
	}
}

func render(v *big.Int) string {
	if v == nil {
		return "null"
	}
	return v.String()
}

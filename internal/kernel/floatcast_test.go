package kernel_test

// Every cast to a float rounds once, to the nearest value of the target's width;
// only a finite value whose nearest float is an infinity is unrepresentable.
//
// Casts to a float went through a float64 and then narrow's round-trip test, which
// is the right test for a cast FROM a float and the wrong one for a cast TO one:
// rounding is what a float is. So 0.1 cast to Float32 was refused, every Int64
// above 2^24 cast to Float32 was refused, and what did convert rounded twice — once
// to a float64 and again to a float32, which is a different number whenever the
// first rounding lands on a float32 midpoint.
//
// The sources are every numeric and temporal type and a grid of Decimals; the values
// include, for each width, points a hair either side of the midpoints where double
// rounding goes wrong, derived from the widths rather than listed. The oracle is
// big.Rat's own nearest-float conversion, which shares no code with the kernel.

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/uerr"
)

// knownFloatCastDefects counts the wrong values per pair.
var knownFloatCastDefects = map[string]knownWrong{}

// srcVal is a source value: an exact rational, or a float special (NaN, ±Inf).
type srcVal struct {
	r *big.Rat
	f float64
}

func (v srcVal) String() string {
	if v.r == nil {
		return fmt.Sprint(v.f)
	}
	return v.r.RatString()
}

// nearest is the oracle: v rounded once to dt's width, and whether that is an
// overflow — a finite value whose nearest float is an infinity.
func nearest(v srcVal, dt dtype.DataType) (float64, bool) {
	if v.r == nil {
		return v.f, false
	}
	var f float64
	if dt.ID() == dtype.TypeFloat32 {
		f32, _ := v.r.Float32()
		f = float64(f32)
	} else {
		f, _ = v.r.Float64()
	}
	return f, math.IsInf(f, 0)
}

func pow2(k int) *big.Int { return new(big.Int).Lsh(big.NewInt(1), uint(k)) }

// roundingBoundaries are, for each float width p and binade 2^k, the integers at
// and one either side of the midpoint between two adjacent floats — the half-ulp
// 2^(k-p) above 2^k and above 2^k+2^(k-1) — with both signs.
func roundingBoundaries() []*big.Int {
	var out []*big.Int
	for _, p := range []int{24, 53} {
		for _, k := range []int{24, 25, 31, 32, 53, 54, 62, 63, 64, 65, 100, 126} {
			if k < p {
				continue
			}
			half := pow2(k - p)
			for _, base := range []*big.Int{pow2(k), new(big.Int).Add(pow2(k), pow2(k-1))} {
				for _, d := range []int64{-1, 0, 1} {
					v := new(big.Int).Add(new(big.Int).Add(base, half), big.NewInt(d))
					out = append(out, v, new(big.Int).Neg(v))
				}
			}
		}
	}
	return out
}

// float64Edges are the float64 values around every Float32 limit.
func float64Edges() []float64 {
	t := 0x1p128 - 0x1p103 // halfway from MaxFloat32 to 2^128: the overflow threshold
	return []float64{
		math.MaxFloat32, -math.MaxFloat32, math.Nextafter(math.MaxFloat32, math.Inf(1)),
		t, -t, math.Nextafter(t, 0), math.Nextafter(t, math.Inf(1)), 0x1p128, 1e39, -1e39,
		math.MaxFloat64, math.SmallestNonzeroFloat32, math.SmallestNonzeroFloat32 / 2,
		1 + 0x1p-24, 1 + 0x1p-24 + 0x1p-52, 0.1, -0.1, 1.5, 0, math.Copysign(0, -1),
		0x1p63, 0x1p64, 16777217, math.NaN(), math.Inf(1), math.Inf(-1),
	}
}

// floatSources is every source type with the values it is swept with.
func floatSources(t *testing.T) map[string]struct {
	dt   dtype.DataType
	vals []srcVal
	col  *data.Column
} {
	t.Helper()
	out := map[string]struct {
		dt   dtype.DataType
		vals []srcVal
		col  *data.Column
	}{}
	put := func(dt dtype.DataType, vals []srcVal, col *data.Column) {
		out[dt.String()] = struct {
			dt   dtype.DataType
			vals []srcVal
			col  *data.Column
		}{dt, vals, col}
	}
	ints := integerTypes(t)
	candidates := append(edges(ints), roundingBoundaries()...)
	for _, dt := range append(ints, temporalTypes()...) {
		phys := dt.Physical()
		var vs []*big.Int
		seen := map[string]bool{}
		for _, v := range candidates {
			if inRange(v, phys) && !seen[v.String()] {
				seen[v.String()] = true
				vs = append(vs, v)
			}
		}
		vals := make([]srcVal, len(vs))
		for i, v := range vs {
			vals[i] = srcVal{r: new(big.Rat).SetInt(v)}
		}
		put(dt, vals, tickColumn(t, dt, vs))
	}

	f64 := float64Edges()
	vals := make([]srcVal, len(f64))
	for i, f := range f64 {
		vals[i] = floatVal(f)
	}
	put(dtype.Float64, vals, data.NewFixed("c", dtype.Float64, f64, bitmap.AllSet(len(f64))))
	f32 := []float32{0.1, math.MaxFloat32, math.SmallestNonzeroFloat32, 1 + 0x1p-23, 16777216,
		float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1)), 0, -1.5}
	vals = make([]srcVal, len(f32))
	for i, f := range f32 {
		vals[i] = floatVal(float64(f))
	}
	put(dtype.Float32, vals, data.NewFixed("c", dtype.Float32, f32, bitmap.AllSet(len(f32))))

	for _, dt := range append(append([]dtype.DataType(nil), decGrid...), dtype.Decimal(38, 24)) {
		rs := sourceValues(dt, ints)
		ten := new(big.Rat).SetInt(pow10Big(int(dt.Scale())))
		seen := map[string]bool{}
		for _, r := range rs {
			seen[r.RatString()] = true
		}
		for _, u := range []*big.Int{
			new(big.Int).Add(pow2(24), big.NewInt(1)), new(big.Int).Sub(pow2(24), big.NewInt(1)),
			new(big.Int).Add(pow2(53), big.NewInt(1)), new(big.Int).Sub(pow2(53), big.NewInt(1)),
			new(big.Int).Add(new(big.Int).Add(pow2(53), pow2(29)), big.NewInt(1)),
		} {
			r := new(big.Rat).Quo(new(big.Rat).SetInt(u), ten)
			if representable(r, dt) && !seen[r.RatString()] {
				seen[r.RatString()] = true
				rs = append(rs, r)
			}
		}
		vals := make([]srcVal, len(rs))
		for i, r := range rs {
			vals[i] = srcVal{r: r}
		}
		put(dt, vals, columnOf(t, dt, rs))
	}
	return out
}

func floatVal(f float64) srcVal {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return srcVal{f: f}
	}
	return srcVal{r: new(big.Rat).SetFloat64(f)}
}

// temporalTypes are the temporal types whose casts to and from an integer are a
// tick count. Time is left out: a Time is a time of day, and a cast to one wraps
// (audit.md S18).
func temporalTypes() []dtype.DataType {
	return []dtype.DataType{dtype.Date, dtype.Datetime(dtype.Nano, ""), dtype.Duration(dtype.Nano)}
}

// tickColumn is intColumn for any type, built at its physical type and relabelled.
func tickColumn(t *testing.T, dt dtype.DataType, vals []*big.Int) *data.Column {
	t.Helper()
	return intColumn(t, dt.Physical(), vals).WithDType(dt)
}

// floatsOf reads a float column back, NaN-safe, with its validity.
func floatsOf(t *testing.T, c *data.Column) ([]float64, bitmap.View) {
	t.Helper()
	switch c.DType().ID() {
	case dtype.TypeFloat32:
		v, err := data.Values[float32](c)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]float64, len(v))
		for i, x := range v {
			out[i] = float64(x)
		}
		return out, c.Validity()
	case dtype.TypeFloat64:
		v, err := data.Values[float64](c)
		if err != nil {
			t.Fatal(err)
		}
		return v, c.Validity()
	}
	t.Fatalf("not a float column: %s", c.DType())
	return nil, bitmap.View{}
}

func TestCastsToAFloatRoundOnce(t *testing.T) {
	floors := map[string]bool{}
	var pairs, values int
	for name, src := range floatSources(t) {
		for _, to := range []dtype.DataType{dtype.Float32, dtype.Float64} {
			if src.dt == to {
				continue
			}
			pairs++
			pair := name + "->" + to.String()
			var wrong []string

			lossy, err := kernel.Cast("c", to, false, src.col)
			if err != nil {
				t.Errorf("%s: CastLossy refused: %v", pair, err)
				continue
			}
			got, valid := floatsOf(t, lossy)
			for i, v := range src.vals {
				values++
				want, overflow := nearest(v, to)
				switch {
				case overflow && valid.Get(i):
					wrong = append(wrong, fmt.Sprintf("lossy %s -> %v, want null", v, got[i]))
				case !overflow && !valid.Get(i):
					wrong = append(wrong, fmt.Sprintf("lossy %s -> null", v))
				case !overflow && !sameFloat(got[i], want):
					wrong = append(wrong, fmt.Sprintf("lossy %s -> %v, want %v", v, got[i], want))
				}

				one, err := kernel.Cast("c", to, true, src.col.Slice(i, 1))
				switch {
				case overflow && err == nil:
					wrong = append(wrong, fmt.Sprintf("strict %s converted, want a refusal", v))
				case overflow && !errors.Is(err, uerr.ErrValue):
					t.Errorf("%s: %s refused with the wrong kind: %v", pair, v, err)
				case overflow:
				case err != nil:
					wrong = append(wrong, fmt.Sprintf("strict %s refused: %s", v, firstLine(err)))
				default:
					g, ok := floatsOf(t, one)
					if !ok.Get(0) || !sameFloat(g[0], want) {
						wrong = append(wrong, fmt.Sprintf("strict %s -> %v valid=%v, want %v", v, g[0], ok.Get(0), want))
					}
				}

				// What the values must reach.
				if overflow {
					floors[pair+" overflow"] = true
				}
				if v.r != nil && !overflow && to.ID() == dtype.TypeFloat32 {
					f64, _ := v.r.Float64()
					if !sameFloat(float64(float32(f64)), want) {
						floors[pair+" double rounding"] = true
					}
				}
				if v.r != nil && name == "Int128" && to.ID() == dtype.TypeFloat64 && v.r.Sign() > 0 {
					if naiveI128(v.r.Num()) != want {
						floors[pair+" i128 double rounding"] = true
					}
				}
			}
			judgeCounted(t, knownFloatCastDefects, pair, wrong)
		}
	}
	for _, f := range []string{
		"Int64->Float32 double rounding", "Uint64->Float32 double rounding",
		"Int128->Float32 double rounding", "Decimal(38, 0)->Float32 double rounding",
		"Int128->Float64 i128 double rounding", "Float64->Float32 overflow",
	} {
		if !floors[f] {
			t.Errorf("the sweep does not reach %q", f)
		}
	}
	// T itself overflows, and the float just below it does not.
	below := srcVal{r: new(big.Rat).SetFloat64(math.Nextafter(0x1p128-0x1p103, 0))}
	if _, o := nearest(below, dtype.Float32); o {
		t.Error("the oracle overflows below the threshold")
	}
	if pairs < 40 || values < 3000 {
		t.Fatalf("%d pairs, %d values — the sweep has gone vacuous", pairs, values)
	}
}

// naiveI128 is Int128.Float64 as it was: the two halves rounded separately.
func naiveI128(v *big.Int) float64 {
	lo := new(big.Int).And(v, new(big.Int).Sub(pow2(64), big.NewInt(1)))
	hi := new(big.Int).Rsh(v, 64)
	return float64(hi.Int64())*0x1p64 + float64(lo.Uint64())
}

// knownTickCastDefects counts the wrong values per temporal <-> integer pair.
var knownTickCastDefects = map[string]knownWrong{}

// TestTemporalIntegerCastsAreExact: a temporal value and an integer convert as a
// tick count, and an integer never passes through a float on the way. Every pair of
// a temporal type and an integer width, both directions, at every integer type's
// edges: exact, or refused under Cast and null under CastLossy.
func TestTemporalIntegerCastsAreExact(t *testing.T) {
	ints := integerTypes(t)
	all := edges(ints)
	var pairs, values int
	for _, tt := range temporalTypes() {
		for _, it := range ints {
			for _, dir := range [][2]dtype.DataType{{tt, it}, {it, tt}} {
				from, to := dir[0], dir[1]
				pairs++
				pair := from.String() + "->" + to.String()
				var src []*big.Int
				for _, v := range all {
					if inRange(v, from.Physical()) {
						src = append(src, v)
					}
				}
				col := tickColumn(t, from, src)
				var wrong []string
				got, err := kernel.Cast("c", to, false, col)
				if err != nil {
					t.Errorf("%s: CastLossy refused: %v", pair, err)
					continue
				}
				for i, g := range intValues(t, got) {
					values++
					want := src[i]
					if !inRange(want, to.Physical()) {
						want = nil
					}
					if (g == nil) != (want == nil) || (g != nil && g.Cmp(want) != 0) {
						wrong = append(wrong, "lossy "+src[i].String()+" -> "+render(g))
					}
				}
				for i, v := range src {
					one, err := kernel.Cast("c", to, true, col.Slice(i, 1))
					switch {
					case !inRange(v, to.Physical()) && err == nil:
						wrong = append(wrong, "strict "+v.String()+" -> "+render(intValues(t, one)[0]))
					case !inRange(v, to.Physical()) && !errors.Is(err, uerr.ErrValue):
						t.Errorf("%s: %s refused with the wrong kind: %v", pair, v, err)
					case inRange(v, to.Physical()) && err != nil:
						wrong = append(wrong, "strict "+v.String()+" refused")
					case inRange(v, to.Physical()):
						if g := intValues(t, one)[0]; g == nil || g.Cmp(v) != 0 {
							wrong = append(wrong, "strict "+v.String()+" -> "+render(g))
						}
					}
				}
				judgeCounted(t, knownTickCastDefects, pair, wrong)
			}
		}
	}
	if pairs != 54 || values < 1000 {
		t.Fatalf("%d pairs, %d values — the sweep has gone vacuous", pairs, values)
	}
}

// TestFloatToIntegerCastsAreExactOrRefused is the other direction, which already
// answers correctly and must stay so: a float converts to an integer exactly when it
// is an integer in the target's range. It guards the range check that comes before
// the conversion — Go leaves an out-of-range float-to-integer conversion to the
// platform, and arm64 saturates where amd64 does not.
func TestFloatToIntegerCastsAreExactOrRefused(t *testing.T) {
	fs := []float64{0, math.Copysign(0, -1), 1, -1, 1.5, -0.5, 0.1, math.NaN(), math.Inf(1), math.Inf(-1),
		math.MaxFloat64, 0x1p53, 0x1p53 + 2}
	for _, k := range []int{7, 8, 15, 16, 31, 32, 63, 64, 127, 128} {
		p := math.Ldexp(1, k)
		fs = append(fs, p, -p, math.Nextafter(p, 0), -math.Nextafter(p, 0))
	}
	var values int
	for _, from := range []dtype.DataType{dtype.Float32, dtype.Float64} {
		var col *data.Column
		if from.ID() == dtype.TypeFloat32 {
			f32 := make([]float32, len(fs))
			for i, f := range fs {
				f32[i] = float32(f)
			}
			col = data.NewFixed("c", from, f32, bitmap.AllSet(len(fs)))
		} else {
			col = data.NewFixed("c", from, fs, bitmap.AllSet(len(fs)))
		}
		srcs, _ := floatsOf(t, col)
		for _, to := range integerTypes(t) {
			pair := from.String() + "->" + to.String()
			for i, f := range srcs {
				values++
				var want *big.Int
				if !math.IsNaN(f) && !math.IsInf(f, 0) {
					if r := new(big.Rat).SetFloat64(f); r.IsInt() && inRange(r.Num(), to) {
						want = r.Num()
					}
				}
				got, err := kernel.Cast("c", to, true, col.Slice(i, 1))
				switch {
				case want == nil && err == nil:
					t.Errorf("%s: %v -> %s under a strict cast", pair, f, render(intValues(t, got)[0]))
				case want == nil && !errors.Is(err, uerr.ErrValue):
					t.Errorf("%s: %v refused with the wrong kind: %v", pair, f, err)
				case want != nil && err != nil:
					t.Errorf("%s: %v refused: %v", pair, f, err)
				case want != nil:
					if g := intValues(t, got)[0]; g == nil || g.Cmp(want) != 0 {
						t.Errorf("%s: %v -> %s", pair, f, render(g))
					}
				}
			}
		}
	}
	if values < 500 {
		t.Fatalf("only %d values", values)
	}
}

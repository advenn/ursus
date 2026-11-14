package i128_test

import (
	"errors"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/fuzzcheck"
)

// The 128-bit integer's fuzz targets (step 509, step 445 of
// context_files/v0.7.2-plan.md): each answer against math/big's, inside fuzzcheck's
// limits.

var limits = fuzzcheck.Limits{Wall: 5 * time.Second, Alloc: 64 << 20}

var two64 = new(big.Int).Lsh(big.NewInt(1), 64)

// bigOf is v's value from its two halves, apart from the package's own String.
func bigOf(v i128.Int128) *big.Int {
	b := new(big.Int).Mul(big.NewInt(v.Hi), two64)
	return b.Add(b, new(big.Int).SetUint64(v.Lo))
}

// integerText is Parse's documented grammar: an optional sign, then digits.
var integerText = regexp.MustCompile(`^[+-]?[0-9]+$`)

// FuzzParse: text Parse reads is a sign and digits whose value big.Int reads in the
// type's range, to the same value; it refuses any other.
func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"0", "-0", "+0", "1", "-1", "170141183460469231731687303715884105727",
		"170141183460469231731687303715884105728", "-170141183460469231731687303715884105728",
		"-170141183460469231731687303715884105729", "0000000000000000000000000000000000000000001",
		"-00170141183460469231731687303715884105728", "18446744073709551615", "18446744073709551616",
		"9223372036854775808", "-9223372036854775809", "", "+", "-", "1_000", "0x10", " 1", "1 ",
		"99999999999999999999999999999999999999999", "1e3", "١",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		fuzzcheck.Run(t, limits, func() error {
			v, ok := i128.Parse(s)
			ref, refOK := new(big.Int), false
			if integerText.MatchString(s) {
				_, refOK = ref.SetString(s, 10)
				refOK = refOK && inRange(ref)
			}
			if ok != refOK {
				t.Fatalf("Parse(%q) = %v, %v; big.Int: %v, in range %v", s, v, ok, ref, refOK)
			}
			if ok && bigOf(v).Cmp(ref) != 0 {
				t.Fatalf("Parse(%q) = %v; big.Int: %v", s, bigOf(v), ref)
			}
			return nil
		})
	})
}

// FuzzArith: on any two values, Add, Sub and Neg wrap as big.Int's sum, difference
// and negation do modulo 2^128; AddChecked, SubChecked and MulChecked are exact or
// false exactly when big.Int's answer leaves the type; DivMod is floor division and
// its remainder, refused only for Min / -1; DivUint64 truncates; Cmp, Sign, String,
// Int64 and Uint64 are big.Int's; Float64 and Float32 are big.Float's one rounding,
// ties to even; and FromFloat64 of b's bits as a float truncates as big.Float does.
func FuzzArith(f *testing.F) {
	edge := []i128.Int128{i128.Zero, i128.One, i128.One.Neg(), i128.Min, i128.Max,
		i128.Min.Add(i128.One), i128.Max.Sub(i128.One), i128.FromUint64(1<<64 - 1),
		{Hi: 1, Lo: 0}, {Hi: -1, Lo: 0}, {Hi: 0, Lo: 1 << 63}, {Hi: 1 << 40, Lo: 1<<11 + 1},
		{Hi: 1, Lo: 1<<63 | 1<<11 | 1}, {Hi: 0, Lo: 1<<53 + 1}, {Hi: -7, Lo: 3}}
	for _, a := range edge {
		for _, b := range edge {
			f.Add(a.Hi, a.Lo, b.Hi, b.Lo)
		}
	}
	f.Fuzz(func(t *testing.T, ahi int64, alo uint64, bhi int64, blo uint64) {
		a, b := i128.Int128{Hi: ahi, Lo: alo}, i128.Int128{Hi: bhi, Lo: blo}
		fuzzcheck.Run(t, limits, func() error {
			checkArith(t, a, b)
			return nil
		})
	})
}

func checkArith(t *testing.T, a, b i128.Int128) {
	t.Helper()
	A, B := bigOf(a), bigOf(b)
	same := func(op string, got i128.Int128, ref *big.Int) {
		t.Helper()
		if bigOf(got).Cmp(ref) != 0 {
			t.Fatalf("%v %s %v = %v; big.Int: %v", A, op, B, bigOf(got), ref)
		}
	}
	same("+ (wrapping)", a.Add(b), wrap(new(big.Int).Add(A, B)))
	same("- (wrapping)", a.Sub(b), wrap(new(big.Int).Sub(A, B)))
	same("negated, of the first,", a.Neg(), wrap(new(big.Int).Neg(A)))
	for _, op := range []struct {
		name string
		f    func(x, y i128.Int128) (i128.Int128, bool)
		big  func(z, x, y *big.Int) *big.Int
	}{
		{"+", i128.Int128.AddChecked, (*big.Int).Add},
		{"-", i128.Int128.SubChecked, (*big.Int).Sub},
		{"*", i128.Int128.MulChecked, (*big.Int).Mul},
	} {
		got, ok := op.f(a, b)
		ref := op.big(new(big.Int), A, B)
		if ok != inRange(ref) {
			t.Fatalf("%v %s %v: ok %v; big.Int: %v", A, op.name, B, ok, ref)
		}
		if ok {
			same(op.name, got, ref)
		}
	}
	if !b.IsZero() {
		q, m, ok := a.DivMod(b)
		rq, rm := new(big.Int).QuoRem(A, B, new(big.Int))
		if rm.Sign() != 0 && rm.Sign() != B.Sign() {
			rq.Sub(rq, big.NewInt(1))
			rm.Add(rm, B)
		}
		if ok != inRange(rq) {
			t.Fatalf("%v // %v: ok %v; big.Int: %v", A, B, ok, rq)
		}
		if ok {
			same("//", q, rq)
		}
		same("%", m, rm)
	}
	if d := b.Lo; d != 0 {
		q, r, ok := a.DivUint64(d)
		D := new(big.Int).SetUint64(d)
		rq, rr := new(big.Int).QuoRem(A, D, new(big.Int))
		if !ok || bigOf(q).Cmp(wrap(rq)) != 0 || new(big.Int).SetUint64(r).Cmp(rr.Abs(rr)) != 0 {
			t.Fatalf("%v.DivUint64(%d) = %v, %d, %v; big.Int: %v, %v", A, d, bigOf(q), r, ok, rq, rr)
		}
	}
	if got, ref := a.Cmp(b), A.Cmp(B); got != ref {
		t.Fatalf("%v.Cmp(%v) = %d; big.Int: %d", A, B, got, ref)
	}
	if a.Sign() != A.Sign() || a.IsZero() != (A.Sign() == 0) {
		t.Fatalf("%v: Sign %d, IsZero %v", A, a.Sign(), a.IsZero())
	}
	if a.String() != A.String() {
		t.Fatalf("String() = %q; big.Int: %q", a.String(), A.String())
	}
	if v, ok := a.Int64(); ok != A.IsInt64() || ok && v != A.Int64() {
		t.Fatalf("%v.Int64() = %d, %v", A, v, ok)
	}
	if v, ok := a.Uint64(); ok != A.IsUint64() || ok && v != A.Uint64() {
		t.Fatalf("%v.Uint64() = %d, %v", A, v, ok)
	}
	f64, _ := new(big.Float).SetInt(A).Float64()
	if got := a.Float64(); math.Float64bits(got) != math.Float64bits(f64) {
		t.Fatalf("%v.Float64() = %v; big.Float: %v", A, got, f64)
	}
	f32, _ := new(big.Float).SetInt(A).Float32()
	if got := a.Float32(); math.Float32bits(got) != math.Float32bits(f32) {
		t.Fatalf("%v.Float32() = %v; big.Float: %v", A, got, f32)
	}
	x := math.Float64frombits(b.Lo)
	v, ok := i128.FromFloat64(x)
	refOK := !math.IsNaN(x) && !math.IsInf(x, 0)
	var ref *big.Int
	if refOK {
		ref, _ = big.NewFloat(x).Int(nil)
		refOK = inRange(ref)
	}
	if ok != refOK || ok && bigOf(v).Cmp(ref) != 0 {
		t.Fatalf("FromFloat64(%v) = %v, %v; big.Float: %v, %v", x, bigOf(v), ok, ref, refOK)
	}
}

// decimalText is ParseDecimal's documented grammar, written out apart from it: an
// optional sign, digits with an optional point, at least one digit, and an optional
// exponent of a sign and digits.
var decimalText = regexp.MustCompile(`^([+-]?)([0-9]*)(?:\.([0-9]*))?(?:[eE]([+-]?[0-9]+))?$`)

// maxExponent is the exponent past which ParseDecimal refuses a value that is not
// zero, as DuckDB and Polars refuse one (TestParseDecimalExponentDoesNotWrap).
const maxExponent = 1 << 40

// referenceDecimal is text read as the unscaled value of a Decimal(precision, scale),
// by math/big: the digits D, times 10 to the exponent less the fraction's digits plus
// the scale, rounded half away from zero, and its range at most precision digits. A
// shift whose power of ten cannot be held is decided by its size: past the precision
// the value is out of range, and below minus the digits it rounds to 0, D being under
// 10^digits. err wraps strconv.ErrSyntax or strconv.ErrRange.
func referenceDecimal(s string, precision, scale int) (*big.Int, error) {
	m := decimalText.FindStringSubmatch(s)
	if m == nil || m[2] == "" && m[3] == "" {
		return nil, strconv.ErrSyntax
	}
	digits := strings.TrimLeft(m[2]+m[3], "0")
	if digits == "" {
		return new(big.Int), nil
	}
	exp, ok := new(big.Int).SetString(strings.TrimPrefix(m[4], "+"), 10)
	if m[4] == "" {
		exp, ok = new(big.Int), true
	}
	if !ok {
		return nil, strconv.ErrSyntax
	}
	if exp.CmpAbs(big.NewInt(maxExponent)) > 0 {
		return nil, strconv.ErrRange
	}
	shift := int(exp.Int64()) - len(m[3]) + scale
	D, _ := new(big.Int).SetString(digits, 10)
	var v *big.Int
	switch {
	case shift > precision:
		return nil, strconv.ErrRange
	case shift < -len(digits)-1:
		v = new(big.Int)
	case shift >= 0:
		v = D.Mul(D, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(shift)), nil))
	default:
		p := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-shift)), nil)
		q, r := new(big.Int).QuoRem(D, p, new(big.Int))
		if r.Lsh(r, 1).Cmp(p) >= 0 {
			q.Add(q, big.NewInt(1))
		}
		v = q
	}
	if v.Cmp(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(precision)), nil)) >= 0 {
		return nil, strconv.ErrRange
	}
	if m[1] == "-" {
		v.Neg(v)
	}
	return v, nil
}

// FuzzParseDecimal: ParseDecimal at any precision from 1 to 38 and scale up to it is
// referenceDecimal's value, or its error, ErrSyntax or ErrRange.
func FuzzParseDecimal(f *testing.F) {
	for _, s := range []string{
		"0", "-0", "1", "12.345", "-12.345", "1.235", "-1.235", "1.225", "0.5", "0.4999", ".5", "5.",
		"1e2", "1E-2", "1.5e+2", "5e-3", "9.995", "99.5", "0.95", "1e9223372036854775807",
		"5e-9223372036854775808", "0e9223372036854775807", "0e9223372036854775808",
		"1e99999999999999999999", "1e-99999999999999999999", "1e1099511627776", "1e1099511627777",
		"1e-1099511627776", "99999999999999999999999999999999999999", "999999999999999999999999999999999999995e-1",
		"170141183460469231731687303715884105727", "1e37", "1e38", "0.00000000000000000000000000000000000001",
		"", ".", "-", "+.", "e5", ".e5", "1e", "1e+", "1.2.3", "1_0", "0x1", " 1", "1 ", "1e5e5", "١",
	} {
		for _, ps := range [][2]uint8{{37, 2}, {37, 0}, {0, 0}, {9, 9}, {37, 37}, {4, 2}} {
			f.Add(s, ps[0], ps[1])
		}
	}
	f.Fuzz(func(t *testing.T, s string, p, sc uint8) {
		precision := 1 + int(p)%38
		scale := int(sc) % (precision + 1)
		fuzzcheck.Run(t, limits, func() error {
			got, err := i128.ParseDecimal(s, precision, scale)
			ref, rerr := referenceDecimal(s, precision, scale)
			if rerr != nil {
				if !errors.Is(err, rerr) {
					t.Fatalf("ParseDecimal(%q, %d, %d) = %v, %v; math/big: %v", s, precision, scale, bigOf(got), err, rerr)
				}
				return nil
			}
			if err != nil || bigOf(got).Cmp(ref) != 0 {
				t.Fatalf("ParseDecimal(%q, %d, %d) = %v, %v; math/big: %v", s, precision, scale, bigOf(got), err, ref)
			}
			return nil
		})
	})
}

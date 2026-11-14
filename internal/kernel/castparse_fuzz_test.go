package kernel_test

import (
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/fuzzcheck"
	_ "github.com/advenn/ursus/internal/fuzzcheck/invariants"
	"github.com/advenn/ursus/internal/kernel"
)

// castFuzzTarget is the type FuzzCastFromString casts to: each integer width,
// Int128, a Decimal of precision 1 to 38 and scale up to it, both floats, Bool, Date,
// and a Datetime, a Time and a Duration at each unit, the Datetime in no zone, in UTC
// or in St John's.
func castFuzzTarget(to, p, sc uint8) dtype.DataType {
	u := dtype.TimeUnit(p % 4)
	switch to % 18 {
	case 0:
		return dtype.Int8
	case 1:
		return dtype.Int16
	case 2:
		return dtype.Int32
	case 3:
		return dtype.Int64
	case 4:
		return dtype.Uint8
	case 5:
		return dtype.Uint16
	case 6:
		return dtype.Uint32
	case 7:
		return dtype.Uint64
	case 8:
		return dtype.Int128
	case 9:
		prec := 1 + int(p)%38
		return dtype.Decimal(uint8(prec), uint8(int(sc)%(prec+1)))
	case 10:
		return dtype.Float32
	case 11:
		return dtype.Float64
	case 12:
		return dtype.Bool
	case 13:
		return dtype.Date
	case 14:
		return dtype.Datetime(u, []string{"", "UTC", "America/St_Johns"}[int(sc)%3])
	case 15:
		return dtype.Time(u)
	default:
		return dtype.Duration(u)
	}
}

var castIntegerText = regexp.MustCompile(`^[+-]?[0-9]+$`)

// referenceCast is s cast to dt by the references FuzzCastFromString holds the cast
// to: strconv for an integer of each width, with an optional sign for an unsigned one
// as the cast documents, and for a Bool; math/big for an Int128; and for a Decimal, a
// float and a temporal type the parsers whose own targets compare them with math/big,
// strconv and the time package (i128's FuzzParseDecimal, dtype's FuzzParseFloat and
// FuzzTemporalRoundTrip). ok false is a null.
func referenceCast(s string, dt dtype.DataType) (v any, ok bool) {
	switch dt.ID() {
	case dtype.TypeInt8, dtype.TypeInt16, dtype.TypeInt32, dtype.TypeInt64:
		n, err := strconv.ParseInt(s, 10, dt.BitWidth())
		switch dt.ID() {
		case dtype.TypeInt8:
			return int8(n), err == nil
		case dtype.TypeInt16:
			return int16(n), err == nil
		case dtype.TypeInt32:
			return int32(n), err == nil
		}
		return n, err == nil
	case dtype.TypeUint8, dtype.TypeUint16, dtype.TypeUint32, dtype.TypeUint64:
		rest := strings.TrimPrefix(strings.TrimPrefix(s, "+"), "-")
		if len(s)-len(rest) > 1 {
			return nil, false
		}
		n, err := strconv.ParseUint(rest, 10, dt.BitWidth())
		ok := err == nil && (n == 0 || !strings.HasPrefix(s, "-"))
		switch dt.ID() {
		case dtype.TypeUint8:
			return uint8(n), ok
		case dtype.TypeUint16:
			return uint16(n), ok
		case dtype.TypeUint32:
			return uint32(n), ok
		}
		return n, ok
	case dtype.TypeInt128:
		b, ok := new(big.Int).SetString(s, 10)
		lim := new(big.Int).Lsh(big.NewInt(1), 127)
		if !castIntegerText.MatchString(s) || !ok || b.Cmp(lim) >= 0 || b.Cmp(lim.Neg(lim)) < 0 {
			return nil, false
		}
		lo := new(big.Int).And(b, new(big.Int).SetUint64(math.MaxUint64)).Uint64()
		return i128.Int128{Hi: new(big.Int).Rsh(b, 64).Int64(), Lo: lo}, true
	case dtype.TypeDecimal:
		d, err := i128.ParseDecimal(s, int(dt.Precision()), int(dt.Scale()))
		return d, err == nil
	case dtype.TypeFloat32:
		f, err := dtype.ParseFloat(s, 32)
		return math.Float32bits(float32(f)), err == nil
	case dtype.TypeFloat64:
		f, err := dtype.ParseFloat(s, 64)
		return math.Float64bits(f), err == nil
	case dtype.TypeBool:
		b, err := strconv.ParseBool(s)
		return b, err == nil
	case dtype.TypeDate:
		n, ok := dtype.ParseTemporal(dt, s)
		return int32(n), ok && n >= math.MinInt32 && n <= math.MaxInt32
	}
	n, ok := dtype.ParseTemporal(dt, s)
	return n, ok
}

// castValue is row i of a cast's column as referenceCast gives it.
func castValue(t *testing.T, c *data.Column, i int) any {
	t.Helper()
	switch c.DType().Physical().ID() {
	case dtype.TypeInt8:
		return data.MustValues[int8](c)[i]
	case dtype.TypeInt16:
		return data.MustValues[int16](c)[i]
	case dtype.TypeInt32:
		return data.MustValues[int32](c)[i]
	case dtype.TypeInt64:
		return data.MustValues[int64](c)[i]
	case dtype.TypeUint8:
		return data.MustValues[uint8](c)[i]
	case dtype.TypeUint16:
		return data.MustValues[uint16](c)[i]
	case dtype.TypeUint32:
		return data.MustValues[uint32](c)[i]
	case dtype.TypeUint64:
		return data.MustValues[uint64](c)[i]
	case dtype.TypeInt128, dtype.TypeDecimal:
		return data.MustValues[i128.Int128](c)[i]
	case dtype.TypeFloat32:
		return math.Float32bits(data.MustValues[float32](c)[i])
	case dtype.TypeFloat64:
		return math.Float64bits(data.MustValues[float64](c)[i])
	case dtype.TypeBool:
		return c.Bools().Get(i)
	}
	t.Fatalf("castValue: %s", c.DType())
	return nil
}

// FuzzCastFromString: any text, cast from the String column [s, s, null] to each
// type castFuzzTarget names, is referenceCast's value in both rows, or a null where it
// has none, and null in the third; a strict cast refuses it exactly where the
// non-strict one gives a null, and otherwise gives the same column (step 509, step 445
// of context_files/v0.7.2-plan.md).
func FuzzCastFromString(f *testing.F) {
	for i, s := range []string{
		"0", "-0", "+5", "-1", "127", "128", "-129", "255", "256", "65535", "4294967296", "9223372036854775807",
		"9223372036854775808", "18446744073709551615", "18446744073709551616", "-9223372036854775809",
		"170141183460469231731687303715884105727", "170141183460469231731687303715884105728",
		"-170141183460469231731687303715884105728", "16777217.5", "1.5", "1.235", "-1.235", "1e2", "1e309",
		"1e-400", "0.1", "3.4028236e38", "inf", "-nan", "NaN", "1_000", "0x1p3", "true", "FALSE", "t", "1",
		"2024-01-02", "2024-01-02T03:04:05.123456789+05:30", "2024-01-02 03:04:05", "03:04:05.5", "24:00:00",
		"1h2m3.5s", "-2562047h47m16.854775808s", "5881580-07-11", "1e99999999999999999999", "", " 1", "x",
	} {
		for to := range uint8(18) {
			f.Add(s, to, uint8(i*7+int(to)), uint8(i))
		}
	}
	f.Fuzz(func(t *testing.T, s string, to, p, sc uint8) {
		dt := castFuzzTarget(to, p, sc)
		valid := bitmap.NewBuilder(3)
		valid.Append(true)
		valid.Append(true)
		valid.Append(false)
		col := data.NewString("s", []string{s, s, ""}, valid.Finish())
		fuzzcheck.Run(t, fuzzcheck.Limits{Wall: 5 * time.Second, Alloc: 64 << 20}, func() error {
			lossy, err := kernel.Cast("x", dt, false, col)
			if err != nil {
				t.Fatalf("Cast(%q, %s) non-strict: %v", s, dt, err)
			}
			if lossy.Len() != 3 || lossy.IsValid(2) || lossy.IsValid(0) != lossy.IsValid(1) {
				t.Fatalf("Cast(%q, %s) non-strict: %d rows, valid %v %v %v", s, dt, lossy.Len(),
					lossy.IsValid(0), lossy.IsValid(1), lossy.IsValid(2))
			}
			ref, ok := referenceCast(s, dt)
			if ok != lossy.IsValid(0) {
				t.Fatalf("Cast(%q, %s) non-strict: valid %v; the reference: %v, %v", s, dt, lossy.IsValid(0), ref, ok)
			}
			if ok {
				for i := range 2 {
					if got := castValue(t, lossy, i); got != ref {
						t.Fatalf("Cast(%q, %s) row %d = %v; the reference: %v", s, dt, i, got, ref)
					}
				}
			}
			strict, serr := kernel.Cast("x", dt, true, col)
			if (serr != nil) == ok {
				t.Fatalf("Cast(%q, %s) strict: %v; the non-strict cast's row valid %v", s, dt, serr, ok)
			}
			if serr == nil {
				for i := range 2 {
					if castValue(t, strict, i) != castValue(t, lossy, i) || strict.IsValid(i) != lossy.IsValid(i) {
						t.Fatalf("Cast(%q, %s) row %d: strict %v, non-strict %v", s, dt, i, castValue(t, strict, i), castValue(t, lossy, i))
					}
				}
			}
			return serr
		})
	})
}

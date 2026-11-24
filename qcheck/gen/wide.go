package gen

import (
	"math"
	"math/big"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"

	"github.com/advenn/ursus/qcheck/model"
)

// The wide types' tables (step 539): Int8, Int16, the unsigned integers, Float32,
// Datetime of each unit naive and in each zone, Duration, Time and Decimal, beside
// the base types, each with step 453's tame, wild and extreme profiles.

// Decimals are the Decimal types a column takes: few digits and many, a scale of
// none to ten, and 38 digits, the most a Decimal holds.
var Decimals = []model.Type{model.Decimal(5, 2), model.Decimal(9, 0), model.Decimal(12, 4),
	model.Decimal(18, 6), model.Decimal(38, 10)}

// wideType draws a wide table's column type: a base type half the time.
func wideType(r *rand.Rand) model.Type {
	switch p := r.IntN(100); {
	case p < 45:
		return model.Base[r.IntN(len(model.Base))]
	case p < 50:
		return []model.Type{model.Int8, model.Int16}[r.IntN(2)]
	case p < 58:
		return []model.Type{model.Uint8, model.Uint16, model.Uint32, model.Uint64}[r.IntN(4)]
	case p < 63:
		return model.Float32
	case p < 78:
		z := 0
		if r.IntN(2) == 0 {
			z = 1 + r.IntN(len(model.Zones)-1)
		}
		return model.Datetime(model.Units[r.IntN(4)], z)
	case p < 85:
		return model.Duration(model.Units[r.IntN(4)])
	case p < 90:
		return model.Time(model.Units[r.IntN(4)])
	}
	return Decimals[r.IntN(len(Decimals))]
}

// ArrowUnit is the Arrow unit of u.
func ArrowUnit(u model.Unit) arrow.TimeUnit {
	return [...]arrow.TimeUnit{arrow.Second, arrow.Millisecond, arrow.Microsecond, arrow.Nanosecond}[u]
}

func wideArrowType(t model.Type) arrow.DataType {
	switch t.Kind() {
	case model.Int8:
		return arrow.PrimitiveTypes.Int8
	case model.Int16:
		return arrow.PrimitiveTypes.Int16
	case model.Uint8:
		return arrow.PrimitiveTypes.Uint8
	case model.Uint16:
		return arrow.PrimitiveTypes.Uint16
	case model.Uint32:
		return arrow.PrimitiveTypes.Uint32
	case model.Uint64:
		return arrow.PrimitiveTypes.Uint64
	case model.Float32:
		return arrow.PrimitiveTypes.Float32
	case model.DatetimeKind:
		return &arrow.TimestampType{Unit: ArrowUnit(t.Unit()), TimeZone: t.Zone()}
	case model.DurationKind:
		return &arrow.DurationType{Unit: ArrowUnit(t.Unit())}
	case model.TimeKind:
		if t.Unit() <= model.Milli {
			return &arrow.Time32Type{Unit: ArrowUnit(t.Unit())}
		}
		return &arrow.Time64Type{Unit: ArrowUnit(t.Unit())}
	case model.DecimalKind:
		return &arrow.Decimal128Type{Precision: int32(t.Prec()), Scale: int32(t.Scale())}
	}
	panic("gen: no Arrow type for " + t.String())
}

// append draws one value of a wide column into its builder.
func (v *values) append(fb array.Builder) {
	t := v.c.Type
	switch fb := fb.(type) {
	case *array.Int8Builder:
		fb.Append(int8(v.signed(8)))
	case *array.Int16Builder:
		fb.Append(int16(v.signed(16)))
	case *array.Uint8Builder:
		fb.Append(uint8(v.unsigned(8)))
	case *array.Uint16Builder:
		fb.Append(uint16(v.unsigned(16)))
	case *array.Uint32Builder:
		fb.Append(uint32(v.unsigned(32)))
	case *array.Uint64Builder:
		fb.Append(v.unsigned(64))
	case *array.Float32Builder:
		fb.Append(v.float32())
	case *array.TimestampBuilder:
		fb.Append(arrow.Timestamp(v.datetime(t)))
	case *array.DurationBuilder:
		fb.Append(arrow.Duration(v.duration(t.Unit())))
	case *array.Time32Builder:
		fb.Append(arrow.Time32(v.time(t.Unit())))
	case *array.Time64Builder:
		fb.Append(arrow.Time64(v.time(t.Unit())))
	case *array.Decimal128Builder:
		u, _ := new(big.Int).SetString(v.decimal(t), 10)
		fb.Append(decimal128.FromBigInt(u))
	default:
		panic("gen: no wide column of " + t.String())
	}
}

// signed is an Int8 or Int16 value.
func (v *values) signed(bits int) int64 {
	lo, hi := -int64(1)<<(bits-1), int64(1)<<(bits-1)-1
	switch {
	case v.ints != nil:
		return wrap(v.ints[v.r.IntN(len(v.ints))], bits)
	case v.c.Profile == model.Tame:
		return int64(v.r.IntN(201)) - 100
	case v.c.Profile == model.Extreme && v.pick(50):
		return []int64{lo, hi, lo + 1, hi - 1, 0, -1, 1}[v.r.IntN(7)]
	}
	return wrap(int64(v.r.Uint64()), bits)
}

// unsigned is an unsigned value of bits.
func (v *values) unsigned(bits int) uint64 {
	mask := uint64(math.MaxUint64) >> (64 - bits)
	switch {
	case v.ints != nil:
		return uint64(v.ints[v.r.IntN(len(v.ints))]+20) & mask
	case v.c.Profile == model.Tame:
		return uint64(v.r.IntN(201))
	case v.c.Profile == model.Extreme && v.pick(50):
		return []uint64{0, 1, mask, mask - 1, mask >> 1, mask>>1 + 1, 255, 256}[v.r.IntN(8)] & mask
	case v.pick(50):
		return v.r.Uint64() & mask
	}
	return uint64(v.r.IntN(2_000_001)) & mask
}

var extremeFloat32 = []float32{float32(math.NaN()), 0, float32(math.Copysign(0, -1)), float32(math.Inf(1)),
	float32(math.Inf(-1)), math.MaxFloat32, -math.MaxFloat32, math.SmallestNonzeroFloat32,
	-math.SmallestNonzeroFloat32, 1.1754943508222875e-38, 1 << 24, 1<<24 + 2, 0.1, 1, -1, 16777216, 3.4e38}

func (v *values) float32() float32 {
	switch {
	case v.flts != nil:
		return float32(v.flts[v.r.IntN(len(v.flts))])
	case v.c.Profile == model.Tame:
		return float32(v.r.IntN(801)-400) / 4
	case v.c.Profile == model.Extreme && v.pick(40):
		return extremeFloat32[v.r.IntN(len(extremeFloat32))]
	case v.pick(10):
		for {
			if f := math.Float32frombits(v.r.Uint32()); !math.IsNaN(float64(f)) && !math.IsInf(float64(f), 0) {
				return f
			}
		}
	}
	return float32(v.r.NormFloat64() * math.Pow(10, float64(v.r.IntN(16)-5)))
}

// The ranges a temporal draws from, in seconds since the epoch.
const (
	sec1900, sec2100 = -2_208_988_800, 4_102_444_800
	sec1980, sec2036 = 315_532_800, 2_082_758_400
	sec0001, sec9999 = -62_135_596_800, 253_402_300_799
	sec2020          = 1_577_836_800
)

// TameDatetime is the range of a tame Datetime's seconds: 2020 and 2021.
const TameDatetimeSeconds = 2 * 365 * 86_400

// datetime is a Datetime value's ticks. A tame one is in 2020 and 2021, at whole
// seconds; a wild one, naive, in 0001 to 9999, or 1900 to 2100 for nanoseconds,
// whose ticks hold only 1677 to 2262; a zoned one in 1980 to 2035 (plan step 471);
// an extreme one, naive, at int64's ends and the calendar's and the epoch's, and,
// zoned, at an instant its zone's offset changes, a tick either side.
func (v *values) datetime(t model.Type) int64 {
	u := t.Unit()
	per := u.PerSecond()
	sub := func() int64 { return v.r.Int64N(per) }
	switch {
	case v.ints != nil:
		return (sec2020 + v.ints[v.r.IntN(len(v.ints))]*3_600) * per
	case v.c.Profile == model.Tame:
		return (sec2020 + v.r.Int64N(TameDatetimeSeconds)) * per
	case v.c.Profile == model.Extreme && v.pick(50):
		if t.Zone() != "" {
			if tr := transitions(t.Zone()); len(tr) > 0 {
				at := tr[v.r.IntN(len(tr))]
				return floorDiv(at, u.Nanos()) + int64(v.r.IntN(3)) - 1
			}
			return []int64{sec1980 * per, sec2036*per - 1}[v.r.IntN(2)]
		}
		ends := []int64{math.MinInt64, math.MinInt64 + 1, math.MaxInt64 - 1, math.MaxInt64, 0, -1, 1, -86_400 * per,
			86_400*per - 1}
		if u < model.Nano {
			ends = append(ends, sec0001*per, (sec9999+1)*per-1)
		}
		return ends[v.r.IntN(len(ends))]
	}
	lo, hi := int64(sec0001), int64(sec9999)
	switch {
	case t.Zone() != "":
		lo, hi = sec1980, sec2036-1
	case u == model.Nano:
		lo, hi = sec1900, sec2100
	}
	return (lo+v.r.Int64N(hi-lo))*per + sub()
}

// WildDurationSeconds bounds a wild Duration: a billion seconds, about 31 years, so
// that one in nanoseconds, 10^18 ticks, holds, and so does a sum or difference of two
// of them, or one moving a wild Datetime(ns).
const WildDurationSeconds = 1_000_000_000

// duration is a Duration value's ticks: a tame one a thousand seconds either side
// of none, a wild one a billion, an extreme one at int64's ends, none, a tick and a
// day.
func (v *values) duration(u model.Unit) int64 {
	per := u.PerSecond()
	switch {
	case v.ints != nil:
		return v.ints[v.r.IntN(len(v.ints))] * 60 * per
	case v.c.Profile == model.Tame:
		return (v.r.Int64N(2_001) - 1_000) * per
	case v.c.Profile == model.Extreme && v.pick(50):
		return []int64{math.MinInt64, math.MaxInt64, math.MinInt64 + 1, math.MaxInt64 - 1, 0, 1, -1,
			86_400 * per, -86_400 * per}[v.r.IntN(9)]
	}
	return v.r.Int64N(2*WildDurationSeconds*per+1) - WildDurationSeconds*per
}

// time is a Time value's ticks, within a day: a tame one at a whole minute, an
// extreme one at the day's ends, noon or a tick in.
func (v *values) time(u model.Unit) int64 {
	day := u.PerDay()
	switch {
	case v.ints != nil:
		return (v.ints[v.r.IntN(len(v.ints))] + 20) * 60 * u.PerSecond()
	case v.c.Profile == model.Tame:
		return v.r.Int64N(1_440) * 60 * u.PerSecond()
	case v.c.Profile == model.Extreme && v.pick(50):
		return []int64{0, day - 1, day / 2, 1}[v.r.IntN(4)]
	}
	return v.r.Int64N(day)
}

// decimal is a Decimal value's unscaled value, as text: a tame one within ten
// thousand of none, a wild one of any digits its precision holds, an extreme one
// at the precision's ends, one unit, one of the last digit and none.
func (v *values) decimal(t model.Type) string {
	p, s := t.Prec(), t.Scale()
	bound := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(p)), nil)
	top := new(big.Int).Sub(bound, big.NewInt(1))
	switch {
	case v.ints != nil:
		x := big.NewInt(v.ints[v.r.IntN(len(v.ints))] * 25)
		return x.Mod(x, bound).String()
	case v.c.Profile == model.Tame:
		x := big.NewInt(int64(v.r.IntN(20_001)) - 10_000)
		if x.CmpAbs(bound) >= 0 {
			x.Rem(x, bound)
		}
		return x.String()
	case v.c.Profile == model.Extreme && v.pick(50):
		one := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(s)), nil)
		ends := []*big.Int{top, new(big.Int).Neg(top), big.NewInt(0), big.NewInt(1), big.NewInt(-1), one,
			new(big.Int).Neg(one)}
		return ends[v.r.IntN(len(ends))].String()
	}
	digits := 1 + v.r.IntN(p)
	var b strings.Builder
	if v.r.IntN(2) == 0 {
		b.WriteByte('-')
	}
	for i := range digits {
		d := v.r.IntN(10)
		if i == 0 && digits > 1 && d == 0 {
			d = 1
		}
		b.WriteByte(byte('0' + d))
	}
	return b.String()
}

// wildRunes are the characters wild text in a wide table draws beside ASCII: two,
// three and four bytes, letters whose case maps to more than one, a combining mark.
var wildRunes = []rune{'é', 'ß', 'İ', 'ı', 'Σ', 'ς', 'ǅ', 'Ω', '日', '本', '́', '🐻', '€', 'ﬁ', 'Å'}

// wildText is wild text of ASCII and wildRunes, so a string function meets runes
// of every width and letters whose case is not one rune.
func (v *values) wildText() string {
	n := v.r.IntN(13)
	var b strings.Builder
	for range n {
		if v.r.IntN(3) == 0 {
			b.WriteRune(wildRunes[v.r.IntN(len(wildRunes))])
		} else {
			b.WriteByte(byte(32 + v.r.IntN(95)))
		}
	}
	return b.String()
}

var (
	zoneMu   sync.Mutex
	zoneTran = map[string][]int64{}
)

// transitions are the instants, in nanoseconds since the epoch, at which zone's
// offset changes in 1980 to 2035, from the zone database Go reads; none where it
// cannot be loaded.
func transitions(zone string) []int64 {
	zoneMu.Lock()
	defer zoneMu.Unlock()
	if tr, ok := zoneTran[zone]; ok {
		return tr
	}
	var out []int64
	if loc, err := time.LoadLocation(zone); err == nil {
		at := time.Unix(sec1980, 0).In(loc)
		end := time.Unix(sec2036, 0)
		for at.Before(end) {
			_, next := at.ZoneBounds()
			if next.IsZero() || !next.After(at) {
				break
			}
			if next.Before(end) {
				out = append(out, next.UnixNano())
			}
			at = next
		}
	}
	zoneTran[zone] = out
	return out
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

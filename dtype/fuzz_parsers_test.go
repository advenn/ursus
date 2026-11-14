package dtype

import (
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus/internal/fuzzcheck"
)

// The text parsers' fuzz targets (step 509, step 445 of context_files/v0.7.2-plan.md):
// each answer is compared with the standard library's, strconv's for a number and the
// time package's for an instant, inside fuzzcheck's limits.

// parserLimits bounds one call of a text parser, which needs microseconds and bytes.
var parserLimits = fuzzcheck.Limits{Wall: 5 * time.Second, Alloc: 64 << 20}

// floatGrammar is ParseFloat's documented grammar, written out apart from the parser:
// [+-] (digits [. [digits]] | . digits) [(e|E) [+-] digits], or [+-] inf, infinity or
// nan in any case. What it leaves out, an underscore between digits and a hexadecimal
// float, strconv reads and ParseFloat refuses, as Polars refuses "1_000" and "0x1p3".
var floatGrammar = regexp.MustCompile(`^[+-]?(?:(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?|(?i:inf|infinity|nan))$`)

// FuzzParseFloat: text in floatGrammar is strconv.ParseFloat's float at either width,
// its bits and its ErrRange, a signed NaN read as NaN, as Polars' cast reads "-nan"
// and strconv reads only an unsigned one; any other text is ErrSyntax.
func FuzzParseFloat(f *testing.F) {
	for _, s := range []string{
		"0", "-0", "+0", "1.5", ".5", "5.", "+.5e-3", "-5.E+3", "1e308", "1e309", "-1e309",
		"4.9e-324", "2e-324", "1e-400", "3.4028235e38", "3.4028236e38", "1.17549435e-38",
		"0.1", "9007199254740993", "123456789012345678901234567890", "00000000000000000001",
		"1234567890123456789", "12345678901234567890", "0.000000000000000000000000000001e30",
		"1.00000000000000011102230246251565404236316680908203125",
		"inf", "-Infinity", "+INF", "NaN", "-nan", "+nan", "nan",
		"1_000", "0x1p3", "0X1P-2", "1__0", "", ".", "e5", "1e", "1e+", "--1", "1.2.3",
		" 1", "1 ", "1e99999", "1e-99999", "infinit", "nanx", "١",
	} {
		f.Add(s, false)
		f.Add(s, true)
	}
	f.Fuzz(func(t *testing.T, s string, f32 bool) {
		bits := 64
		if f32 {
			bits = 32
		}
		fuzzcheck.Run(t, parserLimits, func() error {
			got, err := ParseFloat(s, bits)
			if !floatGrammar.MatchString(s) {
				if !errors.Is(err, strconv.ErrSyntax) {
					t.Fatalf("ParseFloat(%q, %d) = %v, %v: outside the grammar, want ErrSyntax", s, bits, got, err)
				}
				return nil
			}
			ref, rerr := strconv.ParseFloat(s, bits)
			if u := strings.TrimLeft(s, "+-"); u != s && strings.EqualFold(u, "nan") {
				ref, rerr = math.NaN(), nil
			}
			if (err == nil) != (rerr == nil) || errors.Is(err, strconv.ErrRange) != errors.Is(rerr, strconv.ErrRange) {
				t.Fatalf("ParseFloat(%q, %d) = %v, %v; strconv: %v, %v", s, bits, got, err, ref, rerr)
			}
			if err != nil && !errors.Is(err, strconv.ErrRange) {
				t.Fatalf("ParseFloat(%q, %d) = %v: in the grammar, want a float or ErrRange", s, bits, err)
			}
			if !sameFloat(got, ref) {
				t.Fatalf("ParseFloat(%q, %d) = %v (%#x); strconv: %v (%#x)", s, bits,
					got, math.Float64bits(got), ref, math.Float64bits(ref))
			}
			return nil
		})
	})
}

// sameFloat is bit equality, any NaN equal to any other.
func sameFloat(a, b float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) {
		return math.IsNaN(a) && math.IsNaN(b)
	}
	return math.Float64bits(a) == math.Float64bits(b)
}

// FuzzNearestFloat: where NearestFloat settles man × 10^exp, its float is strconv's
// for the same number written out, to the bit and the sign; a number strconv takes
// past the range, NearestFloat leaves to it. The seeds reach every entry of the
// powers-of-ten table, and the exponents either side of it.
func FuzzNearestFloat(f *testing.F) {
	for q := minPow10 - 2; q <= maxPow10+2; q++ {
		f.Add(uint64(1)<<63|uint64(q+400)*0x9E3779B97F4A7C15>>1, int16(q), q%2 == 0)
	}
	for _, m := range []uint64{0, 1, 9, 1<<53 - 1, 1 << 53, 1<<53 + 1, 1<<64 - 1, 9999999999999999999, 4503599627370497} {
		for _, e := range []int16{-400, -343, -325, -308, -23, -22, 0, 22, 23, 308, 309, 400} {
			f.Add(m, e, e < 0)
		}
	}
	f.Fuzz(func(t *testing.T, man uint64, exp int16, neg bool) {
		fuzzcheck.Run(t, parserLimits, func() error {
			got, ok := NearestFloat(man, int(exp), neg)
			if !ok {
				return nil
			}
			text := strconv.FormatUint(man, 10) + "e" + strconv.Itoa(int(exp))
			if neg {
				text = "-" + text
			}
			ref, err := strconv.ParseFloat(text, 64)
			if err != nil {
				t.Fatalf("NearestFloat(%d, %d, %v) = %v, settled; strconv.ParseFloat(%q): %v", man, exp, neg, got, text, err)
			}
			if math.Float64bits(got) != math.Float64bits(ref) {
				t.Fatalf("NearestFloat(%d, %d, %v) = %v (%#x); strconv.ParseFloat(%q) = %v (%#x)",
					man, exp, neg, got, math.Float64bits(got), text, ref, math.Float64bits(ref))
			}
			return nil
		})
	})
}

// roundTripZones are the zones whose rules a round trip of text most often meets: an
// offset of half an hour (St John's), a daylight saving of half an hour (Lord Howe), a
// negative one in the zone file (Dublin) and one suspended a month a year
// (Casablanca); and none, and UTC. Which of their times are daylight saving is the
// zone database's, and CI's differs from Debian's, so nothing here names one.
var roundTripZones = []string{"", "UTC", "Europe/Dublin", "Africa/Casablanca", "Australia/Lord_Howe", "America/St_Johns"}

// FuzzTemporalRoundTrip: a Date, a Datetime in each of roundTripZones, a Time or a
// Duration, at each unit, that FormatTemporal renders, ParseTemporal reads back to its
// ticks. Text ParseTemporal reads is the same ticks; an instant in the years 1 to
// 9998, a time of day and a Duration time.Duration holds are read. And AppendISO, where
// it writes, writes what FormatTemporal does for the type without its zone.
func FuzzTemporalRoundTrip(f *testing.F) {
	for _, ticks := range []int64{0, 1, -1, 86_399, 86_400, 1_700_000_000_000_000, -62_135_596_800_000_000,
		253_402_300_799_999_999, 253_402_300_800_000_000, math.MaxInt64, math.MinInt64,
		1_635_642_000_000_000, 1_649_552_400_000_000, 1_617_454_800_000_000, 1_699_168_000_000_000,
		-2_840_140_800_000_000, 1_000_000_007, 2_147_483_647, -2_147_483_648} {
		for kind := range uint8(4) {
			f.Add(kind, uint8(Micro), uint8(ticks&7), ticks)
		}
		f.Add(uint8(1), uint8(Nano), uint8(3), ticks)
		f.Add(uint8(1), uint8(Second), uint8(4), ticks)
		f.Add(uint8(1), uint8(Milli), uint8(5), ticks)
	}
	f.Fuzz(func(t *testing.T, kind, unit, zone uint8, ticks int64) {
		u := TimeUnit(unit % 4)
		var d DataType
		switch kind % 4 {
		case 0:
			d, ticks = Date, int64(int32(ticks))
		case 1:
			d = Datetime(u, roundTripZones[int(zone)%len(roundTripZones)])
		case 2:
			d = Time(u)
			per, _ := TicksPerDay(d)
			ticks = (ticks%per + per) % per
		case 3:
			d = Duration(u)
		}
		fuzzcheck.Run(t, parserLimits, func() error {
			text := FormatTemporal(d, ticks)
			back, ok := ParseTemporal(d, text)
			if ok && back != ticks {
				t.Fatalf("%s %d renders %q, which reads back as %d", d, ticks, text, back)
			}
			var readable bool
			if d.ID() == TypeDuration {
				_, readable = d.ToDuration(ticks)
			} else {
				at, _ := d.ToTime(ticks)
				readable = at.Year() >= 1 && at.Year() <= 9998
			}
			if readable && !ok {
				t.Fatalf("%s %d renders %q, which ParseTemporal does not read", d, ticks, text)
			}
			if d.ID() == TypeDuration {
				return nil
			}
			naive := d
			if d.ID() == TypeDatetime {
				naive = Datetime(u, "")
			}
			iso, wrote := AppendISO(nil, d, ticks)
			if wrote && string(iso) != FormatTemporal(naive, ticks) {
				t.Fatalf("AppendISO(%s, %d) = %q; FormatTemporal(%s) = %q", d, ticks, iso, naive, FormatTemporal(naive, ticks))
			}
			if at, _ := d.ToTime(ticks); !wrote && at.Year() >= 0 && at.Year() <= 9999 {
				t.Fatalf("AppendISO(%s, %d) wrote nothing for %v", d, ticks, at)
			}
			return nil
		})
	})
}

// textKindType is the type a kind of temporal text infers as, by the CSV reader's
// inference (infer.go's inferred.dtype).
var textKindType = map[TemporalTextKind]DataType{
	DateText:          Date,
	DatetimeText:      Datetime(Micro, ""),
	ZonedDatetimeText: Datetime(Micro, "UTC"),
	TimeText:          Time(Nano),
}

// FuzzTemporalText: text that ClassifyTemporalText calls a kind, ParseTemporalText
// reads as that kind's type, as the CSV reader's inference promises its parse; and
// text Polars' patterns read, read by them to the ticks Go's time.Parse reads it to,
// through ParseTemporal's layouts, wherever both read it.
func FuzzTemporalText(f *testing.F) {
	for _, s := range []string{
		"2024-01-02", "2024-1-2", "02/01/2024", "2.1.2024", "2024/01/02", "01/02/24",
		"2024-01-02T03:04", "2024-01-02 03:04:05", "2024-01-02T03:04:05.123456789",
		"2024-01-02T03:04:05Z", "2024-01-02T03:04:05+09:00", "2024-01-02 03:04:05 -0330",
		"2024-01-02T03:04:05.5+00:00", "2024-02-29", "2023-02-29", "0000-02-29", "9999-12-31T23:59:59.999999999Z",
		"03:04", "3:04", "03:04:05", "03:04:05.000000001", "24:00:00", "23:59:60", "12:00Z",
		"2024-01-02T03:04:05+24:00", "2024-01-02T", "2024-01-02 ", "T03:04", "", ":", "2024", "20240102",
		"2024-01-02T03:04:05.1234567890", "2024-01-02T03:04:05,5", "1970-01-01T00:00:00-00:01",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		fuzzcheck.Run(t, parserLimits, func() error {
			if k := ClassifyTemporalText(s); k != NoTemporalText {
				if _, ok := ParseTemporalText(textKindType[k], s); !ok {
					t.Fatalf("ClassifyTemporalText(%q) is %d, and ParseTemporalText does not read it as %s", s, k, textKindType[k])
				}
			}
			if _, ok := ScanTemporalText(s); !ok {
				return nil
			}
			for _, d := range []DataType{Date, Datetime(Nano, ""), Datetime(Micro, "UTC"), Time(Nano)} {
				a, aok := ParseTemporalText(d, s)
				b, bok := ParseTemporal(d, s)
				if aok && bok && a != b {
					t.Fatalf("%q as %s: %d by Polars' patterns, %d by time.Parse", s, d, a, b)
				}
			}
			return nil
		})
	})
}

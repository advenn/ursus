// Package model is qcheck's vocabulary, shared by every other package of the module:
// the types a generated column or an answer takes, a generated table, and a
// configuration a query runs under.
//
// It knows nothing of ursus, so that step 513's SQL side and step 514's shrinker read
// the same tables and configurations as the self check does; build maps a Type to
// ursus's, and sqlgen will map it to DuckDB's.
package model

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
)

// Type is a column's type: the ones a generated table holds, and the ones an
// expression or an aggregate over them answers. A plain type is its kind alone; a
// Datetime, a Duration, a Time and a Decimal pack their parameters above the kind's
// byte (step 539), so a Type stays a comparable number and each type a query meets
// is one value: a Datetime with its unit and zone, a Decimal with its precision and
// scale.
type Type uint32

// The kinds. Int8 and Int16 came first from casts, Int128 from an integer Sum,
// Uint64 from Count, Len and NUnique: ursus's own answer types (expr.go, agg.go).
// Step 539 adds the unsigned integers, Float32 and the parameterised kinds, whose
// types Datetime, Duration, Time and Decimal make.
const (
	Invalid Type = iota
	Bool
	Int8
	Int16
	Int32
	Int64
	Int128
	Uint64
	Float64
	String
	Date
	Uint8
	Uint16
	Uint32
	Float32
	DatetimeKind
	DurationKind
	TimeKind
	DecimalKind
)

// Base are the types of a core table's columns, step 453's list.
var Base = []Type{Int64, Int32, Float64, String, Bool, Date}

var typeNames = [...]string{"Invalid", "Bool", "Int8", "Int16", "Int32", "Int64", "Int128",
	"Uint64", "Float64", "String", "Date", "Uint8", "Uint16", "Uint32", "Float32",
	"Datetime", "Duration", "Time", "Decimal"}

// Unit is a Datetime's, a Duration's or a Time's tick, numbered as dtype.TimeUnit
// numbers them.
type Unit uint8

const (
	Sec Unit = iota
	Milli
	Micro
	Nano
)

// Units are every unit, coarsest first.
var Units = []Unit{Sec, Milli, Micro, Nano}

var unitNames = [...]string{"s", "ms", "us", "ns"}

func (u Unit) String() string {
	if int(u) < len(unitNames) {
		return unitNames[u]
	}
	return fmt.Sprintf("Unit(%d)", u)
}

// PerSecond is how many ticks of u a second holds.
func (u Unit) PerSecond() int64 { return [...]int64{1, 1_000, 1_000_000, 1_000_000_000}[u] }

// Nanos is one tick of u in nanoseconds.
func (u Unit) Nanos() int64 { return 1_000_000_000 / u.PerSecond() }

// PerDay is how many ticks of u a day holds.
func (u Unit) PerDay() int64 { return 86_400 * u.PerSecond() }

// Zones are the zones a zoned Datetime takes: a fixed set, whose rules over 1980 to
// 2035 every recent zone database agrees on (plan step 471). The first is none, a
// naive Datetime. Lord Howe's daylight saving time is half an hour; Kolkata's
// offset is half an hour and has none. Dublin and Casablanca are left out: their
// daylight saving time is negative in the database's main form and positive in the
// rearguard form CI's Ubuntu has (feature-gaps §9, step 272).
var Zones = []string{"", "UTC", "America/New_York", "Europe/Berlin", "Asia/Kolkata", "Australia/Lord_Howe"}

// Datetime is the Datetime of unit u in zone Zones[z].
func Datetime(u Unit, z int) Type { return DatetimeKind | Type(u)<<8 | Type(z)<<12 }

// Duration is the Duration of unit u.
func Duration(u Unit) Type { return DurationKind | Type(u)<<8 }

// Time is the Time of unit u.
func Time(u Unit) Type { return TimeKind | Type(u)<<8 }

// MaxDecimalPrecision is the most digits a Decimal holds (dtype).
const MaxDecimalPrecision = 38

// Decimal is the Decimal of precision p and scale s.
func Decimal(p, s int) Type { return DecimalKind | Type(p)<<8 | Type(s)<<16 }

// Kind is t without its parameters.
func (t Type) Kind() Type { return t & 0xff }

// Unit is a Datetime's, a Duration's or a Time's unit.
func (t Type) Unit() Unit { return Unit(t >> 8 & 0xf) }

// ZoneIndex is a Datetime's zone, an index of Zones: 0 is naive.
func (t Type) ZoneIndex() int { return int(t >> 12 & 0xf) }

// Zone is a Datetime's zone, "" when naive.
func (t Type) Zone() string { return Zones[t.ZoneIndex()] }

// Prec is a Decimal's precision.
func (t Type) Prec() int { return int(t >> 8 & 0xff) }

// Scale is a Decimal's scale.
func (t Type) Scale() int { return int(t >> 16 & 0xff) }

// String is t as ursus's dtype writes it: "Datetime(us, UTC)", "Decimal(10, 2)".
func (t Type) String() string {
	k := t.Kind()
	switch k {
	case DatetimeKind:
		if z := t.Zone(); z != "" {
			return "Datetime(" + t.Unit().String() + ", " + z + ")"
		}
		return "Datetime(" + t.Unit().String() + ")"
	case DurationKind, TimeKind:
		return typeNames[k] + "(" + t.Unit().String() + ")"
	case DecimalKind:
		return fmt.Sprintf("Decimal(%d, %d)", t.Prec(), t.Scale())
	}
	if int(k) < len(typeNames) && t == k {
		return typeNames[k]
	}
	return fmt.Sprintf("Type(%d)", uint32(t))
}

// ParseType reads a type as String writes it.
func ParseType(s string) (Type, bool) {
	for k := Bool; k <= Float32; k++ {
		if typeNames[k] == s {
			return k, true
		}
	}
	name, args, ok := strings.Cut(s, "(")
	if !ok || !strings.HasSuffix(args, ")") {
		return Invalid, false
	}
	parts := strings.Split(strings.TrimSuffix(args, ")"), ", ")
	unit := func(u string) (Unit, bool) {
		i := slices.Index(unitNames[:], u)
		return Unit(max(i, 0)), i >= 0
	}
	switch name {
	case "Datetime":
		u, ok := unit(parts[0])
		if !ok || len(parts) > 2 {
			return Invalid, false
		}
		z := 0
		if len(parts) == 2 {
			if z = slices.Index(Zones, parts[1]); z <= 0 {
				return Invalid, false
			}
		}
		return Datetime(u, z), true
	case "Duration", "Time":
		u, ok := unit(parts[0])
		if !ok || len(parts) != 1 {
			return Invalid, false
		}
		if name == "Time" {
			return Time(u), true
		}
		return Duration(u), true
	case "Decimal":
		if len(parts) != 2 {
			return Invalid, false
		}
		p, err1 := strconv.Atoi(parts[0])
		s, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil || p < 1 || p > MaxDecimalPrecision || s < 0 || s > p {
			return Invalid, false
		}
		return Decimal(p, s), true
	}
	return Invalid, false
}

// IsSignedInt is an integer of 8 to 128 bits.
func (t Type) IsSignedInt() bool { return t >= Int8 && t <= Int128 }

// IsUnsigned is an unsigned integer.
func (t Type) IsUnsigned() bool { return t == Uint8 || t == Uint16 || t == Uint32 || t == Uint64 }

// IsInt is a signed or an unsigned integer.
func (t Type) IsInt() bool { return t.IsSignedInt() || t.IsUnsigned() }

// IsFloat is Float32 or Float64.
func (t Type) IsFloat() bool { return t == Float32 || t == Float64 }

// IsNumeric is an integer or a float; a Decimal is not, its rules are its own.
func (t Type) IsNumeric() bool { return t.IsInt() || t.IsFloat() }

// IsDecimal is a Decimal.
func (t Type) IsDecimal() bool { return t.Kind() == DecimalKind }

// IsTemporal is a Date, a Datetime, a Duration or a Time.
func (t Type) IsTemporal() bool {
	k := t.Kind()
	return k == Date || k == DatetimeKind || k == DurationKind || k == TimeKind
}

// IsDatetime is a Datetime.
func (t Type) IsDatetime() bool { return t.Kind() == DatetimeKind }

// IsDuration is a Duration.
func (t Type) IsDuration() bool { return t.Kind() == DurationKind }

// IsTime is a Time.
func (t Type) IsTime() bool { return t.Kind() == TimeKind }

// IsInstant is a Date or a Datetime: what OffsetBy, Truncate and Round move.
func (t Type) IsInstant() bool { return t == Date || t.IsDatetime() }

// Bits is an integer's width, signed or unsigned.
func (t Type) Bits() int {
	switch t {
	case Int8, Uint8:
		return 8
	case Int16, Uint16:
		return 16
	case Int32, Uint32:
		return 32
	case Int64, Uint64:
		return 64
	case Int128:
		return 128
	}
	return 0
}

// SignedOfBits is the least signed integer of at least bits, Int128 past 64.
func SignedOfBits(bits int) Type {
	switch {
	case bits <= 8:
		return Int8
	case bits <= 16:
		return Int16
	case bits <= 32:
		return Int32
	case bits <= 64:
		return Int64
	}
	return Int128
}

// Arith is a type arithmetic is generated on: the integers but Int128, the floats
// and a Decimal. Int128's arithmetic is exact or refused (expr.go), so an integer
// Sum's answer is compared and sorted but not computed on; an unsigned integer's Neg
// is refused, which ast.NewUn says; temporal arithmetic has rules of its own
// (ast.NewBin).
func (t Type) Arith() bool { return (t.IsInt() && t != Int128) || t.IsFloat() || t.IsDecimal() }

// Promote is the type two operands meet at in arithmetic, a conditional's branches,
// a comparison and a fill by a column, as ursus's dtype.Promote answers for the
// types qcheck generates: a type with itself; two integers of one sign at the wider,
// a signed and an unsigned one at the least signed type holding both, Int128 beside
// a Uint64; two floats at the wider, an integer and a float at a float wide enough
// for the integer, Float32 for one of 16 bits or fewer; a Decimal and an integer or a
// Decimal at the Decimal holding both, none past 38 digits, and a Decimal and a float
// at Float64; two Datetimes of one zone, two Durations or two Times at the finer
// unit. Text, a Boolean, a Date beside a Datetime, and two zones meet only
// themselves (feature-gaps §9: a conditional's branches meet by Promote, which
// refuses the rest).
func Promote(a, b Type) (Type, bool) {
	switch {
	case a == b:
		return a, true
	case a.IsTemporal() || b.IsTemporal():
		if a.Kind() == b.Kind() && a.Kind() != Date && a.ZoneIndex() == b.ZoneIndex() {
			if a.Unit() > b.Unit() {
				return a, true
			}
			return b, true
		}
		return Invalid, false
	case a.IsDecimal() || b.IsDecimal():
		if a.IsFloat() || b.IsFloat() {
			return Float64, true
		}
		return DecimalMeet(a, b)
	case !a.IsNumeric() || !b.IsNumeric():
		return Invalid, false
	case a.IsFloat() && b.IsFloat():
		if a == Float64 || b == Float64 {
			return Float64, true
		}
		return Float32, true
	case a.IsFloat() || b.IsFloat():
		f, i := a, b
		if b.IsFloat() {
			f, i = b, a
		}
		if f == Float32 && i.Bits() > 16 {
			return Float64, true
		}
		return f, true
	case a == Int128 || b == Int128:
		return Int128, true
	case a.IsSignedInt() == b.IsSignedInt():
		if a.Bits() >= b.Bits() {
			return a, true
		}
		return b, true
	}
	s, u := a, b
	if b.IsSignedInt() {
		s, u = b, a
	}
	if u == Uint64 {
		return Int128, true
	}
	return SignedOfBits(max(s.Bits(), 2*u.Bits())), true
}

// DecimalDigits is t as a Decimal's precision and scale: a Decimal's own, an
// integer's the digits its width holds (dtype.DecimalDigits).
func DecimalDigits(t Type) (p, s int, ok bool) {
	switch t {
	case Int8, Uint8:
		return 3, 0, true
	case Int16, Uint16:
		return 5, 0, true
	case Int32, Uint32:
		return 10, 0, true
	case Int64:
		return 19, 0, true
	case Uint64:
		return 20, 0, true
	case Int128:
		return 39, 0, true
	}
	if t.IsDecimal() {
		return t.Prec(), t.Scale(), true
	}
	return 0, 0, false
}

// DecimalMeet is the Decimal holding every value of a and b, each a Decimal or an
// integer, or none past 38 digits (dtype.DecimalMeet).
func DecimalMeet(a, b Type) (Type, bool) {
	pa, sa, oka := DecimalDigits(a)
	pb, sb, okb := DecimalDigits(b)
	if !oka || !okb {
		return Invalid, false
	}
	s := max(sa, sb)
	p := max(pa-sa, pb-sb) + s
	if p > MaxDecimalPrecision {
		return Invalid, false
	}
	return Decimal(p, s), true
}

// Comparable is whether two types compare with Eq, Lt and the rest: where Promote
// meets them (expr/resolve.go resolveComparison).
func Comparable(a, b Type) bool {
	_, ok := Promote(a, b)
	return ok
}

// Orderable is a type Min, Max and Sort take.
func (t Type) Orderable() bool { return t != Invalid }

// Profile is how a column's values are drawn.
type Profile uint8

const (
	// Tame values: small integers, few distinct values, floats in quarters (whose
	// sums are exact), short ASCII text, dates of two recent years.
	Tame Profile = iota
	// Wild values: integers over the whole range, floats of arbitrary bits across
	// many magnitudes, text of any ASCII, dates across millennia.
	Wild
	// Extreme values among the wild: NaN, ±0, ±Inf, each type's least and greatest,
	// subnormals, Unicode, NUL, text of thousands of bytes.
	Extreme
)

var profileNames = [...]string{"tame", "wild", "extreme"}

func (p Profile) String() string {
	if int(p) < len(profileNames) {
		return profileNames[p]
	}
	return fmt.Sprintf("Profile(%d)", p)
}

// Column describes one generated column.
type Column struct {
	Name     string
	Type     Type
	Profile  Profile
	NullRate float64 // 0, 0.1, 0.5 or 1
	// Card bounds a tame column's distinct values, so keys repeat; 0 is no bound.
	Card int
}

// Table is a generated table: its columns' description and its rows, one Arrow
// record. A seed alone rebuilds it (gen.Table).
type Table struct {
	Name string
	Seed uint64
	Rows int
	Cols []Column
	Rec  arrow.RecordBatch
}

// Col is the column named name, or nil.
func (t *Table) Col(name string) *Column {
	for i := range t.Cols {
		if t.Cols[i].Name == name {
			return &t.Cols[i]
		}
	}
	return nil
}

// String describes the table on one line.
func (t *Table) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d rows, seed %d):", t.Name, t.Rows, t.Seed)
	for _, c := range t.Cols {
		fmt.Fprintf(&b, " %s %s/%s/null %.1f", c.Name, c.Type, c.Profile, c.NullRate)
		if c.Card > 0 {
			fmt.Fprintf(&b, "/card %d", c.Card)
		}
		b.WriteByte(';')
	}
	return strings.TrimSuffix(b.String(), ";")
}

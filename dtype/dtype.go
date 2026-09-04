package dtype

import (
	"strconv"
	"strings"
)

// DataType is a logical column type.
//
// It is a comparable value type: `a == b` is exact type equality, and DataType
// works as a map key. Parameterised and nested types keep their payload in an
// interned *typeExt, so two independently-constructed List(Int64) values share
// one pointer and compare equal.
//
// The zero value is Null.
type DataType struct {
	id    TypeID
	unit  TimeUnit // Time, Datetime, Duration
	prec  uint8    // Decimal
	scale uint8    // Decimal
	ext   *typeExt // interned; nil when the type needs no extra payload
}

// --- Simple types ------------------------------------------------------------
//
// Declared as vars rather than consts because DataType is a struct. They are
// never mutated; treat them as constants.
var (
	Null    = DataType{id: TypeNull}
	Bool    = DataType{id: TypeBool}
	Int8    = DataType{id: TypeInt8}
	Int16   = DataType{id: TypeInt16}
	Int32   = DataType{id: TypeInt32}
	Int64   = DataType{id: TypeInt64}
	Uint8   = DataType{id: TypeUint8}
	Uint16  = DataType{id: TypeUint16}
	Uint32  = DataType{id: TypeUint32}
	Uint64  = DataType{id: TypeUint64}
	Float32 = DataType{id: TypeFloat32}
	Float64 = DataType{id: TypeFloat64}
	Int128  = DataType{id: TypeInt128}
	String  = DataType{id: TypeString}
	Binary  = DataType{id: TypeBinary}
	Date    = DataType{id: TypeDate}
)

// --- Parameterised constructors ----------------------------------------------

// Time is a time of day at the given resolution.
func Time(u TimeUnit) DataType { return DataType{id: TypeTime, unit: u} }

// Duration is an elapsed time at the given resolution.
func Duration(u TimeUnit) DataType { return DataType{id: TypeDuration, unit: u} }

// Datetime is an instant at the given resolution. An empty tz means naive
// (wall-clock, no zone); otherwise tz is an IANA name such as "UTC".
//
// The distinction matters: converting a zone changes the wall clock and keeps the
// instant, while replacing a zone keeps the wall clock and changes the instant.
func Datetime(u TimeUnit, tz string) DataType {
	dt := DataType{id: TypeDatetime, unit: u}
	if tz != "" {
		dt.ext = intern(&typeExt{tz: tz})
	}
	return dt
}

// Decimal is a fixed-point decimal with the given precision (total digits) and
// scale (digits after the point), stored in 128 bits.
func Decimal(precision, scale uint8) DataType {
	return DataType{id: TypeDecimal, prec: precision, scale: scale}
}

// List is a variable-length list of inner.
func List(inner DataType) DataType {
	return DataType{id: TypeList, ext: intern(&typeExt{inner: inner})}
}

// Array is a fixed-length list of exactly size elements of inner.
func Array(inner DataType, size int) DataType {
	return DataType{id: TypeArray, ext: intern(&typeExt{inner: inner, size: size})}
}

// Struct is a composite of named fields, in order.
func Struct(fields ...Field) DataType {
	fs := make([]Field, len(fields))
	copy(fs, fields)
	return DataType{id: TypeStruct, ext: intern(&typeExt{fields: fs})}
}

// Enum is a string type whose category set is frozen and part of the type.
// Physically it is a Uint32 index into cats.
//
// Enum is internable precisely because its categories never change. Categorical —
// whose mapping grows as data arrives — is not, which is why it is deferred until
// there is a per-context string cache to own the mapping.
func Enum(cats ...string) DataType {
	cs := make([]string, len(cats))
	copy(cs, cats)
	return DataType{id: TypeEnum, ext: intern(&typeExt{cats: cs})}
}

// --- Accessors ---------------------------------------------------------------

// ID returns the type's shape tag.
func (d DataType) ID() TypeID { return d.id }

// TimeUnit returns the resolution of a Time, Datetime or Duration; Second otherwise.
func (d DataType) TimeUnit() TimeUnit { return d.unit }

// TimeZone returns a Datetime's IANA zone name, or "" if naive or not a Datetime.
func (d DataType) TimeZone() string {
	if d.ext == nil {
		return ""
	}
	return d.ext.tz
}

// Precision and Scale return a Decimal's parameters.
func (d DataType) Precision() uint8 { return d.prec }
func (d DataType) Scale() uint8     { return d.scale }

// Inner returns the element type of a List or Array, and Null otherwise.
func (d DataType) Inner() DataType {
	if d.ext == nil {
		return Null
	}
	return d.ext.inner
}

// Size returns an Array's fixed length, and 0 otherwise.
func (d DataType) Size() int {
	if d.ext == nil {
		return 0
	}
	return d.ext.size
}

// Fields returns a Struct's fields. The result aliases interned storage and must
// not be mutated.
func (d DataType) Fields() []Field {
	if d.ext == nil {
		return nil
	}
	return d.ext.fields
}

// Categories returns an Enum's category set. The result aliases interned storage
// and must not be mutated.
func (d DataType) Categories() []string {
	if d.ext == nil {
		return nil
	}
	return d.ext.cats
}

// CategoryIndex returns the physical index of an Enum category.
func (d DataType) CategoryIndex(s string) (uint32, bool) {
	if d.ext == nil || d.ext.catIdx == nil {
		return 0, false
	}
	i, ok := d.ext.catIdx[s]
	return i, ok
}

// --- Predicates --------------------------------------------------------------

func (d DataType) IsNull() bool { return d.id == TypeNull }
func (d DataType) IsBool() bool { return d.id == TypeBool }

func (d DataType) IsSignedInteger() bool {
	// TypeInt128 sits apart from the Int8..Int64 run in the enum, because that run
	// was numbered before 128-bit support existed and renumbering would churn every
	// table indexed by TypeID.
	return (d.id >= TypeInt8 && d.id <= TypeInt64) || d.id == TypeInt128
}

func (d DataType) IsUnsignedInteger() bool {
	return d.id >= TypeUint8 && d.id <= TypeUint64
}

func (d DataType) IsInteger() bool {
	return d.IsSignedInteger() || d.IsUnsignedInteger()
}

func (d DataType) IsFloat() bool {
	return d.id == TypeFloat32 || d.id == TypeFloat64
}

// IsNumeric reports whether arithmetic is defined on the type.
//
// Bool is deliberately excluded: `true + true` is a bug in every query it appears
// in, and rejecting it at type-resolution time is more useful than promoting to
// integer the way C does. Decimal is included; its kernels arrive later.
func (d DataType) IsNumeric() bool {
	return d.IsInteger() || d.IsFloat() || d.id == TypeDecimal
}

func (d DataType) IsTemporal() bool {
	return d.id == TypeDate || d.id == TypeTime || d.id == TypeDatetime || d.id == TypeDuration
}

func (d DataType) IsNested() bool {
	return d.id == TypeList || d.id == TypeArray || d.id == TypeStruct
}

// IsString reports whether the type's logical values are strings, including Enum.
//
// Careful: an Enum is logically a string but is stored as a Uint32 index, so a
// caller that uses IsString to decide how to READ a column will reach for the
// string accessor and find nothing there. Use it for semantic questions ("can this
// be compared to a string literal?") and Physical() for storage questions.
func (d DataType) IsString() bool {
	return d.id == TypeString || d.id == TypeEnum
}

// HasStringStorage reports whether values are stored as offsets + character data,
// i.e. whether Column.Strings() is the right accessor. This is the storage
// question, and it deliberately excludes Enum.
func (d DataType) HasStringStorage() bool {
	return d.id == TypeString || d.id == TypeBinary
}

// IsOrdered reports whether ursus can compute a total order over this type.
//
// It is the single source of truth for Sort, Min/Max, ordering comparisons and
// GroupBy, all of which previously carried their own copies of this list — five
// of them, already disagreeing about Binary, Enum and Decimal. Divergence there is
// worse than it sounds: the plan layer's copies decide what is REJECTED AT PLAN
// TIME, and the kernel layer's copies decide what actually works, so a mismatch
// either rejects a working feature or lets a query through to fail at runtime.
//
// Enum is ordered by its CATEGORY INDEX, not lexically. For a frozen category set
// the index order is the order the user declared — Enum("low","med","high") sorts
// low < med < high, which is almost always the intent and is not expressible
// lexically. Categorical, whose mapping grows at runtime, would make the same
// choice non-deterministic, which is one reason it stays unconstructible.
func (d DataType) IsOrdered() bool {
	switch {
	case d.IsNumeric(), d.IsTemporal():
		return true
	case d.id == TypeString, d.id == TypeBinary, d.id == TypeBool, d.id == TypeEnum:
		return true
	default:
		// Null, List, Array, Struct, Object, Categorical.
		return false
	}
}

// IsHashable reports whether values can be group-by keys or deduplicated.
//
// Currently identical to IsOrdered, and deliberately a separate name: they are
// different questions and will diverge (a Struct could be hashable field-by-field
// long before anyone defines a total order over it).
func (d DataType) IsHashable() bool { return d.IsOrdered() }

// BitWidth returns the storage width in bits, or 0 if the type is not fixed width.
func (d DataType) BitWidth() int {
	if int(d.id) >= len(bitWidths) {
		return 0
	}
	return bitWidths[d.id]
}

// IsFixedWidth reports whether values occupy a constant number of bits, i.e.
// whether the column has a flat values buffer and no offsets.
func (d DataType) IsFixedWidth() bool { return d.BitWidth() > 0 }

// Physical returns the storage type used to represent d.
//
// Kernels dispatch on the physical type: Date and Int32 share integer kernels,
// Datetime and Duration share Int64 kernels, Enum indexes are Uint32. Only
// operations that need calendar or category semantics look at the logical type.
func (d DataType) Physical() DataType {
	switch d.id {
	case TypeDate:
		return Int32
	case TypeTime, TypeDatetime, TypeDuration:
		return Int64
	case TypeEnum:
		return Uint32
	case TypeDecimal:
		// A Decimal is an integer of unscaled units: Decimal(10,2) value 12.34 is
		// stored as 1234. 128 bits because that is what Parquet's DECIMAL allows
		// and what precision up to 38 digits needs.
		//
		// This one line is what makes Decimal usable. Everything that dispatches on
		// the physical type — sort comparators, group keys, Min/Max, Take, Concat,
		// data.Values — already handles Int128, so they all start working at once.
		// The work is not in enabling them; it is in the guards that stop the
		// operations for which "the same integer" is the WRONG answer. See Cast in
		// internal/kernel, resolveArithmetic in internal/expr, and renderCell:
		// each rejects or corrects a path where the unscaled integer would leak.
		return Int128
	default:
		return d
	}
}

// FormatDecimal places the decimal point in an unscaled integer's digits.
//
//	FormatDecimal("1234", 2)  == "12.34"
//	FormatDecimal("5", 3)     == "0.005"
//	FormatDecimal("-5", 2)    == "-0.05"
//
// It takes the digits as a string rather than an Int128 so that dtype does not
// depend on the i128 package for what is purely presentation. Callers hold the
// scale (from DataType.Scale) and the digits (from Int128.String).
//
// Without this a Decimal renders as its storage — 12.34 printed as "1234" — which
// is not a rounding error but a factor of 10^scale, and looks entirely plausible.
func FormatDecimal(unscaled string, scale uint8) string {
	if scale == 0 {
		return unscaled
	}
	neg := strings.HasPrefix(unscaled, "-")
	digits := strings.TrimPrefix(unscaled, "-")

	s := int(scale)
	if len(digits) <= s {
		digits = strings.Repeat("0", s-len(digits)+1) + digits
	}
	out := digits[:len(digits)-s] + "." + digits[len(digits)-s:]
	if neg {
		return "-" + out
	}
	return out
}

// --- Equality and rendering ---------------------------------------------------

// Equal reports exact type equality. It is identical to ==, and exists because
// `a.Equal(b)` reads better in conditionals and because a future non-comparable
// type would keep the call sites working.
func (d DataType) Equal(o DataType) bool { return d == o }

// String renders the type with its parameters.
//
// This output is load-bearing: it appears in golden Explain files, in
// ursustest failure messages, and in every type error. It must stay stable.
func (d DataType) String() string {
	switch d.id {
	case TypeTime, TypeDuration:
		return d.id.String() + "(" + d.unit.String() + ")"

	case TypeDatetime:
		if tz := d.TimeZone(); tz != "" {
			return "Datetime(" + d.unit.String() + ", " + tz + ")"
		}
		return "Datetime(" + d.unit.String() + ")"

	case TypeDecimal:
		return "Decimal(" + strconv.Itoa(int(d.prec)) + ", " + strconv.Itoa(int(d.scale)) + ")"

	case TypeList:
		return "List(" + d.Inner().String() + ")"

	case TypeArray:
		return "Array(" + d.Inner().String() + ", " + strconv.Itoa(d.Size()) + ")"

	case TypeStruct:
		var b strings.Builder
		b.WriteString("Struct(")
		for i, f := range d.Fields() {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(f.String()) // includes the "!" non-nullable marker
		}
		b.WriteString(")")
		return b.String()

	case TypeEnum:
		return "Enum(" + strings.Join(d.Categories(), ", ") + ")"

	default:
		return d.id.String()
	}
}

// Package dtype is ursus's type system: DataType, Field and Schema.
//
// It is a public package and the definition site for these types. The root
// package re-exports everything here by type alias, so ursus.Int64 and
// ursus.Schema are the spellings you should normally write. Import dtype
// directly only when implementing a custom scan source.
//
// (It is public rather than internal for a mundane but real reason: an alias to
// an internal type renders on pkg.go.dev as a bare line with no methods
// documented, and DataType and Schema are the two most-read types in the library.)
//
// # DataType is comparable
//
// DataType is a small comparable struct, so `a == b` and `map[DataType]T` both
// work — the kernel registry and the type-promotion table depend on it. Nested
// and parameterised types keep their payload behind an interned pointer, so
// pointer equality is semantic equality. See intern.go.
//
// Its fields are unexported for exactly that reason: an exported Inner or Fields
// would let a caller construct a DataType that bypasses interning, at which point
// `==` silently starts returning false for equal types. Use the constructors.
package dtype

// TypeID identifies a type's shape. It is the switch tag for every dtype-erased
// dispatch in the engine.
type TypeID uint8

const (
	TypeNull TypeID = iota
	TypeBool

	TypeInt8
	TypeInt16
	TypeInt32
	TypeInt64

	TypeUint8
	TypeUint16
	TypeUint32
	TypeUint64

	TypeFloat32
	TypeFloat64

	TypeDecimal

	TypeString
	TypeBinary

	TypeDate     // days since the Unix epoch, int32
	TypeTime     // time of day since midnight, int64 in the type's unit
	TypeDatetime // instant since the Unix epoch, int64 in the type's unit
	TypeDuration // elapsed time, int64 in the type's unit

	TypeList   // variable length, one child type
	TypeArray  // fixed length, one child type
	TypeStruct // named fields

	TypeEnum // frozen category set, part of the type

	// TypeInt128 is the accumulator and output type of integer Sum. Constructible.
	//
	// Categorical and Uint128 remain reserved: Categorical cannot be interned the
	// way Enum can, because its category mapping grows at runtime, and Uint128 has
	// no use while Int128 covers every unsigned value with room to spare.
	TypeCategorical
	TypeInt128
	TypeUint128

	// TypeIDCount is one past the last TypeID. Tables indexed by TypeID are
	// declared [TypeIDCount]T.
	TypeIDCount
)

var typeIDNames = [TypeIDCount]string{
	TypeNull:        "Null",
	TypeBool:        "Bool",
	TypeInt8:        "Int8",
	TypeInt16:       "Int16",
	TypeInt32:       "Int32",
	TypeInt64:       "Int64",
	TypeUint8:       "Uint8",
	TypeUint16:      "Uint16",
	TypeUint32:      "Uint32",
	TypeUint64:      "Uint64",
	TypeFloat32:     "Float32",
	TypeFloat64:     "Float64",
	TypeDecimal:     "Decimal",
	TypeString:      "String",
	TypeBinary:      "Binary",
	TypeDate:        "Date",
	TypeTime:        "Time",
	TypeDatetime:    "Datetime",
	TypeDuration:    "Duration",
	TypeList:        "List",
	TypeArray:       "Array",
	TypeStruct:      "Struct",
	TypeEnum:        "Enum",
	TypeCategorical: "Categorical",
	TypeInt128:      "Int128",
	TypeUint128:     "Uint128",
}

// String returns the type's base name, without parameters.
func (id TypeID) String() string {
	if int(id) < len(typeIDNames) && typeIDNames[id] != "" {
		return typeIDNames[id]
	}
	return "Invalid"
}

// bitWidths is the storage width in bits of each fixed-width physical type.
// Zero means "not fixed width" (String, Binary, List, Struct, Null).
var bitWidths = [TypeIDCount]int{
	TypeBool:     1,
	TypeInt8:     8,
	TypeInt16:    16,
	TypeInt32:    32,
	TypeInt64:    64,
	TypeUint8:    8,
	TypeUint16:   16,
	TypeUint32:   32,
	TypeUint64:   64,
	TypeFloat32:  32,
	TypeFloat64:  64,
	TypeDecimal:  128,
	TypeDate:     32,
	TypeTime:     64,
	TypeDatetime: 64,
	TypeDuration: 64,
	TypeEnum:     32,
	TypeInt128:   128,
	TypeUint128:  128,
}

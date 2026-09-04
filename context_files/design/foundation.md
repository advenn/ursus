No Write tool is available in this read-only planning mode, so here is the complete design as my response.

---

# ursus FOUNDATION layer — implementation design

## 0. Decisions up front

| # | Defect / question | Decision |
|---|---|---|
| D2 | `DataType` not comparable | 32-byte struct, **unexported fields**, nested/enum payload behind an **interned `*typeExt`**. `map[DataType]X` and `==` work. Nested types ship in v0.1. |
| D2b | *(new)* `DataType` fields were exported | Exporting `Inner`/`Fields` lets callers forge a non-interned type and silently break `==`. Fields are unexported; accessors only. |
| D8 | Options hierarchy doesn't typecheck | **Generic option type = interface embedding the specific ones.** Assignability flows generic→specific for free. One adapter type per family. |
| D9 | Import cycle | **`ursus/dtype` is public and is the definition site**; root is a façade of aliases. Public leaves may import `internal/`. `LazyFrame.SQL` deleted. |
| D5 | `context` at execution boundaries | `LazyFrame.Schema()` → **`CollectSchema(ctx)`**; `Explain(ctx, …)`. Eager façade takes `ctx` as first param and is **not generated in step 1**. |
| D14 | SIMD differential test unsatisfiable | Scalar impls compile **unconditionally** under `xxxScalar`; only the ~3-line dispatch wrapper is build-tagged. |
| D16 | ~45 undeclared types | 11 declared now (§10), 34 backlogged with owners. `ursus.Field(name) Expr` deleted (collides with `type Field`). |
| OQ2 | Arrow ownership | **Hidden entirely.** No `Release()` on any public type. `Adopt` uses `Retain` + `runtime.AddCleanup`. Never use `memory.DefaultAllocator`. |
| OQ7 | Generate eager façade | **No.** Not in step 1, not until `LazyFrame` stabilizes, and never with `context.Background()`. |
| OQ8 | Categorical string cache | **Deferred.** `Enum` ships (frozen categories are part of the type → internable). `Categorical` cannot be, because its mapping mutates. `TypeCategorical` reserved. |
| — | Parquet dep blast radius | `pqarrow` for step 1 (~18 modules, incl. gRPC), confined to **two files** behind a `recordSource` seam; exit plan to `parquet/file` (~12 modules) is tech-debt item #1. |

---

## 1. Package layout and import invariants

```
ursus/                                module ursus, go 1.27
├── go.mod  Makefile  .golangci.yml  .github/workflows/ci.yml
├── doc.go              package doc: the 10 principles, the one-page tour
├── dtype_alias.go      MINE  aliases + re-exported ctors for ursus/dtype
├── errors.go           MINE  alias for uerr.Error, ErrorKind consts
├── enums.go            MINE  EngineKind, OptFlags, MissingPolicy, …
├── options_scan.go     MINE  ScanOption / ParquetScanOption / …
├── options_exec.go     MINE  ExecOption / CollectOption / *SinkOption
├── options_explain.go  MINE  ExplainOption
├── scan.go             MINE  ScanParquet, ScanParquetPaths, ScanArrow, ScanFunc
├── ursus.go            A1    Col, Lit, All, When, Concat
├── expr*.go            A1    Expr + namespaces
├── lazy.go             A1    LazyFrame
├── frame.go            A2    DataFrame
├── series.go           A2    Series[T]
│
├── dtype/              PUBLIC LEAF. imports: stdlib + internal/uerr        MINE
│   doc.go typeid.go timeunit.go dtype.go intern.go field.go schema.go categories.go
├── objstore/           PUBLIC LEAF. imports: stdlib only                   MINE
│   store.go local.go readerat.go
├── selector/           PUBLIC. imports: ursus, internal/expr               A1
├── sql/                PUBLIC. imports: ursus, internal/{expr,plan}        later
├── ursustest/          PUBLIC. imports: ursus, dtype, internal/{batch,plan} MINE
│
└── internal/
    ├── uerr/           LEAF. imports stdlib only                           MINE
    ├── bitmap/         imports stdlib                                      A2
    ├── batch/          imports dtype, bitmap, uerr                         A2
    ├── arrowx/         imports dtype, batch, uerr, arrow-go                MINE
    ├── scanopt/        imports dtype, objstore                             MINE
    ├── execopt/        imports dtype                                       MINE
    ├── source/         imports dtype, batch, uerr                          MINE
    │   ├── source.go registry.go
    │   ├── parquet/    imports source, arrowx, scanopt, objstore, arrow-go  MINE
    │   └── memsrc/     in-memory source for tests                           MINE
    ├── expr/           imports dtype, uerr                                  A1
    ├── plan/           imports dtype, expr, source, uerr                    A1
    ├── kernel/         imports bitmap, (simd)                               A2
    ├── physical/       imports batch, kernel, source, plan                  A2
    ├── exec/           imports physical, plan, execopt                      A2
    └── gen/            code generators (kernels; later, the eager façade)
```

**Import invariants** (put these in `doc.go` and enforce in CI with a 20-line `go list -deps` check):

1. **I1 — Levels.** `L0 = {dtype, objstore, internal/uerr, internal/bitmap}`, `L1 = {internal/batch, internal/arrowx, internal/scanopt, internal/execopt, internal/expr, internal/source}`, `L2 = {internal/plan, internal/source/*, internal/kernel}`, `L3 = {internal/physical, internal/exec}`, `L4 = {ursus}`, `L5 = {selector, sql, ursustest}`. A package may import strictly lower levels only.
2. **I2 — The root imports no public package except `ursus/dtype` and `ursus/objstore`,** both of which are L0 and import nothing from ursus. This is the whole cycle fix. Any proposed root method that would require importing `selector`, `sql`, or `ursustest` is rejected on sight — that is why `LazyFrame.SQL(string)` does not exist and `sql.Context.Query` does.
3. **I3 — `internal/` is module-scoped, not package-scoped.** `selector`, `sql` and `ursustest` are public *and* import `internal/expr`, `internal/plan`, `internal/batch`. This is what lets them speak the IR without going through the root, and it is why the layout works at all.
4. **I4 — No internal type appears in an exported *signature* of `ursus/dtype` or `ursus/objstore`.** (They may appear in exported signatures of the root and in unexported method signatures anywhere — see the options scheme.)
5. **I5 — No `init()` outside `internal/kernel`;** no registry-based source discovery, no blank imports.

### Why `ursus/dtype` is public rather than `internal/dtype` + aliases

If `DataType`/`Schema` lived in `internal/`, the root's `type Schema = dtype.Schema` would render on pkg.go.dev as a bare alias line with **zero methods documented** — for the two most-read types in the library. Making the definition site public costs one extra importable package and buys correct godoc. The root still aliases everything, so `ursus.Schema` and `ursus.Int64` are the blessed spellings; `ursus/dtype` is documented as "the definition site — import it directly only from a custom scan source."

The same argument does *not* apply to `internal/uerr` (one struct, two methods — documented on the alias) or `internal/batch` (`Column` is an opaque handle). Those stay internal.

### Repo hygiene note

`/home/beck/GolandProjects/ursus/main.go` is `package main` in the module root and **must be deleted** — it collides with `package ursus`. If a CLI is wanted later it goes in `cmd/ursus/`.

---

## 2. `ursus/dtype` — the type system

### 2.1 `typeid.go`

```go
package dtype

// TypeID identifies a logical type; it is the discriminant of [DataType].
//
// The numeric values are part of the serialized plan format. Never reorder
// them, never reuse a retired value: append new types before numTypeIDs.
type TypeID uint8

const (
	// TypeUnknown is the zero value. It denotes a type that has not been
	// resolved yet — the type of an integer literal before type coercion,
	// or the type of a column in a schema that has not been read. It never
	// appears in a resolved schema; the optimizer's coercion pass either
	// replaces it or reports an error.
	TypeUnknown TypeID = iota

	TypeNull    // the all-null type; coerces to anything
	TypeBoolean // bit-packed

	TypeInt8
	TypeInt16
	TypeInt32
	TypeInt64
	TypeInt128 // reserved (P2): no constructor in v0.1

	TypeUint8
	TypeUint16
	TypeUint32
	TypeUint64
	TypeUint128 // reserved (P2)

	TypeFloat16 // reserved (P2)
	TypeFloat32
	TypeFloat64

	TypeDecimal // 128-bit backing (P1)

	TypeString // UTF-8; storage layout (offsets vs view) is not part of the type
	TypeBinary

	TypeDate     // days since the Unix epoch
	TypeTime     // time of day
	TypeDatetime // instant, with optional IANA time zone
	TypeDuration

	TypeList   // variable-length
	TypeArray  // fixed-length
	TypeStruct

	TypeCategorical // reserved; see [Categories]
	TypeEnum

	numTypeIDs
)

var typeIDNames = [numTypeIDs]string{
	TypeUnknown: "unknown", TypeNull: "null", TypeBoolean: "bool",
	TypeInt8: "i8", TypeInt16: "i16", TypeInt32: "i32", TypeInt64: "i64",
	TypeInt128: "i128",
	TypeUint8: "u8", TypeUint16: "u16", TypeUint32: "u32", TypeUint64: "u64",
	TypeUint128: "u128",
	TypeFloat16: "f16", TypeFloat32: "f32", TypeFloat64: "f64",
	TypeDecimal: "decimal", TypeString: "str", TypeBinary: "bin",
	TypeDate: "date", TypeTime: "time", TypeDatetime: "datetime",
	TypeDuration: "duration",
	TypeList: "list", TypeArray: "array", TypeStruct: "struct",
	TypeCategorical: "categorical", TypeEnum: "enum",
}

// String returns the type's base name, without parameters. Use
// [DataType.String] for the full canonical spelling.
func (id TypeID) String() string {
	if id < numTypeIDs && typeIDNames[id] != "" {
		return typeIDNames[id]
	}
	return "TypeID(" + strconv.Itoa(int(id)) + ")"
}
```

A `TestTypeIDNamesComplete` asserts every `id < numTypeIDs` has a non-empty entry. That is why we hand-write this table instead of pulling in `stringer`.

### 2.2 `timeunit.go`

```go
package dtype

// TimeUnit is the resolution of a [Time], [Datetime] or [Duration] type.
type TimeUnit uint8

const (
	UnitSecond TimeUnit = iota
	UnitMilli
	UnitMicro
	UnitNano

	numTimeUnits
)

var timeUnitNames = [numTimeUnits]string{
	UnitSecond: "s", UnitMilli: "ms", UnitMicro: "us", UnitNano: "ns",
}

func (u TimeUnit) String() string {
	if u < numTimeUnits {
		return timeUnitNames[u]
	}
	return "TimeUnit(" + strconv.Itoa(int(u)) + ")"
}

// Duration returns the length of one tick.
func (u TimeUnit) Duration() time.Duration {
	switch u {
	case UnitSecond:
		return time.Second
	case UnitMilli:
		return time.Millisecond
	case UnitMicro:
		return time.Microsecond
	default:
		return time.Nanosecond
	}
}

// Valid reports whether u is one of the four defined units.
func (u TimeUnit) Valid() bool { return u < numTimeUnits }
```

Note: unlike Polars we keep `UnitSecond`, because Arrow and Parquet both have it and dropping it would force a lossy cast at the I/O boundary.

### 2.3 `dtype.go`

```go
package dtype

// DataType is a logical column type.
//
// # Comparability
//
// DataType is comparable: it works as a map key and with ==. Nested and enum
// payloads live behind an interned pointer, so structurally identical types
// share one payload:
//
//	dtype.List(dtype.Int64) == dtype.List(dtype.Int64)                  // true
//	dtype.Struct(f("a", Int64)) == dtype.Struct(f("a", Int64))          // true
//
// That invariant is why every field is unexported: a DataType assembled by any
// means other than the constructors in this package would compare unequal to an
// identical one built properly. The kernel registry, the type-coercion table
// and dtype-based selector expansion all key on DataType, so this matters.
//
// The zero DataType has ID [TypeUnknown].
//
// Layout is exactly 32 bytes on 64-bit platforms.
type DataType struct {
	id    TypeID   // discriminant
	unit  TimeUnit // Time, Datetime, Duration
	prec  uint8    // Decimal precision, 1..38
	scale int8     // Decimal scale; may be negative, as in Arrow
	width int32    // Array element count
	tz    string   // Datetime IANA name; "" means naive. Not interned: string
	                //   equality is already structural, and this keeps the very
	                //   common Datetime case allocation- and lock-free.
	ext *typeExt   // List/Array element, Struct fields, Enum categories.
	                //   nil for every scalar type. Interned; see intern.go.
}

// ---------------------------------------------------------------- scalars

// The scalar types.
//
// These are variables because Go has no struct constants. Never assign to
// them; TestScalarTypeVars in this package guards against in-repo accidents.
var (
	Unknown = DataType{id: TypeUnknown}
	Null    = DataType{id: TypeNull}
	Boolean = DataType{id: TypeBoolean}

	Int8  = DataType{id: TypeInt8}
	Int16 = DataType{id: TypeInt16}
	Int32 = DataType{id: TypeInt32}
	Int64 = DataType{id: TypeInt64}

	Uint8  = DataType{id: TypeUint8}
	Uint16 = DataType{id: TypeUint16}
	Uint32 = DataType{id: TypeUint32}
	Uint64 = DataType{id: TypeUint64}

	Float32 = DataType{id: TypeFloat32}
	Float64 = DataType{id: TypeFloat64}

	String = DataType{id: TypeString}
	Binary = DataType{id: TypeBinary}
	Date   = DataType{id: TypeDate}
)

// Of returns the parameterless DataType for id. It panics if id names a
// parameterized type (Decimal, Datetime, List, …); use the constructor for
// those. Of exists for generated and reflective code that must not touch the
// package-level variables.
func Of(id TypeID) DataType {
	if parameterized(id) {
		panic("ursus/dtype: Of: " + id.String() + " is parameterized")
	}
	return DataType{id: id}
}

// ------------------------------------------------------- constructors
//
// These take literal arguments and panic on nonsense, in the same spirit as
// regexp.MustCompile. Anything derived from *data* goes through ParseType,
// which returns an error.

// Datetime returns a timestamp type. tz is an IANA name ("UTC",
// "Europe/Berlin") or "" for a naive timestamp. It panics if u is invalid.
func Datetime(u TimeUnit, tz string) DataType {
	mustUnit("Datetime", u)
	return DataType{id: TypeDatetime, unit: u, tz: tz}
}

// Duration returns an elapsed-time type. It panics if u is invalid.
func Duration(u TimeUnit) DataType {
	mustUnit("Duration", u)
	return DataType{id: TypeDuration, unit: u}
}

// Time returns a time-of-day type. It panics if u is invalid.
func Time(u TimeUnit) DataType {
	mustUnit("Time", u)
	return DataType{id: TypeTime, unit: u}
}

// Decimal returns a fixed-point type with the given precision (total
// significant digits, 1..38) and scale (digits after the point; may be
// negative). It panics on out-of-range arguments.
func Decimal(precision uint8, scale int8) DataType {
	if precision == 0 || precision > 38 {
		panic(fmt.Sprintf("ursus/dtype: Decimal: precision %d out of range 1..38", precision))
	}
	if int(scale) > int(precision) {
		panic(fmt.Sprintf("ursus/dtype: Decimal: scale %d exceeds precision %d", scale, precision))
	}
	return DataType{id: TypeDecimal, prec: precision, scale: scale}
}

// List returns a variable-length list type.
func List(inner DataType) DataType {
	return nest(DataType{id: TypeList}, &typeExt{inner: inner})
}

// Array returns a fixed-length list type. It panics if width is negative.
func Array(inner DataType, width int) DataType {
	if width < 0 || width > math.MaxInt32 {
		panic(fmt.Sprintf("ursus/dtype: Array: width %d out of range", width))
	}
	return nest(DataType{id: TypeArray, width: int32(width)}, &typeExt{inner: inner})
}

// Struct returns a struct type. Field names must be non-empty and unique; it
// panics otherwise.
func Struct(fields ...Field) DataType {
	seen := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		if f.Name == "" {
			panic("ursus/dtype: Struct: empty field name")
		}
		if _, dup := seen[f.Name]; dup {
			panic("ursus/dtype: Struct: duplicate field name " + strconv.Quote(f.Name))
		}
		seen[f.Name] = struct{}{}
	}
	return nest(DataType{id: TypeStruct}, &typeExt{fields: slices.Clone(fields)})
}

// Enum returns an enumerated string type with a frozen, ordered category list.
// Categories must be non-empty and unique; it panics otherwise.
//
// The category list is part of the type: Enum("a","b") != Enum("b","a").
func Enum(categories ...string) DataType {
	return nest(DataType{id: TypeEnum}, &typeExt{cats: newCategories(categories)})
}

// -------------------------------------------------------------- accessors

func (d DataType) ID() TypeID        { return d.id }
func (d DataType) TimeUnit() TimeUnit { return d.unit }
func (d DataType) TimeZone() string  { return d.tz }
func (d DataType) Precision() uint8  { return d.prec }
func (d DataType) Scale() int8       { return d.scale }

// Width returns the element count of an [Array] type, or 0.
func (d DataType) Width() int { return int(d.width) }

// Inner returns the element type of a List or Array, or the zero DataType.
func (d DataType) Inner() DataType {
	if d.ext == nil {
		return DataType{}
	}
	return d.ext.inner
}

// NumFields returns the number of fields of a Struct type, or 0.
func (d DataType) NumFields() int {
	if d.ext == nil {
		return 0
	}
	return len(d.ext.fields)
}

// Field returns the i'th field of a Struct type. It panics if i is out of range.
func (d DataType) Field(i int) Field {
	if d.ext == nil || i < 0 || i >= len(d.ext.fields) {
		panic("ursus/dtype: DataType.Field: index out of range")
	}
	return d.ext.fields[i]
}

// Fields iterates the fields of a Struct type. It yields nothing for other types.
func (d DataType) Fields() iter.Seq2[int, Field] {
	return func(yield func(int, Field) bool) {
		if d.ext == nil {
			return
		}
		for i, f := range d.ext.fields {
			if !yield(i, f) {
				return
			}
		}
	}
}

// FieldByName looks up a Struct field.
func (d DataType) FieldByName(name string) (Field, bool) {
	if d.ext == nil {
		return Field{}, false
	}
	for _, f := range d.ext.fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

// Categories returns the frozen category list of an Enum type, or nil.
func (d DataType) Categories() *Categories {
	if d.ext == nil {
		return nil
	}
	return d.ext.cats
}

// -------------------------------------------------------------- predicates

func (d DataType) IsSignedInteger() bool {
	switch d.id {
	case TypeInt8, TypeInt16, TypeInt32, TypeInt64, TypeInt128:
		return true
	}
	return false
}

func (d DataType) IsUnsignedInteger() bool {
	switch d.id {
	case TypeUint8, TypeUint16, TypeUint32, TypeUint64, TypeUint128:
		return true
	}
	return false
}

func (d DataType) IsInteger() bool { return d.IsSignedInteger() || d.IsUnsignedInteger() }

func (d DataType) IsFloat() bool {
	switch d.id {
	case TypeFloat16, TypeFloat32, TypeFloat64:
		return true
	}
	return false
}

// IsNumeric reports whether arithmetic kernels apply. Decimal counts.
func (d DataType) IsNumeric() bool { return d.IsInteger() || d.IsFloat() || d.id == TypeDecimal }

func (d DataType) IsTemporal() bool {
	switch d.id {
	case TypeDate, TypeTime, TypeDatetime, TypeDuration:
		return true
	}
	return false
}

func (d DataType) IsNested() bool {
	switch d.id {
	case TypeList, TypeArray, TypeStruct:
		return true
	}
	return false
}

func (d DataType) IsString() bool { return d.id == TypeString }
func (d DataType) IsBinary() bool { return d.id == TypeBinary }

// IsPrimitive reports whether values of this type occupy a fixed number of
// bits in a single values buffer. Boolean is primitive but bit-packed.
func (d DataType) IsPrimitive() bool {
	return d.IsNumeric() || d.IsTemporal() || d.id == TypeBoolean
}

// IsKnown reports whether the type is resolved, i.e. not [TypeUnknown].
func (d DataType) IsKnown() bool { return d.id != TypeUnknown }

// BitWidth returns the storage width in bits of a primitive type, or 0.
// Boolean reports 1.
func (d DataType) BitWidth() int { … }

// Physical returns the storage type: the type the kernels actually see.
//
//	Date               → Int32
//	Time/Datetime/…    → Int64
//	Categorical/Enum   → Uint32   (dictionary indices)
//	Decimal            → Int128
//	everything else    → itself
func (d DataType) Physical() DataType {
	switch d.id {
	case TypeDate:
		return Int32
	case TypeTime, TypeDatetime, TypeDuration:
		return Int64
	case TypeCategorical, TypeEnum:
		return Uint32
	case TypeDecimal:
		return DataType{id: TypeInt128}
	default:
		return d
	}
}

// Equal is == . It exists so call sites read well and so a future laxer
// comparison has a home.
func (d DataType) Equal(o DataType) bool { return d == o }

// String returns the canonical spelling of the type:
//
//	i64                       f64            str           bool
//	date                      time[us]       duration[ns]
//	datetime[us]              datetime[us,Europe/Berlin]
//	decimal(38,10)
//	list[i64]                 array[i64;3]
//	struct{"a":i64,"b":str!}  enum{"red","green"}
//
// String is INJECTIVE — distinct types produce distinct strings. The nested
// type intern table is keyed on it, and ParseType is its inverse. Any change
// here must preserve injectivity; TestStringInjective enforces it over a
// generated corpus.
func (d DataType) String() string {
	var b strings.Builder
	d.writeTo(&b)
	return b.String()
}

func (d DataType) writeTo(b *strings.Builder) {
	switch d.id {
	case TypeTime, TypeDuration:
		fmt.Fprintf(b, "%s[%s]", d.id, d.unit)
	case TypeDatetime:
		if d.tz == "" {
			fmt.Fprintf(b, "datetime[%s]", d.unit)
		} else {
			fmt.Fprintf(b, "datetime[%s,%s]", d.unit, d.tz)
		}
	case TypeDecimal:
		fmt.Fprintf(b, "decimal(%d,%d)", d.prec, d.scale)
	case TypeList:
		b.WriteString("list[")
		d.Inner().writeTo(b)
		b.WriteByte(']')
	case TypeArray:
		b.WriteString("array[")
		d.Inner().writeTo(b)
		fmt.Fprintf(b, ";%d]", d.width)
	case TypeStruct:
		b.WriteString("struct{")
		for i, f := range d.ext.fields {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(strconv.Quote(f.Name))
			b.WriteByte(':')
			f.Type.writeTo(b)
			if !f.Nullable {
				b.WriteByte('!')
			}
		}
		b.WriteByte('}')
	case TypeEnum:
		b.WriteString("enum{")
		for i, c := range d.ext.cats.values {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(strconv.Quote(c))
		}
		b.WriteByte('}')
	default:
		b.WriteString(d.id.String())
	}
}
```

### 2.4 `intern.go` — the D2 fix

```go
package dtype

// typeExt carries the payload of the types that cannot fit in DataType's
// scalar fields. Instances are interned: nest() guarantees that two
// structurally identical types share one *typeExt, which is what makes
// DataType's == structural rather than referential.
//
// A typeExt is immutable after nest() returns it. Nothing in this package
// mutates one, and no accessor hands out its slices.
type typeExt struct {
	inner  DataType    // List, Array
	fields []Field     // Struct
	cats   *Categories // Enum
}

// internTable maps a canonical type string to the *typeExt for that type.
//
// Entries are never evicted, exactly as reflect.Type canonicalisation never
// evicts. The table is bounded by the number of distinct nested types a
// program constructs, which is a property of its schemas, not of its data. If
// that ever becomes a problem the values can move behind weak.Pointer without
// changing any caller; InternedTypes exists to notice.
var internTable sync.Map // string -> *typeExt

// nest attaches an interned copy of ext to dt.
//
// The intern key is dt.String(), which is injective and already includes every
// discriminating field (id, width, and ext itself), so distinct types can
// never collide and identical types always do.
func nest(dt DataType, ext *typeExt) DataType {
	dt.ext = ext
	key := dt.String()
	if v, ok := internTable.Load(key); ok {
		dt.ext = v.(*typeExt)
		return dt
	}
	v, _ := internTable.LoadOrStore(key, ext)
	dt.ext = v.(*typeExt)
	return dt
}

// InternedTypes reports how many distinct nested types have been interned in
// this process. It is a diagnostic, not an API contract.
func InternedTypes() int {
	n := 0
	internTable.Range(func(_, _ any) bool { n++; return true })
	return n
}
```

Cost profile: scalar and datetime types never touch the table. Nested-type construction is one `sync.Map.Load` on the hot path (read-mostly, so it is a lock-free atomic load after the first store). Nested types are constructed once per plan node, never per row.

### 2.5 `field.go`

```go
package dtype

// Field is a named, typed, optionally nullable column or struct member.
// Field is comparable, because DataType is.
type Field struct {
	Name     string
	Type     DataType
	Nullable bool
}

// NewField returns a nullable field. Note that the zero Field is NOT nullable;
// Arrow's default is, so prefer this constructor to a struct literal unless
// you are setting Nullable explicitly.
func NewField(name string, dt DataType) Field {
	return Field{Name: name, Type: dt, Nullable: true}
}

func (f Field) WithNullable(b bool) Field { f.Nullable = b; return f }
func (f Field) WithName(name string) Field { f.Name = name; return f }
func (f Field) WithType(dt DataType) Field { f.Type = dt; return f }

// String returns `name: type` with ` not null` appended when appropriate,
// matching Arrow's spelling.
func (f Field) String() string {
	if f.Nullable {
		return f.Name + ": " + f.Type.String()
	}
	return f.Name + ": " + f.Type.String() + " not null"
}
```

**Nullability policy for v0.1:** carried faithfully across the Arrow/Parquet boundary and printed, but the *engine* treats every column as potentially nullable. `ursustest.AssertSchemaEqual` compares it by default; `ursustest.IgnoreNullability()` opts out.

### 2.6 `schema.go`

```go
package dtype

// Schema is an ordered, name-unique sequence of [Field]s.
//
// Schema is an immutable value. Copying one is cheap (two words) and safe:
// nothing mutates a Schema after NewSchema returns, and every "mutator" here
// returns a new Schema. Schema is deliberately NOT comparable — use
// [Schema.Equal].
//
// The zero Schema is a valid, empty schema.
type Schema struct {
	fields []Field
	index  map[string]int // nil when len(fields) <= linearScanMax
}

// Schemas up to this size use a linear scan; building a map costs more than it
// saves. The planner builds a Schema per node per query, so this matters.
const linearScanMax = 12

// NewSchema returns a schema over fields. It reports an error if any name is
// empty or repeated.
func NewSchema(fields ...Field) (Schema, error) {
	fs := slices.Clone(fields)
	var idx map[string]int
	if len(fs) > linearScanMax {
		idx = make(map[string]int, len(fs))
	}
	seen := idx
	if seen == nil {
		seen = make(map[string]int, len(fs))
	}
	for i, f := range fs {
		if f.Name == "" {
			return Schema{}, uerr.New(uerr.KindInvalidArgument, "schema",
				"field %d has an empty name", i)
		}
		if prev, dup := seen[f.Name]; dup {
			return Schema{}, uerr.New(uerr.KindDuplicateColumn, "schema",
				"duplicate column %q at positions %d and %d", f.Name, prev, i)
		}
		seen[f.Name] = i
	}
	return Schema{fields: fs, index: idx}, nil
}

// MustSchema is NewSchema for package-level variables and tests. It panics on
// error.
func MustSchema(fields ...Field) Schema {
	s, err := NewSchema(fields...)
	if err != nil {
		panic(err)
	}
	return s
}

func (s Schema) Len() int          { return len(s.fields) }
func (s Schema) Field(i int) Field { return s.fields[i] }

// Fields returns a copy of the field slice. Prefer [Schema.All] or
// [Schema.Field] in hot paths.
func (s Schema) Fields() []Field { return slices.Clone(s.fields) }

// All iterates (index, field) pairs in schema order.
func (s Schema) All() iter.Seq2[int, Field] {
	return func(yield func(int, Field) bool) {
		for i, f := range s.fields {
			if !yield(i, f) {
				return
			}
		}
	}
}

// IndexOf returns the position of name, or -1.
func (s Schema) IndexOf(name string) int {
	if s.index != nil {
		if i, ok := s.index[name]; ok {
			return i
		}
		return -1
	}
	for i := range s.fields {
		if s.fields[i].Name == name {
			return i
		}
	}
	return -1
}

func (s Schema) ByName(name string) (Field, bool) {
	if i := s.IndexOf(name); i >= 0 {
		return s.fields[i], true
	}
	return Field{}, false
}

func (s Schema) Contains(name string) bool { return s.IndexOf(name) >= 0 }

func (s Schema) Names() []string { … }
func (s Schema) Types() []DataType { … }

// Project returns the sub-schema at the given indices, in the given order.
// This is the operation projection pushdown performs.
func (s Schema) Project(indices ...int) (Schema, error) {
	out := make([]Field, 0, len(indices))
	seen := make(map[int]struct{}, len(indices))
	for _, i := range indices {
		if i < 0 || i >= len(s.fields) {
			return Schema{}, uerr.New(uerr.KindInvalidArgument, "project",
				"column index %d out of range [0,%d)", i, len(s.fields))
		}
		if _, dup := seen[i]; dup {
			return Schema{}, uerr.New(uerr.KindDuplicateColumn, "project",
				"column index %d repeated", i)
		}
		seen[i] = struct{}{}
		out = append(out, s.fields[i])
	}
	return NewSchema(out...)
}

// Select is Project by name. Unknown names produce an unknown-column error
// with the available list and near-match suggestions.
func (s Schema) Select(names ...string) (Schema, error) {
	idx := make([]int, 0, len(names))
	for _, n := range names {
		i := s.IndexOf(n)
		if i < 0 {
			return Schema{}, s.UnknownColumnError("select", n)
		}
		idx = append(idx, i)
	}
	return s.Project(idx...)
}

func (s Schema) Append(fields ...Field) (Schema, error) { … }
func (s Schema) Replace(i int, f Field) (Schema, error) { … }
func (s Schema) Rename(old, new string) (Schema, error) { … }
func (s Schema) Drop(names ...string) Schema            { … }

func (s Schema) Equal(o Schema) bool {
	return slices.Equal(s.fields, o.fields) // Field is comparable
}

// EqualIgnoreNullability compares names, order and types only.
func (s Schema) EqualIgnoreNullability(o Schema) bool { … }

// UnknownColumnError builds the canonical unknown-column error for this
// schema, complete with the available-column list and near-match suggestions.
// Every layer that resolves a name should call it, so the message is identical
// everywhere.
func (s Schema) UnknownColumnError(op, name string) error {
	return uerr.UnknownName(op, "column", name, s.Names())
}

// String renders the schema one field per line:
//
//	Schema(3):
//	  tenant_id: str
//	  ts:        datetime[us,UTC]
//	  latency:   f64 not null
func (s Schema) String() string { … }
```

`Schema.Project` is the single function projection pushdown needs, and it is why the scan contract uses `[]int` rather than `[]string`.

### 2.7 `categories.go`

```go
package dtype

// Categories is the frozen, ordered category list of an [Enum] type.
//
// Enum categories are part of the type: two Enum types are equal only if their
// lists are identical and in the same order. That is what lets Enum live
// inside a comparable, interned DataType.
//
// Categorical — Polars' runtime-inferred variant — is deliberately absent from
// v0.1. Its mapping grows as data arrives, so it cannot be part of an immutable
// interned type. When it lands, TypeCategorical will carry a *Categories handle
// compared by IDENTITY, and that identity will be scoped to an execution
// Context rather than being process-global, avoiding Polars' global-string-cache
// footgun. TypeCategorical's numeric value is reserved now so the enum
// numbering never shifts.
type Categories struct {
	values []string
	index  map[string]uint32
}

func newCategories(values []string) *Categories { … } // panics on empty/dup

func (c *Categories) Len() int                        { return len(c.values) }
func (c *Categories) Value(i uint32) (string, bool)   { … }
func (c *Categories) Index(s string) (uint32, bool)   { … }
func (c *Categories) Values() []string                { return slices.Clone(c.values) }
```

---

## 3. `internal/uerr` — error infrastructure

```go
// Package uerr defines ursus' structured error type.
//
// Every error a caller sees from ursus is a *Error. It carries a machine-
// readable Kind, the operation that failed, a one-line message, zero or more
// detail lines, and — when the failure happened inside a query plan — the plan
// node it is attributed to.
//
// Error's Error method returns a MULTI-LINE string. That is deliberate: an
// ursus error is a terminal diagnostic for a human debugging a query, and the
// available-columns list and plan context are the whole value. Use
// [Error.Summary] where a single line is required.
package uerr

type Kind uint8

const (
	KindUnknown Kind = iota
	KindUnknownColumn
	KindDuplicateColumn
	KindTypeMismatch
	KindSchemaMismatch
	KindInvalidArgument
	KindUnsupported
	KindIO
	KindCanceled
	KindOutOfMemory
	KindInternal
	numKinds
)

func (k Kind) String() string { … }

// Sentinels for errors.Is. They carry no context; they exist only to be
// matched against.
type kindError Kind

func (k kindError) Error() string { return Kind(k).String() }

var (
	ErrUnknownColumn   error = kindError(KindUnknownColumn)
	ErrDuplicateColumn error = kindError(KindDuplicateColumn)
	ErrTypeMismatch    error = kindError(KindTypeMismatch)
	ErrSchemaMismatch  error = kindError(KindSchemaMismatch)
	ErrInvalidArgument error = kindError(KindInvalidArgument)
	ErrUnsupported     error = kindError(KindUnsupported)
	ErrIO              error = kindError(KindIO)
	ErrOutOfMemory     error = kindError(KindOutOfMemory)
	ErrInternal        error = kindError(KindInternal)
)

// NodeRef attributes an error to a node of a query plan.
type NodeRef struct {
	ID       int      // 1-based, assigned during planning, stable within a plan
	Kind     string   // "SELECT", "FILTER", "PARQUET SCAN"
	Label    string   // node-specific detail: the predicate, the path glob
	Ancestry []string // rendered ancestors, innermost first
}

type Error struct {
	Kind    Kind
	Op      string   // "select", "scan/parquet", "cast"
	Message string   // one line, no trailing punctuation
	Details []string // extra lines, indented two spaces under Message
	Node    *NodeRef
	Err     error // wrapped cause
}

func New(kind Kind, op, format string, args ...any) *Error {
	return &Error{Kind: kind, Op: op, Message: fmt.Sprintf(format, args...)}
}

func Wrap(err error, kind Kind, op, format string, args ...any) *Error {
	e := New(kind, op, format, args...)
	e.Err = err
	return e
}

func (e *Error) WithDetail(format string, args ...any) *Error {
	e.Details = append(e.Details, fmt.Sprintf(format, args...))
	return e
}

func (e *Error) At(n *NodeRef) *Error { e.Node = n; return e }

// Attribute attaches n to err if err is a *Error that is not already
// attributed. It is what the plan executor calls as an error unwinds, so the
// INNERMOST node wins.
func Attribute(err error, n *NodeRef) error {
	var e *Error
	if errors.As(err, &e) && e.Node == nil {
		e.Node = n
	}
	return err
}

func (e *Error) Unwrap() error { return e.Err }

func (e *Error) Is(target error) bool {
	k, ok := target.(kindError)
	return ok && e.Kind == Kind(k)
}

// Summary is the one-line form: "ursus: select: unknown column \"user_di\"".
func (e *Error) Summary() string { … }

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(e.Summary())
	for _, d := range e.Details {
		b.WriteString("\n  ")
		b.WriteString(d)
	}
	if e.Node != nil {
		fmt.Fprintf(&b, "\n  at node #%d %s", e.Node.ID, e.Node.Kind)
		if e.Node.Label != "" {
			b.WriteString(" " + e.Node.Label)
		}
		for _, a := range e.Node.Ancestry {
			b.WriteString("\n    " + a)
		}
	}
	if e.Err != nil {
		b.WriteString("\n  cause: " + e.Err.Error())
	}
	return b.String()
}

// Format makes %v and %s equal Error(); %q quotes the summary.
func (e *Error) Format(f fmt.State, verb rune) { … }
```

### Near-match suggestions

```go
// maxDetailNames caps how much of a wide schema we print.
const maxDetailNames = 20

// UnknownName builds the canonical "unknown X" error: the message, the
// available list (truncated), and up to three suggestions.
//
//	uerr.UnknownName("select", "column", "user_di", schema.Names())
func UnknownName(op, noun, name string, available []string) *Error {
	e := New(KindUnknownColumn, op, "unknown %s %q", noun, strconv.Quote(name))
	e.WithDetail("available (%d): %s", len(available), joinTruncated(available, maxDetailNames))
	if sug := Nearest(name, available, 3); len(sug) > 0 {
		e.WithDetail("did you mean: %s?", quoteJoin(sug))
	}
	return e
}

// Nearest returns up to max plausible corrections of name, best first.
//
// Ranking:
//  1. exact match ignoring case          (distance -1)
//  2. Damerau-Levenshtein distance, ties broken by the candidate's position
//
// Candidates further than (len(name)+2)/3 edits away are dropped — the same
// budget cmd/compile uses, chosen because it almost never produces a nonsense
// suggestion. Comparison is over runes, not bytes.
func Nearest(name string, candidates []string, max int) []string { … }
```

### Realistic target message

The doc's own example at `ursus-api.md:754-757` is internally inconsistent — it suggests `"top_column"` for input `"typo_column"` when `top_column` is not in the available list. Corrected, this is what the implementation produces:

```
ursus: select: unknown column "user_di"
  available (5): tenant_id, user_id, event, ts, payload
  did you mean: "user_id"?
  at node #3 SELECT [user_di]
    #2 FILTER [tenant_id == "acme"]
    #1 PARQUET SCAN events/*.parquet
```

and for a wide schema:

```
ursus: filter: unknown column "sttus"
  available (41): ts, service, path, latency_ms, status, user_id, region,
    host, method, bytes_in, bytes_out, referrer, ua, trace_id, span_id,
    parent_id, level, msg, err, retry, … and 21 more
  did you mean: "status"?
  at node #2 FILTER [sttus >= 500]
    #1 PARQUET SCAN s3://logs/year=*/month=*/*.parquet
```

**Plan-node attribution threading.** `internal/plan` assigns each node a 1-based `ID` during a single pre-order walk at the start of optimization, and stores a `*uerr.NodeRef` on the node. Every place a node calls out to schema resolution or expression evaluation wraps the result in `uerr.Attribute(err, n.ref)`. Because `Attribute` is a no-op when `Node` is already set, the innermost node that produced the error keeps the attribution and outer nodes only fill in `Ancestry`. No error plumbing beyond a single deferred wrapper per node method.

---

## 4. `internal/arrowx`

### 4.1 Allocator — and a trap you must not step in

```go
// Package arrowx is ursus' only point of contact with apache/arrow-go.
package arrowx

// Alloc is the allocator ursus uses for every Arrow buffer it creates.
//
// It is an explicitly constructed *memory.GoAllocator, NOT memory.DefaultAllocator.
// That distinction is load-bearing: arrow-go's DefaultAllocator switches to a
// cgo mallocator under `-tags mallocator`, a tag a DOWNSTREAM module can set.
// If that happened while ursus hid Retain/Release from users (see Ownership
// below), every un-Released buffer would become a use-after-free. Pinning our
// own GoAllocator makes the ownership model a property of ursus, not of the
// build tags of whoever imports us.
var Alloc memory.Allocator = memory.NewGoAllocator()
```

`TestAllocIsGoAllocator` asserts `Alloc.(*memory.GoAllocator) != nil`. Verified properties we rely on: `GoAllocator.Allocate` over-allocates by 64 and shifts, so **allocator-produced buffers are 64-byte aligned**; `Free` is a no-op; buffers are plain Go heap; there are zero finalizers anywhere in `arrow`/`array`/`memory`.

**Alignment is a hint, not a precondition.** Buffers that reach us via `memory.NewBufferBytes`, `memory.SliceBuffer`, or an mmap'd IPC file may be only 8-byte aligned (the IPC spec mandates 8; arrow-go's own writer pads to 64). Kernels must not assume 64. This is stated as a hard contract to agent 2.

### 4.2 Ownership model — OQ2 resolved

```go
// # Ownership
//
// ursus does not expose Arrow reference counting. There is no Release method on
// DataFrame, LazyFrame, Column or Batch, and ursus never calls Retain or Release
// on buffers it allocated itself.
//
// This is safe because of three measured facts about arrow-go v18.7.0:
//   - memory.GoAllocator.Free is a no-op;
//   - its buffers are ordinary Go heap allocations;
//   - there are no finalizers in arrow, array or memory.
// Refcounting on Go-allocated buffers is therefore advisory bookkeeping with no
// effect on when memory is reclaimed. The GC already does the job, correctly,
// for buffers reachable from a live frame.
//
// The one case where it is NOT bookkeeping is a RecordBatch that arrived from
// outside — from cdata, from the cgo mallocator, from a caller's own allocator —
// where Release really does free memory. Adopt handles that case; see below.
```

```go
// Adopt converts an externally supplied RecordBatch into an ursus batch without
// copying buffers.
//
// It retains rec and registers a cleanup that releases it once the returned
// Batch becomes unreachable. For a Go-allocator-backed record the Release is a
// no-op; for a C-backed one it is what stops the caller's Release from freeing
// buffers the batch is still reading. Either way the user never sees it.
//
// The batch aliases rec's buffers. Nothing in ursus writes through an adopted
// buffer; kernels that need to write allocate their own output.
func Adopt(rec arrow.RecordBatch) (*batch.Batch, error) {
	sch, err := SchemaFromArrow(rec.Schema())
	if err != nil {
		return nil, err
	}
	cols := make([]batch.Column, rec.NumCols())
	for i := range cols {
		if cols[i], err = columnFromArray(rec.Column(i), sch.Field(i).Type); err != nil {
			return nil, err
		}
	}
	b := batch.New(sch, int(rec.NumRows()), cols)

	rec.Retain()
	// runtime.AddCleanup, not SetFinalizer: it cannot resurrect, it runs in one
	// GC cycle, and it works on objects with cycles. b is not reachable from
	// rec, which is AddCleanup's only real requirement.
	runtime.AddCleanup(b, func(r arrow.RecordBatch) { r.Release() }, rec)
	return b, nil
}

// Export wraps a batch as an arrow.RecordBatch without copying.
//
// The returned record's buffers alias the batch's Go slices, so they are kept
// alive by the GC for as long as the caller holds the record. Calling Release
// on it is permitted and does nothing; NOT calling it is also fine. Buffers may
// be only 8-byte aligned; consumers must not assume 64.
func Export(b *batch.Batch) (arrow.RecordBatch, error) { … }
```

**Contract required of agent 2:** `batch.New` returns a heap-allocated `*Batch` that is not embedded in another object (a requirement of `runtime.AddCleanup`), and `batch.Column` provides a buffer-level constructor

```go
func NewColumnFromBuffers(dt dtype.DataType, length, nullCount int,
	validity []byte, validityBitOffset int, buffers ...[]byte) (Column, error)
```

The `validityBitOffset` parameter is not optional. `array.Data.Offset()` is an *element* offset, so a zero-copy slice of a validity bitmap starts mid-byte. If `bitmap.Bitmap` is `{buf []byte, off, len int}` this costs nothing; if it is `{buf []byte, len int}` then every sliced adoption pays a bitmap copy. **`bitmap.Bitmap` must carry a bit offset.**

### 4.3 Type and schema conversion

```go
// ToArrow returns the canonical Arrow type for dt.
//
// Conversion is asymmetric by design: ToArrow produces exactly one Arrow type
// per ursus type, while FromArrow accepts every Arrow spelling that carries the
// same logical meaning. That is what makes
//
//	FromArrow(ToArrow(dt)) == dt
//
// hold for every dt (TestDTypeRoundTrip), while still letting ursus ingest
// arbitrary Arrow data.
func ToArrow(dt dtype.DataType) (arrow.DataType, error)

// FromArrow maps an Arrow type onto the nearest ursus type.
func FromArrow(dt arrow.DataType) (dtype.DataType, error)

func SchemaToArrow(s dtype.Schema) (*arrow.Schema, error)
func SchemaFromArrow(s *arrow.Schema) (dtype.Schema, error)
```

| ursus | `ToArrow` produces | `FromArrow` also accepts |
|---|---|---|
| `Null` | `arrow.Null` | |
| `Boolean` | `FixedWidthTypes.Boolean` | |
| `Int8…Int64`, `Uint8…Uint64` | `PrimitiveTypes.*` | |
| `Float32/64` | `PrimitiveTypes.Float32/64` | `FLOAT16` → `Float32` (widen, v0.1) |
| `String` | `BinaryTypes.String` | `LARGE_STRING`, `STRING_VIEW` |
| `Binary` | `BinaryTypes.Binary` | `LARGE_BINARY`, `BINARY_VIEW`, `FIXED_SIZE_BINARY` |
| `Date` | `FixedWidthTypes.Date32` | `DATE64` |
| `Time(s\|ms)` | `Time32Type{unit}` | |
| `Time(us\|ns)` | `Time64Type{unit}` | |
| `Datetime(u,tz)` | `&TimestampType{u, tz}` | |
| `Duration(u)` | `&DurationType{u}` | |
| `Decimal(p,s)` | `&Decimal128Type{p,s}` | `DECIMAL32/64/256` (widen; 256 errors if p>38) |
| `List(inner)` | `arrow.ListOf` | `LARGE_LIST`, `LIST_VIEW`, `LARGE_LIST_VIEW` |
| `Array(inner,n)` | `arrow.FixedSizeListOf` | |
| `Struct(fields)` | `arrow.StructOf` | |
| `Enum(cats)` | `&DictionaryType{Uint32, String, Ordered:true}` | any integer index type |
| — | — | `MAP`, `UNION`, `RUN_END_ENCODED`, `EXTENSION` → `KindUnsupported` |

Rationale for the omissions: `LargeString`/`StringView` are *storage layouts*, not logical types. Following Polars, ursus has one `String` type and chooses the layout internally (OQ5: start with offsets; the `Column` interface hides it, so views can be added without touching call sites). Giving them TypeIDs would leak a storage decision into the plan format forever.

---

## 5. The scan source contract

```go
// Package source defines the contract between the query engine and a scannable
// dataset. It is ursus' equivalent of DataFusion's TableProvider.
//
// The package deliberately does NOT depend on internal/expr. A source sees a
// narrow, closed predicate language (Term), not the full expression IR. That
// keeps "what can be pushed into a scan" an explicit, testable contract instead
// of an open-ended interpretation problem for every format.
package source

// Source is a scannable dataset. Implementations must be safe for concurrent
// use.
type Source interface {
	// Kind is a short stable identifier: "parquet", "csv", "memory".
	Kind() string

	// Label is the human-readable subject of the scan — the glob, the file
	// list — as it appears in EXPLAIN output and error attribution.
	Label() string

	// Schema resolves the full, unprojected schema.
	//
	// It may perform I/O: a Parquet footer read, CSV type inference, an
	// object-store HEAD. That is why it takes a context, and it is why
	// LazyFrame exposes CollectSchema(ctx) rather than the context-free
	// Schema() the design doc proposed.
	//
	// Implementations must cache a successful result: the planner calls this
	// several times per query. They must NOT cache a failure — a scan whose
	// first schema resolution was cancelled has to be retryable.
	Schema(ctx context.Context) (dtype.Schema, error)

	// Open starts a scan. Batches from the returned Scan have exactly the
	// schema req.OutputSchema(full) reports.
	Open(ctx context.Context, req ScanRequest) (Scan, error)

	// Fingerprint is a stable identity for this source and its options. Two
	// sources with equal fingerprints are interchangeable; common-subplan
	// elimination relies on it.
	Fingerprint() string
}

// PredicatePushdown is implemented by sources that can use a predicate to skip
// data. Sources that do not implement it always receive a nil Predicate.
type PredicatePushdown interface {
	Source
	// Supports reports how completely the source can apply t. The engine
	// re-applies every term not reported Exact, so returning Inexact is
	// always safe.
	Supports(t Term) Pushdown
}

type Pushdown uint8

const (
	PushdownNone    Pushdown = iota // engine applies it; do not pass it down
	PushdownInexact                 // source may prune; engine still applies it
	PushdownExact                   // source applies it exactly; engine drops it
)

// ScanRequest is what the optimizer asks a Source to do.
type ScanRequest struct {
	// Projection lists indices into the source's FULL schema, in output order.
	//   nil            → every column
	//   empty non-nil  → no columns; a row-count-only scan. Legal, and the
	//                    fast path for LazyFrame.Count.
	// Indices must be in range and must not repeat.
	Projection []int

	// Predicate is an implicit conjunction over the full schema. It is a HINT:
	// unless the source reported PushdownExact for a term, the engine applies
	// the term again above the scan. A source may ignore it entirely.
	Predicate []Term

	// Limit bounds the rows the scan must produce; 0 means unbounded. Also a hint.
	Limit int64

	// BatchSize is the preferred rows per batch; 0 means "the source decides".
	// Sources should treat it as a maximum.
	BatchSize int
}

func (r ScanRequest) OutputSchema(full dtype.Schema) (dtype.Schema, error) {
	if r.Projection == nil {
		return full, nil
	}
	return full.Project(r.Projection...)
}

// Scan is one running scan. It is NOT safe for concurrent use; the engine
// creates one Scan per parallel task.
type Scan interface {
	// Next returns the next batch, or (nil, io.EOF) at the end. It must honour
	// ctx: at minimum it returns ctx.Err() promptly between batches.
	Next(ctx context.Context) (*batch.Batch, error)

	// Stats reports what the scan actually did. Readable at any time; final
	// once Next has returned io.EOF. This is what makes projection pushdown
	// testable without parsing EXPLAIN output.
	Stats() Stats

	// Close releases resources. Idempotent, and legal before io.EOF.
	Close() error
}

type Stats struct {
	BatchesProduced int64
	RowsProduced    int64
	RowsSkipped     int64 // pruned by the source
	ColumnsRead     int
	ColumnsTotal    int
	UnitsRead       int64 // row groups / files / chunks actually read
	UnitsTotal      int64
	BytesRead       int64
}

type CompareOp uint8

const (
	OpEq CompareOp = iota
	OpNe
	OpLt
	OpLe
	OpGt
	OpGe
	OpIsNull
	OpIsNotNull
	OpIn
)

// Term is one conjunct of a pushed-down predicate.
//
// Values holds CANONICAL Go representations, chosen so a source can switch on
// Type alone:
//
//	Boolean                        bool
//	all signed integers            int64
//	all unsigned integers          uint64
//	Float32, Float64               float64
//	String                         string
//	Binary                         []byte
//	Date, Time, Datetime           time.Time
//	Duration                       time.Duration
//
// Op == OpIn means Values has any length; OpIsNull/OpIsNotNull means it is
// empty; every other op means exactly one value.
type Term struct {
	Column int
	Op     CompareOp
	Type   dtype.DataType
	Values []any
}

func (t Term) Validate(full dtype.Schema) error // checks index, arity, Go types
```

The `expr.Node` → `[]source.Term` translation lives in `internal/plan`'s pushdown pass, which is the only package that knows both languages. Step 1 passes `Predicate: nil` and the engine's `Filter` operator does all the work; step 2 is purely additive.

### 5.1 Parquet implementation sketch

```go
// Package parquet is ursus' Parquet scan source.
//
// # Dependency isolation
//
// pqarrow is the only reason ursus depends on gRPC, protobuf, x/net and x/text:
// parquet/pqarrow/schema.go:1039 calls flight.DeserializeSchema for exactly one
// line, and file_writer.go:168 calls flight.SerializeSchema. Importing
// arrow+array+memory+bitutil costs 6 external modules; adding pqarrow makes it
// ~18. Every pqarrow reference in ursus lives in reader_pqarrow.go and
// schema_pqarrow.go, behind the recordSource interface below, so replacing it
// with a parquet/file-based reader (which needs no flight, ~12 modules) is a
// two-file change. See TECHDEBT.md item 1.
package parquet

type Source struct {
	paths []string
	opts  scanopt.Parquet

	mu       sync.Mutex
	resolved bool
	schema   dtype.Schema
	files    []*fileMeta // footer metadata, one per path
}

func New(paths []string, opts scanopt.Parquet) *Source

func (s *Source) Kind() string  { return "parquet" }
func (s *Source) Label() string { return labelPaths(s.paths) }

func (s *Source) Fingerprint() string {
	h := sha256.New()
	fmt.Fprintf(h, "parquet\x00%v\x00%v", s.paths, s.opts.Fingerprint())
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func (s *Source) Schema(ctx context.Context) (dtype.Schema, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resolved {
		return s.schema, nil
	}
	fm, err := s.openFooter(ctx, s.paths[0])
	if err != nil {
		return dtype.Schema{}, uerr.Wrap(err, uerr.KindIO, "scan/parquet",
			"reading schema from %s", s.paths[0])
	}
	asch, err := fm.reader.Schema()          // *arrow.Schema
	if err != nil { … }
	sch, err := arrowx.SchemaFromArrow(asch)
	if err != nil { … }
	if s.opts.HasSchema {
		sch = s.opts.Schema                   // explicit override
	} else if len(s.opts.SchemaOverrides) > 0 {
		if sch, err = applyOverrides(sch, s.opts.SchemaOverrides); err != nil { … }
	}
	s.schema, s.resolved = sch, true          // failures are NOT cached
	return s.schema, nil
}

func (s *Source) Open(ctx context.Context, req source.ScanRequest) (source.Scan, error) {
	full, err := s.Schema(ctx)
	if err != nil {
		return nil, err
	}
	out, err := req.OutputSchema(full)
	if err != nil {
		return nil, err
	}

	// A count-only scan never touches a column chunk. GetRecordReader would
	// fail with "no leaf column readers matched col indices", so short-circuit.
	if req.Projection != nil && len(req.Projection) == 0 {
		return newCountScan(s, out, req)
	}

	props := pqarrow.ArrowReadProperties{
		Parallel:  s.opts.ParallelStrategy != scanopt.ParallelNone,
		// CRITICAL: pqarrow reads the ENTIRE FILE into a single record batch
		// when BatchSize <= 0 (file_reader.go:522). Never leave it unset.
		BatchSize: int64(effectiveBatchSize(req.BatchSize, s.opts)),
	}

	fr, err := pqarrow.NewFileReader(rdr, props, arrowx.Alloc)
	if err != nil { … }

	leaves, err := leafIndices(fr.Manifest, req.Projection)
	if err != nil { … }

	rowGroups := allRowGroups(rdr)            // step 2: prune via statistics
	rr, err := fr.GetRecordReader(ctx, leaves, rowGroups)
	if err != nil { … }

	// GetFieldReaders derives the output field order from the caller's index
	// order (SchemaManifest.GetFieldIndices preserves it), so in practice the
	// record's columns already match `out`. Build the permutation from the
	// reader's actual schema anyway: it is O(cols) once per scan, it makes the
	// invariant explicit, and it means we do not silently break if pqarrow's
	// ordering ever changes.
	perm, err := permutation(rr.Schema(), out)
	if err != nil { … }

	return &scan{rr: rr, out: out, perm: perm, stats: source.Stats{
		ColumnsRead:  out.Len(),
		ColumnsTotal: full.Len(),
		UnitsTotal:   int64(rdr.NumRowGroups()),
		UnitsRead:    int64(len(rowGroups)),
	}}, nil
}
```

```go
// leafIndices maps top-level field indices into the parquet LEAF column indices
// GetRecordReader expects. For a flat schema they coincide, but one struct
// column occupies several leaves, so this must go through the manifest.
//
// The order is preserved deliberately: SchemaManifest.GetFieldIndices walks the
// caller's slice in order and dedupes, so passing leaves in projection order is
// what makes the output columns come back in projection order. Do not sort.
func leafIndices(m *pqarrow.SchemaManifest, projection []int) ([]int, error) {
	if projection == nil {
		return nil, nil // nil means "all", which pqarrow expands itself
	}
	out := make([]int, 0, len(projection))
	for _, i := range projection {
		if i < 0 || i >= len(m.Fields) {
			return nil, uerr.New(uerr.KindInternal, "scan/parquet",
				"projection index %d out of range [0,%d)", i, len(m.Fields))
		}
		out = appendLeaves(out, &m.Fields[i])
	}
	return out, nil
}

func appendLeaves(dst []int, f *pqarrow.SchemaField) []int {
	if f.IsLeaf() {
		return append(dst, f.ColIndex)
	}
	for i := range f.Children {
		dst = appendLeaves(dst, &f.Children[i])
	}
	return dst
}
```

```go
type scan struct {
	rr    pqarrow.RecordReader
	out   dtype.Schema
	perm  []int // nil when identity
	stats source.Stats
	done  bool
}

func (s *scan) Next(ctx context.Context) (*batch.Batch, error) {
	// pqarrow.RecordReader.Next takes no context; the ctx handed to
	// GetRecordReader covers the field readers only. Cancellation granularity
	// is therefore ONE BATCH for local files. For object-store reads it is much
	// finer, because objstore.ReaderAt binds the scan's context into every
	// ReadAt. This is a documented limitation, not an oversight.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.done || !s.rr.Next() {
		if err := s.rr.Err(); err != nil {
			return nil, uerr.Wrap(err, uerr.KindIO, "scan/parquet", "reading record batch")
		}
		s.done = true
		return nil, io.EOF
	}
	rec := s.rr.RecordBatch()
	b, err := arrowx.Adopt(rec)
	if err != nil {
		return nil, err
	}
	if s.perm != nil {
		b = b.Permute(s.perm)
	}
	s.stats.BatchesProduced++
	s.stats.RowsProduced += int64(b.Rows())
	return b, nil
}

func (s *scan) Stats() source.Stats { return s.stats }
func (s *scan) Close() error        { s.rr.Release(); return nil }
```

Step-2 row-group pruning slots in as one function, using API already verified present: `rdr.MetaData().RowGroup(i)` → `.ColumnChunk(leaf)` → `.Statistics()`, plus `RowGroupMetaData.ColumnIndexLocation`/`OffsetIndexLocation` for page-level pruning and the bloom-filter reader for equality terms.

### 5.2 Object storage

```go
package objstore

type ObjectMeta struct {
	Path         string
	Size         int64
	LastModified time.Time
	ETag         string
}

type Store interface {
	Get(ctx context.Context, path string) (io.ReadCloser, error)
	GetRange(ctx context.Context, path string, off, n int64) (io.ReadCloser, error)
	Put(ctx context.Context, path string, r io.Reader) error
	List(ctx context.Context, prefix string) iter.Seq2[ObjectMeta, error]
	Stat(ctx context.Context, path string) (ObjectMeta, error)
}

func Local(root string) Store // v0.1. S3/GCS/Azure/HTTP: backlog.

// ReaderAt adapts a Store object into something that satisfies
// parquet.ReaderAtSeeker (io.ReaderAt + io.ReadSeeker), so a Parquet footer and
// its row groups can be read with ranged requests instead of a full download.
//
// It stores ctx. That is deliberate and is the one place in ursus where a
// context lives in a struct: the Arrow reader calls ReadAt without one, and
// binding the scan's context here is what gives object-store scans sub-batch
// cancellation. The returned reader is single-scan-scoped.
//
//nolint:containedctx // documented above
func ReaderAt(ctx context.Context, s Store, path string, size int64) (interface {
	io.ReaderAt
	io.ReadSeeker
}, error)
```

`objstore` imports no Arrow package — `ReaderAt` returns a structural match for `parquet.ReaderAtSeeker`, which keeps `objstore` at L0.

---

## 6. The functional-options scheme (D8 fixed)

**The rule, stated once:**

> For every option family, the *generic* option type is an **interface that embeds** the specific option interfaces. Each specific interface has exactly one unexported `applyX` method. Go's interface assignability then flows generic → specific automatically, with no variance and no runtime checks. Never declare an option type as `type Option func(*T)`.

```go
// options_scan.go — package ursus

// ScanOption is a scan option valid for every source format.
//
// It is an interface embedding each per-format option interface, so a
// ScanOption may be passed anywhere a ParquetScanOption, CSVScanOption, … is
// expected: its method set is a superset of each. That is why these are
// interfaces rather than funcs — a func type has no method set to be a superset
// of, which is exactly why the shape proposed in the design doc could not
// compile.
//
// Options are a closed set: the apply methods are unexported, so only package
// ursus can define one. That is deliberate.
type ScanOption interface {
	ParquetScanOption
	CSVScanOption
	IPCScanOption
	NDJSONScanOption
}

type ParquetScanOption interface{ applyParquetScan(*scanopt.Parquet) }
type CSVScanOption     interface{ applyCSVScan(*scanopt.CSV) }
type IPCScanOption     interface{ applyIPCScan(*scanopt.IPC) }
type NDJSONScanOption  interface{ applyNDJSONScan(*scanopt.NDJSON) }

// commonScan lifts a mutation of the shared scan settings into every per-format
// option interface. Adding a format costs one method here and one embedded
// interface in ScanOption — not one method per option.
type commonScan func(*scanopt.Common)

func (f commonScan) applyParquetScan(o *scanopt.Parquet) { f(&o.Common) }
func (f commonScan) applyCSVScan(o *scanopt.CSV)         { f(&o.Common) }
func (f commonScan) applyIPCScan(o *scanopt.IPC)         { f(&o.Common) }
func (f commonScan) applyNDJSONScan(o *scanopt.NDJSON)   { f(&o.Common) }

// --- generic scan options -------------------------------------------------

func WithSchema(s Schema) ScanOption {
	return commonScan(func(c *scanopt.Common) { c.Schema, c.HasSchema = s, true })
}
func WithSchemaOverrides(m map[string]DataType) ScanOption { … }
func WithNRows(n int64) ScanOption                          { … }
func WithRowIndex(name string, offset uint32) ScanOption    { … }
func WithHivePartitioning(b bool) ScanOption                { … }
func WithHiveSchema(s Schema) ScanOption                    { … }
func WithIncludeFilePaths(col string) ScanOption            { … }
func WithGlob(b bool) ScanOption                            { … }
func WithRechunk(b bool) ScanOption                         { … }
func WithLowMemory(b bool) ScanOption                       { … }
func WithCache(b bool) ScanOption                           { … }
func WithMissingColumns(p MissingPolicy) ScanOption         { … }
func WithExtraColumns(p ExtraPolicy) ScanOption             { … }
func WithStorage(s objstore.Store) ScanOption               { … }
func WithRetries(n int) ScanOption                          { … }
func WithScanBatchSize(rows int) ScanOption                 { … }

// --- Parquet-only ---------------------------------------------------------

type parquetScan func(*scanopt.Parquet)

func (f parquetScan) applyParquetScan(o *scanopt.Parquet) { f(o) }

func WithUseStatistics(b bool) ParquetScanOption {
	return parquetScan(func(o *scanopt.Parquet) { o.UseStatistics = b })
}
func WithParallelStrategy(p ParallelStrategy) ParquetScanOption { … }

// --- CSV-only -------------------------------------------------------------

type csvScan func(*scanopt.CSV)

func (f csvScan) applyCSVScan(o *scanopt.CSV) { f(o) }

func WithSeparator(r rune) CSVScanOption      { … }
func WithHasHeader(b bool) CSVScanOption      { … }
func WithSkipRows(n int) CSVScanOption        { … }
func WithTryParseDates(b bool) CSVScanOption  { … }
```

### Worked examples

```go
// §16's example, now typechecking — generic and format-specific options mixed
// in one variadic call:
lf := ursus.ScanParquetPaths(
	[]string{"s3://logs/year=*/month=*/*.parquet"},
	ursus.WithHivePartitioning(true),          // ScanOption        → OK
	ursus.WithStorage(objstore.S3(s3cfg)),     // ScanOption        → OK
	ursus.WithUseStatistics(true),             // ParquetScanOption → OK
)

// Mixing families is rejected at compile time, with a comprehensible message:
ursus.ScanParquet("f.parquet", ursus.WithSeparator(';'))
// cannot use ursus.WithSeparator(';') (value of interface type ursus.CSVScanOption)
//   as ursus.ParquetScanOption value in argument to ursus.ScanParquet:
//   ursus.CSVScanOption does not implement ursus.ParquetScanOption
//   (missing method applyParquetScan)

// A heterogeneous slice works, because both are ParquetScanOption:
opts := []ursus.ParquetScanOption{
	ursus.WithNRows(1_000_000),
	ursus.WithUseStatistics(true),
}
lf := ursus.ScanParquet("f.parquet", opts...)

// And the reverse is correctly rejected: a ParquetScanOption is not a ScanOption.
var o ursus.ScanOption = ursus.WithUseStatistics(true) // compile error — good
```

### The same shape for execution

```go
// options_exec.go — package ursus

// ExecOption applies to every terminal operation: Collect, CollectBatches,
// Count, Profile and all Sinks.
type ExecOption interface {
	CollectOption
	ParquetSinkOption
	CSVSinkOption
	IPCSinkOption
	NDJSONSinkOption
}

type CollectOption     interface{ applyCollect(*execopt.Collect) }
type ParquetSinkOption interface{ applyParquetSink(*execopt.ParquetSink) }
type CSVSinkOption     interface{ applyCSVSink(*execopt.CSVSink) }
type IPCSinkOption     interface{ applyIPCSink(*execopt.IPCSink) }
type NDJSONSinkOption  interface{ applyNDJSONSink(*execopt.NDJSONSink) }

type commonExec func(*execopt.Common)

func (f commonExec) applyCollect(o *execopt.Collect)         { f(&o.Common) }
func (f commonExec) applyParquetSink(o *execopt.ParquetSink) { f(&o.Common) }
func (f commonExec) applyCSVSink(o *execopt.CSVSink)         { f(&o.Common) }
func (f commonExec) applyIPCSink(o *execopt.IPCSink)         { f(&o.Common) }
func (f commonExec) applyNDJSONSink(o *execopt.NDJSONSink)   { f(&o.Common) }

func WithEngine(e EngineKind) ExecOption      { … }
func WithStrictStreaming() ExecOption         { … }
func WithThreads(n int) ExecOption            { … }
func WithMemoryLimit(bytes int64) ExecOption  { … }
func WithSpillDir(dir string) ExecOption      { … }
func WithBatchSize(rows int) ExecOption       { … }
func WithOptFlags(f OptFlags) ExecOption      { … }

// Collect-only
type collectOnly func(*execopt.Collect)
func (f collectOnly) applyCollect(o *execopt.Collect) { f(o) }
func WithRechunkResult(b bool) CollectOption { … }

// Sink-only
type parquetSink func(*execopt.ParquetSink)
func (f parquetSink) applyParquetSink(o *execopt.ParquetSink) { f(o) }
func WithCompression(c Compression) ParquetSinkOption { … }
func WithRowGroupSize(rows int64) ParquetSinkOption   { … }
```

```go
df, err := lf.Collect(ctx,
	ursus.WithEngine(ursus.EngineStreaming), // ExecOption    → OK
	ursus.WithRechunkResult(true),           // CollectOption → OK
)

err := lf.SinkParquet(ctx, "out.parquet",
	ursus.WithThreads(8),                    // ExecOption          → OK
	ursus.WithCompression(ursus.Zstd),       // ParquetSinkOption   → OK
)
// ursus.WithRechunkResult(true) here → compile error. Correct.
```

`ExplainOption` and `ursustest.Option` are single-family versions of the same shape.

**Dropped from the doc:** `ScanParquetOpts`/`ScanCSVOpts` no longer have a reason to exist. The API is `ScanParquet(path string, opts ...ParquetScanOption)` and `ScanParquetPaths(paths []string, opts ...ParquetScanOption)`.

---

## 7. Root façade

```go
// dtype_alias.go — package ursus

// The type system. ursus.X and dtype.X are the same type; ursus/dtype is the
// definition site and carries the method documentation.
type (
	DataType   = dtype.DataType
	TypeID     = dtype.TypeID
	TimeUnit   = dtype.TimeUnit
	Field      = dtype.Field
	Schema     = dtype.Schema
	Categories = dtype.Categories
)

const (
	TypeUnknown = dtype.TypeUnknown
	TypeNull    = dtype.TypeNull
	// … one line per TypeID …

	UnitSecond = dtype.UnitSecond
	UnitMilli  = dtype.UnitMilli
	UnitMicro  = dtype.UnitMicro
	UnitNano   = dtype.UnitNano
)

var (
	Null, Boolean                                        = dtype.Null, dtype.Boolean
	Int8, Int16, Int32, Int64                            = dtype.Int8, dtype.Int16, dtype.Int32, dtype.Int64
	Uint8, Uint16, Uint32, Uint64                        = dtype.Uint8, dtype.Uint16, dtype.Uint32, dtype.Uint64
	Float32, Float64                                     = dtype.Float32, dtype.Float64
	String, Binary, Date                                 = dtype.String, dtype.Binary, dtype.Date
)

// Forwarders, not aliases, so godoc shows a real signature in package ursus.

// Datetime returns a timestamp type. See [dtype.Datetime].
func Datetime(u TimeUnit, tz string) DataType { return dtype.Datetime(u, tz) }
func Duration(u TimeUnit) DataType            { return dtype.Duration(u) }
func Time(u TimeUnit) DataType                { return dtype.Time(u) }
func Decimal(precision uint8, scale int8) DataType { return dtype.Decimal(precision, scale) }
func List(inner DataType) DataType            { return dtype.List(inner) }
func Array(inner DataType, width int) DataType { return dtype.Array(inner, width) }
func Struct(fields ...Field) DataType         { return dtype.Struct(fields...) }
func Enum(categories ...string) DataType      { return dtype.Enum(categories...) }

func NewField(name string, dt DataType) Field       { return dtype.NewField(name, dt) }
func NewSchema(fields ...Field) (Schema, error)     { return dtype.NewSchema(fields...) }
func MustSchema(fields ...Field) Schema             { return dtype.MustSchema(fields...) }
```

**Naming defect fixed:** the doc declares both `type Field struct` and `func Field(name string) Expr` (`ursus-api.md:133` and `:219`). That does not compile. The type keeps the name; the expression form is only reachable as `Col("s").Struct().Field("x")`, which §4.5 already provides. The top-level `Field(name) Expr` is deleted. Similarly `NullT` becomes `Null`, and `Str`/`Bin` become `String`/`Binary` to match Arrow and Polars.

```go
// errors.go — package ursus

// Error is the structured error returned by every ursus operation.
//
//	var e *ursus.Error
//	if errors.As(err, &e) { … }
//
// Fields:
//   Kind     machine-readable category; compare against the ErrKind* values
//            or use errors.Is with ErrUnknownColumn and friends
//   Op       the failing operation, e.g. "select", "scan/parquet"
//   Message  the one-line summary
//   Details  additional lines: available columns, suggestions, type pairs
//   Node     the plan node this is attributed to, if any
//   Err      the wrapped cause
//
// Error's Error method returns a multi-line diagnostic. Use Summary for one line.
type Error = uerr.Error

type ErrorKind = uerr.Kind

const (
	KindUnknownColumn   = uerr.KindUnknownColumn
	KindDuplicateColumn = uerr.KindDuplicateColumn
	// …
)

var (
	ErrUnknownColumn   = uerr.ErrUnknownColumn
	ErrDuplicateColumn = uerr.ErrDuplicateColumn
	ErrTypeMismatch    = uerr.ErrTypeMismatch
	ErrSchemaMismatch  = uerr.ErrSchemaMismatch
	ErrInvalidArgument = uerr.ErrInvalidArgument
	ErrUnsupported     = uerr.ErrUnsupported
	ErrIO              = uerr.ErrIO
	ErrOutOfMemory     = uerr.ErrOutOfMemory
	ErrInternal        = uerr.ErrInternal
)
```

```go
// scan.go — package ursus

// ScanParquet begins a lazy scan of one Parquet file or glob.
//
// No I/O happens here. The footer is read the first time the plan needs a
// schema — at CollectSchema, Explain or Collect — all of which take a context.
func ScanParquet(path string, opts ...ParquetScanOption) *LazyFrame {
	return ScanParquetPaths([]string{path}, opts...)
}

func ScanParquetPaths(paths []string, opts ...ParquetScanOption) *LazyFrame {
	cfg := scanopt.DefaultParquet()
	for _, o := range opts {
		o.applyParquetScan(&cfg)
	}
	if len(paths) == 0 {
		return errFrame(uerr.New(uerr.KindInvalidArgument, "scan/parquet", "no paths given"))
	}
	return newLazyFrame(plan.NewScan(pqsource.New(paths, cfg)))
}

// ScanArrow scans an arrow-go RecordReader with zero copies.
func ScanArrow(rdr array.RecordReader) *LazyFrame

// ScanFunc scans a caller-supplied source.
func ScanFunc(schema Schema, fn ScanFunc) *LazyFrame
```

### D5: the context fixes

```go
// CollectSchema resolves the frame's output schema without producing any rows.
//
// It takes a context because resolving a scan's schema is I/O: a Parquet footer
// read, a CSV inference pass, an object-store request. The design doc's
// context-free Schema() could not be implemented at all, since objstore.Store.Get
// requires one.
func (lf *LazyFrame) CollectSchema(ctx context.Context) (Schema, error)

// Explain renders the plan. Optimized plans need resolved schemas, hence ctx.
func (lf *LazyFrame) Explain(ctx context.Context, opts ...ExplainOption) (string, error)

// Err returns the sticky build error, if any. It performs no I/O.
func (lf *LazyFrame) Err() error
```

And the eager façade (OQ7): **nothing is generated in step 1.** `DataFrame` gets only operations that touch already-materialised memory (`Lazy`, `Height`, `Width`, `Shape`, `Columns`, `Schema`, `String`, `Glimpse`, `Batch`, `ToArrow`, the generic accessors). When eager mirrors are added, they take `ctx` as the first parameter:

```go
func (df *DataFrame) Select(ctx context.Context, exprs ...Expr) (*DataFrame, error)
```

Generating them over `context.Background()` would throw away the cancellation that is ursus' headline differentiator over Polars, in the API surface most likely to be used from an HTTP handler. Codegen is revisited when the `LazyFrame` method set stops moving; the generator will emit the `ctx` parameter, and a `make check-facade` target will fail if a `LazyFrame` context method has no eager counterpart.

---

## 8. `ursustest`

```go
// Package ursustest provides assertions for testing code that uses ursus.
//
// Every assertion takes a testing.TB, reports with t.Helper() so failures point
// at the caller, and calls t.Errorf rather than t.Fatalf unless the frames are
// structurally incomparable.
package ursustest

type Option interface{ applyAssert(*config) }

type config struct {
	checkRowOrder    bool
	checkColumnOrder bool
	checkDTypes      bool
	checkNullability bool
	absTol, relTol   float64
	maxDiffRows      int
}

func defaults() config {
	return config{checkRowOrder: true, checkColumnOrder: true, checkDTypes: true,
		checkNullability: true, maxDiffRows: 10}
}

type optFunc func(*config)

func (f optFunc) applyAssert(c *config) { f(c) }

func CheckRowOrder(b bool) Option    { return optFunc(func(c *config) { c.checkRowOrder = b }) }
func CheckColumnOrder(b bool) Option { … }
func CheckDTypes(b bool) Option      { … }
func IgnoreNullability() Option      { … }
func WithTolerance(abs, rel float64) Option { … }
func CheckExact() Option             { … }
func MaxDiffRows(n int) Option       { … }

// AssertFrameEqual fails the test if got and want differ.
//
// The report names the first differing cell and prints an aligned excerpt of
// both frames around it, rather than dumping two whole tables:
//
//	frames differ: 2 of 4 columns, 3 of 1000 rows
//	  column "price" (f64): row 17: got 10.25, want 10.5
//	  column "qty"   (i64): row 17: got 3,     want <null>
//
//	  got                        want
//	  ┌─────┬────────┬─────┐     ┌─────┬────────┬─────┐
//	  │ sym │  price │ qty │     │ sym │  price │ qty │
//	  ├─────┼────────┼─────┤     ├─────┼────────┼─────┤
//	  │ AAP │  10.25 │   3 │ ≠   │ AAP │  10.50 │   ⌀ │
//	  └─────┴────────┴─────┘     └─────┴────────┴─────┘
func AssertFrameEqual(tb testing.TB, got, want *ursus.DataFrame, opts ...Option) {
	tb.Helper()
	cfg := apply(defaults(), opts)
	if got == nil || want == nil { … }
	if err := compareFrames(got.Batch(), want.Batch(), cfg); err != nil {
		tb.Error(err)
	}
}

// AssertSchemaEqual fails if the schemas differ, reporting field-by-field.
//
//	schemas differ:
//	  field 1 "ts": got datetime[ms], want datetime[us,UTC]
//	  field 3: got "payload", want "payload_json"
//	  got has 5 fields, want has 6 (missing: "region")
func AssertSchemaEqual(tb testing.TB, got, want ursus.Schema, opts ...Option)

// AssertSeriesEqual is the typed form. It is a generic FUNCTION, not a method,
// so it is unaffected by the "generic methods cannot implement interfaces" rule.
func AssertSeriesEqual[T any](tb testing.TB, got, want *ursus.Series[T], opts ...Option)

// AssertPlan compares the optimized plan against a golden file, so optimizer
// regressions fail loudly.
//
// Set URSUS_UPDATE_GOLDEN=1 to rewrite the golden. An environment variable
// rather than a flag: a package-level flag in a helper library would be
// registered in every importing test binary and collide.
//
//	ursustest.AssertPlan(t, lf, "testdata/plans/filter_select.txt")
func AssertPlan(tb testing.TB, lf *ursus.LazyFrame, goldenFile string, opts ...Option) {
	tb.Helper()
	got, err := lf.Explain(tb.Context(), ursus.Optimized(true))
	…
}

// Fixture helpers, so step-1 tests do not each reinvent them.
func Frame(tb testing.TB, schema ursus.Schema, rows ...[]any) *ursus.DataFrame
func ParquetFile(tb testing.TB, df *ursus.DataFrame, opts ...FixtureOption) string
```

`AssertFrameEqual` reaches the data through `(*ursus.DataFrame).Batch() *batch.Batch`. `internal/batch` is module-internal, so an external caller cannot name the return type — but `ursustest` is in-module and can. This is the standard `x/tools` pattern and it avoids exporting a comparison-only API from the root.

---

## 9. Build and test infrastructure

### 9.1 `go.mod`

```
module ursus

go 1.27

require github.com/apache/arrow-go/v18 v18.7.0

require (
	github.com/andybalholm/brotli v1.2.2 // indirect
	github.com/apache/thrift v0.24.0 // indirect
	github.com/goccy/go-json v0.10.6 // indirect
	github.com/golang/snappy v1.0.0 // indirect
	github.com/google/flatbuffers v25.12.19+incompatible // indirect
	github.com/klauspost/compress v1.19.0 // indirect
	github.com/klauspost/cpuid/v2 v2.4.0 // indirect
	github.com/pierrec/lz4/v4 v4.1.27 // indirect
	github.com/zeebo/xxh3 v1.1.0 // indirect
	golang.org/x/exp v0.0.0-20260112195511-716be5621a96 // indirect
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260414002931-afd174a4e478 // indirect
	google.golang.org/grpc v1.82.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)
```

**One direct dependency. Zero test dependencies** — no testify, no go-cmp; `ursustest` *is* our assertion library and produces better frame diffs than a generic differ would. `go 1.27` is mandatory: with `go 1.26` every generic method fails with `syntax error: method must have no type parameters`.

The gRPC/protobuf/x-net/x-text block exists solely because `pqarrow` imports `arrow/flight` for two lines. Do NOT import `arrow/compute` (experimental, no aggregates/group-by/joins, and it adds thrift on top).

### 9.2 `Makefile`

```make
# Every build, test, vet and bench invocation in this repo sets GOEXPERIMENT=simd.
# Package simd does not compile without it ("build constraints exclude all Go
# files"), and the toolchain is selected per-module, so GOTOOLCHAIN=auto plus
# `go 1.27` in go.mod is what actually runs go1.27.0 here.

export GOTOOLCHAIN := auto

GO       ?= go
GOAMD64  ?= v3                 # free perf for non-SIMD code; does not affect
                               # package simd, which dispatches at runtime
PKGS     ?= ./...
TESTFLAGS?=

SIMD   := GOEXPERIMENT=simd GOAMD64=$(GOAMD64)
NOSIMD := GOEXPERIMENT=      GOAMD64=$(GOAMD64)

.PHONY: all
all: vet test

.PHONY: env
env: ## print the toolchain actually in use (the goenv shim lies)
	@$(SIMD) $(GO) version
	@$(SIMD) $(GO) env GOROOT GOEXPERIMENT GOAMD64 GOTOOLCHAIN

.PHONY: build
build:
	$(SIMD) $(GO) build $(PKGS)

.PHONY: build-nosimd
build-nosimd: ## the library MUST build without the experiment
	$(NOSIMD) $(GO) build $(PKGS)

.PHONY: vet
vet:
	$(SIMD) $(GO) vet $(PKGS)
	$(NOSIMD) $(GO) vet $(PKGS)

.PHONY: test
test: test-simd test-nosimd

.PHONY: test-simd
test-simd:
	$(SIMD) $(GO) test $(TESTFLAGS) $(PKGS)

.PHONY: test-nosimd
test-nosimd:
	$(NOSIMD) $(GO) test $(TESTFLAGS) $(PKGS)

.PHONY: test-emulated
test-emulated: ## exercise the software-emulated simd path
	$(SIMD) GODEBUG=cpu.avx512f=off,cpu.avx2=off $(GO) test $(PKGS)

.PHONY: test-race
test-race:
	$(SIMD) $(GO) test -race $(PKGS)

.PHONY: diff
diff: ## SIMD-vs-scalar differential tests, many seeds
	$(SIMD) $(GO) test -run 'TestDifferential' -count=50 ./internal/kernel/...

.PHONY: bench
bench:
	$(SIMD) $(GO) test -run '^$$' -bench . -benchmem -count=6 $(PKGS)

.PHONY: bench-arrow
bench-arrow: ## the two arrow-go risk benchmarks
	$(SIMD) $(GO) test -run '^$$' -bench 'BenchmarkKernelLoop' -benchmem -count=10 ./internal/arrowx/

.PHONY: soak
soak: ## RSS soak: prove un-Released arrow buffers are collected
	$(SIMD) $(GO) test -run TestSoakNoRelease -tags soak -timeout 30m -v ./internal/arrowx/

.PHONY: cover
cover:
	$(SIMD) $(GO) test -coverprofile=cover.out -covermode=atomic $(PKGS)
	$(SIMD) $(GO) tool cover -func=cover.out | tail -1

.PHONY: golden
golden: ## rewrite plan golden files
	$(SIMD) URSUS_UPDATE_GOLDEN=1 $(GO) test -run 'TestPlan' $(PKGS)

.PHONY: fixtures
fixtures:
	$(SIMD) $(GO) run ./internal/gen/fixtures

.PHONY: generate
generate:
	$(SIMD) $(GO) generate $(PKGS)

.PHONY: check-generate
check-generate: generate
	@git diff --exit-code || (echo "generated files are stale; run make generate" && exit 1)

.PHONY: check-imports
check-imports: ## enforce the layering invariants in doc.go
	$(SIMD) $(GO) run ./internal/gen/imports -check

.PHONY: lint
lint:
	GOEXPERIMENT=simd golangci-lint run

.PHONY: check
check: vet lint test build-nosimd check-generate check-imports
```

(Recipe lines are tabs.)

### 9.3 The `//go:build` scheme — D14 fixed

The doc's rule ("a scalar twin behind `//go:build !goexperiment.simd`") makes the differential test impossible, because only one implementation is ever linked. Correct scheme: **the scalar implementation is always compiled; only the three-line dispatcher is tagged.**

```
internal/kernel/
  add_f64_scalar.go        (no build tag)      func addF64Scalar(dst, a, b []float64)
  add_f64_simd.go          goexperiment.simd   func addF64SIMD(dst, a, b []float64)
  add_f64_dispatch_on.go   goexperiment.simd   func AddF64(...) { addF64SIMD(...) }
  add_f64_dispatch_off.go  !goexperiment.simd  func AddF64(...) { addF64Scalar(...) }
  add_f64_diff_test.go     goexperiment.simd   TestDifferentialAddF64
```

```go
// add_f64_scalar.go — NO build tag. Always compiled, in every configuration.
//
// This is the reference semantics for AddF64. The SIMD implementation must
// agree with it bit for bit, including NaN payloads and signed zero, and
// TestDifferentialAddF64 asserts exactly that. Keeping this file untagged is
// what makes that test possible at all: tagging it !goexperiment.simd would
// mean only one of the two implementations is ever linked.
func addF64Scalar(dst, a, b []float64) {
	for i := range a {
		dst[i] = a[i] + b[i]
	}
}
```

```go
//go:build goexperiment.simd

// add_f64_dispatch_on.go
//
// A function, not `var AddF64 = addF64SIMD`: the simd package documents
// "BUG(global initialization): SIMD-dependent var initializers don't work", and
// a function wrapper inlines away.
func AddF64(dst, a, b []float64) { addF64SIMD(dst, a, b) }
```

```go
//go:build goexperiment.simd

// add_f64_diff_test.go — both implementations are linked in this build.
func TestDifferentialAddF64(t *testing.T) {
	t.Parallel()
	for _, n := range []int{0, 1, 7, 8, 9, 63, 64, 65, 511, 512, 4096} {
		a, b := randomF64(n), randomF64(n)  // includes ±0, ±Inf, NaN, subnormals
		gotS := make([]float64, n)
		gotV := make([]float64, n)
		addF64Scalar(gotS, a, b)
		addF64SIMD(gotV, a, b)
		assertBitIdentical(t, gotV, gotS)   // math.Float64bits, not ==
	}
}
```

File-naming rule: **never use a `_amd64`/`_arm64` filename suffix for a portable `simd` kernel** — that suffix is an implicit GOARCH constraint that would silently drop the file on other architectures. Reserve arch suffixes for `simd/archsimd` code.

### 9.4 CI matrix

```yaml
# .github/workflows/ci.yml
name: ci
on: [push, pull_request]

jobs:
  test:
    strategy:
      fail-fast: false
      matrix:
        include:
          # 1. The load-bearing job: the library MUST build and pass without the
          #    experiment, or every downstream user is forced into GOEXPERIMENT=simd.
          - name: baseline-no-simd
            os: ubuntu-latest
            goexperiment: ""
            goamd64: v1
          # 2. The primary job.
          - name: simd-v3
            os: ubuntu-latest
            goexperiment: simd
            goamd64: v3
          # 3. Force the software-emulated simd path; catches kernels that
          #    accidentally depend on a 512-bit width.
          - name: simd-emulated
            os: ubuntu-latest
            goexperiment: simd
            goamd64: v1
            godebug: cpu.avx512f=off,cpu.avx2=off
          # 4. Race — the morsel scheduler and the intern table are the targets.
          - name: race
            os: ubuntu-latest
            goexperiment: simd
            goamd64: v3
            flags: -race
          # 5. A non-amd64 arch, to prove the portable simd layer is portable
          #    and to catch endianness/alignment assumptions.
          - name: arm64
            os: ubuntu-24.04-arm
            goexperiment: simd
          # 6. macOS, for the linker/arch differences.
          - name: darwin-arm64
            os: macos-latest
            goexperiment: simd
    runs-on: ${{ matrix.os }}
    env:
      GOTOOLCHAIN: auto
      GOEXPERIMENT: ${{ matrix.goexperiment }}
      GOAMD64: ${{ matrix.goamd64 }}
      GODEBUG: ${{ matrix.godebug }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.27.0', check-latest: true }
      - run: go version && go env GOEXPERIMENT GOAMD64 GOROOT
      - run: go build ./...
      - run: go vet ./...
      - run: go test ${{ matrix.flags }} ./...

  extra:
    runs-on: ubuntu-latest
    env: { GOTOOLCHAIN: auto, GOEXPERIMENT: simd, GOAMD64: v3 }
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.27.0' }
      - run: make check-imports        # layering invariants
      - run: make check-generate       # generated files are current
      - run: make diff                 # SIMD vs scalar, 50 seeds
      - run: go test -run TestGoroutineLeak -v ./internal/exec/
      - uses: golangci/golangci-lint-action@v6
```

Jobs 1 and 3 are the ones that would be dropped by a hurried implementer and are the ones that matter most: #1 protects downstream users, #3 protects the correctness of every kernel on hardware that is not this Tiger Lake box.

### 9.5 GoLand / IDE setup

These are the exact settings; without them GoLand reports the entire `simd` import as unresolved and flags every generic method as a syntax error.

1. **Delete `/main.go`.** `package main` in the module root collides with `package ursus` and GoLand will show the whole root as broken.
2. **Settings → Go → GOROOT** — must be the go1.27.0 toolchain, which `GOTOOLCHAIN=auto` puts inside the module cache. Run `GOTOOLCHAIN=auto go env GOROOT` and point GOROOT there (on this machine: `~/go/1.26.2/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.0.linux-amd64`). The `goenv` shim reports 1.26.2 and GoLand will believe it otherwise. The directory is read-only (module cache); that is fine for indexing.
3. **Settings → Go → Build Tags & Vendoring → Custom tags:** `goexperiment.simd`. This is what makes `import "simd"` resolve in the editor — `GOEXPERIMENT=simd` sets that build tag, and GoLand's indexer keys off tags, not environment.
4. **Settings → Go → Go Modules → Environment:** `GOEXPERIMENT=simd;GOAMD64=v3;GOTOOLCHAIN=auto` (semicolon-separated).
5. **Run/Debug Configurations → Edit Configuration Templates → Go Test and Go Build → Environment:** add `GOEXPERIMENT=simd`. Otherwise every gutter-run test fails to build.
6. **Settings → Tools → File Watchers:** `gofmt` on save. Do not add `goimports` with a custom local prefix until the layout settles.
7. Mark `testdata/` as *Excluded* if fixtures grow past a few MB.

For VS Code the equivalent is `"go.toolsEnvVars": {"GOEXPERIMENT": "simd", "GOTOOLCHAIN": "auto"}` and `"go.buildTags": "goexperiment.simd"`.

### 9.6 Repo conventions

- **Files** `snake_case.go`, one concept per file, mirroring the doc's §1 list. Build-tagged variants use `_simd`/`_scalar` suffixes and an explicit `//go:build`; arch suffixes only for `archsimd` code.
- **Doc comments** on every exported identifier, starting with the identifier's name, using `[Name]` doc links. Every package has a `doc.go`. Non-obvious invariants (interning, alignment, ownership, ordering) are documented at the point they are relied on, not only where they are established.
- **Errors**: never `errors.New` or bare `fmt.Errorf` in ursus code — always `uerr.New`/`uerr.Wrap` so `Kind` and `Op` are set. Enforced with golangci-lint `forbidigo`.
- **Panics** only in `Must*` and in the literal-argument constructors documented to panic. Never in response to data.
- **`context.Context`** is always the first parameter and is never stored in a struct, with exactly one documented exception (`objstore.ReaderAt`).
- **No `init()`** outside `internal/kernel`.
- **Code generation**: two things ever get generated — the per-dtype kernel bodies (agent 2) and, later, the eager `DataFrame` façade. Generators are plain `go run ./internal/gen/...` programs using `text/template`, with no external dependencies. Output is committed, carries `// Code generated by … DO NOT EDIT.`, and `make check-generate` fails CI if it is stale. Enum `String()` methods are hand-written with a completeness test; `stringer` is deliberately not a dependency.
- **Tests** are table-driven with `t.Parallel()`; golden files live in `testdata/`; `t.Context()` (Go 1.24+) instead of `context.Background()`.

---

## 10. Undeclared types (D16): what ships now, what is backlog

**Declared in step 1** (all trivial enums or types my own signatures need):

```go
// enums.go — package ursus (aliases over internal/scanopt and internal/execopt)

type EngineKind uint8
const (EngineAuto EngineKind = iota; EngineInMemory; EngineStreaming)

type OptFlags struct {
	PredicatePushdown, ProjectionPushdown, SlicePushdown, LimitPushdown bool
	CommonSubplanElim, SimplifyExpressions, TypeCoercion                bool
	JoinOrdering, CardinalityEstimation, ClusterWithColumns, CollapseJoins bool
}
func DefaultOptFlags() OptFlags   // everything true
func NoOptFlags() OptFlags        // everything false — the differential-test switch

type MissingPolicy uint8   // MissingError | MissingInsert
type ExtraPolicy uint8     // ExtraError | ExtraIgnore
type ParallelStrategy uint8 // ParallelAuto | ParallelRowGroup | ParallelColumn | ParallelNone
type Compression uint8      // Uncompressed | Snappy | Gzip | Zstd | Lz4 | Brotli
type Closed uint8           // ClosedBoth | ClosedLeft | ClosedRight | ClosedNone
type Keep uint8             // KeepFirst | KeepLast | KeepAny | KeepNone
type NullBehavior uint8     // NullIgnore | NullDrop
type Categories = dtype.Categories
```

Rationale for declaring `Closed`/`Keep`/`NullBehavior` now despite step 1 not using them: they are 4-line enums with obvious values, and declaring them prevents an API churn later for zero cost.

**Backlog, with owners:**

| Type | Owner | Milestone |
|---|---|---|
| `RankMethod`, `Interpolation`, `FillStrategy`, `InterpMethod`, `SearchSide`, `OrderPreference` | A1 | expression completeness |
| `Op` (kernel op enum), `BinaryKernel`/`UnaryKernel`, `Registry` | A2 | kernels. Registry key is `struct{Op; DataType; DataType}` — comparable because `DataType` is |
| `SortSpec`, `JoinHow`, `MappingStrategy`, `Interval`, `DynamicOptions` | A1 | grouping/joins |
| `Profile`, `ExplainOption` details | A1 | introspection |
| `Encoding`, `NullValues`, `CSVScanOptions` fields | — | CSV reader |
| `S3Config`, `GCSConfig`, `AzureConfig` | — | object stores |
| `MatchOption`, `UpdateOption`, `ConcatOption`, `TransposeOption`, `PivotOptions`, `UnpivotOptions`, `SampleOption`, `CutOption`, `HistOption`, `ValueCountsOption`, `CastOption`, `SortOption`, `FillOption`, `RollingOption`, `EwmOptions`, `AsOfOption`, `DeltaOption`, `IcebergOption` | mixed | each follows the §6 scheme mechanically |
| `Categorical` / global string cache | — | after OQ8 is re-opened; depends on execution `Context` |
| `ParseType(string) (DataType, error)` | MINE | needed by SQL and config-driven schema overrides; the inverse of `String()` |

---

## 11. Verification plan

### 11.1 Fixtures

`internal/gen/fixtures/main.go` writes committed test files (each < 200 KB) via `pqarrow.NewFileWriter`, regenerated with `make fixtures`:

| file | shape |
|---|---|
| `testdata/simple.parquet` | 5 columns `a i64, b str, x f64, ts datetime[us,UTC], flag bool`; 3 row groups × 1000 rows; nulls in `b` and `x`; `x` monotonically increasing so statistics pruning is testable |
| `testdata/wide.parquet` | 41 columns, 1 row group — the projection-pushdown ratio test |
| `testdata/nested.parquet` | `s struct{a i64, b str}, l list[i64]` — exercises multi-leaf projection |
| `testdata/empty.parquet` | schema, zero rows |
| `testdata/one_row_group_no_stats.parquet` | statistics disabled, so pruning must degrade gracefully |

### 11.2 Step-1 end-to-end proof

```go
func TestWalkingSkeleton(t *testing.T) {
	ctx := t.Context()
	lf := ursus.ScanParquet("testdata/simple.parquet").
		Filter(ursus.Col("x").Gt(5)).
		Select(ursus.Col("a"), ursus.Col("b"))

	// 1. Schema resolves without producing rows, and without reading a column chunk.
	got, err := lf.CollectSchema(ctx)
	must(t, err)
	ursustest.AssertSchemaEqual(t, got, ursus.MustSchema(
		ursus.NewField("a", ursus.Int64),
		ursus.NewField("b", ursus.String),
	))

	// 2. The optimized plan shows the projection reaching the scan.
	ursustest.AssertPlan(t, lf, "testdata/plans/skeleton.txt")

	// 3. The rows are right.
	df, err := lf.Collect(ctx)
	must(t, err)
	ursustest.AssertFrameEqual(t, df, wantFrame(t))
}
```

`testdata/plans/skeleton.txt`:

```
SELECT [a, b]                                             streaming: yes
  FILTER [x > 5]                                          streaming: yes
    PARQUET SCAN testdata/simple.parquet
      projection: [a, b, x]  (3/5 cols)
      predicate:  none                                    → not pushed (v0.1)
```

**Projection pushdown proved by observed I/O, not by the plan string.** A plan snapshot can pass while the reader still touches every column, so the real assertion is on `source.Stats`:

```go
func TestProjectionPushdownReadsOnlyRequestedColumns(t *testing.T) {
	src := pqsource.New([]string{"testdata/wide.parquet"}, scanopt.DefaultParquet())
	full, _ := src.Schema(t.Context())
	sc, err := src.Open(t.Context(), source.ScanRequest{
		Projection: []int{3, 7, 11}, BatchSize: 1024,
	})
	must(t, err)
	defer sc.Close()
	drain(t, sc)

	st := sc.Stats()
	if st.ColumnsRead != 3 || st.ColumnsTotal != 41 {
		t.Fatalf("got %d/%d columns, want 3/41", st.ColumnsRead, st.ColumnsTotal)
	}
	// And the bytes: a counting objstore.Store wrapper asserts we read far less
	// than the whole file.
	if counted.BytesRead > wholeFileSize/8 {
		t.Errorf("read %d bytes of a %d-byte file; projection did not reach the reader",
			counted.BytesRead, wholeFileSize)
	}
	if full.Len() != 41 { … }
}
```

Plus: **column order under a reordering projection** (`Projection: []int{7,3}` must produce columns in that order, guarding the `GetFieldIndices` ordering assumption), **multi-leaf projection** (`nested.parquet`, projecting the struct column only), and **count-only scan** (`Projection: []int{}` must not call `GetRecordReader`).

**Cancellation:**

```go
func TestCollectHonoursContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	n := 0
	for _, err := range lf.CollectBatches(ctx) {
		if n++; n == 1 { cancel() }
		if err != nil {
			if !errors.Is(err, context.Canceled) { t.Fatalf("got %v", err) }
			goto leakcheck
		}
	}
	t.Fatal("iteration completed despite cancel")
leakcheck:
	// Go 1.27's goroutine leak profile is GA. A morsel-driven engine with
	// channel pipelines is exactly the shape that leaks on early return.
	assertNoLeakedGoroutines(t)
}
```

**Options scheme:** a compile-time test. `options_typecheck_test.go` contains the positive cases; `testdata/options_bad/` holds `.go` files driven through `go/types` (or `go vet` in a sub-`go test`) asserting the negative cases fail with the expected message. Cheaper alternative that is enough: a `// ERROR` example in `options_example_test.go` plus a `TestOptionAssignability` that does `var _ ursus.ParquetScanOption = ursus.WithNRows(1)` for every generic option (a plain compile-time assertion — if the scheme breaks, the package stops building).

**Type system:**

```go
func TestDataTypeIsMapKey(t *testing.T) {
	m := map[ursus.DataType]string{
		ursus.Int64:                      "i64",
		ursus.List(ursus.Int64):          "list[i64]",
		ursus.List(ursus.List(ursus.Int64)): "list[list[i64]]",
		ursus.Struct(ursus.NewField("a", ursus.Int64)): "struct",
		ursus.Datetime(ursus.UnitMicro, "UTC"):         "ts",
	}
	// A separately constructed identical nested type must hit the same entry.
	if m[ursus.List(ursus.Int64)] != "list[i64]" { t.Fatal("interning broken") }
	if ursus.List(ursus.Int64) != ursus.List(ursus.Int64) { t.Fatal("== broken") }
}

func TestInternConcurrent(t *testing.T) {  // run under -race
	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() {
			for range 1000 {
				if ursus.Struct(f("a", ursus.Int64), f("b", ursus.String)) !=
				   ursus.Struct(f("a", ursus.Int64), f("b", ursus.String)) {
					t.Error("racy intern produced unequal types")
				}
			}
		})
	}
	wg.Wait()
}

func TestStringInjective(t *testing.T)   // over a generated corpus incl. nested
func TestDTypeRoundTrip(t *testing.T)    // FromArrow(ToArrow(dt)) == dt, all dt
func TestSchemaRoundTripArrow(t *testing.T)
```

### 11.3 Risk microbenchmark A — zero-copy kernel-loop parity

The load-bearing assumption behind using Arrow buffers as our storage is that a Go kernel over a slice obtained from an Arrow buffer runs *exactly* as fast as one over `make([]float64, n)`. Two things could break it: alignment (`GoAllocator` guarantees 64; `make` does not below 512 bytes — misalignment was measured at 72, 80, …, 480) and any indirection in obtaining the slice.

```go
// internal/arrowx/bench_test.go
//
// Both loops run the SAME kernel over the SAME length. The only difference is
// where the backing memory came from. Any gap is an alignment or aliasing
// effect and we need to know about it before the kernel layer is built on top.
func BenchmarkKernelLoop(b *testing.B) {
	for _, n := range []int{64, 512, 4096, 65536, 1 << 20} {
		b.Run(fmt.Sprintf("n=%d/native", n), func(b *testing.B) {
			a, c, dst := make([]float64, n), make([]float64, n), make([]float64, n)
			fill(a); fill(c)
			b.SetBytes(int64(n * 8))
			b.ResetTimer()
			for b.Loop() { kernel.AddF64(dst, a, c) }
		})
		b.Run(fmt.Sprintf("n=%d/arrow", n), func(b *testing.B) {
			a, c, dst := arrowF64(n), arrowF64(n), arrowF64(n) // via arrowx.Alloc
			b.SetBytes(int64(n * 8))
			b.ResetTimer()
			for b.Loop() { kernel.AddF64(dst, a, c) }
		})
	}
}

// And a guard that fails the build if the gap ever exceeds 10%.
func TestArrowBufferKernelParity(t *testing.T) {
	if testing.Short() { t.Skip() }
	native := benchNs(func() { … })
	arrow  := benchNs(func() { … })
	if arrow > native*1.10 {
		t.Errorf("arrow-backed loop is %.1f%% slower than native; zero-copy storage "+
			"is not free — investigate alignment before building kernels on it",
			(arrow/native-1)*100)
	}
}

// Direct evidence for the alignment story.
func TestAllocatorAlignment(t *testing.T) {
	for _, n := range []int{8, 72, 80, 200, 480, 512, 4096} {
		buf := arrowx.Alloc.Allocate(n)
		if uintptr(unsafe.Pointer(&buf[0]))%64 != 0 {
			t.Errorf("Allocate(%d) returned %d-aligned memory; the GoAllocator "+
				"over-allocate-and-shift guarantee is gone", n, align(buf))
		}
		// Contrast: make() gives no such guarantee below 512 bytes. This is why
		// kernels treat 64-byte alignment as a HINT, not a precondition.
	}
}
```

Expected result: parity, because both are plain `[]float64` and Go's bounds-check elimination sees the same shape. If they are not at parity we learn it in week one rather than after the kernel layer exists.

### 11.4 Risk microbenchmark B — RSS soak, proving OQ2

The entire "hide `Retain`/`Release`" decision rests on un-`Release`d Arrow buffers being ordinary garbage. Prove it.

```go
//go:build soak

// TestSoakNoRelease reads a fixture repeatedly, NEVER calls Release, drops every
// reference, and asserts RSS plateaus.
//
// This is the empirical basis for OQ2. If un-Released buffers were retained,
// hiding Release from users would be an unbounded leak, and DataFrame would
// need a Release/Close method after all.
func TestSoakNoRelease(t *testing.T) {
	const iters = 10_000
	path := bigFixture(t) // ~50 MB, 200 row groups

	var samples []int64
	for i := range iters {
		func() {
			src := pqsource.New([]string{path}, scanopt.DefaultParquet())
			sc, err := src.Open(t.Context(), source.ScanRequest{BatchSize: 8192})
			must(t, err)
			defer sc.Close()
			for {
				b, err := sc.Next(t.Context())
				if errors.Is(err, io.EOF) { break }
				must(t, err)
				_ = b.Rows()   // touch it; never Release anything
			}
		}()
		if i%500 == 0 {
			runtime.GC()
			samples = append(samples, rssBytes(t)) // /proc/self/statm
		}
	}
	steady := median(samples[len(samples)/2:])
	peak := slices.Max(samples)
	if peak > 2*steady {
		t.Fatalf("RSS grew from %d to %d over %d iterations: un-Released arrow "+
			"buffers are NOT being collected; OQ2 must be reopened", steady, peak, iters)
	}
}

// The control: the identical loop that DOES call Release. If the two curves are
// indistinguishable, refcounting on Go-allocated buffers is confirmed to be
// bookkeeping with no memory effect — which is exactly the claim we are relying on.
func TestSoakWithRelease(t *testing.T) { … }

// And the cleanup path itself.
func TestAdoptCleanupReleases(t *testing.T) {
	released := make(chan struct{})
	rec := instrumentedRecord(func() { close(released) })
	b, err := arrowx.Adopt(rec)
	must(t, err)
	rec.Release()          // caller drops their reference; ours keeps it alive
	if b.Rows() == 0 { t.Fatal("batch unusable after caller Release") }
	b = nil                // last reference gone
	for range 5 { runtime.GC() }
	select {
	case <-released:
	case <-time.After(10 * time.Second):
		t.Fatal("AddCleanup never released the adopted record")
	}
}
```

### 11.5 Invocations

```bash
make env                       # confirm go1.27.0 is really what runs

GOEXPERIMENT=simd go test ./...                                   # primary
go test ./...                                                     # no experiment — MUST pass
GOEXPERIMENT=simd go test -race ./...
GOEXPERIMENT=simd GODEBUG=cpu.avx512f=off,cpu.avx2=off go test ./...
GOEXPERIMENT=simd go vet ./... && go vet ./...

GOEXPERIMENT=simd go test -run TestWalkingSkeleton -v .
GOEXPERIMENT=simd go test -run 'TestProjectionPushdown|TestCountOnlyScan' -v ./internal/source/parquet/
GOEXPERIMENT=simd go test -run 'TestDataType|TestIntern|TestStringInjective' -count=20 ./dtype/
GOEXPERIMENT=simd go test -run 'TestDifferential' -count=50 ./internal/kernel/

GOEXPERIMENT=simd go test -run '^$' -bench 'BenchmarkKernelLoop' -benchmem -count=10 ./internal/arrowx/
GOEXPERIMENT=simd go test -run TestSoak -tags soak -timeout 30m -v ./internal/arrowx/

URSUS_UPDATE_GOLDEN=1 GOEXPERIMENT=simd go test -run TestPlan ./...
GOEXPERIMENT=simd go test -run TestGoroutineLeak -v ./internal/exec/
make check-imports
```

---

## 12. Contracts the other two agents must honour

**To agent 1 (`expr` / `plan` / `LazyFrame` / optimizer):**

1. `internal/expr` and `internal/plan` import `ursus/dtype`, `internal/uerr`, `internal/source` — never the root, never `selector`, never `sql`.
2. `plan.Node.Schema(ctx context.Context) (dtype.Schema, error)`. Non-leaf nodes ignore `ctx`; leaf scan nodes use it and cache successes, not failures. This is what makes `CollectSchema(ctx)` implementable.
3. Every node carries a `*uerr.NodeRef` assigned in a single pre-order walk at the start of optimization, and every node method ends with `defer func(){ err = uerr.Attribute(err, n.ref) }()`.
4. Projection pushdown produces `source.ScanRequest.Projection` as **top-level field indices into the source's full schema, in output order, without duplicates**. Predicate pushdown produces `[]source.Term` and must consult `source.PredicatePushdown.Supports`; it may only drop a term above the scan when the source reported `PushdownExact`.
5. `selector.Selector.Expr()` builds an `internal/expr.Node`; expansion is performed by the optimizer through the `Node` interface, so `internal/plan` never imports `selector`. `LazyFrame` has no `SQL` method.
6. `LazyFrame` exposes `CollectSchema(ctx)`, `Explain(ctx, …)`, `Err()`. There is no context-free `Schema()`.

**To agent 2 (`bitmap` / `batch` / `kernel` / `physical` / `exec`):**

1. `bitmap.Bitmap` **must carry a bit offset** (`{buf []byte, off, len int}`). `array.Data.Offset()` is an element offset, so a zero-copy slice starts mid-byte; without it every sliced adoption pays a bitmap copy.
2. `batch.New(schema, rows, cols) *Batch` returns a heap-allocated pointer not embedded in another object — `runtime.AddCleanup` in `arrowx.Adopt` requires it.
3. `batch.NewColumnFromBuffers(dt, length, nullCount, validity []byte, validityBitOffset int, buffers ...[]byte) (Column, error)` must exist; `arrowx.Adopt` and `arrowx.Export` are written against it.
4. **Kernels must treat 64-byte alignment as a hint, not a precondition.** Only allocator-produced buffers are guaranteed aligned; `memory.NewBufferBytes`, `memory.SliceBuffer` and mmap'd IPC buffers may be 8-byte aligned.
5. Kernels never call `memory.DefaultAllocator`; they use `arrowx.Alloc`.
6. The build-tag scheme in §9.3 is mandatory: scalar impls untagged, dispatch tagged, differential tests tagged `goexperiment.simd`.
7. The kernel registry keys on `struct{Op; dtype.DataType; dtype.DataType}`, which is comparable because `DataType` is. `Registry` is a concrete struct so it can carry generic methods.
8. `(*ursus.DataFrame).Batch() *batch.Batch` must exist — `ursustest` compares through it.

---

## 13. Sequencing

| Step | Work | Blocks |
|---|---|---|
| 1a | Delete `main.go`; `go.mod`; `Makefile`; `.golangci.yml`; CI; `doc.go` with the import invariants | everything |
| 1b | `internal/uerr` (Error, Kind, NodeRef, `Nearest`, `UnknownName`) | `dtype` |
| 1c | `ursus/dtype` complete + tests (intern, map key, injectivity, concurrency) | everything |
| 1d | `internal/scanopt`, `internal/execopt`; root `enums.go`, `options_scan.go`, `options_exec.go` + assignability tests | scan.go |
| 1e | `ursus/objstore` (`Store`, `ObjectMeta`, `Local`, `ReaderAt`) | parquet source |
| 1f | `internal/arrowx` (Alloc, dtype/schema conversion, round-trip tests) — needs `batch.Column` from A2 for `Adopt`/`Export`, so land conversions first and adoption second | parquet source |
| 1g | `internal/source` interfaces + `internal/source/memsrc` | A1's plan, A2's operators |
| 1h | `internal/gen/fixtures` + committed testdata | all tests |
| 1i | `internal/source/parquet` (schema, projection, `GetRecordReader`, stats) | walking skeleton |
| 1j | root `dtype_alias.go`, `errors.go`, `scan.go` | walking skeleton |
| 1k | `ursustest` (`AssertSchemaEqual` first — it needs only `dtype`; then `AssertPlan`; then `AssertFrameEqual` once `batch` exists) | all tests |
| 1l | Benchmarks A and B, soak, goroutine-leak test | go/no-go on OQ2 |

Items 1b–1e and 1g have no dependency on either other agent and can start immediately. 1f and 1k's `AssertFrameEqual` are the only places I block on agent 2, and only on `batch.Column`'s buffer-level constructor — which is why it is stated as an explicit contract above rather than discovered later.

---

## Critical files for implementation

- `/home/beck/GolandProjects/ursus/dtype/dtype.go` — `DataType`, constructors, predicates, canonical `String()` (with `/home/beck/GolandProjects/ursus/dtype/intern.go` as its inseparable other half)
- `/home/beck/GolandProjects/ursus/dtype/schema.go` — `Schema`, `Project`, `UnknownColumnError`
- `/home/beck/GolandProjects/ursus/options_scan.go` — the embedded-interface options scheme (the pattern every other option family copies)
- `/home/beck/GolandProjects/ursus/internal/source/source.go` — `Source`/`Scan`/`ScanRequest`/`Term`, the contract between the optimizer and every format
- `/home/beck/GolandProjects/ursus/internal/arrowx/adopt.go` — the ownership model (`Retain` + `runtime.AddCleanup`, `Alloc` pinned to `GoAllocator`)
- `/home/beck/GolandProjects/ursus/Makefile` — the `GOEXPERIMENT=simd` discipline and the mandatory no-experiment build
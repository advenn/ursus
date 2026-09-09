# `ursus` — Proposed Go API

A Polars-class dataframe library for Go: expression DSL, lazy execution with a query
optimizer, streaming/larger-than-RAM execution, Arrow memory, SIMD kernels.

Companion docs: [`dataframe-landscape.md`](./dataframe-landscape.md) (why) ·
[`dataframe-features.md`](./dataframe-features.md) (what) ·
[`go-1.27-release-notes.md`](./go-1.27-release-notes.md) (the language we're targeting)

> **⚠️ This document is the VISION. For what actually exists and compiles, read
> [`step-1-as-built.md`](./step-1-as-built.md).** Building the walking skeleton
> corrected ~24 defects in this file — several of them hard compile errors. The
> three worst are fixed inline below and marked **[CORRECTED]**; the rest are
> catalogued in the as-built document, which is authoritative wherever the two
> disagree.

> **On borrowing from Polars.** We copy the *conceptual model* and *method vocabulary*,
> not source. API names and shapes are not a meaningful legal risk (`Google v. Oracle`,
> 2021), Polars is MIT-licensed, and Narwhals/Ibis/cuDF all already reimplement this
> vocabulary openly. The rule for us: **read the docs, never the Rust.**

---

## 0. Design principles

| # | Principle | Consequence |
| --- | --- | --- |
| 1 | **Lazy is the real API.** | `Scan*` returns `*LazyFrame`. `DataFrame` methods are thin wrappers over `Lazy().…​.Collect()`. One implementation, not three. |
| 2 | **Public types are concrete structs.** | Go 1.27: *a generic method cannot implement an interface method.* If `Expr` were an interface it could never have `Gt[T]`. Polymorphism lives in **unexported** interfaces. |
| 3 | **Errors are values, deferred.** | Builder methods can't return `error` without destroying chaining. Carry a sticky error (`bufio.Scanner` / `sql.Rows` idiom); surface it at `Collect`/`Err`. |
| 4 | **`context.Context` at every execution boundary.** | Cancellation and deadlines — something Polars cannot offer. Never in builder methods, always in `Collect`/`Sink`/`Batches`. |
| 5 | **Named methods, never operator tricks.** | No overloading in Go. `a.Gt(5)` not `a > 5`. This also lets us avoid Polars' `~selector` vs `~expr` ambiguity. |
| 6 | **Functional options for wide parameter surfaces.** | Readers have ~20 knobs. `ScanParquet(path, WithNRows(1000), WithHivePartitioning(true))`. |
| 7 | **Generics for the type boundary, not the core.** | The engine is dynamically typed over `DataType`. Generics appear where Go values meet columns: literals, typed series, struct scanning, UDFs. |
| 8 | **Iterators for streaming output.** | `iter.Seq2[*DataFrame, error]` so results compose with the rest of Go. |
| 9 | **No row index.** | Row order is a property, not an addressable label space. `WithRowIndex()` when you explicitly want one. |
| 10 | **Streaming is introspectable and can be strict.** | Polars silently falls back to in-memory and users discover OOM at runtime. We expose per-operator streamability in `Explain` and offer `WithStrictStreaming()`. |

---

## 1. Package layout

```
ursus/
├─ ursus.go           Col, Lit, All, When, Concat, top-level functions
├─ expr.go            Expr + operator/aggregation methods
├─ expr_str.go        StrExpr    (.Str())
├─ expr_dt.go         DtExpr     (.Dt())
├─ expr_list.go       ListExpr   (.List())
├─ expr_arr.go        ArrExpr    (.Arr())
├─ expr_struct.go     StructExpr (.Struct())
├─ expr_cat.go        CatExpr    (.Cat())
├─ expr_bin.go        BinExpr    (.Bin())
├─ expr_name.go       NameExpr   (.Name())
├─ frame.go           DataFrame  (eager façade)
├─ lazy.go            LazyFrame  (the engine's front door)
├─ series.go          Series[T]
├─ dtype.go           DataType, Field, Schema
├─ groupby.go         LazyGroupBy, GroupByDynamic, Rolling
├─ window.go          Over, WindowSpec, MappingStrategy
├─ join.go            JoinHow, JoinOption, AsOf, JoinWhere
├─ sortspec.go        Asc / Desc / SortSpec
├─ options.go         CollectOption, EngineKind, OptFlags
├─ selector/          column selectors (Polars `cs.*`)
├─ io/
│  ├─ parquet/  csv/  ipc/  ndjson/  avro/
│  └─ objstore/       S3 / GCS / Azure / HTTP behind one interface
├─ sql/               SQL frontend → the same logical plan
├─ ursustest/         AssertFrameEqual, AssertSeriesEqual
└─ internal/
   ├─ arrowx/         arrow-go interop, zero-copy in/out
   ├─ bitmap/         validity bitmaps (SIMD)
   ├─ kernel/         compute kernels, generated per dtype
   ├─ plan/           LogicalPlan + optimizer passes
   ├─ physical/       physical operators
   ├─ exec/           morsel scheduler, backpressure, memory accounting
   └─ spill/          disk-backed hash tables and external merge sort
```

---

## 2. Type system

```go
type TypeID uint8

const (
    TypeNull TypeID = iota
    TypeBoolean
    TypeInt8; TypeInt16; TypeInt32; TypeInt64; TypeInt128
    TypeUint8; TypeUint16; TypeUint32; TypeUint64; TypeUint128
    TypeFloat32; TypeFloat64
    TypeDecimal
    TypeString; TypeBinary
    TypeDate; TypeTime; TypeDatetime; TypeDuration
    TypeList; TypeArray; TypeStruct
    TypeCategorical; TypeEnum
)

// [CORRECTED] As originally written this did NOT compile as claimed: a struct with
// a []Field member is not comparable, so `dt1 == dt2` and map[DataType]T were both
// errors — while the kernel registry and the promotion table need exactly those.
//
// The fix is interning. The struct stays small and comparable; every parameterised
// or nested payload lives behind a canonical *typeExt, so pointer equality IS
// semantic equality. Fields are unexported because an exported Inner or Fields
// would let a caller forge a non-interned value and silently break `==`.
type DataType struct {
    id    TypeID
    unit  TimeUnit // Time, Datetime, Duration
    prec  uint8    // Decimal
    scale uint8    // Decimal
    ext   *typeExt // interned; nil when the type needs no payload
}

// Accessors replace the exported fields: ID, TimeUnit, TimeZone, Precision, Scale,
// Inner, Size, Fields, Categories. See ursus/dtype.

type TimeUnit uint8
const (UnitSecond TimeUnit = iota; UnitMilli; UnitMicro; UnitNano)

// Constructors
var (
    Bool, Int8, Int16, Int32, Int64, Uint8, Uint16, Uint32, Uint64 DataType
    Float32, Float64, Str, Bin, Date, NullT                        DataType
)
func Datetime(u TimeUnit, tz string) DataType
func Duration(u TimeUnit) DataType
func Time(u TimeUnit) DataType
func Decimal(precision, scale uint8) DataType
func List(inner DataType) DataType
func Array(inner DataType, size int) DataType
func Struct(fields ...Field) DataType
func Enum(categories ...string) DataType

func (d DataType) IsNumeric() bool
func (d DataType) IsInteger() bool
func (d DataType) IsFloat() bool
func (d DataType) IsTemporal() bool
func (d DataType) IsNested() bool
func (d DataType) Physical() DataType   // Categorical → Uint32, Date → Int32, …
func (d DataType) String() string
func (d DataType) Equal(o DataType) bool

type Field struct {
    Name     string
    Type     DataType
    Nullable bool
}

// Schema is ordered and name-unique.
type Schema struct{ /* fields []Field + index map[string]int */ }

func NewSchema(fields ...Field) Schema
func (s Schema) Len() int
func (s Schema) Field(i int) Field
func (s Schema) ByName(name string) (Field, bool)
func (s Schema) Names() []string
func (s Schema) Types() []DataType
func (s Schema) Fields() iter.Seq2[int, Field]
func (s Schema) String() string
```

**Rule:** every logical-plan node implements `Schema(input Schema) (Schema, error)` so
schemas resolve **without touching data**. This is what makes `LazyFrame.Schema()`,
IDE-time validation, and projection pushdown possible.

**Null vs NaN** stays distinct throughout: `IsNull`/`IsNaN`, `FillNull`/`FillNan`.
Float total-ordering for sorts and hash-grouping treats NaN as equal to NaN and greater
than everything else — documented as a deliberate deviation from IEEE 754.

---

## 3. `Series[T]` — the typed column

```go
// T is the Go representation of the column's dtype.
// Nulls are NOT boxed into an Option — validity is a parallel bitmap, so the
// values slice stays a flat, SIMD-friendly []T.
type Series[T any] struct{ /* unexported */ }

func NewSeries[T any](name string, values []T) *Series[T]
func NewSeriesNullable[T any](name string, values []T, valid []bool) *Series[T]

func (s *Series[T]) Name() string
func (s *Series[T]) Rename(name string) *Series[T]
func (s *Series[T]) DType() DataType
func (s *Series[T]) Len() int
func (s *Series[T]) NullCount() int
func (s *Series[T]) IsValid(i int) bool
func (s *Series[T]) Get(i int) (value T, valid bool)
func (s *Series[T]) Values() []T          // zero-copy; ignores validity
func (s *Series[T]) All() iter.Seq2[T, bool]
func (s *Series[T]) Slice(offset, length int) *Series[T]   // zero-copy
func (s *Series[T]) Chunks() int
func (s *Series[T]) Rechunk() *Series[T]

// Generic methods (Go 1.27) — the payoff. Previously these had to be
// package-level functions with an explicit receiver argument.
func (s *Series[T]) Map[U any](fn func(T) U) *Series[U]
func (s *Series[T]) MapErr[U any](fn func(T) (U, error)) (*Series[U], error)
func (s *Series[T]) Fold[A any](init A, fn func(A, T) A) A
func (s *Series[T]) Cast[U any]() (*Series[U], error)

// Erased form, for handing columns to the engine.
func (s *Series[T]) Column() Column
type Column struct{ /* dtype-erased, engine-facing */ }
func TypedColumn[T any](c Column) (*Series[T], error)
```

---

## 4. `Expr` — the expression DSL

### 4.1 Core

```go
// Concrete struct. Zero value is invalid; always construct via Col/Lit/etc.
type Expr struct{ node exprNode }   // exprNode is an unexported interface

// Constructors
func Col(names ...string) Expr        // 1 name → one column; N names → expansion
func ColRegex(pattern string) Expr    // explicit; no ^…$ magic
func ColDType(types ...DataType) Expr
func All() Expr
func Exclude(names ...string) Expr
func Nth(i int) Expr
func Lit[T Literal](v T) Expr         // generic: Lit(5), Lit("x"), Lit(time.Now())
func LitNull(dt DataType) Expr
func Element() Expr                   // inside List().Eval()
func Field(name string) Expr          // inside Struct() contexts
func RowIndex() Expr

// [CORRECTED] As originally written this did NOT compile: time.Duration's
// underlying type is int64, so listing it beside ~int64 gives overlapping type
// sets. []byte was also missing its tilde.
//
// Dropping time.Duration costs nothing — ~int64 already admits it, and a
// `case time.Duration:` in the type switch recovers the named type exactly, so a
// Duration literal still gets Duration(ns) rather than Int64.
type Literal interface {
    ~bool |
        ~int | ~int8 | ~int16 | ~int32 | ~int64 |
        ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 |
        ~float32 | ~float64 |
        ~string | ~[]byte |
        time.Time
}
```

### 4.2 Operators — scalars lift automatically

Polars leans on Python operator overloading (`pl.col("a") > 5`). Go can't, but a
**generic method with a union constraint** gets us most of the ergonomics back:

```go
// Operand is either another Expr or a Go scalar that lifts to a literal.
type Operand interface{ Expr | Literal }

func (e Expr) Add[T Operand](v T) Expr
func (e Expr) Sub[T Operand](v T) Expr
func (e Expr) Mul[T Operand](v T) Expr
func (e Expr) Div[T Operand](v T) Expr
func (e Expr) FloorDiv[T Operand](v T) Expr
func (e Expr) Mod[T Operand](v T) Expr
func (e Expr) Pow[T Operand](v T) Expr
func (e Expr) Neg() Expr

func (e Expr) Eq[T Operand](v T) Expr
func (e Expr) Ne[T Operand](v T) Expr
func (e Expr) Lt[T Operand](v T) Expr
func (e Expr) Le[T Operand](v T) Expr
func (e Expr) Gt[T Operand](v T) Expr
func (e Expr) Ge[T Operand](v T) Expr
func (e Expr) EqMissing[T Operand](v T) Expr   // null == null → true
func (e Expr) NeMissing[T Operand](v T) Expr

func (e Expr) And(others ...Expr) Expr
func (e Expr) Or(others ...Expr) Expr
func (e Expr) Xor(other Expr) Expr
func (e Expr) Not() Expr
```

Giving `Col("age").Gt(30)` instead of `Col("age").Gt(Lit(30))` — a real ergonomic win
across thousands of call sites.

> ✅ **Verified on go1.27.0 linux/amd64** (see §18). `interface{ Expr | Literal }`
> compiles: a struct type is a legal union term alongside an embedded constraint
> interface, and inference resolves `T` for all three call shapes —
> `a.Gt(5)`, `a.Gt("x")`, `a.Gt(Col("b"))`. Dispatch inside is
> `switch x := any(v).(type) { case Expr: … ; default: … }`.
> Apply this pattern **uniformly** to every binary operator.

### 4.3 Aggregations, predicates, computation

```go
// Aggregations (also usable inside GroupBy.Agg and Over)
func (e Expr) Sum() Expr;      func (e Expr) Mean() Expr
func (e Expr) Min() Expr;      func (e Expr) Max() Expr
func (e Expr) MinBy(by Expr) Expr;  func (e Expr) MaxBy(by Expr) Expr
func (e Expr) Median() Expr;   func (e Expr) Quantile(q float64, interp Interpolation) Expr
func (e Expr) Std(ddof int) Expr;   func (e Expr) Var(ddof int) Expr
func (e Expr) Count() Expr;    func (e Expr) Len() Expr
func (e Expr) NUnique() Expr;  func (e Expr) ApproxNUnique() Expr
func (e Expr) First() Expr;    func (e Expr) Last() Expr
func (e Expr) Product() Expr;  func (e Expr) NullCount() Expr
func (e Expr) Any() Expr;      func (e Expr) AllTrue() Expr
func (e Expr) ArgMin() Expr;   func (e Expr) ArgMax() Expr
func (e Expr) Implode() Expr
func (e Expr) BitAnd() Expr;   func (e Expr) BitOr() Expr;  func (e Expr) BitXor() Expr

// Predicates
func (e Expr) IsNull() Expr;      func (e Expr) IsNotNull() Expr
func (e Expr) IsNan() Expr;       func (e Expr) IsNotNan() Expr
func (e Expr) IsFinite() Expr;    func (e Expr) IsInfinite() Expr
func (e Expr) IsUnique() Expr;    func (e Expr) IsDuplicated() Expr
func (e Expr) IsFirstDistinct() Expr;  func (e Expr) IsLastDistinct() Expr
func (e Expr) IsBetween(lo, hi Expr, closed Closed) Expr
func (e Expr) IsIn(other Expr) Expr
func (e Expr) IsClose(other Expr, absTol, relTol float64) Expr

// Math / stats
func (e Expr) Abs() Expr;  func (e Expr) Sqrt() Expr;  func (e Expr) Cbrt() Expr
func (e Expr) Exp() Expr;  func (e Expr) Log(base float64) Expr
func (e Expr) Log10() Expr; func (e Expr) Log1p() Expr; func (e Expr) Sign() Expr
func (e Expr) Round(decimals int) Expr
func (e Expr) RoundSigFigs(n int) Expr
func (e Expr) Floor() Expr; func (e Expr) Ceil() Expr
func (e Expr) Clip(lo, hi Expr) Expr
func (e Expr) Sin() Expr /* … Cos, Tan, Cot, Arc*, *h, Degrees, Radians … */
func (e Expr) Skew(bias bool) Expr;  func (e Expr) Kurtosis(fisher, bias bool) Expr
func (e Expr) Entropy(base float64, normalize bool) Expr
func (e Expr) Hist(bins []float64, opts ...HistOption) Expr
func (e Expr) Mode() Expr
func (e Expr) Rank(method RankMethod, descending bool) Expr
func (e Expr) Diff(n int, nullBehavior NullBehavior) Expr
func (e Expr) PctChange(n int) Expr
func (e Expr) Dot(other Expr) Expr
func (e Expr) ValueCounts(opts ...ValueCountsOption) Expr
func (e Expr) UniqueCounts() Expr
func (e Expr) SearchSorted(element Expr, side SearchSide) Expr
func (e Expr) Hash(seed uint64) Expr

// Cumulative
func (e Expr) CumSum(reverse bool) Expr
func (e Expr) CumProd(reverse bool) Expr
func (e Expr) CumMin(reverse bool) Expr
func (e Expr) CumMax(reverse bool) Expr
func (e Expr) CumCount(reverse bool) Expr

// Exponentially weighted
func (e Expr) EwmMean(opts EwmOptions) Expr
func (e Expr) EwmStd(opts EwmOptions) Expr
func (e Expr) EwmVar(opts EwmOptions) Expr
func (e Expr) EwmSum(opts EwmOptions) Expr
func (e Expr) EwmMeanBy(by Expr, halfLife Interval) Expr
```

### 4.4 Manipulation

```go
func (e Expr) Alias(name string) Expr
func (e Expr) Cast(dt DataType, opts ...CastOption) Expr   // WithStrict(false) → nulls
func (e Expr) Sort(opts ...SortOption) Expr
func (e Expr) SortBy(specs ...SortSpec) Expr
func (e Expr) ArgSort(opts ...SortOption) Expr
func (e Expr) Reverse() Expr
func (e Expr) Unique(maintainOrder bool) Expr
func (e Expr) Head(n int) Expr;  func (e Expr) Tail(n int) Expr
func (e Expr) Slice(offset, length Expr) Expr
func (e Expr) TopK(k int) Expr;  func (e Expr) TopKBy(k int, by ...SortSpec) Expr
func (e Expr) BottomK(k int) Expr
func (e Expr) Shift(n int, fillValue Expr) Expr
func (e Expr) Filter(preds ...Expr) Expr
func (e Expr) Gather(indices Expr) Expr
func (e Expr) GatherEvery(n, offset int) Expr
func (e Expr) Get(index Expr) Expr
func (e Expr) Explode() Expr;  func (e Expr) Flatten() Expr
func (e Expr) FillNull(strategy FillStrategy, opts ...FillOption) Expr
func (e Expr) FillNullWith(value Expr) Expr
func (e Expr) FillNan(value Expr) Expr
func (e Expr) DropNulls() Expr;  func (e Expr) DropNans() Expr
func (e Expr) Interpolate(method InterpMethod) Expr
func (e Expr) InterpolateBy(by Expr) Expr
func (e Expr) ForwardFill(limit int) Expr
func (e Expr) BackwardFill(limit int) Expr
func (e Expr) Replace(old, new Expr) Expr
func (e Expr) ReplaceStrict(old, new, defaultValue Expr, returnDType DataType) Expr
func (e Expr) Cut(breaks []float64, opts ...CutOption) Expr
func (e Expr) QCut(quantiles []float64, opts ...CutOption) Expr
func (e Expr) Reshape(dims ...int) Expr
func (e Expr) Rle() Expr;  func (e Expr) RleID() Expr
func (e Expr) Repeat(n Expr) Expr
func (e Expr) Sample(opts ...SampleOption) Expr
func (e Expr) Shuffle(seed uint64) Expr
func (e Expr) SetSorted(descending bool) Expr   // optimizer hint
func (e Expr) ShrinkDType() Expr
func (e Expr) ToPhysical() Expr
func (e Expr) Pipe(fn func(Expr) Expr) Expr
```

### 4.5 Namespaces

Polars' `.str` / `.dt` / `.list` accessors become methods returning small wrapper structs
whose methods return `Expr`, so chaining continues naturally.

```go
func (e Expr) Str() StrExpr
func (e Expr) Dt() DtExpr
func (e Expr) List() ListExpr
func (e Expr) Arr() ArrExpr
func (e Expr) Struct() StructExpr
func (e Expr) Cat() CatExpr
func (e Expr) Bin() BinExpr
func (e Expr) Name() NameExpr
func (e Expr) Meta() MetaExpr
```

```go
type StrExpr struct{ e Expr }

func (s StrExpr) Contains(pattern string, literal bool) Expr
func (s StrExpr) ContainsAny(patterns []string, asciiCaseInsensitive bool) Expr
func (s StrExpr) StartsWith(prefix Expr) Expr
func (s StrExpr) EndsWith(suffix Expr) Expr
func (s StrExpr) Find(pattern string, literal bool) Expr
func (s StrExpr) FindMany(patterns []string, opts ...MatchOption) Expr
func (s StrExpr) CountMatches(pattern string, literal bool) Expr
func (s StrExpr) Extract(pattern string, group int) Expr
func (s StrExpr) ExtractAll(pattern string) Expr
func (s StrExpr) ExtractGroups(pattern string) Expr
func (s StrExpr) Replace(pattern, value Expr, literal bool) Expr
func (s StrExpr) ReplaceAll(pattern, value Expr, literal bool) Expr
func (s StrExpr) ReplaceMany(patterns, replacements []string) Expr
func (s StrExpr) Split(by Expr, inclusive bool) Expr
func (s StrExpr) SplitExact(by Expr, n int) Expr
func (s StrExpr) SplitN(by Expr, n int) Expr
func (s StrExpr) Join(delimiter string, ignoreNulls bool) Expr
func (s StrExpr) LenBytes() Expr
func (s StrExpr) LenChars() Expr
func (s StrExpr) Slice(offset, length Expr) Expr
func (s StrExpr) Head(n Expr) Expr;  func (s StrExpr) Tail(n Expr) Expr
func (s StrExpr) PadStart(length int, fill rune) Expr
func (s StrExpr) PadEnd(length int, fill rune) Expr
func (s StrExpr) ZFill(length Expr) Expr
func (s StrExpr) StripChars(chars Expr) Expr
func (s StrExpr) StripCharsStart(chars Expr) Expr
func (s StrExpr) StripCharsEnd(chars Expr) Expr
func (s StrExpr) StripPrefix(prefix Expr) Expr
func (s StrExpr) StripSuffix(suffix Expr) Expr
func (s StrExpr) ToLower() Expr;  func (s StrExpr) ToUpper() Expr
func (s StrExpr) ToTitle() Expr;  func (s StrExpr) Reverse() Expr
func (s StrExpr) Normalize(form NormalizationForm) Expr
func (s StrExpr) EscapeRegex() Expr
func (s StrExpr) Encode(enc Encoding) Expr
func (s StrExpr) Decode(enc Encoding, strict bool) Expr
func (s StrExpr) JSONDecode(dt DataType, inferLen int) Expr
func (s StrExpr) JSONPathMatch(path Expr) Expr
func (s StrExpr) ToDate(format string, opts ...ParseOption) Expr
func (s StrExpr) ToDatetime(format string, opts ...ParseOption) Expr
func (s StrExpr) ToTime(format string, opts ...ParseOption) Expr
func (s StrExpr) ToInteger(base int, strict bool) Expr
func (s StrExpr) ToDecimal(inferLen int) Expr
```

```go
type DtExpr struct{ e Expr }

func (d DtExpr) Year() Expr; func (d DtExpr) ISOYear() Expr; func (d DtExpr) Quarter() Expr
func (d DtExpr) Month() Expr; func (d DtExpr) Week() Expr; func (d DtExpr) Weekday() Expr
func (d DtExpr) Day() Expr; func (d DtExpr) OrdinalDay() Expr
func (d DtExpr) Hour() Expr; func (d DtExpr) Minute() Expr; func (d DtExpr) Second() Expr
func (d DtExpr) Millisecond() Expr; func (d DtExpr) Microsecond() Expr
func (d DtExpr) Nanosecond() Expr
func (d DtExpr) Date() Expr; func (d DtExpr) Time() Expr; func (d DtExpr) Datetime() Expr
func (d DtExpr) DaysInMonth() Expr
func (d DtExpr) MonthStart() Expr; func (d DtExpr) MonthEnd() Expr
func (d DtExpr) IsLeapYear() Expr; func (d DtExpr) IsBusinessDay(opts ...BusinessOption) Expr
func (d DtExpr) AddBusinessDays(n Expr, opts ...BusinessOption) Expr
func (d DtExpr) OffsetBy(by Interval) Expr
func (d DtExpr) Truncate(every Interval) Expr
func (d DtExpr) Round(every Interval) Expr
func (d DtExpr) Combine(t Expr, unit TimeUnit) Expr
func (d DtExpr) Replace(opts ...DatePartOption) Expr
func (d DtExpr) ConvertTimeZone(tz string) Expr    // instant preserved, wall clock moves
func (d DtExpr) ReplaceTimeZone(tz string, opts ...TZOption) Expr  // wall clock preserved
func (d DtExpr) BaseUTCOffset() Expr; func (d DtExpr) DSTOffset() Expr
func (d DtExpr) CastTimeUnit(u TimeUnit) Expr
func (d DtExpr) WithTimeUnit(u TimeUnit) Expr
func (d DtExpr) Epoch(u TimeUnit) Expr; func (d DtExpr) Timestamp(u TimeUnit) Expr
func (d DtExpr) TotalDays() Expr /* … Hours, Minutes, Seconds, Milli, Micro, Nano */
func (d DtExpr) Strftime(format string) Expr
func (d DtExpr) ToString(format string) Expr
```

```go
type ListExpr struct{ e Expr }

func (l ListExpr) Get(index Expr, nullOnOOB bool) Expr
func (l ListExpr) First() Expr; func (l ListExpr) Last() Expr
func (l ListExpr) Head(n Expr) Expr; func (l ListExpr) Tail(n Expr) Expr
func (l ListExpr) Slice(offset, length Expr) Expr
func (l ListExpr) Len() Expr
func (l ListExpr) Contains(item Expr) Expr
func (l ListExpr) CountMatches(item Expr) Expr
func (l ListExpr) Sum() Expr /* Mean, Median, Min, Max, Std, Var, NUnique */
func (l ListExpr) Sort(opts ...SortOption) Expr
func (l ListExpr) Reverse() Expr; func (l ListExpr) Unique(maintainOrder bool) Expr
func (l ListExpr) ArgMin() Expr; func (l ListExpr) ArgMax() Expr
func (l ListExpr) Diff(n int, nb NullBehavior) Expr
func (l ListExpr) Shift(n Expr) Expr
func (l ListExpr) DropNulls() Expr
func (l ListExpr) Explode() Expr
func (l ListExpr) Join(separator Expr, ignoreNulls bool) Expr
func (l ListExpr) Concat(other Expr) Expr
func (l ListExpr) Gather(indices Expr, nullOnOOB bool) Expr
func (l ListExpr) GatherEvery(n, offset Expr) Expr
func (l ListExpr) Filter(pred Expr) Expr      // pred written against Element()
func (l ListExpr) Eval(expr Expr) Expr        // run a sub-expression per list
func (l ListExpr) Sample(opts ...SampleOption) Expr
func (l ListExpr) ToArray(width int) Expr
func (l ListExpr) ToStruct(opts ...ToStructOption) Expr
func (l ListExpr) SetUnion(other Expr) Expr
func (l ListExpr) SetIntersection(other Expr) Expr
func (l ListExpr) SetDifference(other Expr) Expr
func (l ListExpr) SetSymmetricDifference(other Expr) Expr
```

```go
type StructExpr struct{ e Expr }
func (s StructExpr) Field(names ...string) Expr
func (s StructExpr) Unnest() Expr
func (s StructExpr) RenameFields(names []string) Expr
func (s StructExpr) WithFields(exprs ...Expr) Expr
func (s StructExpr) Drop(names ...string) Expr
func (s StructExpr) JSONEncode() Expr

type NameExpr struct{ e Expr }
func (n NameExpr) Keep() Expr
func (n NameExpr) Prefix(p string) Expr
func (n NameExpr) Suffix(s string) Expr
func (n NameExpr) Map(fn func(string) string) Expr
func (n NameExpr) Replace(old, new string) Expr
func (n NameExpr) ToLower() Expr; func (n NameExpr) ToUpper() Expr
func (n NameExpr) PrefixFields(p string) Expr
func (n NameExpr) SuffixFields(s string) Expr

type MetaExpr struct{ e Expr }
func (m MetaExpr) RootNames() []string
func (m MetaExpr) OutputName() (string, error)
func (m MetaExpr) HasMultipleOutputs() bool
func (m MetaExpr) IsColumn() bool
func (m MetaExpr) IsLiteral() bool
func (m MetaExpr) TreeFormat() string
func (m MetaExpr) Serialize() ([]byte, error)
```

### 4.6 Conditionals

```go
func When(pred Expr) WhenBuilder

type WhenBuilder struct{ /* … */ }
func (w WhenBuilder) Then(value Expr) ThenBuilder

type ThenBuilder struct{ /* … */ }
func (t ThenBuilder) When(pred Expr) WhenBuilder   // chained else-if
func (t ThenBuilder) Otherwise(value Expr) Expr
```

```go
grade := ursus.When(ursus.Col("score").Ge(90)).Then(ursus.Lit("A")).
    When(ursus.Col("score").Ge(80)).Then(ursus.Lit("B")).
    Otherwise(ursus.Lit("F")).Alias("grade")
```

### 4.7 Top-level functions

```go
// Horizontal (row-wise across columns)
func SumHorizontal(exprs ...Expr) Expr
func MeanHorizontal(exprs ...Expr) Expr
func MinHorizontal(exprs ...Expr) Expr
func MaxHorizontal(exprs ...Expr) Expr
func AllHorizontal(exprs ...Expr) Expr
func AnyHorizontal(exprs ...Expr) Expr
func CumSumHorizontal(exprs ...Expr) Expr

func Coalesce(exprs ...Expr) Expr
func ConcatStr(exprs []Expr, opts ...ConcatStrOption) Expr
func ConcatList(exprs ...Expr) Expr
func ConcatArr(exprs ...Expr) Expr
func StructOf(exprs ...Expr) Expr
func Format(fmt string, args ...Expr) Expr

// Folds
func Fold[A any](init Expr, fn func(acc, next Expr) Expr, exprs ...Expr) Expr
func Reduce(fn func(acc, next Expr) Expr, exprs ...Expr) Expr
func CumFold[A any](init Expr, fn func(acc, next Expr) Expr, exprs ...Expr) Expr

// Ranges & constructors
func IntRange(start, end, step Expr, dt DataType) Expr
func IntRanges(start, end, step Expr, dt DataType) Expr
func LinearSpace(start, end Expr, n int, closed Closed) Expr
func DateRange(start, end Expr, every Interval, closed Closed) Expr
func DatetimeRange(start, end Expr, every Interval, opts ...RangeOption) Expr
func TimeRange(start, end Expr, every Interval, closed Closed) Expr
func DateOf(year, month, day Expr) Expr
func DatetimeOf(parts ...Expr) Expr
func DurationOf(opts ...DurationOption) Expr
func FromEpoch(e Expr, u TimeUnit) Expr
func BusinessDayCount(start, end Expr, opts ...BusinessOption) Expr
func Repeat(value Expr, n Expr, dt DataType) Expr
func Ones(n Expr, dt DataType) Expr
func Zeros(n Expr, dt DataType) Expr

// Statistics & indices
func Corr(a, b Expr, opts ...CorrOption) Expr
func Cov(a, b Expr, ddof int) Expr
func RollingCorr(a, b Expr, windowSize int, opts ...RollingOption) Expr
func RollingCov(a, b Expr, windowSize int, opts ...RollingOption) Expr
func ArgSortBy(specs ...SortSpec) Expr
func ArgWhere(pred Expr) Expr
```

---

## 5. Selectors

Polars' `~selector` (complement) vs `~expr` (negate) ambiguity is a real trap. Named
methods remove it entirely.

```go
package selector

type Selector struct{ /* concrete struct */ }

// By dtype
func Numeric() Selector;   func Integer() Selector;  func SignedInteger() Selector
func UnsignedInteger() Selector; func Float() Selector; func Decimal() Selector
func Boolean() Selector;   func String() Selector;   func Binary() Selector
func Temporal() Selector;  func Date() Selector;     func Datetime() Selector
func Time() Selector;      func Duration() Selector
func Categorical() Selector; func Enum() Selector
func List() Selector;      func Array() Selector;    func Struct() Selector
func ByDType(types ...ursus.DataType) Selector

// By name / position
func ByName(names ...string) Selector
func StartsWith(prefixes ...string) Selector
func EndsWith(suffixes ...string) Selector
func Contains(subs ...string) Selector
func Matches(pattern string) Selector
func Alpha() Selector; func Alphanumeric() Selector; func Digit() Selector
func All() Selector; func First() Selector; func Last() Selector
func ByIndex(indices ...int) Selector

// Set algebra — named, unambiguous
func (s Selector) Union(o Selector) Selector
func (s Selector) Intersect(o Selector) Selector
func (s Selector) Difference(o Selector) Selector
func (s Selector) SymmetricDifference(o Selector) Selector
func (s Selector) Complement() Selector       // was Polars' `~sel`
func (s Selector) Exclude(names ...string) Selector

// Bridge to expressions — `Expr()` is explicit, so `.Not()` is never ambiguous
func (s Selector) Expr() ursus.Expr
func (s Selector) Expand(schema ursus.Schema) []string
```

```go
// "multiply every float column except `price` by 1.1, suffixing the name"
df.WithColumns(
    selector.Float().Exclude("price").Expr().Mul(1.1).Name().Suffix("_adj"),
)
```

---

## 6. `LazyFrame` — the contexts

```go
type LazyFrame struct{ plan plan.Node; err error }

// --- Contexts -------------------------------------------------------------
func (lf *LazyFrame) Select(exprs ...Expr) *LazyFrame
func (lf *LazyFrame) SelectSeq(exprs ...Expr) *LazyFrame     // no intra-context parallelism
func (lf *LazyFrame) WithColumns(exprs ...Expr) *LazyFrame
func (lf *LazyFrame) WithColumnsSeq(exprs ...Expr) *LazyFrame
func (lf *LazyFrame) Filter(preds ...Expr) *LazyFrame        // AND-ed
func (lf *LazyFrame) Remove(preds ...Expr) *LazyFrame        // inverse of Filter
func (lf *LazyFrame) GroupBy(keys ...Expr) *LazyGroupBy
func (lf *LazyFrame) Sort(specs ...SortSpec) *LazyFrame

// --- Structure ------------------------------------------------------------
func (lf *LazyFrame) Drop(names ...string) *LazyFrame
func (lf *LazyFrame) Rename(mapping map[string]string) *LazyFrame
func (lf *LazyFrame) Cast(casts map[string]DataType, strict bool) *LazyFrame
func (lf *LazyFrame) CastAll(dt DataType, strict bool) *LazyFrame
func (lf *LazyFrame) WithRowIndex(name string, offset uint32) *LazyFrame
func (lf *LazyFrame) MatchToSchema(s Schema, opts ...MatchOption) *LazyFrame
func (lf *LazyFrame) Pipe(fn func(*LazyFrame) *LazyFrame) *LazyFrame

// --- Rows -----------------------------------------------------------------
func (lf *LazyFrame) Head(n int) *LazyFrame
func (lf *LazyFrame) Tail(n int) *LazyFrame
func (lf *LazyFrame) Limit(n int) *LazyFrame
func (lf *LazyFrame) Slice(offset int64, length uint32) *LazyFrame
func (lf *LazyFrame) First() *LazyFrame;  func (lf *LazyFrame) Last() *LazyFrame
func (lf *LazyFrame) Reverse() *LazyFrame
func (lf *LazyFrame) Gather(indices Expr) *LazyFrame
func (lf *LazyFrame) GatherEvery(n, offset int) *LazyFrame
func (lf *LazyFrame) Unique(subset []string, keep Keep, maintainOrder bool) *LazyFrame
func (lf *LazyFrame) TopK(k int, by ...SortSpec) *LazyFrame
func (lf *LazyFrame) BottomK(k int, by ...SortSpec) *LazyFrame
func (lf *LazyFrame) Sample(opts ...SampleOption) *LazyFrame

// --- Nulls ----------------------------------------------------------------
func (lf *LazyFrame) DropNulls(subset ...string) *LazyFrame
func (lf *LazyFrame) DropNans(subset ...string) *LazyFrame
func (lf *LazyFrame) FillNull(value Expr, subset ...string) *LazyFrame
func (lf *LazyFrame) FillNan(value Expr, subset ...string) *LazyFrame
func (lf *LazyFrame) Interpolate() *LazyFrame

// --- Combining ------------------------------------------------------------
func (lf *LazyFrame) Join(other *LazyFrame, opts ...JoinOption) *LazyFrame
func (lf *LazyFrame) JoinAsOf(other *LazyFrame, opts ...AsOfOption) *LazyFrame
func (lf *LazyFrame) JoinWhere(other *LazyFrame, preds ...Expr) *LazyFrame
func (lf *LazyFrame) Concat(others ...*LazyFrame) *LazyFrame
func (lf *LazyFrame) MergeSorted(other *LazyFrame, key string) *LazyFrame
func (lf *LazyFrame) Update(other *LazyFrame, opts ...UpdateOption) *LazyFrame

func Concat(frames []*LazyFrame, opts ...ConcatOption) *LazyFrame
func ConcatHorizontal(frames ...*LazyFrame) *LazyFrame

// --- Reshaping ------------------------------------------------------------
func (lf *LazyFrame) Explode(columns ...Expr) *LazyFrame
func (lf *LazyFrame) Unnest(columns ...string) *LazyFrame
func (lf *LazyFrame) Unpivot(opts UnpivotOptions) *LazyFrame
func (lf *LazyFrame) Pivot(opts PivotOptions) *LazyFrame
func (lf *LazyFrame) Transpose(opts ...TransposeOption) *LazyFrame

// --- Optimizer hints ------------------------------------------------------
func (lf *LazyFrame) SetSorted(column string, descending bool) *LazyFrame
func (lf *LazyFrame) Cache() *LazyFrame

// --- Introspection --------------------------------------------------------
func (lf *LazyFrame) Schema() (Schema, error)      // no data touched
func (lf *LazyFrame) Explain(opts ...ExplainOption) (string, error)
func (lf *LazyFrame) Err() error                   // sticky error
func (lf *LazyFrame) Serialize() ([]byte, error)
func DeserializeLazyFrame(b []byte) (*LazyFrame, error)
```

### Sort specs

```go
type SortSpec struct { /* … */ }

func Asc(e Expr) SortSpec
func Desc(e Expr) SortSpec
func (s SortSpec) NullsFirst() SortSpec
func (s SortSpec) NullsLast() SortSpec

lf.Sort(ursus.Desc(ursus.Col("ts")), ursus.Asc(ursus.Col("id")).NullsLast())
```

### Error handling in practice

```go
lf := ursus.ScanParquet("events/*.parquet").
    Filter(ursus.Col("tenant_id").Eq(tenant)).
    Select(ursus.Col("typo_column"))    // ← unknown column; no panic, no error here

df, err := lf.Collect(ctx)              // ← error surfaces here, with full context:
// ursus: select: unknown column "typo_column"
//   available: tenant_id, user_id, event, ts, payload
//   did you mean: "top_column"?
//   at plan node #3 (Select), source: events/*.parquet
```

`Err()` is available at any point for callers who want to check early.

---

## 7. Grouping and windows

```go
type LazyGroupBy struct{ /* … */ }

func (g *LazyGroupBy) Agg(exprs ...Expr) *LazyFrame
func (g *LazyGroupBy) MaintainOrder() *LazyGroupBy
func (g *LazyGroupBy) Having(preds ...Expr) *LazyGroupBy

// Shorthands
func (g *LazyGroupBy) Count() *LazyFrame;  func (g *LazyGroupBy) Len(name string) *LazyFrame
func (g *LazyGroupBy) Sum() *LazyFrame;    func (g *LazyGroupBy) Mean() *LazyFrame
func (g *LazyGroupBy) Min() *LazyFrame;    func (g *LazyGroupBy) Max() *LazyFrame
func (g *LazyGroupBy) Median() *LazyFrame; func (g *LazyGroupBy) NUnique() *LazyFrame
func (g *LazyGroupBy) First() *LazyFrame;  func (g *LazyGroupBy) Last() *LazyFrame
func (g *LazyGroupBy) Head(n int) *LazyFrame; func (g *LazyGroupBy) Tail(n int) *LazyFrame
func (g *LazyGroupBy) Quantile(q float64, i Interpolation) *LazyFrame

// Typed escape hatch (generic method)
func (g *LazyGroupBy) MapGroups[T any](outSchema Schema, fn func(*DataFrame) (T, error)) *LazyFrame
```

### Calendar-aware intervals

`time.Duration` cannot express "1 month" or "1 year" — they are not fixed lengths. So:

```go
type Interval struct{ months, days int32; nanos int64 }

func Every(s string) Interval          // "1y", "3mo", "1w", "2d", "6h", "15m", "30s", "1ns"
func FromDuration(d time.Duration) Interval
func (i Interval) IsCalendar() bool    // true if months != 0
func (i Interval) String() string
```

### Dynamic and rolling temporal grouping

```go
type DynamicOptions struct {
    Every            Interval    // where windows start
    Period           Interval    // window duration; zero → Every
    Offset           Interval
    Closed           Closed      // ClosedLeft (default) | Right | Both | None
    Label            Label       // LabelLeft (default) | Right | DataPoint
    StartBy          StartBy     // StartByWindow | StartByDataPoint | StartByMonday…
    IncludeBoundaries bool
    GroupBy          []Expr      // categorical partitioning inside the temporal grouping
}
func (lf *LazyFrame) GroupByDynamic(index Expr, o DynamicOptions) *LazyGroupBy

type RollingOptions struct {
    Period  Interval
    Offset  Interval
    Closed  Closed
    GroupBy []Expr
}
func (lf *LazyFrame) Rolling(index Expr, o RollingOptions) *LazyGroupBy

func (lf *LazyFrame) Upsample(index Expr, every Interval, opts ...UpsampleOption) *LazyFrame
```

> `GroupByDynamic` → one group **per interval** (fixed windows, exist even when empty).
> `Rolling` → one group **per row** (each row anchors its own backwards-looking window).
> Both require the index column sorted; `SetSorted` avoids a re-sort.

### Window functions (`over`)

```go
type MappingStrategy uint8
const (
    MapGroupsToRows MappingStrategy = iota // default; preserves original row order
    MapExplode                             // faster; REORDERS output
    MapJoin                                // aggregate to a List, repeated per row
)

type WindowSpec struct {
    PartitionBy []Expr
    OrderBy     []SortSpec
    Mapping     MappingStrategy
}

func (e Expr) Over(partitionBy ...Expr) Expr
func (e Expr) OverWith(spec WindowSpec) Expr
```

```go
// dense rank of speed within each type, one row per input row
ursus.Col("speed").Rank(ursus.RankDense, true).Over(ursus.Col("type")).Alias("rank_in_type")

// deviation from the group mean
ursus.Col("x").Sub(ursus.Col("x").Mean().Over(ursus.Col("g"))).Alias("centered")
```

### Rolling aggregations on expressions

```go
func (e Expr) RollingMean(windowSize int, opts ...RollingOption) Expr
func (e Expr) RollingSum(windowSize int, opts ...RollingOption) Expr
func (e Expr) RollingMin(windowSize int, opts ...RollingOption) Expr
func (e Expr) RollingMax(windowSize int, opts ...RollingOption) Expr
func (e Expr) RollingStd(windowSize int, opts ...RollingOption) Expr
func (e Expr) RollingVar(windowSize int, opts ...RollingOption) Expr
func (e Expr) RollingMedian(windowSize int, opts ...RollingOption) Expr
func (e Expr) RollingQuantile(q float64, windowSize int, opts ...RollingOption) Expr
func (e Expr) RollingSkew(windowSize int, opts ...RollingOption) Expr
func (e Expr) RollingKurtosis(windowSize int, opts ...RollingOption) Expr
func (e Expr) RollingRank(windowSize int, opts ...RollingOption) Expr

// `…By` variants: window defined by a TEMPORAL index, not a row count.
// Essential for irregular time series.
func (e Expr) RollingMeanBy(by Expr, window Interval, opts ...RollingOption) Expr
// … Sum, Min, Max, Std, Var, Median, Quantile, Rank

// Generic escape hatch
func (e Expr) RollingMap[T any](windowSize int, fn func([]T) (T, error), opts ...RollingOption) Expr

// Options: WithMinPeriods(n), WithWeights(w), WithCenter(), WithClosed(c)
```

---

## 8. Joins

```go
type JoinHow uint8
const (
    JoinInner JoinHow = iota
    JoinLeft
    JoinRight
    JoinFull
    JoinSemi
    JoinAnti
    JoinCross
)

type Validation uint8
const (
    ValidateNone Validation = iota
    ValidateOneToOne     // 1:1
    ValidateOneToMany    // 1:m
    ValidateManyToOne    // m:1
    ValidateManyToMany   // m:m
)

func On(exprs ...Expr) JoinOption
func LeftOn(exprs ...Expr) JoinOption
func RightOn(exprs ...Expr) JoinOption
func How(h JoinHow) JoinOption
func Suffix(s string) JoinOption              // default "_right"
func Coalesce(b bool) JoinOption              // merge same-named key columns
func NullsEqual(b bool) JoinOption            // do null keys match?
func Validate(v Validation) JoinOption        // error on cardinality violation
func MaintainJoinOrder(o OrderPreference) JoinOption
```

```go
enriched := orders.Join(customers,
    ursus.On(ursus.Col("customer_id")),
    ursus.How(ursus.JoinLeft),
    ursus.Validate(ursus.ManyToOne),   // catches accidental fan-out at runtime
)
```

`Validate` is worth building early: accidental row multiplication from an unexpected
many-to-many key is the single most common analytics bug.

### As-of joins

```go
type AsOfStrategy uint8
const (AsOfBackward AsOfStrategy = iota; AsOfForward; AsOfNearest)

func AsOfOn(e Expr) AsOfOption
func AsOfLeftOn(e Expr) AsOfOption;  func AsOfRightOn(e Expr) AsOfOption
func AsOfBy(exprs ...Expr) AsOfOption          // exact match applied BEFORE the asof match
func AsOfStrategyOpt(s AsOfStrategy) AsOfOption
func AsOfTolerance(i Interval) AsOfOption
func AsOfAllowExactMatches(b bool) AsOfOption
```

```go
// Attach the most recent quote (within 1 minute) to each trade, per symbol.
trades.JoinAsOf(quotes,
    ursus.AsOfOn(ursus.Col("ts")),
    ursus.AsOfBy(ursus.Col("symbol")),
    ursus.AsOfStrategyOpt(ursus.AsOfBackward),
    ursus.AsOfTolerance(ursus.Every("1m")),
)
```

### Inequality joins

```go
sessions.JoinWhere(events,
    ursus.Col("ts").Ge(ursus.Col("start")),
    ursus.Col("ts").Lt(ursus.Col("end")),
)
```

Predicates name the join's **output** columns: left columns keep their own names and
a colliding right column takes the suffix, so `k` and `k_right`. There is no
frame-qualified form — this sketch previously showed `Col("events.ts")`, which
nothing can resolve, because `expr.Col` is a bare name looked up in one schema.

`WhereExists` and `WhereNotExists` are the semi and anti forms — SQL's `EXISTS` and
`NOT EXISTS`. They take the same predicates and return the LEFT frame, so a row with
three satisfying partners comes back once:

```go
customers.WhereNotExists(orders,
    ursus.Col("c_custkey").Eq(ursus.Col("o_custkey")),
    ursus.Col("o_totalprice").Gt(1000.0),
)
```

An equality between the two sides becomes a hash join key
(`collapse_cross_join`); the remaining predicates are evaluated per surviving pair.
With no equality among them every pair is tested, which is the loop join. `Explain`
shows which happened — `JOIN INNER … no coalesce` against `JOIN CROSS`.

---

## 9. I/O

### Scan (lazy) / Read (eager) / Sink (streaming) / Write (eager)

```go
// Scans — the optimizer pushes predicates & projections INTO these readers.
func ScanParquet(paths ...string) *LazyFrame
func ScanCSV(paths ...string) *LazyFrame
func ScanIPC(paths ...string) *LazyFrame
func ScanNDJSON(paths ...string) *LazyFrame
func ScanAvro(paths ...string) *LazyFrame
func ScanDelta(uri string, opts ...DeltaOption) *LazyFrame
func ScanIceberg(table string, opts ...IcebergOption) *LazyFrame
func ScanArrow(rdr array.RecordReader) *LazyFrame      // zero-copy from arrow-go
func ScanFunc(schema Schema, fn ScanFunc) *LazyFrame   // custom source (TableProvider)

// With options
func ScanParquetOpts(paths []string, opts ...ParquetScanOption) *LazyFrame
func ScanCSVOpts(paths []string, opts ...CSVScanOption) *LazyFrame

// Eager
func ReadParquet(ctx context.Context, paths ...string) (*DataFrame, error)
func ReadCSV(ctx context.Context, paths ...string) (*DataFrame, error)
// … ReadIPC, ReadNDJSON, ReadJSON, ReadAvro

// Sinks — stream results out without materializing the frame.
func (lf *LazyFrame) SinkParquet(ctx context.Context, path string, opts ...ParquetSinkOption) error
func (lf *LazyFrame) SinkCSV(ctx context.Context, path string, opts ...CSVSinkOption) error
func (lf *LazyFrame) SinkIPC(ctx context.Context, path string, opts ...IPCSinkOption) error
func (lf *LazyFrame) SinkNDJSON(ctx context.Context, path string, opts ...NDJSONSinkOption) error
func (lf *LazyFrame) SinkPartitioned(ctx context.Context, root string, by []string, opts ...SinkOption) error
func (lf *LazyFrame) SinkFunc(ctx context.Context, fn func(*DataFrame) error, opts ...CollectOption) error
```

### Common scan options

```go
func WithSchema(s Schema) ScanOption
func WithSchemaOverrides(m map[string]DataType) ScanOption
func WithInferSchemaLength(n int) ScanOption
func WithNRows(n int) ScanOption
func WithRowIndex(name string, offset uint32) ScanOption
func WithHivePartitioning(b bool) ScanOption
func WithHiveSchema(s Schema) ScanOption
func WithIncludeFilePaths(colName string) ScanOption
func WithGlob(b bool) ScanOption
func WithRechunk(b bool) ScanOption
func WithLowMemory(b bool) ScanOption
func WithCache(b bool) ScanOption
func WithMissingColumns(policy MissingPolicy) ScanOption   // Error | Insert
func WithExtraColumns(policy ExtraPolicy) ScanOption       // Error | Ignore
func WithStorage(s objstore.Store) ScanOption
func WithRetries(n int) ScanOption

// Parquet-specific
func WithUseStatistics(b bool) ParquetScanOption   // skip row groups via min/max
func WithParallelStrategy(p ParallelStrategy) ParquetScanOption // RowGroup|Column|Auto|None

// CSV-specific
func WithSeparator(r rune) CSVScanOption
func WithHasHeader(b bool) CSVScanOption
func WithQuoteChar(r rune) CSVScanOption
func WithCommentPrefix(s string) CSVScanOption
func WithNullValues(v NullValues) CSVScanOption
func WithSkipRows(n int) CSVScanOption
func WithTryParseDates(b bool) CSVScanOption
func WithIgnoreErrors(b bool) CSVScanOption
func WithEncoding(e Encoding) CSVScanOption
```

### Object storage

```go
package objstore

type Store interface {
    Get(ctx context.Context, path string) (io.ReadCloser, error)
    GetRange(ctx context.Context, path string, off, n int64) (io.ReadCloser, error)
    Put(ctx context.Context, path string, r io.Reader) error
    List(ctx context.Context, prefix string) iter.Seq2[ObjectMeta, error]
    Stat(ctx context.Context, path string) (ObjectMeta, error)
}

func Local(root string) Store
func S3(cfg S3Config) Store
func GCS(cfg GCSConfig) Store
func Azure(cfg AzureConfig) Store
func HTTP(client *http.Client) Store
```

`GetRange` is the important method — Parquet footer + selective row-group reads over the
network are what make cloud scans fast.

### Arrow interop (zero-copy, both directions)

```go
func (df *DataFrame) ToArrow() arrow.RecordBatch
func (df *DataFrame) ToArrowTable() arrow.Table
func FromArrow(rec arrow.RecordBatch) (*DataFrame, error)
func FromArrowTable(t arrow.Table) (*DataFrame, error)
func (lf *LazyFrame) ArrowStream(ctx context.Context, opts ...CollectOption) array.RecordReader
```

---

## 10. Execution

```go
type EngineKind uint8
const (
    EngineAuto EngineKind = iota   // in-memory unless the plan looks large
    EngineInMemory
    EngineStreaming
)

func WithEngine(e EngineKind) CollectOption
func WithStrictStreaming() CollectOption      // error instead of silent in-memory fallback
func WithThreads(n int) CollectOption
func WithMemoryLimit(bytes int64) CollectOption
func WithSpillDir(dir string) CollectOption
func WithBatchSize(rows int) CollectOption
func WithOptFlags(f OptFlags) CollectOption

// Per-pass switches, for debugging and benchmarking.
type OptFlags struct {
    PredicatePushdown     bool
    ProjectionPushdown    bool
    SlicePushdown         bool
    CommonSubplanElim     bool
    SimplifyExpressions   bool
    TypeCoercion          bool
    JoinOrdering          bool
    CardinalityEstimation bool
    ClusterWithColumns    bool
    CollapseJoins         bool
    LimitPushdown         bool
}
func DefaultOptFlags() OptFlags
func NoOptFlags() OptFlags
```

```go
// Terminal operations — all take a context.
func (lf *LazyFrame) Collect(ctx context.Context, opts ...CollectOption) (*DataFrame, error)
func (lf *LazyFrame) CollectBatches(ctx context.Context, opts ...CollectOption) iter.Seq2[*DataFrame, error]
func (lf *LazyFrame) Profile(ctx context.Context, opts ...CollectOption) (*DataFrame, *Profile, error)
func (lf *LazyFrame) Count(ctx context.Context, opts ...CollectOption) (int64, error)

// Generic terminals (Go 1.27 generic methods) — the Go-native payoff.
func (lf *LazyFrame) CollectInto[T any](ctx context.Context, opts ...CollectOption) ([]T, error)
func (lf *LazyFrame) Stream[T any](ctx context.Context, opts ...CollectOption) iter.Seq2[T, error]
func (lf *LazyFrame) CollectScalar[T any](ctx context.Context, opts ...CollectOption) (T, error)
```

### Streaming consumption

```go
for batch, err := range lf.CollectBatches(ctx, ursus.WithEngine(ursus.EngineStreaming)) {
    if err != nil {
        return err
    }
    if err := publish(ctx, batch); err != nil {
        return err
    }
}
```

Cancelling `ctx` tears down the whole pipeline — every operator selects on
`ctx.Done()`. **Test these paths with `GODEBUG` off and
`/debug/pprof/goroutineleak` on** (new in Go 1.27); a morsel-driven engine with channel
pipelines is exactly the shape that leaks goroutines on early return.

### Structs in and out

```go
type Trade struct {
    Symbol string    `ursus:"symbol"`
    TS     time.Time `ursus:"ts"`
    Price  float64   `ursus:"price"`
    Size   int64     `ursus:"size,omitempty"`
}

// Out — generic method, no explicit receiver argument needed
trades, err := lf.CollectInto[Trade](ctx)

// Out, streaming
for t, err := range lf.Stream[Trade](ctx) { … }

// In
lf := ursus.FromStructs(trades)                    // inferred schema
lf := ursus.FromStructsSchema(trades, mySchema)    // explicit
```

Struct↔column mapping is resolved **once per query** into a positional plan (field index
→ column index → typed accessor), not per row. Reflection cost is amortized to zero.

### Eager façade

```go
type DataFrame struct{ /* … */ }

func (df *DataFrame) Lazy() *LazyFrame
func (df *DataFrame) Height() int
func (df *DataFrame) Width() int
func (df *DataFrame) Shape() (rows, cols int)
func (df *DataFrame) Columns() []string
func (df *DataFrame) Schema() Schema
func (df *DataFrame) String() string        // pretty terminal table
func (df *DataFrame) Glimpse() string
func (df *DataFrame) Describe() (*DataFrame, error)
func (df *DataFrame) EstimatedSize() int64

// Generic accessors — previously impossible as methods
func (df *DataFrame) Column[T any](name string) (*Series[T], error)
func (df *DataFrame) At[T any](row int, col string) (T, bool, error)
func (df *DataFrame) Rows[T any]() iter.Seq2[T, error]

// Eager mirrors — generated wrappers over Lazy().…​.Collect(context.Background())
func (df *DataFrame) Select(exprs ...Expr) (*DataFrame, error)
func (df *DataFrame) WithColumns(exprs ...Expr) (*DataFrame, error)
func (df *DataFrame) Filter(preds ...Expr) (*DataFrame, error)
// …
```

---

## 11. User-defined functions

Ordered by cost. Go's advantage over Polars is #2: **a per-element UDF in Go is a
function call**, not a Python interpreter round trip.

```go
// 1. Batch UDF — a function over whole chunks. Vectorized, cheapest.
func (e Expr) MapBatches[In, Out any](outType DataType, fn func([]In, []bool) ([]Out, []bool, error)) Expr

// 2. Element UDF — per value. In Go this is genuinely fast.
func (e Expr) MapElements[In, Out any](outType DataType, fn func(In) (Out, error)) Expr

// 3. Group UDF — a function over each group's sub-frame.
func (g *LazyGroupBy) MapGroups[T any](outSchema Schema, fn func(*DataFrame) (T, error)) *LazyFrame

// 4. Window UDF
func (e Expr) RollingMap[T any](windowSize int, fn func([]T) (T, error), opts ...RollingOption) Expr
func (e Expr) CumulativeEval(expr Expr, minPeriods int, reverse bool) Expr

// 5. Frame-level composition
func (lf *LazyFrame) Pipe(fn func(*LazyFrame) *LazyFrame) *LazyFrame
func (lf *LazyFrame) MapBatchesFrame(outSchema Schema, fn func(*DataFrame) (*DataFrame, error)) *LazyFrame

// Registered, reusable, named UDFs (for the SQL frontend and plan serialization)
func RegisterScalarUDF[In, Out any](name string, outType DataType, fn func(In) (Out, error)) error
func RegisterAggUDF[In, Acc, Out any](name string, outType DataType, agg Aggregator[In, Acc, Out]) error

type Aggregator[In, Acc, Out any] struct {
    Init   func() Acc
    Step   func(Acc, In) Acc
    Merge  func(Acc, Acc) Acc     // required for parallel + spilled aggregation
    Finish func(Acc) (Out, error)
}
```

**UDFs must declare their output `DataType`** so the schema stays resolvable without
running them. They should also declare a purity/shape hint
(`Elementwise` / `Aggregating` / `Filtering`) so the optimizer knows whether a predicate
containing them can be pushed into a scan.

---

## 12. SIMD kernel layer

Go 1.27's `simd` package (portable, vector-size-agnostic, `GOEXPERIMENT=simd`) is the
right level for ~95% of kernels; `simd/archsimd` for the hot remainder.

### Portable kernel — the documented loop idiom

```go
//go:build goexperiment.simd

package kernel

import "simd"

// AddF64 computes dst = a + b elementwise.
func AddF64(dst, a, b []float64) {
    for i := 0; i < len(a); {
        va, n := simd.LoadFloat64sPart(a[i:])
        vb, _ := simd.LoadFloat64sPart(b[i:])
        va.Add(vb).StorePart(dst[i:])
        i += n
    }
}

// GtF64 writes a comparison result straight into a validity-style bitmap.
func GtF64(out []byte, a []float64, scalar float64) {
    vs := simd.BroadcastFloat64s(scalar)
    for i := 0; i < len(a); {
        va, n := simd.LoadFloat64sPart(a[i:])
        writeMask(out, i, va.Greater(vs))   // Mask64s → packed bits
        i += n
    }
}
```

`simd`'s mask model maps directly onto Arrow validity bitmaps:

| Need | `simd` primitive |
| --- | --- |
| Comparison → boolean column | `Equal`, `NotEqual`, `Less`, `LessEqual`, `Greater`, `GreaterEqual` → `MaskNs` |
| Null-aware arithmetic | `x.Masked(validityMask)` — zero the invalid lanes |
| `when/then/otherwise`, `fill_null` | `x.IfElse(mask, y)` |
| Saturating integer aggregation | `AddSaturated`, `SubSaturated` |
| `dot`, weighted rolling means | `MulAdd` (FMA) |
| Hashing / checksums | `CarrylessMultiplyEven` / `Odd`, `HasHardwareCarrylessMultiply()` |
| Bit-packed boolean ops | `Uint64s` `And`/`Or`/`Xor`/`AndNot`/`Not` over the bitmap |
| Runtime width | `simd.VectorBitSize()`, `v.Len()`, `simd.Emulated()` |

### Dispatch

The `simd` types are distinct per element width (`Float64s`, `Int32s`, …) with distinct
`Load*` constructors, so kernels cannot be written generically over `T`. Plan:

- **Generate** the ~10 primitive-type kernel bodies from one template (`go:generate`).
- Present a **generic façade** to the engine:

```go
type Numeric interface {
    ~int8 | ~int16 | ~int32 | ~int64 |
    ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~float32 | ~float64
}

// [CORRECTED] This signature took ONE validity bitmap and no input validities,
// which makes `null + 1 == null` literally inexpressible — defect D13.
//
// Kernels are now split by their relationship to nulls, and each class is handed
// only the bitmaps it is entitled to see. A Total kernel cannot get propagation
// wrong because it is not given them at all; the dispatcher computes `va AND vb`.
type TotalBinary[T Numeric]  func(dst, a, b []T)
type TotalCompare[T Numeric] func(out *bitmap.Builder, a, b []T)
type PartialBinary[T Numeric] func(dst []T, ok *bitmap.Builder, a, b []T, in bitmap.View)
type MissingCompare[T Numeric] func(out *bitmap.Builder, a, b []T, va, vb bitmap.View)
type KleeneBinary func(outVals, outValid *bitmap.Builder, a, va, b, vb bitmap.View)

// Registry is a concrete struct so it can carry generic methods.
type Registry struct{ /* … */ }
func (r *Registry) Binary[T Numeric](op Op) (BinaryKernel[T], bool)
func (r *Registry) Unary[T Numeric](op Op) (UnaryKernel[T], bool)
```

- Ship a **scalar fallback** behind `//go:build !goexperiment.simd` so the library builds
  and passes tests without the experiment enabled. **Every SIMD kernel must have a
  scalar twin and a differential test asserting bit-identical results.**
- `simd.Emulated()` reports software emulation; use it to pick batch sizes and to warn in
  `Profile()` output.
- Reserve `archsimd` for: validity-bitmap popcount/AND-fold, string-view 4-byte prefix
  comparison, hash-table probe vectors, and Parquet bit-unpacking — where a fixed
  512-bit width and `Compress`/`Expand`/`Permute` are worth arch-specific code.

### Memory alignment

Allocate all column buffers **64-byte aligned** (Arrow's recommendation) so SIMD loops
need no scalar prologue. This is a property of our `memory.Allocator` implementation,
not of the kernels.

---

## 13. SQL frontend

```go
package sql

type Context struct{ /* … */ }

func New(opts ...Option) *Context
func (c *Context) Register(name string, lf *ursus.LazyFrame) *Context
func (c *Context) RegisterMany(frames map[string]*ursus.LazyFrame) *Context
func (c *Context) Unregister(names ...string) *Context
func (c *Context) Tables() []string
func (c *Context) Query(query string) (*ursus.LazyFrame, error)   // → the SAME logical plan
func (c *Context) Execute(ctx context.Context, query string) (*ursus.DataFrame, error)

func Expr(s string) (ursus.Expr, error)    // a SQL fragment as an expression
```

**Rule: SQL lowers to the same `plan.Node` IR as the expression API.** There is never a
second engine. Build this after the expression API stabilizes.

---

## 14. Testing utilities — ship these in v0.1

```go
package ursustest

func AssertFrameEqual(t testing.TB, got, want *ursus.DataFrame, opts ...Option)
func AssertSeriesEqual[T any](t testing.TB, got, want *ursus.Series[T], opts ...Option)
func AssertSchemaEqual(t testing.TB, got, want ursus.Schema)

func CheckRowOrder(b bool) Option
func CheckColumnOrder(b bool) Option
func CheckDTypes(b bool) Option
func WithTolerance(abs, rel float64) Option
func CheckExact() Option

// Snapshot the optimized plan — catches optimizer regressions.
func AssertPlan(t testing.TB, lf *ursus.LazyFrame, goldenFile string)
```

---

## 15. Naming map: Polars → `ursus`

| Polars | `ursus` | Note |
| --- | --- | --- |
| `pl.col("a")` | `ursus.Col("a")` | |
| `pl.lit(5)` | `ursus.Lit(5)` | generic |
| `a > 5` | `a.Gt(5)` | scalar auto-lifts |
| `a & b` | `a.And(b)` | |
| `~a` | `a.Not()` | |
| `~cs.numeric()` | `selector.Numeric().Complement()` | ambiguity removed |
| `cs.numeric() \| cs.string()` | `selector.Numeric().Union(selector.String())` | |
| `.alias("x")` | `.Alias("x")` | |
| `with_columns` | `WithColumns` | |
| `group_by(...).agg(...)` | `GroupBy(...).Agg(...)` | |
| `group_by_dynamic` | `GroupByDynamic` | |
| `n_unique` | `NUnique` | Go initialisms |
| `str.to_uppercase()` | `Str().ToUpper()` | |
| `dt.year()` | `Dt().Year()` | |
| `list.eval(...)` | `List().Eval(...)` | |
| `arr.*` | `Arr().*` | fixed-size |
| `struct.field("x")` | `Struct().Field("x")` | |
| `name.prefix("p")` | `Name().Prefix("p")` | |
| `is_null()` | `IsNull()` | |
| `sort(descending=True)` | `Sort(ursus.Desc(...))` | |
| `join(..., how="left")` | `Join(..., ursus.How(ursus.JoinLeft))` | |
| `join_asof` | `JoinAsOf` | |
| `join_where` | `JoinWhere` | |
| `over("g")` | `.Over(Col("g"))` | |
| `scan_parquet` | `ScanParquet` | |
| `sink_parquet` | `SinkParquet(ctx, path)` | takes a context |
| `collect(engine="streaming")` | `Collect(ctx, WithEngine(EngineStreaming))` | |
| `collect_batches` | `CollectBatches(ctx)` → `iter.Seq2` | |
| `explain()` | `Explain()` | returns `(string, error)` |
| `map_elements` | `MapElements[In, Out]` | fast in Go |
| `to_arrow` | `ToArrow` | zero-copy |
| `pl.SQLContext` | `sql.Context` | |
| *(none)* | `Validate(ManyToOne)` on joins | promoted; catches fan-out |
| *(none)* | `WithStrictStreaming()` | no silent fallback |
| *(none)* | `CollectInto[T]`, `Stream[T]` | Go structs |
| *(none)* | `context.Context` | cancellation |

---

## 16. Worked examples

### Larger-than-RAM ETL

```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
defer cancel()

err := ursus.ScanParquetOpts(
        []string{"s3://logs/year=*/month=*/*.parquet"},
        ursus.WithHivePartitioning(true),
        ursus.WithStorage(objstore.S3(s3cfg)),
        ursus.WithUseStatistics(true),
    ).
    Filter(
        ursus.Col("status").Ge(500),
        ursus.Col("ts").IsBetween(ursus.Lit(start), ursus.Lit(end), ursus.ClosedBoth),
    ).
    WithColumns(
        ursus.Col("path").Str().Extract(`^/api/v(\d+)/`, 1).Cast(ursus.Int32).Alias("api_version"),
        ursus.Col("latency_ms").Log10().Alias("log_latency"),
    ).
    GroupByDynamic(ursus.Col("ts"), ursus.DynamicOptions{
        Every:   ursus.Every("1h"),
        Closed:  ursus.ClosedLeft,
        GroupBy: []ursus.Expr{ursus.Col("service"), ursus.Col("api_version")},
    }).
    Agg(
        ursus.Len().Alias("errors"),
        ursus.Col("latency_ms").Quantile(0.99, ursus.InterpLinear).Alias("p99"),
        ursus.Col("user_id").NUnique().Alias("affected_users"),
    ).
    Sort(ursus.Desc(ursus.Col("errors"))).
    SinkParquet(ctx, "s3://reports/error-rollup.parquet")
```

Only the `status`, `ts`, `path`, `latency_ms`, `user_id`, and `service` columns are ever
read; row groups whose `status` statistics exclude `>= 500` are skipped; hive partition
pruning drops whole directories; and nothing larger than a batch is ever resident.

### Financial as-of join, results into Go structs

```go
type Enriched struct {
    Symbol string    `ursus:"symbol"`
    TS     time.Time `ursus:"ts"`
    Price  float64   `ursus:"price"`
    Spread float64   `ursus:"spread"`
}

lf := ursus.ScanParquet("trades.parquet").
    SetSorted("ts", false).
    JoinAsOf(
        ursus.ScanParquet("quotes.parquet").SetSorted("ts", false),
        ursus.AsOfOn(ursus.Col("ts")),
        ursus.AsOfBy(ursus.Col("symbol")),
        ursus.AsOfTolerance(ursus.Every("1m")),
    ).
    WithColumns(
        ursus.Col("ask").Sub(ursus.Col("bid")).Alias("spread"),
    ).
    Filter(ursus.Col("spread").IsNotNull())

out, err := lf.CollectInto[Enriched](ctx)
```

### Window functions and selectors

```go
df, err := lf.
    WithColumns(
        // rank within each category
        ursus.Col("revenue").Rank(ursus.RankDense, true).
            Over(ursus.Col("category")).Alias("rank_in_cat"),
        // share of category total
        ursus.Col("revenue").
            Div(ursus.Col("revenue").Sum().Over(ursus.Col("category"))).
            Alias("share"),
        // 7-day trailing mean on an irregular index
        ursus.Col("revenue").
            RollingMeanBy(ursus.Col("date"), ursus.Every("7d")).
            Alias("ma7"),
        // round every float column to 2dp, in place
        selector.Float().Expr().Round(2),
    ).
    Filter(ursus.Col("rank_in_cat").Le(10)).
    Collect(ctx)
```

### Inspecting the plan before running it

```go
plan, err := lf.Explain(ursus.Optimized(true), ursus.ShowStreamability(true))
fmt.Println(plan)
// SORT [revenue DESC]                                   streaming: spills
//   AGGREGATE [service, api_version] × 3                 streaming: spills
//     GROUP_BY_DYNAMIC every=1h closed=left              streaming: yes
//       WITH_COLUMNS [api_version, log_latency]          streaming: yes
//         PARQUET SCAN s3://logs/year=*/month=*/*.parquet
//           projection: [ts, service, path, latency_ms, status, user_id]  (6/41 cols)
//           predicate:  [status >= 500]                  → pushed to row-group stats
//           hive prune: year, month                      → 1,204 / 51,840 files
```

---

## 17. Open questions to settle before coding

1. ~~**`Operand` union constraint.**~~ **Resolved — it works.** See §18.
2. **Arrow ownership.** `arrow-go` uses explicit `Retain`/`Release`. Do we expose that,
   or wrap everything in GC-managed handles and accept the finalizer cost? Leaning:
   hide it, expose `Release()` only on frames obtained via zero-copy `FromArrow`.
3. **`Series[T]` vs erased `Column`.** Two representations means two code paths. Proposal:
   `Column` is the only engine-facing type; `Series[T]` is a typed *view* over it,
   constructed on demand and free.
4. **Chunked vs contiguous columns.** Chunking makes `Concat` O(1) but complicates every
   kernel. Proposal: chunked internally, `Rechunk()` before kernels that need contiguity,
   and measure.
5. **String representation.** Arrow `StringView` (16-byte views, 4-byte prefix) is the
   better analytics layout but is more work and less widely supported than the
   offsets+data layout. Proposal: start with offsets, design the kernels behind an
   interface so views can be added without touching call sites.
6. **Spilling design.** Partitioned hash tables to disk vs. sort-merge fallback for
   aggregation. Proposal: radix-partition on the hash, spill whole partitions, process
   partition-at-a-time — the DuckDB approach.
7. **How much of `DataFrame` to generate.** Proposal: `go:generate` the eager façade from
   the `LazyFrame` method set so it cannot drift.
8. **Categorical/Enum global string cache.** Cross-frame category comparison needs a
   shared registry. Scope it per-`Context` rather than global, to avoid Polars' global
   string cache footgun.

---

## 18. Verified language behaviour

Run on **go1.27.0 linux/amd64**, AVX-512 hardware. These are measured results, not
assumptions — the four claims this API design rests on.

### 18.1 Union constraint with a struct term ✅

```go
type Expr struct{ node exprNode }
type Literal interface{ ~bool | ~int | ~int64 | ~float64 | ~string }
type Operand interface{ Expr | Literal }        // compiles

func (e Expr) Gt[T Operand](v T) Expr {
    switch x := any(v).(type) {
    case Expr: /* … */
    default:   /* lift x to a literal node */
    }
}
```

Inference succeeds for `a.Gt(5)`, `a.Gt("x")`, and `a.Gt(Col("b"))`.
**`Col("a").Gt(5)` needs no explicit `Lit(...)`.**

### 18.2 Generic methods on generic types ✅

```go
type Series[T any] struct{ vals []T }

func (s *Series[T]) Map[U any](fn func(T) U) *Series[U]
func (s *Series[T]) Fold[A any](init A, fn func(A, T) A) A
```

Both compile and run. `Series[int].Map(func(int) string)` correctly yields
`*Series[string]` with `U` inferred. This is what makes the typed layer fluent rather
than a pile of package-level functions.

### 18.3 Generic methods cannot implement interfaces ✅ (confirmed limitation)

```
cannot use S{} (value of struct type S) as Mapper value in variable declaration:
    S does not implement Mapper (wrong type for method Map)
        have Map[U any](func(int) U) Mapper
        want Map(func(int) int) Mapper
```

**This is why every public type is a concrete struct** (principle #2). Any type that
needs a generic method can never be reached through an interface, so `Expr`,
`DataFrame`, `LazyFrame`, `Series[T]`, `DataType`, and `Selector` are all structs, and
all polymorphism lives in unexported interfaces (`exprNode`, `plan.Node`).

### 18.4 The `simd` package ✅

```
$ GOEXPERIMENT=simd go run .
VectorBitSize: 512   Emulated: false
add:          [11 22 33 44 55 66 77]
masked(>3.5): {0,0,0,4,5,6,7,0}
```

The documented `LoadPart`/`StorePart` loop idiom works exactly as written for a
7-element tail on a 512-bit (8×float64) vector — no scalar prologue or epilogue needed:

```go
func AddF64(dst, a, b []float64) {
    for i := 0; i < len(a); {
        va, n := simd.LoadFloat64sPart(a[i:])
        vb, _ := simd.LoadFloat64sPart(b[i:])
        va.Add(vb).StorePart(dst[i:])
        i += n
    }
}
```

`Greater` → `Mask64s` → `Masked` composes exactly as needed for null-aware kernels: the
mask is the validity bitmap, and `Masked` zeroes invalid lanes in one instruction.
`simd.Emulated()` correctly reports hardware support.

### 18.5 Toolchain gotcha

`go version` reports the toolchain selected **for the current module**. In a directory
whose `go.mod` lacks `go 1.27`, it reported `go1.27.0`; in a scratch module defaulting to
`go 1.26.2`, generic methods failed with `syntax error: method must have no type
parameters`. **Every module and scratch test in this repo must declare `go 1.27`.**

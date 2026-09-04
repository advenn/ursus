// Package ursus is a Polars-class dataframe library for Go: an expression DSL,
// lazy execution with a query optimizer, Arrow memory, and SIMD kernels.
//
// # A tour
//
//	df, err := ursus.Scan(src).
//		Filter(ursus.Col("price").Gt(5)).
//		Select(ursus.Col("id"), ursus.Col("price")).
//		Collect(ctx)
//
// Nothing runs until Collect. In between, ursus resolves the schema, expands any
// multi-column selections, type-checks every expression, and pushes the projection
// down into the scan so only the columns actually used are ever read.
//
// # The design in ten lines
//
//  1. Lazy is the real API. Eager helpers are thin wrappers over it.
//  2. Public types are concrete structs — Go forbids generic methods on
//     interfaces, and generic methods are the point.
//  3. Errors are deferred and sticky, surfaced at Collect, so chaining stays clean.
//  4. context.Context appears at execution boundaries only, never in builders.
//  5. Named methods, never operator tricks: a.Gt(5), not a > 5.
//  6. Functional options where the parameter surface is wide.
//  7. Generics at the boundary where Go values meet columns, not in the engine.
//  8. Iterators for streaming output.
//  9. No row index: row order is a property, not a label space.
//  10. Null is not NaN, and both are handled explicitly everywhere.
//
// # Building
//
// ursus requires Go 1.27 and GOEXPERIMENT=simd. Use the Makefile, or export the
// variable yourself — package simd does not compile without it.
package ursus

import (
	"strconv"
	"time"

	"ursus/dtype"
	"ursus/i128"
	"ursus/internal/expr"
	"ursus/internal/uerr"
)

// Re-exported type system. These aliases are the blessed spellings: write
// ursus.Int64 and ursus.Schema rather than importing ursus/dtype directly.
type (
	// Int128Value is a signed 128-bit integer, the Go representation of an Int128
	// column. Read one with df.Column[ursus.Int128Value]("total").
	Int128Value = i128.Int128

	// DataType is a logical column type. Comparable, usable as a map key.
	DataType = dtype.DataType
	// Field is a named, typed, nullable column slot.
	Field = dtype.Field
	// Schema is an ordered, name-unique sequence of Fields.
	Schema = dtype.Schema
	// TimeUnit is the resolution of a Time, Datetime or Duration.
	TimeUnit = dtype.TimeUnit
)

// Simple types.
var (
	Bool    = dtype.Bool
	Int8    = dtype.Int8
	Int16   = dtype.Int16
	Int32   = dtype.Int32
	Int64   = dtype.Int64
	Uint8   = dtype.Uint8
	Uint16  = dtype.Uint16
	Uint32  = dtype.Uint32
	Uint64  = dtype.Uint64
	Float32 = dtype.Float32
	Float64 = dtype.Float64
	// Int128 is the accumulator and output type of integer Sum. Widening to 128
	// bits is what makes an integer sum incapable of silently overflowing.
	Int128 = dtype.Int128
	String = dtype.String
	Binary = dtype.Binary
	Date   = dtype.Date
	NullT  = dtype.Null
)

// Error kinds, for branching on a failure without matching on its message.
//
//	if errors.Is(err, ursus.ErrResource) { retry with a bigger memory limit }
//
// These are re-exported because the sentinels themselves live under internal/, so
// no package outside this module could reach them — which made "a caller should be
// able to detect that without matching on a message" true only for callers inside
// the module.
var (
	// ErrSchema is an unknown, duplicate or ambiguous column.
	ErrSchema = uerr.ErrSchema
	// ErrType is an operation not defined for the given types.
	ErrType = uerr.ErrType
	// ErrValue is a bad literal, an out-of-range cast, an unparseable pattern.
	ErrValue = uerr.ErrValue
	// ErrUnsupported is a well-formed request ursus cannot yet serve.
	ErrUnsupported = uerr.ErrUnsupported
	// ErrIO is a failure reading or writing a data source.
	ErrIO = uerr.ErrIO
	// ErrResource is a limit ursus refused to exceed: the query would have worked
	// with a larger WithMemoryLimit or a writable WithSpillDir.
	ErrResource = uerr.ErrResource
	// ErrInternal is a bug in ursus. Users should never legitimately see one.
	ErrInternal = uerr.ErrInternal
)

// Time resolutions.
const (
	Second = dtype.Second
	Milli  = dtype.Milli
	Micro  = dtype.Micro
	Nano   = dtype.Nano
)

// Parameterised type constructors.
var (
	Datetime = dtype.Datetime
	Duration = dtype.Duration
	TimeOf   = dtype.Time
	Decimal  = dtype.Decimal
	List     = dtype.List
	Array    = dtype.Array
	Enum     = dtype.Enum
)

// StructOf builds a struct type. Named StructOf rather than Struct to leave the
// bare name available and to match DateOf / TimeOf.
func StructOf(fields ...Field) DataType { return dtype.Struct(fields...) }

// Of builds a nullable field; NotNull builds a non-nullable one.
var (
	Of      = dtype.Of
	NotNull = dtype.NotNull
)

// NewSchema builds a schema, rejecting duplicate column names.
func NewSchema(fields ...Field) (*Schema, error) { return dtype.NewSchema(fields...) }

// MustSchema is NewSchema for tests and package-level vars.
func MustSchema(fields ...Field) *Schema { return dtype.MustSchema(fields...) }

// --- literal construction ----------------------------------------------------

// litNode converts a Go value into a literal IR node.
//
// The `~` in the Literal constraint means a caller may pass a defined type such as
// `type UserID int64`, which no `case int64` will match. litNodeReflect handles
// that; it runs once at plan-build time, never per row.
func litNode(v any) expr.Node {
	switch x := v.(type) {
	case bool:
		return &expr.Lit{Value: x, DT: dtype.Bool}
	case int:
		return &expr.Lit{Value: int64(x), DT: dtype.Int64}
	case int8:
		return &expr.Lit{Value: x, DT: dtype.Int8}
	case int16:
		return &expr.Lit{Value: x, DT: dtype.Int16}
	case int32:
		return &expr.Lit{Value: x, DT: dtype.Int32}
	case int64:
		return &expr.Lit{Value: x, DT: dtype.Int64}
	case uint:
		return &expr.Lit{Value: uint64(x), DT: dtype.Uint64}
	case uint8:
		return &expr.Lit{Value: x, DT: dtype.Uint8}
	case uint16:
		return &expr.Lit{Value: x, DT: dtype.Uint16}
	case uint32:
		return &expr.Lit{Value: x, DT: dtype.Uint32}
	case uint64:
		return &expr.Lit{Value: x, DT: dtype.Uint64}
	case float32:
		return &expr.Lit{Value: x, DT: dtype.Float32}
	case float64:
		return &expr.Lit{Value: x, DT: dtype.Float64}
	case string:
		return &expr.Lit{Value: x, DT: dtype.String}
	case []byte:
		return &expr.Lit{Value: x, DT: dtype.Binary}

	// Matched before int64 would be considered, so a Duration keeps its identity
	// even though ~int64 also admits it. This is why dropping time.Duration from
	// the Literal union costs nothing.
	case time.Duration:
		return &expr.Lit{Value: int64(x), DT: dtype.Duration(dtype.Nano)}

	// The instant is preserved in UTC. A literal never smuggles in a
	// *time.Location; use the temporal namespace for wall-clock work.
	case time.Time:
		return &expr.Lit{Value: x.UTC(), DT: dtype.Datetime(dtype.Nano, "UTC")}

	default:
		return litNodeReflect(v)
	}
}

func quote(s string) string { return strconv.Quote(s) }

func errNoColumns() error {
	return uerr.New(uerr.KindSchema, "col", "Col requires at least one column name")
}

func errNoCoalesceArgs() error {
	return uerr.New(uerr.KindSchema, "coalesce",
		"Coalesce requires at least one expression")
}

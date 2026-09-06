// Package parquet reads and writes Apache Parquet files.
//
// # Why the low-level API and not pqarrow
//
// arrow-go ships pqarrow, which converts a Parquet file to arrow Records in one
// call. It is not used here for two reasons.
//
// The dependency: pqarrow imports arrow/flight for a single metadata helper, and
// flight pulls in grpc, protobuf, genproto, x/net and x/text. Measured on this
// tree, parquet/file took go.mod from 7 modules to 14 — thrift and the compression
// codecs, all of which a Parquet reader genuinely needs. pqarrow would add a gRPC
// stack to the dependency graph of a library whose job is to read a file.
//
// The conversion: pqarrow would produce arrow arrays that ursus would immediately
// have to unwrap into its own Columns. Reading straight from the column chunk
// readers into ursus buffers skips a whole materialisation, and it is the same
// code either way — the def-level expansion below is what pqarrow does internally.
//
// # What is supported
//
// FLAT schemas of the primitive types, plus DECIMAL. data.Column has no child
// columns, so a List or a Struct has nowhere to land and pretending otherwise
// would mean silently flattening someone's data.
//
// A nested column is therefore NAMED in the schema — `tags: List(Int64)` — and
// refused when a query asks to read it. It used to abort the whole file, which
// meant a file with twenty flat columns and one struct had no readable columns at
// all. Narrowing the refusal from the file to the column keeps the guarantee that
// mattered (nothing is silently missing: selecting it, or selecting everything,
// still fails) without the collateral damage.
package parquet

import (
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/schema"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// toDataType maps a Parquet column to an ursus type.
//
// It switches on the LOGICAL type first and the physical type second, which is
// the whole of the mapping: Parquet stores an Int8 and an Int32 identically and
// only the logical annotation tells them apart.
//
// The deprecated ConvertedType path costs nothing. arrow-go calls ToLogicalType
// when it builds schema nodes from a footer, so a file written in 2016 arrives
// here with a populated LogicalType and this function never has to know.
func toDataType(c *schema.Column) (dtype.DataType, error) {
	if c.MaxRepetitionLevel() > 0 {
		return dtype.Null, unsupportedColumn(c,
			"repeated (list) columns are not supported").
			Hint("a repeated column has no flat representation in ursus")
	}
	// A flat schema puts every column exactly one level below the root, so a
	// definition level above 1 means the column is inside a group.
	if c.MaxDefinitionLevel() > 1 {
		return dtype.Null, unsupportedColumn(c,
			"nested (struct or map) columns are not supported").
			Hint("column path is %q", c.Path())
	}
	return leafType(c)
}

// leafType maps a leaf's physical and logical type, and asks nothing about where
// it sits.
//
// The separation is not cosmetic. toDataType answers two questions at once — what
// type is this, and can ursus read it as a flat column — and inside a nested
// column the second one is meaningless: every leaf under a struct has a
// definition level above 1, and every leaf under a list has a repetition level
// above 0, because that is what being nested MEANS. Asking toDataType for the
// type of `user.city` therefore returned Null, and the schema named it
// `Struct(age: Int64, city: Null)` — the right shape with the inside erased.
func leafType(c *schema.Column) (dtype.DataType, error) {
	p := c.PhysicalType()
	switch lt := c.LogicalType().(type) {
	case schema.StringLogicalType:
		if p != parquet.Types.ByteArray {
			return dtype.Null, unsupportedColumn(c, "STRING on a %s column", p)
		}
		return dtype.String, nil

	case schema.IntLogicalType:
		return intType(c, lt)

	case schema.DecimalLogicalType:
		prec, scale := lt.Precision(), lt.Scale()
		// 38 digits is what fits in 128 bits, which is ursus's Decimal storage. A
		// wider decimal is legal Parquet and would silently truncate.
		if prec < 1 || prec > 38 || scale < 0 || int32(scale) > prec {
			return dtype.Null, unsupportedColumn(c,
				"DECIMAL(%d, %d) is outside the range ursus can store", prec, scale).
				Hint("ursus stores decimals in 128 bits, so precision must be 1..38")
		}
		// The STORAGE width matters as much as the precision. A writer may use a
		// wider FIXED_LEN_BYTE_ARRAY than the precision needs, and the reader gets
		// whatever width the schema declares. Refusing here means the file fails
		// when it is opened rather than partway through a query that has already
		// been planned.
		if p == parquet.Types.FixedLenByteArray && c.TypeLength() > 16 {
			return dtype.Null, unsupportedColumn(c,
				"DECIMAL is stored in %d bytes, which does not fit in 128 bits",
				c.TypeLength())
		}
		return dtype.Decimal(uint8(prec), uint8(scale)), nil

	case schema.DateLogicalType:
		return dtype.Date, nil

	case schema.TimeLogicalType:
		u, ok := ursusTimeUnit(lt.TimeUnit())
		if !ok {
			return dtype.Null, unsupportedColumn(c,
				"TIME has an unknown resolution")
		}
		return dtype.Time(u), nil

	case schema.TimestampLogicalType:
		u, ok := ursusTimeUnit(lt.TimeUnit())
		if !ok {
			return dtype.Null, unsupportedColumn(c,
				"TIMESTAMP has an unknown resolution")
		}
		// Parquet stores a BOOLEAN, not a zone name. So an instant written from any
		// named zone reads back labelled UTC — the instant is exact, the label is not
		// recoverable, and there is nowhere in the format to put it.
		if lt.IsAdjustedToUTC() {
			return dtype.Datetime(u, "UTC"), nil
		}
		return dtype.Datetime(u, ""), nil

	case schema.NoLogicalType, schema.NullLogicalType:
		return physicalOnly(c, p)

	default:
		// JSON, BSON, UUID, ENUM, INTERVAL, FLOAT16, VARIANT, LIST, MAP.
		return dtype.Null, unsupportedColumn(c,
			"logical type %s is not supported", c.LogicalType())
	}
}

// intType maps an INT annotation, which carries the width and signedness that the
// physical type does not.
func intType(c *schema.Column, lt schema.IntLogicalType) (dtype.DataType, error) {
	signed := lt.IsSigned()
	switch lt.BitWidth() {
	case 8:
		if signed {
			return dtype.Int8, nil
		}
		return dtype.Uint8, nil
	case 16:
		if signed {
			return dtype.Int16, nil
		}
		return dtype.Uint16, nil
	case 32:
		if signed {
			return dtype.Int32, nil
		}
		return dtype.Uint32, nil
	case 64:
		if signed {
			return dtype.Int64, nil
		}
		return dtype.Uint64, nil
	default:
		return dtype.Null, unsupportedColumn(c, "INT(%d) is not a Parquet width", lt.BitWidth())
	}
}

// physicalOnly maps a column with no logical annotation.
func physicalOnly(c *schema.Column, p parquet.Type) (dtype.DataType, error) {
	switch p {
	case parquet.Types.Boolean:
		return dtype.Bool, nil
	case parquet.Types.Int32:
		return dtype.Int32, nil
	case parquet.Types.Int64:
		return dtype.Int64, nil
	case parquet.Types.Float:
		return dtype.Float32, nil
	case parquet.Types.Double:
		return dtype.Float64, nil
	case parquet.Types.ByteArray:
		// An un-annotated BYTE_ARRAY is bytes, not text. Calling it String would
		// hand back invalid UTF-8 as a Go string.
		return dtype.Binary, nil
	case parquet.Types.FixedLenByteArray:
		return dtype.Binary, nil
	case parquet.Types.Int96:
		// INT96 is the deprecated nanosecond timestamp. It is readable in principle
		// but ursus has no Datetime support in this reader yet, and reading it as
		// three raw words would be worse than refusing.
		return dtype.Null, unsupportedColumn(c,
			"INT96 columns are not supported").
			Hint("INT96 is a deprecated timestamp encoding")
	default:
		return dtype.Null, unsupportedColumn(c, "physical type %s is not supported", p)
	}
}

func unsupportedColumn(c *schema.Column, format string, args ...any) *uerr.Error {
	return uerr.New(uerr.KindUnsupported, "scan_parquet", format, args...).
		Hint("column %q", c.Name())
}

// --- writing -------------------------------------------------------------------

// toNode maps an ursus field to a Parquet schema node.
//
// Every column is written OPTIONAL, regardless of the field's Nullable flag. A
// column written REQUIRED cannot hold a null ever again, and ursus's nullability
// is a property of the batch in hand rather than a guarantee about the data — a
// filter that happens to remove every null would otherwise write a file that
// rejects the same data tomorrow.
func toNode(f dtype.Field) (schema.Node, error) {
	rep := parquet.Repetitions.Optional
	name := f.Name

	switch f.Type.ID() {
	case dtype.TypeBool:
		return schema.NewPrimitiveNode(name, rep, parquet.Types.Boolean, -1, -1)
	case dtype.TypeFloat32:
		return schema.NewPrimitiveNode(name, rep, parquet.Types.Float, -1, -1)
	case dtype.TypeFloat64:
		return schema.NewPrimitiveNode(name, rep, parquet.Types.Double, -1, -1)
	case dtype.TypeString:
		return schema.NewPrimitiveNodeLogical(name, rep,
			schema.StringLogicalType{}, parquet.Types.ByteArray, -1, -1)
	case dtype.TypeBinary:
		return schema.NewPrimitiveNode(name, rep, parquet.Types.ByteArray, -1, -1)
	case dtype.TypeDate:
		return schema.NewPrimitiveNodeLogical(name, rep,
			schema.DateLogicalType{}, parquet.Types.Int32, -1, -1)

	case dtype.TypeInt8, dtype.TypeInt16, dtype.TypeInt32,
		dtype.TypeUint8, dtype.TypeUint16, dtype.TypeUint32:
		return schema.NewPrimitiveNodeLogical(name, rep,
			schema.NewIntLogicalType(int8(f.Type.BitWidth()), isSigned(f.Type)),
			parquet.Types.Int32, -1, -1)

	case dtype.TypeInt64, dtype.TypeUint64:
		return schema.NewPrimitiveNodeLogical(name, rep,
			schema.NewIntLogicalType(64, isSigned(f.Type)),
			parquet.Types.Int64, -1, -1)

	case dtype.TypeDecimal:
		// FIXED_LEN_BYTE_ARRAY(16) holds the full 128-bit range for any precision
		// ursus accepts, and avoids having to pick INT32 or INT64 by precision.
		return schema.NewPrimitiveNodeLogical(name, rep,
			schema.NewDecimalLogicalType(int32(f.Type.Precision()), int32(f.Type.Scale())),
			parquet.Types.FixedLenByteArray, 16, -1)

	case dtype.TypeInt128:
		// DECIMAL(38, 0) on FLBA(16) — a scale-zero decimal IS an integer, and it is
		// the only 128-bit shape Parquet has. The writer needs nothing new: its FLBA
		// arm reads data.TypedColumn[i128.Int128], which succeeds on an Int128 column
		// exactly as it does on a Decimal one.
		//
		// 38 is the widest precision DECIMAL allows in sixteen bytes, and Int128's
		// range is very slightly wider — ~1.7e38 against 1e38-1. A sum that large is
		// representable in the bytes and over-declared in the schema. Every reader in
		// practice ignores the declared precision; checking it on the hot path would
		// cost more than the case is worth. Read back, the column returns as
		// Decimal(38, 0) rather than Int128, because nothing in the file distinguishes
		// them — an asymmetry that is documented rather than hidden.
		return schema.NewPrimitiveNodeLogical(name, rep,
			schema.NewDecimalLogicalType(38, 0),
			parquet.Types.FixedLenByteArray, 16, -1)

	case dtype.TypeDatetime:
		u, ok := parquetTimeUnit(f.Type.TimeUnit())
		if !ok {
			return nil, secondUnitErr(f)
		}
		// isAdjustedToUTC is the ONLY zone information Parquet carries — there is no
		// slot for a zone name. So a named zone survives as its instant and comes back
		// labelled UTC; naive and UTC both round-trip exactly.
		return schema.NewPrimitiveNodeLogical(name, rep,
			schema.NewTimestampLogicalType(f.Type.TimeZone() != "", u),
			parquet.Types.Int64, -1, -1)

	case dtype.TypeTime:
		u, ok := parquetTimeUnit(f.Type.TimeUnit())
		if !ok {
			return nil, secondUnitErr(f)
		}
		// Parquet requires TIME(MILLIS) on INT32 and TIME(MICROS|NANOS) on INT64 —
		// the pairing is checked by the library and a mismatch is a construction
		// error, not a silent reinterpretation.
		phys := parquet.Types.Int64
		if u == schema.TimeUnitMillis {
			phys = parquet.Types.Int32
		}
		return schema.NewPrimitiveNodeLogical(name, rep,
			schema.NewTimeLogicalType(false, u), phys, -1, -1)

	default:
		// Duration stays refused ON PURPOSE rather than for lack of work: arrow-go's
		// IntervalLogicalType.toThrift panics outright, so there is no route through
		// this library at all. Enum and the nested types are simply not written yet.
		return nil, uerr.New(uerr.KindUnsupported, "sink_parquet",
			"cannot write column %q of type %s to Parquet", f.Name, f.Type).
			Hint("supported: Bool, the integer and float types, Int128, Decimal, " +
				"Date, Time, Datetime, String and Binary").
			Hint("Duration has no Parquet logical type; cast it to Int64 to store " +
				"the tick count")
	}
}

// parquetTimeUnit maps a ursus TimeUnit onto Parquet's three.
//
// Parquet has no SECOND resolution, and arrow-go's createTimeUnit does not return an
// error for one — it PANICS. dtype.Second is ursus's zero value, so
// `Datetime(Second, "")` is trivially constructible and would take the sink down.
// This is the guard; the refusal below it is what the user sees instead.
func parquetTimeUnit(u dtype.TimeUnit) (schema.TimeUnitType, bool) {
	switch u {
	case dtype.Milli:
		return schema.TimeUnitMillis, true
	case dtype.Micro:
		return schema.TimeUnitMicros, true
	case dtype.Nano:
		return schema.TimeUnitNanos, true
	default:
		return schema.TimeUnitUnknown, false
	}
}

func secondUnitErr(f dtype.Field) error {
	return uerr.New(uerr.KindUnsupported, "sink_parquet",
		"cannot write column %q of type %s: Parquet has no second resolution",
		f.Name, f.Type).
		Hint("Parquet stores TIME and TIMESTAMP at milli, micro or nanosecond " +
			"resolution only").
		Hint("cast to a finer unit first, e.g. .Cast(ursus.Datetime(ursus.Milli, tz))")
}

func isSigned(d dtype.DataType) bool { return d.IsSignedInteger() }

// ursusTimeUnit is parquetTimeUnit's inverse. Parquet has no second resolution, so
// the mapping is total in this direction for every unit a file can legally carry.
func ursusTimeUnit(u schema.TimeUnitType) (dtype.TimeUnit, bool) {
	switch u {
	case schema.TimeUnitMillis:
		return dtype.Milli, true
	case schema.TimeUnitMicros:
		return dtype.Micro, true
	case schema.TimeUnitNanos:
		return dtype.Nano, true
	default:
		return dtype.Second, false
	}
}

// --- nested schema mapping ------------------------------------------------------

// fieldType maps ONE top-level Parquet field to an ursus type.
//
// It returns the type it managed to name, the leaf column index when the field is
// a readable flat column, and — when it is not readable — the error explaining
// why, ready to be returned if a query ever asks for it.
//
// # Naming a type ursus cannot read is the point
//
// dtype already has List, Array and Struct: they were declared in step 1 and have
// never been produced by anything. Naming a nested column here costs little and
// buys two things. A user sees `tags: List(Int64)` in Schema() instead of an
// opaque refusal, so they know what is in their file. And the mapping is what any
// real nested support has to do first, so this is a down-payment rather than a
// placeholder.
//
// What it does NOT buy is the ability to read one. That is refused in Open, per
// column, and the message is the one toDataType has always produced.
func fieldType(sc *schema.Schema, n schema.Node) (dtype.DataType, int, error) {
	dt := nodeType(sc, n)

	// A LIST of fixed-width elements is readable: listCol owns the level machine,
	// and the leaf it reads from is the ELEMENT's, not the group's. Everything
	// else nested is still named and refused.
	if dt.ID() == dtype.TypeList {
		// Readable elements are the fixed-width types plus String and Binary. The
		// gate was IsFixedWidth alone, which excluded String — the most common
		// element type in real Parquet after the integers — because String has no
		// entry in bitWidths.
		//
		// The precise refusal for an element that still cannot be read comes from
		// newListElems, which knows which accumulators exist; this only has to
		// decide whether to hand the column a leaf.
		if leaf, ok := listElementLeaf(sc, n); ok && readableElem(dt.Inner()) {
			return dt, leaf, nil
		}
		return dt, -1, unsupportedNode(n, refusalFor(n))
	}

	// Otherwise readable means exactly one thing: a primitive, not repeated,
	// sitting directly under the root, whose physical type maps.
	if n.Type() == schema.Primitive && n.RepetitionType() != parquet.Repetitions.Repeated {
		if leaf := sc.ColumnIndexByNode(n); leaf >= 0 {
			c := sc.Column(leaf)
			if _, err := leafType(c); err != nil {
				return dt, -1, err // a physical type ursus has no mapping for
			}
			if c.MaxDefinitionLevel() <= 1 && c.MaxRepetitionLevel() == 0 {
				return dt, leaf, nil
			}
		}
	}
	return dt, -1, unsupportedNode(n, refusalFor(n))
}

// readableElem reports whether a List element type has an accumulator.
//
// It mirrors newListElems and must stay in step with it: this decides whether the
// column gets a leaf, and that one decides whether the leaf can be decoded.
func readableElem(elem dtype.DataType) bool {
	switch elem.ID() {
	case dtype.TypeString, dtype.TypeBinary:
		return true
	}
	return elem.IsFixedWidth()
}

// listElementLeaf finds the leaf column a LIST's elements live in.
//
// Only the three-level encoding is recognised: list -> repeated group -> element.
// A shape this does not match is still NAMED as a list by nodeType; it simply
// cannot be read, which is the same answer as before this step.
func listElementLeaf(sc *schema.Schema, n schema.Node) (int, bool) {
	g, ok := n.(*schema.GroupNode)
	if !ok || g.NumFields() != 1 {
		return -1, false
	}
	rep, ok := g.Field(0).(*schema.GroupNode)
	if !ok || rep.NumFields() != 1 {
		return -1, false
	}
	el := rep.Field(0)
	if el.Type() != schema.Primitive {
		return -1, false
	}
	leaf := sc.ColumnIndexByNode(el)
	return leaf, leaf >= 0
}

// refusalFor keeps the wording a user sees identical to what toDataType has
// always produced, so the diagnostic did not change — only when it appears.
func refusalFor(n schema.Node) string {
	if n.RepetitionType() == parquet.Repetitions.Repeated {
		return "repeated (list) columns are not supported"
	}
	if g, ok := n.(*schema.GroupNode); ok {
		switch g.LogicalType().(type) {
		case schema.ListLogicalType:
			return "repeated (list) columns are not supported"
		case schema.MapLogicalType:
			return "map columns are not supported"
		}
	}
	return "nested (struct or map) columns are not supported"
}

// nodeType names a node's type and makes no judgement about readability.
//
// Recursive, and total: a shape it cannot name becomes Null rather than an error,
// because failing to name the INSIDE of a column is no reason to hide the column
// itself — the read is refused either way, and a user is better served by
// `Struct(age: Int64, city: Null)` than by nothing at all.
func nodeType(sc *schema.Schema, n schema.Node) dtype.DataType {
	if n.Type() == schema.Primitive {
		leaf := sc.ColumnIndexByNode(n)
		if leaf < 0 {
			return dtype.Null
		}
		dt, err := leafType(sc.Column(leaf))
		if err != nil {
			return dtype.Null
		}
		// A repeated primitive is a list of scalars with no wrapper group — legal
		// Parquet. The test is the node's OWN repetition, not its max repetition
		// level: an element inside a LIST group inherits a level above zero from
		// its parent, and wrapping there would give List(List(T)).
		if n.RepetitionType() == parquet.Repetitions.Repeated {
			return dtype.List(dt)
		}
		return dt
	}

	g, ok := n.(*schema.GroupNode)
	if !ok {
		return dtype.Null
	}
	switch g.LogicalType().(type) {
	case schema.ListLogicalType:
		if el, ok := listElement(sc, g); ok {
			return dtype.List(el)
		}
		return dtype.List(dtype.Null)
	case schema.MapLogicalType:
		// Arrow represents a MAP as a list of key/value structs, and so does this.
		if kv, ok := mapEntry(sc, g); ok {
			return dtype.List(kv)
		}
		return dtype.List(dtype.Null)
	}
	return structType(sc, g)
}

// structType maps a group's fields, naming what it can.
//
// An inner field this cannot name becomes Null rather than aborting: failing to
// name the inside of a column is no reason to hide the column, and the read is
// refused either way.
func structType(sc *schema.Schema, g *schema.GroupNode) dtype.DataType {
	fields := make([]dtype.Field, g.NumFields())
	for i := range g.NumFields() {
		f := g.Field(i)
		fields[i] = dtype.Field{
			Name:     f.Name(),
			Type:     nodeType(sc, f),
			Nullable: f.RepetitionType() != parquet.Repetitions.Required,
		}
	}
	return dtype.Struct(fields...)
}

// listElement unwraps list -> repeated group -> element, returning the element
// type. ok is false for any other shape.
func listElement(sc *schema.Schema, g *schema.GroupNode) (dtype.DataType, bool) {
	if g.NumFields() != 1 {
		return dtype.Null, false
	}
	rep, ok := g.Field(0).(*schema.GroupNode)
	if !ok || rep.NumFields() != 1 {
		return dtype.Null, false
	}
	return nodeType(sc, rep.Field(0)), true
}

// mapEntry unwraps map -> repeated key_value -> {key, value} into a Struct.
func mapEntry(sc *schema.Schema, g *schema.GroupNode) (dtype.DataType, bool) {
	if g.NumFields() != 1 {
		return dtype.Null, false
	}
	kv, ok := g.Field(0).(*schema.GroupNode)
	if !ok || kv.NumFields() != 2 {
		return dtype.Null, false
	}
	return structType(sc, kv), true
}

// unsupportedNode is unsupportedColumn for a node that may not be a leaf, so it
// cannot take a *schema.Column. The message and hint match what a flat refusal
// has always produced.
func unsupportedNode(n schema.Node, msg string) error {
	return uerr.New(uerr.KindUnsupported, "scan_parquet", "%s", msg).
		Hint("column path is %q", n.Path())
}

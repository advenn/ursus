package parquet

import (
	"encoding/binary"
	"math"

	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/metadata"
	"github.com/apache/arrow-go/v18/parquet/schema"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// --- encodings ------------------------------------------------------------------

// allowedEncodings is the set arrow-go can actually decode.
//
// # This list prevents a PANIC, not an error
//
// arrow-go's typed_encoder.go ends in `panic("unimplemented encoding ...")` at
// eleven sites, and the panic is reachable from HasNext() inside ReadBatch. A
// BYTE_ARRAY column encoded with BYTE_STREAM_SPLIT is legal Parquet 2.11 and has
// no decoder in arrow-go, so a file someone else wrote can take down the process
// of anyone who reads it.
//
// Checking the column chunk's declared encodings before touching a page turns that
// into an error naming the column and the encoding. A panic in a library is never
// the right answer to a malformed or merely newer input file.
var allowedEncodings = map[parquet.Encoding]bool{
	parquet.Encodings.Plain:                true,
	parquet.Encodings.PlainDict:            true,
	parquet.Encodings.RLE:                  true,
	parquet.Encodings.RLEDict:              true,
	parquet.Encodings.DeltaBinaryPacked:    true,
	parquet.Encodings.DeltaByteArray:       true,
	parquet.Encodings.DeltaLengthByteArray: true,
	parquet.Encodings.ByteStreamSplit:      true, // decodable says on which types
}

func checkEncodings(rg *metadata.RowGroupMetaData, col int, name string) error {
	// arrow-go's ColumnChunk panics past the last column. A wrong layout reached it:
	// a later file with fewer columns, laid out as the first (audit.md §5, I1).
	if col < 0 || col >= rg.NumColumns() {
		return uerr.Internalf("scan_parquet: column %q maps to leaf %d, and the row "+
			"group has %d", name, col, rg.NumColumns())
	}
	cc, err := rg.ColumnChunk(col)
	if err != nil {
		return uerr.Wrap(err, uerr.KindIO, "scan_parquet",
			"reading metadata for column %q", name)
	}
	for _, e := range cc.Encodings() {
		if !allowedEncodings[e] {
			return uerr.New(uerr.KindUnsupported, "scan_parquet",
				"column %q uses the %s encoding, which cannot be decoded", name, e).
				Hint("this is checked up front because the decoder panics rather " +
					"than returning an error")
		}
		if !decodable(cc.Type(), e) {
			return uerr.New(uerr.KindUnsupported, "scan_parquet",
				"column %q uses the %s encoding on a %s column, which cannot be decoded",
				name, e, cc.Type()).
				Hint("this is checked up front because the decoder panics rather " +
					"than returning an error")
		}
	}
	return nil
}

// decodable is whether arrow-go v18.7.0 decodes encoding e on physical type t
// (parquet/internal/encoding/typed_encoder.go). Every other pairing reaches a panic.
//
// It used to check one pairing, BYTE_STREAM_SPLIT, and that one wrongly: the
// integers decode it. FIXED_LEN_BYTE_ARRAY under DELTA_BYTE_ARRAY, which
// parquet-mr's v2 writer produces, got through to the panic.
//
// RLE is allowed on every type because the levels are RLE-encoded, and a chunk's
// list of encodings does not say which pages used which.
func decodable(t parquet.Type, e parquet.Encoding) bool {
	switch e {
	case parquet.Encodings.Plain, parquet.Encodings.RLE:
		return true
	case parquet.Encodings.PlainDict, parquet.Encodings.RLEDict:
		return t != parquet.Types.Boolean
	}
	switch t {
	case parquet.Types.Int32, parquet.Types.Int64:
		return e == parquet.Encodings.DeltaBinaryPacked || e == parquet.Encodings.ByteStreamSplit
	case parquet.Types.Float, parquet.Types.Double, parquet.Types.FixedLenByteArray:
		return e == parquet.Encodings.ByteStreamSplit
	case parquet.Types.ByteArray:
		return e == parquet.Encodings.DeltaLengthByteArray || e == parquet.Encodings.DeltaByteArray
	}
	return false
}

// --- column reader construction --------------------------------------------------

// flbaAsBytes reads a FIXED_LEN_BYTE_ARRAY column through byteArrayCol: each
// value is bytes, of one length. The conversion is a slice header; byteArrayCol
// copies the bytes out, as it must for a buffer arrow-go reuses.
type flbaAsBytes struct {
	*file.FixedLenByteArrayColumnChunkReader
	buf []parquet.FixedLenByteArray
}

func (f *flbaAsBytes) ReadBatch(n int64, vals []parquet.ByteArray, defs, reps []int16) (int64, int, error) {
	if cap(f.buf) < len(vals) {
		f.buf = make([]parquet.FixedLenByteArray, len(vals))
	}
	buf := f.buf[:len(vals)]
	total, read, err := f.FixedLenByteArrayColumnChunkReader.ReadBatch(n, buf, defs, reps)
	for i := range read {
		vals[i] = parquet.ByteArray(buf[i])
	}
	return total, read, err
}

// julianUnixEpoch is 1970-01-01 as a Julian day number.
const julianUnixEpoch = 2_440_588

// int96Nanos reads Parquet's deprecated INT96 timestamp — eight little-endian bytes
// of nanoseconds into the day, then four of the Julian day — as nanoseconds since
// the Unix epoch. One that does not fit Datetime(ns), about 292 years either side of
// 1970, is refused rather than wrapped: raised, since a conversion here has no
// error to return, and the reader recovers it as the value error it is.
func int96Nanos(v parquet.Int96) int64 {
	nanos := binary.LittleEndian.Uint64(v[:8])
	day := int64(binary.LittleEndian.Uint32(v[8:])) - julianUnixEpoch
	const perDay = 86_400_000_000_000
	refuse := func(why string) {
		uerr.Raise(uerr.New(uerr.KindValue, "scan_parquet",
			"an INT96 timestamp (Julian day %d, %d ns into it) %s", day+julianUnixEpoch, nanos, why).
			Hint("INT96 is read as Datetime(ns), which spans 1677-09-21 to 2262-04-11"))
	}
	if nanos >= perDay {
		refuse("has more nanoseconds than a day")
	}
	if day > math.MaxInt64/perDay || day < math.MinInt64/perDay-1 {
		refuse("is outside Datetime(ns)")
	}
	// Before 1970 the instant is counted down from the END of its day, because the
	// day's start can be past what an int64 holds: day*perDay for 1677-09-21 is
	// below MinInt64 and wrapped to a value in 2262.
	if day >= 0 {
		at := day * perDay
		sum := at + int64(nanos)
		if sum < at {
			refuse("is outside Datetime(ns)")
		}
		return sum
	}
	end := (day + 1) * perDay
	sum := end - (perDay - int64(nanos))
	if sum > end {
		refuse("is outside Datetime(ns)")
	}
	return sum
}

// levelsOf reads only the levels of a column whose values are not wanted, through
// whichever typed reader it has: a NULL column, which PyArrow and ursus write on
// INT32, holds levels and no values at all.
func levelsOf(cr file.ColumnChunkReader) (func(defs, reps []int16) (int, error), bool) {
	switch t := cr.(type) {
	case *file.Int32ColumnChunkReader:
		return readLevels[int32](t), true
	case *file.Int64ColumnChunkReader:
		return readLevels[int64](t), true
	case *file.Int96ColumnChunkReader:
		return readLevels[parquet.Int96](t), true
	case *file.Float32ColumnChunkReader:
		return readLevels[float32](t), true
	case *file.Float64ColumnChunkReader:
		return readLevels[float64](t), true
	case *file.BooleanColumnChunkReader:
		return readLevels[bool](t), true
	case *file.ByteArrayColumnChunkReader:
		return readLevels[parquet.ByteArray](t), true
	case *file.FixedLenByteArrayColumnChunkReader:
		return readLevels[parquet.FixedLenByteArray](t), true
	}
	return nil, false
}

func readLevels[P any](cr batchReader[P]) func(defs, reps []int16) (int, error) {
	var vals []P
	return func(defs, reps []int16) (int, error) {
		if cap(vals) < len(defs) {
			vals = make([]P, len(defs))
		}
		total, _, err := cr.ReadBatch(int64(len(defs)), vals[:len(defs)], defs, reps)
		if err := batchErr(cr, total, err); err != nil {
			return 0, uerr.Wrap(err, uerr.KindIO, "scan_parquet", "reading a column chunk")
		}
		return int(total), nil
	}
}

// nullCol reads a column of Parquet's NULL logical type, which holds rows and no
// values, by its definition levels.
//
// It used to Skip the rows, which had two faults. Skip reports the count it was
// asked for, not the count there was, so a truncated chunk read as made-up rows.
// And a skipped row says nothing about whether a struct enclosing the column was
// present, so a struct whose first field was Null could not be read at all.
type nullCol struct {
	levels  func(defs, reps []int16) (int, error)
	closeCR func() error
	defs    []int16
	rows    int

	parentDef int16
	parent    *bitmap.Builder
}

func (c *nullCol) trackParent(level int16) {
	c.parentDef, c.parent = level, bitmap.NewBuilder(0)
}

func (c *nullCol) parentValidity() bitmap.View {
	v := c.parent.Finish()
	c.parent = bitmap.NewBuilder(0)
	return v
}

func (c *nullCol) read(n int) (int, error) {
	if cap(c.defs) < n {
		c.defs = make([]int16, n)
	}
	got, err := c.levels(c.defs[:n], nil)
	if err != nil {
		return 0, err
	}
	if c.parent != nil {
		for _, d := range c.defs[:got] {
			c.parent.Append(d >= c.parentDef)
		}
	}
	c.rows += got
	return got, nil
}

func (c *nullCol) finish(name string) *data.Column {
	col := data.NewNull(name, dtype.Null, c.rows)
	c.rows = 0
	return col
}

func (c *nullCol) close() error { return c.closeCR() }

func newColReader(cr file.ColumnChunkReader, desc *schema.Column, dt dtype.DataType) (colReader, error) {
	maxDef := desc.MaxDefinitionLevel()
	name := desc.Name()

	// A List is read by listCol, which owns the level machine; the element type
	// decides which instantiation. desc is the ELEMENT's leaf descriptor, so its
	// levels are the ones the machine needs.
	if dt.IsListLike() {
		return newListReader(cr, desc, dt)
	}
	if dt.ID() == dtype.TypeNull {
		levels, ok := levelsOf(cr)
		if !ok {
			return nil, uerr.Internalf("parquet: column %q of type Null has no level reader", name)
		}
		return &nullCol{levels: levels, closeCR: cr.Close}, nil
	}

	switch t := cr.(type) {
	case *file.BooleanColumnChunkReader:
		return &boolCol{cr: t, maxDef: maxDef,
			out: bitmap.NewBuilder(0), valid: bitmap.NewBuilder(0)}, nil

	case *file.ByteArrayColumnChunkReader:
		if dt.ID() == dtype.TypeDecimal {
			return nil, uerr.New(uerr.KindUnsupported, "scan_parquet",
				"column %q stores a DECIMAL in a variable-length BYTE_ARRAY", name).
				Hint("ursus reads DECIMAL from INT32, INT64 and FIXED_LEN_BYTE_ARRAY")
		}
		return &byteArrayCol{cr: t, maxDef: maxDef, dt: dt}, nil

	case *file.Int32ColumnChunkReader:
		if dt.ID() == dtype.TypeDecimal {
			return newFixed[int32, i128.Int128](t, maxDef, dt,
				func(v int32) i128.Int128 { return i128.FromInt64(int64(v)) }), nil
		}
		return int32Reader(t, maxDef, dt, name)

	case *file.Int64ColumnChunkReader:
		if dt.ID() == dtype.TypeDecimal {
			return newFixed[int64, i128.Int128](t, maxDef, dt, i128.FromInt64), nil
		}
		switch dt.ID() {
		case dtype.TypeInt64, dtype.TypeDatetime, dtype.TypeDuration:
			// A Datetime is an int64 tick count; the logical type in the file is what
			// says at which resolution. Nothing converts.
			return newFixed[int64, int64](t, maxDef, dt, identity[int64]), nil
		case dtype.TypeTime:
			// TIME(MICROS) and TIME(NANOS) are INT64; TIME(MILLIS) is INT32 and lands
			// in int32Reader instead.
			return newFixed[int64, int64](t, maxDef, dt, identity[int64]), nil
		case dtype.TypeUint64:
			return newFixed[int64, uint64](t, maxDef, dt,
				func(v int64) uint64 { return uint64(v) }), nil
		}

	case *file.Float32ColumnChunkReader:
		return newFixed[float32, float32](t, maxDef, dt, identity[float32]), nil

	case *file.Float64ColumnChunkReader:
		return newFixed[float64, float64](t, maxDef, dt, identity[float64]), nil

	case *file.Int96ColumnChunkReader:
		return newFixed[parquet.Int96, int64](t, maxDef, dt, int96Nanos), nil

	case *file.FixedLenByteArrayColumnChunkReader:
		if dt.ID() == dtype.TypeBinary {
			return &byteArrayCol{cr: &flbaAsBytes{FixedLenByteArrayColumnChunkReader: t},
				maxDef: maxDef, dt: dt}, nil
		}
		if dt.ID() != dtype.TypeDecimal {
			return nil, uerr.New(uerr.KindUnsupported, "scan_parquet",
				"column %q is a FIXED_LEN_BYTE_ARRAY that is not a DECIMAL", name)
		}
		if n := desc.TypeLength(); n > 16 {
			return nil, uerr.New(uerr.KindUnsupported, "scan_parquet",
				"column %q is a %d-byte DECIMAL, which does not fit in 128 bits", name, n)
		}
		// The length check above is what makes the conversion infallible, so the
		// hot loop needs no error branch.
		return newFixed[parquet.FixedLenByteArray, i128.Int128](t, maxDef, dt,
			func(v parquet.FixedLenByteArray) i128.Int128 {
				d, _ := decimalFromBytes(v)
				return d
			}), nil
	}

	return nil, uerr.New(uerr.KindUnsupported, "scan_parquet",
		"column %q (%s) has no reader", name, dt)
}

// int32Reader narrows a physical INT32 to whichever width the logical type says.
// Parquet stores Int8, Int16 and Int32 identically; only the annotation separates
// them, and reading all three as Int32 would change the schema the plan resolved.
func int32Reader(t *file.Int32ColumnChunkReader, maxDef int16, dt dtype.DataType, name string) (colReader, error) {
	switch dt.ID() {
	case dtype.TypeInt8:
		return newFixed[int32, int8](t, maxDef, dt, func(v int32) int8 { return int8(v) }), nil
	case dtype.TypeInt16:
		return newFixed[int32, int16](t, maxDef, dt, func(v int32) int16 { return int16(v) }), nil
	case dtype.TypeInt32, dtype.TypeDate:
		return newFixed[int32, int32](t, maxDef, dt, identity[int32]), nil
	case dtype.TypeTime:
		// TIME(MILLIS) is the one temporal type Parquet puts on INT32, and ursus
		// stores every Time as int64 ticks — so this widens where Date, whose ursus
		// storage is also 32 bits, does not.
		return newFixed[int32, int64](t, maxDef, dt,
			func(v int32) int64 { return int64(v) }), nil
	case dtype.TypeUint8:
		return newFixed[int32, uint8](t, maxDef, dt, func(v int32) uint8 { return uint8(v) }), nil
	case dtype.TypeUint16:
		return newFixed[int32, uint16](t, maxDef, dt, func(v int32) uint16 { return uint16(v) }), nil
	case dtype.TypeUint32:
		return newFixed[int32, uint32](t, maxDef, dt, func(v int32) uint32 { return uint32(v) }), nil
	default:
		return nil, uerr.New(uerr.KindUnsupported, "scan_parquet",
			"column %q is INT32 but mapped to %s", name, dt)
	}
}

func newFixed[P any, T data.Fixed](cr batchReader[P], maxDef int16, dt dtype.DataType, conv func(P) T) colReader {
	_, direct := any([]P(nil)).([]T)
	return &fixedCol[P, T]{cr: cr, maxDef: maxDef, dt: dt, conv: conv, direct: direct}
}

func identity[T any](v T) T { return v }

// newListReader builds the reader for a List column.
//
// The level machine is listCol and is element-agnostic; all that varies is the
// accumulator underneath it, so this picks one and hands it over.
func newListReader(cr file.ColumnChunkReader, desc *schema.Column, dt dtype.DataType) (colReader, error) {
	elems, err := newListElems(cr, desc, dt.Inner())
	if err != nil {
		return nil, err
	}
	// The element's own repetition decides where the empty list sits: an optional
	// element adds a level for "a null element", a required one does not.
	maxDef := desc.MaxDefinitionLevel()
	emptyDef, nullElem := maxDef-1, int16(-1)
	if desc.SchemaNode().RepetitionType() == parquet.Repetitions.Optional {
		emptyDef, nullElem = maxDef-2, maxDef-1
	}
	// Cross-check against the list group itself: it is present, and the list
	// empty, at exactly the number of non-required nodes from the root down to it.
	// element -> repeated "list" -> the list group.
	if group := desc.SchemaNode().Parent().Parent(); definedAt(group) != emptyDef {
		return nil, uerr.New(uerr.KindUnsupported, "scan_parquet",
			"column %q is a list whose definition levels do not match its shape "+
				"(an empty list at %d, the list present at %d)",
			desc.Path(), emptyDef, definedAt(group))
	}
	return &listCol{
		elems:    elems,
		maxDef:   maxDef,
		maxRep:   desc.MaxRepetitionLevel(),
		dt:       dt,
		emptyDef: emptyDef,
		nullElem: nullElem,
		valid:    bitmap.NewBuilder(0),
	}, nil
}

// definedAt is the definition level at which n is present: the number of
// non-required nodes from the root down to n, n included.
func definedAt(n schema.Node) int16 {
	var lvl int16
	for ; n != nil && n.Parent() != nil; n = n.Parent() {
		if n.RepetitionType() != parquet.Repetitions.Required {
			lvl++
		}
	}
	return lvl
}

// newListElems picks the accumulator for a List's element type.
//
// The refusals name what is missing rather than saying "not a list ursus can
// read": each remaining case is one more accumulator, and saying so is the
// difference between a gap and a wall.
func newListElems(cr file.ColumnChunkReader, desc *schema.Column,
	elem dtype.DataType) (listElems, error) {

	fixed := func() *bitmap.Builder { return bitmap.NewBuilder(0) }

	// List(Null), which PyArrow infers for a list that only ever holds None and
	// ursus writes since step 108: only levels, whatever the physical type.
	if elem.ID() == dtype.TypeNull {
		if levels, ok := levelsOf(cr); ok {
			return &nullElems{levels: levels, closeCR: cr.Close}, nil
		}
	}

	switch t := cr.(type) {
	case *file.ByteArrayColumnChunkReader:
		// String and Binary share the physical reader and differ only in the type
		// the child column is finished with.
		switch elem.ID() {
		case dtype.TypeString, dtype.TypeBinary:
			return &byteArrayElems{cr: t, valid: fixed()}, nil
		}

	case *file.Int32ColumnChunkReader:
		// Narrowed as int32Reader narrows a flat column. Int8 and Int16 were read
		// as int32 values under an Int8 or Int16 type, which data.NewFixed refuses
		// with a panic: a list of small integers from any other writer was
		// unreadable.
		switch elem.ID() {
		case dtype.TypeInt8:
			return &fixedElems[int32, int8]{cr: t, conv: func(v int32) int8 { return int8(v) }, valid: fixed()}, nil
		case dtype.TypeInt16:
			return &fixedElems[int32, int16]{cr: t, conv: func(v int32) int16 { return int16(v) }, valid: fixed()}, nil
		case dtype.TypeInt32, dtype.TypeDate:
			return &fixedElems[int32, int32]{cr: t, conv: identity[int32], valid: fixed()}, nil
		// The unsigned types and TIME(MILLIS) share INT32 storage, the unsigned ones
		// reinterpreting its bits as a flat column's reader does (audit.md I17: they
		// were refused, under a hint that said they were read).
		case dtype.TypeUint8:
			return &fixedElems[int32, uint8]{cr: t, conv: func(v int32) uint8 { return uint8(v) }, valid: fixed()}, nil
		case dtype.TypeUint16:
			return &fixedElems[int32, uint16]{cr: t, conv: func(v int32) uint16 { return uint16(v) }, valid: fixed()}, nil
		case dtype.TypeUint32:
			return &fixedElems[int32, uint32]{cr: t, conv: func(v int32) uint32 { return uint32(v) }, valid: fixed()}, nil
		case dtype.TypeTime:
			return &fixedElems[int32, int64]{cr: t, conv: func(v int32) int64 { return int64(v) }, valid: fixed()}, nil
		case dtype.TypeDecimal:
			return &fixedElems[int32, i128.Int128]{cr: t, conv: func(v int32) i128.Int128 { return i128.FromInt64(int64(v)) }, valid: fixed()}, nil
		}

	case *file.Int64ColumnChunkReader:
		switch elem.ID() {
		case dtype.TypeInt64, dtype.TypeDatetime, dtype.TypeDuration, dtype.TypeTime:
			return &fixedElems[int64, int64]{cr: t, conv: identity[int64], valid: fixed()}, nil
		case dtype.TypeUint64:
			return &fixedElems[int64, uint64]{cr: t, conv: func(v int64) uint64 { return uint64(v) }, valid: fixed()}, nil
		case dtype.TypeDecimal:
			return &fixedElems[int64, i128.Int128]{cr: t, conv: i128.FromInt64, valid: fixed()}, nil
		}

	case *file.FixedLenByteArrayColumnChunkReader:
		// DECIMAL, and Int128 written as DECIMAL(38, 0): big-endian two's complement.
		// A value has at most sixteen bytes in a FLBA(16), so decimalFromBytes cannot
		// fail here; a wider one would have been refused when the schema was mapped.
		if elem.ID() == dtype.TypeDecimal || elem.ID() == dtype.TypeInt128 {
			return &fixedElems[parquet.FixedLenByteArray, i128.Int128]{cr: t,
				conv: func(v parquet.FixedLenByteArray) i128.Int128 {
					d, _ := decimalFromBytes(v)
					return d
				}, valid: fixed()}, nil
		}

	case *file.BooleanColumnChunkReader:
		if elem.ID() == dtype.TypeBool {
			return &boolElems{cr: t, bits: fixed(), valid: fixed()}, nil
		}

	case *file.Float32ColumnChunkReader:
		if elem.ID() == dtype.TypeFloat32 {
			return &fixedElems[float32, float32]{cr: t, conv: identity[float32], valid: fixed()}, nil
		}

	case *file.Float64ColumnChunkReader:
		if elem.ID() == dtype.TypeFloat64 {
			return &fixedElems[float64, float64]{cr: t, conv: identity[float64], valid: fixed()}, nil
		}
	}
	return nil, uerr.New(uerr.KindUnsupported, "scan_parquet",
		"column %q is a list of %s, which cannot be read yet", desc.Name(), elem).
		Hint("lists of the integer, float, Bool, Decimal, temporal, String and Binary " +
			"types are read; nested elements are not")
}

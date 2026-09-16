// Package arrowout exports an ursus batch as Arrow, which is how anything outside
// this module reads ursus's output without a copy.
//
// # Why this is mostly free
//
// ursus already stores Arrow's memory layout. data.Column holds *memory.Buffer
// allocated through arrowx, validity is an LSB-numbered bitmap, strings are int32
// offsets plus a character buffer, lists are offsets plus a child. A data.Column IS
// an Arrow array with a different Go type wrapped around the same bytes, so the
// conversion is a re-wrap and the bytes never move.
//
// # Except at two seams, and both would be silent
//
//   - Decimal and Int128. i128.Int128 is {Hi int64, Lo uint64}; arrow's
//     decimal128.Num is {lo uint64, hi int64}. The widths match, the types look
//     compatible, and a zero-copy wrap would swap the high and low 64 bits of every
//     value — structurally valid and wrong by about 2^64. Every integer Sum outputs
//     Int128, so this is the common path, not a corner. The words are swapped here.
//
//   - Time at second or millisecond resolution. ursus stores every Time as int64
//     ticks; Arrow puts those two units on Time32 and only microseconds and
//     nanoseconds on Time64. So they narrow. Step 57's wrap is what makes the
//     narrowing total: a time of day is below 86_400_000 milliseconds, which fits
//     int32 with room to spare, so this cannot be the silently-wrapped int32 that
//     step 49 removed from rescaleTemporal and step 57 removed from the Parquet
//     writer.
//
// # The exported buffers are refcount-inert, deliberately
//
// arrow.Buffer.Release() at a zero refcount does `b.buf, b.length = nil, 0` — it
// nils the slice header INSIDE the shared Buffer. So a Buffer handed to a caller who
// releases it twice stops having data, and anything else pointing at that same
// Buffer stops having data with it. Silently: no panic, just an empty column.
//
// Every buffer here is therefore re-wrapped with memory.NewBufferBytes, which
// produces a Buffer with no allocator and no parent — and Release on one of those is
// unconditionally a no-op. The data is still shared, because NewBufferBytes does not
// copy; only the refcounting is severed. The exported record's lifetime is then
// completely independent of the frame's, in both directions, which is the model
// arrowx states for ursus's own memory: "ursus hides Arrow's Retain/Release
// refcounting completely."
//
// # What that guard is worth TODAY, stated exactly
//
// For the payload it is belt-and-braces, and saying otherwise would be the prose
// claim this project counts as its largest defect class. data.Column keeps its
// *memory.Buffer fields unexported and offers only RawFixed/RawOffsets/RawChars,
// which return []byte — so the payload path cannot hand out a live Buffer whether or
// not this package wraps. The guard matters the day someone adds the obvious
// accessor returning c.fixed directly, which is a change this file would otherwise
// not notice.
//
// The one path that yields a live Buffer at all is validity: bitmap.View.Buffer()
// allocates when the bitmap carries a non-zero bit offset, which is what a
// non-byte-aligned Column.Slice produces. Even there no ordinary sequence empties
// it — Record.Release is itself refcounted, so releasing a record twice does not
// release its buffers twice, and this package keeps a reference that stops the count
// reaching zero.
//
// So the wrapping prevents nothing that is reachable today. It is kept because it
// makes the contract a SENTENCE rather than a case analysis — "Release on an
// exported buffer is a no-op", true unconditionally and safe to promise in the
// public doc — and TestReleaseIsInert asserts that property directly rather than
// through a scenario that cannot happen.
package arrowout

import (
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/arrowx"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// Type maps an ursus type to its Arrow equivalent.
//
// It is total over dtype.TypeID: every declared id either has an arm or reaches the
// refusal, and TestEveryTypeIDIsMappedOrNamed walks the enum to prove no id falls
// through silently. A type added without an arm is a refusal, never a wrong mapping.
func Type(d dtype.DataType) (arrow.DataType, error) {
	switch d.ID() {
	case dtype.TypeNull:
		return arrow.Null, nil
	case dtype.TypeBool:
		return arrow.FixedWidthTypes.Boolean, nil

	case dtype.TypeInt8:
		return arrow.PrimitiveTypes.Int8, nil
	case dtype.TypeInt16:
		return arrow.PrimitiveTypes.Int16, nil
	case dtype.TypeInt32:
		return arrow.PrimitiveTypes.Int32, nil
	case dtype.TypeInt64:
		return arrow.PrimitiveTypes.Int64, nil

	case dtype.TypeUint8:
		return arrow.PrimitiveTypes.Uint8, nil
	case dtype.TypeUint16:
		return arrow.PrimitiveTypes.Uint16, nil
	case dtype.TypeUint32:
		return arrow.PrimitiveTypes.Uint32, nil
	case dtype.TypeUint64:
		return arrow.PrimitiveTypes.Uint64, nil

	case dtype.TypeFloat32:
		return arrow.PrimitiveTypes.Float32, nil
	case dtype.TypeFloat64:
		return arrow.PrimitiveTypes.Float64, nil

	case dtype.TypeString:
		return arrow.BinaryTypes.String, nil
	case dtype.TypeBinary:
		return arrow.BinaryTypes.Binary, nil

	case dtype.TypeDate:
		// Both are int32 days since the epoch. The one exact temporal match.
		return arrow.FixedWidthTypes.Date32, nil

	case dtype.TypeTime:
		u, err := timeUnit(d)
		if err != nil {
			return nil, err
		}
		switch u {
		case arrow.Second, arrow.Millisecond:
			return &arrow.Time32Type{Unit: u}, nil
		default:
			return &arrow.Time64Type{Unit: u}, nil
		}

	case dtype.TypeDatetime:
		u, err := timeUnit(d)
		if err != nil {
			return nil, err
		}
		return &arrow.TimestampType{Unit: u, TimeZone: d.TimeZone()}, nil

	case dtype.TypeDuration:
		u, err := timeUnit(d)
		if err != nil {
			return nil, err
		}
		return &arrow.DurationType{Unit: u}, nil

	case dtype.TypeDecimal:
		return &arrow.Decimal128Type{
			Precision: int32(d.Precision()), Scale: int32(d.Scale()),
		}, nil

	case dtype.TypeInt128:
		// Arrow has no 128-bit INTEGER type; Decimal128 with scale 0 is how a
		// 128-bit integer is spelled, and it is what every Arrow producer uses for
		// one. Precision 38 is Decimal128's maximum and covers ~1e38, a little short
		// of int128's 1.7e38 — the top and bottom of the range are outside the
		// declared precision while remaining exactly representable in the 128 bits
		// Arrow actually stores. Named here rather than discovered by a reader.
		return &arrow.Decimal128Type{Precision: 38, Scale: 0}, nil

	case dtype.TypeList:
		inner, err := Type(d.Inner())
		if err != nil {
			return nil, err
		}
		return arrow.ListOf(inner), nil

	case dtype.TypeStruct:
		fields := make([]arrow.Field, 0, len(d.Fields()))
		for _, f := range d.Fields() {
			ft, err := Type(f.Type)
			if err != nil {
				return nil, err
			}
			fields = append(fields, arrow.Field{
				Name: f.Name, Type: ft, Nullable: f.Nullable,
			})
		}
		return arrow.StructOf(fields...), nil
	}

	return nil, uerr.New(uerr.KindUnsupported, "arrow",
		"%s has no Arrow equivalent in ursus yet", d).
		Hint("Array, Enum, Categorical and Uint128 are declared in the type enum " +
			"but not yet exportable")
}

func timeUnit(d dtype.DataType) (arrow.TimeUnit, error) {
	switch d.TimeUnit() {
	case dtype.Second:
		return arrow.Second, nil
	case dtype.Milli:
		return arrow.Millisecond, nil
	case dtype.Micro:
		return arrow.Microsecond, nil
	case dtype.Nano:
		return arrow.Nanosecond, nil
	}
	// Unreachable through any constructor; a corrupt unit byte from a spill file is
	// the one way here, and it must not become a silent 10^9x scale error.
	return 0, uerr.Internalf("arrow: %s has an unknown time unit", d)
}

// Schema maps an ursus schema to an Arrow schema.
func Schema(s *dtype.Schema) (*arrow.Schema, error) {
	fields := make([]arrow.Field, 0, s.Len())
	for _, f := range s.All() {
		t, err := Type(f.Type)
		if err != nil {
			return nil, err
		}
		fields = append(fields, arrow.Field{
			Name: f.Name, Type: t, Nullable: f.Nullable,
		})
	}
	return arrow.NewSchema(fields, nil), nil
}

// Record exports a batch as an Arrow record, sharing ursus's memory.
//
// The caller may Release it or not; see the package doc. Nothing here owns anything
// the frame does not already own.
func Record(b *data.Batch) (arrow.Record, error) {
	sch, err := Schema(b.Schema())
	if err != nil {
		return nil, err
	}
	cols := make([]arrow.Array, 0, b.NumCols())
	for _, c := range b.Columns() {
		a, err := Column(c)
		if err != nil {
			for _, done := range cols {
				done.Release()
			}
			return nil, err
		}
		cols = append(cols, a)
	}
	rec := array.NewRecord(sch, cols, int64(b.Rows()))
	// NewRecord retains each array; this drops the local reference so the record is
	// the only owner, which is the idiom every arrow-go caller expects.
	for _, a := range cols {
		a.Release()
	}
	return rec, nil
}

// Column exports one column as an Arrow array.
func Column(c *data.Column) (arrow.Array, error) {
	d, err := columnData(c)
	if err != nil {
		return nil, err
	}
	a := array.MakeFromData(d)
	d.Release()
	return a, nil
}

func columnData(c *data.Column) (arrow.ArrayData, error) {
	at, err := Type(c.DType())
	if err != nil {
		return nil, err
	}
	n := c.Len()
	valid := share(c.Validity().Buffer())
	nulls := c.NullCount()

	switch c.DType().ID() {
	case dtype.TypeNull:
		// Arrow's Null array carries no buffers at all and every row is null.
		return array.NewData(at, n, []*memory.Buffer{nil}, nil, n, 0), nil

	case dtype.TypeBool:
		return array.NewData(at, n,
			[]*memory.Buffer{valid, share(c.Bools().Buffer())}, nil, nulls, 0), nil

	case dtype.TypeString, dtype.TypeBinary:
		// Offsets are NOT rebased by Column.Slice — they stay absolute into the whole
		// character buffer, which is exactly what Arrow reads. Arrow requires the
		// offsets to be monotonic, not to start at zero.
		return array.NewData(at, n,
			[]*memory.Buffer{valid, shareBytes(c.RawOffsets()), shareBytes(c.RawChars())},
			nil, nulls, 0), nil

	case dtype.TypeList:
		child, err := columnData(c.Child())
		if err != nil {
			return nil, err
		}
		d := array.NewData(at, n,
			[]*memory.Buffer{valid, shareBytes(c.RawOffsets())},
			[]arrow.ArrayData{child}, nulls, 0)
		child.Release()
		return d, nil

	case dtype.TypeStruct:
		kids := make([]arrow.ArrayData, 0, len(c.Fields()))
		for _, f := range c.Fields() {
			k, err := columnData(f)
			if err != nil {
				for _, done := range kids {
					done.Release()
				}
				return nil, err
			}
			kids = append(kids, k)
		}
		d := array.NewData(at, n, []*memory.Buffer{valid}, kids, nulls, 0)
		for _, k := range kids {
			k.Release()
		}
		return d, nil

	case dtype.TypeDecimal, dtype.TypeInt128:
		buf, err := swapWords(c, n)
		if err != nil {
			return nil, err
		}
		return array.NewData(at, n, []*memory.Buffer{valid, buf}, nil, nulls, 0), nil

	case dtype.TypeTime:
		if u, err := timeUnit(c.DType()); err == nil &&
			(u == arrow.Second || u == arrow.Millisecond) {
			buf, err := narrowToInt32(c, n)
			if err != nil {
				return nil, err
			}
			return array.NewData(at, n, []*memory.Buffer{valid, buf}, nil, nulls, 0), nil
		}
	}

	// Every remaining type is a fixed-width payload Arrow reads at the same width.
	return array.NewData(at, n,
		[]*memory.Buffer{valid, shareBytes(c.RawFixed())}, nil, nulls, 0), nil
}

// swapWords converts ursus's {Hi, Lo} 128-bit words into Arrow's {lo, hi}.
//
// This is the seam the package doc names. It allocates, because the bytes genuinely
// differ; there is no reinterpretation that makes these two layouts agree.
//
// The words are written explicitly rather than through decimal128.New, so the
// layout this depends on is stated here in full — low word first, then high — and
// does not silently follow a struct definition in another module.
func swapWords(c *data.Column, n int) (*memory.Buffer, error) {
	v, err := data.Values[i128.Int128](c)
	if err != nil {
		return nil, err
	}
	buf := arrowx.NewBuffer(n * 16)
	out := data.Reinterpret[uint64](buf.Bytes())[: n*2 : n*2]
	for i, x := range v {
		out[2*i] = x.Lo
		out[2*i+1] = uint64(x.Hi)
	}
	return share(buf), nil
}

// narrowToInt32 converts a second- or millisecond-resolution Time to Arrow's Time32.
//
// The narrowing is total rather than hopeful: step 57 wraps every Time into
// [0, 24h), so the largest value here is 86_399_999 milliseconds, which is a
// thousandth of int32's range. The check is kept anyway because a Time column that
// reached this without passing either producer — a corrupt spill file, a source
// written later — must not become a silently wrapped int32, which is the defect
// step 49 removed from rescaleTemporal and step 57 removed from the Parquet writer.
func narrowToInt32(c *data.Column, n int) (*memory.Buffer, error) {
	v, err := data.Values[int64](c)
	if err != nil {
		return nil, err
	}
	perDay, _ := dtype.TicksPerDay(c.DType())
	buf := arrowx.NewBuffer(n * 4)
	out := data.Reinterpret[int32](buf.Bytes())[:n]
	for i, t := range v {
		if c.IsValid(i) && (t < 0 || t >= perDay) {
			return nil, uerr.Internalf(
				"arrow: time %s at row %d is outside [0, 24h) and does not fit Arrow's "+
					"Time32", dtype.FormatTemporal(c.DType(), t), i)
		}
		out[i] = int32(t)
	}
	return share(buf), nil
}

// share re-wraps a buffer so that Release on it can never reach ursus's own memory.
// See the package doc: a Buffer with no allocator and no parent has an inert
// Release, and NewBufferBytes does not copy.
func share(b *memory.Buffer) *memory.Buffer {
	if b == nil {
		return nil
	}
	return memory.NewBufferBytes(b.Bytes())
}

func shareBytes(b []byte) *memory.Buffer {
	if b == nil {
		return nil
	}
	return memory.NewBufferBytes(b)
}

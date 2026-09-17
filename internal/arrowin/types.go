// Package arrowin imports Arrow data into ursus, by copying it.
//
// # Why a copy
//
// ursus stores Arrow's layout, so sharing the bytes looks free. It is not safe with
// today's types: bitmap.View, StringAccessor and ListAccessor hold raw []byte and
// cannot keep an owner alive; strings read through Get alias the character buffer
// and reach user code; and a record from an IPC reader is valid only until that
// reader's next Next, while C Data memory is freed through the producer's release
// callback. A column that outlived its record would read freed memory, silently. So
// every buffer is copied into ursus's own allocator, and nothing imported keeps any
// Arrow object alive.
//
// # Arrow is looser than ursus, in four places, and each would be silent
//
//   - A null list row may own a non-empty range of the child. ursus's list kernels
//     assume a null list owns nothing.
//   - A null struct row may hold valid field values. struct.field hands back the
//     field's own validity.
//   - A null slot may hold any bytes. Every ursus producer zeroes it.
//   - A record's columns may be longer than the record.
//
// Import normalises all four: a null row is empty, zeroed, and null in every field
// beneath it, which is the shape the Parquet reader and kernel.Take already produce.
// See builder.go.
package arrowin

import (
	"errors"
	"fmt"
	"time"

	"github.com/apache/arrow-go/v18/arrow"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/arrowx"
	"github.com/advenn/ursus/internal/uerr"
)

const op = "arrow"

// Schema maps an Arrow schema to an ursus one.
//
// Every field is NULLABLE, whatever Arrow declares. arrow.Field.Nullable's zero value
// is false, Go producers routinely leave it unset while writing nulls, and arrow-go
// never checks the claim — while ursus's optimizer acts on it, folding `x AND false`
// to `false` exactly when x is declared non-nullable. Trusting an unchecked false
// would turn a producer's omission into a wrong answer.
func Schema(s *arrow.Schema) (*dtype.Schema, error) {
	if s == nil {
		return nil, uerr.New(uerr.KindValue, op, "the Arrow schema is nil")
	}
	fields := make([]dtype.Field, 0, s.NumFields())
	for _, f := range s.Fields() {
		dt, err := Type(f.Type, f.Metadata)
		if err != nil {
			return nil, within(err, "column %q", f.Name)
		}
		fields = append(fields, dtype.Field{Name: f.Name, Type: dt, Nullable: true})
	}
	return dtype.NewSchema(fields...)
}

// Type maps an Arrow type to ursus. meta is the metadata of the FIELD holding it,
// which is where export marks an Int128 (see arrowx.FieldTypeKey).
//
// It is total over arrow.Type: every id either maps or is refused by name, and
// TestEveryArrowTypeIsMappedOrRefused walks the enum to prove it. An id this
// version of the switch has never seen — a newer arrow-go — is refused too, never
// guessed at.
func Type(at arrow.DataType, meta arrow.Metadata) (dtype.DataType, error) {
	if at == nil {
		return dtype.Null, uerr.New(uerr.KindValue, op, "the Arrow type is nil")
	}
	dt, err := typeOf(at)
	if err != nil {
		return dtype.Null, err
	}

	mark, marked := meta.GetValue(arrowx.FieldTypeKey)
	if !marked {
		return dt, nil
	}
	if mark != arrowx.FieldTypeInt128 {
		return dtype.Null, uerr.New(uerr.KindUnsupported, op,
			"field metadata %s=%q names a type this version of ursus does not know",
			arrowx.FieldTypeKey, mark)
	}
	// Honoured only on EXACTLY Decimal(38, 0), which is what export writes. On
	// Decimal128(10, 2) it would silently drop the scale and read 1.50 as 150.
	if dt != dtype.Decimal(38, 0) {
		return dtype.Null, uerr.New(uerr.KindValue, op,
			"field metadata %s=%q is on a %s, but an Int128 is exported as "+
				"decimal128(38, 0)", arrowx.FieldTypeKey, mark, at)
	}
	return dtype.Int128, nil
}

// typeOf maps an Arrow type without reading any field metadata.
func typeOf(at arrow.DataType) (dtype.DataType, error) {
	switch at.ID() {
	case arrow.NULL:
		return dtype.Null, nil
	case arrow.BOOL:
		return dtype.Bool, nil

	case arrow.INT8:
		return dtype.Int8, nil
	case arrow.INT16:
		return dtype.Int16, nil
	case arrow.INT32:
		return dtype.Int32, nil
	case arrow.INT64:
		return dtype.Int64, nil
	case arrow.UINT8:
		return dtype.Uint8, nil
	case arrow.UINT16:
		return dtype.Uint16, nil
	case arrow.UINT32:
		return dtype.Uint32, nil
	case arrow.UINT64:
		return dtype.Uint64, nil

	case arrow.FLOAT16:
		// ursus has no half float. Every float16 is exactly a float32, so widening
		// loses nothing.
		return dtype.Float32, nil
	case arrow.FLOAT32:
		return dtype.Float32, nil
	case arrow.FLOAT64:
		return dtype.Float64, nil

	case arrow.STRING, arrow.LARGE_STRING, arrow.STRING_VIEW:
		return dtype.String, nil
	case arrow.BINARY, arrow.LARGE_BINARY, arrow.BINARY_VIEW, arrow.FIXED_SIZE_BINARY:
		return dtype.Binary, nil

	case arrow.DATE32:
		return dtype.Date, nil
	case arrow.DATE64:
		// Milliseconds, and the spec says a whole number of days. The builder checks
		// that per row rather than trusting it.
		return dtype.Date, nil

	case arrow.TIMESTAMP:
		t, ok := at.(*arrow.TimestampType)
		if !ok {
			return dtype.Null, unexpected(at)
		}
		u, err := unit(t.Unit, at)
		if err != nil {
			return dtype.Null, err
		}
		if err := checkZone(t.TimeZone); err != nil {
			return dtype.Null, err
		}
		return dtype.Datetime(u, t.TimeZone), nil

	case arrow.TIME32:
		t, ok := at.(*arrow.Time32Type)
		if !ok {
			return dtype.Null, unexpected(at)
		}
		u, err := unit(t.Unit, at)
		if err != nil {
			return dtype.Null, err
		}
		return dtype.Time(u), nil
	case arrow.TIME64:
		t, ok := at.(*arrow.Time64Type)
		if !ok {
			return dtype.Null, unexpected(at)
		}
		u, err := unit(t.Unit, at)
		if err != nil {
			return dtype.Null, err
		}
		return dtype.Time(u), nil

	case arrow.DURATION:
		t, ok := at.(*arrow.DurationType)
		if !ok {
			return dtype.Null, unexpected(at)
		}
		u, err := unit(t.Unit, at)
		if err != nil {
			return dtype.Null, err
		}
		return dtype.Duration(u), nil

	case arrow.DECIMAL32, arrow.DECIMAL64, arrow.DECIMAL128:
		t, ok := at.(arrow.DecimalType)
		if !ok {
			return dtype.Null, unexpected(at)
		}
		// The Parquet reader's rule, and for the same reason: ursus stores 128 bits.
		// Arrow's scale is an int32 and may be negative, which a uint8 would wrap to
		// 254 — so the range is checked before the narrowing, not after.
		p, s := t.GetPrecision(), t.GetScale()
		if p < 1 || p > 38 || s < 0 || s > p {
			return dtype.Null, uerr.New(uerr.KindUnsupported, op,
				"%s is outside the range ursus can store", at).
				Hint("ursus stores decimals in 128 bits: precision 1..38, scale 0..precision")
		}
		return dtype.Decimal(uint8(p), uint8(s)), nil

	case arrow.LIST, arrow.LARGE_LIST, arrow.FIXED_SIZE_LIST,
		arrow.LIST_VIEW, arrow.LARGE_LIST_VIEW:
		t, ok := at.(arrow.ListLikeType)
		if !ok {
			return dtype.Null, unexpected(at)
		}
		ef := t.ElemField()
		inner, err := Type(ef.Type, ef.Metadata)
		if err != nil {
			return dtype.Null, err
		}
		return dtype.List(inner), nil

	case arrow.MAP:
		// A map is a list of key/value structs, which is exactly what it is on disk.
		t, ok := at.(*arrow.MapType)
		if !ok {
			return dtype.Null, unexpected(at)
		}
		entries, err := structOf([]arrow.Field{t.KeyField(), t.ItemField()}, at)
		if err != nil {
			return dtype.Null, err
		}
		return dtype.List(entries), nil

	case arrow.STRUCT:
		t, ok := at.(*arrow.StructType)
		if !ok {
			return dtype.Null, unexpected(at)
		}
		return structOf(t.Fields(), at)

	case arrow.DICTIONARY:
		// A dictionary is an encoding, not a type: the values are what a query reads.
		t, ok := at.(*arrow.DictionaryType)
		if !ok {
			return dtype.Null, unexpected(at)
		}
		return typeOf(t.ValueType)

	case arrow.EXTENSION:
		t, ok := at.(arrow.ExtensionType)
		if !ok {
			return dtype.Null, unexpected(at)
		}
		dt, err := typeOf(t.StorageType())
		if err != nil {
			return dtype.Null, within(err, "extension type %q stores %s",
				t.ExtensionName(), t.StorageType())
		}
		return dt, nil

	case arrow.DECIMAL256:
		return dtype.Null, refused(at, "ursus stores decimals in 128 bits")
	case arrow.INTERVAL_MONTHS, arrow.INTERVAL_DAY_TIME, arrow.INTERVAL_MONTH_DAY_NANO:
		return dtype.Null, refused(at, "ursus has no calendar interval type; "+
			"a duration is a fixed length of time")
	case arrow.SPARSE_UNION, arrow.DENSE_UNION:
		return dtype.Null, refused(at, "ursus has no union type")
	case arrow.RUN_END_ENCODED:
		return dtype.Null, refused(at, "decode it before handing it to ursus")
	}

	return dtype.Null, uerr.New(uerr.KindUnsupported, op,
		"Arrow type %s (id %d) is not known to this version of ursus", at, int(at.ID()))
}

// structOf maps a struct's fields. Every field is nullable, for the reason Schema
// gives, and because data.NewStruct forces it — so a declared non-nullable field
// would make the schema disagree with the column the builder produces.
func structOf(afs []arrow.Field, at arrow.DataType) (dtype.DataType, error) {
	// data.NewStruct takes its length from its first field. A struct with none would
	// report zero rows whatever its length, and as a frame's only column, an empty
	// frame.
	if len(afs) == 0 {
		return dtype.Null, uerr.New(uerr.KindUnsupported, op,
			"%s has no fields, and ursus cannot represent a struct without one", at)
	}
	fields := make([]dtype.Field, 0, len(afs))
	seen := make(map[string]bool, len(afs))
	for _, f := range afs {
		// Arrow allows duplicate names. ursus looks a field up by name and would
		// return the first one for both.
		if seen[f.Name] {
			return dtype.Null, uerr.New(uerr.KindUnsupported, op,
				"%s has two fields named %q, and ursus addresses fields by name", at, f.Name)
		}
		seen[f.Name] = true
		dt, err := Type(f.Type, f.Metadata)
		if err != nil {
			return dtype.Null, within(err, "field %q", f.Name)
		}
		fields = append(fields, dtype.Field{Name: f.Name, Type: dt, Nullable: true})
	}
	return dtype.Struct(fields...), nil
}

func unit(u arrow.TimeUnit, at arrow.DataType) (dtype.TimeUnit, error) {
	switch u {
	case arrow.Second:
		return dtype.Second, nil
	case arrow.Millisecond:
		return dtype.Milli, nil
	case arrow.Microsecond:
		return dtype.Micro, nil
	case arrow.Nanosecond:
		return dtype.Nano, nil
	}
	return 0, uerr.New(uerr.KindValue, op, "%s has an unknown time unit %d", at, int(u))
}

// checkZone refuses a zone ursus cannot load.
//
// Arrow allows a fixed offset such as "+09:00". time.LoadLocation does not, and every
// place ursus reads a zone — the .dt functions, temporal grouping, formatting — falls
// back to UTC when loading fails. So an accepted "+09:00" column would answer `hour`
// nine hours off for every row, with no error anywhere.
func checkZone(tz string) error {
	if tz == "" {
		return nil
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return uerr.New(uerr.KindUnsupported, op,
			"time zone %q cannot be loaded, and ursus would read it as UTC", tz).
			Hint("use an IANA name such as \"Asia/Tokyo\"; a fixed offset such as " +
				"\"+09:00\" is not a zone ursus can load")
	}
	return nil
}

// within names where an error happened while KEEPING its kind. A refusal of an
// unsupported type inside a struct is still KindUnsupported, not whatever the wrapper
// would have guessed; errors.Is walks causes, so a guessed outer kind would make
// both kinds match and a caller could not tell which one was the real one.
func within(err error, format string, args ...any) error {
	var e *uerr.Error
	if !errors.As(err, &e) {
		return err
	}
	out := *e
	out.Msg = fmt.Sprintf(format, args...) + ": " + e.Msg
	return &out
}

func refused(at arrow.DataType, why string) error {
	return uerr.New(uerr.KindUnsupported, op, "%s is not supported: %s", at, why)
}

// unexpected is an id whose Go type is not the one arrow-go pairs with it — a
// hand-built DataType, since arrow-go's own constructors never produce one.
func unexpected(at arrow.DataType) error {
	return uerr.New(uerr.KindValue, op,
		"Arrow type %s reports id %s but is a %T", at, at.ID(), at)
}

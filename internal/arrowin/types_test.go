package arrowin_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/extensions"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/arrowin"
	"github.com/advenn/ursus/internal/arrowx"
	"github.com/advenn/ursus/internal/uerr"
)

var i64 = arrow.PrimitiveTypes.Int64

// typeCase is one sample Arrow type per id, and the ursus type it must become — or
// the zero DataType with refused set.
type typeCase struct {
	at      arrow.DataType
	want    dtype.DataType
	refused bool
}

// sampleArrowType is a switch without a default, so an id with no case returns
// ok=false and the walk below reports it, rather than skipping it silently.
func sampleArrowType(id arrow.Type) (typeCase, bool) {
	no := func(at arrow.DataType) (typeCase, bool) { return typeCase{at: at, refused: true}, true }
	to := func(at arrow.DataType, dt dtype.DataType) (typeCase, bool) {
		return typeCase{at: at, want: dt}, true
	}
	switch id {
	case arrow.NULL:
		return to(arrow.Null, dtype.Null)
	case arrow.BOOL:
		return to(arrow.FixedWidthTypes.Boolean, dtype.Bool)
	case arrow.UINT8:
		return to(arrow.PrimitiveTypes.Uint8, dtype.Uint8)
	case arrow.INT8:
		return to(arrow.PrimitiveTypes.Int8, dtype.Int8)
	case arrow.UINT16:
		return to(arrow.PrimitiveTypes.Uint16, dtype.Uint16)
	case arrow.INT16:
		return to(arrow.PrimitiveTypes.Int16, dtype.Int16)
	case arrow.UINT32:
		return to(arrow.PrimitiveTypes.Uint32, dtype.Uint32)
	case arrow.INT32:
		return to(arrow.PrimitiveTypes.Int32, dtype.Int32)
	case arrow.UINT64:
		return to(arrow.PrimitiveTypes.Uint64, dtype.Uint64)
	case arrow.INT64:
		return to(i64, dtype.Int64)
	case arrow.FLOAT16:
		return to(arrow.FixedWidthTypes.Float16, dtype.Float32)
	case arrow.FLOAT32:
		return to(arrow.PrimitiveTypes.Float32, dtype.Float32)
	case arrow.FLOAT64:
		return to(arrow.PrimitiveTypes.Float64, dtype.Float64)
	case arrow.STRING:
		return to(arrow.BinaryTypes.String, dtype.String)
	case arrow.BINARY:
		return to(arrow.BinaryTypes.Binary, dtype.Binary)
	case arrow.FIXED_SIZE_BINARY:
		return to(&arrow.FixedSizeBinaryType{ByteWidth: 4}, dtype.Binary)
	case arrow.DATE32:
		return to(arrow.FixedWidthTypes.Date32, dtype.Date)
	case arrow.DATE64:
		return to(arrow.FixedWidthTypes.Date64, dtype.Date)
	case arrow.TIMESTAMP:
		return to(&arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: "Asia/Tokyo"},
			dtype.Datetime(dtype.Micro, "Asia/Tokyo"))
	case arrow.TIME32:
		return to(arrow.FixedWidthTypes.Time32ms, dtype.Time(dtype.Milli))
	case arrow.TIME64:
		return to(arrow.FixedWidthTypes.Time64ns, dtype.Time(dtype.Nano))
	case arrow.INTERVAL_MONTHS:
		return no(arrow.FixedWidthTypes.MonthInterval)
	case arrow.INTERVAL_DAY_TIME:
		return no(arrow.FixedWidthTypes.DayTimeInterval)
	case arrow.DECIMAL128:
		return to(&arrow.Decimal128Type{Precision: 10, Scale: 2}, dtype.Decimal(10, 2))
	case arrow.DECIMAL256:
		return no(&arrow.Decimal256Type{Precision: 40, Scale: 2})
	case arrow.LIST:
		return to(arrow.ListOf(i64), dtype.List(dtype.Int64))
	case arrow.STRUCT:
		return to(arrow.StructOf(arrow.Field{Name: "a", Type: i64}),
			dtype.Struct(dtype.Field{Name: "a", Type: dtype.Int64, Nullable: true}))
	case arrow.SPARSE_UNION:
		return no(arrow.SparseUnionOf([]arrow.Field{{Name: "a", Type: i64}}, []arrow.UnionTypeCode{0}))
	case arrow.DENSE_UNION:
		return no(arrow.DenseUnionOf([]arrow.Field{{Name: "a", Type: i64}}, []arrow.UnionTypeCode{0}))
	case arrow.DICTIONARY:
		return to(&arrow.DictionaryType{IndexType: arrow.PrimitiveTypes.Int8,
			ValueType: arrow.BinaryTypes.String}, dtype.String)
	case arrow.MAP:
		return to(arrow.MapOf(arrow.BinaryTypes.String, i64), dtype.List(dtype.Struct(
			dtype.Field{Name: "key", Type: dtype.String, Nullable: true},
			dtype.Field{Name: "value", Type: dtype.Int64, Nullable: true},
		)))
	case arrow.EXTENSION:
		return to(extensions.NewUUIDType(), dtype.Binary)
	case arrow.FIXED_SIZE_LIST:
		return to(arrow.FixedSizeListOf(2, i64), dtype.List(dtype.Int64))
	case arrow.DURATION:
		return to(arrow.FixedWidthTypes.Duration_s, dtype.Duration(dtype.Second))
	case arrow.LARGE_STRING:
		return to(arrow.BinaryTypes.LargeString, dtype.String)
	case arrow.LARGE_BINARY:
		return to(arrow.BinaryTypes.LargeBinary, dtype.Binary)
	case arrow.LARGE_LIST:
		return to(arrow.LargeListOf(i64), dtype.List(dtype.Int64))
	case arrow.INTERVAL_MONTH_DAY_NANO:
		return no(arrow.FixedWidthTypes.MonthDayNanoInterval)
	case arrow.RUN_END_ENCODED:
		return no(arrow.RunEndEncodedOf(arrow.PrimitiveTypes.Int32, i64))
	case arrow.STRING_VIEW:
		return to(arrow.BinaryTypes.StringView, dtype.String)
	case arrow.BINARY_VIEW:
		return to(arrow.BinaryTypes.BinaryView, dtype.Binary)
	case arrow.LIST_VIEW:
		return to(arrow.ListViewOf(i64), dtype.List(dtype.Int64))
	case arrow.LARGE_LIST_VIEW:
		return to(arrow.LargeListViewOf(i64), dtype.List(dtype.Int64))
	case arrow.DECIMAL32:
		return to(&arrow.Decimal32Type{Precision: 9, Scale: 2}, dtype.Decimal(9, 2))
	case arrow.DECIMAL64:
		return to(&arrow.Decimal64Type{Precision: 18, Scale: 3}, dtype.Decimal(18, 3))
	}
	return typeCase{}, false
}

// TestEveryArrowTypeIsMappedOrRefused walks arrow.Type until the stringer stops
// naming ids, so the walk's bound comes from arrow-go rather than from this file.
func TestEveryArrowTypeIsMappedOrRefused(t *testing.T) {
	var mapped, refused int
	id := arrow.Type(0)
	for ; !strings.HasPrefix(id.String(), "Type("); id++ {
		c, ok := sampleArrowType(id)
		if !ok {
			t.Errorf("arrow type %s has no sample, so nothing proves it maps or is refused", id)
			continue
		}
		if c.at.ID() != id {
			t.Errorf("the sample for %s is a %s", id, c.at.ID())
			continue
		}
		got, err := arrowin.Type(c.at, arrow.Metadata{})
		if c.refused {
			refused++
			if !errors.Is(err, uerr.ErrUnsupported) {
				t.Errorf("%s: want an unsupported refusal, got %v, %v", c.at, got, err)
			} else if !strings.Contains(err.Error(), c.at.String()) {
				t.Errorf("%s: the refusal does not name the type: %v", c.at, err)
			}
			continue
		}
		mapped++
		if err != nil {
			t.Errorf("%s: %v", c.at, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s maps to %s, want %s", c.at, got, c.want)
		}
	}

	if mapped != 38 || refused != 7 {
		t.Errorf("%d ids mapped and %d refused, want 38 and 7", mapped, refused)
	}
	// If arrow-go adds a type, the walk above covers it only once it has a sample —
	// so the enum's end is pinned, and an upgrade that moves it fails here by name.
	if id != 45 {
		t.Errorf("arrow.Type now ends at %d, not 45 — a type was added; give it a sample "+
			"and a decision", id)
	}
}

// TestAnUnknownIDIsRefused: an id past the end is what a newer arrow-go would hand
// an ursus built against this one.
func TestAnUnknownIDIsRefused(t *testing.T) {
	_, err := arrowin.Type(&unknownType{}, arrow.Metadata{})
	if !errors.Is(err, uerr.ErrUnsupported) {
		t.Fatalf("an unknown id: %v", err)
	}
}

type unknownType struct{ arrow.NullType }

func (*unknownType) ID() arrow.Type { return 200 }

func TestTypeRefusals(t *testing.T) {
	for _, c := range []struct {
		name string
		at   arrow.DataType
		meta arrow.Metadata
		kind error
		says string
	}{
		{
			name: "a fixed-offset zone, which every reader would treat as UTC",
			at:   &arrow.TimestampType{Unit: arrow.Second, TimeZone: "+09:00"},
			kind: uerr.ErrUnsupported, says: `"+09:00"`,
		},
		{
			name: "a negative scale, which would wrap to 254",
			at:   &arrow.Decimal128Type{Precision: 10, Scale: -2},
			kind: uerr.ErrUnsupported, says: "decimal(10, -2)",
		},
		{
			name: "a scale above the precision",
			at:   &arrow.Decimal128Type{Precision: 5, Scale: 6},
			kind: uerr.ErrUnsupported, says: "decimal(5, 6)",
		},
		{
			name: "a precision above 38",
			at:   &arrow.Decimal128Type{Precision: 39, Scale: 0},
			kind: uerr.ErrUnsupported, says: "decimal(39, 0)",
		},
		{
			name: "a struct with no fields",
			at:   arrow.StructOf(),
			kind: uerr.ErrUnsupported, says: "no fields",
		},
		{
			name: "duplicate field names",
			at:   arrow.StructOf(arrow.Field{Name: "a", Type: i64}, arrow.Field{Name: "a", Type: i64}),
			kind: uerr.ErrUnsupported, says: `two fields named "a"`,
		},
		{
			name: "a refused type inside a struct keeps its kind and says where",
			at: arrow.StructOf(arrow.Field{Name: "iv",
				Type: arrow.FixedWidthTypes.MonthInterval}),
			kind: uerr.ErrUnsupported, says: `field "iv"`,
		},
		{
			name: "a refused type inside a list",
			at:   arrow.ListOf(&arrow.Decimal256Type{Precision: 40, Scale: 0}),
			kind: uerr.ErrUnsupported, says: "decimal256",
		},
		{
			name: "an extension whose storage is refused names both",
			at: extensions.NewOpaqueType(&arrow.Decimal256Type{Precision: 40, Scale: 0},
				"wide", "elsewhere"),
			kind: uerr.ErrUnsupported, says: `extension type "arrow.opaque" stores decimal256`,
		},
		{
			name: "the Int128 mark on a decimal with a scale",
			at:   &arrow.Decimal128Type{Precision: 10, Scale: 2},
			meta: int128Mark(),
			kind: uerr.ErrValue, says: "decimal128(38, 0)",
		},
		{
			name: "the Int128 mark on an integer",
			at:   i64,
			meta: int128Mark(),
			kind: uerr.ErrValue, says: arrowx.FieldTypeKey,
		},
		{
			name: "an ursus type name this version does not know",
			at:   &arrow.Decimal128Type{Precision: 38, Scale: 0},
			meta: arrow.NewMetadata([]string{arrowx.FieldTypeKey}, []string{"Uint128"}),
			kind: uerr.ErrUnsupported, says: `"Uint128"`,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := arrowin.Type(c.at, c.meta)
			if err == nil {
				t.Fatalf("%s was accepted as %s", c.at, got)
			}
			var ue *uerr.Error
			if !errors.As(err, &ue) || !errors.Is(&uerr.Error{Kind: ue.Kind}, c.kind) {
				t.Errorf("the top-level kind is wrong: %v", err)
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("the error should say %q: %v", c.says, err)
			}
		})
	}
}

func int128Mark() arrow.Metadata {
	return arrow.NewMetadata([]string{arrowx.FieldTypeKey}, []string{arrowx.FieldTypeInt128})
}

// TestTheInt128MarkIsHonouredExactly: on Decimal(38, 0), at the top, inside a list,
// inside a struct, and through a dictionary; and a Decimal(38, 0) without the mark
// stays a Decimal.
func TestTheInt128MarkIsHonouredExactly(t *testing.T) {
	dec := &arrow.Decimal128Type{Precision: 38, Scale: 0}
	marked := arrow.Field{Name: "item", Type: dec, Nullable: true, Metadata: int128Mark()}

	for _, c := range []struct {
		name string
		at   arrow.DataType
		meta arrow.Metadata
		want dtype.DataType
	}{
		{"marked", dec, int128Mark(), dtype.Int128},
		{"unmarked", dec, arrow.Metadata{}, dtype.Decimal(38, 0)},
		{"marked, dictionary-encoded",
			&arrow.DictionaryType{IndexType: arrow.PrimitiveTypes.Int8, ValueType: dec},
			int128Mark(), dtype.Int128},
		{"a list element", arrow.ListOfField(marked), arrow.Metadata{}, dtype.List(dtype.Int128)},
		{"a struct child", arrow.StructOf(arrow.Field{Name: "s", Type: dec, Metadata: int128Mark()}),
			arrow.Metadata{}, dtype.Struct(dtype.Field{Name: "s", Type: dtype.Int128, Nullable: true})},
	} {
		got, err := arrowin.Type(c.at, c.meta)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestSchemaIsAlwaysNullable(t *testing.T) {
	s := arrow.NewSchema([]arrow.Field{
		{Name: "a", Type: i64, Nullable: false},
		{Name: "s", Type: arrow.StructOf(arrow.Field{Name: "x", Type: i64, Nullable: false})},
	}, nil)
	got, err := arrowin.Schema(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range got.All() {
		if !f.Nullable {
			t.Errorf("column %q is declared non-nullable; Arrow's flag is unchecked and "+
				"its zero value is false", f.Name)
		}
	}
	st, _ := got.ByName("s")
	for _, f := range st.Type.Fields() {
		if !f.Nullable {
			t.Errorf("struct field %q is declared non-nullable", f.Name)
		}
	}
}

// TestSchemaNamesTheColumn: a refusal deep in a schema says which column.
func TestSchemaNamesTheColumn(t *testing.T) {
	s := arrow.NewSchema([]arrow.Field{
		{Name: "ok", Type: i64},
		{Name: "bad", Type: arrow.FixedWidthTypes.DayTimeInterval},
	}, nil)
	_, err := arrowin.Schema(s)
	if !errors.Is(err, uerr.ErrUnsupported) || !strings.Contains(err.Error(), `column "bad"`) {
		t.Fatalf("got %v", err)
	}
}

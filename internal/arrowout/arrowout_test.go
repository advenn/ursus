package arrowout_test

// The Arrow export, checked on VALUES rather than on types.
//
// Two of the mappings here would be wrong in a way no type assertion can see. A
// 128-bit column exported with the words in ursus's order is a valid Decimal128
// array holding numbers wrong by about 2^64, and a second-resolution Time exported
// at the wrong width is a valid Time32 array holding a truncated clock. Both are
// structurally perfect and numerically nonsense, so every assertion below reads the
// value back out.

import (
	"errors"
	"strings"
	"testing"
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/arrowout"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

const rows = 3

func allSet() bitmap.View { return bitmap.AllSet(rows) }

// sampleType is one representative DataType per TypeID.
//
// It is a map rather than a switch with a default so that a TypeID with no entry is
// a FAILURE of the coverage test below rather than a silent skip — the same
// discipline castagree_test.go uses, and for the same reason.
func sampleType(id dtype.TypeID) (dtype.DataType, bool) {
	switch id {
	case dtype.TypeNull:
		return dtype.Null, true
	case dtype.TypeBool:
		return dtype.Bool, true
	case dtype.TypeInt8:
		return dtype.Int8, true
	case dtype.TypeInt16:
		return dtype.Int16, true
	case dtype.TypeInt32:
		return dtype.Int32, true
	case dtype.TypeInt64:
		return dtype.Int64, true
	case dtype.TypeUint8:
		return dtype.Uint8, true
	case dtype.TypeUint16:
		return dtype.Uint16, true
	case dtype.TypeUint32:
		return dtype.Uint32, true
	case dtype.TypeUint64:
		return dtype.Uint64, true
	case dtype.TypeFloat32:
		return dtype.Float32, true
	case dtype.TypeFloat64:
		return dtype.Float64, true
	case dtype.TypeString:
		return dtype.String, true
	case dtype.TypeBinary:
		return dtype.Binary, true
	case dtype.TypeDate:
		return dtype.Date, true
	case dtype.TypeTime:
		return dtype.Time(dtype.Nano), true
	case dtype.TypeDatetime:
		return dtype.Datetime(dtype.Micro, "UTC"), true
	case dtype.TypeDuration:
		return dtype.Duration(dtype.Second), true
	case dtype.TypeDecimal:
		return dtype.Decimal(10, 2), true
	case dtype.TypeInt128:
		return dtype.Int128, true
	case dtype.TypeList:
		return dtype.List(dtype.Int64), true
	case dtype.TypeStruct:
		return dtype.Struct(dtype.Of("f", dtype.Int64)), true
	case dtype.TypeArray:
		return dtype.Array(dtype.Int64, 2), true
	case dtype.TypeEnum:
		return dtype.Enum("a", "b"), true
	case dtype.TypeCategorical, dtype.TypeUint128:
		// Reserved in the enum and not constructible, so there is nothing to sample.
		return dtype.DataType{}, false
	}
	return dtype.DataType{}, false
}

// notExportable is every TypeID Type() is expected to refuse, by name.
//
// The list is the point: a type added to the enum without an Arrow arm lands here
// as an unexpected refusal and fails, rather than quietly joining the set of things
// that do not work.
var notExportable = map[dtype.TypeID]bool{
	dtype.TypeArray:       true,
	dtype.TypeEnum:        true,
	dtype.TypeCategorical: true,
	dtype.TypeUint128:     true,
}

// TestEveryTypeIDIsMappedOrNamed walks the whole enum.
//
// Nothing may fall through silently: an id either has an Arrow type or is refused
// with a user-facing error that names it. The refused SET is asserted too, so the
// day Enum becomes constructible this test says so rather than continuing to pass.
func TestEveryTypeIDIsMappedOrNamed(t *testing.T) {
	var mapped, refused int
	for i := range int(dtype.TypeIDCount) {
		id := dtype.TypeID(i)
		d, ok := sampleType(id)
		if !ok {
			if !notExportable[id] {
				t.Errorf("TypeID %d (%s) has no sample and is not listed as "+
					"unexportable — add one, or say why it cannot be exported",
					i, id)
			}
			continue
		}

		at, err := arrowout.Type(d)
		switch {
		case err == nil && notExportable[id]:
			t.Errorf("%s exports as %s but is listed as unexportable — delete the "+
				"entry", d, at)
		case err == nil:
			mapped++
			if at == nil {
				t.Errorf("%s mapped to a nil Arrow type with no error", d)
			}
		case notExportable[id]:
			refused++
			// A user asked for something ursus cannot do yet; that is not a bug.
			if errors.Is(err, uerr.ErrInternal) {
				t.Errorf("%s is refused as an ursus bug: %v", d, err)
			}
			if !strings.Contains(err.Error(), d.String()) {
				t.Errorf("the refusal should name the type, got: %v", err)
			}
		default:
			t.Errorf("%s has a sample and no Arrow type: %v", d, err)
		}
	}

	if mapped < 20 {
		t.Errorf("only %d types mapped; the enum declares more", mapped)
	}
	if refused == 0 {
		t.Error("nothing was refused, so the refusal path is untested")
	}
}

// --- the two seams -----------------------------------------------------------

// TestInt128KeepsItsValue is the one that a zero-copy wrap gets wrong.
//
// i128.Int128 is {Hi, Lo}; arrow's decimal128.Num is {lo, hi}. Reinterpreting the
// bytes produces a valid Decimal128 array in which every value has its two halves
// swapped — for a small positive number that means a colossal one, and for a
// negative number it means a positive.
//
// Every integer Sum outputs Int128, so this is the ordinary group-by path.
func TestInt128KeepsItsValue(t *testing.T) {
	vals := []i128.Int128{
		i128.FromInt64(1),
		i128.FromInt64(-1),
		{Hi: 5, Lo: 7}, // both words non-zero, so a swap cannot look right
	}
	c := data.NewFixed("c", dtype.Int128, vals, allSet())

	a, err := arrowout.Column(c)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()

	dec, ok := a.(*array.Decimal128)
	if !ok {
		t.Fatalf("Int128 exported as %T, want *array.Decimal128", a)
	}
	for i, want := range vals {
		got := dec.Value(i)
		if got.LowBits() != want.Lo || got.HighBits() != want.Hi {
			t.Errorf("row %d: got {hi:%d lo:%d}, want {hi:%d lo:%d} — the 64-bit "+
				"halves are swapped", i, got.HighBits(), got.LowBits(), want.Hi, want.Lo)
		}
	}
}

// TestTimeNarrowsByUnit pins which Arrow width each Time resolution takes, and that
// the value survives it. Arrow puts seconds and milliseconds on Time32 and
// microseconds and nanoseconds on Time64; ursus stores all four as int64.
func TestTimeNarrowsByUnit(t *testing.T) {
	const noon = int64(12 * 3600) // seconds; scaled per unit below

	for _, c := range []struct {
		unit   dtype.TimeUnit
		perSec int64
		is32   bool
	}{
		{dtype.Second, 1, true},
		{dtype.Milli, 1_000, true},
		{dtype.Micro, 1_000_000, false},
		{dtype.Nano, 1_000_000_000, false},
	} {
		t.Run(c.unit.String(), func(t *testing.T) {
			dt := dtype.Time(c.unit)
			want := noon * c.perSec
			col := data.NewFixed("c", dt, []int64{0, want, 1}, allSet())

			a, err := arrowout.Column(col)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Release()

			var got int64
			switch v := a.(type) {
			case *array.Time32:
				if !c.is32 {
					t.Fatalf("%s exported as Time32, want Time64", dt)
				}
				got = int64(v.Value(1))
			case *array.Time64:
				if c.is32 {
					t.Fatalf("%s exported as Time64, want Time32", dt)
				}
				got = int64(v.Value(1))
			default:
				t.Fatalf("%s exported as %T", dt, a)
			}
			if got != want {
				t.Errorf("noon is %d, want %d", got, want)
			}
		})
	}
}

// TestTimeOutsideTheDayIsRefusedRatherThanTruncated.
//
// Step 57 wraps every Time into [0, 24h) at both producers, so this is unreachable
// through any query — which is exactly why it needs a test. A bare int32() here
// would be the third instance of the construct step 49 removed from rescaleTemporal
// and step 57 removed from the Parquet writer.
func TestTimeOutsideTheDayIsRefusedRatherThanTruncated(t *testing.T) {
	dt := dtype.Time(dtype.Milli)
	col := data.NewFixed("c", dt, []int64{0, 1, 1 << 40}, allSet())

	if _, err := arrowout.Column(col); err == nil {
		t.Fatal("a tick outside [0, 24h) was exported as Time32; int32() would have " +
			"truncated it into a plausible time of day")
	}
}

// --- the ordinary types ------------------------------------------------------

// TestValuesSurviveTheExport covers the types whose layout ursus and Arrow already
// share, on values rather than on types, including nulls.
func TestValuesSurviveTheExport(t *testing.T) {
	valid := func(bits ...bool) bitmap.View {
		b := bitmap.NewBuilder(len(bits))
		for _, x := range bits {
			b.Append(x)
		}
		return b.Finish()
	}
	v := valid(true, false, true)

	cases := []struct {
		name string
		col  *data.Column
		want []string // "" means null
	}{
		{"int64", data.NewFixed("c", dtype.Int64, []int64{7, 0, 9}, v),
			[]string{"7", "", "9"}},
		{"uint8", data.NewFixed("c", dtype.Uint8, []uint8{1, 0, 255}, v),
			[]string{"1", "", "255"}},
		{"float64", data.NewFixed("c", dtype.Float64, []float64{1.5, 0, -2.25}, v),
			[]string{"1.5", "", "-2.25"}},
		{"string", data.NewString("c", []string{"a", "x", "ccc"}, v),
			[]string{"a", "", "ccc"}},
		{"bool", data.NewBool("c", valid(true, true, false), v),
			[]string{"true", "", "false"}},
		{"date", data.NewFixed("c", dtype.Date, []int32{0, 1, 19000}, v),
			[]string{"1970-01-01", "", "2022-01-08"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, err := arrowout.Column(c.col)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Release()

			if a.Len() != rows {
				t.Fatalf("%d rows, want %d", a.Len(), rows)
			}
			for i, want := range c.want {
				if want == "" {
					if !a.IsNull(i) {
						t.Errorf("row %d should be null, got %q", i, a.ValueStr(i))
					}
					continue
				}
				if a.IsNull(i) {
					t.Errorf("row %d is null, want %q", i, want)
					continue
				}
				if got := a.ValueStr(i); got != want {
					t.Errorf("row %d = %q, want %q", i, got, want)
				}
			}
		})
	}
	if len(cases) < 6 {
		t.Error("the case list has shrunk")
	}
}

// TestSlicedColumnsExportCorrectly.
//
// Column.Slice leaves a fixed payload re-windowed to offset zero but the validity
// bitmap carrying a BIT OFFSET, and leaves string offsets absolute rather than
// rebased. Arrow's single element offset cannot express the first of those, so the
// validity must be normalised — and a test that only exports unsliced columns would
// never find out.
func TestSlicedColumnsExportCorrectly(t *testing.T) {
	bits := bitmap.NewBuilder(8)
	for i := range 8 {
		bits.Append(i != 3) // one null, at an index a slice will move
	}
	full := data.NewFixed("c", dtype.Int64,
		[]int64{0, 1, 2, 3, 4, 5, 6, 7}, bits.Finish())

	// offset 3 is not byte-aligned, which is the case that needs the copy.
	s := full.Slice(3, 4)
	a, err := arrowout.Column(s)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()

	if a.Len() != 4 {
		t.Fatalf("%d rows, want 4", a.Len())
	}
	if !a.IsNull(0) {
		t.Error("row 0 of the slice was the null and is not null after export")
	}
	iv := a.(*array.Int64)
	for i, want := range []int64{3, 4, 5, 6} {
		if got := iv.Value(i); got != want {
			t.Errorf("row %d = %d, want %d", i, got, want)
		}
	}
}

// TestStringSliceKeepsAbsoluteOffsets is the other half of the slice question.
// Column.Slice does not rebase string offsets, and Arrow does not require the first
// offset to be zero — so this must work with no copy of the character buffer.
func TestStringSliceKeepsAbsoluteOffsets(t *testing.T) {
	full := data.NewString("c", []string{"aa", "bb", "cc", "dd"}, bitmap.AllSet(4))
	a, err := arrowout.Column(full.Slice(2, 2))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()

	sv := a.(*array.String)
	for i, want := range []string{"cc", "dd"} {
		if got := sv.Value(i); got != want {
			t.Errorf("row %d = %q, want %q", i, got, want)
		}
	}
}

// TestNestedTypesExport covers List and Struct, which recurse.
func TestNestedTypesExport(t *testing.T) {
	t.Run("list", func(t *testing.T) {
		child := data.NewFixed("child", dtype.Int64, []int64{1, 2, 3}, bitmap.AllSet(3))
		col := data.NewList("l", []int32{0, 2, 3}, child, bitmap.AllSet(2))

		a, err := arrowout.Column(col)
		if err != nil {
			t.Fatal(err)
		}
		defer a.Release()

		lv, ok := a.(*array.List)
		if !ok {
			t.Fatalf("exported as %T, want *array.List", a)
		}
		if lv.Len() != 2 {
			t.Fatalf("%d rows, want 2", lv.Len())
		}
		if got := lv.ValueStr(0); got != "[1,2]" {
			t.Errorf("row 0 = %s, want [1,2]", got)
		}
	})

	t.Run("struct", func(t *testing.T) {
		f := data.NewFixed("f", dtype.Int64, []int64{7, 8, 9}, allSet())
		col := data.NewStruct("s", []*data.Column{f}, allSet())

		a, err := arrowout.Column(col)
		if err != nil {
			t.Fatal(err)
		}
		defer a.Release()

		sv, ok := a.(*array.Struct)
		if !ok {
			t.Fatalf("exported as %T, want *array.Struct", a)
		}
		if sv.NumField() != 1 {
			t.Fatalf("%d fields, want 1", sv.NumField())
		}
		if got := sv.Field(0).(*array.Int64).Value(2); got != 9 {
			t.Errorf("field f row 2 = %d, want 9", got)
		}
	})
}

// --- the two guarantees the doc makes ----------------------------------------

// TestExportSharesMemory proves the zero-copy claim rather than asserting it in
// prose. The exported values buffer must be the SAME allocation ursus holds, not an
// equal copy of it.
func TestExportSharesMemory(t *testing.T) {
	col := data.NewFixed("c", dtype.Int64, []int64{1, 2, 3}, allSet())
	mine := col.RawFixed()

	a, err := arrowout.Column(col)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()

	theirs := a.Data().Buffers()[1].Bytes()
	if unsafe.SliceData(mine) != unsafe.SliceData(theirs) {
		t.Error("the exported values buffer is a copy — the whole point of this " +
			"package is that ursus already stores Arrow's layout")
	}
}

// TestReleaseIsInert is the ownership guarantee, and it is aimed carefully.
//
// arrow's Buffer.Release at a zero refcount does `b.buf, b.length = nil, 0` — it
// empties the shared Buffer rather than merely dropping a reference. A record whose
// buffers were live would therefore stop having data after a double release, and so
// would anything else pointing at the same Buffer.
//
// The VALIDITY path is where that is reachable. bitmap.View.Buffer() returns a live
// allocator-backed Buffer when the bit offset is non-zero, which is exactly what a
// non-byte-aligned Column.Slice produces — so the slice at offset 3 below is not
// incidental, it is the whole setup. The payload path cannot reach it at all today,
// because data.Column exposes []byte and not *memory.Buffer; that half of the guard
// is protection against an accessor someone will reasonably want to add later, and
// the package doc says so rather than implying this test covers it.
func TestReleaseIsInert(t *testing.T) {
	bits := bitmap.NewBuilder(8)
	for i := range 8 {
		bits.Append(i != 4)
	}
	full := data.NewFixed("c", dtype.Int64,
		[]int64{0, 1, 2, 3, 4, 5, 6, 7}, bits.Finish())
	col := full.Slice(3, 4) // bit offset 3: NOT byte-aligned

	schema := dtype.MustSchema(dtype.Of("c", dtype.Int64))
	b, err := data.NewBatch(schema, []*data.Column{col})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := arrowout.Record(b)
	if err != nil {
		t.Fatal(err)
	}

	valid := rec.Column(0).Data().Buffers()[0]
	if valid == nil {
		t.Fatal("no validity buffer, so this test proves nothing about releasing one")
	}
	before := valid.Len()
	if before == 0 {
		t.Fatal("the validity buffer is empty before any release")
	}

	// The property is asserted DIRECTLY rather than through a user scenario, because
	// no ordinary sequence reaches it: Record.Release is itself refcounted, so
	// releasing a record twice does not release its buffers twice, and the buffers
	// never fall to zero while this package holds its own reference. Over-releasing
	// the buffer is the only way to ask the question, and the question is worth
	// asking — "Release is a no-op" is what the public doc promises a caller.
	rec.Release()
	for range 5 {
		valid.Release()
	}

	if got := valid.Len(); got != before {
		t.Errorf("the validity buffer went from %d bytes to %d — Release reached "+
			"real memory, so an exported buffer is not inert", before, got)
	}

	// And the frame itself is untouched, which is what a user would notice.
	v, err := data.Values[int64](b.Column(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 4 || v[0] != 3 || v[3] != 6 {
		t.Errorf("the frame's data is %v after releasing an exported record twice", v)
	}
}

// TestRecordCarriesTheSchema checks the record level rather than the column level.
func TestRecordCarriesTheSchema(t *testing.T) {
	schema := dtype.MustSchema(
		dtype.Of("a", dtype.Int64),
		dtype.NotNull("b", dtype.String),
	)
	b, err := data.NewBatch(schema, []*data.Column{
		data.NewFixed("a", dtype.Int64, []int64{1, 2, 3}, allSet()),
		data.NewString("b", []string{"x", "y", "z"}, allSet()),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := arrowout.Record(b)
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Release()

	if rec.NumRows() != 3 || rec.NumCols() != 2 {
		t.Fatalf("record is %dx%d, want 3x2", rec.NumRows(), rec.NumCols())
	}
	got := rec.Schema()
	if got.Field(0).Name != "a" || got.Field(1).Name != "b" {
		t.Errorf("field names are %q, %q", got.Field(0).Name, got.Field(1).Name)
	}
	// Nullability travels, because a consumer writing IPC will record it.
	if !got.Field(0).Nullable || got.Field(1).Nullable {
		t.Errorf("nullability did not survive: a=%v b=%v",
			got.Field(0).Nullable, got.Field(1).Nullable)
	}
	if got.Field(1).Type.ID() != arrow.STRING {
		t.Errorf("b exported as %s, want string", got.Field(1).Type)
	}
}

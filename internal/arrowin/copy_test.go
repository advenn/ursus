package arrowin_test

// Every producer type, copied — top level, sliced, and inside a list and a struct
// whose child is itself sliced.
//
// # The oracle is not ursus
//
// Each case names two Arrow arrays: what a producer hands ursus, and what ursus must
// export afterwards, both built by arrow-go from JSON. The copy is exported through
// arrowout and compared with array.Equal. So the expected values are stated in Arrow's
// terms, by an implementation that reads offsets, bitmaps and slices exactly as the
// spec says, and cannot share a blind spot with the importer.
//
// # The fixture shape is the point
//
// Step 59's lesson: a List column whose child is exactly its window hid two broken
// kernels, because that is the only shape the Parquet reader builds. Arrow producers
// build every shape. So each case runs over an array whose rows sit in the MIDDLE of
// its buffers — sliced, and nested over a child that is sliced out of three copies of
// itself — and anti-vacuity counters prove each shape actually ran.

import (
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/extensions"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/arrowin"
	"github.com/advenn/ursus/internal/arrowout"
)

type copyCase struct {
	name    string
	src     arrow.DataType
	srcJSON string
	dt      dtype.DataType
	want    arrow.DataType // ursus's export type
	// wantJSON defaults to srcJSON.
	wantJSON string
}

var (
	str  = arrow.BinaryTypes.String
	bin  = arrow.BinaryTypes.Binary
	f32  = arrow.PrimitiveTypes.Float32
	date = arrow.FixedWidthTypes.Date32
)

// copyCases has four or more rows each, a null in the middle, and — where the type
// can hold it — a value that differs from its neighbours in a way a shifted read
// would get wrong.
var copyCases = []copyCase{
	{name: "bool", src: arrow.FixedWidthTypes.Boolean, srcJSON: `[true, false, null, true, false]`,
		dt: dtype.Bool, want: arrow.FixedWidthTypes.Boolean},
	{name: "int8", src: arrow.PrimitiveTypes.Int8, srcJSON: `[1, -2, null, 127, -128]`,
		dt: dtype.Int8, want: arrow.PrimitiveTypes.Int8},
	{name: "int16", src: arrow.PrimitiveTypes.Int16, srcJSON: `[1, -2, null, 32767, 4]`,
		dt: dtype.Int16, want: arrow.PrimitiveTypes.Int16},
	{name: "int32", src: arrow.PrimitiveTypes.Int32, srcJSON: `[1, -2, null, 2147483647, 4]`,
		dt: dtype.Int32, want: arrow.PrimitiveTypes.Int32},
	{name: "int64", src: i64, srcJSON: `[1, -2, null, 9223372036854775807, 4]`,
		dt: dtype.Int64, want: i64},
	{name: "uint8", src: arrow.PrimitiveTypes.Uint8, srcJSON: `[1, 2, null, 255, 4]`,
		dt: dtype.Uint8, want: arrow.PrimitiveTypes.Uint8},
	{name: "uint16", src: arrow.PrimitiveTypes.Uint16, srcJSON: `[1, 2, null, 65535, 4]`,
		dt: dtype.Uint16, want: arrow.PrimitiveTypes.Uint16},
	{name: "uint32", src: arrow.PrimitiveTypes.Uint32, srcJSON: `[1, 2, null, 4294967295, 4]`,
		dt: dtype.Uint32, want: arrow.PrimitiveTypes.Uint32},
	{name: "uint64", src: arrow.PrimitiveTypes.Uint64, srcJSON: `[1, 2, null, 18446744073709551615, 4]`,
		dt: dtype.Uint64, want: arrow.PrimitiveTypes.Uint64},
	{name: "float16", src: arrow.FixedWidthTypes.Float16, srcJSON: `[1.5, -0.25, null, 65504, 3]`,
		dt: dtype.Float32, want: f32},
	{name: "float32", src: f32, srcJSON: `[1.5, -0.25, null, 3.4e38, 3]`,
		dt: dtype.Float32, want: f32},
	{name: "float64", src: arrow.PrimitiveTypes.Float64, srcJSON: `[1.5, -0.25, null, 1e308, 3]`,
		dt: dtype.Float64, want: arrow.PrimitiveTypes.Float64},

	{name: "string", src: str, srcJSON: `["a", "", null, "héllo", "zz"]`,
		dt: dtype.String, want: str},
	{name: "large_string", src: arrow.BinaryTypes.LargeString, srcJSON: `["a", "", null, "héllo", "zz"]`,
		dt: dtype.String, want: str},
	{name: "string_view", src: arrow.BinaryTypes.StringView,
		srcJSON: `["a", "a string longer than twelve bytes", null, "", "another long string value"]`,
		dt:      dtype.String, want: str},
	{name: "binary", src: bin, srcJSON: `["AQI=", "", null, "/w==", "AAAA"]`,
		dt: dtype.Binary, want: bin},
	{name: "large_binary", src: arrow.BinaryTypes.LargeBinary, srcJSON: `["AQI=", "", null, "/w==", "AAAA"]`,
		dt: dtype.Binary, want: bin},
	{name: "binary_view", src: arrow.BinaryTypes.BinaryView,
		srcJSON: `["AQI=", "AAECAwQFBgcICQoLDA0ODxAR", null, "", "/w=="]`,
		dt:      dtype.Binary, want: bin},
	{name: "fixed_size_binary", src: &arrow.FixedSizeBinaryType{ByteWidth: 2},
		srcJSON: `["AQI=", "AwQ=", null, "/w8=", "AAA="]`,
		dt:      dtype.Binary, want: bin},

	{name: "date32", src: date, srcJSON: `[0, 19000, null, -1, 3]`,
		dt: dtype.Date, want: date},
	{name: "date64", src: arrow.FixedWidthTypes.Date64,
		srcJSON:  `[0, 1641600000000, null, -86400000, 259200000]`,
		wantJSON: `[0, 19000, null, -1, 3]`,
		dt:       dtype.Date, want: date},
	{name: "timestamp", src: &arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: "Asia/Tokyo"},
		srcJSON: `[0, 1700000000000000, null, -1, 5]`,
		dt:      dtype.Datetime(dtype.Micro, "Asia/Tokyo"),
		want:    &arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: "Asia/Tokyo"}},
	{name: "time32", src: arrow.FixedWidthTypes.Time32s, srcJSON: `[0, 3600, null, 86399, 1]`,
		dt: dtype.Time(dtype.Second), want: arrow.FixedWidthTypes.Time32s},
	{name: "time64", src: arrow.FixedWidthTypes.Time64us, srcJSON: `[0, 3600000000, null, 86399999999, 1]`,
		dt: dtype.Time(dtype.Micro), want: arrow.FixedWidthTypes.Time64us},
	{name: "duration", src: arrow.FixedWidthTypes.Duration_ms, srcJSON: `[0, -5, null, 9223372036854775807, 1]`,
		dt: dtype.Duration(dtype.Milli), want: arrow.FixedWidthTypes.Duration_ms},

	{name: "decimal32", src: &arrow.Decimal32Type{Precision: 9, Scale: 2},
		srcJSON: `["1.23", "-9999999.99", null, "0.01", "5.00"]`,
		dt:      dtype.Decimal(9, 2), want: &arrow.Decimal128Type{Precision: 9, Scale: 2}},
	{name: "decimal64", src: &arrow.Decimal64Type{Precision: 18, Scale: 3},
		srcJSON: `["1.234", "-999999999999999.999", null, "0.001", "5.000"]`,
		dt:      dtype.Decimal(18, 3), want: &arrow.Decimal128Type{Precision: 18, Scale: 3}},
	{name: "decimal128", src: &arrow.Decimal128Type{Precision: 38, Scale: 4},
		srcJSON: `["1.2345", "-9999999999999999999999999999999999.9999", null, "0.0001", "18446744073709551616.0000"]`,
		dt:      dtype.Decimal(38, 4), want: &arrow.Decimal128Type{Precision: 38, Scale: 4}},

	{name: "list", src: arrow.ListOf(i64), srcJSON: `[[1, 2], [], null, [3, null, 4], [5]]`,
		dt: dtype.List(dtype.Int64), want: arrow.ListOf(i64)},
	{name: "large_list", src: arrow.LargeListOf(i64), srcJSON: `[[1, 2], [], null, [3, null, 4], [5]]`,
		dt: dtype.List(dtype.Int64), want: arrow.ListOf(i64)},
	{name: "fixed_size_list", src: arrow.FixedSizeListOf(2, i64),
		srcJSON:  `[[1, 2], [3, null], null, [5, 6], [7, 8]]`,
		wantJSON: `[[1, 2], [3, null], null, [5, 6], [7, 8]]`,
		dt:       dtype.List(dtype.Int64), want: arrow.ListOf(i64)},
	{name: "list_view", src: arrow.ListViewOf(i64), srcJSON: `[[1, 2], [], null, [3, null, 4], [5]]`,
		dt: dtype.List(dtype.Int64), want: arrow.ListOf(i64)},
	{name: "large_list_view", src: arrow.LargeListViewOf(i64), srcJSON: `[[1, 2], [], null, [3, null, 4], [5]]`,
		dt: dtype.List(dtype.Int64), want: arrow.ListOf(i64)},
	{name: "map", src: arrow.MapOf(str, i64),
		srcJSON: `[[{"key": "a", "value": 1}], [], null, [{"key": "b", "value": null}, {"key": "c", "value": 3}], [{"key": "d", "value": 4}]]`,
		dt: dtype.List(dtype.Struct(
			dtype.Field{Name: "key", Type: dtype.String, Nullable: true},
			dtype.Field{Name: "value", Type: dtype.Int64, Nullable: true})),
		want: arrow.ListOf(arrow.StructOf(
			arrow.Field{Name: "key", Type: str, Nullable: true},
			arrow.Field{Name: "value", Type: i64, Nullable: true}))},
	{name: "struct", src: arrow.StructOf(arrow.Field{Name: "a", Type: i64}, arrow.Field{Name: "b", Type: str, Nullable: true}),
		srcJSON: `[{"a": 1, "b": "x"}, {"a": 2, "b": null}, null, {"a": 4, "b": "yy"}, {"a": 5, "b": ""}]`,
		dt: dtype.Struct(dtype.Field{Name: "a", Type: dtype.Int64, Nullable: true},
			dtype.Field{Name: "b", Type: dtype.String, Nullable: true}),
		want: arrow.StructOf(arrow.Field{Name: "a", Type: i64, Nullable: true},
			arrow.Field{Name: "b", Type: str, Nullable: true})},
	{name: "dictionary", src: &arrow.DictionaryType{IndexType: arrow.PrimitiveTypes.Int16, ValueType: str},
		srcJSON: `["b", "a", null, "b", "c"]`,
		dt:      dtype.String, want: str},
	{name: "extension", src: extensions.NewUUIDType(),
		srcJSON:  `["00000000-0000-0000-0000-000000000001", "ffffffff-ffff-ffff-ffff-ffffffffffff", null, "00000000-0000-0000-0000-000000000000", "01020304-0506-0708-090a-0b0c0d0e0f10"]`,
		wantJSON: `["AAAAAAAAAAAAAAAAAAAAAQ==", "/////////////////////w==", null, "AAAAAAAAAAAAAAAAAAAAAA==", "AQIDBAUGBwgJCgsMDQ4PEA=="]`,
		dt:       dtype.Binary, want: bin},
}

func fromJSON(t *testing.T, mem memory.Allocator, dt arrow.DataType, js string) arrow.Array {
	t.Helper()
	a, _, err := array.FromJSON(mem, dt, strings.NewReader(js))
	if err != nil {
		t.Fatalf("building %s from %s: %v", dt, js, err)
	}
	t.Cleanup(a.Release)
	return a
}

func keep(t *testing.T, a arrow.Array) arrow.Array {
	t.Helper()
	t.Cleanup(a.Release)
	return a
}

// padded returns arr's rows sliced out of three copies of itself, so its data offset
// is non-zero and there are real rows on both sides of it in every buffer.
func padded(t *testing.T, mem memory.Allocator, arr arrow.Array) arrow.Array {
	t.Helper()
	big, err := array.Concatenate([]arrow.Array{arr, arr, arr}, mem)
	if err != nil {
		t.Fatalf("concatenating %s: %v", arr.DataType(), err)
	}
	t.Cleanup(big.Release)
	n := int64(arr.Len())
	return keep(t, array.NewSlice(big, n, 2*n))
}

// listOver builds a four-row list over child, whose rows are: [0, 1), empty, null,
// [1, len). The null row owns nothing; see normalise_test.go for one that does.
func listOver(t *testing.T, mem memory.Allocator, child arrow.Array) arrow.Array {
	t.Helper()
	n := int32(child.Len())
	offs := memory.NewResizableBuffer(mem)
	offs.Resize(5 * 4)
	copy(arrow.Int32Traits.CastFromBytes(offs.Bytes()), []int32{0, 1, 1, 1, n})
	valid := memory.NewResizableBuffer(mem)
	valid.Resize(1)
	valid.Bytes()[0] = 0b1011
	d := array.NewData(arrow.ListOf(child.DataType()), 4,
		[]*memory.Buffer{valid, offs}, []arrow.ArrayData{child.Data()}, 1, 0)
	offs.Release()
	valid.Release()
	defer d.Release()
	return keep(t, array.MakeFromData(d))
}

// structOver builds a struct with one field, f, and no nulls of its own.
func structOver(t *testing.T, child arrow.Array) arrow.Array {
	t.Helper()
	st := arrow.StructOf(arrow.Field{Name: "f", Type: child.DataType(), Nullable: true})
	d := array.NewData(st, child.Len(), []*memory.Buffer{nil},
		[]arrow.ArrayData{child.Data()}, 0, 0)
	defer d.Release()
	return keep(t, array.MakeFromData(d))
}

func slice(t *testing.T, a arrow.Array, i, j int) arrow.Array {
	t.Helper()
	return keep(t, array.NewSlice(a, int64(i), int64(j)))
}

// assertCopies converts src as dt and requires ursus's export of it to equal want.
func assertCopies(t *testing.T, label string, src arrow.Array, dt dtype.DataType, want arrow.Array) bool {
	t.Helper()
	col, err := arrowin.Column("c", src, dt, 0, src.Len())
	if err != nil {
		t.Errorf("%s: %v", label, err)
		return false
	}
	got, err := arrowout.Column(col)
	if err != nil {
		t.Errorf("%s: exporting the copy: %v", label, err)
		return false
	}
	defer got.Release()
	if !array.Equal(got, want) {
		t.Errorf("%s:\n got:  %v\n want: %v", label, got, want)
		return false
	}
	return true
}

func TestEveryProducerTypeCopies(t *testing.T) {
	mem := memory.NewCheckedAllocator(memory.NewGoAllocator())
	defer mem.AssertSize(t, 0)

	shapes := map[string]int{}
	seen := map[arrow.Type]bool{}
	fieldDT := func(dt dtype.DataType) dtype.DataType {
		return dtype.Struct(dtype.Field{Name: "f", Type: dt, Nullable: true})
	}

	for _, c := range copyCases {
		t.Run(c.name, func(t *testing.T) {
			src := fromJSON(t, mem, c.src, c.srcJSON)
			wj := c.wantJSON
			if wj == "" {
				wj = c.srcJSON
			}
			want := fromJSON(t, mem, c.want, wj)
			seen[c.src.ID()] = true
			n := src.Len()

			run := func(shape string, s arrow.Array, dt dtype.DataType, w arrow.Array) {
				if assertCopies(t, shape, s, dt, w) {
					shapes[shape]++
				}
			}

			run("whole", src, c.dt, want)
			run("sliced", slice(t, src, 1, n-1), c.dt, slice(t, want, 1, n-1))

			// The child's data offset is non-zero, and its buffers hold rows on both
			// sides of the ones the list reads.
			inner := padded(t, mem, src)
			if inner.Data().Offset() == 0 {
				t.Fatal("padding did not move the child's data offset")
			}
			run("list over a sliced child", listOver(t, mem, inner), dtype.List(c.dt),
				listOver(t, mem, want))
			run("sliced list over a sliced child", slice(t, listOver(t, mem, inner), 1, 4),
				dtype.List(c.dt), slice(t, listOver(t, mem, want), 1, 4))
			run("struct over a sliced field", structOver(t, inner), fieldDT(c.dt),
				structOver(t, want))
			run("sliced struct over a sliced field", slice(t, structOver(t, inner), 1, n-1),
				fieldDT(c.dt), slice(t, structOver(t, want), 1, n-1))

			// Two levels: a list over a sliced struct over a sliced field.
			outer := padded(t, mem, structOver(t, inner))
			run("list over a sliced struct over a sliced field",
				slice(t, listOver(t, mem, outer), 1, 4),
				dtype.List(fieldDT(c.dt)), slice(t, listOver(t, mem, structOver(t, want)), 1, 4))
		})
	}

	// Anti-vacuity: every shape ran for every case, and the cases cover every id the
	// type map says it converts, apart from NULL, which has no values to slice.
	for _, shape := range []string{"whole", "sliced", "list over a sliced child",
		"sliced list over a sliced child", "struct over a sliced field",
		"sliced struct over a sliced field", "list over a sliced struct over a sliced field"} {
		if shapes[shape] != len(copyCases) {
			t.Errorf("shape %q passed for %d of %d cases", shape, shapes[shape], len(copyCases))
		}
	}
	for id := arrow.Type(0); !strings.HasPrefix(id.String(), "Type("); id++ {
		c, ok := sampleArrowType(id)
		if ok && !c.refused && id != arrow.NULL && !seen[id] {
			t.Errorf("%s converts, and no copy case produces one", id)
		}
	}
}

// TestNullArraysCopy is the one id with no values: every row is null and there is
// no bitmap to read.
func TestNullArraysCopy(t *testing.T) {
	arr := array.NewNull(5)
	defer arr.Release()
	for _, s := range []arrow.Array{arr, array.NewSlice(arr, 1, 4)} {
		col, err := arrowin.Column("n", s, dtype.Null, 0, s.Len())
		if err != nil {
			t.Fatal(err)
		}
		if col.Len() != s.Len() || col.NullCount() != s.Len() {
			t.Errorf("a Null array of %d rows became %d rows with %d nulls",
				s.Len(), col.Len(), col.NullCount())
		}
	}
}

// TestValidityRunsCrossWords: eachRun reads the bitmap a word at a time and skips
// whole words that continue the current run. Every case above is shorter than one
// word, so that skip was never taken — a fast path that ignored WHICH validity the
// run held passed all of them. These arrays span several words, start mid-word, and
// switch validity exactly at, just before and just after word boundaries.
func TestValidityRunsCrossWords(t *testing.T) {
	mem := memory.NewCheckedAllocator(memory.NewGoAllocator())
	defer mem.AssertSize(t, 0)

	patterns := map[string]func(i int) bool{
		"all valid":             func(int) bool { return true },
		"all null":              func(int) bool { return false },
		"blocks of a word":      func(i int) bool { return (i/64)%2 == 1 },
		"blocks off the word":   func(i int) bool { return ((i+3)/61)%2 == 0 },
		"one null per word":     func(i int) bool { return i%64 != 63 },
		"one valid per word":    func(i int) bool { return i%64 == 0 },
		"every seventh is null": func(i int) bool { return i%7 != 0 },
	}
	const n = 300
	for name, valid := range patterns {
		t.Run(name, func(t *testing.T) {
			ib := array.NewInt64Builder(mem)
			sb := array.NewStringBuilder(mem)
			bb := array.NewBooleanBuilder(mem)
			for i := range n {
				if !valid(i) {
					ib.AppendNull()
					sb.AppendNull()
					bb.AppendNull()
					continue
				}
				ib.Append(int64(i))
				sb.Append(strings.Repeat("x", i%5))
				bb.Append(i%3 == 0)
			}
			ints, strs, bools := keep(t, ib.NewArray()), keep(t, sb.NewArray()), keep(t, bb.NewArray())
			ib.Release()
			sb.Release()
			bb.Release()

			for _, c := range []struct {
				arr arrow.Array
				dt  dtype.DataType
			}{{ints, dtype.Int64}, {strs, dtype.String}, {bools, dtype.Bool}} {
				for _, w := range [][2]int{{0, n}, {5, n - 3}, {63, 130}, {64, 129}, {65, 257}} {
					s := slice(t, c.arr, w[0], w[1])
					assertCopies(t, c.dt.String()+" "+name, s, c.dt, s)
				}
			}
		})
	}
}

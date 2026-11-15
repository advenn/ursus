package arrowmeta

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/bitutil"
	"github.com/apache/arrow-go/v18/arrow/decimal128"
	"github.com/apache/arrow-go/v18/arrow/decimal256"
	"github.com/apache/arrow-go/v18/arrow/endian"
	"github.com/apache/arrow-go/v18/arrow/extensions"
	"github.com/apache/arrow-go/v18/arrow/float16"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// gen builds Arrow types and arrays from a seed, for the walk's differential against
// arrow-go: every type arrow-go's IPC writer writes, nested in each other, views with
// variadic buffers among them, dictionaries of any value at any depth, unions, runs
// and extensions; or, for a big-endian file, the types whose byte order arrow-go's
// reader swaps (ipc's swapType).
type gen struct {
	rng *rand.Rand
	mem memory.Allocator
	be  bool
}

func newGen(seed uint64, be bool) *gen {
	return &gen{rng: rand.New(rand.NewPCG(seed, 519)), mem: memory.NewGoAllocator(), be: be}
}

func (g *gen) pick(n int) int { return g.rng.IntN(n) }

// leaf is a type of no children.
func (g *gen) leaf() arrow.DataType {
	units := []arrow.TimeUnit{arrow.Second, arrow.Millisecond, arrow.Microsecond, arrow.Nanosecond}
	pool := []arrow.DataType{
		arrow.Null, arrow.FixedWidthTypes.Boolean,
		arrow.PrimitiveTypes.Int8, arrow.PrimitiveTypes.Int16, arrow.PrimitiveTypes.Int32, arrow.PrimitiveTypes.Int64,
		arrow.PrimitiveTypes.Uint8, arrow.PrimitiveTypes.Uint16, arrow.PrimitiveTypes.Uint32, arrow.PrimitiveTypes.Uint64,
		arrow.FixedWidthTypes.Float16, arrow.PrimitiveTypes.Float32, arrow.PrimitiveTypes.Float64,
		&arrow.Decimal128Type{Precision: 9, Scale: 2}, &arrow.Decimal256Type{Precision: 40, Scale: 3},
		arrow.FixedWidthTypes.Date32, arrow.FixedWidthTypes.Date64,
		arrow.FixedWidthTypes.Time32s, arrow.FixedWidthTypes.Time32ms, arrow.FixedWidthTypes.Time64us, arrow.FixedWidthTypes.Time64ns,
		&arrow.TimestampType{Unit: units[g.pick(4)]}, &arrow.TimestampType{Unit: units[g.pick(4)], TimeZone: "Europe/Dublin"},
		&arrow.DurationType{Unit: units[g.pick(4)]},
		arrow.FixedWidthTypes.MonthInterval, arrow.FixedWidthTypes.DayTimeInterval, arrow.FixedWidthTypes.MonthDayNanoInterval,
		&arrow.FixedSizeBinaryType{ByteWidth: 1 + g.pick(5)},
	}
	if !g.be {
		pool = append(pool, arrow.BinaryTypes.Binary, arrow.BinaryTypes.String, arrow.BinaryTypes.LargeBinary,
			arrow.BinaryTypes.LargeString, arrow.BinaryTypes.BinaryView, arrow.BinaryTypes.StringView,
			arrow.BinaryTypes.BinaryView, arrow.BinaryTypes.StringView)
	}
	return pool[g.pick(len(pool))]
}

// typ is a type nested at most depth deep.
//
// A big-endian record's types are leaves without offsets: arrow-go's writer reads a
// column's offsets to rebase them, which a swapped column's are not, and its reader
// does not swap a nested column's children back as written.
func (g *gen) typ(depth int) arrow.DataType {
	if g.be || depth <= 0 || g.pick(3) == 0 {
		return g.leaf()
	}
	switch g.pick(13) {
	case 0:
		return arrow.ListOfField(g.field("item", depth-1))
	case 1:
		return arrow.LargeListOfField(g.field("item", depth-1))
	case 2:
		return arrow.FixedSizeListOfField(int32(1+g.pick(3)), g.field("item", depth-1))
	case 3:
		fields := make([]arrow.Field, 1+g.pick(3))
		for i := range fields {
			fields[i] = g.field(fmt.Sprintf("f%d", i), depth-1)
		}
		return arrow.StructOf(fields...)
	case 4:
		keys := []arrow.DataType{arrow.BinaryTypes.String, arrow.PrimitiveTypes.Int32, arrow.BinaryTypes.LargeString}
		return arrow.MapOf(keys[g.pick(len(keys))], g.typ(depth-1))
	case 5, 6:
		fields := make([]arrow.Field, 1+g.pick(3))
		codes := make([]arrow.UnionTypeCode, len(fields))
		for i := range fields {
			fields[i] = g.field(fmt.Sprintf("u%d", i), depth-1)
			codes[i] = arrow.UnionTypeCode(3 * i)
		}
		if g.pick(2) == 0 {
			return arrow.SparseUnionOf(fields, codes)
		}
		return arrow.DenseUnionOf(fields, codes)
	case 7:
		return arrow.ListViewOfField(g.field("item", depth-1))
	case 8:
		return arrow.LargeListViewOfField(g.field("item", depth-1))
	case 9:
		return arrow.RunEndEncodedOf(arrow.PrimitiveTypes.Int32, g.leaf())
	case 10, 11:
		idx := []arrow.DataType{arrow.PrimitiveTypes.Int8, arrow.PrimitiveTypes.Int16, arrow.PrimitiveTypes.Int32,
			arrow.PrimitiveTypes.Int64, arrow.PrimitiveTypes.Uint8, arrow.PrimitiveTypes.Uint16,
			arrow.PrimitiveTypes.Uint32, arrow.PrimitiveTypes.Uint64}
		return &arrow.DictionaryType{IndexType: idx[g.pick(len(idx))], ValueType: g.dictValues()}
	}
	if g.pick(2) == 0 {
		return extensions.NewUUIDType()
	}
	json, err := extensions.NewJSONType([]arrow.DataType{arrow.BinaryTypes.String, arrow.BinaryTypes.StringView}[g.pick(2)])
	if err != nil {
		panic(err)
	}
	return json
}

// dictValues is a dictionary's value type: text and binary of every layout, views
// most often, so that dictionary batches hold variadic counts, a leaf, or views in a
// list or a struct. arrow-go's IPC reader misreads its own dictionaries of runs, list
// views, unions and dictionaries.
func (g *gen) dictValues() arrow.DataType {
	switch g.pick(8) {
	case 0:
		return g.leaf()
	case 1:
		return arrow.ListOf(arrow.BinaryTypes.StringView)
	case 2:
		return arrow.StructOf(arrow.Field{Name: "v", Type: arrow.BinaryTypes.BinaryView, Nullable: true},
			arrow.Field{Name: "i", Type: arrow.PrimitiveTypes.Int32, Nullable: true})
	}
	return []arrow.DataType{arrow.BinaryTypes.String, arrow.BinaryTypes.LargeString, arrow.BinaryTypes.Binary,
		arrow.BinaryTypes.StringView, arrow.BinaryTypes.BinaryView}[g.pick(5)]
}

// field is a field named name of a type nested at most depth deep, with metadata now
// and then.
func (g *gen) field(name string, depth int) arrow.Field {
	f := arrow.Field{Name: name, Type: g.typ(depth), Nullable: g.pick(4) != 0}
	if g.pick(5) == 0 {
		f.Metadata = arrow.NewMetadata([]string{"k", "step"}, []string{name, "519"})
	}
	return f
}

// validity is a bitmap of n values, about one in five null, and its nulls; nil and
// none where nulls is false or none fell.
func (g *gen) validity(n int, nulls bool) ([]bool, *memory.Buffer, int) {
	valid := make([]bool, n)
	null := 0
	for i := range valid {
		valid[i] = !nulls || g.pick(5) != 0
		if !valid[i] {
			null++
		}
	}
	if null == 0 {
		return valid, nil, 0
	}
	bits := make([]byte, bitutil.BytesForBits(int64(n)))
	for i, v := range valid {
		if v {
			bitutil.SetBit(bits, i)
		}
	}
	return valid, memory.NewBufferBytes(bits), null
}

func int32s(v []int32) *memory.Buffer { return memory.NewBufferBytes(arrow.Int32Traits.CastToBytes(v)) }
func int64s(v []int64) *memory.Buffer { return memory.NewBufferBytes(arrow.Int64Traits.CastToBytes(v)) }

// sizes is n list sizes, 0 for a null list, and their running offsets.
func (g *gen) sizes(valid []bool) (sizes []int, offsets []int, total int) {
	sizes = make([]int, len(valid))
	offsets = make([]int, len(valid)+1)
	for i, v := range valid {
		if v {
			sizes[i] = g.pick(4)
		}
		total += sizes[i]
		offsets[i+1] = total
	}
	return sizes, offsets, total
}

// array is n values of dt, nulls among them where nulls is true.
func (g *gen) array(dt arrow.DataType, n int, nulls bool) arrow.Array {
	switch dt := dt.(type) {
	case *arrow.ListType, *arrow.LargeListType, *arrow.MapType:
		valid, bm, null := g.validity(n, nulls)
		_, offs, total := g.sizes(valid)
		var child arrow.Array
		var buf *memory.Buffer
		if m, ok := dt.(*arrow.MapType); ok {
			keys := g.array(m.KeyType(), total, false)
			items := g.array(m.ItemType(), total, true)
			child = array.NewStructData(array.NewData(m.Elem(), total, []*memory.Buffer{nil},
				[]arrow.ArrayData{keys.Data(), items.Data()}, 0, 0))
		} else {
			child = g.array(dt.(arrow.ListLikeType).Elem(), total, true)
		}
		if _, ok := dt.(*arrow.LargeListType); ok {
			buf = int64s(widen64(offs))
		} else {
			buf = int32s(widen32(offs))
		}
		return array.MakeFromData(array.NewData(dt, n, []*memory.Buffer{bm, buf}, []arrow.ArrayData{child.Data()}, null, 0))
	case *arrow.ListViewType, *arrow.LargeListViewType:
		valid, bm, null := g.validity(n, nulls)
		sz, offs, total := g.sizes(valid)
		child := g.array(dt.(arrow.ListLikeType).Elem(), total, true)
		var ob, sb *memory.Buffer
		if _, ok := dt.(*arrow.LargeListViewType); ok {
			ob, sb = int64s(widen64(offs[:n])), int64s(widen64(sz))
		} else {
			ob, sb = int32s(widen32(offs[:n])), int32s(widen32(sz))
		}
		return array.MakeFromData(array.NewData(dt, n, []*memory.Buffer{bm, ob, sb}, []arrow.ArrayData{child.Data()}, null, 0))
	case *arrow.FixedSizeListType:
		_, bm, null := g.validity(n, nulls)
		child := g.array(dt.Elem(), n*int(dt.Len()), true)
		return array.MakeFromData(array.NewData(dt, n, []*memory.Buffer{bm}, []arrow.ArrayData{child.Data()}, null, 0))
	case *arrow.StructType:
		_, bm, null := g.validity(n, nulls)
		children := make([]arrow.ArrayData, dt.NumFields())
		for i, f := range dt.Fields() {
			children[i] = g.array(f.Type, n, true).Data()
		}
		return array.MakeFromData(array.NewData(dt, n, []*memory.Buffer{bm}, children, null, 0))
	case *arrow.SparseUnionType:
		ids := make([]int8, n)
		children := make([]arrow.ArrayData, dt.NumFields())
		for i := range ids {
			ids[i] = int8(dt.TypeCodes()[g.pick(len(children))])
		}
		for i, f := range dt.Fields() {
			children[i] = g.array(f.Type, n, true).Data()
		}
		return array.MakeFromData(array.NewData(dt, n,
			[]*memory.Buffer{nil, memory.NewBufferBytes(arrow.Int8Traits.CastToBytes(ids))}, children, 0, 0))
	case *arrow.DenseUnionType:
		ids := make([]int8, n)
		offs := make([]int32, n)
		counts := make([]int, dt.NumFields())
		for i := range ids {
			c := g.pick(len(counts))
			ids[i], offs[i] = int8(dt.TypeCodes()[c]), int32(counts[c])
			counts[c]++
		}
		children := make([]arrow.ArrayData, dt.NumFields())
		for i, f := range dt.Fields() {
			children[i] = g.array(f.Type, counts[i], true).Data()
		}
		return array.MakeFromData(array.NewData(dt, n,
			[]*memory.Buffer{nil, memory.NewBufferBytes(arrow.Int8Traits.CastToBytes(ids)), int32s(offs)}, children, 0, 0))
	case *arrow.RunEndEncodedType:
		var ends []int32
		for end := 0; end < n; {
			end = min(n, end+1+g.pick(3))
			ends = append(ends, int32(end))
		}
		re := array.NewInt32Data(array.NewData(arrow.PrimitiveTypes.Int32, len(ends), []*memory.Buffer{nil, int32s(ends)}, nil, 0, 0))
		values := g.array(dt.Encoded(), len(ends), true)
		return array.MakeFromData(array.NewData(dt, n, []*memory.Buffer{nil},
			[]arrow.ArrayData{re.Data(), values.Data()}, 0, 0))
	case *arrow.DictionaryType:
		dict := g.array(dt.ValueType, 1+g.pick(5), true)
		return array.NewDictionaryArray(dt, g.indices(dt.IndexType, n, dict.Len(), nulls), dict)
	case arrow.ExtensionType:
		return array.NewExtensionArrayWithStorage(dt, g.storage(dt, n, nulls))
	}
	return g.leafArray(dt, n, nulls)
}

func widen32(v []int) []int32 {
	out := make([]int32, len(v))
	for i, x := range v {
		out[i] = int32(x)
	}
	return out
}

func widen64(v []int) []int64 {
	out := make([]int64, len(v))
	for i, x := range v {
		out[i] = int64(x)
	}
	return out
}

// storage is n values of an extension type's storage that the type takes: a UUID's
// sixteen bytes, a JSON value's text.
func (g *gen) storage(dt arrow.ExtensionType, n int, nulls bool) arrow.Array {
	if _, ok := dt.(*extensions.JSONType); !ok {
		return g.array(dt.StorageType(), n, nulls)
	}
	b := array.NewBuilder(g.mem, dt.StorageType())
	for range n {
		if nulls && g.pick(5) == 0 {
			b.AppendNull()
			continue
		}
		if err := b.AppendValueFromString(fmt.Sprintf(`{"a": %d, "b": [%q]}`, g.pick(100), strings.Repeat("j", g.pick(20)))); err != nil {
			panic(err)
		}
	}
	return b.NewArray()
}

// indices is n indices of type dt into a dictionary of k values.
func (g *gen) indices(dt arrow.DataType, n, k int, nulls bool) arrow.Array {
	b := array.NewBuilder(g.mem, dt)
	for range n {
		if nulls && g.pick(5) == 0 {
			b.AppendNull()
			continue
		}
		if err := b.AppendValueFromString(fmt.Sprint(g.pick(k))); err != nil {
			panic(err)
		}
	}
	return b.NewArray()
}

// text is a string for a value: short, past a view's inline twelve bytes, and now and
// then past 32 KiB, so that a view column's data spans more than one buffer.
func (g *gen) text() string {
	switch g.pick(40) {
	case 0:
		return strings.Repeat("v", 33<<10+g.pick(100))
	case 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15:
		return strings.Repeat("long text ", 1+g.pick(4))
	}
	return strings.Repeat("s", g.pick(6))
}

// leafArray is n values of a type of no children, built by arrow-go's builder.
func (g *gen) leafArray(dt arrow.DataType, n int, nulls bool) arrow.Array {
	b := array.NewBuilder(g.mem, dt)
	for range n {
		if nulls && g.pick(5) == 0 {
			b.AppendNull()
			continue
		}
		switch b := b.(type) {
		case *array.NullBuilder:
			b.AppendNull()
		case *array.BooleanBuilder:
			b.Append(g.pick(2) == 0)
		case *array.Int8Builder:
			b.Append(int8(g.rng.Uint32()))
		case *array.Int16Builder:
			b.Append(int16(g.rng.Uint32()))
		case *array.Int32Builder:
			b.Append(int32(g.rng.Uint32()))
		case *array.Int64Builder:
			b.Append(int64(g.rng.Uint64()))
		case *array.Uint8Builder:
			b.Append(uint8(g.rng.Uint32()))
		case *array.Uint16Builder:
			b.Append(uint16(g.rng.Uint32()))
		case *array.Uint32Builder:
			b.Append(g.rng.Uint32())
		case *array.Uint64Builder:
			b.Append(g.rng.Uint64())
		case *array.Float16Builder:
			b.Append(float16.New(float32(g.rng.NormFloat64())))
		case *array.Float32Builder:
			b.Append(float32(g.rng.NormFloat64()))
		case *array.Float64Builder:
			b.Append(g.rng.NormFloat64() * 1e6)
		case *array.Decimal128Builder:
			b.Append(decimal128.FromI64(int64(g.pick(1e9)) - 5e8))
		case *array.Decimal256Builder:
			b.Append(decimal256.FromI64(int64(g.rng.Uint64() >> 2)))
		case *array.Date32Builder:
			b.Append(arrow.Date32(g.pick(40000) - 20000))
		case *array.Date64Builder:
			b.Append(arrow.Date64(int64(g.pick(40000)-20000) * 86_400_000))
		case *array.Time32Builder:
			b.Append(arrow.Time32(g.pick(86400)))
		case *array.Time64Builder:
			b.Append(arrow.Time64(g.pick(86_400_000_000)))
		case *array.TimestampBuilder:
			b.Append(arrow.Timestamp(int64(g.rng.Uint64() >> 8)))
		case *array.DurationBuilder:
			b.Append(arrow.Duration(int64(g.rng.Uint64()>>8) - 1<<54))
		case *array.MonthIntervalBuilder:
			b.Append(arrow.MonthInterval(g.pick(1000) - 500))
		case *array.DayTimeIntervalBuilder:
			b.Append(arrow.DayTimeInterval{Days: int32(g.pick(100)), Milliseconds: int32(g.pick(86_400_000))})
		case *array.MonthDayNanoIntervalBuilder:
			b.Append(arrow.MonthDayNanoInterval{Months: int32(g.pick(20)), Days: int32(g.pick(40)), Nanoseconds: int64(g.rng.Uint64() >> 4)})
		case *array.FixedSizeBinaryBuilder:
			v := make([]byte, dt.(*arrow.FixedSizeBinaryType).ByteWidth)
			for i := range v {
				v[i] = byte(g.rng.Uint32())
			}
			b.Append(v)
		case *array.BinaryBuilder:
			b.AppendString(g.text())
		case *array.StringBuilder:
			b.Append(g.text())
		case *array.LargeStringBuilder:
			b.Append(g.text())
		case *array.BinaryViewBuilder:
			b.AppendString(g.text())
		case *array.StringViewBuilder:
			b.Append(g.text())
		default:
			panic(fmt.Sprintf("gen: no values for %s (%T)", dt, b))
		}
	}
	return b.NewArray()
}

// record is a record of one to five columns nested at most three deep, of up to
// twenty rows, with schema metadata now and then.
func (g *gen) record() arrow.RecordBatch {
	fields := make([]arrow.Field, 1+g.pick(5))
	cols := make([]arrow.Array, len(fields))
	n := g.pick(21)
	for i := range fields {
		fields[i] = g.field(fmt.Sprintf("c%d", i), 3)
		cols[i] = g.array(fields[i].Type, n, fields[i].Nullable)
	}
	var md *arrow.Metadata
	if g.pick(3) == 0 {
		m := arrow.NewMetadata([]string{"origin"}, []string{"step 519"})
		md = &m
	}
	return array.NewRecordBatch(arrow.NewSchema(fields, md), cols, int64(n))
}

// writeIPC is rec written by arrow-go as an IPC file or stream, twice, with opts.
func writeIPC(t testing.TB, file bool, rec arrow.RecordBatch, opts ...ipc.Option) []byte {
	t.Helper()
	var buf bytes.Buffer
	opts = append(opts, ipc.WithSchema(rec.Schema()), ipc.WithAllocator(memory.NewGoAllocator()))
	var w interface {
		Write(arrow.RecordBatch) error
		Close() error
	}
	if file {
		fw, err := ipc.NewFileWriter(&buf, opts...)
		if err != nil {
			t.Fatal(err)
		}
		w = fw
	} else {
		w = ipc.NewWriter(&buf, opts...)
	}
	for range 2 {
		if err := w.Write(rec); err != nil {
			t.Fatalf("writing %s: %v", rec.Schema(), err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// bigEndian is rec as a big-endian writer holds it: each buffer's values' bytes
// reversed, as arrow-go's reader reverses them back (ipc's swapType), and the schema
// saying so. rec's types are gen's with be set: fixed widths, structs, fixed-size
// lists and sparse unions of them.
func bigEndian(rec arrow.RecordBatch) arrow.RecordBatch {
	cols := make([]arrow.Array, rec.NumCols())
	for i, c := range rec.Columns() {
		cols[i] = array.MakeFromData(swapped(c.Data()))
	}
	s := rec.Schema()
	md := s.Metadata()
	return array.NewRecordBatch(arrow.NewSchemaWithEndian(s.Fields(), &md, endian.BigEndian), cols, rec.NumRows())
}

// swapped is d with its values' byte order reversed, in new buffers.
func swapped(d arrow.ArrayData) arrow.ArrayData {
	bufs := make([]*memory.Buffer, len(d.Buffers()))
	for i, b := range d.Buffers() {
		if b != nil {
			bufs[i] = memory.NewBufferBytes(bytes.Clone(b.Bytes()))
		}
	}
	// reverse reverses each of the fields of each width bytes in buffer 1, which
	// widths lists in order, a value at a time.
	reverse := func(fields ...int) {
		if bufs[1] == nil {
			return
		}
		width := 0
		for _, f := range fields {
			width += f
		}
		v := bufs[1].Bytes()
		for at := 0; at+width <= len(v); at += width {
			for p, f := at, 0; f < len(fields); p, f = p+fields[f], f+1 {
				for l, r := p, p+fields[f]-1; l < r; l, r = l+1, r-1 {
					v[l], v[r] = v[r], v[l]
				}
			}
		}
	}
	switch dt := d.DataType().(type) {
	case *arrow.Decimal128Type:
		reverse(16)
	case *arrow.Decimal256Type:
		reverse(32)
	case *arrow.DayTimeIntervalType:
		reverse(4, 4)
	case *arrow.MonthDayNanoIntervalType:
		reverse(4, 4, 8)
	case arrow.FixedWidthDataType:
		if w := dt.BitWidth(); w > 8 && dt.ID() != arrow.FIXED_SIZE_BINARY {
			reverse(w / 8)
		}
	}
	children := make([]arrow.ArrayData, len(d.Children()))
	for i, c := range d.Children() {
		children[i] = swapped(c)
	}
	return array.NewData(d.DataType(), d.Len(), bufs, children, d.NullN(), d.Offset())
}

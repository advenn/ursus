package ursus_test

// Arrow import through the public API.
//
// # The poisoning allocator is what gives the lifetime tests teeth
//
// A frame that ALIASED Arrow memory instead of copying it would pass every value
// comparison with arrow-go's default allocator: GoAllocator.Free does nothing, so
// the bytes stay put and the garbage collector keeps them alive for as long as ursus
// holds the slice. The bug is real only where memory is actually reused — an IPC
// reader's next record, a C producer's release callback — and a test cannot see it
// unless freed memory CHANGES. poisoner overwrites every byte it is asked to free, so
// a frame still pointing at them reads 0xDB instead of its data.

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/ursustest"
)

// poisoner overwrites memory as it is freed, or moved by a reallocation.
type poisoner struct {
	inner memory.Allocator

	mu    sync.Mutex
	freed int
}

func (p *poisoner) Allocate(n int) []byte { return p.inner.Allocate(n) }

func (p *poisoner) Reallocate(n int, b []byte) []byte {
	out := p.inner.Reallocate(n, b)
	if len(b) > 0 && (len(out) == 0 || unsafe.SliceData(out) != unsafe.SliceData(b)) {
		p.poison(b)
	}
	return out
}

func (p *poisoner) Free(b []byte) {
	p.poison(b)
	p.inner.Free(b)
}

func (p *poisoner) poison(b []byte) {
	for i := range b {
		b[i] = 0xDB
	}
	p.mu.Lock()
	p.freed += len(b)
	p.mu.Unlock()
}

func (p *poisoner) bytesFreed() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.freed
}

// importSchema covers a fixed-width, a validity bitmap, strings, a list and a struct
// — each layout a copy could alias — in types that export back as themselves, so the
// original Arrow arrays are the expected values.
var importSchema = arrow.NewSchema([]arrow.Field{
	{Name: "id", Type: arrow.PrimitiveTypes.Int64, Nullable: true},
	{Name: "name", Type: arrow.BinaryTypes.String, Nullable: true},
	{Name: "tags", Type: arrow.ListOf(arrow.PrimitiveTypes.Int64), Nullable: true},
	{Name: "person", Type: arrow.StructOf(
		arrow.Field{Name: "age", Type: arrow.PrimitiveTypes.Int64, Nullable: true},
		arrow.Field{Name: "city", Type: arrow.BinaryTypes.String, Nullable: true}), Nullable: true},
	{Name: "ok", Type: arrow.FixedWidthTypes.Boolean, Nullable: true},
	{Name: "score", Type: arrow.PrimitiveTypes.Float64, Nullable: true},
}, nil)

// importRecords builds two ten-row records from JSON with its own allocator.
func importRecords(t *testing.T) []arrow.RecordBatch {
	t.Helper()
	var recs []arrow.RecordBatch
	for r := range 2 {
		var sb strings.Builder
		sb.WriteString("[")
		for i := range 10 {
			if i > 0 {
				sb.WriteString(",")
			}
			id := r*10 + i
			switch {
			case i == 3:
				sb.WriteString(`{"id": null, "name": null, "tags": null, "person": null, "ok": null, "score": null}`)
			default:
				sb.WriteString(strings.NewReplacer("ID", strconv.Itoa(id), "N", strings.Repeat("n", i)).Replace(
					`{"id": ID, "name": "name-N", "tags": [ID, 1, ID], "person": {"age": ID, "city": "cityN"}, "ok": true, "score": ID.5}`))
			}
		}
		sb.WriteString("]")
		rec, _, err := array.RecordFromJSON(memory.NewGoAllocator(), importSchema, strings.NewReader(sb.String()))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(rec.Release)
		recs = append(recs, rec)
	}
	return recs
}

func ipcBytes(t *testing.T, recs []arrow.RecordBatch) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := ipc.NewWriter(&buf, ipc.WithSchema(recs[0].Schema()), ipc.WithAllocator(memory.NewGoAllocator()))
	for _, rec := range recs {
		if err := w.Write(rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// assertMatchesRecords compares a collected frame with the records it was imported
// from, column by column, through arrow-go's array.Equal.
func assertMatchesRecords(t *testing.T, df *ursus.DataFrame, recs []arrow.RecordBatch) {
	t.Helper()
	got, err := df.Record()
	if err != nil {
		t.Fatal(err)
	}
	defer got.Release()
	for c := range int(recs[0].NumCols()) {
		parts := make([]arrow.Array, len(recs))
		for i, rec := range recs {
			parts[i] = rec.Column(c)
		}
		want, err := array.Concatenate(parts, memory.NewGoAllocator())
		if err != nil {
			t.Fatal(err)
		}
		if !array.Equal(got.Column(c), want) {
			t.Errorf("column %q:\n got:  %v\n want: %v", recs[0].ColumnName(c), got.Column(c), want)
		}
		want.Release()
	}
}

// TestScanArrowRecordsCopiesBeforeReturning reads records through a poisoning
// allocator, releases every one of them and the reader, proves the memory was
// actually freed and overwritten — and only THEN runs the query.
func TestScanArrowRecordsCopiesBeforeReturning(t *testing.T) {
	sent := importRecords(t)
	p := &poisoner{inner: memory.NewGoAllocator()}
	mem := memory.NewCheckedAllocator(p)

	r, err := ipc.NewReader(bytes.NewReader(ipcBytes(t, sent)), ipc.WithAllocator(mem))
	if err != nil {
		t.Fatal(err)
	}
	var recs []arrow.RecordBatch
	for r.Next() {
		rec := r.RecordBatch()
		rec.Retain()
		recs = append(recs, rec)
	}
	if err := r.Err(); err != nil {
		t.Fatal(err)
	}
	held := mem.CurrentAlloc()

	lf := ursus.ScanArrowRecords(recs...)
	for _, rec := range recs {
		rec.Release()
	}
	r.Release()

	// Anti-vacuity, before the query runs: the records lived in this allocator, all
	// of it was returned, and returning it overwrote it.
	if held == 0 {
		t.Fatal("the reader allocated nothing through the test allocator, so poisoning proves nothing")
	}
	mem.AssertSize(t, 0)
	if p.bytesFreed() == 0 {
		t.Fatal("nothing was freed, so nothing was poisoned")
	}

	df, err := lf.Collect(t.Context(), ursus.WithBatchSize(3))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesRecords(t, df, sent)
}

func TestScanArrowRecordsRefusals(t *testing.T) {
	recs := importRecords(t)
	other := arrow.NewSchema([]arrow.Field{{Name: "id", Type: arrow.BinaryTypes.String}}, nil)
	otherRec, _, err := array.RecordFromJSON(memory.NewGoAllocator(), other, strings.NewReader(`[{"id": "a"}]`))
	if err != nil {
		t.Fatal(err)
	}
	defer otherRec.Release()

	released, _, err := array.RecordFromJSON(memory.NewGoAllocator(), importSchema, strings.NewReader(`[]`))
	if err != nil {
		t.Fatal(err)
	}
	released.Release()

	interval := arrow.NewSchema([]arrow.Field{{Name: "iv", Type: arrow.FixedWidthTypes.MonthInterval}}, nil)
	ivRec, _, err := array.RecordFromJSON(memory.NewGoAllocator(), interval, strings.NewReader(`[]`))
	if err != nil {
		t.Fatal(err)
	}
	defer ivRec.Release()

	for _, c := range []struct {
		name string
		lf   *ursus.LazyFrame
		kind error
		says string
	}{
		{"no records", ursus.ScanArrowRecords(), uerr.ErrValue, "at least one record"},
		{"a nil record", ursus.ScanArrowRecords(nil), uerr.ErrValue, "record 0 is nil"},
		{"a second record with another schema", ursus.ScanArrowRecords(recs[0], otherRec), uerr.ErrSchema, "record 1"},
		{"a released record", ursus.ScanArrowRecords(recs[0], released), uerr.ErrValue, "released"},
		{"a type ursus cannot hold", ursus.ScanArrowRecords(ivRec), uerr.ErrUnsupported, "month_interval"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.lf.Collect(t.Context())
			if err == nil {
				t.Fatal("accepted")
			}
			var ue *uerr.Error
			if !errors.As(err, &ue) || !errors.Is(&uerr.Error{Kind: ue.Kind}, c.kind) {
				t.Errorf("wrong top-level kind: %v", err)
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("the error should say %q: %v", c.says, err)
			}
		})
	}
}

// arrowBits packs a validity pattern into a buffer.
func arrowBits(valid ...bool) *memory.Buffer {
	b := make([]byte, (len(valid)+7)/8)
	for i, v := range valid {
		if v {
			b[i/8] |= 1 << (i % 8)
		}
	}
	return memory.NewBufferBytes(b)
}

func arrowRecord(t *testing.T, name string, arr arrow.Array, nullable bool, rows int64) arrow.RecordBatch {
	t.Helper()
	s := arrow.NewSchema([]arrow.Field{{Name: name, Type: arr.DataType(), Nullable: nullable}}, nil)
	rec := array.NewRecordBatch(s, []arrow.Array{arr}, rows)
	t.Cleanup(rec.Release)
	return rec
}

// TestANullListOwningElementsSumsToNull is the public face of import normalisation.
// Arrow lets a null list row cover elements; list.sum credits a row with every
// element in its range; so without normalisation a null list sums to a number.
func TestANullListOwningElementsSumsToNull(t *testing.T) {
	child, _, err := array.FromJSON(memory.NewGoAllocator(), arrow.PrimitiveTypes.Int64,
		strings.NewReader(`[1, 2, 100, 200, 300, 3]`))
	if err != nil {
		t.Fatal(err)
	}
	defer child.Release()
	offs := []int32{0, 2, 5, 6}
	d := array.NewData(arrow.ListOf(arrow.PrimitiveTypes.Int64), 3,
		[]*memory.Buffer{arrowBits(true, false, true),
			memory.NewBufferBytes(arrow.Int32Traits.CastToBytes(offs))},
		[]arrow.ArrayData{child.Data()}, 1, 0)
	list := array.MakeFromData(d)
	d.Release()
	defer list.Release()

	df, err := ursus.ScanArrowRecords(arrowRecord(t, "l", list, true, 3)).
		Select(ursus.Col("l").List().Sum().Alias("sum"), ursus.Col("l").List().Len().Alias("len")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want, err := ursus.Frame(
		ursus.ValuesNullable("sum", []int64{3, 0, 3}, []bool{true, false, true}),
		ursus.ValuesNullable("len", []int64{2, 0, 1}, []bool{true, false, true}),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, df, want, ursustest.IgnoreDTypes())
}

// TestANullStructsFieldIsNull: arrow-go's StructBuilder.AppendValues and pyarrow's
// from_arrays leave a null struct's fields valid.
func TestANullStructsFieldIsNull(t *testing.T) {
	age, _, err := array.FromJSON(memory.NewGoAllocator(), arrow.PrimitiveTypes.Int64,
		strings.NewReader(`[30, 40, 50]`))
	if err != nil {
		t.Fatal(err)
	}
	defer age.Release()
	d := array.NewData(arrow.StructOf(arrow.Field{Name: "age", Type: arrow.PrimitiveTypes.Int64}), 3,
		[]*memory.Buffer{arrowBits(true, false, true)}, []arrow.ArrayData{age.Data()}, 1, 0)
	st := array.MakeFromData(d)
	d.Release()
	defer st.Release()

	df, err := ursus.ScanArrowRecords(arrowRecord(t, "p", st, true, 3)).
		Select(ursus.Col("p").Struct().Field("age").Alias("age")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want, err := ursus.Frame(ursus.ValuesNullable("age", []int64{30, 0, 50}, []bool{true, false, true})).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, df, want)
}

// TestAnArrowFieldsNullableFlagIsNotTrusted: a producer declaring a column
// non-nullable while writing nulls into it — arrow.Field's zero value — must still
// get every null counted.
func TestAnArrowFieldsNullableFlagIsNotTrusted(t *testing.T) {
	v, _, err := array.FromJSON(memory.NewGoAllocator(), arrow.PrimitiveTypes.Int64,
		strings.NewReader(`[1, null, 3, null]`))
	if err != nil {
		t.Fatal(err)
	}
	defer v.Release()
	lf := ursus.ScanArrowRecords(arrowRecord(t, "v", v, false, 4))

	schema, err := lf.CollectSchema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if f, _ := schema.ByName("v"); !f.Nullable {
		t.Error("the column was declared non-nullable on the Arrow producer's word")
	}
	nulls, err := lf.Filter(ursus.Col("v").IsNull()).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if nulls.Height() != 2 {
		t.Errorf("found %d null rows, want 2", nulls.Height())
	}
}

// TestARecordShorterThanItsColumns: the rows past a record's NumRows are not part of
// it.
func TestARecordShorterThanItsColumns(t *testing.T) {
	v, _, err := array.FromJSON(memory.NewGoAllocator(), arrow.PrimitiveTypes.Int64,
		strings.NewReader(`[1, 2, 3, 4, 5]`))
	if err != nil {
		t.Fatal(err)
	}
	defer v.Release()
	df, err := ursus.ScanArrowRecords(arrowRecord(t, "v", v, true, 3)).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 3 {
		t.Errorf("a 3-row record over 5-row arrays gave %d rows", df.Height())
	}
}

// TestFramesRoundTripThroughArrow: export, IPC, import. The List frame is taken with
// Tail, so its column arrives with absolute offsets into a child holding rows before
// the window, and the Sum is Int128, which only survives through the field mark.
func TestFramesRoundTripThroughArrow(t *testing.T) {
	sums, err := salesFrame().
		GroupBy(ursus.Col("region")).
		Agg(ursus.Col("qty").Sum().Alias("total")).
		Sort(ursus.Asc(ursus.Col("region"))).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tail, err := slicedListFrame(t).Tail(t.Context(), 4)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name string
		df   *ursus.DataFrame
	}{{"an Int128 sum", sums}, {"a sliced List frame", tail}} {
		t.Run(c.name, func(t *testing.T) {
			rec, err := c.df.Record()
			if err != nil {
				t.Fatal(err)
			}
			defer rec.Release()
			r, err := ipc.NewReader(bytes.NewReader(ipcBytes(t, []arrow.RecordBatch{rec})))
			if err != nil {
				t.Fatal(err)
			}
			defer r.Release()
			if !r.Next() {
				t.Fatalf("no record: %v", r.Err())
			}
			back, err := ursus.ScanArrowRecords(r.RecordBatch()).Collect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			ursustest.AssertFrameEqual(t, back, c.df)
		})
	}

	if f, _ := sums.Schema().ByName("total"); f.Type != dtype.Int128 {
		t.Fatalf("the fixture's sum is %s, not Int128, so the mark is not exercised", f.Type)
	}
}

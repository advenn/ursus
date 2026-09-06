package parquet

// Reading a repeated column is the one place in this reader where a row does not
// correspond to a level, so these drive listCol's state machine directly.

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/schema"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/source"
)

// row is one list-column row: values, and whether the row itself is non-null.
// A present-but-empty row is {vals: nil, ok: true}; a null row is {ok: false}.
type row struct {
	vals []int64
	ok   bool
}

// writeLists builds a one-column file of `optional group (LIST) { repeated group
// list { optional int64 element } }` and emits the def/rep levels by hand.
//
// The level encoding IS the thing under test, so it is spelled out here rather
// than delegated:
//
//	def 3  an element is present
//	def 2  a null element inside a present list
//	def 1  a present but EMPTY list        <- consumes no element
//	def 0  a null list                     <- consumes no element
//	rep 0  this level starts a new row
//	rep 1  this level continues the row already open
func writeLists(t *testing.T, rows []row) string {
	t.Helper()

	opt := parquet.Repetitions.Optional
	el, err := schema.NewPrimitiveNodeLogical("element", opt,
		schema.NewIntLogicalType(64, true), parquet.Types.Int64, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	lst, err := schema.ListOf(el, opt, -1)
	if err != nil {
		t.Fatal(err)
	}
	tags, err := schema.NewGroupNodeLogical("tags", opt,
		schema.FieldList{lst.Field(0)}, schema.NewListLogicalType(), -1)
	if err != nil {
		t.Fatal(err)
	}
	root, err := schema.NewGroupNode("schema", parquet.Repetitions.Required,
		schema.FieldList{tags}, -1)
	if err != nil {
		t.Fatal(err)
	}

	var vals []int64
	var defs, reps []int16
	for _, r := range rows {
		switch {
		case !r.ok:
			defs, reps = append(defs, 0), append(reps, 0)
		case len(r.vals) == 0:
			defs, reps = append(defs, 1), append(reps, 0)
		default:
			for j, v := range r.vals {
				vals = append(vals, v)
				defs = append(defs, 3)
				if j == 0 {
					reps = append(reps, 0)
				} else {
					reps = append(reps, 1)
				}
			}
		}
	}

	path := filepath.Join(t.TempDir(), "lists.parquet")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := file.NewParquetWriter(f, root)
	rg := w.AppendRowGroup()
	cw, err := rg.NextColumn()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cw.(*file.Int64ColumnChunkWriter).WriteBatch(vals, defs, reps); err != nil {
		t.Fatal(err)
	}
	// w.Close() closes the underlying file too, so f is not closed here.
	for _, c := range []interface{ Close() error }{cw, rg, w} {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// readLists reads the whole file back through the ordinary scan path.
func readLists(t *testing.T, path string, batch int) []row {
	t.Helper()
	s := openSource(t, path)
	bs, err := s.Open(t.Context(), source.ScanSpec{BatchSize: batch})
	if err != nil {
		t.Fatalf("a list column must be readable: %v", err)
	}
	defer bs.Close()

	var out []row
	for {
		b, err := bs.Next(t.Context())
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, decodeLists(t, b.Columns()[0])...)
	}
}

func decodeLists(t *testing.T, c *data.Column) []row {
	t.Helper()
	if c.DType() != dtype.List(dtype.Int64) {
		t.Fatalf("column typed %s, want List(Int64)", c.DType())
	}
	acc := c.Lists()
	elems, err := data.Values[int64](acc.Child())
	if err != nil {
		t.Fatal(err)
	}
	out := make([]row, 0, c.Len())
	for i := range c.Len() {
		start, end, ok := acc.Get(i)
		if !ok {
			out = append(out, row{ok: false})
			continue
		}
		// nil rather than an empty slice, so an empty list and a null list are
		// distinguished by `ok` alone and never by slice identity.
		var vals []int64
		if end > start {
			vals = append(vals, elems[start:end]...)
		}
		out = append(out, row{vals: vals, ok: true})
	}
	return out
}

func sameRows(a, b []row) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ok != b[i].ok || len(a[i].vals) != len(b[i].vals) {
			return false
		}
		for j := range a[i].vals {
			if a[i].vals[j] != b[i].vals[j] {
				return false
			}
		}
	}
	return true
}

// TestListRoundTripsEveryRowState is the state machine, end to end.
//
// The four states all have to be distinguishable, and two of them — an empty list
// and a null list — append no elements and leave the offset unmoved. Only the
// validity bit separates those, exactly as for an empty string against a null
// string, and getting it wrong is one row quietly holding the wrong answer.
func TestListRoundTripsEveryRowState(t *testing.T) {
	want := []row{
		{vals: []int64{10, 11}, ok: true},
		{ok: true},                    // present but EMPTY
		{ok: false},                   // NULL list
		{vals: []int64{12}, ok: true}, // back to a normal row afterwards
		{vals: []int64{0}, ok: true},  // a list holding a zero is not an empty list
		{ok: true},                    // two empties in a row
		{ok: true},
		{vals: []int64{-1, -2, -3}, ok: true},
	}
	got := readLists(t, writeLists(t, want), 8192)
	if !sameRows(got, want) {
		t.Errorf("round trip changed the rows\n got: %+v\nwant: %+v", got, want)
	}
}

// TestListSpansTheReadBlock is the case a fixture of short lists cannot produce.
//
// listCol refills its levels a block at a time and a row is closed only when the
// NEXT rep==0 arrives, so a row longer than the block is assembled across two
// ReadBatch calls. If `open` did not survive a refill, this row would be split in
// two — and every fixture with lists shorter than listLevelBlock would pass.
func TestListSpansTheReadBlock(t *testing.T) {
	long := make([]int64, listLevelBlock+37)
	for i := range long {
		long[i] = int64(i)
	}
	want := []row{
		{vals: []int64{1}, ok: true},
		{vals: long, ok: true},
		{vals: []int64{2}, ok: true},
	}
	got := readLists(t, writeLists(t, want), 8192)
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3 — a row split at the block boundary", len(got))
	}
	if !sameRows(got, want) {
		t.Errorf("the long row was not reassembled: lengths %d, %d, %d",
			len(got[0].vals), len(got[1].vals), len(got[2].vals))
	}
}

// TestListIsBatchSizeInvariant: batch size is a tuning knob, never a semantic one.
// Small sizes split rows across batches, which is what exercises Concat's offset
// shifting.
func TestListIsBatchSizeInvariant(t *testing.T) {
	want := []row{
		{vals: []int64{1, 2}, ok: true},
		{ok: true},
		{ok: false},
		{vals: []int64{3}, ok: true},
		{vals: []int64{4, 5, 6}, ok: true},
	}
	path := writeLists(t, want)
	for _, size := range []int{1, 2, 3, 5, 8192} {
		got := readLists(t, path, size)
		if !sameRows(got, want) {
			t.Errorf("batch size %d changed the rows\n got: %+v\nwant: %+v",
				size, got, want)
		}
	}
}

// --- List(String) --------------------------------------------------------------

// srow is one row of a string list. elems carries the values; null[i] marks an
// element that is NULL rather than empty, which is a state only a nullable
// element type has.
type srow struct {
	elems []string
	null  []bool
	ok    bool // the ROW is non-null
}

// writeStringLists mirrors writeLists for BYTE_ARRAY elements.
//
//	def 3  an element is present     def 1  a present but EMPTY list
//	def 2  a NULL element            def 0  a null list
func writeStringLists(t *testing.T, rows []srow) string {
	t.Helper()

	opt := parquet.Repetitions.Optional
	el, err := schema.NewPrimitiveNodeLogical("element", opt,
		schema.StringLogicalType{}, parquet.Types.ByteArray, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	lst, err := schema.ListOf(el, opt, -1)
	if err != nil {
		t.Fatal(err)
	}
	tags, err := schema.NewGroupNodeLogical("tags", opt,
		schema.FieldList{lst.Field(0)}, schema.NewListLogicalType(), -1)
	if err != nil {
		t.Fatal(err)
	}
	root, err := schema.NewGroupNode("schema", parquet.Repetitions.Required,
		schema.FieldList{tags}, -1)
	if err != nil {
		t.Fatal(err)
	}

	var vals []parquet.ByteArray
	var defs, reps []int16
	for _, r := range rows {
		switch {
		case !r.ok:
			defs, reps = append(defs, 0), append(reps, 0)
		case len(r.elems) == 0:
			defs, reps = append(defs, 1), append(reps, 0)
		default:
			for j, v := range r.elems {
				isNull := j < len(r.null) && r.null[j]
				if isNull {
					defs = append(defs, 2)
				} else {
					vals = append(vals, parquet.ByteArray(v))
					defs = append(defs, 3)
				}
				if j == 0 {
					reps = append(reps, 0)
				} else {
					reps = append(reps, 1)
				}
			}
		}
	}

	path := filepath.Join(t.TempDir(), "strlists.parquet")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := file.NewParquetWriter(f, root)
	rg := w.AppendRowGroup()
	cw, err := rg.NextColumn()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cw.(*file.ByteArrayColumnChunkWriter).WriteBatch(vals, defs, reps); err != nil {
		t.Fatal(err)
	}
	for _, c := range []interface{ Close() error }{cw, rg, w} {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func readStringLists(t *testing.T, path string, batch int) []srow {
	t.Helper()
	s := openSource(t, path)
	bs, err := s.Open(t.Context(), source.ScanSpec{BatchSize: batch})
	if err != nil {
		t.Fatalf("a List(String) column must be readable: %v", err)
	}
	defer bs.Close()

	var out []srow
	for {
		b, err := bs.Next(t.Context())
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		c := b.Columns()[0]
		if c.DType() != dtype.List(dtype.String) {
			t.Fatalf("column typed %s, want List(String)", c.DType())
		}
		acc := c.Lists()
		child, err := data.TypedColumn[string](acc.Child())
		if err != nil {
			t.Fatal(err)
		}
		for i := range c.Len() {
			start, end, ok := acc.Get(i)
			if !ok {
				out = append(out, srow{ok: false})
				continue
			}
			r := srow{ok: true}
			for e := start; e < end; e++ {
				v, present := child.Get(int(e))
				r.elems = append(r.elems, v)
				r.null = append(r.null, !present)
			}
			out = append(out, r)
		}
	}
}

func sameSRows(a, b []srow) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ok != b[i].ok || len(a[i].elems) != len(b[i].elems) {
			return false
		}
		for j := range a[i].elems {
			an := j < len(a[i].null) && a[i].null[j]
			bn := j < len(b[i].null) && b[i].null[j]
			if an != bn {
				return false
			}
			if !an && a[i].elems[j] != b[i].elems[j] {
				return false
			}
		}
	}
	return true
}

// TestStringListRoundTrips covers the four row states plus the two an element type
// with a length adds: an EMPTY STRING element, which is not an empty list and not
// a null; and a NULL element, which occupies a slot and moves the element offset
// but not the byte offset.
func TestStringListRoundTrips(t *testing.T) {
	want := []srow{
		{elems: []string{"a", "bb"}, null: []bool{false, false}, ok: true},
		{ok: true},  // present but EMPTY list
		{ok: false}, // NULL list
		{elems: []string{""}, null: []bool{false}, ok: true},            // an empty STRING
		{elems: []string{"x", ""}, null: []bool{true, false}, ok: true}, // a NULL element
		{elems: []string{"héllo", "日本語", "🌍"}, null: []bool{false, false, false}, ok: true},
		{elems: []string{"tail"}, null: []bool{false}, ok: true},
	}
	for _, size := range []int{1, 2, 3, 8192} {
		got := readStringLists(t, writeStringLists(t, want), size)
		if !sameSRows(got, want) {
			t.Errorf("batch size %d changed the rows\n got: %+v\nwant: %+v",
				size, got, want)
		}
	}
}

// TestStringListSpansTheReadBlock is the cursor, for the new accumulator: a row
// longer than listLevelBlock is assembled across two refills.
func TestStringListSpansTheReadBlock(t *testing.T) {
	long := make([]string, listLevelBlock+37)
	nulls := make([]bool, len(long))
	for i := range long {
		long[i] = "e" + itoa(i) // multi-byte lengths, so byte offsets vary per element
	}
	want := []srow{
		{elems: []string{"first"}, null: []bool{false}, ok: true},
		{elems: long, null: nulls, ok: true},
		{elems: []string{"last"}, null: []bool{false}, ok: true},
	}
	got := readStringLists(t, writeStringLists(t, want), 8192)
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3 — a row split at the block boundary", len(got))
	}
	if !sameSRows(got, want) {
		t.Errorf("the long row was not reassembled: lengths %d, %d, %d",
			len(got[0].elems), len(got[1].elems), len(got[2].elems))
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}

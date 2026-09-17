package arrowin_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/arrowin"
	"github.com/advenn/ursus/internal/arrowout"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// roundTripBatch holds the types whose export is not the identity — Int128 and
// Decimal swap words, Time(ms) narrows to Time32 — and an Int128 two lists and a
// struct deep, which is where the field mark has to survive.
func roundTripBatch(t *testing.T) *data.Batch {
	t.Helper()
	const n = 6
	all := bitmap.AllSet(n)
	someNull := func() bitmap.View {
		b := bitmap.NewBuilder(n)
		for i := range n {
			b.Append(i != 2)
		}
		return b.Finish()
	}

	big := []i128.Int128{{Hi: -1, Lo: 7}, {Hi: 1 << 40, Lo: 3}, {}, {Hi: 0, Lo: 1 << 63}, {Hi: -9, Lo: 0}, {Hi: 5, Lo: 5}}
	ints := data.NewFixed("big", dtype.Int128, big, someNull())
	dec := data.NewFixed("dec", dtype.Decimal(20, 3), big, all)
	clock := data.NewFixed("clock", dtype.Time(dtype.Milli), []int64{0, 1, 86_399_999, 0, 3_600_000, 5}, someNull())
	words := data.NewString("words", []string{"a", "bb", "", "dddd", "e", "ff"}, someNull())

	// deep: List(Struct{xs: List(Int128)}), one struct per row, xs of i%3 elements.
	var xsElems []i128.Int128
	xsOffs := []int32{0}
	for i := range n {
		for k := range i % 3 {
			xsElems = append(xsElems, i128.Int128{Hi: int64(i), Lo: uint64(k)})
		}
		xsOffs = append(xsOffs, int32(len(xsElems)))
	}
	xs := data.NewList("xs", xsOffs, data.NewFixed("item", dtype.Int128, xsElems, bitmap.AllSet(len(xsElems))), all)
	st := data.NewStruct("item", []*data.Column{xs}, all)
	offs := make([]int32, n+1)
	for i := range offs {
		offs[i] = int32(i)
	}
	deep := data.NewList("deep", offs, st, someNull())

	cols := []*data.Column{ints, dec, clock, words, deep}
	fields := make([]dtype.Field, len(cols))
	for i, c := range cols {
		fields[i] = dtype.Field{Name: c.Name(), Type: c.DType(), Nullable: true}
	}
	schema, err := dtype.NewSchema(fields...)
	if err != nil {
		t.Fatal(err)
	}
	b, err := data.NewBatch(schema, cols)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestRoundTripThroughIPC: ursus → Arrow → IPC bytes → Arrow → ursus, over the whole
// batch and over a slice of it, so every nested column arrives with absolute
// offsets into a child that holds rows before and after the window.
func TestRoundTripThroughIPC(t *testing.T) {
	full := roundTripBatch(t)
	for _, c := range []struct {
		name  string
		batch *data.Batch
	}{{"whole", full}, {"sliced", full.Slice(1, 4)}} {
		t.Run(c.name, func(t *testing.T) {
			mem := memory.NewCheckedAllocator(memory.NewGoAllocator())
			defer mem.AssertSize(t, 0)

			sent, err := arrowout.Record(c.batch)
			if err != nil {
				t.Fatal(err)
			}
			defer sent.Release()

			var buf bytes.Buffer
			w := ipc.NewWriter(&buf, ipc.WithSchema(sent.Schema()), ipc.WithAllocator(mem))
			if err := w.Write(sent); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			r, err := ipc.NewReader(&buf, ipc.WithAllocator(mem))
			if err != nil {
				t.Fatal(err)
			}
			defer r.Release()
			if !r.Next() {
				t.Fatalf("no record: %v", r.Err())
			}
			rec := r.RecordBatch()

			schema, err := arrowin.Schema(r.Schema())
			if err != nil {
				t.Fatal(err)
			}
			if !schema.Equal(c.batch.Schema()) {
				t.Fatalf("the schema came back as %s, want %s — the Int128 mark was lost somewhere",
					schema, c.batch.Schema())
			}
			if err := arrowin.NewChecker(schema).Check(rec); err != nil {
				t.Fatal(err)
			}
			cols := make([]int, schema.Len())
			for i := range cols {
				cols[i] = i
			}
			got, err := arrowin.Batch(rec, schema, cols, 0, int(rec.NumRows()))
			if err != nil {
				t.Fatal(err)
			}

			again, err := arrowout.Record(got)
			if err != nil {
				t.Fatal(err)
			}
			defer again.Release()
			for i := range schema.Len() {
				if !array.Equal(again.Column(i), sent.Column(i)) {
					t.Errorf("column %q changed:\n sent: %v\n back: %v",
						schema.Field(i).Name, sent.Column(i), again.Column(i))
				}
			}
		})
	}
}

// TestBatchReadsTheRecordsRowsNotTheArrays: arrow-go lets a column be longer than
// its record, and the rows past the record are not part of it.
func TestBatchReadsTheRecordsRowsNotTheArrays(t *testing.T) {
	arr := int64s(t, []int64{1, 2, 3, 4, 5})
	s := arrow.NewSchema([]arrow.Field{{Name: "v", Type: i64}}, nil)
	rec := array.NewRecordBatch(s, []arrow.Array{arr}, 3)
	defer rec.Release()

	schema, err := arrowin.Schema(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := arrowin.NewChecker(schema).Check(rec); err != nil {
		t.Fatal(err)
	}
	b, err := arrowin.Batch(rec, schema, []int{0}, 0, int(rec.NumRows()))
	if err != nil {
		t.Fatal(err)
	}
	if b.Rows() != 3 {
		t.Errorf("a 3-row record over 5-row arrays gave %d rows", b.Rows())
	}
}

// fakeRecord substitutes one column, to build what arrow-go's own constructor refuses.
type fakeRecord struct {
	arrow.RecordBatch
	col arrow.Array
}

func (f fakeRecord) Column(int) arrow.Array { return f.col }

func TestCheckerRefusals(t *testing.T) {
	mem := memory.NewGoAllocator()
	ints := int64s(t, []int64{1, 2, 3})
	s := arrow.NewSchema([]arrow.Field{{Name: "v", Type: i64}}, nil)
	want, err := arrowin.Schema(s)
	if err != nil {
		t.Fatal(err)
	}
	live := array.NewRecordBatch(s, []arrow.Array{ints}, 3)
	defer live.Release()

	released := array.NewRecordBatch(s, []arrow.Array{ints}, 3)
	released.Release()

	dead := array.NewData(i64, 3, []*memory.Buffer{nil, bytesOf([]int64{1, 2, 3})}, nil, 0, 0)
	releasedArr := array.MakeFromData(dead)
	dead.Release()
	releasedArr.Release()

	other := arrow.NewSchema([]arrow.Field{{Name: "v", Type: str}}, nil)
	strs := fromJSON(t, mem, str, `["a", "b", "c"]`)
	otherRec := array.NewRecordBatch(other, []arrow.Array{strs}, 3)
	defer otherRec.Release()

	for _, c := range []struct {
		name string
		rec  arrow.RecordBatch
		kind error
		says string
	}{
		{"nil", nil, uerr.ErrValue, "nil"},
		{"a released record", released, uerr.ErrValue, "was released"},
		{"a different schema", otherRec, uerr.ErrSchema, "differs from the scan's"},
		{"an array of another type than its field", fakeRecord{live, strs}, uerr.ErrSchema, "is declared int64 and holds utf8"},
		{"a released array inside a live record", fakeRecord{live, releasedArr}, uerr.ErrValue, "released"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := arrowin.NewChecker(want).Check(c.rec)
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
	if err := arrowin.NewChecker(want).Check(live); err != nil {
		t.Errorf("the live record itself was refused: %v", err)
	}
}

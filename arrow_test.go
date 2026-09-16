package ursus_test

// The Arrow export from the outside: what a user actually does with it.
//
// internal/arrowout covers the type map exhaustively. These are the assertions that
// only the public surface can make — that a real query's output converts, that the
// result is consumable by the rest of the Arrow ecosystem rather than merely
// well-formed, and that streaming a larger-than-one-batch result works.

import (
	"bytes"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus"
)

func salesFrame() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.Values("region", []string{"eu", "us", "eu", "us", "eu"}),
		ursus.Values("qty", []int64{1, 2, 3, 4, 5}),
	)
}

// TestGroupBySumExportsItsInt128 is the assertion that justifies the word swap.
//
// EVERY integer Sum outputs Int128 — so the single most ordinary analytical query
// there is produces a column with no Arrow integer equivalent, exported as
// Decimal128(38,0). ursus stores {Hi, Lo}; Arrow stores {lo, hi}. A zero-copy wrap
// would hand back a valid Decimal128 array in which eu's 9 is 9*2^64.
func TestGroupBySumExportsItsInt128(t *testing.T) {
	df, err := salesFrame().
		GroupBy(ursus.Col("region")).
		Agg(ursus.Col("qty").Sum().Alias("total")).
		Sort(ursus.Asc(ursus.Col("region"))).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	rec, err := df.Record()
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Release()

	dec, ok := rec.Column(1).(*array.Decimal128)
	if !ok {
		t.Fatalf("the Sum exported as %T, want *array.Decimal128", rec.Column(1))
	}
	// eu = 1+3+5, us = 2+4.
	for i, want := range []uint64{9, 6} {
		v := dec.Value(i)
		if v.HighBits() != 0 || v.LowBits() != want {
			t.Errorf("row %d = {hi:%d lo:%d}, want %d — the 128-bit halves are "+
				"swapped, so the value is out by 2^64",
				i, v.HighBits(), v.LowBits(), want)
		}
	}
}

// TestRecordSurvivesAnIPCRoundTrip is the claim that the export is consumable, not
// merely well-formed. arrow-go's IPC writer is an independent reader of the layout:
// if the offsets, validity or widths were wrong in a way ursus's own accessors
// tolerate, this is where it shows.
//
// It also happens to be Arrow IPC / Feather output, which ursus does not otherwise
// have — for the cost of the writer the caller already has.
func TestRecordSurvivesAnIPCRoundTrip(t *testing.T) {
	df, err := salesFrame().
		Select(
			ursus.Col("region"),
			ursus.Col("qty"),
			ursus.Col("qty").Gt(2).Alias("big"),
			ursus.Col("qty").Cast(ursus.Float64).Alias("f"),
		).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rec, err := df.Record()
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Release()

	var buf bytes.Buffer
	w := ipc.NewWriter(&buf, ipc.WithSchema(rec.Schema()),
		ipc.WithAllocator(memory.NewGoAllocator()))
	if err := w.Write(rec); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := ipc.NewReader(&buf, ipc.WithAllocator(memory.NewGoAllocator()))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Release()
	if !r.Next() {
		t.Fatalf("no record came back: %v", r.Err())
	}
	got := r.Record()

	if got.NumRows() != 5 || got.NumCols() != 4 {
		t.Fatalf("round trip gave %dx%d, want 5x4", got.NumRows(), got.NumCols())
	}
	if s := got.Column(0).(*array.String); s.Value(0) != "eu" || s.Value(4) != "eu" {
		t.Errorf("region round-tripped as %q..%q", s.Value(0), s.Value(4))
	}
	if b := got.Column(2).(*array.Boolean); b.Value(0) || !b.Value(2) {
		t.Errorf("the boolean column round-tripped wrongly: %v", got.Column(2))
	}
	if f := got.Column(3).(*array.Float64); f.Value(4) != 5 {
		t.Errorf("the float column round-tripped as %v", f.Value(4))
	}
}

// TestNullsSurviveTheExport, because validity is the half of the layout that a
// "looks right" test misses: every value can be correct while every null is lost.
func TestNullsSurviveTheExport(t *testing.T) {
	df, err := ursus.Frame(
		ursus.ValuesNullable("v", []int64{1, 0, 3}, []bool{true, false, true}),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rec, err := df.Record()
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Release()

	col := rec.Column(0)
	if col.NullN() != 1 {
		t.Errorf("%d nulls, want 1", col.NullN())
	}
	if !col.IsNull(1) {
		t.Error("row 1 was null and is not null after export")
	}
	if col.IsNull(0) || col.IsNull(2) {
		t.Error("a valid row became null")
	}
}

// TestCollectRecordsStreams checks the iterator form, which is what makes a result
// bigger than memory exportable.
func TestCollectRecordsStreams(t *testing.T) {
	const rows = 1000
	vals := make([]int64, rows)
	for i := range vals {
		vals[i] = int64(i)
	}

	var seen, batches int64
	for rec, err := range ursus.Frame(ursus.Values("v", vals)).
		CollectRecords(t.Context(), ursus.WithBatchSize(128)) {
		if err != nil {
			t.Fatal(err)
		}
		batches++
		seen += rec.NumRows()
		rec.Release()
	}
	if seen != rows {
		t.Errorf("streamed %d rows, want %d", seen, rows)
	}
	if batches < 2 {
		t.Errorf("got %d record(s) at batch size 128 over %d rows — the iterator "+
			"is not streaming and this test proves nothing", batches, rows)
	}
}

// TestArrowSchemaIsAvailableWithoutRunning: asking what the Arrow schema would be is
// how a caller finds out about an unexportable type before doing the work.
func TestArrowSchemaIsAvailableWithoutRunning(t *testing.T) {
	df, err := salesFrame().Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	s, err := df.ArrowSchema()
	if err != nil {
		t.Fatal(err)
	}
	if s.Field(0).Name != "region" || s.Field(0).Type.ID() != arrow.STRING {
		t.Errorf("field 0 is %s", s.Field(0))
	}
	if s.Field(1).Type.ID() != arrow.INT64 {
		t.Errorf("field 1 is %s", s.Field(1))
	}
}

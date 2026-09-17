package ursus_test

// ScanArrow: a stream, opened once per scan, copied a window at a time.

import (
	"bytes"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/ursustest"
)

// streamCounts is what a scan asked of its factory and readers.
type streamCounts struct{ opens, nexts, releases atomic.Int64 }

type countedReader struct {
	array.RecordReader
	c *streamCounts
}

func (r *countedReader) Next() bool {
	r.c.nexts.Add(1)
	return r.RecordReader.Next()
}

func (r *countedReader) Release() {
	r.c.releases.Add(1)
	r.RecordReader.Release()
}

// ipcFactory opens a fresh IPC reader over the same bytes on every call.
func ipcFactory(stream []byte, mem memory.Allocator, c *streamCounts) func() (array.RecordReader, error) {
	return func() (array.RecordReader, error) {
		c.opens.Add(1)
		r, err := ipc.NewReader(bytes.NewReader(stream), ipc.WithAllocator(mem))
		if err != nil {
			return nil, err
		}
		return &countedReader{RecordReader: r, c: c}, nil
	}
}

// TestScanArrowCopiesBeforeTheReaderMovesOn: an IPC reader frees each record's body
// when it reads the next one, and every body lives in a poisoning allocator. Records
// are ten rows and batches three, so a record is read across several Nexts — a scan
// that released it, or let the reader advance, before its last window would read
// poison in the windows after.
func TestScanArrowCopiesBeforeTheReaderMovesOn(t *testing.T) {
	sent := importRecords(t)
	p := &poisoner{inner: memory.NewGoAllocator()}
	mem := memory.NewCheckedAllocator(p)
	var c streamCounts

	df, err := ursus.ScanArrow(ipcFactory(ipcBytes(t, sent), mem, &c)).
		Collect(t.Context(), ursus.WithBatchSize(3))
	if err != nil {
		t.Fatal(err)
	}

	// Before comparing anything: every reader went back, all memory with it, and
	// that memory was overwritten on the way.
	if o, r := c.opens.Load(), c.releases.Load(); o != 2 || r != 2 {
		t.Errorf("opens=%d releases=%d, want the schema probe and one scan, each released", o, r)
	}
	mem.AssertSize(t, 0)
	if p.bytesFreed() == 0 {
		t.Fatal("nothing was freed through the poisoning allocator, so this proves nothing")
	}

	assertMatchesRecords(t, df, sent)
}

// TestScanArrowIsReopenedPerScan: a self-join scans twice in one query, and a frame
// collected twice runs twice. Both would read an empty second pass from a reused
// reader.
func TestScanArrowIsReopenedPerScan(t *testing.T) {
	sent := importRecords(t)
	var c streamCounts
	lf := ursus.ScanArrow(ipcFactory(ipcBytes(t, sent), memory.NewGoAllocator(), &c))

	joined, err := lf.Select(ursus.Col("id"), ursus.Col("name")).
		Join(lf.Select(ursus.Col("id"), ursus.Col("score")), ursus.JoinOn(ursus.Col("id"))).
		Sort(ursus.Asc(ursus.Col("id"))).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if joined.Height() != 18 {
		t.Errorf("the self-join has %d rows, want 18 — one per non-null id", joined.Height())
	}

	first, err := lf.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := lf.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, second, first)
	assertMatchesRecords(t, second, sent)

	if o, r := c.opens.Load(), c.releases.Load(); o != r {
		t.Errorf("%d readers opened and %d released", o, r)
	}
}

// TestScanArrowStopsEarly: Head asks for no record past the one holding its rows,
// and a caller breaking out of CollectBatches releases the reader.
func TestScanArrowStopsEarly(t *testing.T) {
	sent := importRecords(t)
	stream := ipcBytes(t, sent)

	var head streamCounts
	df, err := ursus.ScanArrow(ipcFactory(stream, memory.NewGoAllocator(), &head)).Head(2).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 2 {
		t.Errorf("Head(2) gave %d rows", df.Height())
	}
	if n := head.nexts.Load(); n != 1 {
		t.Errorf("Head(2) asked the stream for %d records; both rows are in the first", n)
	}
	if o, r := head.opens.Load(), head.releases.Load(); o != r {
		t.Errorf("Head: %d readers opened and %d released", o, r)
	}

	var brk streamCounts
	mem := memory.NewCheckedAllocator(memory.NewGoAllocator())
	for _, err := range ursus.ScanArrow(ipcFactory(stream, mem, &brk)).CollectBatches(t.Context(), ursus.WithBatchSize(3)) {
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	if o, r := brk.opens.Load(), brk.releases.Load(); o != 2 || r != 2 {
		t.Errorf("after breaking out: opens=%d releases=%d, want 2 and 2", o, r)
	}
	mem.AssertSize(t, 0)
}

// TestPlanningAScanReadsNoRecord: Explain and CollectSchema learn the schema from a
// reader and release it without reading.
func TestPlanningAScanReadsNoRecord(t *testing.T) {
	stream := ipcBytes(t, importRecords(t))
	var c streamCounts
	lf := ursus.ScanArrow(ipcFactory(stream, memory.NewGoAllocator(), &c)).Select(ursus.Col("id"))

	if _, err := lf.Explain(t.Context()); err != nil {
		t.Fatal(err)
	}
	schema, err := lf.CollectSchema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if f, _ := schema.ByName("id"); !f.Nullable {
		t.Error("id is declared non-nullable")
	}
	if o, n, r := c.opens.Load(), c.nexts.Load(), c.releases.Load(); o != 1 || n != 0 || r != 1 {
		t.Errorf("opens=%d nexts=%d releases=%d, want 1, 0, 1", o, n, r)
	}

	ursustest.AssertPlan(t, lf, "testdata/plans/arrow_scan.txt")
}

func TestScanArrowRefusals(t *testing.T) {
	if _, err := ursus.ScanArrow(nil).Collect(t.Context()); !errors.Is(err, uerr.ErrValue) {
		t.Errorf("a nil open: %v", err)
	}

	rr, err := array.NewRecordReader(importSchema, importRecords(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = ursus.ScanArrow(func() (array.RecordReader, error) { return rr, nil }).Collect(t.Context())
	if !errors.Is(err, uerr.ErrValue) || !strings.Contains(err.Error(), "already returned") {
		t.Errorf("one reader returned twice: %v", err)
	}

	iv := arrow.NewSchema([]arrow.Field{{Name: "iv", Type: arrow.FixedWidthTypes.DayTimeInterval}}, nil)
	_, err = ursus.ScanArrow(func() (array.RecordReader, error) { return array.NewRecordReader(iv, nil) }).
		Collect(t.Context())
	if !errors.Is(err, uerr.ErrUnsupported) {
		t.Errorf("an unsupported type: %v", err)
	}
}

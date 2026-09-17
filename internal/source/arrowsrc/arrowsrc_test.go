package arrowsrc_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/source"
	"github.com/advenn/ursus/internal/source/arrowsrc"
	"github.com/advenn/ursus/internal/uerr"
)

var abc = arrow.NewSchema([]arrow.Field{
	{Name: "a", Type: arrow.PrimitiveTypes.Int64},
	{Name: "b", Type: arrow.BinaryTypes.String},
	{Name: "c", Type: arrow.PrimitiveTypes.Int64},
}, nil)

// records builds one record per size, numbering rows across all of them.
func records(t *testing.T, schema *arrow.Schema, sizes ...int) []arrow.RecordBatch {
	t.Helper()
	var out []arrow.RecordBatch
	row := 0
	for _, n := range sizes {
		var sb strings.Builder
		sb.WriteString("[")
		for i := range n {
			if i > 0 {
				sb.WriteString(",")
			}
			fmt.Fprintf(&sb, `{"a": %d, "b": "r%d", "c": %d}`, row, row, -row)
			row++
		}
		sb.WriteString("]")
		rec, _, err := array.RecordFromJSON(memory.NewGoAllocator(), schema, strings.NewReader(sb.String()))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(rec.Release)
		out = append(out, rec)
	}
	return out
}

// counting wraps a reader and counts what the source asks of it.
type counting struct {
	array.RecordReader
	c *counts
}

type counts struct{ opens, nexts, releases atomic.Int64 }

func (r *counting) Next() bool {
	r.c.nexts.Add(1)
	return r.RecordReader.Next()
}

func (r *counting) Release() {
	r.c.releases.Add(1)
	r.RecordReader.Release()
}

// factory returns a fresh in-memory reader over recs on every call.
func factory(schema *arrow.Schema, recs []arrow.RecordBatch, c *counts) func() (array.RecordReader, error) {
	return func() (array.RecordReader, error) {
		c.opens.Add(1)
		rr, err := array.NewRecordReader(schema, recs)
		if err != nil {
			return nil, err
		}
		return &counting{RecordReader: rr, c: c}, nil
	}
}

// drain reads every batch and returns their row counts and the concatenated values
// of column 0.
func drain(t *testing.T, bs source.BatchSource) (sizes []int, first []int64) {
	t.Helper()
	for {
		b, err := bs.Next(t.Context())
		if errors.Is(err, io.EOF) {
			return sizes, first
		}
		if err != nil {
			t.Fatal(err)
		}
		sizes = append(sizes, b.Rows())
		v, err := data.Values[int64](b.Column(0))
		if err != nil {
			t.Fatal(err)
		}
		first = append(first, v...)
	}
}

func TestSchemaOpensOneReaderAndReadsNoRecord(t *testing.T) {
	var c counts
	src := arrowsrc.New(factory(abc, records(t, abc, 4), &c))
	for range 3 {
		s, err := src.Schema(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if s.Len() != 3 {
			t.Fatalf("schema %s", s)
		}
	}
	if o, n, r := c.opens.Load(), c.nexts.Load(), c.releases.Load(); o != 1 || n != 0 || r != 1 {
		t.Errorf("opens=%d nexts=%d releases=%d, want 1, 0, 1", o, n, r)
	}
}

func TestACancelledSchemaIsNotCached(t *testing.T) {
	var c counts
	src := arrowsrc.New(factory(abc, records(t, abc, 1), &c))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := src.Schema(ctx); err == nil {
		t.Fatal("a cancelled context was ignored")
	}
	if _, err := src.Schema(t.Context()); err != nil {
		t.Fatalf("the cancellation was cached: %v", err)
	}
}

// TestOpenDoesNoIO: a scan planned and never read opens nothing, so abandoning it
// leaks nothing.
func TestOpenDoesNoIO(t *testing.T) {
	var c counts
	src := arrowsrc.New(factory(abc, records(t, abc, 4), &c))
	bs, err := src.Open(t.Context(), source.ScanSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if err := bs.Close(); err != nil {
		t.Fatal(err)
	}
	if o, r := c.opens.Load(), c.releases.Load(); o != 1 || r != 1 {
		t.Errorf("opens=%d releases=%d, want only the schema probe's 1 and 1", o, r)
	}
}

// TestBatchesAreWindowsOfRecords: batch boundaries fall inside records and records
// are never merged, an empty record is skipped rather than ending the stream, and
// the reader is released as soon as the stream ends.
func TestBatchesAreWindowsOfRecords(t *testing.T) {
	var c counts
	src := arrowsrc.New(factory(abc, records(t, abc, 10, 0, 4), &c))
	bs, err := src.Open(t.Context(), source.ScanSpec{BatchSize: 3})
	if err != nil {
		t.Fatal(err)
	}
	sizes, vals := drain(t, bs)
	if fmt.Sprint(sizes) != "[3 3 3 1 3 1]" {
		t.Errorf("batch sizes %v, want [3 3 3 1 3 1]", sizes)
	}
	if fmt.Sprint(vals) != "[0 1 2 3 4 5 6 7 8 9 10 11 12 13]" {
		t.Errorf("values %v", vals)
	}
	if o, r := c.opens.Load(), c.releases.Load(); o != 2 || r != 2 {
		t.Errorf("after the end: opens=%d releases=%d, want 2 and 2", o, r)
	}
	for range 2 {
		if err := bs.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if r := c.releases.Load(); r != 2 {
		t.Errorf("Close released again: %d releases", r)
	}
	if _, err := bs.Next(t.Context()); !errors.Is(err, uerr.ErrInternal) {
		t.Errorf("Next after Close: %v", err)
	}
}

// TestMaxRowsStopsAskingForRecords: Head(n) on a stream must not read the stream.
func TestMaxRowsStopsAskingForRecords(t *testing.T) {
	var c counts
	src := arrowsrc.New(factory(abc, records(t, abc, 10, 10, 10), &c))
	bs, err := src.Open(t.Context(), source.ScanSpec{BatchSize: 5, MaxRows: 12})
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()
	sizes, _ := drain(t, bs)
	if fmt.Sprint(sizes) != "[5 5 2]" {
		t.Errorf("batch sizes %v, want [5 5 2]", sizes)
	}
	if n := c.nexts.Load(); n != 2 {
		t.Errorf("the reader was asked for %d records; 12 rows are in the first 2", n)
	}
}

func TestProjectionSelectsAndOrders(t *testing.T) {
	var c counts
	src := arrowsrc.New(factory(abc, records(t, abc, 3), &c))
	bs, err := src.Open(t.Context(), source.ScanSpec{Projection: []string{"c", "a"}})
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()
	if got := bs.Schema().Names(); fmt.Sprint(got) != "[c a]" {
		t.Errorf("schema %v, want [c a]", got)
	}
	_, vals := drain(t, bs)
	if fmt.Sprint(vals) != "[0 -1 -2]" {
		t.Errorf("column c read %v, want [0 -1 -2]", vals)
	}
	if p := src.Projections(); fmt.Sprint(p) != "[[c a]]" {
		t.Errorf("Projections() = %v", p)
	}
}

// TestAFactoryReturningOneReaderIsRefused is the natural mistake: the schema probe
// releases the reader, and arrow-go's in-memory reader then reports no records and
// no error, which reads as an empty stream.
func TestAFactoryReturningOneReaderIsRefused(t *testing.T) {
	rr, err := array.NewRecordReader(abc, records(t, abc, 3))
	if err != nil {
		t.Fatal(err)
	}
	src := arrowsrc.New(func() (array.RecordReader, error) { return rr, nil })
	bs, err := src.Open(t.Context(), source.ScanSpec{})
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()
	_, err = bs.Next(t.Context())
	if !errors.Is(err, uerr.ErrValue) || !strings.Contains(err.Error(), "already returned") {
		t.Fatalf("got %v — a reused reader must be refused, not read as empty", err)
	}
}

// TestASchemaChangeBetweenOpensIsRefused: the query was planned against the first
// reader's schema; a later reader with its columns swapped would be read under the
// planned names.
func TestASchemaChangeBetweenOpensIsRefused(t *testing.T) {
	cab := arrow.NewSchema([]arrow.Field{abc.Field(2), abc.Field(1), abc.Field(0)}, nil)
	first, later := records(t, abc, 2), records(t, cab, 2)
	var calls atomic.Int64
	src := arrowsrc.New(func() (array.RecordReader, error) {
		if calls.Add(1) == 1 {
			return array.NewRecordReader(abc, first)
		}
		return array.NewRecordReader(cab, later)
	})
	bs, err := src.Open(t.Context(), source.ScanSpec{})
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()
	_, err = bs.Next(t.Context())
	if !errors.Is(err, uerr.ErrSchema) {
		t.Fatalf("got %v", err)
	}
}

// failing is a reader whose stream breaks after its schema.
type failing struct{ array.RecordReader }

func (failing) Next() bool { return false }
func (failing) Err() error { return errors.New("connection reset") }

func TestAStreamErrorSurfaces(t *testing.T) {
	src := arrowsrc.New(func() (array.RecordReader, error) {
		rr, err := array.NewRecordReader(abc, nil)
		return &failing{rr}, err
	})
	bs, err := src.Open(t.Context(), source.ScanSpec{})
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()
	if _, err := bs.Next(t.Context()); !errors.Is(err, uerr.ErrIO) || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("got %v", err)
	}
}

func TestOpenErrorsAndNilReaders(t *testing.T) {
	for _, c := range []struct {
		name string
		open func() (array.RecordReader, error)
		kind error
	}{
		{"an error", func() (array.RecordReader, error) { return nil, errors.New("no such stream") }, uerr.ErrIO},
		{"neither", func() (array.RecordReader, error) { return nil, nil }, uerr.ErrValue},
		{"a typed nil", func() (array.RecordReader, error) { return (*counting)(nil), nil }, uerr.ErrValue},
	} {
		if _, err := arrowsrc.New(c.open).Schema(t.Context()); !errors.Is(err, c.kind) {
			t.Errorf("%s: got %v", c.name, err)
		}
	}
}

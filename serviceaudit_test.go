package ursus_test

// What the audit of 0.4's service-safety work found (step 120).

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
)

// trackedStream counts the streams a CSVFile opened and closed.
type trackedStream struct {
	io.Reader
	closes *atomic.Int32
}

func (s trackedStream) Close() error { s.closes.Add(1); return nil }

type resetReader struct{}

func (resetReader) Read([]byte) (int, error) { return 0, errors.New("the connection was reset") }

// TestANonNullableCSVColumnWithAnEmptyCell: a schema given with WithSchema may
// declare a column non-nullable, and the file may leave a cell of it empty. Since
// step 98 checks that in production, the query failed as ursus's own bug.
func TestANonNullableCSVColumnWithAnEmptyCell(t *testing.T) {
	csv := func(body string) []ursus.CSVFile {
		return []ursus.CSVFile{{Name: "a.csv", Open: func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(body)), nil
		}}}
	}
	schema := ursus.WithSchema(ursus.MustSchema(dtype.NotNull("a", ursus.Int64), dtype.Of("b", ursus.String)))

	_, err := ursus.ScanCSVFrom(csv("a,b\n1,x\n,y\n3,z\n"), schema).Collect(t.Context())
	if !errors.Is(err, ursus.ErrValue) || !strings.Contains(err.Error(), `row 2 has no value for column "a"`) {
		t.Errorf("want a value error naming row 2 and the column, got %v", err)
	}
	// The control: the same file under a nullable declaration reads its null.
	got := runs(t, ursus.ScanCSVFrom(csv("a,b\n1,x\n,y\n3,z\n"),
		ursus.WithSchema(ursus.MustSchema(dtype.Of("a", ursus.Int64), dtype.Of("b", ursus.String)))), "a")
	if strings.Join(got, " ") != "1 ∅ 3" {
		t.Errorf("%v", got)
	}
}

// TestAFailedCSVOpenClosesItsStream: Open read the header from the stream it had
// just opened, and returned the error without closing it. With WithSchema the
// planner never reads the stream, so the scan's Open is the first, and a service
// retrying over a failing stream leaked one per attempt.
func TestAFailedCSVOpenClosesItsStream(t *testing.T) {
	var opens, closes atomic.Int32
	files := []ursus.CSVFile{{Name: "broken.csv", Open: func(context.Context) (io.ReadCloser, error) {
		opens.Add(1)
		return trackedStream{Reader: resetReader{}, closes: &closes}, nil
	}}}
	lf := ursus.ScanCSVFrom(files, ursus.WithSchema(ursus.MustSchema(dtype.Of("a", ursus.Int64))))
	if _, err := lf.Collect(t.Context()); err == nil {
		t.Fatal("a stream that fails on its first read gave no error")
	}
	if opens.Load() == 0 || closes.Load() != opens.Load() {
		t.Errorf("opened %d streams and closed %d", opens.Load(), closes.Load())
	}
}

// TestAPanicWhilePlanningClosesWhatWasOpened: step 95 closed what a plan had opened
// when it returned an error, and not when it panicked. Collect recovers a panic
// into an error, so the process carried on with the leak.
func TestAPanicWhilePlanningClosesWhatWasOpened(t *testing.T) {
	var opens, closes atomic.Int32
	healthy := []ursus.CSVFile{{Name: "ok.csv", Open: func(context.Context) (io.ReadCloser, error) {
		opens.Add(1)
		return trackedStream{Reader: strings.NewReader("a\n1\n"), closes: &closes}, nil
	}}}
	panics := []ursus.CSVFile{{Name: "nil-client.csv", Open: func(context.Context) (io.ReadCloser, error) {
		panic("a nil client")
	}}}
	schema := ursus.WithSchema(ursus.MustSchema(dtype.Of("a", ursus.Int64)))
	lf := ursus.Concat([]*ursus.LazyFrame{ursus.ScanCSVFrom(healthy, schema), ursus.ScanCSVFrom(panics, schema)})
	if _, err := lf.Collect(t.Context()); err == nil {
		t.Fatal("a panicking Open gave no error")
	}
	if opens.Load() == 0 {
		t.Fatal("the healthy input was never opened, so this tests nothing")
	}
	if closes.Load() != opens.Load() {
		t.Errorf("opened %d streams and closed %d", opens.Load(), closes.Load())
	}
}

package ursus_test

// Step 418: a source keeps its schema, but not a caller's cancellation. One
// cancelled Collect made every later Collect of the same LazyFrame fail, and callers
// waiting on another's schema read waited past their own deadlines.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

// TestACancelledCollectDoesNotPoisonTheFrame: CSV, NDJSON and Parquet from a caller's
// openers, the first open seeing its caller hang up: that Collect fails with the
// cancellation, and the next Collect of the same LazyFrame reads, where it gave the
// first one's context.Canceled for good.
func TestACancelledCollectDoesNotPoisonTheFrame(t *testing.T) {
	const rows = 50
	ids := make([]int64, rows)
	for i := range ids {
		ids[i] = int64(i)
	}
	src := ursus.Frame(ursus.Values("id", ids))
	var csvB, ndjsonB, pqB bytes.Buffer
	for _, w := range []func() error{
		func() error { return src.WriteCSV(t.Context(), &csvB) },
		func() error { return src.WriteNDJSON(t.Context(), &ndjsonB) },
		func() error { return src.WriteParquet(t.Context(), &pqB) },
	} {
		if err := w(); err != nil {
			t.Fatal(err)
		}
	}
	// hangUp cancels the first Collect from inside its first open, as a client that
	// goes away while the file is opened.
	var cancelFirst context.CancelFunc
	var opens atomic.Int32
	hangUp := func(ctx context.Context) error {
		if opens.Add(1) == 1 {
			cancelFirst()
			return ctx.Err()
		}
		return nil
	}
	frames := map[string]func() *ursus.LazyFrame{
		"CSV": func() *ursus.LazyFrame {
			return ursus.ScanCSVFrom([]ursus.CSVFile{{Name: "f.csv", Open: func(ctx context.Context) (io.ReadCloser, error) {
				if err := hangUp(ctx); err != nil {
					return nil, err
				}
				return io.NopCloser(bytes.NewReader(csvB.Bytes())), nil
			}}})
		},
		"NDJSON": func() *ursus.LazyFrame {
			return ursus.ScanNDJSONFrom([]ursus.NDJSONFile{{Name: "f.ndjson", Open: func(ctx context.Context) (io.ReadCloser, error) {
				if err := hangUp(ctx); err != nil {
					return nil, err
				}
				return io.NopCloser(bytes.NewReader(ndjsonB.Bytes())), nil
			}}})
		},
		"Parquet": func() *ursus.LazyFrame {
			return ursus.ScanParquetFrom([]ursus.ParquetFile{{Name: "f.parquet", Open: func(ctx context.Context) (io.ReaderAt, int64, error) {
				if err := hangUp(ctx); err != nil {
					return nil, 0, err
				}
				return bytes.NewReader(pqB.Bytes()), int64(pqB.Len()), nil
			}}})
		},
	}
	for name, make := range frames {
		opens.Store(0)
		lf := make()
		first, cancel := context.WithCancel(t.Context())
		cancelFirst = cancel
		if _, err := lf.Collect(first); !errors.Is(err, context.Canceled) {
			t.Errorf("%s: the first Collect: got %v, want context.Canceled", name, err)
		}
		cancel()
		df, err := lf.Collect(t.Context())
		if err != nil || df.Height() != rows {
			t.Errorf("%s: the next Collect: %v rows, %v; want %d", name, heightOrNone(df), err, rows)
		}
	}
}

// TestAWaiterKeepsItsOwnDeadline: while one Collect's schema read is held in its
// stream's Open, a second Collect of the same LazyFrame under a 100 ms deadline gives
// up when its deadline passes, where it waited as long as the first; the first then
// reads.
func TestAWaiterKeepsItsOwnDeadline(t *testing.T) {
	release := make(chan struct{})
	opened := make(chan struct{}, 1)
	lf := ursus.ScanCSVFrom([]ursus.CSVFile{{Name: "slow.csv", Open: func(ctx context.Context) (io.ReadCloser, error) {
		select {
		case opened <- struct{}{}:
		default:
		}
		<-release
		return io.NopCloser(strings.NewReader("a\n1\n2\n")), nil
	}}})
	first := make(chan error, 1)
	go func() {
		_, err := lf.Collect(context.Background())
		first <- err
	}()
	<-opened

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	second := make(chan error, 1)
	go func() {
		_, err := lf.Collect(ctx)
		second <- err
	}()
	select {
	case err := <-second:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("the waiter: got %v, want its deadline", err)
		}
	case <-time.After(5 * time.Second):
		t.Errorf("the waiter outlived its 100 ms deadline by 5 s")
	}
	close(release)
	if err := <-first; err != nil {
		t.Errorf("the first Collect: %v", err)
	}
}

// heightOrNone is df's height, or -1 for no frame.
func heightOrNone(df *ursus.DataFrame) int {
	if df == nil {
		return -1
	}
	return df.Height()
}

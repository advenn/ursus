package ursus_test

// Parquet row groups are decoded in parallel, and the answer is the serial one
// (step 102).
//
// Step 101's profile found the serial reader at 37–47% of the CPU of the queries it
// measured, and so at roughly their whole wall time. The parallel reader keeps the
// serial reader's order exactly; these pin that, and what it must release however a
// query ends.

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/ursustest"
)

// rowGroupFiles writes n rows, a third to each of three Parquet files, in row groups
// of 1000: 30 row groups for n = 30,000. Every type the reader decodes differently
// is here, with nulls.
func rowGroupFiles(t *testing.T, n int) [][]byte {
	t.Helper()
	ids := make([]int64, n)
	f := make([]float64, n)
	fValid := make([]bool, n)
	s := make([]string, n)
	sValid := make([]bool, n)
	small := make([]int32, n)
	day := make([]time.Time, n)
	flag := make([]bool, n)
	csv := make([]string, n)
	for i := range n {
		ids[i] = int64(i)
		f[i], fValid[i] = float64(i)/7, i%5 != 0
		s[i], sValid[i] = fmt.Sprintf("s%05d", i%977), i%11 != 0
		small[i] = int32(i % 300)
		day[i] = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i%1000)
		flag[i] = i%3 == 0
		csv[i] = strings.Repeat("x,", i%4) + "y"
	}
	frame := ursus.Frame(
		ursus.Values("id", ids),
		ursus.ValuesNullable("f", f, fValid),
		ursus.ValuesNullable("s", s, sValid),
		ursus.Values("small", small),
		ursus.Values("day", day),
		ursus.Values("flag", flag),
		ursus.Values("csv", csv),
	).WithColumns(
		ursus.Col("day").Cast(ursus.Date),
		ursus.Col("csv").Str().Split(",").Alias("parts"),
		ursus.Col("small").Cast(ursus.Decimal(18, 3)).Alias("dec"),
	)
	var files [][]byte
	for k := range 3 {
		var buf bytes.Buffer
		part := frame.Filter(ursus.Col("id").Ge(int64(k * n / 3)).And(ursus.Col("id").Lt(int64((k + 1) * n / 3))))
		if err := part.WriteParquet(t.Context(), &buf, ursus.WithRowGroupRows(1000)); err != nil {
			t.Fatal(err)
		}
		files = append(files, buf.Bytes())
	}
	return files
}

// countedReaderAt is a file that counts how many times it is opened and closed, and
// can be told to panic on any read of its column chunks — everything before the
// footer — while the footer itself still reads.
type countedReaderAt struct {
	*bytes.Reader
	footerStart int64
	panicData   bool
	closed      *atomic.Int32
	once        atomic.Bool
}

func (c *countedReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if c.panicData && off < c.footerStart {
		panic("ReadAt boom")
	}
	return c.Reader.ReadAt(p, off)
}

func (c *countedReaderAt) Close() error {
	if c.once.CompareAndSwap(false, true) {
		c.closed.Add(1)
	}
	return nil
}

// countedFiles is ScanParquetFrom over files, counting opens and closes.
type countedFiles struct {
	opened, closed atomic.Int32
	panicData      atomic.Bool
}

func (cf *countedFiles) scan(files [][]byte) *ursus.LazyFrame {
	var pf []ursus.ParquetFile
	for i, b := range files {
		// The footer is the last 8 + len bytes: its length, then "PAR1".
		footerLen := int64(binary.LittleEndian.Uint32(b[len(b)-8:]))
		start := int64(len(b)) - 8 - footerLen
		pf = append(pf, ursus.ParquetFile{
			Name: fmt.Sprintf("part-%d.parquet", i),
			Open: func(context.Context) (io.ReaderAt, int64, error) {
				cf.opened.Add(1)
				return &countedReaderAt{Reader: bytes.NewReader(b), footerStart: start,
					panicData: cf.panicData.Load(), closed: &cf.closed}, int64(len(b)), nil
			},
		})
	}
	return ursus.ScanParquetFrom(pf)
}

// settledGoroutines waits for the goroutine count to fall to at most want, and
// returns what it settled at.
func settledGoroutines(want int) int {
	n := runtime.NumGoroutine()
	for range 100 {
		if n <= want {
			return n
		}
		time.Sleep(5 * time.Millisecond)
		n = runtime.NumGoroutine()
	}
	return n
}

func TestParallelParquetScanMatchesSerial(t *testing.T) {
	files := rowGroupFiles(t, 30000)
	scan := func() *ursus.LazyFrame { return (&countedFiles{}).scan(files) }
	queries := map[string]func() *ursus.LazyFrame{
		"every column": scan,
		"a pruning filter": func() *ursus.LazyFrame {
			return scan().Filter(ursus.Col("id").Ge(int64(4500)).And(ursus.Col("id").Lt(int64(17250))))
		},
		"two columns": func() *ursus.LazyFrame { return scan().Select(ursus.Col("s"), ursus.Col("dec")) },
		"a limit":     func() *ursus.LazyFrame { return scan().Head(2500) },
	}
	for name, q := range queries {
		want, err := q().Collect(t.Context(), ursus.WithThreads(1))
		if err != nil {
			t.Fatal(err)
		}
		for _, bs := range []int{7, 1000, 8192} {
			t.Run(fmt.Sprintf("%s, batch %d", name, bs), func(t *testing.T) {
				got, err := q().Collect(t.Context(), ursus.WithThreads(8), ursus.WithBatchSize(bs))
				if err != nil {
					t.Fatal(err)
				}
				ursustest.AssertFrameEqual(t, got, want)
			})
		}
	}
}

func TestParallelParquetScanReleasesEverything(t *testing.T) {
	files := rowGroupFiles(t, 30000)

	t.Run("a consumer that stops after one batch", func(t *testing.T) {
		base := runtime.NumGoroutine()
		cf := &countedFiles{}
		for _, err := range cf.scan(files).CollectBatches(t.Context(), ursus.WithThreads(8), ursus.WithBatchSize(100)) {
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		if o, c := cf.opened.Load(), cf.closed.Load(); o != c {
			t.Errorf("%d files opened and %d closed", o, c)
		}
		if n := settledGoroutines(base); n > base {
			t.Errorf("%d goroutines still running, against %d before", n, base)
		}
	})
	t.Run("a cancelled context", func(t *testing.T) {
		base := runtime.NumGoroutine()
		cf := &countedFiles{}
		ctx, cancel := context.WithCancel(t.Context())
		var err error
		for _, e := range cf.scan(files).CollectBatches(ctx, ursus.WithThreads(8), ursus.WithBatchSize(100)) {
			if e != nil {
				err = e
				break
			}
			cancel()
		}
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Errorf("want the cancellation, got %v", err)
		}
		if o, c := cf.opened.Load(), cf.closed.Load(); o != c {
			t.Errorf("%d files opened and %d closed", o, c)
		}
		if n := settledGoroutines(base); n > base {
			t.Errorf("%d goroutines still running, against %d before", n, base)
		}
	})
}

// TestParallelRowGroupPanicIsAnError: a panic decoding a row group on a worker
// goroutine — here the file's own ReadAt, on a column chunk — is an error from the
// query, and the reader still releases every file and every goroutine.
func TestParallelRowGroupPanicIsAnError(t *testing.T) {
	files := rowGroupFiles(t, 30000)
	base := runtime.NumGoroutine()
	cf := &countedFiles{}
	lf := cf.scan(files) // planned while the files read cleanly
	if _, err := lf.CollectSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	cf.panicData.Store(true)
	_, err := lf.Collect(t.Context(), ursus.WithThreads(8))
	if err == nil {
		t.Fatal("a panicking read returned no error")
	}
	if !errors.Is(err, uerr.ErrIO) && !errors.Is(err, uerr.ErrInternal) {
		t.Errorf("want an I/O or internal error, got %v", err)
	}
	if !strings.Contains(err.Error(), "ReadAt boom") {
		t.Errorf("the error lost the panic: %v", err)
	}
	if o, c := cf.opened.Load(), cf.closed.Load(); o != c {
		t.Errorf("%d files opened and %d closed", o, c)
	}
	if n := settledGoroutines(base); n > base {
		t.Errorf("%d goroutines still running, against %d before", n, base)
	}
}

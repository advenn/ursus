package readfuzz_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/ipc"

	"github.com/advenn/ursus"
)

// errWrappingEOF is a read's failure that wraps io.EOF, as a network reader's may:
// not the end of the input, which io.Reader's contract has Read answer as io.EOF
// itself, "because callers will test for EOF using ==".
var errWrappingEOF = fmt.Errorf("the connection was reset: %w", io.EOF)

// failingStream serves b, and fails with errWrappingEOF once at bytes are read.
type failingStream struct {
	b  []byte
	at int
}

func (s *failingStream) Read(p []byte) (int, error) {
	if s.at <= 0 {
		return 0, errWrappingEOF
	}
	n := copy(p[:min(len(p), s.at)], s.b)
	s.b, s.at = s.b[n:], s.at-n
	return n, nil
}

// holeyFile is b read at, its bytes in [from, to) not there: a read reaching them
// gives the bytes before them and io.EOF, as a ReaderAt over a ranged GET whose body
// ends early may.
type holeyFile struct {
	b        []byte
	from, to int64
}

func (h holeyFile) ReadAt(p []byte, off int64) (int, error) {
	if off < h.to && off+int64(len(p)) > h.from {
		n := 0
		if off < h.from {
			n = copy(p, h.b[off:h.from])
		}
		return n, io.EOF
	}
	return bytes.NewReader(h.b).ReadAt(p, off)
}

// TestAReadFailingWithAnErrorWrappingEOFIsRefused (step 534): a read whose stream
// fails with an error wrapping io.EOF, or whose ReaderAt answers io.EOF for bytes
// inside the file, is refused, by Collect and by Count, each with an ErrIO that does
// not wrap io.EOF; the engine takes an error that does for the end of its input,
// and answered the rows before it, with no error. Each input reads whole, the
// differential's other side. The streams are a CSV, an NDJSON and a text of lines,
// each plain and gzip, through ScanCSVFrom, ScanNDJSONFrom and ScanLinesFrom, and an
// IPC stream, plain and LZ4, through ScanIPCStreamFrom, failing a quarter, half and
// three quarters of the way; the ReaderAts, an IPC file through ScanIPCFrom and a
// Parquet file of five row groups through ScanParquetFrom, each without the bytes
// from its middle to its footer. A Parquet file's Count is its footer's, which reads
// no column chunk (step 370), as pyarrow's and DuckDB's are (step 521): the whole
// file's, or refused.
func TestAReadFailingWithAnErrorWrappingEOFIsRefused(t *testing.T) {
	ctx := t.Context()
	type opener = func(context.Context) (io.ReadCloser, error)
	csvText, ndText := cutText("CSV", 400, 3), cutText("NDJSON", 400, 3)
	csvScan := func(open opener) *ursus.LazyFrame {
		return ursus.ScanCSVFrom([]ursus.CSVFile{{Name: "failing.csv", Open: open}})
	}
	ndScan := func(open opener) *ursus.LazyFrame {
		return ursus.ScanNDJSONFrom([]ursus.NDJSONFile{{Name: "failing.ndjson", Open: open}})
	}
	linesScan := func(open opener) *ursus.LazyFrame {
		return ursus.ScanLinesFrom([]ursus.LinesFile{{Name: "failing.txt", Open: open}})
	}
	ipcScan := func(open opener) *ursus.LazyFrame {
		return ursus.ScanIPCStreamFrom([]ursus.IPCStreamFile{{Name: "failing.arrows", Open: open}})
	}
	ints := intRecord(t)
	streams := []struct {
		name string
		b    []byte
		scan func(opener) *ursus.LazyFrame
	}{
		{"CSV", csvText, csvScan},
		{"CSV, gzip", compress(t, csvText, 1), csvScan},
		{"NDJSON", ndText, ndScan},
		{"NDJSON, gzip", compress(t, ndText, 1), ndScan},
		{"lines", ndText, linesScan},
		{"lines, gzip", compress(t, ndText, 1), linesScan},
		{"IPC stream", writeIPC(t, false, ints), ipcScan},
		{"IPC stream, LZ4", writeIPC(t, false, ints, ipc.WithLZ4()), ipcScan},
	}
	for _, s := range streams {
		whole, err := s.scan(func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(s.b)), nil
		}).Collect(ctx)
		if err != nil || whole.Height() == 0 {
			t.Fatalf("%s, whole: %v, %v", s.name, whole, err)
		}
		for _, at := range []int{len(s.b) / 4, len(s.b) / 2, 3 * len(s.b) / 4} {
			lf := s.scan(func(context.Context) (io.ReadCloser, error) {
				return io.NopCloser(&failingStream{b: s.b, at: at}), nil
			})
			refusedNotEnded(t, fmt.Sprintf("%s failing after %d of its %d bytes, of %d rows whole", s.name, at, len(s.b),
				whole.Height()), lf, -1)
		}
	}

	ipcFile := writeIPC(t, true, ints)
	var pq bytes.Buffer
	if err := ursus.ScanArrowRecords(ints).WriteParquet(ctx, &pq, ursus.WithRowGroupRows(200)); err != nil {
		t.Fatal(err)
	}
	files := []struct {
		name   string
		b      []byte
		footer int64 // where its footer begins
		counts bool  // whether Count is the footer's
		scan   func(open func(context.Context) (io.ReaderAt, int64, error)) *ursus.LazyFrame
	}{
		{"IPC file", ipcFile, int64(len(ipcFile)) - 10 - int64(binary.LittleEndian.Uint32(ipcFile[len(ipcFile)-10:])), false,
			func(open func(context.Context) (io.ReaderAt, int64, error)) *ursus.LazyFrame {
				return ursus.ScanIPCFrom([]ursus.IPCFile{{Name: "holey.arrow", Open: open}})
			}},
		{"Parquet file", pq.Bytes(), int64(pq.Len()) - 8 - int64(binary.LittleEndian.Uint32(pq.Bytes()[pq.Len()-8:])), true,
			func(open func(context.Context) (io.ReaderAt, int64, error)) *ursus.LazyFrame {
				return ursus.ScanParquetFrom([]ursus.ParquetFile{{Name: "holey.parquet", Open: open}})
			}},
	}
	for _, f := range files {
		whole, err := f.scan(func(context.Context) (io.ReaderAt, int64, error) {
			return bytes.NewReader(f.b), int64(len(f.b)), nil
		}).Collect(ctx)
		if err != nil || whole.Height() == 0 {
			t.Fatalf("%s, whole: %v, %v", f.name, whole, err)
		}
		from := int64(len(f.b) / 2)
		lf := f.scan(func(context.Context) (io.ReaderAt, int64, error) {
			return holeyFile{b: f.b, from: from, to: f.footer}, int64(len(f.b)), nil
		})
		footerCount := int64(-1)
		if f.counts {
			footerCount = int64(whole.Height())
		}
		refusedNotEnded(t, fmt.Sprintf("%s without its bytes %d to %d, of %d rows whole", f.name, from, f.footer,
			whole.Height()), lf, footerCount)
	}
}

// refusedNotEnded fails t unless lf's Collect and Count are each refused, described
// as what, with an ErrIO that does not wrap io.EOF; Count may answer footerCount
// instead where that is not -1.
func refusedNotEnded(t *testing.T, what string, lf *ursus.LazyFrame, footerCount int64) {
	t.Helper()
	df, err := lf.Collect(t.Context())
	n, cerr := lf.Count(t.Context())
	switch {
	case err == nil:
		t.Errorf("%s: read as %d rows, and no error", what, df.Height())
	case errors.Is(err, io.EOF) || !errors.Is(err, ursus.ErrIO):
		t.Errorf("%s: Collect refuses it with %v, not an ErrIO, or one the engine takes for the end", what, err)
	}
	switch {
	case cerr == nil && n == footerCount:
	case cerr == nil:
		t.Errorf("%s: Count says %d rows, and no error", what, n)
	case errors.Is(cerr, io.EOF) || !errors.Is(cerr, ursus.ErrIO):
		t.Errorf("%s: Count refuses it with %v, not an ErrIO, or one the engine takes for the end", what, cerr)
	}
}

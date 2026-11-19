package readfuzz_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/fuzzcheck"
	"github.com/advenn/ursus/internal/zstdframe"
)

// zstdWindowed is text as klauspost's streaming writer frames it, flushed before its
// end so that no content size is written, its Window_Descriptor then wd: a larger
// window leaves the frame what it was.
func zstdWindowed(t *testing.T, text []byte, wd byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf, zstd.WithEncoderConcurrency(1), zstd.WithWindowSize(1<<10))
	if err == nil {
		_, err = w.Write(text)
	}
	if err == nil {
		err = w.Flush()
	}
	if err == nil {
		err = w.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	if b[4]&(1<<5) != 0 {
		t.Fatalf("the streaming writer wrote a single segment, % x", b[:6])
	}
	b[5] = wd
	return b
}

// zstdSingleSegment is a frame of text whose header declares a single segment of fcs
// bytes, its content size its window: refused at the header past 128 MiB, so the
// content size need not be text's.
func zstdSingleSegment(text []byte, fcs uint64) []byte {
	b := binary.LittleEndian.AppendUint64([]byte{0x28, 0xb5, 0x2f, 0xfd, 3<<6 | 1<<5}, fcs)
	h := len(text)<<3 | 1 // a last raw block
	return append(append(b, byte(h), byte(h>>8), byte(h>>16)), text...)
}

// zstdDeclarePast changes, in place, the header of every Zstandard frame of b, an
// Arrow IPC input, where a frame's magic number begins one, to declare a window of 256
// MiB in the bytes it had: a Window_Descriptor where its header has room for one, a
// single segment's content size otherwise. It is how many it changed.
func zstdDeclarePast(b []byte) int {
	n := 0
	for i := 0; ; i++ {
		j := bytes.Index(b[i:], []byte{0x28, 0xb5, 0x2f, 0xfd})
		if j < 0 || i+j+14 > len(b) {
			return n
		}
		i += j
		f := b[i:]
		fhd := f[4]
		switch {
		case fhd&(1<<3) != 0 || fhd&3 != 0: // reserved, or a dictionary: not a writer's frame
			continue
		case fhd&(1<<5) == 0:
			f[5] = 18 << 3
		case fhd>>6 == 0:
			f[4], f[5] = fhd&(1<<2), 18<<3
		case fhd>>6 == 1:
			f[4], f[5], f[6] = fhd&(1<<2)|1, 18<<3, 0
		case fhd>>6 == 2:
			binary.LittleEndian.PutUint32(f[5:], 256<<20)
		default:
			binary.LittleEndian.PutUint64(f[5:], 256<<20)
		}
		n++
	}
}

// refusedForTheWindow fails t unless err refuses a Zstandard frame declaring a window
// past 128 MiB: KindIO, a *zstdframe.WindowError its cause, the limit named.
func refusedForTheWindow(t *testing.T, what string, err error) {
	t.Helper()
	var we *zstdframe.WindowError
	if !errors.As(err, &we) || we.Window <= zstdframe.MaxWindow || !errors.Is(err, ursus.ErrIO) ||
		!strings.Contains(err.Error(), "past 128 MiB") {
		t.Fatalf("%s: %v", what, err)
	}
}

// TestZstdWindowPastTheCap is step 528's: a Zstandard frame declaring a window past
// 128 MiB, the reference decoder's limit, which klauspost's decoder made whole before
// it decoded a block, is refused, naming the limit, through every reader that
// decompresses one, within fuzzcheck.Run's bounds of the targets, which check each
// input here as they check one the fuzzer makes. The CSV, NDJSON and JSON readers of a
// frame declaring each window past the limit by its Window_Descriptor or as a single
// segment, after a frame of 1 MiB or alone, and step 511's kept input, a single
// segment of 512 MiB; Arrow IPC files and streams Polars and pyarrow wrote, every
// Zstandard frame of theirs declaring 256 MiB.
func TestZstdWindowPastTheCap(t *testing.T) {
	texts := map[string][]byte{
		"CSV":    []byte("a,b\n1,x\n2,y\n"),
		"NDJSON": []byte("{\"a\":1,\"b\":\"x\"}\n{\"a\":2,\"b\":\"y\"}\n"),
		"JSON":   []byte(`[{"a":1,"b":"x"},{"a":2,"b":"y"}]`),
	}
	past := map[string]func(text []byte) []byte{
		"144 MiB":           func(b []byte) []byte { return zstdWindowed(t, b, 17<<3|1) },
		"256 MiB":           func(b []byte) []byte { return zstdWindowed(t, b, 18<<3) },
		"512 MiB":           func(b []byte) []byte { return zstdWindowed(t, b, 19<<3) },
		"the format's most": func(b []byte) []byte { return zstdWindowed(t, b, 0xff) },
		"a single segment of 128 MiB and a byte": func(b []byte) []byte {
			return zstdSingleSegment(b, zstdframe.MaxWindow+1)
		},
		"a single segment of 2^64-1 bytes": func(b []byte) []byte { return zstdSingleSegment(b, 1<<64-1) },
		"256 MiB after a frame of 1 MiB": func(b []byte) []byte {
			return slices.Concat(zstdWindowed(t, b[:len(b)/2], 10<<3), zstdWindowed(t, b[len(b)/2:], 18<<3))
		},
	}
	kept := corpusFile(t, "FuzzScanNDJSON", "finding-zstd-window-512mib")
	read := func(t *testing.T, what string, b []byte, scan func([]byte) *ursus.LazyFrame) {
		var r read
		fuzzcheck.Run(t, limits(len(b)), func() error { return r.of(t, scan(b)) })
		err := r.schemaErr
		if err == nil {
			err = r.err
		}
		refusedForTheWindow(t, what, err)
	}
	csv := func(b []byte) *ursus.LazyFrame { return ursus.ScanCSVReader(b, "t.csv") }
	nd := func(b []byte) *ursus.LazyFrame { return ursus.ScanNDJSONBytes(b, "t.ndjson") }
	doc := func(b []byte) *ursus.LazyFrame { return ursus.ScanJSONBytes(b, "t.json") }
	for name, frame := range past {
		t.Run(name, func(t *testing.T) {
			b := frame(texts["CSV"])
			scanCSVOne(t, b, 0)
			read(t, "CSV", b, csv)
			b = frame(texts["NDJSON"])
			scanNDJSONOne(t, b)
			read(t, "NDJSON", b, nd)
			b = frame(texts["JSON"])
			scanJSONOne(t, b)
			read(t, "JSON", b, doc)
		})
	}
	t.Run("step 511's kept input", func(t *testing.T) {
		scanCSVOne(t, kept, 0)
		scanNDJSONOne(t, kept)
		scanJSONOne(t, kept)
		read(t, "CSV", kept, csv)
		read(t, "NDJSON", kept, nd)
		read(t, "JSON", kept, doc)
	})
	for _, name := range []string{"polars_types_zstd.arrow", "polars_null_zstd.arrow", "polars_null_zstd.arrows",
		"pyarrow_null_zstd.arrow", "pyarrow_null_zstd.arrows"} {
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "ipc", name))
			if err != nil {
				t.Fatal(err)
			}
			if zstdDeclarePast(b) == 0 {
				t.Fatal("no Zstandard frame")
			}
			if strings.HasSuffix(name, ".arrows") {
				scanIPCStreamOne(t, b)
				read(t, "an IPC stream", b, func(b []byte) *ursus.LazyFrame { return ursus.ScanIPCStreamBytes(b, name) })
			} else {
				scanIPCFileOne(t, b)
				read(t, "an IPC file", b, func(b []byte) *ursus.LazyFrame { return ursus.ScanIPCBytes(b, name) })
			}
		})
	}
}

// TestZstdWindowBelowTheCap: a frame declaring a window past the targets' bound and up
// to 128 MiB, which skipZstdWindow skips, is read at the cost of its window: within
// fuzzcheck.Run's bounds widened by the window and 1 MiB, the decoder's, for each time
// a reader decompresses the stream, twice, the schema inferred and the rows read. The
// kept inputs the targets skip, of 64 MiB (FuzzScanJSON) and 104 MiB (FuzzScanCSV),
// and frames of 24, 64 and 128 MiB through each text reader.
func TestZstdWindowBelowTheCap(t *testing.T) {
	type input struct {
		name string
		b    []byte
		scan func([]byte) *ursus.LazyFrame
	}
	csvText := []byte("a,b\n1,x\n2,y\n")
	ndText := []byte("{\"a\":1,\"b\":\"x\"}\n{\"a\":2,\"b\":\"y\"}\n")
	csv := func(b []byte) *ursus.LazyFrame { return ursus.ScanCSVReader(b, "t.csv") }
	nd := func(b []byte) *ursus.LazyFrame { return ursus.ScanNDJSONBytes(b, "t.ndjson") }
	doc := func(b []byte) *ursus.LazyFrame { return ursus.ScanJSONBytes(b, "t.json") }
	keptCSV, keptOpts := csvCorpusFile(t, "zstd-window-past-the-bound")
	inputs := []input{
		{"FuzzScanJSON's kept input", corpusFile(t, "FuzzScanJSON", "finding-zstd-window-64mib"), doc},
		{"FuzzScanCSV's kept input", compress(t, keptCSV, keptOpts), func(b []byte) *ursus.LazyFrame {
			o, _ := csvOptions(keptOpts)
			return ursus.ScanCSVReader(b, "t.csv", o...)
		}},
	}
	for _, wd := range []byte{14<<3 | 4, 16 << 3, 17 << 3} {
		inputs = append(inputs,
			input{"CSV", zstdWindowed(t, csvText, wd), csv},
			input{"NDJSON", zstdWindowed(t, ndText, wd), nd},
			input{"JSON", zstdWindowed(t, []byte(`[{"a":1}]`), wd), doc})
	}
	for _, in := range inputs {
		w := zstdWindowRead(in.b)
		if w <= maxZstdWindow || w > zstdframe.MaxWindow {
			t.Fatalf("%s declares %d bytes, which the targets do not skip", in.name, w)
		}
		lim := limits(len(in.b))
		lim.Alloc += 2 * (int64(w) + 1<<20)
		var r read
		fuzzcheck.Run(t, lim, func() error { return r.of(t, in.scan(in.b)) })
		var we *zstdframe.WindowError
		if errors.As(r.schemaErr, &we) || errors.As(r.err, &we) {
			t.Fatalf("%s, a window of %d bytes, is refused for it", in.name, w)
		}
	}
}

// csvCorpusFile is FuzzScanCSV's corpus entry name: its bytes and its options.
func csvCorpusFile(t *testing.T, name string) ([]byte, uint32) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "fuzz", "FuzzScanCSV", name))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 3 || lines[0] != "go test fuzz v1" {
		t.Fatalf("%s is not a corpus entry of FuzzScanCSV's", name)
	}
	lit, ok := strings.CutPrefix(lines[1], "[]byte(")
	lit, ok2 := strings.CutSuffix(lit, ")")
	num, ok3 := strings.CutPrefix(lines[2], "uint32(")
	num, ok4 := strings.CutSuffix(num, ")")
	raw, err := strconv.Unquote(lit)
	opts, err2 := strconv.ParseUint(num, 10, 32)
	if !ok || !ok2 || !ok3 || !ok4 || err != nil || err2 != nil {
		t.Fatalf("%s is not a corpus entry of FuzzScanCSV's", name)
	}
	return []byte(raw), uint32(opts)
}

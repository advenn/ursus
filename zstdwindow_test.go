package ursus_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/zstdframe"
	"github.com/advenn/ursus/ursustest"
)

// TestZstdWindowUpToTheCapReadsAsBefore is step 528's: no Zstandard stream is refused
// for its window below the limit of 128 MiB. Streams klauspost's encoder writes under
// windows of 1, 8, 64 and 128 MiB, declared as they are, a CSV's, an NDJSON's and a
// JSON document's, read as the text klauspost's decoder made with its defaults, which
// ursus decompressed with before the limit, gives: the same frame. The zstd CLI's
// streams at those windows are the zstdwindow oracle group's, Polars 2.1.0's readings.
func TestZstdWindowUpToTheCapReadsAsBefore(t *testing.T) {
	ctx := t.Context()
	var csv, nd strings.Builder
	var rows []map[string]any
	csv.WriteString("i,s,f\n")
	for i := range 2000 {
		fmt.Fprintf(&csv, "%d,s%d,%d.5\n", i, i%31, i%7)
		row := map[string]any{"i": i, "s": fmt.Sprintf("s%d", i%31), "f": float64(i%7) + 0.5}
		line, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		nd.Write(append(line, '\n'))
		rows = append(rows, row)
	}
	doc, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	texts := []struct {
		name string
		text []byte
		scan func(b []byte) *ursus.LazyFrame
	}{
		{"CSV", []byte(csv.String()), func(b []byte) *ursus.LazyFrame { return ursus.ScanCSVReader(b, "t.csv") }},
		{"NDJSON", []byte(nd.String()), func(b []byte) *ursus.LazyFrame { return ursus.ScanNDJSONBytes(b, "t.ndjson") }},
		{"JSON", doc, func(b []byte) *ursus.LazyFrame { return ursus.ScanJSONBytes(b, "t.json") }},
	}
	before := func(b []byte) []byte {
		d, err := zstd.NewReader(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		out, err := io.ReadAll(d)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	for _, window := range []int{1 << 20, 8 << 20, 64 << 20, zstdframe.MaxWindow} {
		var buf bytes.Buffer
		enc, err := zstd.NewWriter(&buf, zstd.WithWindowSize(window), zstd.WithLowerEncoderMem(true),
			zstd.WithEncoderConcurrency(1))
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range texts {
			buf.Reset()
			enc.Reset(&buf)
			if _, err := enc.Write(x.text); err != nil {
				t.Fatal(err)
			}
			if err := enc.Flush(); err != nil { // the window declared, not the text's size
				t.Fatal(err)
			}
			if err := enc.Close(); err != nil {
				t.Fatal(err)
			}
			b := slices.Clone(buf.Bytes())
			if got := zstdframe.Windows(b); len(got) != 1 || got[0] != uint64(window) {
				t.Fatalf("the encoder declared windows %v, not %d bytes", got, window)
			}
			got, err := x.scan(b).Collect(ctx)
			if err != nil {
				t.Fatalf("%s in a window of %d bytes: %v", x.name, window, err)
			}
			want, err := x.scan(before(b)).Collect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			ursustest.AssertFrameEqual(t, got, want)
		}
	}
}

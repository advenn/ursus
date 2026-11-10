package ursus_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"

	"github.com/advenn/ursus"
)

// csvProbeFrame is the frame the csvwrite oracle group writes under each option
// (testdata/oracle/csvwrite.json holds Polars 2.1.0's text of it, spelled in
// oracle_csvwrite_test.go); here the compressed writers are checked on it.
func csvProbeFrame() *ursus.LazyFrame {
	c := ursus.Col
	at := func(y int, mo time.Month, d, h, mi, s, ns int) time.Time {
		return time.Date(y, mo, d, h, mi, s, ns, time.UTC)
	}
	valid := []bool{true, false, true}
	return ursus.Frame(
		i64s("i", 1, nil, -3),
		ursus.Values("f", []float64{1.5, 0.1, 1e20}),
		strs("s", "a", "b,c", `q"x`),
		ursus.ValuesNullable("t", []time.Time{at(2020, 1, 2, 3, 4, 5, 600_000), {}, at(1999, 12, 31, 23, 59, 59, 0)}, valid),
		ursus.ValuesNullable("tm", []time.Time{at(1970, 1, 1, 1, 2, 3, 0), {}, at(1970, 1, 1, 23, 59, 59, 500_000_000)}, valid),
		ursus.ValuesNullable("b", []bool{true, false, false}, []bool{true, true, false}),
	).Select(
		c("i"), c("f"), c("s"),
		c("t").Cast(ursus.Date).Alias("d"),
		c("t").Cast(ursus.Datetime(ursus.Micro, "")),
		c("tm").Cast(ursus.TimeOf(ursus.Nano)),
		c("b"),
	)
}

func csvOut(t *testing.T, lf *ursus.LazyFrame, opts ...ursus.CSVSinkOption) string {
	t.Helper()
	var b bytes.Buffer
	if err := lf.WriteCSV(t.Context(), &b, opts...); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestCSVWriteRefusals is step 354's refusal Polars cannot be asked: a negative float
// precision, which it refuses at its Python layer. A format a type cannot take
// ("cannot format NaiveDateTime with format '%Y %z'") and a directive that is none are
// recorded with every text the writer gives, Polars 2.1.0's since step 502
// (testdata/oracle/csvwrite.json), each a value error here.
func TestCSVWriteRefusals(t *testing.T) {
	at := ursus.Frame(ursus.Values("t", []time.Time{time.Date(2020, 1, 2, 3, 4, 5, 123456000, time.UTC)}))
	if err := at.WriteCSV(t.Context(), io.Discard, ursus.WithFloatPrecision(-2)); !errors.Is(err, ursus.ErrValue) {
		t.Errorf("a negative precision: %v, want a value error", err)
	}
}

// TestCSVWriteCompressed is a CSV written by gzip or Zstandard, read back by its first
// bytes, and SinkCSV's check of its path's extension, Polars'.
func TestCSVWriteCompressed(t *testing.T) {
	ctx := t.Context()
	lf := csvProbeFrame()
	plain := csvOut(t, lf)
	for _, tc := range []struct {
		name  string
		opts  []ursus.CSVSinkOption
		magic string
		open  func(io.Reader) (io.Reader, error)
	}{
		{"gzip", []ursus.CSVSinkOption{ursus.WithCSVCompression(ursus.CSVGzip)}, "\x1f\x8b",
			func(r io.Reader) (io.Reader, error) { return gzip.NewReader(r) }},
		{"gzip at 9", []ursus.CSVSinkOption{ursus.WithCSVCompression(ursus.CSVGzip), ursus.WithCSVCompressionLevel(9)}, "\x1f\x8b",
			func(r io.Reader) (io.Reader, error) { return gzip.NewReader(r) }},
		{"Zstandard", []ursus.CSVSinkOption{ursus.WithCSVCompression(ursus.CSVZstd)}, "\x28\xb5\x2f\xfd",
			func(r io.Reader) (io.Reader, error) { return zstd.NewReader(r) }},
		{"Zstandard at 19", []ursus.CSVSinkOption{ursus.WithCSVCompression(ursus.CSVZstd), ursus.WithCSVCompressionLevel(19)}, "\x28\xb5\x2f\xfd",
			func(r io.Reader) (io.Reader, error) { return zstd.NewReader(r) }},
	} {
		raw := csvOut(t, lf, tc.opts...)
		if !strings.HasPrefix(raw, tc.magic) {
			t.Errorf("%s: begins %q", tc.name, raw[:4])
			continue
		}
		r, err := tc.open(strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		text, err := io.ReadAll(r)
		if err != nil || string(text) != plain {
			t.Errorf("%s: decompressed %q, %v", tc.name, text, err)
		}
		back, err := ursus.ScanCSVReader([]byte(raw), "c.csv").Collect(ctx)
		if err != nil || back.Height() != 3 {
			t.Errorf("%s: read back %v, %v", tc.name, back, err)
		}
	}

	// A codec's level past its range is refused; Polars panics past gzip's 9.
	for _, tc := range []struct {
		c     ursus.CSVCompression
		level int
	}{{ursus.CSVGzip, 10}, {ursus.CSVGzip, -2}, {ursus.CSVZstd, 0}, {ursus.CSVZstd, 23}} {
		if err := lf.WriteCSV(ctx, io.Discard, ursus.WithCSVCompression(tc.c), ursus.WithCSVCompressionLevel(tc.level)); !errors.Is(err, ursus.ErrValue) {
			t.Errorf("codec %d at %d: %v", tc.c, tc.level, err)
		}
	}

	// SinkCSV's path ends as its codec's files do, and an uncompressed one's does not,
	// unless told otherwise: Polars' check_extension.
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		opts []ursus.CSVSinkOption
		ok   bool
	}{
		{"c.csv.gz", []ursus.CSVSinkOption{ursus.WithCSVCompression(ursus.CSVGzip)}, true},
		{"c.gz", []ursus.CSVSinkOption{ursus.WithCSVCompression(ursus.CSVGzip)}, true},
		{"c.csv", []ursus.CSVSinkOption{ursus.WithCSVCompression(ursus.CSVGzip)}, false},
		{"c.csv.zst", []ursus.CSVSinkOption{ursus.WithCSVCompression(ursus.CSVZstd)}, true},
		{"c.csv.zstd", []ursus.CSVSinkOption{ursus.WithCSVCompression(ursus.CSVZstd)}, false},
		{"plain.csv.zst", nil, false},
		{"plain.txt", nil, true},
		{"unchecked.csv", []ursus.CSVSinkOption{ursus.WithCSVCompression(ursus.CSVGzip), ursus.WithCSVCheckExtension(false)}, true},
	} {
		path := filepath.Join(dir, tc.name)
		err := lf.SinkCSV(ctx, path, tc.opts...)
		if (err == nil) != tc.ok {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if _, statErr := os.Stat(path); (statErr == nil) != tc.ok {
			t.Errorf("%s: written %v", tc.name, statErr == nil)
		}
		if tc.ok {
			back, err := ursus.ScanCSV(path).Collect(ctx)
			if err != nil || back.Height() != 3 {
				t.Errorf("%s: read back %v, %v", tc.name, back, err)
			}
		}
	}
}

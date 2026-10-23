package ursus_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestArrowIPC is step 332: Polars' Arrow IPC files and streams, read and written.
// The fixtures in testdata/ipc are Polars 1.44.1's write_ipc and write_ipc_stream of
// one frame, uncompressed, LZ4 and Zstandard: string views, a large list, a struct.
func TestArrowIPC(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	// The frame Polars wrote, as ursus reads it: its dates in days and its instants in
	// microseconds, with no zone.
	want := func(t *testing.T) *ursus.DataFrame {
		t.Helper()
		lf := ursus.Frame(i64s("i", 1, nil, 3), strs("s", "x", nil, "a longer string than twelve bytes"),
			f64s("f", 1.5, 2.25, nil), ursus.ValuesNullable("b", []bool{true, false, false}, []bool{true, false, true}),
			ursus.ValuesNullable("d", []int32{19724, 0, 10956}, []bool{true, false, true}),
			ursus.ValuesNullable("t", []int64{1704164645000006, 0, 0}, []bool{true, false, true})).
			Select(c("i"), c("s"), c("f"), c("b"), c("d").Cast(ursus.Date), c("t").Cast(ursus.Datetime(ursus.Micro, "")))
		return collect(t, lf)
	}

	t.Run("Polars' files", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			lf   *ursus.LazyFrame
		}{
			{"a file", ursus.ScanIPC(filepath.Join("testdata", "ipc", "polars_types.arrow"))},
			{"LZ4", ursus.ScanIPC(filepath.Join("testdata", "ipc", "polars_types_lz4.arrow"))},
			{"Zstandard", ursus.ScanIPC(filepath.Join("testdata", "ipc", "polars_types_zstd.arrow"))},
			{"a stream", ursus.ScanIPCStream(filepath.Join("testdata", "ipc", "polars_types.arrows"))},
		} {
			got, err := tc.lf.Select(c("i"), c("s"), c("f"), c("b"), c("d"), c("t")).Collect(ctx)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			ursustest.AssertFrameEqual(t, got, want(t))
			// The list and the struct, by value.
			rows, err := tc.lf.Select(c("l"), c("st")).Collect(ctx)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if s := rows.String(); !strings.Contains(s, "[1, 2]") || !strings.Contains(s, "[]") ||
				!strings.Contains(s, `{x: 1, y: "p"}`) || !strings.Contains(s, `{x: null, y: "q"}`) {
				t.Errorf("%s: the list and the struct:\n%s", tc.name, s)
			}
		}
		s, err := ursus.ReadIPCSchema(filepath.Join("testdata", "ipc", "polars_types.arrow"))
		if err != nil || strings.Join(s.Names(), " ") != "i s f b d t l st" || s.Field(6).Type != ursus.List(ursus.Int64) {
			t.Errorf("the schema: %v, %v", s, err)
		}
	})

	t.Run("written and read back", func(t *testing.T) {
		lf := ursus.ScanIPC(filepath.Join("testdata", "ipc", "polars_types.arrow"))
		all, err := lf.Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		for _, tc := range []struct {
			name string
			opts []ursus.IPCSinkOption
		}{
			{"uncompressed", nil},
			{"LZ4", []ursus.IPCSinkOption{ursus.WithIPCCompression(ursus.IPCLZ4)}},
			{"Zstandard", []ursus.IPCSinkOption{ursus.WithIPCCompression(ursus.IPCZstd)}},
		} {
			var file, stream bytes.Buffer
			if err := lf.WriteIPC(ctx, &file, tc.opts...); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if err := lf.WriteIPCStream(ctx, &stream, tc.opts...); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			fp, sp := filepath.Join(dir, tc.name+".arrow"), filepath.Join(dir, tc.name+".arrows")
			if err := os.WriteFile(fp, file.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(sp, stream.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, back := range []*ursus.LazyFrame{ursus.ScanIPC(fp), ursus.ScanIPCStream(sp)} {
				got, err := back.Collect(ctx)
				if err != nil {
					t.Fatalf("%s read back: %v", tc.name, err)
				}
				ursustest.AssertFrameEqual(t, got, all)
			}
		}
		// Compression changes the bytes; and a file of a batch a row reads back in
		// its order.
		var plain, lz4, zstd bytes.Buffer
		for _, w := range []struct {
			buf  *bytes.Buffer
			opts []ursus.IPCSinkOption
		}{{&plain, nil}, {&lz4, []ursus.IPCSinkOption{ursus.WithIPCCompression(ursus.IPCLZ4)}},
			{&zstd, []ursus.IPCSinkOption{ursus.WithIPCCompression(ursus.IPCZstd)}}} {
			if err := lf.WriteIPC(ctx, w.buf, w.opts...); err != nil {
				t.Fatal(err)
			}
		}
		if bytes.Equal(plain.Bytes(), lz4.Bytes()) || bytes.Equal(plain.Bytes(), zstd.Bytes()) ||
			bytes.Equal(lz4.Bytes(), zstd.Bytes()) {
			t.Error("the three codecs wrote the same bytes")
		}
		rows := filepath.Join(dir, "rows.arrow")
		if err := lf.SinkIPC(ctx, rows, ursus.WithBatchSize(1)); err != nil {
			t.Fatal(err)
		}
		got, err := ursus.ScanIPC(rows).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, all)

		// SinkIPC writes the file whole, or nothing.
		path := filepath.Join(dir, "sunk.arrow")
		if err := lf.Filter(c("i").IsNotNull()).SinkIPC(ctx, path); err != nil {
			t.Fatal(err)
		}
		got, err = ursus.ScanIPC(path).Collect(ctx)
		if err != nil || got.Height() != 2 || got.Width() != 8 {
			t.Errorf("sunk: %v, %v", got, err)
		}
		if err := lf.Select(c("nope")).SinkIPC(ctx, filepath.Join(dir, "never.arrow")); err == nil {
			t.Error("a failed query wrote a file")
		}
		if _, err := os.Stat(filepath.Join(dir, "never.arrow")); !os.IsNotExist(err) {
			t.Errorf("a failed sink left a file: %v", err)
		}
		if left, _ := filepath.Glob(filepath.Join(dir, ".never.arrow.*")); len(left) > 0 {
			t.Errorf("a failed sink left its temporary: %v", left)
		}
	})

	t.Run("the wrong format", func(t *testing.T) {
		if _, err := ursus.ScanIPC(filepath.Join("testdata", "ipc", "polars_types.arrows")).Collect(ctx); err == nil ||
			!strings.Contains(err.Error(), "ScanIPCStream") {
			t.Errorf("a stream read as a file: %v", err)
		}
		if _, err := ursus.ScanIPCStream(filepath.Join("testdata", "ipc", "polars_types.arrow")).Collect(ctx); err == nil ||
			!strings.Contains(err.Error(), "ScanIPC") {
			t.Errorf("a file read as a stream: %v", err)
		}
		if _, err := ursus.ScanIPC(filepath.Join("testdata", "ipc", "absent.arrow")).Collect(ctx); err == nil {
			t.Error("a missing file read")
		}
	})
}

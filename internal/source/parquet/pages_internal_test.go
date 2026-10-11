package parquet

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/compress"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/schema"

	"github.com/advenn/ursus/internal/arrowout"
	"github.com/advenn/ursus/internal/source"
)

// pagesCol is one column of the fixture: its node, and how to write rows [lo, hi)
// given their definition levels.
type pagesCol struct {
	node  schema.Node
	write func(cw file.ColumnChunkWriter, lo, hi int, defs []int16) error
}

// present is whether row i of a column holds a value, under one null pattern.
func present(nulls string, i int) bool {
	switch nulls {
	case "scattered":
		return i%7 != 3
	case "runs":
		return i%200 >= 60 // runs of 60 nulls, then 140 values
	case "all":
		return false
	}
	return true
}

func pagesNode(t *testing.T, name string, rep parquet.Repetition, lt schema.LogicalType,
	pt parquet.Type) schema.Node {
	t.Helper()
	n, err := schema.NewPrimitiveNodeLogical(name, rep, lt, pt, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// typed builds a column whose row i holds val(i), written through writer W.
func typed[V any](t *testing.T, name string, optional bool, lt schema.LogicalType, pt parquet.Type,
	nulls string, val func(i int) V,
	wb func(cw file.ColumnChunkWriter, vals []V, defs []int16) error) pagesCol {
	rep := parquet.Repetitions.Required
	if optional {
		rep = parquet.Repetitions.Optional
	}
	return pagesCol{
		node: pagesNode(t, name, rep, lt, pt),
		write: func(cw file.ColumnChunkWriter, lo, hi int, defs []int16) error {
			// A hundred values a call, so a small page size closes many pages.
			for at := lo; at < hi; at += 100 {
				end := min(at+100, hi)
				var vals []V
				var d []int16
				for i := at; i < end; i++ {
					if !optional || present(nulls, i) {
						vals = append(vals, val(i))
					}
				}
				if optional {
					d = defs[at-lo : end-lo]
				}
				if err := wb(cw, vals, d); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func writeBatch[W interface {
	WriteBatch([]V, []int16, []int16) (int64, error)
}, V any](cw file.ColumnChunkWriter, vals []V, defs []int16) error {
	_, err := cw.(W).WriteBatch(vals, defs, nil)
	return err
}

// writePagesFixture writes rows rows of every type the reader decodes from its
// pages, in row groups of 1,700, under props.
func writePagesFixture(t *testing.T, path string, rows int, nulls string, props ...parquet.WriterProperty) {
	t.Helper()
	words := []string{"AIR", "AIR REG", "DELIVER IN PERSON", "", "MAIL", "TRUCK", "é漢字", "x"}
	opt, req := true, false
	none := schema.NoLogicalType{}
	cols := []pagesCol{
		typed(t, "i8", opt, schema.NewIntLogicalType(8, true), parquet.Types.Int32, nulls,
			func(i int) int32 { return int32(int8(i * 37)) }, writeBatch[*file.Int32ColumnChunkWriter, int32]),
		typed(t, "i16", req, schema.NewIntLogicalType(16, true), parquet.Types.Int32, nulls,
			func(i int) int32 { return int32(int16(i * 7919)) }, writeBatch[*file.Int32ColumnChunkWriter, int32]),
		typed(t, "i32", opt, none, parquet.Types.Int32, nulls,
			func(i int) int32 { return int32(i%41) - 20 }, writeBatch[*file.Int32ColumnChunkWriter, int32]),
		typed(t, "u8", opt, schema.NewIntLogicalType(8, false), parquet.Types.Int32, nulls,
			func(i int) int32 { return int32(uint8(i * 3)) }, writeBatch[*file.Int32ColumnChunkWriter, int32]),
		typed(t, "u16", opt, schema.NewIntLogicalType(16, false), parquet.Types.Int32, nulls,
			func(i int) int32 { return int32(uint16(i * 977)) }, writeBatch[*file.Int32ColumnChunkWriter, int32]),
		typed(t, "u32", opt, schema.NewIntLogicalType(32, false), parquet.Types.Int32, nulls,
			func(i int) int32 { return int32(uint32(i) * 2654435761) }, writeBatch[*file.Int32ColumnChunkWriter, int32]),
		typed(t, "i64", opt, none, parquet.Types.Int64, nulls,
			func(i int) int64 { return int64(i) * 1_000_003 }, writeBatch[*file.Int64ColumnChunkWriter, int64]),
		typed(t, "u64", req, schema.NewIntLogicalType(64, false), parquet.Types.Int64, nulls,
			func(i int) int64 { return int64(uint64(i) * 0x9e3779b97f4a7c15) }, writeBatch[*file.Int64ColumnChunkWriter, int64]),
		typed(t, "f32", opt, none, parquet.Types.Float, nulls,
			func(i int) float32 { return float32(i%13) / 4 }, writeBatch[*file.Float32ColumnChunkWriter, float32]),
		typed(t, "f64", req, none, parquet.Types.Double, nulls,
			func(i int) float64 { return float64(i) * 0.5 }, writeBatch[*file.Float64ColumnChunkWriter, float64]),
		typed(t, "date", opt, schema.DateLogicalType{}, parquet.Types.Int32, nulls,
			func(i int) int32 { return int32(19000 + i%400) }, writeBatch[*file.Int32ColumnChunkWriter, int32]),
		typed(t, "ts", opt, schema.NewTimestampLogicalType(true, schema.TimeUnitMicros), parquet.Types.Int64, nulls,
			func(i int) int64 { return int64(i) * 3_600_000_123 }, writeBatch[*file.Int64ColumnChunkWriter, int64]),
		typed(t, "tms", opt, schema.NewTimeLogicalType(true, schema.TimeUnitMillis), parquet.Types.Int32, nulls,
			func(i int) int32 { return int32(i * 1000 % 86_400_000) }, writeBatch[*file.Int32ColumnChunkWriter, int32]),
		typed(t, "tus", opt, schema.NewTimeLogicalType(true, schema.TimeUnitMicros), parquet.Types.Int64, nulls,
			func(i int) int64 { return int64(i) * 999_999 % 86_400_000_000 }, writeBatch[*file.Int64ColumnChunkWriter, int64]),
		typed(t, "dec32", opt, schema.NewDecimalLogicalType(9, 2), parquet.Types.Int32, nulls,
			func(i int) int32 { return int32(i*101) - 50_000 }, writeBatch[*file.Int32ColumnChunkWriter, int32]),
		typed(t, "dec64", opt, schema.NewDecimalLogicalType(18, 3), parquet.Types.Int64, nulls,
			func(i int) int64 { return int64(i)*1_234_567 - 1e9 }, writeBatch[*file.Int64ColumnChunkWriter, int64]),
		typed(t, "s", opt, schema.StringLogicalType{}, parquet.Types.ByteArray, nulls,
			func(i int) parquet.ByteArray { return parquet.ByteArray(words[i%len(words)]) },
			writeBatch[*file.ByteArrayColumnChunkWriter, parquet.ByteArray]),
		typed(t, "s_req", req, schema.StringLogicalType{}, parquet.Types.ByteArray, nulls,
			func(i int) parquet.ByteArray { return parquet.ByteArray(words[(i*5)%len(words)]) },
			writeBatch[*file.ByteArrayColumnChunkWriter, parquet.ByteArray]),
		// Distinct and long: a small dictionary limit makes the writer fall back from
		// the dictionary to PLAIN partway through a chunk.
		typed(t, "wide", opt, schema.StringLogicalType{}, parquet.Types.ByteArray, nulls,
			func(i int) parquet.ByteArray {
				return parquet.ByteArray(fmt.Sprintf("w%d-%s", i, strings.Repeat("y", i%23)))
			},
			writeBatch[*file.ByteArrayColumnChunkWriter, parquet.ByteArray]),
		typed(t, "bin", opt, none, parquet.Types.ByteArray, nulls,
			func(i int) parquet.ByteArray { return parquet.ByteArray{byte(i), 0, byte(i >> 8)} },
			writeBatch[*file.ByteArrayColumnChunkWriter, parquet.ByteArray]),
	}
	fields := make(schema.FieldList, len(cols))
	for i, c := range cols {
		fields[i] = c.node
	}
	root, err := schema.NewGroupNode("schema", parquet.Repetitions.Required, fields, -1)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := file.NewParquetWriter(f, root, file.WithWriterProps(parquet.NewWriterProperties(props...)))
	for lo := 0; lo < rows; lo += 1700 {
		hi := min(lo+1700, rows)
		defs := make([]int16, hi-lo)
		for i := range defs {
			if present(nulls, lo+i) {
				defs[i] = 1
			}
		}
		rg := w.AppendRowGroup()
		for _, c := range cols {
			cw, err := rg.NextColumn()
			if err != nil {
				t.Fatal(err)
			}
			if err := c.write(cw, lo, hi, defs); err != nil {
				t.Fatal(err)
			}
			if err := cw.Close(); err != nil {
				t.Fatal(err)
			}
		}
		if err := rg.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// readAllPages reads path in batches of batch rows, with the reader's own pages on
// or off.
func readAllPages(t *testing.T, path string, batch int, own bool) []string {
	t.Helper()
	defer func(was bool) { ownPages = was }(ownPages)
	ownPages = own
	bs, err := openSource(t, path).Open(t.Context(), source.ScanSpec{Threads: 1, BatchSize: batch})
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()
	var out []string
	for {
		b, err := bs.Next(t.Context())
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		rec, err := arrowout.Record(b)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprint(rec))
		rec.Release()
	}
}

// TestOwnPagesReadAsArrowGoDoes: every type the reader decodes from its pages, in
// every layout a writer chooses, read with its own pages and through arrow-go's
// column reader, batch for batch (step 192).
//
// The layouts: V1 and V2 data pages; dictionary-encoded and PLAIN; pages of a
// hundred values and of the default size; a dictionary limit small enough that a
// chunk falls back to PLAIN partway; ZSTD. Each under four null patterns: none,
// scattered, runs of 60, and every row.
func TestOwnPagesReadAsArrowGoDoes(t *testing.T) {
	type layout struct {
		name  string
		props []parquet.WriterProperty
	}
	v1 := parquet.WithDataPageVersion(parquet.DataPageV1)
	v2 := parquet.WithDataPageVersion(parquet.DataPageV2)
	dict, plain := parquet.WithDictionaryDefault(true), parquet.WithDictionaryDefault(false)
	small := parquet.WithDataPageSize(512)
	overflow := parquet.WithDictionaryPageSizeLimit(300)
	layouts := []layout{
		{"v1 dictionary", []parquet.WriterProperty{v1, dict}},
		{"v2 dictionary", []parquet.WriterProperty{v2, dict}},
		{"v1 plain", []parquet.WriterProperty{v1, plain}},
		{"v2 plain", []parquet.WriterProperty{v2, plain}},
		{"v1 small pages", []parquet.WriterProperty{v1, dict, small}},
		{"v2 small pages plain", []parquet.WriterProperty{v2, plain, small}},
		{"v1 dictionary overflow", []parquet.WriterProperty{v1, dict, small, overflow}},
		{"v2 dictionary overflow", []parquet.WriterProperty{v2, dict, small, overflow}},
		{"zstd", []parquet.WriterProperty{v1, dict, parquet.WithCompression(compress.Codecs.Zstd)}},
	}
	dir := t.TempDir()
	for _, l := range layouts {
		for _, nulls := range []string{"none", "scattered", "runs", "all"} {
			name := fmt.Sprintf("%s, nulls %s", l.name, nulls)
			path := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".parquet")
			writePagesFixture(t, path, 4000, nulls, l.props...)
			checkOwned(t, path, 20)
			if strings.Contains(l.name, "overflow") && nulls != "all" {
				checkFallsBack(t, path, "wide")
			}
			for _, batch := range []int{1024, 333} {
				own, theirs := readAllPages(t, path, batch, true), readAllPages(t, path, batch, false)
				if len(own) != len(theirs) {
					t.Fatalf("%s, batches of %d: %d batches read from pages, %d through arrow-go",
						name, batch, len(own), len(theirs))
				}
				for i := range own {
					if own[i] != theirs[i] {
						t.Fatalf("%s, batches of %d: batch %d differs\npages:\n%.2000s\narrow-go:\n%.2000s",
							name, batch, i, own[i], theirs[i])
					}
				}
			}
		}
	}
}

// checkOwned requires want of the file's columns to be read from their pages.
func checkOwned(t *testing.T, path string, want int) {
	t.Helper()
	pf, err := file.OpenParquetFile(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer pf.Close()
	sc := pf.MetaData().Schema
	src := openSource(t, path)
	full, err := src.Schema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	owned := 0
	for ci := range sc.NumColumns() {
		if ownable(pf.MetaData().RowGroup(0), ci, sc.Column(ci), full.Field(ci).Type) {
			owned++
		}
	}
	if owned != want {
		t.Fatalf("%d columns of %s are read from their pages, want %d", owned, path, want)
	}
}

// checkFallsBack requires column name's first chunk to hold data pages of the
// dictionary's indices and data pages of PLAIN values, as a writer leaves it when
// the dictionary outgrows its limit. The dictionary page is PLAIN itself, so the
// chunk's list of encodings cannot say; its data pages are counted.
func checkFallsBack(t *testing.T, path, name string) {
	t.Helper()
	pf, err := file.OpenParquetFile(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer pf.Close()
	pr, err := pf.RowGroup(0).GetColumnPageReader(pf.MetaData().Schema.ColumnIndexByName(name))
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	var dict, plain int
	for pr.Next() {
		switch pg := pr.Page().(type) {
		case *file.DataPageV1, *file.DataPageV2:
			switch parquet.Encoding(pg.Encoding()) {
			case parquet.Encodings.RLEDict, parquet.Encodings.PlainDict:
				dict++
			case parquet.Encodings.Plain:
				plain++
			}
		}
	}
	if dict == 0 || plain == 0 {
		t.Fatalf("column %s of %s: %d dictionary pages and %d PLAIN, want both", name, path, dict, plain)
	}
}

// hybridEncode writes values as runs: an RLE run for a repeat of eight or more,
// and bit-packed groups of eight for the rest, padding the last group.
func hybridEncode(vals []uint32, width int) []byte {
	var out []byte
	putRLE := func(v uint32, n int) {
		out = binary.AppendUvarint(out, uint64(n)<<1)
		for i := range (width + 7) / 8 {
			out = append(out, byte(v>>(8*i)))
		}
	}
	putPacked := func(vs []uint32) {
		groups := (len(vs) + 7) / 8
		out = binary.AppendUvarint(out, uint64(groups)<<1|1)
		bits := make([]byte, groups*width)
		for i, v := range vs {
			for b := range width {
				if v>>uint(b)&1 == 1 {
					at := i*width + b
					bits[at/8] |= 1 << uint(at%8)
				}
			}
		}
		out = append(out, bits...)
	}
	for i := 0; i < len(vals); {
		j := i
		for j < len(vals) && vals[j] == vals[i] {
			j++
		}
		if j-i >= 8 {
			putRLE(vals[i], j-i)
			i = j
			continue
		}
		// A packed stretch up to the next long repeat, in whole groups where it can.
		k := i
		for k < len(vals) {
			r := k
			for r < len(vals) && vals[r] == vals[k] {
				r++
			}
			if r-k >= 8 && (k-i)%8 == 0 {
				break
			}
			k++
		}
		putPacked(vals[i:k])
		i = k
	}
	return out
}

// TestHybridDecodesWhatItWasGiven: random values at every width from 0 to 32, in
// repeats and noise, encoded and decoded back, at reads of random sizes.
func TestHybridDecodesWhatItWasGiven(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for width := 0; width <= 32; width++ {
		for trial := range 20 {
			n := 1 + rng.IntN(3000)
			vals := make([]uint32, n)
			mask := uint64(1)<<uint(width) - 1
			for i := range vals {
				switch {
				case i > 0 && rng.IntN(10) < 6:
					vals[i] = vals[i-1]
				default:
					vals[i] = uint32(rng.Uint64() & mask)
				}
			}
			var h hybrid
			h.init(hybridEncode(vals, width), width)
			got := make([]uint32, 0, n)
			buf := make([]uint32, 64)
			for len(got) < n {
				want := min(1+rng.IntN(100), n-len(got))
				val, k, ok, err := h.run(want, buf, "test")
				if err != nil {
					t.Fatalf("width %d, trial %d: %v after %d values", width, trial, err, len(got))
				}
				if ok {
					for range k {
						got = append(got, val)
					}
				} else {
					got = append(got, buf[:k]...)
				}
			}
			for i := range vals {
				if got[i] != vals[i] {
					t.Fatalf("width %d, trial %d: value %d is %d, want %d", width, trial, i, got[i], vals[i])
				}
			}
		}
	}
}

// TestHybridRefusesTruncatedData: data cut anywhere is an error when more values
// are asked for than it holds, and never a panic.
func TestHybridRefusesTruncatedData(t *testing.T) {
	vals := make([]uint32, 500)
	for i := range vals {
		vals[i] = uint32(i % 13)
		if i > 300 {
			vals[i] = 5
		}
	}
	// A bit-packed run whose last bytes are gone holds the values its bytes do, and
	// asking for the rest is an error, not zeros: twenty 4-bit values, three groups
	// of twelve bytes, cut to eight, which hold sixteen.
	packed := make([]uint32, 20)
	for i := range packed {
		packed[i] = uint32(i % 16)
	}
	short := hybridEncode(packed, 4)
	short = short[:len(short)-4]
	var h hybrid
	h.init(short, 4)
	got := make([]uint32, 16)
	if err := h.read(got, "test"); err != nil || got[15] != 15 {
		t.Fatalf("the sixteen values the cut run holds: %v, %v", got, err)
	}
	if err := h.read(make([]uint32, 4), "test"); err == nil {
		t.Fatal("the four values cut from a bit-packed run read without an error")
	}

	enc := hybridEncode(vals, 4)
	for cut := range len(enc) {
		var h hybrid
		h.init(enc[:cut], 4)
		if err := h.read(make([]uint32, len(vals)), "test"); err == nil {
			t.Fatalf("data cut at %d of %d bytes read %d values", cut, len(enc), len(vals))
		}
	}
}

// FuzzHybrid: any bytes, at any width, decode to values or an error.
func FuzzHybrid(f *testing.F) {
	f.Add(hybridEncode([]uint32{1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 3}, 2), 2, 11)
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0x0f}, 32, 100)
	f.Fuzz(func(t *testing.T, data []byte, width, n int) {
		if width < 0 || width > 32 || n < 0 || n > 1<<16 {
			return
		}
		var h hybrid
		h.init(data, width)
		dst := make([]uint32, n)
		if err := h.read(dst, "fuzz"); err != nil {
			return
		}
		if width < 32 {
			for _, v := range dst {
				if v >= 1<<uint(width) && width > 0 || width == 0 && v != 0 {
					t.Fatalf("a %d-bit value of %d", width, v)
				}
			}
		}
	})
}

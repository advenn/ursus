package parquet

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"slices"
	"testing"

	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/schema"

	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/source"
)

func allocatedBytes() uint64 {
	s := []metrics.Sample{{Name: "/gc/heap/allocs:bytes"}}
	metrics.Read(s)
	return s[0].Value.Uint64()
}

// TestReadingAllocatesAboutWhatItProduces: 400,000 rows of three Int64 columns and a
// nullable String, read serially and on four threads, against the bytes of the
// columns produced.
//
// A fixed-width column dropped its values slice every batch, its comment saying
// data.NewFixed wraps it; NewFixed copies, so each batch's values were allocated
// twice. In h2o j5's profile over Parquet that was 0.74 GB (step 137), the CSV
// reader's mistake of step 136 again. Step 137 kept the slice; since step 163 the
// column takes it, and a String column takes its offsets and characters, which
// data.NewStringParts had copied. Nothing covered a String column until then.
//
// From the second batch on, as the CSV reader's test reads: the first grows what a
// reader grows once.
//
// Not parallel, so no other test allocates while it reads.
func TestReadingAllocatesAboutWhatItProduces(t *testing.T) {
	const rows = 400_000
	cols := make([][]int64, 3)
	for c := range cols {
		cols[c] = make([]int64, rows)
		for i := range rows {
			cols[c][i] = int64(i*(c+1)) % 100_003
		}
	}
	strs := make([]string, rows)
	for i := range rows {
		if i%9 != 0 {
			strs[i] = fmt.Sprintf("value-%d", i%5_000)
		}
	}
	path := filepath.Join(t.TempDir(), "t.parquet")
	writeIntsAndStrings(t, path, []string{"a", "b", "c"}, cols, "s", strs)

	for _, threads := range []int{1, 4} {
		bs, err := openSource(t, path).Open(t.Context(), source.ScanSpec{Threads: threads, BatchSize: 8192})
		if err != nil {
			t.Fatal(err)
		}
		first, err := bs.Next(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		n := first.Rows()
		runtime.GC()
		before := allocatedBytes()
		var produced int64
		for {
			b, err := bs.Next(t.Context())
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			n += b.Rows()
			for _, c := range b.Columns() {
				produced += c.NBytes()
			}
		}
		allocated := int64(allocatedBytes() - before)
		bs.Close()
		if n != rows {
			t.Fatalf("%d threads: read %d rows, want %d", threads, n, rows)
		}
		t.Logf("%d threads: allocated %.1f MB to produce %.1f MB: %.2f", threads,
			float64(allocated)/(1<<20), float64(produced)/(1<<20), float64(allocated)/float64(produced))
		// Not under the race detector, where arrow-go's string decoding allocates
		// about a third more: 1.49 against 1.21 at step 163. The first batch is
		// checked in every build.
		if allocated > produced*13/10 && !raceDetector {
			t.Errorf("%d threads: reading allocated %d bytes to produce %d", threads, allocated, produced)
		}
		// Each column owns its slices: the first batch must read as it did after every
		// later one was read.
		ss, err := data.TypedColumn[string](first.Column(len(cols)))
		if err != nil {
			t.Fatal(err)
		}
		for i := range first.Rows() {
			v, ok := ss.Get(i)
			if want := strs[i]; ok != (i%9 != 0) || v != want {
				t.Fatalf("%d threads: the first batch's row %d of the strings reads %q (%v), want %q, "+
					"after the rest was read", threads, i, v, ok, want)
			}
		}
		for c := range cols {
			vals, err := data.TypedColumn[int64](first.Column(c))
			if err != nil {
				t.Fatal(err)
			}
			for i := range first.Rows() {
				if v, _ := vals.Get(i); v != cols[c][i] {
					t.Fatalf("%d threads: the first batch's row %d of column %d reads %d, want %d, "+
						"after the rest was read", threads, i, c, v, cols[c][i])
				}
			}
		}
	}
}

// TestAListColumnsFirstBatchSurvivesTheRest: a List column's elements are kept in a
// slice the column reuses from batch to batch, as a flat column's values are, which
// is safe only because data.NewFixed copies out of it. Nothing else read a batch
// after the next one was built: readLists decodes each as it arrives.
func TestAListColumnsFirstBatchSurvivesTheRest(t *testing.T) {
	rows := make([]row, 5000)
	for i := range rows {
		switch i % 7 {
		case 0:
			rows[i] = row{ok: false}
		case 1:
			rows[i] = row{ok: true}
		default:
			rows[i] = row{vals: []int64{int64(i), int64(i * 3), int64(i % 11)}, ok: true}
		}
	}
	bs, err := openSource(t, writeLists(t, rows)).Open(t.Context(), source.ScanSpec{BatchSize: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()
	first, err := bs.Next(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	n := first.Rows()
	for {
		b, err := bs.Next(t.Context())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		n += b.Rows()
	}
	if n != len(rows) {
		t.Fatalf("read %d rows, want %d", n, len(rows))
	}
	got := decodeLists(t, first.Columns()[0])
	for i, r := range got {
		want := rows[i]
		if r.ok != want.ok || !slices.Equal(r.vals, want.vals) {
			t.Fatalf("the first batch's row %d reads %v, want %v, after the rest was read", i, r, want)
		}
	}
}

// writeIntsAndStrings writes required Int64 columns and a nullable String column,
// an empty string standing for a null, in one row group.
func writeIntsAndStrings(t *testing.T, path string, names []string, ints [][]int64, sname string, strs []string) {
	t.Helper()
	fields := make(schema.FieldList, 0, len(names)+1)
	for _, n := range names {
		node, err := schema.NewPrimitiveNodeLogical(n, parquet.Repetitions.Required,
			schema.NewIntLogicalType(64, true), parquet.Types.Int64, -1, -1)
		if err != nil {
			t.Fatal(err)
		}
		fields = append(fields, node)
	}
	snode, err := schema.NewPrimitiveNodeLogical(sname, parquet.Repetitions.Optional,
		schema.StringLogicalType{}, parquet.Types.ByteArray, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	fields = append(fields, snode)
	root, err := schema.NewGroupNode("schema", parquet.Repetitions.Required, fields, -1)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := file.NewParquetWriter(f, root)
	rg := w.AppendRowGroup()
	for _, c := range ints {
		cw, err := rg.NextColumn()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cw.(*file.Int64ColumnChunkWriter).WriteBatch(c, nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := cw.Close(); err != nil {
			t.Fatal(err)
		}
	}
	cw, err := rg.NextColumn()
	if err != nil {
		t.Fatal(err)
	}
	var vals []parquet.ByteArray
	defs := make([]int16, len(strs))
	for i, s := range strs {
		if s != "" {
			vals, defs[i] = append(vals, parquet.ByteArray(s)), 1
		}
	}
	if _, err := cw.(*file.ByteArrayColumnChunkWriter).WriteBatch(vals, defs, nil); err != nil {
		t.Fatal(err)
	}
	if err := cw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rg.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

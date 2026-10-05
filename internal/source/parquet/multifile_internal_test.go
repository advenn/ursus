package parquet

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/schema"

	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/source"
)

// writeInts writes one row group of REQUIRED Int64 columns, named and filled in
// order.
func writeInts(t *testing.T, path string, names []string, cols ...[]int64) {
	t.Helper()
	fields := make(schema.FieldList, len(names))
	for i, n := range names {
		node, err := schema.NewPrimitiveNodeLogical(n, parquet.Repetitions.Required,
			schema.NewIntLogicalType(64, true), parquet.Types.Int64, -1, -1)
		if err != nil {
			t.Fatal(err)
		}
		fields[i] = node
	}
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
	for _, c := range cols {
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
	if err := rg.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestOpenHonoursAReorderedProjection: a projection is the exact column list to
// emit, IN THAT ORDER, and Next names chunk i after the projection's field i.
//
// The column plans were built in the FILE's order, so {b, a} over a file {a, b}
// emitted a's values under the name b. No public query reaches it — projection
// pushdown emits projections in source order — which is why it is asserted here.
func TestOpenHonoursAReorderedProjection(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ab.parquet")
	writeInts(t, p, []string{"a", "b"}, []int64{1, 2}, []int64{10, 20})
	r, err := openSource(t, p).Open(t.Context(), source.ScanSpec{Projection: []string{"b", "a"}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	b, err := r.Next(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := b.Schema().Names(); !slices.Equal(got, []string{"b", "a"}) {
		t.Fatalf("columns %v, want [b a]", got)
	}
	got, err := data.Values[int64](b.Column(0))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []int64{10, 20}) {
		t.Errorf("column b = %v, want [10 20] — a's values under b's name", got)
	}
}

// TestSchemaStopsWhenTheContextIsCancelled: Schema reads every file's footer, and
// a glob can name thousands. A cancelled query must stop between footers, not read
// all of them first.
func TestSchemaStopsWhenTheContextIsCancelled(t *testing.T) {
	dir := t.TempDir()
	var opens []Opener
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	for i := range 3 {
		p := filepath.Join(dir, "part"+string(rune('0'+i))+".parquet")
		writeInts(t, p, []string{"a"}, []int64{int64(i)})
		opens = append(opens, func(context.Context) (parquet.ReaderAtSeeker, io.Closer, error) {
			if i == 1 {
				cancel() // the query is cancelled while the second footer is read
			}
			f, err := os.Open(p)
			return f, f, err
		})
	}
	_, err := New(opens, "three parts", Options{}).Schema(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Schema = %v, want context.Canceled before the third footer", err)
	}
}

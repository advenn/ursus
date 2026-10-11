package ursus_test

// Step 179, audit row I23: a null count, and a number's bounds, are trusted only in
// a file from a writer known to write them in full. arrow-go cannot tell an absent
// null_count from zero, nor an absent bound from 0, so a file from any other writer
// is read whole where either would decide.

import (
	"os"
	"path/filepath"
	"testing"

	arrowpq "github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/schema"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/source/parquet"
)

// fourGroups writes v, an optional Int64, and s, an optional string, in four row
// groups — v: [1 2 3] [∅ 5 6] [7 8 9] [10 ∅ 12]; s: a, b, c, d by group — with
// the given created_by.
func fourGroups(t *testing.T, createdBy string) string {
	t.Helper()
	v := schema.NewInt64Node("v", arrowpq.Repetitions.Optional, -1)
	s, err := schema.NewPrimitiveNodeLogical("s", arrowpq.Repetitions.Optional, schema.StringLogicalType{},
		arrowpq.Types.ByteArray, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	root, err := schema.NewGroupNode("schema", arrowpq.Repetitions.Required, schema.FieldList{v, s}, -1)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "groups.parquet")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := file.NewParquetWriter(f, root, file.WithWriterProps(arrowpq.NewWriterProperties(arrowpq.WithCreatedBy(createdBy))))
	for g, vals := range [][]int64{{1, 2, 3}, {5, 6}, {7, 8, 9}, {10, 12}} {
		defs := []int16{1, 1, 1}
		switch g {
		case 1:
			defs = []int16{0, 1, 1}
		case 3:
			defs = []int16{1, 0, 1}
		}
		rg := w.AppendRowGroup()
		cw, _ := rg.NextColumn()
		if _, err := cw.(*file.Int64ColumnChunkWriter).WriteBatch(vals, defs, nil); err != nil {
			t.Fatal(err)
		}
		cw.Close()
		cw, _ = rg.NextColumn()
		str := arrowpq.ByteArray(string(rune('a' + g)))
		if _, err := cw.(*file.ByteArrayColumnChunkWriter).WriteBatch([]arrowpq.ByteArray{str, str, str},
			[]int16{1, 1, 1}, nil); err != nil {
			t.Fatal(err)
		}
		cw.Close()
		rg.Close()
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPruningTrustsOnlyAKnownWritersStatistics(t *testing.T) {
	c := ursus.Col
	for _, tc := range []struct {
		name   string
		filter ursus.Expr
		rows   int
		// skipped from a known writer's file, and from another's.
		known, other int
	}{
		{"IsNull", c("v").IsNull(), 2, 2, 0},
		{"a number's range", c("v").Gt(9), 2, 3, 0},
		{"a string's range", c("s").Eq("c"), 3, 3, 3},
	} {
		for _, writer := range []struct {
			createdBy string
			skipped   int
		}{
			{"parquet-cpp-arrow version 25.0.1", tc.known},
			{"DuckDB version v1.5.5 (build d8cdaa33fd)", tc.known},
			{"Polars", tc.known},
			{"parquet-go version 18.7.0", tc.known},
			{"some-other-writer version 0.1", tc.other},
			{"", tc.other},
		} {
			t.Run(tc.name+"/"+writer.createdBy, func(t *testing.T) {
				path := fourGroups(t, writer.createdBy)
				src := parquet.New([]parquet.Opener{fileOpener(t, path)}, path, parquet.DefaultOptions())
				df, err := ursus.Scan(src).Filter(tc.filter).Collect(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if df.Height() != tc.rows {
					t.Fatalf("%d rows, want %d", df.Height(), tc.rows)
				}
				if _, skipped := src.RowGroupStats(); skipped != writer.skipped {
					t.Errorf("skipped %d row groups, want %d", skipped, writer.skipped)
				}
			})
		}
	}
}

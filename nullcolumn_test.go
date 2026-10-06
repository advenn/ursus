package ursus_test

// A Null-typed column concatenates, writes and reads back (step 108).
//
// audit.md J12: any frame with a Null column — the type of a typed null literal,
// and what an all-null CSV column infers as — failed every Collect that
// concatenated batches, with "concat is not implemented for Null". I18: both
// writers refused it, where Polars and PyArrow write it, PyArrow as Parquet's NULL
// logical type on INT32; and the reader read that type as its physical type.

import (
	"bytes"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
)

// nullColumnFrame is three rows with a Null column in the middle.
func nullColumnFrame() *ursus.LazyFrame {
	return ursus.Frame(ursus.Values("id", []int64{1, 2, 3}), ursus.Values("name", []string{"a", "b", "c"})).
		Select(ursus.Col("id"), ursus.Null(dtype.Null).Alias("nothing"), ursus.Col("name"))
}

// requireNullColumn checks df is nullColumnFrame's rows, with "nothing" Null and null.
func requireNullColumn(t *testing.T, df *ursus.DataFrame, rows int) {
	t.Helper()
	if df.Height() != rows {
		t.Fatalf("%d rows, want %d", df.Height(), rows)
	}
	typ, got := textOf(t, df, "nothing")
	if typ != "Null" {
		t.Fatalf("nothing is %s, want Null", typ)
	}
	for i, v := range got {
		if v != "∅" {
			t.Fatalf("row %d of nothing is %q, want null", i, v)
		}
	}
}

func TestANullColumnConcatenates(t *testing.T) {
	t.Run("Concat", func(t *testing.T) {
		df, err := ursus.Concat([]*ursus.LazyFrame{nullColumnFrame(), nullColumnFrame()}).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		requireNullColumn(t, df, 6)
	})
	t.Run("Collect over batches of one row", func(t *testing.T) {
		df, err := nullColumnFrame().Collect(t.Context(), ursus.WithBatchSize(1))
		if err != nil {
			t.Fatal(err)
		}
		requireNullColumn(t, df, 3)
	})
}

func TestANullColumnWritesAndReadsBack(t *testing.T) {
	t.Run("Parquet, across row groups", func(t *testing.T) {
		var buf bytes.Buffer
		big := ursus.Concat([]*ursus.LazyFrame{nullColumnFrame(), nullColumnFrame(), nullColumnFrame(), nullColumnFrame()})
		if err := big.WriteParquet(t.Context(), &buf, ursus.WithRowGroupRows(5)); err != nil {
			t.Fatal(err)
		}
		df, err := ursus.ScanParquetBytes(buf.Bytes(), "null.parquet").Collect(t.Context(), ursus.WithBatchSize(4))
		if err != nil {
			t.Fatal(err)
		}
		requireNullColumn(t, df, 12)
		if _, ids := textOf(t, df, "id"); !slices.Equal(ids, []string{"1", "2", "3", "1", "2", "3", "1", "2", "3", "1", "2", "3"}) {
			t.Errorf("the columns beside it moved: id %v", ids)
		}
	})
	t.Run("Parquet written by PyArrow", func(t *testing.T) {
		b, err := os.ReadFile("testdata/parquet/pyarrow_null_column.parquet")
		if err != nil {
			t.Fatal(err)
		}
		df, err := ursus.ScanParquetBytes(b, "pyarrow_null_column.parquet").Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		requireNullColumn(t, df, 3)
		if _, names := textOf(t, df, "name"); !slices.Equal(names, []string{"a", "∅", "c"}) {
			t.Errorf("name %v, want [a ∅ c]", names)
		}
	})
	t.Run("CSV, as empty fields", func(t *testing.T) {
		var buf bytes.Buffer
		if err := nullColumnFrame().WriteCSV(t.Context(), &buf); err != nil {
			t.Fatal(err)
		}
		want := "id,nothing,name\n1,,a\n2,,b\n3,,c\n"
		if got := buf.String(); got != want {
			t.Errorf("wrote %q, want %q", got, want)
		}
		if !strings.Contains(buf.String(), ",,") {
			t.Error("the null column wrote something")
		}
	})
}

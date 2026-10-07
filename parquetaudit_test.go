package ursus_test

// What the audit of 0.4's Parquet work found (step 119).

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/advenn/ursus"
)

// TestNullFieldsInNestedColumns: PyArrow infers Null for a struct field or a list
// element that only ever holds None. Step 108 mapped Parquet's NULL type to Null,
// and the reader then could not say whether a struct whose first field was Null
// was present — ErrInternal on a file that read before — nor read a List(Null),
// which ursus itself writes.
func TestNullFieldsInNestedColumns(t *testing.T) {
	c := ursus.Col
	lf := ursus.ScanParquet("testdata/parquet/pyarrow_null_nested.parquet")
	df, err := lf.Select(
		c("s").IsNull().Alias("s_null"),
		c("s").Struct().Field("b").Alias("b"),
		c("l").IsNull().Alias("l_null"),
		c("l").List().Len().Alias("n"),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]string{
		"s_null": {"false", "true", "false"},
		"b":      {"1", "∅", "3"},
		"l_null": {"false", "false", "true"},
		"n":      {"2", "0", "∅"},
	} {
		if _, got := textOf(t, df, name); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
	sch, err := lf.CollectSchema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := sch.Field(1).Type.String(); got != "List(Null)" {
		t.Errorf("l is %s, want List(Null)", got)
	}
}

// TestParquetEncodingsByPhysicalType: the up-front check that keeps arrow-go's
// decoder from panicking knew one pairing, BYTE_STREAM_SPLIT, and refused it on
// the integers, which decode it. A FIXED_LEN_BYTE_ARRAY under DELTA_BYTE_ARRAY,
// which does not decode, reached the panic and was reported as a corrupt file.
func TestParquetEncodingsByPhysicalType(t *testing.T) {
	df, err := ursus.ScanParquet("testdata/parquet/pyarrow_bss_int.parquet").Collect(t.Context())
	if err != nil {
		t.Fatalf("an INT32 column under BYTE_STREAM_SPLIT: %v", err)
	}
	if _, got := textOf(t, df, "i"); !slices.Equal(got, []string{"1", "-2", "3", "400000"}) {
		t.Errorf("%v", got)
	}

	_, err = ursus.ScanParquet("testdata/parquet/pyarrow_flba_delta.parquet").Collect(t.Context())
	if !errors.Is(err, ursus.ErrUnsupported) || !strings.Contains(err.Error(), "DELTA_BYTE_ARRAY") {
		t.Errorf("want the encoding refused by name, got %v", err)
	}
}

// TestALaterFileFailingCostsNoEarlierRows: the parallel reader opens the next file,
// and reads a later row group's statistics, as soon as a worker is free. A failure
// there was returned at once, ahead of the rows of the row groups already in
// flight, which come first in the input. The serial reader never did that.
func TestALaterFileFailingCostsNoEarlierRows(t *testing.T) {
	c := ursus.Col
	var a, b bytes.Buffer
	vals := make([]int64, 30)
	for i := range vals {
		vals[i] = int64(i + 1)
	}
	if err := ursus.Frame(ursus.Values("x", vals)).WriteParquet(t.Context(), &a, ursus.WithRowGroupRows(10)); err != nil {
		t.Fatal(err)
	}
	if err := ursus.Frame(ursus.Values("x", []int64{100})).WriteParquet(t.Context(), &b); err != nil {
		t.Fatal(err)
	}
	gone := errors.New("the object was deleted")
	files := func() []ursus.ParquetFile {
		var opens atomic.Int32
		return []ursus.ParquetFile{
			{Name: "a", Open: func(context.Context) (io.ReaderAt, int64, error) {
				return bytes.NewReader(a.Bytes()), int64(a.Len()), nil
			}},
			{Name: "b", Open: func(context.Context) (io.ReaderAt, int64, error) {
				// The schema is read while the query is planned; the scan's open fails.
				if opens.Add(1) > 1 {
					return nil, 0, gone
				}
				return bytes.NewReader(b.Bytes()), int64(b.Len()), nil
			}},
		}
	}

	t.Run("every row before the failure, then the failure", func(t *testing.T) {
		rows := 0
		var err error
		for bt, e := range ursus.ScanParquetFrom(files()).CollectBatches(t.Context(), ursus.WithThreads(4)) {
			if e != nil {
				err = e
				break
			}
			rows += bt.Height()
		}
		if rows != 30 || !errors.Is(err, gone) {
			t.Errorf("%d rows, then %v; want file a's 30, then its failure", rows, err)
		}
	})
	t.Run("a head the first file satisfies", func(t *testing.T) {
		got := runs(t, ursus.ScanParquetFrom(files()).Filter(c("x").Gt(0)).Head(5), "x")
		if !slices.Equal(got, []string{"1", "2", "3", "4", "5"}) {
			t.Errorf("%v", got)
		}
	})
}

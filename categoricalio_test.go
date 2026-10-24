package ursus_test

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus"
)

// TestCategoricalIO is step 352: a Categorical in and out of Arrow, IPC and Parquet as
// Polars writes one, a dictionary of its values, and QCut and Hist answering one, as
// Polars' qcut and hist do. Every answer is Polars 1.44.1's.
func TestCategoricalIO(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	lf := ursus.Frame(strs("s", "b", "a", "b", nil, "c"), i64s("v", 1, 2, 3, 4, 5)).
		Select(c("s").Cast(ursus.Categorical).Alias("c"), c("v"))
	want := "b a b <nil> c Categorical"

	// Polars' own files: its Parquet footer and IPC schema call the column a
	// dictionary of text.
	if got := cells(t, ursus.ScanParquet(filepath.Join("testdata", "parquet", "polars_categorical.parquet")), "c"); got != want {
		t.Errorf("Polars' Parquet: %s", got)
	}
	if got := cells(t, ursus.ScanIPC(filepath.Join("testdata", "ipc", "polars_categorical.arrow")), "c"); got != want {
		t.Errorf("Polars' IPC: %s", got)
	}

	// Written and read back.
	path := filepath.Join(t.TempDir(), "c.parquet")
	if err := lf.SinkParquet(ctx, path); err != nil {
		t.Fatal(err)
	}
	if got := cells(t, ursus.ScanParquet(path), "c"); got != want {
		t.Errorf("Parquet, written and read: %s", got)
	}
	var ipcBuf bytes.Buffer
	if err := lf.WriteIPC(ctx, &ipcBuf); err != nil {
		t.Fatal(err)
	}
	ipcPath := filepath.Join(t.TempDir(), "c.arrow")
	writeText(t, filepath.Dir(ipcPath), filepath.Base(ipcPath), ipcBuf.String())
	if got := cells(t, ursus.ScanIPC(ipcPath), "c"); got != want {
		t.Errorf("IPC, written and read: %s", got)
	}

	// Arrow: a dictionary of its values, its codes the order they first appear in.
	df := collect(t, lf)
	rec, err := df.Record()
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Release()
	col := rec.Column(0)
	dict, ok := col.(*array.Dictionary)
	if !ok {
		t.Fatalf("an Arrow %s, want a dictionary", col.DataType())
	}
	if dict.Dictionary().String() != `["b" "a" "c"]` || dict.Indices().String() != "[0 1 0 (null) 2]" {
		t.Errorf("the dictionary %s, its indices %s", dict.Dictionary(), dict.Indices())
	}
	if got := cells(t, ursus.ScanArrowRecords(rec), "c"); got != want {
		t.Errorf("Arrow, out and in: %s", got)
	}

	// A dictionary of numbers is its numbers still.
	mem := memory.NewGoAllocator()
	ib := array.NewInt32Builder(mem)
	ib.AppendValues([]int32{7, 9}, nil)
	values := ib.NewArray()
	xb := array.NewUint8Builder(mem)
	xb.AppendValues([]uint8{1, 0, 1}, nil)
	indices := xb.NewArray()
	nums := array.NewDictionaryArray(&arrow.DictionaryType{IndexType: arrow.PrimitiveTypes.Uint8, ValueType: arrow.PrimitiveTypes.Int32}, indices, values)
	numRec := array.NewRecord(arrow.NewSchema([]arrow.Field{{Name: "n", Type: nums.DataType()}}, nil), []arrow.Array{nums}, 3)
	if got := cells(t, ursus.ScanArrowRecords(numRec), "n"); got != "9 7 9 Int32" {
		t.Errorf("a dictionary of numbers: %s", got)
	}

	// QCut's labels come from the data, and Hist's category: a Categorical each.
	x := ursus.Frame(i64s("x", 1, 5, 9, nil))
	if got := cells(t, x.Select(c("x").QCut([]float64{0.5})), "x"); got != "(-inf, 5] (-inf, 5] (5, inf] <nil> Categorical" {
		t.Errorf("QCut: %s", got)
	}
	h, err := x.Select(c("x").Hist(ursus.HistBins(0, 5, 10), ursus.HistCategory())).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s := h.Schema().String(); !strings.Contains(s, "category: Categorical") {
		t.Errorf("Hist: %s", s)
	}
}

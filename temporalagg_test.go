package ursus_test

// Step 173: the mean and median of instants and durations, and their quantiles (step
// 300). Their answers are Polars 2.1.0's, recorded in the temporalagg group of
// testdata/oracle and spelled in oracle_temporalagg_test.go since step 498, which took
// this file's tables. What stays: a spread of instants refused, and Parquet's UUID and
// JSON columns read as Polars reads them.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/schema"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
)

// TestTemporalVarAndStdStillRefuse: a spread of instants is not implemented, as
// Polars' frame std and var answer null for one. A quantile answers since step 300,
// the temporalagg group's quantile cases.
func TestTemporalVarAndStdStillRefuse(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(ursus.Values("t", []time.Time{time.Unix(0, 0), time.Unix(60, 0)}))
	for name, e := range map[string]ursus.Expr{
		"var": c("t").Var(1), "std": c("t").Std(1),
	} {
		if _, err := lf.GroupBy().Agg(e).Collect(t.Context()); err == nil {
			t.Errorf("%s of a Datetime answered; it is not implemented", name)
		}
	}
}

// TestParquetUUIDAndJSONRead: a UUID column reads as its sixteen bytes, Binary, and a
// JSON column as its text, String, as Polars reads them.
func TestParquetUUIDAndJSONRead(t *testing.T) {
	u, err := schema.NewPrimitiveNodeLogical("u", parquet.Repetitions.Optional, schema.UUIDLogicalType{},
		parquet.Types.FixedLenByteArray, 16, -1)
	if err != nil {
		t.Fatal(err)
	}
	j, err := schema.NewPrimitiveNodeLogical("j", parquet.Repetitions.Optional, schema.JSONLogicalType{},
		parquet.Types.ByteArray, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	root, err := schema.NewGroupNode("schema", parquet.Repetitions.Required, schema.FieldList{u, j}, -1)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "uj.parquet")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := file.NewParquetWriter(f, root)
	rg := w.AppendRowGroup()
	cw, _ := rg.NextColumn()
	one := make(parquet.FixedLenByteArray, 16)
	one[15] = 1
	if _, err := cw.(*file.FixedLenByteArrayColumnChunkWriter).WriteBatch(
		[]parquet.FixedLenByteArray{one}, []int16{1, 0}, nil); err != nil {
		t.Fatal(err)
	}
	cw.Close()
	cw, _ = rg.NextColumn()
	if _, err := cw.(*file.ByteArrayColumnChunkWriter).WriteBatch(
		[]parquet.ByteArray{parquet.ByteArray(`{"a":1}`)}, []int16{1, 0}, nil); err != nil {
		t.Fatal(err)
	}
	cw.Close()
	rg.Close()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	df, err := ursus.ScanParquet(path).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if ut, jt := df.Schema().Field(0).Type, df.Schema().Field(1).Type; ut != dtype.Binary || jt != dtype.String {
		t.Fatalf("u is %s and j %s, want Binary and String", ut, jt)
	}
	js, err := df.Column[string]("j")
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := js.Get(0); !ok || v != `{"a":1}` {
		t.Fatalf("j reads %q", v)
	}
	if _, ok := js.Get(1); ok {
		t.Fatal("a null JSON value read as a value")
	}
	if df.Height() != 2 {
		t.Fatalf("%d rows, want 2", df.Height())
	}
}

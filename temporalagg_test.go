package ursus_test

// Step 173: the mean and median of instants and durations, as Polars 1.44.1 answers
// them in the bench's own environment, and Parquet's UUID and JSON columns read as
// Polars reads them.

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

// agg1 is one aggregate of col over the frame, rendered, with its type.
func agg1(t *testing.T, lf *ursus.LazyFrame, e ursus.Expr) (string, dtype.DataType) {
	t.Helper()
	df, err := lf.GroupBy().Agg(e.Alias("r")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	typ := df.Schema().Field(0).Type
	return rendered172(t, df, "r"), typ
}

func TestTemporalMeanAndMedianAnswerAsPolars(t *testing.T) {
	c := ursus.Col
	us := dtype.Datetime(dtype.Micro, "")
	at := func(y int, mo time.Month, d, h, mi, s, usec int) time.Time {
		return time.Date(y, mo, d, h, mi, s, usec*1000, time.UTC)
	}
	instants := func(ts ...time.Time) *ursus.LazyFrame {
		return ursus.Frame(ursus.Values("t", ts)).Select(c("t").Cast(us))
	}
	dates := func(ts []time.Time, ok []bool) *ursus.LazyFrame {
		return ursus.Frame(ursus.ValuesNullable("d", ts, ok)).Select(c("d").Cast(dtype.Date))
	}
	for _, tc := range []struct {
		name string
		lf   *ursus.LazyFrame
		e    ursus.Expr
		want string
		typ  dtype.DataType
	}{
		{"date mean", dates([]time.Time{at(2024, 1, 1, 0, 0, 0, 0), at(2024, 1, 4, 0, 0, 0, 0), {}}, []bool{true, true, false}),
			c("d").Mean(), "2024-01-02T12:00:00.000000Z ", us},
		{"date median, even", dates([]time.Time{at(2024, 1, 1, 0, 0, 0, 0), at(2024, 1, 2, 0, 0, 0, 0)}, []bool{true, true}),
			c("d").Median(), "2024-01-01T12:00:00.000000Z ", us},
		{"date mean before 1970", dates([]time.Time{at(1969, 12, 31, 0, 0, 0, 0), at(1969, 12, 30, 0, 0, 0, 0)}, []bool{true, true}),
			c("d").Mean(), "1969-12-30T12:00:00.000000Z ", us},
		{"datetime mean", instants(at(2024, 1, 1, 0, 0, 0, 0), at(2024, 1, 1, 0, 1, 0, 0), at(2024, 1, 1, 0, 3, 0, 0)),
			c("t").Mean(), "2024-01-01T00:01:20.000000Z ", us},
		{"datetime median", instants(at(2024, 1, 1, 0, 0, 0, 0), at(2024, 1, 1, 0, 1, 0, 0), at(2024, 1, 1, 0, 3, 0, 0)),
			c("t").Median(), "2024-01-01T00:01:00.000000Z ", us},
		{"datetime mean, truncated", instants(at(2024, 1, 1, 0, 0, 0, 1), at(2024, 1, 1, 0, 0, 0, 2)),
			c("t").Mean(), "2024-01-01T00:00:00.000001Z ", us},
		{"datetime median, even, truncated", instants(at(2024, 1, 1, 0, 0, 0, 1), at(2024, 1, 1, 0, 0, 0, 2)),
			c("t").Median(), "2024-01-01T00:00:00.000001Z ", us},
		{"datetime mean before 1970, toward zero", instants(at(1969, 12, 31, 23, 59, 59, 999999), at(1969, 12, 31, 23, 59, 59, 999998)),
			c("t").Mean(), "1969-12-31T23:59:59.999999Z ", us},
		{"datetime median before 1970, toward zero", instants(at(1969, 12, 31, 23, 59, 59, 999999), at(1969, 12, 31, 23, 59, 59, 999998)),
			c("t").Median(), "1969-12-31T23:59:59.999999Z ", us},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, typ := agg1(t, tc.lf, tc.e)
			if typ != tc.typ || got != tc.want {
				t.Fatalf("%q of %s, want %q of %s", got, typ, tc.want, tc.typ)
			}
		})
	}

	// Times and durations keep their types.
	hm := func(h, m int) time.Duration { return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute }
	times := ursus.Frame(ursus.Values("u", []time.Duration{hm(1, 0), hm(3, 0), hm(4, 0)})).
		Select(c("u").Cast(dtype.Time(dtype.Nano)))
	if got, typ := agg1(t, times, c("u").Mean()); typ.ID() != dtype.TypeTime || got != "02:40:00.000000000 " {
		t.Errorf("time mean %q of %s", got, typ)
	}
	if got, typ := agg1(t, times, c("u").Median()); typ.ID() != dtype.TypeTime || got != "03:00:00.000000000 " {
		t.Errorf("time median %q of %s", got, typ)
	}
	durs := ursus.Frame(ursus.ValuesNullable("du", []time.Duration{time.Second, 4 * time.Second, 0}, []bool{true, true, false}))
	if got, typ := agg1(t, durs, c("du").Median()); typ.ID() != dtype.TypeDuration || got != "2.5s " {
		t.Errorf("duration median %q of %s", got, typ)
	}

	// Per group, as any aggregate.
	df, err := ursus.Frame(ursus.Values("g", []string{"a", "a", "b"}),
		ursus.Values("t", []time.Time{at(2024, 1, 1, 0, 0, 0, 0), at(2024, 1, 1, 0, 2, 0, 0), at(2024, 5, 5, 0, 0, 0, 0)})).
		Select(c("g"), c("t").Cast(us)).GroupBy(c("g")).Agg(c("t").Mean().Alias("m")).
		Sort(ursus.Asc(c("g"))).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := rendered172(t, df, "m"); got != "2024-01-01T00:01:00.000000Z 2024-05-05T00:00:00.000000Z " {
		t.Errorf("grouped means %q", got)
	}
}

func TestTemporalVarStdAndQuantileStillRefuse(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(ursus.Values("t", []time.Time{time.Unix(0, 0), time.Unix(60, 0)}))
	for name, e := range map[string]ursus.Expr{
		"var": c("t").Var(1), "std": c("t").Std(1), "quantile": c("t").Quantile(0.9, ursus.InterpLinear),
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

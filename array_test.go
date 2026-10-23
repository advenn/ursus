package ursus_test

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/selector"
	"github.com/advenn/ursus/ursustest"
)

// arrayFrame is Polars' frame of step 341's tests: a List of three, a null element
// in one, a null list, cast to an Array of three; and k.
func arrayFrame() *ursus.LazyFrame {
	c := ursus.Col
	return ursus.Frame(i64s("k", 1, 2, 3, 4), i64s("x", 1, 4, nil, 7), i64s("y", 2, nil, nil, 8), i64s("z", 3, 6, nil, 9)).
		Select(c("k"), ursus.When(c("k").Eq(int64(3))).Then(ursus.Null(ursus.List(ursus.Int64))).
			Otherwise(ursus.ConcatList(c("x"), c("y"), c("z"))).Cast(ursus.Array(ursus.Int64, 3)).Alias("a"))
}

// cells is each row of q's column name as text.
func cells(t *testing.T, q *ursus.LazyFrame, name string) string {
	t.Helper()
	df, err := q.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rows, err := df.ToMaps()
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = fmt.Sprint(r[name])
	}
	return strings.Join(out, " ") + " " + df.Schema().Field(df.Schema().IndexOf(name)).Type.String()
}

// TestArrayType is step 341: the Array type, a fixed-size list, as Polars 1.44.1
// holds it. Every answer is Polars'.
func TestArrayType(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	lf := arrayFrame()
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		col  string
		want string
	}{
		{"a List cast to an Array", lf, "a", "[1 2 3] [4 <nil> 6] <nil> [7 8 9] Array(Int64, 3)"},
		{"and back", lf.Select(c("a").Cast(ursus.List(ursus.Int64))), "a", "[1 2 3] [4 <nil> 6] <nil> [7 8 9] List(Int64)"},
		{"to_list", lf.Select(c("a").Arr().ToList()), "a", "[1 2 3] [4 <nil> 6] <nil> [7 8 9] List(Int64)"},
		{"to_array", lf.Select(c("a").Arr().ToList().List().ToArray(3)), "a", "[1 2 3] [4 <nil> 6] <nil> [7 8 9] Array(Int64, 3)"},
		{"its elements cast", lf.Select(c("a").Cast(ursus.Array(ursus.Float64, 3))), "a",
			"[1 2 3] [4 <nil> 6] <nil> [7 8 9] Array(Float64, 3)"},
		{"of no element", ursus.Frame(i64s("i", 1, 2)).Select(c("i").Implode().List().Head(0).Alias("e")).
			Select(c("e").List().ToArray(0)), "e", "[] Array(Int64, 0)"},
		{"exploded", lf.Explode("a"), "a", "1 2 3 4 <nil> 6 <nil> 7 8 9 Int64"},
		{"an expression exploded", lf.Select(c("a").Explode()), "a", "1 2 3 4 <nil> 6 <nil> 7 8 9 Int64"},
		{"filtered and sorted", lf.Filter(c("k").Ne(int64(1))).Sort(ursus.Desc(c("k"))), "a",
			"[7 8 9] <nil> [4 <nil> 6] Array(Int64, 3)"},
		{"stacked", ursus.Concat([]*ursus.LazyFrame{lf.Head(2), lf.Tail(1)}), "a",
			"[1 2 3] [4 <nil> 6] [7 8 9] Array(Int64, 3)"},
		{"joined", lf.Join(ursus.Frame(i64s("k", 4, 1)), ursus.JoinOn(c("k"))).Sort(ursus.Asc(c("k"))), "a",
			"[1 2 3] [7 8 9] Array(Int64, 3)"},
		{"selected by type", lf.Select(selector.Array().Expr()), "a", "[1 2 3] [4 <nil> 6] <nil> [7 8 9] Array(Int64, 3)"},
		// A key since step 344, as Polars groups by one.
		{"a key", lf.GroupBy(c("a")).MaintainOrder().Agg(c("k").Sum()), "a", "[1 2 3] [4 <nil> 6] <nil> [7 8 9] Array(Int64, 3)"},
	} {
		if got := cells(t, tc.q, tc.col); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		// Polars: "not all elements have the specified width 2".
		{"a List of another width", lf.Select(c("a").Arr().ToList().List().ToArray(2)), "not all elements have the specified width 2"},
		{"a List of another width, cast", lf.Select(c("a").Arr().ToList().Cast(ursus.Array(ursus.Int64, 2))),
			"not all elements have the specified width 2"},
		// Polars: "cannot cast Array to a different width".
		{"an Array of another size", lf.Select(c("a").Cast(ursus.Array(ursus.Int64, 2))), "cannot cast"},
		{"an Array to text", lf.Select(c("a").Cast(ursus.String)), "cannot cast"},
		{"a List as an Array", lf.Select(c("a").Arr().ToList().Arr().ToList()), "requires an Array"},
		{"an Array sorted", lf.Sort(ursus.Asc(c("a"))), "sort"},
	} {
		if _, err := tc.q.Collect(ctx); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error naming %q", tc.name, err, tc.want)
		}
	}
	// Past a memory limit a sort carries an Array through its spill.
	big := ursus.Frame(ursus.Values("k", seqInts(5000))).Select(c("k"),
		ursus.ConcatList(c("k"), c("k").Mul(int64(2))).List().ToArray(2).Alias("a"))
	q := big.Sort(ursus.Desc(c("k")))
	got, stats, err := spillCollect(t, q, 16<<10, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Spills == 0 {
		t.Error("a sort of an Array under 16 KiB did not spill")
	}
	ursustest.AssertFrameEqual(t, got, collect(t, q))
}

// seqInts is 0 to n-1.
func seqInts(n int) []int64 {
	v := make([]int64, n)
	for i := range v {
		v[i] = int64(i)
	}
	return v
}

type arrayRow struct {
	K int64
	A [3]int64
	P *[2]float64
}

// TestArrayRows is step 341's Go side: a Go array is an Array of its size, a pointer
// to one a nullable one, read back as itself, as a slice, and as a map's []any.
func TestArrayRows(t *testing.T) {
	ctx := t.Context()
	rows := []arrayRow{{1, [3]int64{1, 2, 3}, &[2]float64{0.5, 1}}, {2, [3]int64{4, 5, 6}, nil}}
	df, err := ursus.FromRows(rows).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := df.Schema().String(); !strings.Contains(got, "A: Array(Int64, 3)") || !strings.Contains(got, "P: Array(Float64, 2)") {
		t.Errorf("FromRows' schema: %s", got)
	}
	back, err := df.Rows[arrayRow]()
	if err != nil || len(back) != 2 || back[0].A != rows[0].A || *back[0].P != *rows[0].P || back[1].P != nil {
		t.Errorf("Rows: %+v %v", back, err)
	}
	type asSlices struct {
		A []int64
		P []float64
	}
	sl, err := df.Rows[asSlices]()
	if err != nil || fmt.Sprint(sl) != "[{[1 2 3] [0.5 1]} {[4 5 6] []}]" {
		t.Errorf("Rows as slices: %v %v", sl, err)
	}
	m, err := df.ToMaps()
	if err != nil || fmt.Sprint(m[0]["A"], m[1]["P"]) != "[1 2 3] <nil>" {
		t.Errorf("ToMaps: %v %v", m, err)
	}
	// A nil pointer's row is null, and holds no element: exploded, it is one null.
	if got := cells(t, ursus.FromRows(rows).Explode("P"), "P"); got != "0.5 1 <nil> Float64" {
		t.Errorf("a null Go array exploded: %s", got)
	}
	type wrong struct{ A [2]int64 }
	if _, err := df.Rows[wrong](); err == nil || !strings.Contains(err.Error(), "its own size") {
		t.Errorf("a Go array of another size: %v", err)
	}
}

// TestArrayIO is step 341's files: an Array is an Arrow FixedSizeList, in IPC and in
// Parquet's footer, as Polars writes it, and a JSON list.
func TestArrayIO(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	// Polars 1.44.1's write_ipc and write_parquet of one frame: an Array of three
	// integers with a null element and a null row, and an Array of two floats.
	const ints = "[1 2 3] [4 <nil> 6] <nil> [7 8 9] Array(Int64, 3)"
	const floats = "[0.5 1] <nil> [2 3] [4 -0] Array(Float64, 2)"
	for _, lf := range []*ursus.LazyFrame{
		ursus.ScanIPC(filepath.Join("testdata", "ipc", "polars_array.arrow")),
		ursus.ScanParquet(filepath.Join("testdata", "parquet", "polars_array.parquet")),
	} {
		if got := cells(t, lf, "a"); got != ints {
			t.Errorf("Polars' a: %s", got)
		}
		if got := cells(t, lf, "f"); got != floats {
			t.Errorf("Polars' f: %s", got)
		}
	}
	// A footer that calls a list of two elements an Array of three is refused, where
	// Polars panics.
	_, err := ursus.ScanParquet(filepath.Join("testdata", "parquet", "pyarrow_bad_array.parquet")).Collect(ctx)
	if err == nil || !strings.Contains(err.Error(), "a row of 2 elements") {
		t.Errorf("a false Arrow schema: %v", err)
	}

	lf := arrayFrame()
	want := collect(t, lf)
	path := filepath.Join(t.TempDir(), "a.arrow")
	if err := lf.SinkIPC(ctx, path); err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, collect(t, ursus.ScanIPC(path)), want)
	var pq bytes.Buffer
	if err := lf.WriteParquet(ctx, &pq); err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, collect(t, ursus.ScanParquetBytes(pq.Bytes(), "a.parquet")), want)
	// Without an Array the footer is as it was: a List stays one.
	var plain bytes.Buffer
	if err := lf.Select(c("a").Arr().ToList()).WriteParquet(ctx, &plain); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(plain.Bytes(), []byte("ARROW:schema")) || !bytes.Contains(pq.Bytes(), []byte("ARROW:schema")) {
		t.Error("the Arrow schema is in a footer without an Array, or missing from one with")
	}

	var js bytes.Buffer
	if err := lf.WriteNDJSON(ctx, &js); err != nil {
		t.Fatal(err)
	}
	if got := js.String(); got != "{\"k\":1,\"a\":[1,2,3]}\n{\"k\":2,\"a\":[4,null,6]}\n{\"k\":3,\"a\":null}\n{\"k\":4,\"a\":[7,8,9]}\n" {
		t.Errorf("NDJSON: %q", got)
	}
	sch := ursus.MustSchema(ursus.Field{Name: "k", Type: ursus.Int64, Nullable: true},
		ursus.Field{Name: "a", Type: ursus.Array(ursus.Int64, 3), Nullable: true})
	ursustest.AssertFrameEqual(t, collect(t, ursus.ScanNDJSONBytes(js.Bytes(), "a.ndjson", ursus.WithNDJSONSchema(sch))), want)
	_, err = ursus.ScanNDJSONBytes([]byte("{\"k\":1,\"a\":[1,2]}\n"), "a.ndjson", ursus.WithNDJSONSchema(sch)).Collect(ctx)
	if err == nil || !strings.Contains(err.Error(), "width 3") {
		t.Errorf("NDJSON of another width: %v", err)
	}
	var csv strings.Builder
	if err := lf.WriteCSV(ctx, &csv); err == nil || !strings.Contains(err.Error(), "Array") {
		t.Errorf("CSV: %v", err)
	}
}

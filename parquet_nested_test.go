package ursus_test

// Every List and Struct element type round-trips through Parquet, generated.
//
// Each element type the flat writer writes, as a list column of every shape — a null
// list, an empty one, one value, one null, and several with a null among them — at
// row-group sizes from one row to all of them, so a list is cut at every place a
// boundary can fall. And a struct with a field of every such type, holding a null
// struct and a null field. Written, read back, equal.
//
// Int128 has no Parquet type of its own: it is written as DECIMAL(38, 0) and reads
// back as Decimal(38, 0), a documented asymmetry, so its values are compared after a
// cast back.

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
)

// knownNestedRoundTripDefects names each type and route that does not round-trip.
var knownNestedRoundTripDefects = map[string]string{}

var nestedElementTypes = []dtype.DataType{
	dtype.Int8, dtype.Int16, dtype.Int32, dtype.Int64,
	dtype.Uint8, dtype.Uint16, dtype.Uint32, dtype.Uint64, dtype.Int128,
	dtype.Float32, dtype.Float64, dtype.Bool, dtype.String, dtype.Binary,
	dtype.Date, dtype.Time(dtype.Milli), dtype.Time(dtype.Micro), dtype.Time(dtype.Nano),
	dtype.Datetime(dtype.Milli, ""), dtype.Datetime(dtype.Micro, "UTC"), dtype.Datetime(dtype.Nano, ""),
	dtype.Decimal(10, 2), dtype.Decimal(38, 10),
}

// typedColumn is n values 1..n of type dt, null where valid is false, built by a cast
// from Int64 so every type gets values from one source.
func typedColumn(t *testing.T, name string, dt dtype.DataType, valid []bool) *ursus.Column {
	t.Helper()
	vals := make([]int64, len(valid))
	for i := range vals {
		vals[i] = int64(i + 1)
	}
	e := ursus.Col(name)
	if dt.ID() == dtype.TypeBinary {
		e = e.Cast(ursus.String)
	}
	df, err := ursus.Frame(ursus.ValuesNullable(name, vals, valid)).Select(e.Cast(dt)).Collect(t.Context())
	if err != nil {
		t.Fatalf("building %s: %v", dt, err)
	}
	c, _ := df.Batch().ByName(name)
	return c
}

// roundTripWrong writes lf with the options, reads it back, and says how it differs.
// A column of Int128 at any depth reads back as Decimal(38, 0), and is cast back.
func roundTripWrong(t *testing.T, lf *ursus.LazyFrame, back ursus.Expr, opts ...ursus.ParquetSinkOption) string {
	t.Helper()
	var b bytes.Buffer
	if err := lf.WriteParquet(t.Context(), &b, opts...); err != nil {
		return "write: " + err.Error()
	}
	read := ursus.ScanParquetBytes(b.Bytes(), "x.parquet").Select(back)
	got, err := read.Collect(t.Context())
	if err != nil {
		return "read: " + err.Error()
	}
	want, err := lf.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return framesDiffer(t, got, want)
}

func TestParquetNestedRoundTrips(t *testing.T) {
	c := ursus.Col
	// [null list], [], [v], [null], [v, null, v, v]: offsets over a child of seven.
	offs := []int32{0, 0, 0, 1, 2, 6}
	listValid := validity(false, true, true, true, true)
	childValid := []bool{true, false, true, false, true, true}
	var routes int
	seen := map[string]bool{}
	check := func(key, wrong string) {
		routes++
		seen[key] = true
		why, known := knownNestedRoundTripDefects[key]
		switch {
		case known && wrong == "":
			t.Errorf("%s round-trips now; delete it from knownNestedRoundTripDefects (%s)", key, why)
		case !known && wrong != "":
			t.Errorf("%s: %s", key, wrong)
		}
	}

	for _, dt := range nestedElementTypes {
		child := typedColumn(t, "item", dt, childValid)
		list := data.NewList("l", offs, child, listValid)
		back := c("l").Cast(dtype.List(dt))
		for _, rg := range []int{1, 2, 3, 1000} {
			check(fmt.Sprintf("List(%s) in row groups of %d", dt, rg),
				roundTripWrong(t, ursus.Frame(list), back, ursus.WithRowGroupRows(rg)))
		}
	}

	// A struct with a field of every element type but Int128, whose Decimal(38, 0)
	// read-back a struct cannot cast away, over four rows: a null struct, a row with
	// a null field, and values.
	var fields []*ursus.Column
	for i, dt := range nestedElementTypes {
		if dt.ID() == dtype.TypeInt128 {
			continue
		}
		fields = append(fields, typedColumn(t, fmt.Sprintf("f%d", i), dt, []bool{true, false, true, true}))
	}
	st := data.NewStruct("s", fields, validity(false, true, true, true))
	for _, rg := range []int{1, 3, 1000} {
		check(fmt.Sprintf("Struct of every type in row groups of %d", rg),
			roundTripWrong(t, ursus.Frame(st), c("s"), ursus.WithRowGroupRows(rg)))
	}

	for key := range knownNestedRoundTripDefects {
		if !seen[key] {
			t.Errorf("knownNestedRoundTripDefects names %q, which is not a route", key)
		}
	}
	if routes != len(nestedElementTypes)*4+3 {
		t.Fatalf("%d routes ran — the sweep has gone vacuous", routes)
	}
}

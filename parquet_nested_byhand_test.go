package ursus_test

// Nested columns through Parquet, by hand: v0.3-scope.md §2.2 and audit.md's I17.
//
// ursus reads a List of primitives and a Struct of primitive fields from Parquet,
// and could write neither back. Each case writes a frame and reads it back, and the
// two must be equal; the I17 cases read lists a pyarrow file holds, whose element
// types the reader refused.

import (
	"bytes"
	"math"
	"os"
	"slices"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// knownNestedParquetDefects names each case that answers wrongly today, with what it
// answers.
var knownNestedParquetDefects = map[string]string{
	"List(Int64)":                    "refused at write: no nested column is written",
	"List(String)":                   "refused at write: no nested column is written",
	"List(Float64) with NaN":         "refused at write: no nested column is written",
	"Struct(a Int64, b String)":      "refused at write: no nested column is written",
	"List(Bool)":                     "refused at write: no nested column is written",
	"List(Uint8)":                    "refused at write: no nested column is written",
	"List(Uint64)":                   "refused at write: no nested column is written",
	"List(Decimal(10, 2))":           "refused at write: no nested column is written",
	"List(Time(ms))":                 "refused at write: no nested column is written",
	"lists across row groups of two": "refused at write: no nested column is written",
	"pyarrow List(Bool)":             "refused at read: the list element type has no accumulator (I17)",
	"pyarrow List(Uint8)":            "refused at read: the list element type has no accumulator (I17)",
	"pyarrow List(Uint16)":           "refused at read: the list element type has no accumulator (I17)",
	"pyarrow List(Uint32)":           "refused at read: the list element type has no accumulator (I17)",
	"pyarrow List(Uint64)":           "refused at read: the list element type has no accumulator (I17)",
	"pyarrow List(Decimal(10, 2))":   "refused at read: the list element type has no accumulator (I17)",
	"pyarrow List(Time(ms))":         "refused at read: the list element type has no accumulator (I17)",
}

// validity is a bitmap from bools.
func validity(v ...bool) bitmap.View {
	b := bitmap.NewBuilder(len(v))
	for _, x := range v {
		b.Append(x)
	}
	return b.Finish()
}

// fourLists is the shape every list case uses: [x, y], [], null, [z].
func fourLists(name string, child *ursus.Column) *ursus.Column {
	return data.NewList(name, []int32{0, 2, 2, 2, 3}, child, validity(true, true, false, true))
}

// parquetRoundTrip writes lf to Parquet and reads it back.
func parquetRoundTrip(t *testing.T, lf *ursus.LazyFrame, opts ...ursus.ParquetSinkOption) (*ursus.DataFrame, error) {
	t.Helper()
	var b bytes.Buffer
	if err := lf.WriteParquet(t.Context(), &b, opts...); err != nil {
		return nil, err
	}
	return ursus.ScanParquetBytes(b.Bytes(), "x.parquet").Collect(t.Context())
}

func TestNestedParquetByHand(t *testing.T) {
	c := ursus.Col
	roundTrips := func(lf *ursus.LazyFrame, opts ...ursus.ParquetSinkOption) func(*testing.T) string {
		return func(t *testing.T) string {
			got, err := parquetRoundTrip(t, lf, opts...)
			if err != nil {
				return err.Error()
			}
			want, err := lf.Collect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			return framesDiffer(t, got, want)
		}
	}
	fixture, err := os.ReadFile("testdata/parquet/pyarrow_lists.parquet")
	if err != nil {
		t.Fatal(err)
	}
	// readsAs reads one column of the pyarrow fixture and requires want.
	readsAs := func(name string, want *ursus.Column) func(*testing.T) string {
		return func(t *testing.T) string {
			got, err := ursus.ScanParquetBytes(fixture, "pyarrow_lists.parquet").Select(c(name)).Collect(t.Context())
			if err != nil {
				return err.Error()
			}
			w, err := ursus.Frame(want).Collect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			return framesDiffer(t, got, w)
		}
	}
	refused := func(lf *ursus.LazyFrame, words ...string) func(*testing.T) string {
		return func(t *testing.T) string {
			var b bytes.Buffer
			err := lf.WriteParquet(t.Context(), &b)
			return refusal(nil, err, ursus.ErrUnsupported, words...)
		}
	}
	i128s := func(vals ...string) []i128.Int128 {
		out := make([]i128.Int128, len(vals))
		for i, s := range vals {
			out[i], _ = i128.Parse(s)
		}
		return out
	}
	three := validity(true, false, true) // [x, null] [] null [z]
	ms := func(h, m, s, milli int64) int64 { return ((h*60+m)*60+s)*1000 + milli }

	cases := []ioCase{
		// --- written by ursus, read back ---
		{"List(Int64)", roundTrips(ursus.Frame(fourLists("l",
			data.NewFixed("item", dtype.Int64, []int64{1, 0, 3}, three))))},
		{"List(String)", roundTrips(ursus.Frame(fourLists("l",
			data.NewString("item", []string{"", "x", "z"}, three))))},
		{"List(Float64) with NaN", roundTrips(ursus.Frame(fourLists("l",
			data.NewFixed("item", dtype.Float64, []float64{math.NaN(), 0, -1.5}, three))))},
		{"Struct(a Int64, b String)", roundTrips(ursus.Frame(data.NewStruct("s", []*ursus.Column{
			data.NewFixed("a", dtype.Int64, []int64{1, 0, 3}, validity(true, false, true)),
			data.NewString("b", []string{"x", "y", ""}, validity(true, true, false)),
		}, validity(true, true, false))))},
		{"List(Bool)", roundTrips(ursus.Frame(fourLists("l",
			data.NewBool("item", validity(true, false, false), three))))},
		{"List(Uint8)", roundTrips(ursus.Frame(fourLists("l",
			data.NewFixed("item", dtype.Uint8, []uint8{1, 0, 255}, three))))},
		{"List(Uint64)", roundTrips(ursus.Frame(fourLists("l",
			data.NewFixed("item", dtype.Uint64, []uint64{1, 0, math.MaxUint64}, three))))},
		{"List(Decimal(10, 2))", roundTrips(ursus.Frame(fourLists("l",
			data.NewFixed("item", dtype.Decimal(10, 2), i128s("125", "0", "-350"), three))))},
		{"List(Time(ms))", roundTrips(ursus.Frame(fourLists("l",
			data.NewFixed("item", dtype.Time(dtype.Milli), []int64{ms(1, 2, 3, 4), 0, ms(23, 59, 59, 0)}, three))))},
		{"lists across row groups of two", roundTrips(ursus.Frame(
			data.NewList("l", []int32{0, 2, 2, 2, 3, 6}, data.NewFixed("item", dtype.Int64,
				[]int64{1, 2, 3, 4, 5, 6}, bitmap.AllSet(6)), validity(true, true, false, true, true))),
			ursus.WithRowGroupRows(2))},

		// --- written by pyarrow: audit.md's I17 element types ---
		{"pyarrow List(Bool)", readsAs("lb", fourLists("lb",
			data.NewBool("element", validity(true, false, false), validity(true, false, true))))},
		{"pyarrow List(Uint8)", readsAs("lu8", fourLists("lu8",
			data.NewFixed("element", dtype.Uint8, []uint8{1, 255, 0}, validity(true, true, false))))},
		{"pyarrow List(Uint16)", readsAs("lu16", fourLists("lu16",
			data.NewFixed("element", dtype.Uint16, []uint16{1, 65535, 0}, validity(true, true, false))))},
		{"pyarrow List(Uint32)", readsAs("lu32", fourLists("lu32",
			data.NewFixed("element", dtype.Uint32, []uint32{1, math.MaxUint32, 0}, validity(true, true, false))))},
		{"pyarrow List(Uint64)", readsAs("lu64", fourLists("lu64",
			data.NewFixed("element", dtype.Uint64, []uint64{0, math.MaxUint64, 0}, validity(true, true, false))))},
		{"pyarrow List(Decimal(10, 2))", readsAs("ldec", fourLists("ldec",
			data.NewFixed("element", dtype.Decimal(10, 2), i128s("125", "0", "-350"), validity(true, false, true))))},
		{"pyarrow List(Time(ms))", readsAs("lt", fourLists("lt",
			data.NewFixed("element", dtype.Time(dtype.Milli), []int64{ms(1, 2, 3, 4), 0, ms(23, 59, 59, 0)},
				validity(true, false, true))))},

		// --- deeper nesting stays refused, by name ---
		{"control: List(List(Int64)) is refused", refused(ursus.Frame(data.NewList("l", []int32{0, 1},
			data.NewList("item", []int32{0, 1}, data.NewFixed("item", dtype.Int64, []int64{1}, bitmap.AllSet(1)),
				bitmap.AllSet(1)), bitmap.AllSet(1))), "List(List")},
		{"control: List(Struct) is refused", refused(ursus.Frame(data.NewList("l", []int32{0, 1},
			data.NewStruct("item", []*ursus.Column{data.NewFixed("a", dtype.Int64, []int64{1}, bitmap.AllSet(1))},
				bitmap.AllSet(1)), bitmap.AllSet(1))), "List(Struct")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wrong := tc.run(t)
			why, known := knownNestedParquetDefects[tc.name]
			switch {
			case known && wrong == "":
				t.Errorf("answers correctly now; delete it from knownNestedParquetDefects (%s)", why)
			case known:
				t.Logf("known defect (%s): %s", why, wrong)
			case wrong != "":
				t.Error(wrong)
			}
		})
	}
	for name := range knownNestedParquetDefects {
		if !slices.ContainsFunc(cases, func(c ioCase) bool { return c.name == name }) {
			t.Errorf("knownNestedParquetDefects names %q, which is not a case", name)
		}
	}
}

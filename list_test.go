package ursus_test

// A List column read from Parquet has to survive the ordinary query path — Take,
// Concat, rendering — or it is readable but not usable.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/schema"

	"github.com/advenn/ursus"
)

// writeListFile builds `id: int64` beside `tags: List(Int64)`, with the def/rep
// levels written by hand — ursus cannot write nested Parquet, so the fixture has
// to come from the layer below.
//
//	def 3  element present     def 1  present but EMPTY list
//	def 2  null element        def 0  null list
//	rep 0  starts a row        rep 1  continues the open row
func writeListFile(t *testing.T, ids []int64, tags [][]int64, present []bool) string {
	t.Helper()
	opt, req := parquet.Repetitions.Optional, parquet.Repetitions.Required

	idNode, err := schema.NewPrimitiveNodeLogical("id", req,
		schema.NewIntLogicalType(64, true), parquet.Types.Int64, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	el, err := schema.NewPrimitiveNodeLogical("element", opt,
		schema.NewIntLogicalType(64, true), parquet.Types.Int64, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	lst, err := schema.ListOf(el, opt, -1)
	if err != nil {
		t.Fatal(err)
	}
	tagsNode, err := schema.NewGroupNodeLogical("tags", opt,
		schema.FieldList{lst.Field(0)}, schema.NewListLogicalType(), -1)
	if err != nil {
		t.Fatal(err)
	}
	root, err := schema.NewGroupNode("schema", req,
		schema.FieldList{idNode, tagsNode}, -1)
	if err != nil {
		t.Fatal(err)
	}

	var vals []int64
	var defs, reps []int16
	for i := range tags {
		switch {
		case !present[i]:
			defs, reps = append(defs, 0), append(reps, 0)
		case len(tags[i]) == 0:
			defs, reps = append(defs, 1), append(reps, 0)
		default:
			for j, v := range tags[i] {
				vals = append(vals, v)
				defs = append(defs, 3)
				if j == 0 {
					reps = append(reps, 0)
				} else {
					reps = append(reps, 1)
				}
			}
		}
	}

	path := filepath.Join(t.TempDir(), "lists.parquet")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := file.NewParquetWriter(f, root)
	rg := w.AppendRowGroup()

	cw, err := rg.NextColumn()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cw.(*file.Int64ColumnChunkWriter).WriteBatch(ids, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := cw.Close(); err != nil {
		t.Fatal(err)
	}

	cw, err = rg.NextColumn()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cw.(*file.Int64ColumnChunkWriter).WriteBatch(vals, defs, reps); err != nil {
		t.Fatal(err)
	}
	// w.Close() closes the file too.
	for _, c := range []interface{ Close() error }{cw, rg, w} {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func listFixture(t *testing.T) string {
	t.Helper()
	return writeListFile(t,
		[]int64{1, 2, 3, 4},
		[][]int64{{10, 11}, {}, nil, {12}},
		[]bool{true, true, false, true}, // row 3's list is NULL, row 2's is EMPTY
	)
}

// TestListReadsThroughAQuery is the whole point: scan, project alongside a flat
// column, collect, and see it.
func TestListReadsThroughAQuery(t *testing.T) {
	df, err := ursus.ScanParquet(listFixture(t)).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("a file with a readable list column must collect: %v", err)
	}
	if df.Height() != 4 || df.Width() != 2 {
		t.Fatalf("got %dx%d, want 4x2\n%s", df.Height(), df.Width(), df)
	}

	// Rendering is the cheapest place to see empty-versus-null go wrong, and it is
	// the only thing a user looks at first.
	got := df.String()
	for _, want := range []string{"[10, 11]", "[]", "null", "[12]"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered frame is missing %q:\n%s", want, got)
		}
	}
}

// TestListSurvivesTakeAndConcat drives the two kernels a list has to pass
// through. Both fail SILENTLY when wrong — a filtered row keeps its old list, or
// a later batch reads an earlier batch's elements — so the check is on values.
func TestListSurvivesTakeAndConcat(t *testing.T) {
	path := listFixture(t)

	// A NON-IDENTITY selection. Filtering to everything would leave the offsets
	// unchanged and pass even if takeList ignored the selection entirely.
	for _, size := range []int{1, 2, 3, 8192} {
		df, err := ursus.ScanParquet(path).
			Filter(ursus.Col("id").Gt(int64(2))).
			Collect(t.Context(), ursus.WithBatchSize(size), ursus.WithVerify())
		if err != nil {
			t.Fatalf("batch size %d: %v", size, err)
		}
		if df.Height() != 2 {
			t.Fatalf("batch size %d: got %d rows, want 2\n%s", size, df.Height(), df)
		}
		out := df.String()
		// id 3 has a NULL list, id 4 has [12]. If takeList ignored the selection
		// these would still read [10, 11] and [].
		if !strings.Contains(out, "[12]") {
			t.Errorf("batch size %d: row id=4 lost its list\n%s", size, out)
		}
		if strings.Contains(out, "[10, 11]") {
			t.Errorf("batch size %d: a filtered-out row's list survived\n%s", size, out)
		}
	}
}

// TestListConcatShiftsOffsets is the Concat case the filter test cannot reach.
//
// With a filter, the surviving rows happened to start with a NULL list, whose
// child is empty — so the offset shift was zero and removing it changed nothing.
// Reading every row at batch size 1 puts a two-element list in the first part, so
// every later part's offsets must be shifted past it.
//
// Without the shift row 4 renders [10] instead of [12]: it reads the FIRST
// batch's elements. Plausible data, no error.
func TestListConcatShiftsOffsets(t *testing.T) {
	path := writeListFile(t,
		[]int64{1, 2, 3, 4},
		[][]int64{{10, 11}, {20}, {}, {12}},
		[]bool{true, true, true, true},
	)
	for _, size := range []int{1, 2, 3, 8192} {
		df, err := ursus.ScanParquet(path).
			Collect(t.Context(), ursus.WithBatchSize(size), ursus.WithVerify())
		if err != nil {
			t.Fatalf("batch size %d: %v", size, err)
		}
		out := df.String()
		for _, want := range []string{"[10, 11]", "[20]", "[]", "[12]"} {
			if !strings.Contains(out, want) {
				t.Errorf("batch size %d: missing %q — a later batch is reading an "+
					"earlier batch's elements\n%s", size, want, out)
			}
		}
	}
}

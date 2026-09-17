package ursus_test

// A List column crossing a batch boundary.
//
// Every List batch-size test before this file read its lists with ScanParquet, and the
// Parquet reader builds a FRESH List column per batch: offsets starting at zero, a
// child holding exactly that batch's elements. None of them ever produced a SLICED
// List, which is the only shape that exercises the contract Column.Slice states — that
// a derived list keeps its offsets absolute and its child whole.
//
// These go through the paths that do slice: DataFrame.Lazy, whose memory source slices
// any stored batch larger than the batch size, and Tail, which slices the batch it
// keeps.

import (
	"testing"

	"github.com/advenn/ursus"
)

// slicedListFrame has distinct values in every row, a non-empty row BEFORE any slice
// point, an empty row and a null row — so an element counted twice or absorbed into
// the wrong row changes a visible number rather than disappearing into a zero.
//
//	id  tags
//	1   [1, 2]
//	2   [10, 20, 30]
//	3   []
//	4   null
//	5   [100]
//	6   [1000, 2000]
func slicedListFrame(t *testing.T) *ursus.DataFrame {
	t.Helper()
	path := writeListFile(t,
		[]int64{1, 2, 3, 4, 5, 6},
		[][]int64{{1, 2}, {10, 20, 30}, {}, nil, {100}, {1000, 2000}},
		[]bool{true, true, true, false, true, true},
	)
	df, err := ursus.ScanParquet(path).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return df
}

var listBatchSizes = []int{1, 2, 3, 4, 5, 8192}

// TestListFrameSurvivesReLazy is the concatList path.
//
// df.Lazy() hands the frame to a memory source that slices it into batch-size pieces,
// and Collect concatenates them again. With a batch size smaller than the frame every
// piece after the first is a sliced List — and the result must be the frame it
// started as.
func TestListFrameSurvivesReLazy(t *testing.T) {
	df := slicedListFrame(t)
	want := df.String()

	for _, size := range listBatchSizes {
		got, err := df.Lazy().Collect(t.Context(), ursus.WithBatchSize(size))
		if err != nil {
			t.Fatalf("batch size %d: %v", size, err)
		}
		if got.String() != want {
			t.Errorf("batch size %d changed the frame:\n got:\n%s\nwant:\n%s",
				size, got.String(), want)
		}
	}
}

// TestListReductionsAreBatchSizeInvariant is the listReduce path.
//
// The reference is computed straight from Parquet, which never slices. Every
// reduction over a re-lazied frame must match it at every batch size.
func TestListReductionsAreBatchSizeInvariant(t *testing.T) {
	df := slicedListFrame(t)
	reductions := func(lf *ursus.LazyFrame) *ursus.LazyFrame {
		return lf.Select(
			ursus.Col("id"),
			ursus.Col("tags").List().Len().Alias("len"),
			ursus.Col("tags").List().Sum().Alias("sum"),
			ursus.Col("tags").List().Min().Alias("min"),
			ursus.Col("tags").List().Max().Alias("max"),
			ursus.Col("tags").List().Mean().Alias("mean"),
		)
	}

	ref, err := reductions(df.Lazy()).Collect(t.Context(), ursus.WithBatchSize(8192))
	if err != nil {
		t.Fatal(err)
	}
	want := ref.String()

	for _, size := range listBatchSizes {
		got, err := reductions(df.Lazy()).Collect(t.Context(), ursus.WithBatchSize(size))
		if err != nil {
			t.Fatalf("batch size %d: %v", size, err)
		}
		if got.String() != want {
			t.Errorf("batch size %d:\n got:\n%s\nwant:\n%s", size, got.String(), want)
		}
	}
}

// TestListReductionAfterTail is the Batch.Slice path with no memory source involved.
// Tail keeps the LAST rows of a batch by slicing it, so the kept rows' offsets start
// well past zero.
func TestListReductionAfterTail(t *testing.T) {
	df := slicedListFrame(t)

	got, err := df.Lazy().Tail(2).
		Select(ursus.Col("tags").List().Sum().Alias("sum")).
		Collect(t.Context(), ursus.WithBatchSize(8192))
	if err != nil {
		t.Fatal(err)
	}
	// rows 5 and 6: [100] and [1000, 2000].
	ref, err := ursus.Frame(ursus.Values("x", []int64{100, 3000})).
		Select(ursus.Col("x").Cast(ursus.Int128).Alias("sum")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != ref.String() {
		t.Errorf("sum over Tail(2):\n got:\n%s\nwant:\n%s", got.String(), ref.String())
	}
}

// TestSplitListSurvivesReLazy reaches the same path with no file at all: Split builds
// the List column, and the frame then goes back through Lazy at a small batch size.
func TestSplitListSurvivesReLazy(t *testing.T) {
	df, err := ursus.Frame(
		ursus.Values("s", []string{"a,b", "c,d,e", "f", "g,h"}),
	).Select(ursus.Col("s").Str().Split(",").Alias("parts")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := df.String()

	for _, size := range listBatchSizes {
		got, err := df.Lazy().Collect(t.Context(), ursus.WithBatchSize(size))
		if err != nil {
			t.Fatalf("batch size %d: %v", size, err)
		}
		if got.String() != want {
			t.Errorf("batch size %d:\n got:\n%s\nwant:\n%s", size, got.String(), want)
		}
	}
}

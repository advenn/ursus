package memsrc

import (
	"context"
	"io"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/source"
)

// memsrc had no tests at all before step 37, and the property it lacked is the one
// that made WithThreads a no-op on in-memory data for six steps: it returned the
// batches it was built with and ignored the requested batch size, so a frame built
// from one set of slices was ONE batch of however many rows, and a pipeline over it
// had one unit of work to distribute.

func rowsBatch(t *testing.T, n int) (*dtype.Schema, *data.Batch) {
	t.Helper()
	s := dtype.MustSchema(dtype.NotNull("v", dtype.Int64))
	vals := make([]int64, n)
	for i := range n {
		vals[i] = int64(i)
	}
	b, err := data.NewBatch(s, []*data.Column{
		data.NewFixed("v", dtype.Int64, vals, bitmap.AllSet(n)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, b
}

// sizes reads a source to EOF and returns the row count of each batch, plus every
// value in order.
func sizes(t *testing.T, src *Source, spec source.ScanSpec) ([]int, []int64) {
	t.Helper()
	r, err := src.Open(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	var got []int
	var vals []int64
	for {
		b, err := r.Next(context.Background())
		if err == io.EOF {
			return got, vals
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, b.Rows())
		v, err := data.Values[int64](b.Column(0))
		if err != nil {
			t.Fatal(err)
		}
		vals = append(vals, v...)
	}
}

// TestSourceSplitsToBatchSize is the step, and the tail is the interesting half.
func TestSourceSplitsToBatchSize(t *testing.T) {
	for _, c := range []struct {
		name string
		rows int
		size int
		want []int
	}{
		{"a short tail", 1000, 128, []int{128, 128, 128, 128, 128, 128, 128, 104}},
		{"an exact multiple leaves no empty batch", 1024, 128, []int{128, 128, 128, 128, 128, 128, 128, 128}},
		{"a batch size larger than the frame", 100, 8192, []int{100}},
		{"a batch size of one", 3, 1, []int{1, 1, 1}},
		{"an empty frame yields nothing", 0, 128, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, b := rowsBatch(t, c.rows)
			src, err := New(s, b)
			if err != nil {
				t.Fatal(err)
			}
			got, vals := sizes(t, src, source.ScanSpec{BatchSize: c.size})

			if len(got) != len(c.want) {
				t.Fatalf("%d batches %v, want %d %v", len(got), got, len(c.want), c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("batch sizes %v, want %v", got, c.want)
				}
			}
			// The values, not just the counts: a split that loses or repeats a row
			// gets the shape right and the data wrong.
			if len(vals) != c.rows {
				t.Fatalf("%d rows total, want %d", len(vals), c.rows)
			}
			for i, v := range vals {
				if v != int64(i) {
					t.Fatalf("row %d is %d — the split lost or reordered rows", i, v)
				}
			}
		})
	}
}

// TestSourceDefaultsWhenUnasked: a spec with no batch size gets the same default the
// Parquet reader uses, rather than the whole frame.
func TestSourceDefaultsWhenUnasked(t *testing.T) {
	s, b := rowsBatch(t, defaultBatchSize+5)
	src, err := New(s, b)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := sizes(t, src, source.ScanSpec{})
	want := []int{defaultBatchSize, 5}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("batch sizes %v, want %v", got, want)
	}
}

// TestSourceKeepsSmallerBatches: splitting is the fix, merging is not. A source
// built from batches already smaller than the requested size keeps its own
// boundaries — coalescing them would be a different policy with its own argument.
func TestSourceKeepsSmallerBatches(t *testing.T) {
	s, b1 := rowsBatch(t, 10)
	_, b2 := rowsBatch(t, 7)
	src, err := New(s, b1, b2)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := sizes(t, src, source.ScanSpec{BatchSize: 128})
	if len(got) != 2 || got[0] != 10 || got[1] != 7 {
		t.Errorf("batch sizes %v, want [10 7]", got)
	}
}

// TestSourceSkipsEmptyBatches: a zero-row batch is not end of stream, and returning
// one would let a consumer that checks Rows() == 0 truncate the scan.
func TestSourceSkipsEmptyBatches(t *testing.T) {
	s, empty := rowsBatch(t, 0)
	_, full := rowsBatch(t, 5)
	src, err := New(s, empty, full, empty)
	if err != nil {
		t.Fatal(err)
	}
	got, vals := sizes(t, src, source.ScanSpec{BatchSize: 128})
	if len(got) != 1 || got[0] != 5 || len(vals) != 5 {
		t.Errorf("batch sizes %v with %d values, want [5] and 5", got, len(vals))
	}
}

// TestSourceStillRecordsProjections guards the property this source exists to
// demonstrate — that pushdown reaches the reader — against the split changing how
// Open works.
func TestSourceStillRecordsProjections(t *testing.T) {
	s := dtype.MustSchema(
		dtype.NotNull("a", dtype.Int64),
		dtype.NotNull("b", dtype.Int64),
	)
	vals := []int64{1, 2, 3}
	b, err := data.NewBatch(s, []*data.Column{
		data.NewFixed("a", dtype.Int64, vals, bitmap.AllSet(3)),
		data.NewFixed("b", dtype.Int64, vals, bitmap.AllSet(3)),
	})
	if err != nil {
		t.Fatal(err)
	}
	src, err := New(s, b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.Open(context.Background(),
		source.ScanSpec{Projection: []string{"b"}, BatchSize: 2}); err != nil {
		t.Fatal(err)
	}
	p := src.Projections()
	if len(p) != 1 || len(p[0]) != 1 || p[0][0] != "b" {
		t.Errorf("Projections() = %v, want [[b]]", p)
	}
}

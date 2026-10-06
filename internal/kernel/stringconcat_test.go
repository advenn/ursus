package kernel_test

// Concat of String and Binary columns copies offsets and characters, not rows
// (step 103), and answers as the per-row version did.
//
// Every Collect ends in a Concat, serially, and it built one Go string per row and
// copied every byte twice: 0.37 s of h2o j3's 0.58 s at 2M rows. The parts it sees
// are derived columns as often as not — the memory source slices, and so does every
// operator that windows a batch — and a sliced String column keeps its whole
// character buffer with absolute offsets, so a part's first offset is rarely zero.

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/uerr"
)

// stringPart is one column to concatenate, and the rows it should contribute.
type stringPart struct {
	col  *data.Column
	want []*string // nil is a null row
}

// randomStringPart builds a String or Binary column of n rows, some null and some
// empty, and returns a slice of it — so its offsets start past zero — or the whole.
func randomStringPart(r *rand.Rand, dt dtype.DataType, n int) stringPart {
	vals := make([]string, n+4)
	vb := bitmap.NewBuilder(n + 4)
	for i := range vals {
		vals[i] = fmt.Sprintf("%0*d", r.IntN(6), r.IntN(1000))
		vb.Append(r.IntN(5) != 0)
	}
	col := data.NewString("s", vals, vb.Finish()).WithDType(dt)
	if r.IntN(2) == 0 {
		col = col.Slice(2, n) // offsets start at vals[2]'s, not zero
	} else {
		col = col.Slice(0, n)
	}
	want := make([]*string, n)
	acc := col.Strings()
	for i := range n {
		if col.IsValid(i) {
			v := acc.Get(i)
			want[i] = &v
		}
	}
	return stringPart{col, want}
}

func TestConcatStringsMatchesRowByRow(t *testing.T) {
	r := rand.New(rand.NewPCG(103, 1))
	for _, dt := range []dtype.DataType{dtype.String, dtype.Binary} {
		schema := dtype.MustSchema(dtype.Of("s", dt))
		for trial := range 200 {
			var parts []stringPart
			for range 1 + r.IntN(5) {
				switch r.IntN(6) {
				case 0: // an empty part
					parts = append(parts, randomStringPart(r, dt, 0))
				case 1: // every row null, with no character buffer at all
					n := r.IntN(4)
					parts = append(parts, stringPart{data.NewNull("s", dt, n), make([]*string, n)})
				default:
					parts = append(parts, randomStringPart(r, dt, r.IntN(40)))
				}
			}
			var batches []*data.Batch
			var want []*string
			for _, p := range parts {
				b, err := data.NewBatch(schema, []*data.Column{p.col})
				if err != nil {
					t.Fatal(err)
				}
				batches = append(batches, b)
				want = append(want, p.want...)
			}
			out, err := kernel.Concat(schema, batches)
			if err != nil {
				t.Fatal(err)
			}
			got := out.Column(0)
			if got.Len() != len(want) || got.DType() != dt {
				t.Fatalf("%s trial %d: %d rows of %s, want %d of %s", dt, trial, got.Len(), got.DType(), len(want), dt)
			}
			acc := got.Strings()
			for i, w := range want {
				switch {
				case w == nil && got.IsValid(i):
					t.Fatalf("%s trial %d row %d: %q, want null", dt, trial, i, acc.Get(i))
				case w != nil && !got.IsValid(i):
					t.Fatalf("%s trial %d row %d: null, want %q", dt, trial, i, *w)
				case w != nil && acc.Get(i) != *w:
					t.Fatalf("%s trial %d row %d: %q, want %q", dt, trial, i, acc.Get(i), *w)
				}
			}
		}
	}
}

func TestConcatStringsRefusesPastItsOffsets(t *testing.T) {
	defer func(old int64) { data.MaxStringBytes = old }(data.MaxStringBytes)
	data.MaxStringBytes = 100

	schema := dtype.MustSchema(dtype.Of("s", dtype.String))
	part := func() *data.Batch {
		b, err := data.NewBatch(schema, []*data.Column{data.NewString("s", []string{fmt.Sprintf("%060d", 0)}, bitmap.View{})})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	err := catch(func() error {
		_, err := kernel.Concat(schema, []*data.Batch{part(), part()})
		return err
	})
	if !errors.Is(err, uerr.ErrResource) {
		t.Fatalf("120 characters under a 100-character limit: want a resource error, got %v", err)
	}
}

package kernel_test

import (
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
)

// TestNullColumnIsGatherable is the regression test for a bug that reached a
// shipped release: data.NewNull carries no payload buffer, so anything that
// GATHERS from it fails — Take returns "no fixed-width payload" for a numeric
// column and PANICS for a string one.
//
// Both are reachable from ordinary queries. Outer joins gather from padding on
// every unmatched row, and Min over an all-null group did the same (see
// TestMinOverAnAllNullGroup).
func TestNullColumnIsGatherable(t *testing.T) {
	types := []dtype.DataType{
		dtype.Bool, dtype.Int8, dtype.Int16, dtype.Int32, dtype.Int64,
		dtype.Uint8, dtype.Uint16, dtype.Uint32, dtype.Uint64,
		dtype.Float32, dtype.Float64, dtype.Int128, dtype.String, dtype.Binary,
		dtype.Date, dtype.Decimal(10, 2),
	}

	for _, dt := range types {
		t.Run(dt.String(), func(t *testing.T) {
			c, err := kernel.NullColumn("pad", dt, 3)
			if err != nil {
				t.Fatalf("NullColumn(%s): %v", dt, err)
			}
			if c.DType() != dt {
				t.Errorf("dtype = %s, want %s", c.DType(), dt)
			}
			for i := range 3 {
				if c.IsValid(i) {
					t.Errorf("row %d should be null", i)
				}
			}

			// The whole point: it survives a gather, including a NullIndex slot.
			got, err := kernel.Take(c, []int32{kernel.NullIndex, 0, 2, 1})
			if err != nil {
				t.Fatalf("Take on a null %s column: %v", dt, err)
			}
			if got.Len() != 4 {
				t.Errorf("Take produced %d rows, want 4", got.Len())
			}
			for i := range 4 {
				if got.IsValid(i) {
					t.Errorf("gathered row %d should be null", i)
				}
			}

			// And a concat, which is what exec.Collect does to every result.
			s, err := dtype.NewSchema(dtype.Field{Name: "pad", Type: dt, Nullable: true})
			if err != nil {
				t.Fatal(err)
			}
			b1, err := data.NewBatch(s, []*data.Column{c})
			if err != nil {
				t.Fatal(err)
			}
			out, err := kernel.Concat(s, []*data.Batch{b1, b1})
			if err != nil {
				t.Fatalf("Concat of null %s columns: %v", dt, err)
			}
			if out.Rows() != 6 {
				t.Errorf("Concat produced %d rows, want 6", out.Rows())
			}
		})
	}
}

// TestNullColumnValuesAreReadable checks the payload is real rather than merely
// present: a typed read must succeed and return zeros.
func TestNullColumnValuesAreReadable(t *testing.T) {
	c, err := kernel.NullColumn("x", dtype.Int64, 2)
	if err != nil {
		t.Fatal(err)
	}
	vals, err := data.Values[int64](c)
	if err != nil {
		t.Fatalf("a NullColumn must have a readable payload: %v", err)
	}
	if len(vals) != 2 || vals[0] != 0 || vals[1] != 0 {
		t.Errorf("values = %v, want [0 0]", vals)
	}

	d, err := kernel.NullColumn("d", dtype.Decimal(10, 2), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := data.Values[i128.Int128](d); err != nil {
		t.Errorf("a Decimal null column must read as Int128: %v", err)
	}
}

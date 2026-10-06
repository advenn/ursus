package kernel_test

// A List column holds at most MaxListElements elements, and past that it is refused
// rather than wrapped (step 99).
//
// A List's offsets are 32-bit, as a String's are. Step 87 bounded a String's
// characters (audit.md S24); a List's elements had no bound, so the offsets that
// concatenation, a gather or an implode compute as int32 wrapped silently past 2^31
// elements. Measured once, outside the suite: two one-row List(Bool) columns of 2^30
// elements concatenated to a second row spanning [2^30, -2^31), with no error.
//
// The limit is lowered here, as bigstring_test lowers the String one, so the test
// costs nothing.

import (
	"errors"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/uerr"
)

// catch runs f and returns what it raised, if anything: NewList has no error to
// return, and raises instead.
func catch(f func() error) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = uerr.FromPanic(v, "test")
		}
	}()
	return f()
}

func TestListOffsetsDoNotWrap(t *testing.T) {
	defer func(old int64) { data.MaxListElements = old }(data.MaxListElements)
	data.MaxListElements = 100

	schema := dtype.MustSchema(dtype.Of("l", dtype.List(dtype.Int64)))
	// list is a one-row List column whose row holds n elements.
	list := func(n int) *data.Batch {
		child := data.NewFixed("item", dtype.Int64, make([]int64, n), bitmap.View{})
		b, err := data.NewBatch(schema, []*data.Column{
			data.NewList("l", []int32{0, int32(n)}, child, bitmap.View{})})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	t.Run("concatenated past the limit: refused", func(t *testing.T) {
		a, b := list(60), list(60)
		err := catch(func() error {
			_, err := kernel.Concat(schema, []*data.Batch{a, b})
			return err
		})
		if !errors.Is(err, uerr.ErrResource) {
			t.Fatalf("want a resource error, got: %v", err)
		}
	})
	t.Run("gathered past the limit: refused", func(t *testing.T) {
		a := list(60)
		err := catch(func() error {
			_, err := kernel.Take(a.Column(0), []int32{0, 0})
			return err
		})
		if !errors.Is(err, uerr.ErrResource) {
			t.Fatalf("want a resource error, got: %v", err)
		}
	})
	t.Run("built past the limit: refused", func(t *testing.T) {
		err := catch(func() error { list(101); return nil })
		if !errors.Is(err, uerr.ErrResource) {
			t.Fatalf("want a resource error, got: %v", err)
		}
	})
	t.Run("control: exactly the limit is built, and reads back", func(t *testing.T) {
		out, err := kernel.Concat(schema, []*data.Batch{list(40), list(60)})
		if err != nil {
			t.Fatal(err)
		}
		start, end, _ := out.Column(0).Lists().Get(1)
		if start != 40 || end != 100 {
			t.Errorf("the second row spans [%d, %d), want [40, 100)", start, end)
		}
	})
}

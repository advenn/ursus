package kernel

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// TestTakeChunkedIsTake: a selection taken a chunk at a time, the chunks run in any
// order, answers what Take answers, to the bit: every type's values and nulls, a
// null index's row, and a chunk that does not divide the selection (step 327).
func TestTakeChunkedIsTake(t *testing.T) {
	const n = 1000
	r := rand.New(rand.NewPCG(3, 27))
	vb := bitmap.NewBuilder(n)
	ints, f32s, decs, u8s := make([]int64, n), make([]float32, n), make([]i128.Int128, n), make([]uint8, n)
	strs, bits := make([]string, n), bitmap.NewBuilder(n)
	for i := range n {
		vb.Append(i%3 != 0)
		ints[i], f32s[i], u8s[i] = r.Int64(), r.Float32(), uint8(i)
		decs[i] = i128.FromInt64(r.Int64N(1 << 40))
		strs[i] = strings.Repeat("ab", r.IntN(5)) + string(rune('a'+i%26))
		bits.Append(r.IntN(2) == 0)
	}
	valid := vb.Finish()
	cols := []*data.Column{
		data.NewFixed("i64", dtype.Int64, ints, valid),
		data.NewFixed("f32", dtype.Float32, f32s, bitmap.AllSet(n)),
		data.NewFixed("dec", dtype.Decimal(20, 2), decs, valid),
		data.NewFixed("u8", dtype.Uint8, u8s, bitmap.AllSet(n)),
		data.NewString("s", strs, valid),
		data.NewString("t", strs, bitmap.AllSet(n)),
		data.NewBool("b", bits.Finish(), valid),
	}
	sel := make([]int32, 2500)
	for j := range sel {
		sel[j] = int32(r.IntN(n))
		if j%97 == 0 {
			sel[j] = NullIndex
		}
	}
	// The chunks backwards, as a parallel run may finish them.
	backwards := func(n int, fn func(k int) error) error {
		for k := n - 1; k >= 0; k-- {
			if err := fn(k); err != nil {
				return err
			}
		}
		return nil
	}
	for _, c := range cols {
		want, err := Take(c, sel)
		if err != nil {
			t.Fatal(err)
		}
		for _, chunk := range []int{1, 7, 256, 2500, 4096} {
			got, err := TakeChunked(c, sel, chunk, backwards)
			if err != nil {
				t.Fatalf("%s by %d: %v", c.Name(), chunk, err)
			}
			if err := sameColumn(got, want); err != nil {
				t.Errorf("%s by %d: %v", c.Name(), chunk, err)
			}
		}
	}
	// An error from the run is the answer.
	stop := errors.New("stop")
	if _, err := TakeChunked(cols[0], sel, 7, func(int, func(int) error) error { return stop }); err != stop {
		t.Errorf("a failed run: %v", err)
	}
	if _, err := TakeChunked(cols[4], sel, 7, func(int, func(int) error) error { return stop }); err != stop {
		t.Errorf("a failed run of text: %v", err)
	}
}

// sameColumn is nil if a and b hold the same values and nulls, to the bit.
func sameColumn(a, b *data.Column) error {
	if a.Len() != b.Len() || a.DType() != b.DType() || a.Name() != b.Name() {
		return fmt.Errorf("%s %s of %d vs %s %s of %d", a.Name(), a.DType(), a.Len(), b.Name(), b.DType(), b.Len())
	}
	for i := range a.Len() {
		if a.IsValid(i) != b.IsValid(i) {
			return fmt.Errorf("row %d's validity %v vs %v", i, a.IsValid(i), b.IsValid(i))
		}
	}
	switch a.DType().Physical().ID() {
	case dtype.TypeString:
		as, bs := a.Strings(), b.Strings()
		return eachValid(a, func(i int) bool { return as.Get(i) == bs.Get(i) })
	case dtype.TypeBool:
		return eachValid(a, func(i int) bool { return a.Bools().Get(i) == b.Bools().Get(i) })
	case dtype.TypeInt64:
		return sameValues[int64](a, b)
	case dtype.TypeFloat32:
		return sameValues[float32](a, b)
	case dtype.TypeInt128:
		return sameValues[i128.Int128](a, b)
	case dtype.TypeUint8:
		return sameValues[uint8](a, b)
	}
	return fmt.Errorf("sameColumn reads no %s", a.DType())
}

func sameValues[T int64 | float32 | i128.Int128 | uint8](a, b *data.Column) error {
	av, err := data.Values[T](a)
	if err != nil {
		return err
	}
	bv, err := data.Values[T](b)
	if err != nil {
		return err
	}
	return eachValid(a, func(i int) bool { return av[i] == bv[i] })
}

func eachValid(c *data.Column, same func(i int) bool) error {
	for i := range c.Len() {
		if c.IsValid(i) && !same(i) {
			return fmt.Errorf("row %d differs", i)
		}
	}
	return nil
}

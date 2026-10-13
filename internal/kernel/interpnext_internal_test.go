package kernel

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// TestInterpolationFromBothWalksIsTheKernels: InterpolateFrom, given what a walk each
// way finds — the last value and its position, each row's own, the next value and
// its position from the end — answers as winInterpolate and winInterpolateNearest do
// over the column whole (step 238). Over integers and Float32s with runs of nulls of
// every length, even and odd gaps for nearest's tie, and nulls at both ends.
func TestInterpolationFromBothWalksIsTheKernels(t *testing.T) {
	rng := rand.New(rand.NewPCG(238, 1))
	const n = 2000
	iv := make([]int64, n)
	fv := make([]float32, n)
	ok := make([]bool, n)
	for i := range n {
		iv[i] = int64(rng.IntN(1000)) - 500
		fv[i] = float32(rng.NormFloat64() * 10)
		// Nulls in runs of 0 to 12, and none at all near the ends sometimes.
		ok[i] = rng.IntN(13) < 4 && i > 3 && i < n-5
	}
	bits := bitmap.NewBuilder(n)
	for _, v := range ok {
		bits.Append(v)
	}
	valid := bits.Finish()
	ident := make([]int32, n)
	for i := range ident {
		ident[i] = int32(i)
	}
	seg := Segments{Perm: ident, Bounds: []int32{0, n}}
	for _, col := range []*data.Column{
		data.NewFixed("i", dtype.Int64, iv, valid),
		data.NewFixed("f", dtype.Float32, fv, valid),
	} {
		// The two walks, written out.
		prevSel, nextSel := make([]int32, n), make([]int32, n)
		prevAt, nextBack := make([]int64, n), make([]int64, n)
		pOK, nOK := bitmap.NewBuilder(n), bitmap.NewBuilder(n)
		own := make([]uint32, n)
		last := int32(-1)
		for i := range n {
			if ok[i] {
				last = int32(i)
			}
			prevSel[i], prevAt[i] = NullIndex, 0
			if last >= 0 {
				prevSel[i], prevAt[i] = last, int64(last)
			}
			pOK.Append(last >= 0)
			own[i] = uint32(i + 1)
		}
		next := int32(-1)
		nb := make([]bool, n)
		for i := n - 1; i >= 0; i-- {
			if ok[i] {
				next = int32(i)
			}
			nextSel[i] = NullIndex
			if next >= 0 {
				nextSel[i], nextBack[i] = next, int64(n-1-int(next))
			}
			nb[i] = next >= 0
		}
		for _, v := range nb {
			nOK.Append(v)
		}
		prevVal, err := Take(col, prevSel)
		if err != nil {
			t.Fatal(err)
		}
		nextVal, err := Take(col, nextSel)
		if err != nil {
			t.Fatal(err)
		}
		at := data.NewFixed("at", dtype.Int64, prevAt, pOK.Finish())
		back := data.NewFixed("back", dtype.Int64, nextBack, nOK.Finish())
		pos := data.NewFixed("own", dtype.Uint32, own, bitmap.AllSet(n))
		for _, nearest := range []bool{false, true} {
			out := dtype.Float64
			if col.DType() == dtype.Float32 {
				out = dtype.Float32
			}
			if nearest {
				out = col.DType()
			}
			var want *data.Column
			if nearest {
				want, err = winInterpolateNearest("w", col, seg)
			} else {
				want, err = winInterpolate("w", out, col, seg)
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := InterpolateFrom(nearest, "w", out, n, prevVal, at, pos, nextVal, back)
			if err != nil {
				t.Fatal(err)
			}
			wf, err := floatsOf(want)
			if err != nil {
				t.Fatal(err)
			}
			gf, err := floatsOf(got)
			if err != nil {
				t.Fatal(err)
			}
			for i := range n {
				if want.IsValid(i) != got.IsValid(i) ||
					(want.IsValid(i) && math.Float64bits(wf[i]) != math.Float64bits(gf[i])) {
					t.Fatalf("%s nearest=%v: row %d is %v (valid %v), want %v (valid %v)",
						col.Name(), nearest, i, gf[i], got.IsValid(i), wf[i], want.IsValid(i))
				}
			}
			if got.DType() != want.DType() {
				t.Fatalf("%s nearest=%v: a %s, want a %s", col.Name(), nearest, got.DType(), want.DType())
			}
		}
	}
}

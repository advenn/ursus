package kernel

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// TestConcatOwnedIsConcat: concatenating batches it owns gives what Concat gives,
// over fixed-width and string columns with nulls, one batch and none, and it clears
// the slice it was handed, so the caller holds no batch it gave up (step 128).
func TestConcatOwnedIsConcat(t *testing.T) {
	rng := rand.New(rand.NewPCG(128, 1))
	sch := dtype.MustSchema(dtype.Of("i", dtype.Int64), dtype.Of("s", dtype.String), dtype.Of("f", dtype.Float64))
	batch := func(n int) *data.Batch {
		iv := make([]int64, n)
		sv := make([]string, n)
		fv := make([]float64, n)
		vb := bitmap.NewBuilder(n)
		for k := range n {
			iv[k], sv[k], fv[k] = rng.Int64(), fmt.Sprint(rng.IntN(1000)), rng.Float64()
			vb.Append(rng.IntN(5) > 0)
		}
		valid := vb.Finish()
		b, err := data.NewBatch(sch, []*data.Column{
			data.NewFixed("i", dtype.Int64, iv, valid),
			data.NewString("s", sv, valid),
			data.NewFixed("f", dtype.Float64, fv, bitmap.View{}),
		})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, nb := range []int{0, 1, 2, 7} {
		in := make([]*data.Batch, nb)
		for k := range in {
			in[k] = batch(rng.IntN(50))
		}
		want, err := Concat(sch, append([]*data.Batch(nil), in...))
		if err != nil {
			t.Fatal(err)
		}
		got, err := ConcatOwned(sch, in)
		if err != nil {
			t.Fatal(err)
		}
		for k, b := range in {
			if b != nil {
				t.Fatalf("%d batches: batch %d is still held", nb, k)
			}
		}
		if got.Rows() != want.Rows() {
			t.Fatalf("%d batches: %d rows, want %d", nb, got.Rows(), want.Rows())
		}
		for ci := range sch.Len() {
			g, w := got.Column(ci), want.Column(ci)
			for r := range want.Rows() {
				if g.IsValid(r) != w.IsValid(r) {
					t.Fatalf("%d batches: column %d row %d validity differs", nb, ci, r)
				}
			}
		}
		gs, ws := got.Column(1).Strings(), want.Column(1).Strings()
		gi, _ := data.Values[int64](got.Column(0))
		wi, _ := data.Values[int64](want.Column(0))
		for r := range want.Rows() {
			if gs.Get(r) != ws.Get(r) || gi[r] != wi[r] {
				t.Fatalf("%d batches: row %d differs", nb, r)
			}
		}
	}
}

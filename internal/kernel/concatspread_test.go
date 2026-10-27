package kernel

import (
	"fmt"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// TestConcatOwnedNIsConcat: since step 389 Collect copies each column's rows on the
// query's threads, a piece each, where the pieces fall anywhere in the parts. The
// answer is Concat's, row for row: integers, floats, Int128 and text with nulls, and a
// Bool, which is copied whole; parts empty, of one row, sliced out of a larger column,
// and text parts with no payload; at 32-bit offsets and past MaxStringBytes at 64; on
// 2 to 7 workers. A panic in a piece is raised again in the caller.
func TestConcatOwnedNIsConcat(t *testing.T) {
	defer func(m int) { parallelMin = m }(parallelMin)
	defer func(old int64) { data.MaxStringBytes = old }(data.MaxStringBytes)
	parallelMin = 1
	rng := rand.New(rand.NewPCG(389, 1))
	sch := dtype.MustSchema(dtype.Of("i", dtype.Int64), dtype.Of("s", dtype.String), dtype.Of("f", dtype.Float64),
		dtype.Of("q", dtype.Int128), dtype.Of("b", dtype.Bool))
	batch := func(n int) *data.Batch {
		iv, fv, qv := make([]int64, n), make([]float64, n), make([]i128.Int128, n)
		vb, bb := bitmap.NewBuilder(n), bitmap.NewBuilder(n)
		for k := range n {
			iv[k], fv[k], qv[k] = rng.Int64(), rng.NormFloat64(), i128.FromInt64(rng.Int64())
			vb.Append(rng.IntN(5) > 0)
			bb.Append(rng.IntN(2) == 0)
		}
		valid := vb.Finish()
		// The text: a slice of a larger column, so its offsets start past 0; or a
		// column of nulls with no payload at all.
		var s *data.Column
		if rng.IntN(5) == 0 {
			s = data.NewNull("s", dtype.String, n)
		} else {
			off := rng.IntN(20)
			sv := make([]string, off+n+rng.IntN(20))
			for k := range sv {
				sv[k] = fmt.Sprintf("%05d", rng.IntN(100_000))[:rng.IntN(6)]
			}
			s = data.NewString("s", sv, bitmap.View{}).Slice(off, n)
		}
		b, err := data.NewBatch(sch, []*data.Column{
			data.NewFixed("i", dtype.Int64, iv, valid),
			s,
			data.NewFixed("f", dtype.Float64, fv, bitmap.View{}),
			data.NewFixed("q", dtype.Int128, qv, valid),
			data.NewBool("b", bb.Finish(), bitmap.View{}),
		})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, limit := range []int64{math.MaxInt32, 64} {
		data.MaxStringBytes = limit
		for _, workers := range []int{2, 3, 4, 7} {
			for _, nb := range []int{1, 2, 5, 40} {
				in := make([]*data.Batch, nb)
				for k := range in {
					switch rng.IntN(6) {
					case 0:
						in[k] = batch(0)
					case 1:
						in[k] = batch(1)
					default:
						in[k] = batch(rng.IntN(300))
					}
				}
				want, err := Concat(sch, append([]*data.Batch(nil), in...))
				if err != nil {
					t.Fatal(err)
				}
				got, err := ConcatOwnedN(sch, in, workers)
				if err != nil {
					t.Fatal(err)
				}
				name := fmt.Sprintf("offsets to %d, %d workers, %d batches", limit, workers, nb)
				if got.Rows() != want.Rows() {
					t.Fatalf("%s: %d rows, want %d", name, got.Rows(), want.Rows())
				}
				sameColumns(t, name, got, want)
			}
		}
	}

	// A panic in a piece reaches the caller, where the query recovers it.
	defer func() {
		if v := recover(); v != "boom" {
			t.Fatalf("recovered %v, want the piece's panic", v)
		}
	}()
	spreader(4)(1000, func(lo, hi int) {
		if lo > 0 {
			panic("boom")
		}
	})
	t.Fatal("a piece's panic was lost")
}

// sameColumns fails unless got and want hold the same rows, column by column.
func sameColumns(t *testing.T, name string, got, want *data.Batch) {
	t.Helper()
	for ci := range want.NumCols() {
		g, w := got.Column(ci), want.Column(ci)
		for r := range want.Rows() {
			if g.IsValid(r) != w.IsValid(r) {
				t.Fatalf("%s: column %d row %d validity differs", name, ci, r)
			}
		}
		switch w.DType().ID() {
		case dtype.TypeInt64:
			gv, _ := data.Values[int64](g)
			wv, _ := data.Values[int64](w)
			for r := range wv {
				if gv[r] != wv[r] {
					t.Fatalf("%s: column %d row %d: %d, want %d", name, ci, r, gv[r], wv[r])
				}
			}
		case dtype.TypeFloat64:
			gv, _ := data.Values[float64](g)
			wv, _ := data.Values[float64](w)
			for r := range wv {
				if math.Float64bits(gv[r]) != math.Float64bits(wv[r]) {
					t.Fatalf("%s: column %d row %d: %v, want %v", name, ci, r, gv[r], wv[r])
				}
			}
		case dtype.TypeInt128:
			gv, _ := data.Values[i128.Int128](g)
			wv, _ := data.Values[i128.Int128](w)
			for r := range wv {
				if gv[r] != wv[r] {
					t.Fatalf("%s: column %d row %d differs", name, ci, r)
				}
			}
		case dtype.TypeString:
			gs, ws := g.Strings(), w.Strings()
			// A null row's bytes too, none, its offsets in order; but for a column of
			// nulls alone, which may have no offsets to read.
			payload := w.NullCount() < w.Len()
			for r := range want.Rows() {
				if (payload || w.IsValid(r)) && gs.Get(r) != ws.Get(r) {
					t.Fatalf("%s: column %d row %d: %q, want %q", name, ci, r, gs.Get(r), ws.Get(r))
				}
			}
		case dtype.TypeBool:
			gb, wb := g.Bools(), w.Bools()
			for r := range want.Rows() {
				if gb.Get(r) != wb.Get(r) {
					t.Fatalf("%s: column %d row %d differs", name, ci, r)
				}
			}
		default:
			t.Fatalf("%s: no comparison for %s", name, w.DType())
		}
	}
}

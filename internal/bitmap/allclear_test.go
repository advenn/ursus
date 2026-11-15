package bitmap_test

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/advenn/ursus/internal/bitmap"
)

// Zeros is the all-clear form, with no storage (step 529): a column of n nulls, a
// Null column's above all, held a bit a row to say what its type says. Every reader
// of a View is this package's, so each must answer of the form what it answers of
// the same bits stored, and the model every test here compares with is ref's, the
// bits one by one.

// operand is a view and the bits it holds.
type operand struct {
	name string
	v    bitmap.View
	r    ref
}

// operands are views of n bits in every form a View takes: all set and all clear
// with no storage, and bits stored, random, all clear and all set; each form also
// sliced out of a longer view at a bit offset, which the forms with no storage keep.
func operands(rng *rand.Rand, n int) []operand {
	ones := make(ref, n)
	for i := range ones {
		ones[i] = true
	}
	random := randRef(rng, n)
	out := []operand{
		{"AllSet", bitmap.AllSet(n), ones},
		{"Zeros", bitmap.Zeros(n), make(ref, n)},
		{"stored random", random.view(), random},
		{"stored zeros", make(ref, n).view(), make(ref, n)},
		{"stored ones", ones.view(), ones},
	}
	off := 1 + rng.IntN(70)
	long := randRef(rng, off+n+rng.IntN(9))
	out = append(out,
		operand{"AllSet sliced", bitmap.AllSet(len(long)).Slice(off, n), ones},
		operand{"Zeros sliced", bitmap.Zeros(len(long)).Slice(off, n), make(ref, n)},
		operand{"stored sliced", long.view().Slice(off, n), long[off : off+n]},
	)
	return out
}

// lengths straddle every byte and word boundary a view's arithmetic has.
var lengths = []int{0, 1, 2, 7, 8, 9, 15, 16, 17, 63, 64, 65, 127, 128, 129, 300, 1000}

func words(v bitmap.View) []uint64 {
	var out []uint64
	v.Words(func(w uint64, nbits int) bool {
		out = append(out, w, uint64(nbits))
		return true
	})
	return out
}

// TestZerosReadsAsStoredZeros: the all-clear form answers every reader as the same
// bits stored do, sliced or not, and Buffer is the one reader that makes them.
func TestZerosReadsAsStoredZeros(t *testing.T) {
	rng := rand.New(rand.NewPCG(529, 1))
	for _, n := range lengths {
		stored := make(ref, n).view()
		for _, v := range []bitmap.View{bitmap.Zeros(n), bitmap.Zeros(n+70).Slice(rng.IntN(71), n)} {
			ctx := fmt.Sprintf("Zeros of %d bits", n)
			checkEqual(t, v, make(ref, n), ctx)
			if v.CountUnset() != n {
				t.Fatalf("%s: CountUnset = %d", ctx, v.CountUnset())
			}
			if !v.IsAllClear() || v.IsAllSet() {
				t.Fatalf("%s: IsAllClear %v, IsAllSet %v", ctx, v.IsAllClear(), v.IsAllSet())
			}
			if got, want := words(v), words(stored); !slices.Equal(got, want) {
				t.Fatalf("%s: Words %v, and stored zeros' %v", ctx, got, want)
			}
			if buf, off := v.RawBits(); buf != nil || off != 0 {
				t.Fatalf("%s: RawBits (%v, %d), want none", ctx, buf, off)
			}
			if !v.IsByteAligned() {
				t.Fatalf("%s: not byte-aligned", ctx)
			}
			b := v.Buffer()
			if b == nil || b.Len() < (n+7)/8 {
				t.Fatalf("%s: Buffer %v, want %d bytes", ctx, b, (n+7)/8)
			}
			checkEqual(t, bitmap.NewView(b.Bytes(), 0, n), make(ref, n), ctx+", its Buffer")
			for _, s := range [][2]int{{0, n}, {n / 3, n - n/3}, {n / 2, n / 4}, {n, 0}} {
				sub := v.Slice(s[0], s[1])
				if !sub.IsAllClear() {
					t.Fatalf("%s: Slice(%d, %d) left the form", ctx, s[0], s[1])
				}
				checkEqual(t, sub, make(ref, s[1]), ctx+", sliced")
			}
		}
		if bitmap.AllSet(n).IsAllClear() || stored.IsAllClear() {
			t.Fatalf("%d bits: the all-set form, or stored zeros, says it is the all-clear form", n)
		}
	}
}

// TestCombinatorsOverEveryForm: And, Or, AndNot, Not, AndMulti, AppendSetPositions
// and a Builder's AppendView, of every pair of forms, are the bits one by one; and
// where the answer is all clear or all set for its operands' forms alone, it is that
// form, with no storage.
func TestCombinatorsOverEveryForm(t *testing.T) {
	rng := rand.New(rand.NewPCG(529, 2))
	for _, n := range lengths {
		ops := operands(rng, n)
		for _, a := range ops {
			ctx := fmt.Sprintf("%d bits, %s", n, a.name)
			not := make(ref, n)
			for i := range n {
				not[i] = !a.r[i]
			}
			checkEqual(t, bitmap.Not(a.v), not, "Not of "+ctx)
			for _, b := range ops {
				ctx := ctx + " with " + b.name
				and, or, andNot := make(ref, n), make(ref, n), make(ref, n)
				var pos []int32
				for i := range n {
					and[i] = a.r[i] && b.r[i]
					or[i] = a.r[i] || b.r[i]
					andNot[i] = a.r[i] && !b.r[i]
					if and[i] {
						pos = append(pos, int32(i))
					}
				}
				checkEqual(t, bitmap.And(a.v, b.v), and, "And of "+ctx)
				checkEqual(t, bitmap.Or(a.v, b.v), or, "Or of "+ctx)
				checkEqual(t, bitmap.AndNot(a.v, b.v), andNot, "AndNot of "+ctx)
				if got := bitmap.AppendSetPositions(nil, a.v, b.v); !slices.Equal(got, pos) {
					t.Fatalf("AppendSetPositions of %s: %v, want %v", ctx, got, pos)
				}
				for _, c := range ops[:3] {
					all := make(ref, n)
					for i := range n {
						all[i] = and[i] && c.r[i]
					}
					checkEqual(t, bitmap.AndMulti(a.v, b.v, c.v), all, "AndMulti of "+ctx+" and "+c.name)
				}
				bld := bitmap.NewBuilder(0)
				bld.AppendView(a.v)
				bld.AppendView(b.v)
				checkEqual(t, bld.Finish(), append(append(ref{}, a.r...), b.r...), "AppendView of "+ctx)
			}
		}
		clear, set := bitmap.Zeros(n), bitmap.AllSet(n)
		random := randRef(rng, n).view()
		for name, v := range map[string]bitmap.View{
			"And(Zeros, stored)":     bitmap.And(clear, random),
			"And(stored, Zeros)":     bitmap.And(random, clear),
			"Or(Zeros, Zeros)":       bitmap.Or(clear, clear),
			"AndNot(stored, AllSet)": bitmap.AndNot(random, set),
			"AndNot(Zeros, stored)":  bitmap.AndNot(clear, random),
			"Not(AllSet)":            bitmap.Not(set),
			"AndMulti with Zeros":    bitmap.AndMulti(random, set, clear),
		} {
			if !v.IsAllClear() {
				t.Errorf("%d bits: %s is not the all-clear form", n, name)
			}
		}
		if !bitmap.Not(clear).IsAllSet() || !bitmap.Or(clear, set).IsAllSet() {
			t.Errorf("%d bits: Not(Zeros) or Or(Zeros, AllSet) is not the all-set form", n)
		}
	}
}

// TestZerosAllocatesNothing: Zeros of 2^40 bits, the rows a 232-byte IPC stream may
// declare, sliced, counted, read and combined with the other form, allocates nothing.
func TestZerosAllocatesNothing(t *testing.T) {
	var sink int
	allocs := testing.AllocsPerRun(100, func() {
		v := bitmap.Zeros(1<<40).Slice(1<<20, 1<<39)
		sink += v.CountSet() + v.CountUnset() + v.Len()
		if v.Get(1<<38) || !v.IsAllClear() {
			sink++
		}
		sink += bitmap.And(v, bitmap.AllSet(v.Len())).Len()
		sink += bitmap.Or(v, v).Len() + bitmap.Not(bitmap.AllSet(1<<40)).Len()
		sink += bitmap.AndNot(bitmap.AllSet(v.Len()), bitmap.AllSet(v.Len())).Len()
		sink += len(bitmap.AppendSetPositions(nil, v, bitmap.AllSet(v.Len())))
	})
	if allocs != 0 {
		t.Fatalf("Zeros of 2^40 bits and its readers allocated %v times", allocs)
	}
	_ = sink
}

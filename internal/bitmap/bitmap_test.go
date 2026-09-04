package bitmap_test

import (
	"math/rand/v2"
	"testing"

	"ursus/internal/bitmap"
)

// ref is the obvious, slow, obviously-correct model every test compares against.
type ref []bool

func (r ref) view() bitmap.View {
	b := bitmap.NewBuilder(len(r))
	for _, v := range r {
		b.Append(v)
	}
	return b.Finish()
}

func randRef(rng *rand.Rand, n int) ref {
	r := make(ref, n)
	for i := range r {
		r[i] = rng.IntN(2) == 1
	}
	return r
}

func checkEqual(t *testing.T, got bitmap.View, want ref, ctx string) {
	t.Helper()
	if got.Len() != len(want) {
		t.Fatalf("%s: Len = %d, want %d", ctx, got.Len(), len(want))
	}
	for i, w := range want {
		if got.Get(i) != w {
			t.Fatalf("%s: bit %d = %v, want %v", ctx, i, got.Get(i), w)
		}
	}
	set := 0
	for _, w := range want {
		if w {
			set++
		}
	}
	if got.CountSet() != set {
		t.Fatalf("%s: CountSet = %d, want %d", ctx, got.CountSet(), set)
	}
	// PopCount goes through Words; CountSet goes through arrow's SIMD path.
	// They must agree, which is what validates the Words iterator.
	if got.PopCount() != set {
		t.Fatalf("%s: PopCount = %d, want %d (Words iterator is wrong)", ctx, got.PopCount(), set)
	}
}

func TestBuilderAndView(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	// Lengths that straddle byte and word boundaries in every way.
	for _, n := range []int{0, 1, 2, 7, 8, 9, 15, 16, 17, 63, 64, 65, 127, 128, 129, 1000} {
		r := randRef(rng, n)
		checkEqual(t, r.view(), r, "builder")
	}
}

// TestAppendBitsAtEveryChunkSize is the regression test for defect D14b.
//
// A SIMD kernel appends its movemask n bits at a time, where n is the runtime lane
// count. Every one of these chunk sizes must produce an identical bitmap. Chunk
// sizes 1, 2 and 4 are the ones that break naive byte-indexed packing, and they
// are exactly the ones that occur under GODEBUG=simd=128 and simd=0.
func TestAppendBitsAtEveryChunkSize(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	const n = 1000
	r := randRef(rng, n)

	for _, chunk := range []int{1, 2, 3, 4, 5, 7, 8, 13, 16, 32, 56, 64} {
		b := bitmap.NewBuilder(n)
		for i := 0; i < n; {
			k := min(chunk, n-i)
			var word uint64
			for j := range k {
				if r[i+j] {
					word |= 1 << uint(j) // LSB-first, matching archsimd ToBits()
				}
			}
			b.AppendBits(word, k)
			i += k
		}
		checkEqual(t, b.Finish(), r, "chunk size")
	}
}

// TestSliceOffsets is the regression test for the Arrow bit-offset trap: a sliced
// bitmap's bits do not start on a byte boundary, and every accessor must account
// for that.
func TestSliceOffsets(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 5))
	const n = 500
	r := randRef(rng, n)
	v := r.view()

	for off := range 70 { // sweep every bit position within the first byte and beyond
		for _, length := range []int{0, 1, 7, 8, 9, 63, 64, 65, 100} {
			if off+length > n {
				continue
			}
			got := v.Slice(off, length)
			checkEqual(t, got, r[off:off+length], "slice")

			// Slicing a slice must compose offsets, not reset them.
			if length >= 10 {
				inner := got.Slice(3, 5)
				checkEqual(t, inner, r[off+3:off+8], "nested slice")
			}
		}
	}
}

func TestAllSet(t *testing.T) {
	v := bitmap.AllSet(100)
	if !v.IsAllSet() {
		t.Error("AllSet should report IsAllSet")
	}
	if v.CountSet() != 100 || v.CountUnset() != 0 {
		t.Errorf("CountSet=%d CountUnset=%d", v.CountSet(), v.CountUnset())
	}
	for i := range 100 {
		if !v.Get(i) {
			t.Fatalf("bit %d not set", i)
		}
	}
	// Slicing the no-storage form stays in the no-storage form.
	s := v.Slice(10, 20)
	if !s.IsAllSet() || s.Len() != 20 {
		t.Error("slicing AllSet should stay AllSet")
	}
	if v.PopCount() != 100 {
		t.Errorf("PopCount = %d (Words iterator is wrong for AllSet)", v.PopCount())
	}
}

func TestCombinators(t *testing.T) {
	rng := rand.New(rand.NewPCG(13, 17))
	const n = 300
	ra, rb := randRef(rng, n), randRef(rng, n)

	// Test at several offsets so the operations are exercised on unaligned inputs,
	// which is the case that actually occurs after a Filter.
	base := randRef(rng, n+40)
	for _, off := range []int{0, 1, 5, 8, 13} {
		full := base.view()
		a := full.Slice(off, n)
		wantA := base[off : off+n]

		b := rb.view()

		gotAnd := bitmap.And(a, b)
		wantAnd := make(ref, n)
		for i := range n {
			wantAnd[i] = wantA[i] && rb[i]
		}
		checkEqual(t, gotAnd, wantAnd, "And")

		gotOr := bitmap.Or(a, b)
		wantOr := make(ref, n)
		for i := range n {
			wantOr[i] = wantA[i] || rb[i]
		}
		checkEqual(t, gotOr, wantOr, "Or")

		gotAndNot := bitmap.AndNot(a, b)
		wantAndNot := make(ref, n)
		for i := range n {
			wantAndNot[i] = wantA[i] && !rb[i]
		}
		checkEqual(t, gotAndNot, wantAndNot, "AndNot")

		gotNot := bitmap.Not(a)
		wantNot := make(ref, n)
		for i := range n {
			wantNot[i] = !wantA[i]
		}
		checkEqual(t, gotNot, wantNot, "Not")
	}
	_ = ra

	// The all-set shortcut must be an identity, not an approximation.
	av := ra.view()
	if got := bitmap.And(bitmap.AllSet(n), av); got.CountSet() != av.CountSet() {
		t.Error("And with AllSet is not an identity")
	}
	if got := bitmap.Or(av, bitmap.AllSet(n)); got.CountSet() != n {
		t.Error("Or with AllSet is not saturating")
	}
	if got := bitmap.AndNot(av, bitmap.AllSet(n)); got.CountSet() != 0 {
		t.Error("AndNot with an all-set mask should clear everything")
	}
}

// TestCombinatorsEndingOnAByteBoundary is the regression test for the defect
// TestCombinators above could not see.
//
// # Why the existing coverage missed it
//
// TestCombinators sweeps offsets {0, 1, 5, 8, 13} at n=300 — genuinely unaligned
// inputs, and it still passed with the bug present. The reason is arithmetic:
// arrow-go's alignedBitmapOp only corrupts the final byte when the range ENDS on
// a byte boundary while starting at a non-zero offset, and (off+300)%8 is 4, 5, 1,
// 4, 1 for those five offsets. Never 0. The bug was one constant away from being
// caught for eighteen steps.
//
// So this sweeps offset × length exhaustively over two full bytes' worth of each,
// which cannot miss a residue class. h2o gb7's 96 wrong rows were offset 8192,
// length 8192 — (8192+8192)%8 == 0, the case nothing exercised.
func TestCombinatorsEndingOnAByteBoundary(t *testing.T) {
	rng := rand.New(rand.NewPCG(29, 31))
	const span = 200
	base := randRef(rng, span)
	other := randRef(rng, span)

	for off := range 17 {
		for ln := 1; ln <= 72; ln++ {
			if off+ln > span {
				continue
			}
			a := base.view().Slice(off, ln)
			b := other.view().Slice(off, ln)
			wa, wb := base[off:off+ln], other[off:off+ln]

			and := make(ref, ln)
			or := make(ref, ln)
			andNot := make(ref, ln)
			not := make(ref, ln)
			for i := range ln {
				and[i] = wa[i] && wb[i]
				or[i] = wa[i] || wb[i]
				andNot[i] = wa[i] && !wb[i]
				not[i] = !wa[i]
			}

			ctx := func(op string) string {
				return op + " off=" + itoa(off) + " len=" + itoa(ln)
			}
			checkEqual(t, bitmap.And(a, b), and, ctx("And"))
			checkEqual(t, bitmap.Or(a, b), or, ctx("Or"))
			checkEqual(t, bitmap.AndNot(a, b), andNot, ctx("AndNot"))
			checkEqual(t, bitmap.Not(a), not, ctx("Not"))

			// Buffer normalises the offset away through bitutil.CopyBitmap, which
			// is the same risk class as the defect this test exists for: another
			// arrow-go entry point that takes an offset and a length. Swept here
			// rather than trusted.
			norm := bitmap.NewView(a.Buffer().Bytes(), 0, ln)
			checkEqual(t, norm, ref(wa), ctx("Buffer"))
		}
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

func TestAndMulti(t *testing.T) {
	rng := rand.New(rand.NewPCG(19, 23))
	const n = 128
	a, b, c := randRef(rng, n), randRef(rng, n), randRef(rng, n)

	got := bitmap.AndMulti(a.view(), bitmap.AllSet(n), b.view(), c.view())
	want := make(ref, n)
	for i := range n {
		want[i] = a[i] && b[i] && c[i]
	}
	checkEqual(t, got, want, "AndMulti")

	// All operands all-set collapses to the no-storage form.
	if !bitmap.AndMulti(bitmap.AllSet(n), bitmap.AllSet(n)).IsAllSet() {
		t.Error("AndMulti over all-set views should stay allocation-free")
	}
}

func TestAppendView(t *testing.T) {
	rng := rand.New(rand.NewPCG(29, 31))
	base := randRef(rng, 200)
	v := base.view()

	// Concatenate three unaligned slices and check the result end to end. This is
	// the operation behind concatenating filtered batches.
	b := bitmap.NewBuilder(0)
	b.AppendView(v.Slice(3, 40))
	b.AppendView(v.Slice(50, 7))
	b.AppendView(v.Slice(100, 65))

	var want ref
	want = append(want, base[3:43]...)
	want = append(want, base[50:57]...)
	want = append(want, base[100:165]...)

	checkEqual(t, b.Finish(), want, "AppendView")
}

func TestBufferNormalisesOffset(t *testing.T) {
	rng := rand.New(rand.NewPCG(37, 41))
	base := randRef(rng, 200)
	v := base.view().Slice(5, 100)

	buf := v.Buffer()
	if buf == nil {
		t.Fatal("Buffer returned nil for a materialised view")
	}
	// The returned buffer must be readable from bit 0 — that is what array.NewData
	// requires, since it applies its own offset separately.
	round := bitmap.NewView(buf.Bytes(), 0, 100)
	checkEqual(t, round, base[5:105], "Buffer")

	if bitmap.AllSet(10).Buffer() != nil {
		t.Error("an all-set view must produce a nil buffer (Arrow omits it)")
	}
}

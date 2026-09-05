package kernel

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
)

// This file is the answer to a profile. ArgSort was 42% of the engine's hot-path
// CPU, of which 99.85% sat inside sort.SliceStable — Go's reflection-swapped,
// symMerge-based stable sort — reached through four closures per comparison.
//
// The last of those closures was the giveaway. OrderKeyF64 turns a float into a
// uint64 whose unsigned ordering IS the documented total order, and it was being
// recomputed on every comparison: O(n log n) calls to answer an O(n) question.
// Computing that key once per row instead turns the whole problem into a radix
// sort, which needs no comparator at all.
//
// # Stability is met by construction rather than defended
//
// ArgSort's doc calls its stability load-bearing three times over — multi-pass
// sorts, reproducible output under ties, and the ArgTopK equivalence. An LSD
// radix sort is stable because each pass distributes into buckets in input
// order, so the property falls out of the algorithm instead of being an
// obligation on it. TestArgTopKMatchesArgSort is what holds that claim to
// account.

// orderKey converts a column into one order-preserving uint64 per row.
//
// The contract is the same one OrderKeyF64 documents: unsigned ordering of the
// result equals ursus's total order over the values. ok is false for types with
// no such encoding — String, Int128 and therefore Decimal — which fall back to
// the comparator path.
//
// Nulls are NOT encoded here. Their slot is undefined and never read: the caller
// partitions them out first, because null placement is a question about position
// rather than about value. See sortPass.
func orderKey(c *data.Column) (keys []uint64, ok bool, err error) {
	n := c.Len()

	// Bool lives in its own payload shape, so it is answered before the physical
	// type switch — the same order valueComparator asks the questions in.
	if c.DType().ID() == dtype.TypeBool {
		bits := c.Bools()
		keys = make([]uint64, n)
		for i := range n {
			if bits.Get(i) {
				keys[i] = 1 // false < true
			}
		}
		return keys, true, nil
	}
	if c.DType().HasStringStorage() {
		return nil, false, nil
	}

	switch c.DType().Physical().ID() {
	case dtype.TypeInt8:
		return signedKeys[int8](c, n)
	case dtype.TypeInt16:
		return signedKeys[int16](c, n)
	case dtype.TypeInt32:
		return signedKeys[int32](c, n)
	case dtype.TypeInt64:
		return signedKeys[int64](c, n)

	case dtype.TypeUint8:
		return unsignedKeys[uint8](c, n)
	case dtype.TypeUint16:
		return unsignedKeys[uint16](c, n)
	case dtype.TypeUint32:
		return unsignedKeys[uint32](c, n)
	case dtype.TypeUint64:
		return unsignedKeys[uint64](c, n)

	case dtype.TypeFloat32:
		v, err := data.Values[float32](c)
		if err != nil {
			return nil, false, err
		}
		keys = make([]uint64, n)
		for i := range n {
			// Widened, not shifted: the 32-bit key already orders correctly and
			// zero-extending preserves that.
			keys[i] = uint64(OrderKeyF32(v[i]))
		}
		return keys, true, nil

	case dtype.TypeFloat64:
		v, err := data.Values[float64](c)
		if err != nil {
			return nil, false, err
		}
		keys = make([]uint64, n)
		for i := range n {
			keys[i] = OrderKeyF64(v[i])
		}
		return keys, true, nil
	}
	return nil, false, nil
}

// signedKeys widens to int64 and flips the sign bit.
//
// Widening first is what makes one expression serve all four widths: the value
// order of an int8 is preserved by sign extension, and flipping the top bit maps
// [-2^63, 2^63) onto [0, 2^64) monotonically.
func signedKeys[T int8 | int16 | int32 | int64](c *data.Column, n int) ([]uint64, bool, error) {
	v, err := data.Values[T](c)
	if err != nil {
		return nil, false, err
	}
	keys := make([]uint64, n)
	for i := range n {
		keys[i] = uint64(int64(v[i])) ^ (1 << 63)
	}
	return keys, true, nil
}

func unsignedKeys[T uint8 | uint16 | uint32 | uint64](c *data.Column, n int) ([]uint64, bool, error) {
	v, err := data.Values[T](c)
	if err != nil {
		return nil, false, err
	}
	keys := make([]uint64, n)
	for i := range n {
		keys[i] = uint64(v[i])
	}
	return keys, true, nil
}

// radixable reports whether every key column can be sorted without a comparator.
//
// All or nothing: a mixed set falls back wholesale, because an LSD pass over one
// column has to be a stable sort of the WHOLE array by that column, and a
// comparator pass interleaved with radix passes would not compose.
func radixable(cols []*data.Column) bool {
	for _, c := range cols {
		// An all-null column carries NO payload — data.NewNull's whole point, and
		// reachable from Take and Select. NewComparator already refuses it with
		// "has no fixed-width payload", so a radix path that happily sorted it
		// would quietly make this fast path MORE capable than the one it replaces
		// and change what a query does. Declining keeps the two identical; whether
		// the refusal is right at all is a separate question from this one.
		if c.IsPayloadFree() {
			return false
		}
		if c.DType().ID() == dtype.TypeBool {
			continue
		}
		if c.DType().HasStringStorage() {
			return false
		}
		switch c.DType().Physical().ID() {
		case dtype.TypeInt8, dtype.TypeInt16, dtype.TypeInt32, dtype.TypeInt64,
			dtype.TypeUint8, dtype.TypeUint16, dtype.TypeUint32, dtype.TypeUint64,
			dtype.TypeFloat32, dtype.TypeFloat64:
		default:
			return false
		}
	}
	return true
}

// radixSorter owns the scratch buffers, which are reused across column passes.
//
// scratch and keys are needed by every sort; the other three exist only to split
// nulls out and put them back, so they are allocated on first use. A sort with no
// nulls anywhere — the common one — therefore pays for one extra permutation
// buffer and one key buffer rather than four permutation buffers, which on a
// million rows is 12 MB against 16.
type radixSorter struct {
	scratch []int32
	keys    []uint64 // keys gathered into permutation order; see radixByKey

	nonNull []int32
	nulls   []int32
	out     []int32
}

// keyBuf returns the gathered-key buffer, grown as needed and reused across
// column passes exactly as scratch is.
func (r *radixSorter) keyBuf(n int) []uint64 {
	if cap(r.keys) < n {
		r.keys = make([]uint64, n)
	}
	return r.keys[:n]
}

// needNullBuffers allocates the null-partition scratch the first time a column
// with nulls is reached.
func (r *radixSorter) needNullBuffers(n int) {
	if r.out != nil {
		return
	}
	r.nonNull = make([]int32, 0, n)
	r.nulls = make([]int32, 0, n)
	r.out = make([]int32, 0, n)
}

// argSortRadix sorts n rows by cols under specs, with no comparisons.
//
// # Columns are processed LAST key first
//
// Each pass is a stable sort of the whole permutation by ONE column, so applying
// them from the least significant key to the most significant leaves the array in
// lexicographic order — the same LSD argument that makes the byte passes work,
// one level up. Doing it first-to-last instead would leave the array sorted by
// the last key with the earlier ones as tiebreakers, which is backwards.
//
// This is what makes the window case work at all: windowSink.segments always
// sorts on the partition id plus an order key, and it was 60% of ArgSort's time.
func argSortRadix(cols []*data.Column, specs []SortSpec, n int) ([]int32, error) {
	idx := make([]int32, n)
	for i := range idx {
		idx[i] = int32(i)
	}
	if n < 2 || len(cols) == 0 {
		return idx, nil
	}

	r := &radixSorter{scratch: make([]int32, n)}

	for k := len(cols) - 1; k >= 0; k-- {
		keys, ok, err := orderKey(cols[k])
		if err != nil {
			return nil, err
		}
		if !ok {
			// radixable was consulted first, so this is unreachable; returning
			// rather than panicking keeps a future type addition from crashing a
			// query before anyone has written its key extractor.
			return nil, nil
		}
		if specs[k].Descending {
			// Inverting every bit reverses the order EXACTLY and leaves equal keys
			// equal, so descending needs no second code path and costs no
			// stability. Applied to the value key only — null placement is
			// independent of direction, which is what nullRule's doc insists on.
			for i := range keys {
				keys[i] = ^keys[i]
			}
		}
		r.sortPass(idx, keys, cols[k], specs[k].NullsLast)
	}
	return idx, nil
}

// sortPass stably sorts idx by one column, IN PLACE from the caller's view.
//
// radixByKey returns whichever of its two buffers the last round wrote into, so
// the result is copied back into idx before returning. That one O(n) copy per
// column is invisible next to the rounds themselves, and it means the caller
// always owns exactly one live permutation buffer — without it, a pass that ran
// an odd number of rounds would hand back the scratch buffer and the next pass
// would be given the same slice as both source and destination.
//
// Nulls are partitioned rather than compared. withNulls applies placement BEFORE
// direction — so NullsLast means the same thing ascending and descending — and a
// partition reproduces that exactly, without needing a 65th bit in the key to
// hold a value that sorts below or above everything.
func (r *radixSorter) sortPass(idx []int32, keys []uint64, c *data.Column, nullsLast bool) {
	if c.NullCount() == 0 {
		// The common case, and worth its own branch: a partition that separates
		// nothing still walks and copies the whole permutation.
		copy(idx, r.radixByKey(idx, r.scratch, keys))
		return
	}

	r.needNullBuffers(len(idx))
	valid := c.Validity()
	r.nonNull, r.nulls = r.nonNull[:0], r.nulls[:0]
	for _, row := range idx {
		if valid.Get(int(row)) {
			r.nonNull = append(r.nonNull, row)
		} else {
			r.nulls = append(r.nulls, row)
		}
	}
	sorted := r.radixByKey(r.nonNull, r.scratch[:len(r.nonNull)], keys)

	out := r.out[:0]
	if nullsLast {
		out = append(out, sorted...)
		out = append(out, r.nulls...)
	} else {
		out = append(out, r.nulls...)
		out = append(out, sorted...)
	}
	r.out = out[:0]
	copy(idx, out)
}

// radixByKey is the LSD byte-wise pass: eight rounds of 256 buckets.
//
// Returns either src or dst depending on how many rounds ran, which is why the
// caller must treat the return value as the live buffer and the argument as
// scrap.
//
// # The keys travel WITH their rows
//
// `keys` is indexed by ROW and `src` is a permutation, so the obvious loop body —
//
//	d := byte(keys[row] >> (8 * b))
//
// — is a RANDOM read into an n*8-byte array. On the first round the permutation is
// the identity and that read is sequential; from the second round on it is
// scattered, over an array that is 80 MB at ten million rows. A two-key window
// sort runs ten or eleven rounds and all but the first read that way, which made
// this loop the single hottest thing in the engine: 0.52s of the 0.62s
// windowSink costs, with the group-id work it sits next to at 0.05s.
//
// So the keys are gathered into src order ONCE, and from then on each round reads
// them sequentially and scatters the key alongside the row it belongs to. A fully
// random read becomes a sequential read plus a second bucketed write, and a
// bucketed write touches 256 active cache lines where the read touched n.
//
// The second key buffer is `keys` ITSELF. Nothing reads it in row order after the
// gather, and argSortRadix allocates a fresh one per column, so it is free to be
// overwritten — worth saying out loud, because it is the one thing here that
// would surprise a reader.
func (r *radixSorter) radixByKey(src, dst []int32, keys []uint64) []int32 {
	n := len(src)
	if n < 2 {
		return src
	}

	ka, kb := r.keyBuf(n), keys[:n]
	for i, row := range src {
		ka[i] = keys[row]
	}

	// All eight histograms in ONE pass over the data. Eight separate passes would
	// read the key array eight times for no reason.
	var hist [8][256]int32
	for _, k := range ka {
		for b := range 8 {
			hist[b][byte(k>>(8*b))]++
		}
	}

	for b := range 8 {
		// A byte that takes one value everywhere sorts nothing. Skipping it is
		// what makes a narrow key cheap: an Int32 partition id spends two rounds
		// here rather than eight, and a Bool spends one.
		//
		// A skipped round must not swap the key buffers either. ka is indexed by
		// POSITION now, so if it were swapped while src was not, every later round
		// would read another row's key — silently, and only for keys that happen to
		// have a uniform byte in the middle.
		if hist[b][byte(ka[0]>>(8*b))] == int32(n) {
			continue
		}

		var off [256]int32
		sum := int32(0)
		for v := range 256 {
			off[v] = sum
			sum += hist[b][v]
		}
		// Distributing in INPUT ORDER is the whole stability argument: equal bytes
		// land in the bucket in the order they were read.
		for i, row := range src {
			d := byte(ka[i] >> (8 * b))
			dst[off[d]] = row
			kb[off[d]] = ka[i]
			off[d]++
		}
		src, dst = dst, src
		ka, kb = kb, ka
	}
	return src
}

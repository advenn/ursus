package kernel

import (
	"bytes"
	"encoding/binary"
)

// KeyTable maps encoded group keys to dense int32 ids, in first-seen order.
//
// It replaces the `map[string]int32` that six operators used to identify groups:
// the hash aggregate and its spilling twin, both join build sinks, and the window
// sink's partitioning. A profile of a high-cardinality string group-by put 74% of
// hashAggSink.Consume inside that map — 61% in mapaccess2_faststr and 13% in
// mapassign_faststr — against 6% encoding the key and 5% doing the actual
// arithmetic.
//
// # Why a map was the wrong shape, specifically
//
// Every call site wanted get-or-insert:
//
//	id, seen := ids[string(k)]
//	if !seen { id = int32(len(ids)); ids[string(k)] = id }
//
// Go has no fused form, so a new key is hashed and probed TWICE. On a
// high-cardinality group-by, where misses are the common case, that is half the
// work thrown away. A probe that missed here already knows the slot to fill.
//
// Two more differences, in the order they mattered:
//
//   - The hash is stored per id, so a probe rejects on eight bytes before it
//     touches the key. That is aimed at the memeqbody time the map spent
//     comparing keys in full.
//   - Keys live in one arena rather than an individually allocated string each,
//     so comparison reads contiguous memory and insertion does not allocate.
//
// # The arena is chunks, and the per-key arrays double
//
// The arena was one []byte grown by append, and so were hashes and offsets. Past
// 256 elements append adds about a quarter at a time, so the allocations on the way
// to a final size F summed to about 5F, and each growth held the old array beside
// the new one while it copied, the old one uncounted. h2o gb10, with one group per
// row, spent half of everything it allocated there: 11.5 GB of 22.9 (step 132).
//
// The arena is now chunks that never move, so it allocates about F and holds no
// second copy. A key never straddles two chunks; one longer than a chunk gets a
// chunk of its own. hashes and refs, indexed by id on every probe, stay flat arrays
// and double, about 2F.
//
// # Ids are dense and first-seen ordered, and that is load-bearing
//
// id N is the Nth DISTINCT key handed to GetOrInsert. Accumulators index their
// storage by it, hashAggSink.firstSeen records an input ordinal per id, and both
// Merge implementations build a remap over it. Assigning ids by slot index
// instead would be silently wrong everywhere at once, which is what the
// differential test against map[string]int32 exists to catch.
//
// KeyAt reads back by id, so the table's internal order is never observable.
//
// # Not safe for concurrent use, except Get
//
// GetOrInsert mutates. Get touches nothing, which is what lets joinTable keep its
// promise that nothing changes after the build freezes — the property that would
// let several probe workers share one table with no lock.
type KeyTable struct {
	// slots is the open-addressed index: a power-of-two ring, 0 for empty, and
	// otherwise a key's id+1 in the low 32 bits under the top 32 bits of its hash,
	// its tag. Linear probing, because the load factor is capped low enough that
	// clusters stay short and a linear walk is friendlier to the cache than any
	// scheme that jumps.
	//
	// The tag is what keeps the walk in the ring. A probe passes several occupied
	// slots on its way to its own, near the 3/4 cap about eight for a new key, and
	// each was checked against hashes[id], an array indexed by id: a read at a random
	// place, a cache miss, per slot passed. A 1.5-million-key join build spent about
	// 170 ns an insert there (step 126). The tag rejects nearly every other key's
	// slot from the ring itself, which is read in order.
	slots []uint64
	mask  uint64

	// Per id, parallel:
	hashes []uint64 // lets grow rehash without re-reading a single key
	// refs has n+1 entries, the first 0: refs[i+1] is where key i ENDS, its chunk
	// above refShift and its offset in the chunk below. Key i starts where key i-1
	// ended if that was in the same chunk, and at the chunk's start if not: keys
	// are stored in id order, and a key that does not fit starts a new chunk.
	//
	// 64-bit, because the arena holds every distinct key of the query: a group-by
	// over enough distinct strings passes 2 GiB, where int32 offsets wrapped and a
	// key was read from the wrong bytes (audit.md S24).
	refs []uint64

	// chunks is the arena. Each is filled up to its capacity and never grown, so a
	// key's bytes never move. chunkBytes is their capacities' sum; nextChunk is the
	// size of the next one, doubling from firstChunk to maxChunk.
	chunks     [][]byte
	chunkBytes int64
	nextChunk  int

	// touched holds touch's sum, which nothing reads.
	touched uint64
}

// initialSlots is small because most group-bys are small, and growth is cheap:
// rehashing reads the stored hashes rather than the keys.
const initialSlots = 64

// The arena's chunk sizes: small first, doubling to maxChunk. A chunk is allocated
// whole, so at most one is part empty, besides what a key too long for the rest of
// one leaves.
//
// Small first, because most tables are small, and some run under tiny limits: a
// spilling group-by's sub-sinks each hold a key or a few, under the 2 KiB limits
// its tests use. With a first chunk of 1 KiB, and hashes made for 64 keys, a
// one-key table counted 2.5 KiB where it had counted 1, and those sub-sinks
// partitioned to maxSpillDepth (step 133). It counts 1.2 now.
const (
	firstChunk = 64
	maxChunk   = 1 << 20
)

// firstIDs is the capacity hashes starts at, for the same reason.
const firstIDs = 8

// refShift splits a ref: the chunk above, the offset in it below. 2^24 chunks of up
// to 2^40 bytes.
const (
	refShift = 40
	refMask  = 1<<refShift - 1
)

// maxLoadNum/maxLoadDen cap the load factor at 3/4. Past that, linear probing's
// cluster lengths grow fast enough to undo the advantage over a bucketed map.
const (
	maxLoadNum = 3
	maxLoadDen = 4
)

func NewKeyTable() *KeyTable {
	t := &KeyTable{}
	t.init(initialSlots)
	return t
}

func (t *KeyTable) init(n int) {
	t.slots = make([]uint64, n)
	t.mask = uint64(n - 1)
	// The leading zero, planted once, so key id ends at refs[id+1] and starts by
	// refs[id] with no fixup, and Len is len(refs)-1 with no guard.
	t.refs = make([]uint64, 1, 1+initialSlots)
}

// Len is the number of distinct keys, which is also the next id GetOrInsert will
// hand out.
func (t *KeyTable) Len() int {
	if t.refs == nil {
		return 0
	}
	return len(t.refs) - 1
}

// GetOrInsert returns the id for key, inserting it if this is its first sighting.
// inserted reports which happened.
//
// key is only read, never retained: the bytes are copied into the arena on
// insert. That matters because every caller passes GroupKeyEncoder.Encode's
// return value, which aliases a buffer the encoder reuses on the next row.
func (t *KeyTable) GetOrInsert(key []byte) (id int32, inserted bool) {
	if t.slots == nil {
		t.init(initialSlots)
	}
	return t.getOrInsert(key, probeHash(key))
}

// ManyChunk is the most keys GetOrInsertMany and GetMany take at once.
const ManyChunk = 64

// GetOrInsertMany is GetOrInsert for up to ManyChunk keys, in order: ids[j] and
// inserted[j] answer keys[j], exactly as calling GetOrInsert on each in turn would.
//
// # Why a batch
//
// Past the size of the cache, an insert waits on main memory for its first slot,
// and a lookup does: in a million-key table that one read was two thirds of an
// insert's time. One key at a time, each waits in turn. Here the keys' slots are
// read first, in a loop whose reads do not depend on one another, so the processor
// has many in flight at once; the inserts that follow find them in cache.
func (t *KeyTable) GetOrInsertMany(keys [][]byte, ids []int32, inserted []bool) {
	if t.slots == nil {
		t.init(initialSlots)
	}
	var hs [ManyChunk]uint64
	for j, k := range keys {
		hs[j] = probeHash(k)
	}
	t.touch(hs[:len(keys)])
	for j, k := range keys {
		ids[j], inserted[j] = t.getOrInsert(k, hs[j])
	}
}

// GetMany is Get for up to ManyChunk keys, with GetOrInsertMany's reason: ids[j] is
// keys[j]'s id, or -1, and found[j] says which.
//
// Like Get it writes nothing to the table, so probe workers can share one. The
// first slot of every key is read into first, and each lookup starts from it: the
// reads are used, so nothing has to be stored to keep them. Storing their sum in the
// table, as GetOrInsertMany does, was a data race between probe workers, which the
// race detector found in the gate (step 126).
func (t *KeyTable) GetMany(keys [][]byte, ids []int32, found []bool) {
	if t.slots == nil {
		for j := range keys {
			ids[j], found[j] = -1, false
		}
		return
	}
	var hs, first [ManyChunk]uint64
	for j, k := range keys {
		hs[j] = probeHash(k)
	}
	for j, h := range hs[:len(keys)] {
		first[j] = t.slots[h&t.mask]
	}
	for j, k := range keys {
		ids[j], found[j] = t.getFrom(k, hs[j], first[j])
	}
}

// touch reads each hash's first slot. The reads are independent, which is the
// point; the sum is kept only so the compiler cannot drop them. It writes the table,
// as an insert does, so it serves GetOrInsertMany and not GetMany.
func (t *KeyTable) touch(hs []uint64) {
	var sum uint64
	for _, h := range hs {
		sum += t.slots[h&t.mask]
	}
	t.touched = sum
}

func (t *KeyTable) getOrInsert(key []byte, h uint64) (id int32, inserted bool) {
	tag := h >> 32
	for i := h & t.mask; ; i = (i + 1) & t.mask {
		s := t.slots[i]
		if s == 0 {
			id = int32(len(t.refs) - 1)
			t.hashes = pushDoubling(t.hashes, h)
			t.refs = pushDoubling(t.refs, t.store(key))
			t.slots[i] = slotOf(h, id)
			if (len(t.refs)-1)*maxLoadDen >= len(t.slots)*maxLoadNum {
				t.grow()
			}
			return id, true
		}
		if s>>32 != tag {
			continue
		}
		got := int32(uint32(s)) - 1
		// The tag has already rejected every key whose hash differs in its top 32
		// bits, which is all but about one in four billion. bytes.Equal decides the
		// rest, and is load-bearing: the alternative is two groups silently merged.
		// Comparing hashes[got] first used to sit here, and now would cost a read at
		// a random place on every match to reject almost nothing.
		if bytes.Equal(t.keyAt(got), key) {
			return got, false
		}
	}
}

// Get looks up a key without inserting it. It mutates nothing, so a frozen table
// may be read from several goroutines at once.
func (t *KeyTable) Get(key []byte) (id int32, ok bool) {
	if t.slots == nil {
		return -1, false
	}
	return t.get(key, probeHash(key))
}

func (t *KeyTable) get(key []byte, h uint64) (id int32, ok bool) {
	return t.getFrom(key, h, t.slots[h&t.mask])
}

// getFrom is get, given the key's first slot word already read.
func (t *KeyTable) getFrom(key []byte, h, s uint64) (id int32, ok bool) {
	tag := h >> 32
	for i := h & t.mask; ; {
		if s == 0 {
			return -1, false
		}
		if s>>32 == tag {
			got := int32(uint32(s)) - 1
			if bytes.Equal(t.keyAt(got), key) {
				return got, true
			}
		}
		i = (i + 1) & t.mask
		s = t.slots[i]
	}
}

// slotOf is a slot's word for key id with hash h: its tag above, id+1 below, so
// that no occupied slot is 0.
func slotOf(h uint64, id int32) uint64 { return h>>32<<32 | uint64(uint32(id+1)) }

// KeyAt returns the key bytes for an id. The result aliases the arena, so a caller
// that keeps it past Reset must copy. A later insert does not move it: chunks never
// move.
//
// This is what lets both Merge implementations walk their keys in id order. They
// used to invert the map into a []string first, purely because a Go map cannot be
// indexed by value; here it is slice arithmetic.
func (t *KeyTable) KeyAt(id int32) []byte { return t.keyAt(id) }

func (t *KeyTable) keyAt(id int32) []byte {
	start, end := t.refs[id], t.refs[id+1]
	c := end >> refShift
	var from uint64
	if start>>refShift == c {
		from = start & refMask
	}
	return t.chunks[c][from : end&refMask]
}

// store copies key into the arena and returns the ref of its end. The first key,
// even an empty one, makes the first chunk, so keyAt always has one to read.
func (t *KeyTable) store(key []byte) uint64 {
	n := len(t.chunks)
	if n == 0 || len(key) > cap(t.chunks[n-1])-len(t.chunks[n-1]) {
		size := max(t.nextChunk, firstChunk)
		t.nextChunk = min(2*size, maxChunk)
		// A key longer than the next chunk gets one of exactly its size, and the
		// doubling carries on from where it was.
		size = max(size, len(key))
		t.chunks = append(t.chunks, make([]byte, 0, size))
		t.chunkBytes += int64(size)
		n++
	}
	c := append(t.chunks[n-1], key...)
	t.chunks[n-1] = c
	return uint64(n-1)<<refShift | uint64(len(c))
}

// pushDoubling appends v, doubling the capacity when it is full. append grows a
// large slice by about a quarter at a time, which allocates about five times the
// final size on the way there; doubling, about twice.
func pushDoubling[T any](s []T, v T) []T {
	if len(s) == cap(s) {
		g := make([]T, len(s), max(2*cap(s), firstIDs))
		copy(g, s)
		s = g
	}
	return append(s, v)
}

// NBytes is what this table holds, EXACTLY.
//
// The map it replaced could not answer this — join.go carried a 48-bytes-per-key
// estimate covering a string header, a value, a tophash byte and Go's load-factor
// slack, and said so. A memory limit is enforced against this number, so an
// estimate spills too early or too late; four slices can simply be measured.
func (t *KeyTable) NBytes() int64 {
	return int64(cap(t.slots))*8 +
		int64(cap(t.hashes))*8 +
		int64(cap(t.refs))*8 +
		t.chunkBytes +
		int64(cap(t.chunks))*24 // the chunks' slice headers
}

// Reset empties the table and releases its memory, for the repartitioning paths
// that used to assign a fresh map. Capacity is deliberately NOT kept: repartition
// is what runs when memory is already short.
func (t *KeyTable) Reset() { *t = KeyTable{} }

// grow doubles the slot ring and re-places every id from the stored hashes. No
// key is read and no key is moved — the arena and refs are untouched, so every id
// keeps its meaning.
// Reserve sizes the table for at least n keys, if it is not already that big.
//
// Growth is doubling from 64 slots and each doubling REHASHES every entry, so a
// table that ends up with millions of keys pays about seventeen full rehashes to get
// there — random probes, every time. A caller that knows its rough scale up front can
// skip almost all of it.
//
// It is a hint: the table still grows on demand, and passing a number that turns out
// too small costs nothing but the doublings it did not save.
func (t *KeyTable) Reserve(n int) {
	if n <= 0 {
		return
	}
	// The same 3/4 load factor GetOrInsert grows at, so a table reserved for n keys
	// does not immediately grow on the nth insert.
	want := 1
	for want < n*4/3 {
		want *= 2
	}
	if t.slots == nil {
		t.init(want)
		return
	}
	for len(t.slots) < want {
		t.grow()
	}
}

func (t *KeyTable) grow() {
	slots := make([]uint64, len(t.slots)*2)
	mask := uint64(len(slots) - 1)
	for id, h := range t.hashes {
		for i := h & mask; ; i = (i + 1) & mask {
			if slots[i] == 0 {
				slots[i] = slotOf(h, int32(id))
				break
			}
		}
	}
	t.slots, t.mask = slots, mask
}

// probeHash hashes a key to choose a slot. It reads eight bytes at a time.
//
// # Deliberately NOT HashKey
//
// HashKey is the obvious thing to reach for and would be a regression: it is
// FNV-1a byte at a time, one multiply per byte, while the Go map this replaces
// hashes with hardware AES. Reusing it would hand back much of what the fused
// get-or-insert wins.
//
// The two hashes answer different questions, and conflating them would be a
// correctness bug rather than a style choice:
//
//   - HashKey decides which SPILL FILE a key is routed to (partitionOf). It must
//     be seedless and stable across processes, because a key written to partition
//     p has to be looked for in partition p after a reload.
//   - probeHash decides only which slot to probe first. Ids come from first-seen
//     order and KeyAt reads back by id, so nothing outside this file can observe
//     which slot anything landed in, and the hash is free to be chosen for speed.
//
// The length is mixed in, so keys that are prefixes of one another start in
// different places — though correctness rests on bytes.Equal, not on that.
func probeHash(b []byte) uint64 {
	const (
		m1 = 0xff51afd7ed558ccd
		m2 = 0xc4ceb9fe1a85ec53
	)
	h := uint64(len(b)) ^ 0x9E3779B97F4A7C15
	for len(b) >= 8 {
		h ^= binary.LittleEndian.Uint64(b)
		h *= m1
		h ^= h >> 29
		b = b[8:]
	}
	if len(b) > 0 {
		var tail uint64
		for _, c := range b {
			tail = tail<<8 | uint64(c)
		}
		h ^= tail
		h *= m2
	}
	return Mix64(h)
}

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
	// slots is the open-addressed index: a power-of-two ring of key ids, with -1
	// for empty. Linear probing, because the load factor is capped low enough that
	// clusters stay short and a linear walk is friendlier to the cache than any
	// scheme that jumps.
	slots []int32
	mask  uint64

	// Per id, parallel:
	hashes []uint64 // lets grow rehash without re-reading a single key
	offs   []int32  // n+1 entries; key i is arena[offs[i]:offs[i+1]]
	arena  []byte
}

// initialSlots is small because most group-bys are small, and growth is cheap:
// rehashing reads the stored hashes rather than the keys.
const initialSlots = 64

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
	t.slots = make([]int32, n)
	for i := range t.slots {
		t.slots[i] = -1
	}
	t.mask = uint64(n - 1)
	// The leading zero, planted once, so KeyAt is offs[id]:offs[id+1] with no
	// fixup and Len is len(offs)-1 with no guard.
	t.offs = make([]int32, 1, 1+initialSlots)
}

// Len is the number of distinct keys, which is also the next id GetOrInsert will
// hand out.
func (t *KeyTable) Len() int {
	if t.offs == nil {
		return 0
	}
	return len(t.offs) - 1
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
	h := probeHash(key)
	for i := h & t.mask; ; i = (i + 1) & t.mask {
		got := t.slots[i]
		if got < 0 {
			id = int32(len(t.offs) - 1)
			t.hashes = append(t.hashes, h)
			t.arena = append(t.arena, key...)
			t.offs = append(t.offs, int32(len(t.arena)))
			t.slots[i] = id
			if (len(t.offs)-1)*maxLoadDen >= len(t.slots)*maxLoadNum {
				t.grow()
			}
			return id, true
		}
		// The hash comparison is what actually separates keys here: slot collisions
		// are common (the mask is small), full 64-bit hash collisions are not.
		// bytes.Equal is the backstop for the case that does not happen — and it is
		// load-bearing anyway, because the alternative is two groups silently merged.
		//
		// It is also, honestly, unreachable by any test: probeHash mixes in the
		// length, so even "ab" against "ab\x00" is rejected by the hash. Replacing
		// this with a prefix comparison leaves the whole suite green.
		if t.hashes[got] == h && bytes.Equal(t.keyAt(got), key) {
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
	h := probeHash(key)
	for i := h & t.mask; ; i = (i + 1) & t.mask {
		got := t.slots[i]
		if got < 0 {
			return -1, false
		}
		if t.hashes[got] == h && bytes.Equal(t.keyAt(got), key) {
			return got, true
		}
	}
}

// KeyAt returns the key bytes for an id. The result aliases the arena and is
// invalidated by the next insert, so a caller that keeps it must copy.
//
// This is what lets both Merge implementations walk their keys in id order. They
// used to invert the map into a []string first, purely because a Go map cannot be
// indexed by value; here it is slice arithmetic.
func (t *KeyTable) KeyAt(id int32) []byte { return t.keyAt(id) }

func (t *KeyTable) keyAt(id int32) []byte {
	return t.arena[t.offs[id]:t.offs[id+1]]
}

// NBytes is what this table holds, EXACTLY.
//
// The map it replaced could not answer this — join.go carried a 48-bytes-per-key
// estimate covering a string header, a value, a tophash byte and Go's load-factor
// slack, and said so. A memory limit is enforced against this number, so an
// estimate spills too early or too late; four slices can simply be measured.
func (t *KeyTable) NBytes() int64 {
	return int64(cap(t.slots))*4 +
		int64(cap(t.hashes))*8 +
		int64(cap(t.offs))*4 +
		int64(cap(t.arena))
}

// Reset empties the table and releases its memory, for the repartitioning paths
// that used to assign a fresh map. Capacity is deliberately NOT kept: repartition
// is what runs when memory is already short.
func (t *KeyTable) Reset() { *t = KeyTable{} }

// grow doubles the slot ring and re-places every id from the stored hashes. No
// key is read and no key is moved — the arena and offs are untouched, so every id
// keeps its meaning.
func (t *KeyTable) grow() {
	slots := make([]int32, len(t.slots)*2)
	for i := range slots {
		slots[i] = -1
	}
	mask := uint64(len(slots) - 1)
	for id, h := range t.hashes {
		for i := h & mask; ; i = (i + 1) & mask {
			if slots[i] < 0 {
				slots[i] = int32(id)
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

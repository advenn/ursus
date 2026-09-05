package kernel_test

// KeyTable replaced a map[string]int32 in six operators, and its whole
// correctness argument is that it is indistinguishable from that map: the same
// sequence of keys must produce the same sequence of ids, not merely a consistent
// grouping. So the central test is a differential, the same shape radix_test.go
// uses against the comparator sort.
//
// Ids being dense and first-seen ordered is what accumulator storage,
// hashAggSink.firstSeen and both Merge remaps are indexed by, so it is the
// property most worth pinning and the one hardest to notice breaking.

import (
	"bytes"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus/internal/kernel"
)

// TestKeyTableMatchesTheMap is the differential.
//
// Keys repeat heavily on purpose: a corpus of distinct keys would pass with an
// implementation that never looked anything up.
func TestKeyTableMatchesTheMap(t *testing.T) {
	rng := rand.New(rand.NewPCG(11, 23))

	// A small alphabet of key shapes, drawn with replacement, so hits vastly
	// outnumber misses after the first few hundred rows — the group-by case.
	var corpus [][]byte
	for i := range 200 {
		k := make([]byte, 1+rng.IntN(20))
		for j := range k {
			// Includes 0x00: the group-key encoder writes it as the null tag, so
			// keys are not NUL-safe and an implementation that treats them as C
			// strings truncates.
			k[j] = byte(rng.IntN(4))
		}
		if i%17 == 0 {
			k = k[:0] // the empty key, which is the no-partition-key group
		}
		corpus = append(corpus, k)
	}

	tab := kernel.NewKeyTable()
	ids := map[string]int32{}

	for i := range 20_000 {
		key := corpus[rng.IntN(len(corpus))]

		wantID, seen := ids[string(key)]
		if !seen {
			wantID = int32(len(ids))
			ids[string(key)] = wantID
		}

		gotID, inserted := tab.GetOrInsert(key)
		if gotID != wantID {
			t.Fatalf("row %d, key %q: id = %d, map says %d", i, key, gotID, wantID)
		}
		if inserted == seen {
			t.Fatalf("row %d, key %q: inserted = %v, but the map had it = %v",
				i, key, inserted, seen)
		}
		if tab.Len() != len(ids) {
			t.Fatalf("row %d: Len = %d, map has %d", i, tab.Len(), len(ids))
		}
	}

	// Every key must read back by id. This is the arena's own invariant, and it is
	// what both Merge implementations rely on now that they no longer invert a map
	// into a []string.
	for k, id := range ids {
		if got := tab.KeyAt(id); !bytes.Equal(got, []byte(k)) {
			t.Errorf("KeyAt(%d) = %q, want %q", id, got, k)
		}
	}
}

// TestKeyTableGrowthPreservesEverything: growth rehashes from the stored hashes
// rather than from the keys, so a slip there loses ids without touching the arena
// — the table would keep the right BYTES under the wrong numbers.
//
// The count is far past the 64-slot start, so this crosses many doublings.
func TestKeyTableGrowthPreservesEverything(t *testing.T) {
	const n = 5000
	tab := kernel.NewKeyTable()

	key := func(i int) []byte { return []byte("key-" + itoa(i)) }

	for i := range n {
		id, inserted := tab.GetOrInsert(key(i))
		if !inserted || id != int32(i) {
			t.Fatalf("insert %d: id = %d, inserted = %v", i, id, inserted)
		}
	}
	if tab.Len() != n {
		t.Fatalf("Len = %d, want %d", tab.Len(), n)
	}

	// Every key still resolves to its original id, and still reads back.
	for i := range n {
		id, inserted := tab.GetOrInsert(key(i))
		if inserted {
			t.Fatalf("key %d was re-inserted after growth", i)
		}
		if id != int32(i) {
			t.Fatalf("key %d: id = %d after growth, want %d", i, id, i)
		}
		if got := tab.KeyAt(id); !bytes.Equal(got, key(i)) {
			t.Fatalf("KeyAt(%d) = %q, want %q", id, got, key(i))
		}
	}
}

// TestKeyTablePrefixKeysAreDistinct is the case a length-blind comparison merges.
//
// "ab" and "ab\x00" differ only by a trailing NUL, which is exactly what the
// encoder appends for a null field — so conflating them would merge a group
// having a null with one that does not.
func TestKeyTablePrefixKeysAreDistinct(t *testing.T) {
	keys := [][]byte{
		{},
		{0x00},
		{0x00, 0x00},
		[]byte("ab"),
		[]byte("ab\x00"),
		[]byte("ab\x00\x00"),
		[]byte("abc"),
	}

	tab := kernel.NewKeyTable()
	seen := map[int32][]byte{}
	for _, k := range keys {
		id, inserted := tab.GetOrInsert(k)
		if !inserted {
			t.Fatalf("key %q collided with %q — they must be distinct groups",
				k, seen[id])
		}
		seen[id] = bytes.Clone(k)
	}
	if tab.Len() != len(keys) {
		t.Errorf("Len = %d, want %d distinct keys", tab.Len(), len(keys))
	}
}

// TestKeyTableGetDoesNotInsert: the probe side of a join calls Get on a frozen
// table, and joinTable's doc promises nothing mutates after the build finishes —
// the property that would let probe workers share one table without a lock. A Get
// that inserted would also invent build rows that do not exist.
func TestKeyTableGetDoesNotInsert(t *testing.T) {
	tab := kernel.NewKeyTable()
	tab.GetOrInsert([]byte("present"))

	before, beforeBytes := tab.Len(), tab.NBytes()

	if id, ok := tab.Get([]byte("absent")); ok {
		t.Errorf("Get on an absent key returned id %d, ok=true", id)
	}
	if id, ok := tab.Get([]byte("present")); !ok || id != 0 {
		t.Errorf("Get on a present key = (%d, %v), want (0, true)", id, ok)
	}

	if tab.Len() != before {
		t.Errorf("Get changed Len from %d to %d", before, tab.Len())
	}
	if tab.NBytes() != beforeBytes {
		t.Errorf("Get changed NBytes from %d to %d", beforeBytes, tab.NBytes())
	}
}

// TestKeyTableCollisionsDoNotMerge forces many keys through a table that starts
// with 64 slots, so clusters form and the probe walk is exercised properly. If
// the walk stopped at the first occupied slot instead of comparing, distinct keys
// would share an id and every aggregate over them would silently combine.
func TestKeyTableCollisionsDoNotMerge(t *testing.T) {
	const n = 1000
	tab := kernel.NewKeyTable()

	byID := make([][]byte, 0, n)
	for i := range n {
		k := []byte("k" + itoa(i))
		id, inserted := tab.GetOrInsert(k)
		if !inserted {
			t.Fatalf("key %q reported as already present, colliding with %q",
				k, byID[id])
		}
		byID = append(byID, bytes.Clone(k))
	}

	for id, k := range byID {
		got, inserted := tab.GetOrInsert(k)
		if inserted || got != int32(id) {
			t.Fatalf("key %q: id = %d (inserted %v), want %d", k, got, inserted, id)
		}
	}
}

// TestKeyTableResetReleases: repartitioning used to assign a fresh map, which
// dropped everything. Reset runs when memory is already short, so keeping
// capacity would defeat its purpose.
func TestKeyTableResetReleases(t *testing.T) {
	tab := kernel.NewKeyTable()
	for i := range 500 {
		tab.GetOrInsert([]byte("key-" + itoa(i)))
	}
	if tab.NBytes() == 0 {
		t.Fatal("a populated table reports zero bytes")
	}

	tab.Reset()

	if tab.Len() != 0 {
		t.Errorf("Len after Reset = %d, want 0", tab.Len())
	}
	if tab.NBytes() != 0 {
		t.Errorf("NBytes after Reset = %d, want 0 — Reset must release, not retain",
			tab.NBytes())
	}
	// And it must be usable again, from scratch, with ids restarting at 0.
	if id, inserted := tab.GetOrInsert([]byte("fresh")); !inserted || id != 0 {
		t.Errorf("after Reset, first insert = (%d, %v), want (0, true)", id, inserted)
	}
}

// TestKeyTableNBytesGrowsWithContent is the weak but load-bearing claim: a memory
// limit is enforced against this number, and the bug it replaced was reporting
// ZERO.
func TestKeyTableNBytesGrowsWithContent(t *testing.T) {
	tab := kernel.NewKeyTable()
	start := tab.NBytes()

	for i := range 2000 {
		tab.GetOrInsert([]byte("a-reasonably-long-key-" + itoa(i)))
	}
	if got := tab.NBytes(); got <= start {
		t.Errorf("NBytes = %d after 2000 keys, was %d when empty", got, start)
	}
	// The arena alone is over 20 bytes per key, so anything near zero means the
	// accounting is not looking at the keys at all.
	if got := tab.NBytes(); got < 2000*20 {
		t.Errorf("NBytes = %d, which is less than the key bytes alone", got)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}

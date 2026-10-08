package kernel_test

import (
	"bytes"
	"runtime"
	"runtime/metrics"
	"strconv"
	"testing"

	"github.com/advenn/ursus/internal/kernel"
)

func allocatedBytes() uint64 {
	s := []metrics.Sample{{Name: "/gc/heap/allocs:bytes"}}
	metrics.Read(s)
	return s[0].Value.Uint64()
}

// TestKeyTableAllocatesAboutWhatItHolds: a million distinct keys, about 24 bytes
// each, against what the table says it holds.
//
// The arena, hashes and offsets grew by append, which past 256 elements adds about a
// quarter at a time: the allocations on the way to a final size F sum to about 5F.
// h2o gb10's profile put half of everything the query allocated there (step 132).
// The arena is now chunks that never move, about F, and the per-key arrays double,
// about 2F; the slot ring always doubled. Under 2 × NBytes; it was about 4.
//
// Not parallel, so no other test allocates while it reads.
func TestKeyTableAllocatesAboutWhatItHolds(t *testing.T) {
	const n = 1 << 20
	buf := make([]byte, 0, 32)
	key := func(i int) []byte {
		buf = append(buf[:0], "group-key-"...)
		return strconv.AppendInt(buf, int64(i)*7919, 10)
	}
	runtime.GC()
	before := allocatedBytes()
	tab := kernel.NewKeyTable()
	for i := range n {
		tab.GetOrInsert(key(i))
	}
	allocated := int64(allocatedBytes() - before)
	held := tab.NBytes()
	t.Logf("allocated %.1f MB to hold %.1f MB: %.2f", float64(allocated)/(1<<20),
		float64(held)/(1<<20), float64(allocated)/float64(held))
	if allocated > 2*held {
		t.Errorf("inserting %d keys allocated %d bytes to hold %d", n, allocated, held)
	}
	if tab.Len() != n {
		t.Fatalf("Len = %d, want %d", tab.Len(), n)
	}
	for i := range n {
		if got := tab.KeyAt(int32(i)); !bytes.Equal(got, key(i)) {
			t.Fatalf("KeyAt(%d) = %q, want %q", i, got, key(i))
		}
	}
}

// TestKeyTableKeysOfEveryLength: the arena is chunks, a key never straddles two, and
// a key longer than the largest chunk, 1 MiB, gets one of its own. Empty keys, a key
// of exactly 1 MiB, and keys of up to 2 MiB, interleaved: every id and every key
// reads back, before and after the table grows past them.
func TestKeyTableKeysOfEveryLength(t *testing.T) {
	lengths := []int{0, 1, 7, 0, 300, 4 << 10, 1, 1<<20 + 1, 0, 12, 1 << 20, 5, 2 << 20, 2, 0, 64 << 10}
	var keys [][]byte
	for round := range 8 {
		for j, l := range lengths {
			k := make([]byte, l)
			for i := range k {
				k[i] = byte(i*31 + j + round*7)
			}
			if l > 0 {
				// Distinct across rounds even where the pattern repeats.
				k[0], k[l-1] = byte(round), byte(j)
			}
			keys = append(keys, k)
		}
	}
	tab := kernel.NewKeyTable()
	want := map[string]int32{}
	check := func() {
		t.Helper()
		for _, k := range keys {
			id, ok := want[string(k)]
			if !ok {
				continue
			}
			if got := tab.KeyAt(id); !bytes.Equal(got, k) {
				t.Fatalf("KeyAt(%d) is %d bytes, want the %d-byte key it was given", id, len(got), len(k))
			}
			if got, found := tab.Get(k); !found || got != id {
				t.Fatalf("Get of the %d-byte key = %d, %v; want %d", len(k), got, found, id)
			}
		}
	}
	for i, k := range keys {
		id, inserted := tab.GetOrInsert(k)
		wantID, seen := want[string(k)]
		if seen {
			if inserted || id != wantID {
				t.Fatalf("key %d (%d bytes) seen before: id %d, inserted %v; want %d", i, len(k), id, inserted, wantID)
			}
			continue
		}
		if !inserted || id != int32(len(want)) {
			t.Fatalf("key %d (%d bytes): id %d, inserted %v; want %d, new", i, len(k), id, inserted, len(want))
		}
		want[string(k)] = id
		if i == len(keys)/3 {
			check()
		}
	}
	check()
	if tab.Len() != len(want) {
		t.Errorf("Len = %d, want %d", tab.Len(), len(want))
	}
	// A memory limit is enforced against NBytes, and a key longer than a chunk is
	// the one that could be left out of it.
	var keyBytes int64
	for k := range want {
		keyBytes += int64(len(k))
	}
	if tab.NBytes() < keyBytes {
		t.Errorf("NBytes = %d, less than the %d bytes of keys it holds", tab.NBytes(), keyBytes)
	}
}

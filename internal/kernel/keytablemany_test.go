package kernel

import (
	"encoding/binary"
	"math/rand/v2"
	"testing"
)

// TestGetOrInsertManyIsGetOrInsertInOrder: a chunk of keys answers exactly as the
// same keys one at a time, in order — ids, the inserted flags, and lookups after —
// with a key repeated inside a chunk, whose second sighting must find the first,
// and across the doublings a table goes through mid-chunk (step 126).
func TestGetOrInsertManyIsGetOrInsertInOrder(t *testing.T) {
	rng := rand.New(rand.NewPCG(126, 1))
	for round := range 20 {
		one, many := NewKeyTable(), NewKeyTable()
		universe := 1 + rng.IntN(5000)
		keyOf := func(v int) []byte {
			b := binary.BigEndian.AppendUint32(nil, uint32(v))
			return append(b, make([]byte, v%5)...) // lengths differ, as strings' do
		}
		for range 50 {
			m := 1 + rng.IntN(ManyChunk)
			keys := make([][]byte, m)
			for j := range keys {
				keys[j] = keyOf(rng.IntN(universe))
			}
			if m > 1 && rng.IntN(3) == 0 {
				keys[m-1] = keys[0] // repeated within the chunk
			}
			ids := make([]int32, m)
			ins := make([]bool, m)
			many.GetOrInsertMany(keys, ids, ins)
			for j, k := range keys {
				id, inserted := one.GetOrInsert(k)
				if id != ids[j] || inserted != ins[j] {
					t.Fatalf("round %d key %d: many gave (%d, %v), one at a time (%d, %v)",
						round, j, ids[j], ins[j], id, inserted)
				}
			}
		}
		// GetMany over present and absent keys answers as Get.
		keys := make([][]byte, ManyChunk)
		for j := range keys {
			keys[j] = keyOf(rng.IntN(2 * universe))
		}
		ids := make([]int32, ManyChunk)
		found := make([]bool, ManyChunk)
		many.GetMany(keys, ids, found)
		for j, k := range keys {
			id, ok := one.Get(k)
			if ok != found[j] || ok && id != ids[j] {
				t.Fatalf("round %d lookup %d: many gave (%d, %v), Get (%d, %v)", round, j, ids[j], found[j], id, ok)
			}
		}
	}
	// An empty table answers every lookup as absent.
	ids := []int32{7}
	found := []bool{true}
	NewKeyTable().GetMany([][]byte{{1}}, ids, found)
	if found[0] {
		t.Error("an empty table found a key")
	}
}

// TestGetManyWritesNothing: probe workers share one frozen table and call GetMany
// on it at once. Its first version kept the slot reads alive by storing their sum
// in the table, a data race the gate's -race run found. Under -race this fails on
// any write; without it, it checks the answers.
func TestGetManyWritesNothing(t *testing.T) {
	tab := NewKeyTable()
	keys := make([][]byte, ManyChunk)
	for j := range keys {
		keys[j] = binary.BigEndian.AppendUint64(nil, uint64(j*7))
		tab.GetOrInsert(keys[j])
	}
	done := make(chan bool)
	for range 4 {
		go func() {
			ids := make([]int32, ManyChunk)
			found := make([]bool, ManyChunk)
			ok := true
			for range 200 {
				tab.GetMany(keys, ids, found)
				for j := range keys {
					ok = ok && found[j] && ids[j] == int32(j)
				}
			}
			done <- ok
		}()
	}
	for range 4 {
		if !<-done {
			t.Error("a concurrent GetMany answered wrongly")
		}
	}
}

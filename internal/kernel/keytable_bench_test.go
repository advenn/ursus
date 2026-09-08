package kernel

import (
	"encoding/binary"
	"testing"
)

// KeyTable had no benchmark, which is why step 27's layout change was judged on a
// profile's flat time and turned out 57% slower end to end. The two sizes matter
// more than the two shapes: step 27's own closing note asks for a table "that does
// not fit cache as well as one that does", because the change that failed did so by
// pushing the index out of L2.
//
//	small   64k keys — index ~1.2 MB, around this machine's L2
//	large    4M keys — index ~75 MB, nowhere near it
//
// Keys are 12 bytes: what GroupKeyEncoder emits for a non-null int64 is 9, and a
// two-column key is more, so this is the realistic middle.
func keyFor(buf []byte, i int) []byte {
	buf = buf[:0]
	buf = append(buf, 0x01)
	buf = binary.BigEndian.AppendUint64(buf, uint64(i))
	return append(buf, 0xAA, 0xBB, 0xCC)
}

func filledTable(n int) *KeyTable {
	t := NewKeyTable()
	t.Reserve(n)
	buf := make([]byte, 0, 16)
	for i := range n {
		t.GetOrInsert(keyFor(buf, i))
	}
	return t
}

// benchGet probes keys that are all PRESENT, which is what a join does: its probe
// side mostly matches. A miss-heavy benchmark measures a different loop.
func benchGet(b *testing.B, n int) {
	t := filledTable(n)
	buf := make([]byte, 0, 16)
	b.ReportAllocs()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		// Multiply by a large odd constant so successive probes land far apart,
		// rather than walking the table in insertion order and reading it
		// sequentially — which would measure the prefetcher, not the table.
		if _, ok := t.Get(keyFor(buf, (i*2654435761)%n)); !ok {
			b.Fatal("missing key")
		}
		i++
	}
}

// benchGetMiss probes keys that are all ABSENT: the rejection path, where a probe
// walks a cluster and gives up.
func benchGetMiss(b *testing.B, n int) {
	t := filledTable(n)
	buf := make([]byte, 0, 16)
	b.ReportAllocs()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		if _, ok := t.Get(keyFor(buf, n+(i*2654435761)%n)); ok {
			b.Fatal("unexpected key")
		}
		i++
	}
}

func benchInsert(b *testing.B, n int) {
	buf := make([]byte, 0, 16)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		t := NewKeyTable()
		t.Reserve(n)
		for i := range n {
			t.GetOrInsert(keyFor(buf, i))
		}
	}
}

func BenchmarkKeyTableGetSmall(b *testing.B)     { benchGet(b, 64<<10) }
func BenchmarkKeyTableGetLarge(b *testing.B)     { benchGet(b, 4<<20) }
func BenchmarkKeyTableGetMissSmall(b *testing.B) { benchGetMiss(b, 64<<10) }
func BenchmarkKeyTableGetMissLarge(b *testing.B) { benchGetMiss(b, 4<<20) }
func BenchmarkKeyTableInsert(b *testing.B)       { benchInsert(b, 1<<20) }

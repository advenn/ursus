package kernel

import (
	"math"
	"testing"
)

func keyParts(keys ...[]int64) *IntKeyParts {
	tables := make([]*IntKeyTable, len(keys))
	for i, ks := range keys {
		tables[i] = NewIntKeyTable()
		for _, k := range ks {
			tables[i].GetOrInsert(k)
		}
	}
	return NewIntKeyParts(tables)
}

// TestIntKeyBitsHoldTheKeysAndNoOthers: step 202's bitmap, over keys spread across
// tables, negative ones, a range at its rules' edges, and a table with a null key.
func TestIntKeyBitsHoldTheKeysAndNoOthers(t *testing.T) {
	var a, b []int64
	member := map[int64]bool{}
	for k := int64(-500); k < 10_000; k += 3 {
		if k%2 == 0 {
			a = append(a, k)
		} else {
			b = append(b, k)
		}
		member[k] = true
	}
	withNull := NewIntKeyTable()
	withNull.GetOrInsertNull()
	withNull.GetOrInsert(42)
	member[42] = true
	parts := NewIntKeyParts([]*IntKeyTable{keyParts(a).Table(0), keyParts(b).Table(0), withNull})
	bits := parts.Bits()
	if bits == nil {
		t.Fatal("dense keys made no bitmap")
	}
	for k := int64(-600); k < 10_100; k++ {
		if bits.Has(k) != member[k] {
			t.Fatalf("Has(%d) = %v", k, bits.Has(k))
		}
	}
	for _, k := range []int64{math.MinInt64, math.MaxInt64, -501, 10_000} {
		if bits.Has(k) {
			t.Errorf("Has(%d) outside the range", k)
		}
	}
	if parts.Bits() != bits {
		t.Error("Bits built twice")
	}

	for _, c := range []struct {
		name string
		keys []int64
		want bool
	}{
		{"two keys a megabit apart", []int64{0, 1<<23 - 1}, true},
		{"two keys past a megabit apart", []int64{0, 1 << 23}, false},
		{"64 bits a key, past a megabit", spreadKeys(1<<18, 64), true},
		{"65 bits a key, past a megabit", spreadKeys(1<<18, 65), false},
		{"the whole int64 range", []int64{math.MinInt64, math.MaxInt64}, false},
		{"none", nil, false},
	} {
		if got := keyParts(c.keys).Bits() != nil; got != c.want {
			t.Errorf("%s: a bitmap is %v, want %v", c.name, got, c.want)
		}
	}
}

// spreadKeys is n keys step apart from zero.
func spreadKeys(n int, step int64) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = int64(i) * step
	}
	return out
}

// TestIntKeyIndexAnswersAsTheTables: step 203's direct index gives every key the id
// the tables give it, across three tables whose ids it offsets by their bases, with
// a null key, negative keys, absent keys and keys outside the range; and is built
// only where the keys are dense enough.
func TestIntKeyIndexAnswersAsTheTables(t *testing.T) {
	// Each key in the table PartitionOf gives it, as a partitioned build places it.
	parts := make([][]int64, 3)
	for k := int64(-300); k < 3_000; k += 2 {
		p := PartitionOf(IntHash(k), 3)
		parts[p] = append(parts[p], k)
	}
	tables := []*IntKeyTable{keyParts(parts[0]).Table(0), keyParts(parts[1]).Table(0), keyParts(parts[2]).Table(0)}
	tables[1].GetOrInsertNull()
	hashed := NewIntKeyParts(tables)
	indexed := NewIntKeyParts(tables)
	if !indexed.Index() || indexed.IndexBytes() == 0 {
		t.Fatal("dense keys made no index")
	}
	probe := []int64{math.MinInt64, math.MaxInt64, -301, -300, -299, 0, 1, 2999, 3000, 3001}
	for k := int64(-310); k < 3_010; k++ {
		probe = append(probe, k)
	}
	for lo := 0; lo < len(probe); lo += ManyChunk {
		keys := probe[lo:min(lo+ManyChunk, len(probe))]
		want, got := make([]int32, len(keys)), make([]int32, len(keys))
		// The hash path, read before any index exists on this one.
		var hs, first [ManyChunk]uint64
		var part [ManyChunk]int
		for j, key := range keys {
			h := IntHash(key)
			p := PartitionOf(h, len(hashed.tables))
			hs[j], part[j], first[j] = h, p, hashed.tables[p].slots[h&hashed.tables[p].mask]
		}
		for j, key := range keys {
			want[j] = hashed.tables[part[j]].getFrom(key, hs[j], first[j])
			if want[j] >= 0 {
				want[j] += hashed.base[part[j]]
			}
		}
		indexed.GetMany(keys, got)
		for j := range keys {
			if got[j] != want[j] {
				t.Fatalf("key %d: index %d, tables %d", keys[j], got[j], want[j])
			}
		}
	}
	for _, c := range []struct {
		name string
		keys []int64
		want bool
	}{
		{"4 slots a key, past a mega-slot", spreadKeys(1<<19, 4), true},
		{"5 slots a key, past a mega-slot", spreadKeys(1<<19, 5), false},
		{"two keys a mega-slot apart", []int64{0, 1<<20 - 1}, true},
		{"two keys past it", []int64{0, 1 << 20}, false},
		{"none", nil, false},
	} {
		if got := keyParts(c.keys).Index(); got != c.want {
			t.Errorf("%s: an index is %v, want %v", c.name, got, c.want)
		}
	}
}

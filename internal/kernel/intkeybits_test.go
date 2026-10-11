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

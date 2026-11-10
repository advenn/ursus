package kernel

import (
	"math"
	"math/big"
	"math/rand/v2"
	"slices"
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
// tables, negative ones and a table with a null key, against a map of the keys; and
// over generated key sets at each edge of its density rule (denseKeySets), built
// exactly where the rule, in math/big, says, and then holding its keys and no others
// (step 503, where a table of six sets and the answers typed beside them was).
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

	outcomes := map[string]map[bool]int{}
	for _, c := range denseKeySets(t) {
		parts := c.parts()
		bits := parts.Bits()
		want := denseRule(c.keys, maxBitsPerKey, maxBitsAnyway)
		if (bits != nil) != want {
			t.Errorf("%s: a bitmap is %v, the rule says %v", c.name, bits != nil, want)
			continue
		}
		if outcomes[c.name] == nil {
			outcomes[c.name] = map[bool]int{}
		}
		outcomes[c.name][want]++
		if bits == nil {
			continue
		}
		for _, k := range c.probes() {
			if bits.Has(k) != c.member[k] {
				t.Fatalf("%s: Has(%d) = %v", c.name, k, bits.Has(k))
			}
		}
	}
	for _, name := range []string{"bits anyway", "bits per key"} {
		if outcomes[name][true] == 0 || outcomes[name][false] == 0 {
			t.Errorf("%s: %v built and not; the sets miss an edge", name, outcomes[name])
		}
	}
}

// denseRule is the rule Bits' and Index's docs state, computed in math/big so that a
// range past what a uint64 counts shows: n keys spanning hi-lo+1 values make a
// structure where the span, perKey slots a key rounded down, is at most n, or the
// span is at most anyway; for no keys, and for a span of 2^64, none.
func denseRule(keys []int64, perKey, anyway int) bool {
	if len(keys) == 0 {
		return false
	}
	return denseRuleOf(slices.Min(keys), slices.Max(keys), len(keys), perKey, anyway)
}

// denseRuleOf is denseRule of n keys from lo to hi.
func denseRuleOf(lo, hi int64, n, perKey, anyway int) bool {
	if n <= 0 {
		return false
	}
	span := new(big.Int).Sub(big.NewInt(hi), big.NewInt(lo))
	span.Add(span, big.NewInt(1))
	if span.BitLen() > 64 {
		return false
	}
	perKeyOK := new(big.Int).Quo(span, big.NewInt(int64(perKey))).Cmp(big.NewInt(int64(n))) <= 0
	return perKeyOK || span.Cmp(big.NewInt(int64(anyway))) <= 0
}

// denseKeySet is a generated set of distinct keys, each placed in one of tables,
// with a null key in one of them where null.
type denseKeySet struct {
	name   string
	keys   []int64
	tables int
	null   bool
	member map[int64]bool
	rng    *rand.Rand
}

func (c denseKeySet) parts() *IntKeyParts {
	tables := make([]*IntKeyTable, c.tables)
	for i := range tables {
		tables[i] = NewIntKeyTable()
	}
	if c.null {
		tables[c.rng.IntN(c.tables)].GetOrInsertNull()
	}
	for _, k := range c.keys {
		tables[c.rng.IntN(c.tables)].GetOrInsert(k)
	}
	return NewIntKeyParts(tables)
}

// probes is every key of a small set, a sample of a large one, each with its
// neighbours, and the ends of the int64 range.
func (c denseKeySet) probes() []int64 {
	out := []int64{math.MinInt64, math.MaxInt64}
	for i, k := range c.keys {
		if len(c.keys) > 2000 && i > 2 && i < len(c.keys)-2 && c.rng.IntN(len(c.keys)/1000) != 0 {
			continue
		}
		out = append(out, k, k-1, k+1)
	}
	return out
}

// denseKeySets generates the sets the rule is asked of, around each of its edges:
// two to fifty keys over a span a few either side of a bitmap's or an index's
// anyway; as many keys as make perKey slots a key past anyway, the span a few either
// side of perKey times the count and of a whole slot more; keys at both ends of the
// int64 range, whose span a uint64 wraps; a few keys anywhere; none, and a null
// alone.
func denseKeySets(t *testing.T) []denseKeySet {
	t.Helper()
	rng := rand.New(rand.NewPCG(503, 202))
	var out []denseKeySet
	add := func(name string, keys []int64) {
		member := make(map[int64]bool, len(keys))
		for _, k := range keys {
			if member[k] {
				t.Fatalf("%s: the key %d twice", name, k)
			}
			member[k] = true
		}
		out = append(out, denseKeySet{name: name, keys: keys, tables: 1 + rng.IntN(3), null: rng.IntN(3) == 0,
			member: member, rng: rand.New(rand.NewPCG(rng.Uint64(), 1))})
	}
	// lo is where a span of span values starts: anywhere it fits, the low end of the
	// range or the high.
	lo := func(span uint64) int64 {
		switch rng.IntN(3) {
		case 0:
			return math.MinInt64 + rng.Int64N(16)
		case 1:
			return int64(uint64(math.MaxInt64) - span + 1 - uint64(rng.Int64N(16)))
		}
		return rng.Int64N(1<<62) - 1<<61
	}
	// spread is n keys over exactly span values from l: both ends, and one key in
	// each of n-2 slots of step values between, so every key is distinct.
	spread := func(l int64, span uint64, n int, step uint64) []int64 {
		keys := []int64{l, int64(uint64(l) + span - 1)}
		for i := 1; i < n-1; i++ {
			keys = append(keys, int64(uint64(l)+uint64(i)*step-uint64(rng.Int64N(int64(step)))))
		}
		return keys
	}
	for _, rule := range []struct {
		name                 string
		perKey, anyway, edge int
	}{
		{"bits", maxBitsPerKey, maxBitsAnyway, 70}, {"index", maxIndexPerKey, maxIndexAnyway, 6},
	} {
		// The span either side of anyway, of keys too few for perKey to matter.
		for _, d := range []int{-2, -1, 0, 1, 2} {
			span := uint64(rule.anyway + d)
			n := 2 + rng.IntN(49)
			add(rule.name+" anyway", spread(lo(span), span, n, (span-2)/uint64(n)))
		}
		// The span either side of perKey slots a key, and of a whole slot more, past
		// anyway: perKey*n + d slots, rounded down, are n for d below perKey.
		for _, d := range []int{-rule.edge, -1, 0, rule.perKey - 1, rule.perKey, rule.edge} {
			n := rule.anyway/rule.perKey + 2 + rng.IntN(3000)
			span := uint64(rule.perKey*n + d)
			add(rule.name+" per key", spread(lo(span), span, n, uint64(rule.perKey)))
		}
	}
	for a := range int64(3) {
		for b := range int64(3) {
			add("the int64 range's ends", []int64{math.MinInt64 + a, math.MaxInt64 - b})
		}
	}
	for range 20 {
		n := 1 + rng.IntN(20)
		window := int64(1) << (5 + rng.IntN(57)) // 32 values to 2^61
		base := rng.Int64N(1<<62) - 1<<61
		seen := map[int64]bool{}
		var keys []int64
		for len(keys) < n {
			k := base + rng.Int64N(window)
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
		add("a few", keys)
	}
	add("none", nil)
	add("none", nil)
	out[len(out)-2].null, out[len(out)-1].null = false, true // no key at all, and a null alone
	return out
}

// TestIntKeyIndexAnswersAsTheTables: step 203's direct index gives every key the id
// the tables give it, across three tables whose ids it offsets by their bases, with
// a null key, negative keys, absent keys and keys outside the range; and is built
// exactly where its density rule, in math/big, says, over the generated key sets and
// over DenseIndexFits' ends and counts (step 503, where a table of five was).
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
	outcomes := map[string]map[bool]int{}
	for _, c := range denseKeySets(t) {
		got, want := c.parts().Index(), denseRule(c.keys, maxIndexPerKey, maxIndexAnyway)
		if got != want {
			t.Errorf("%s: an index is %v, the rule says %v", c.name, got, want)
		}
		if outcomes[c.name] == nil {
			outcomes[c.name] = map[bool]int{}
		}
		outcomes[c.name][want]++
	}
	for _, name := range []string{"index anyway", "index per key"} {
		if outcomes[name][true] == 0 || outcomes[name][false] == 0 {
			t.Errorf("%s: %v built and not; the sets miss an edge", name, outcomes[name])
		}
	}
	// DenseIndexFits, which a dense build asks of its keys' ends and count, by the same
	// rule: over spans of every width, counts either side of each edge.
	rng := rand.New(rand.NewPCG(503, 203))
	for range 2000 {
		l := rng.Int64N(1<<62) - 1<<61
		span := uint64(1) << rng.IntN(62)
		span += uint64(rng.Int64N(int64(span)))
		n := int(span/maxIndexPerKey) + rng.IntN(9) - 4
		if rng.IntN(2) == 0 {
			n = 1 + rng.IntN(1000)
		}
		h := int64(uint64(l) + span - 1)
		want := denseRuleOf(l, h, n, maxIndexPerKey, maxIndexAnyway)
		if got := DenseIndexFits(l, h, n); got != want {
			t.Errorf("DenseIndexFits(%d, %d, %d) = %v, the rule says %v", l, h, n, got, want)
		}
	}
}

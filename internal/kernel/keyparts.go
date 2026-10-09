package kernel

// Partitioned key tables (steps 150 and 151).
//
// A group-by's fold and a join's build split their keys among several tables, one
// per goroutine, by the top half of each key's hash: each table indexes its slots by
// the low bits, so a partition taken from those would crowd its keys into a fraction
// of its own slots.

// KeyHash is the hash a KeyTable gives key, for a caller that routes keys to tables
// before inserting them (GetOrInsertHashed).
func KeyHash(key []byte) uint64 { return probeHash(key) }

// PartitionOf is the partition, of n, a key of hash h belongs to.
func PartitionOf(h uint64, n int) int { return int((h >> 32) * uint64(n) >> 32) }

// KeyParts is several KeyTables, each holding the keys PartitionOf gives it, read as
// one: a key's id is its table's base, the keys of the tables before it, plus its id
// in its own table. A join's partitioned build hands one to the probe (step 151).
//
// Like a frozen KeyTable it mutates nothing when read, so probe workers share one.
type KeyParts struct {
	tables []*KeyTable
	base   []int32
	n      int
}

// NewKeyParts reads tables, in order, as one.
func NewKeyParts(tables []*KeyTable) *KeyParts {
	k := &KeyParts{tables: tables, base: make([]int32, len(tables))}
	for i, t := range tables {
		k.base[i] = int32(k.n)
		k.n += t.Len()
	}
	return k
}

// Len is the number of keys in every table.
func (k *KeyParts) Len() int { return k.n }

// Base is the first id of table p's keys.
func (k *KeyParts) Base(p int) int32 { return k.base[p] }

// NBytes is what every table holds.
func (k *KeyParts) NBytes() int64 {
	var n int64
	for _, t := range k.tables {
		n += t.NBytes()
	}
	return n
}

// GetMany is KeyTable.GetMany across the tables: ids[j] is keys[j]'s id, or -1.
// Each key's first slot is read before any is compared, for KeyTable.GetMany's
// reason.
func (k *KeyParts) GetMany(keys [][]byte, ids []int32, found []bool) {
	var (
		hs, first [ManyChunk]uint64
		part      [ManyChunk]int
	)
	for j, key := range keys {
		h := probeHash(key)
		p := PartitionOf(h, len(k.tables))
		hs[j], part[j] = h, p
		if t := k.tables[p]; t.slots != nil {
			first[j] = t.slots[h&t.mask]
		}
	}
	for j, key := range keys {
		t := k.tables[part[j]]
		if t.slots == nil {
			ids[j], found[j] = -1, false
			continue
		}
		id, ok := t.getFrom(key, hs[j], first[j])
		if ok {
			id += k.base[part[j]]
		}
		ids[j], found[j] = id, ok
	}
}

package kernel

import (
	"slices"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// IntKeyTable is KeyTable for a key of one integer column (step 159).
//
// # Why a second table
//
// A KeyTable takes a key as bytes: GroupKeyEncoder writes a validity tag and the
// value, 9 bytes for an Int64, through a closure per row; the table hashes them a
// byte at a time past the first eight, and compares them with bytes.Equal through
// its arena, two dependent reads away. Step 157 found encoding and looking up keys a
// fifth to half of six of seven PDS-H queries, nearly all of them joining on one
// integer column.
//
// Here the key is the int64 itself: hashed with one Mix64, compared with ==, and
// stored in a flat slice by id. The slots keep KeyTable's layout — a tag of the
// hash's top half over id+1 — and its batched reads.
//
// # Which keys
//
// Every integer type, and every type stored as one (Date, Datetime, Duration,
// Time), widened to int64 by its bits: sign-extended if signed, zero-extended if
// not. That is a bijection for each type, and both sides of a join are cast to one
// key type first, so equal keys widen equal. A null key is not in the table; a
// caller under NullsEqual, where a null is a key, keeps the byte path.
type IntKeyTable struct {
	slots []uint64 // as KeyTable's: hash top half << 32 | id+1, 0 empty
	mask  uint64
	keys  []int64 // by id
}

// IntHash is the hash IntKeyTable gives k.
func IntHash(k int64) uint64 { return Mix64(uint64(k)) }

func NewIntKeyTable() *IntKeyTable {
	t := &IntKeyTable{}
	t.init(initialSlots)
	return t
}

func (t *IntKeyTable) init(n int) {
	t.slots = make([]uint64, n)
	t.mask = uint64(n - 1)
}

// Len is the number of keys, which is also the next id.
func (t *IntKeyTable) Len() int { return len(t.keys) }

// NBytes is what the table holds.
func (t *IntKeyTable) NBytes() int64 { return int64(cap(t.slots))*8 + int64(cap(t.keys))*8 }

// Reserve sizes the table for n keys, as KeyTable.Reserve does.
func (t *IntKeyTable) Reserve(n int) {
	want := 1
	for want < n*maxLoadDen/maxLoadNum {
		want *= 2
	}
	for len(t.slots) < want {
		t.grow()
	}
	if cap(t.keys) < n {
		k := make([]int64, len(t.keys), n)
		copy(k, t.keys)
		t.keys = k
	}
}

// GetOrInsertMany is KeyTable.GetOrInsertMany for up to ManyChunk keys: ids[j] and
// inserted[j] answer keys[j]. Every key's first slot is read before any is placed.
func (t *IntKeyTable) GetOrInsertMany(keys []int64, ids []int32, inserted []bool) {
	var hs [ManyChunk]uint64
	var sum uint64
	for j, k := range keys {
		h := IntHash(k)
		hs[j] = h
		sum += t.slots[h&t.mask]
	}
	_ = sum
	for j, k := range keys {
		ids[j], inserted[j] = t.getOrInsert(k, hs[j])
	}
}

func (t *IntKeyTable) getOrInsert(k int64, h uint64) (int32, bool) {
	tag := h >> 32
	for i := h & t.mask; ; i = (i + 1) & t.mask {
		s := t.slots[i]
		if s == 0 {
			id := int32(len(t.keys))
			t.keys = pushDoubling(t.keys, k)
			t.slots[i] = slotOf(h, id)
			if len(t.keys)*maxLoadDen >= len(t.slots)*maxLoadNum {
				t.grow()
			}
			return id, true
		}
		if s>>32 == tag {
			if got := int32(uint32(s)) - 1; t.keys[got] == k {
				return got, false
			}
		}
	}
}

// GetMany is KeyTable.GetMany for up to ManyChunk keys: ids[j] is keys[j]'s id, or
// -1. It writes nothing, so probe workers share one table.
func (t *IntKeyTable) GetMany(keys []int64, ids []int32) {
	var hs, first [ManyChunk]uint64
	for j, k := range keys {
		h := IntHash(k)
		hs[j], first[j] = h, t.slots[h&t.mask]
	}
	for j, k := range keys {
		ids[j] = t.getFrom(k, hs[j], first[j])
	}
}

// getFrom finds k, of hash h, whose first slot s has been read.
func (t *IntKeyTable) getFrom(k int64, h, s uint64) int32 {
	tag := h >> 32
	for i := h & t.mask; ; {
		if s == 0 {
			return -1
		}
		if s>>32 == tag {
			if got := int32(uint32(s)) - 1; t.keys[got] == k {
				return got
			}
		}
		i = (i + 1) & t.mask
		s = t.slots[i]
	}
}

// grow doubles the slots, rehashing each key; Mix64 is cheaper than storing hashes.
func (t *IntKeyTable) grow() {
	slots := make([]uint64, len(t.slots)*2)
	mask := uint64(len(slots) - 1)
	for id, k := range t.keys {
		h := IntHash(k)
		for i := h & mask; ; i = (i + 1) & mask {
			if slots[i] == 0 {
				slots[i] = slotOf(h, int32(id))
				break
			}
		}
	}
	t.slots, t.mask = slots, mask
}

// IntKeyParts is several IntKeyTables, each holding the keys PartitionOf gives it by
// IntHash, read as one, as KeyParts reads KeyTables: a key's id is its table's base
// plus its id there.
type IntKeyParts struct {
	tables []*IntKeyTable
	base   []int32
	n      int
}

// NewIntKeyParts reads tables, in order, as one.
func NewIntKeyParts(tables []*IntKeyTable) *IntKeyParts {
	k := &IntKeyParts{tables: tables, base: make([]int32, len(tables))}
	for i, t := range tables {
		k.base[i] = int32(k.n)
		k.n += t.Len()
	}
	return k
}

func (k *IntKeyParts) Len() int                 { return k.n }
func (k *IntKeyParts) Base(p int) int32         { return k.base[p] }
func (k *IntKeyParts) Tables() int              { return len(k.tables) }
func (k *IntKeyParts) Table(p int) *IntKeyTable { return k.tables[p] }

// NBytes is what every table holds.
func (k *IntKeyParts) NBytes() int64 {
	var n int64
	for _, t := range k.tables {
		n += t.NBytes()
	}
	return n
}

// GetMany is IntKeyTable.GetMany across the tables, for up to ManyChunk keys.
func (k *IntKeyParts) GetMany(keys []int64, ids []int32) {
	var (
		hs, first [ManyChunk]uint64
		part      [ManyChunk]int
	)
	for j, key := range keys {
		h := IntHash(key)
		p := PartitionOf(h, len(k.tables))
		t := k.tables[p]
		hs[j], part[j], first[j] = h, p, t.slots[h&t.mask]
	}
	for j, key := range keys {
		id := k.tables[part[j]].getFrom(key, hs[j], first[j])
		if id >= 0 {
			id += k.base[part[j]]
		}
		ids[j] = id
	}
}

// IsIntKey reports whether a key of type dt is IntKeys's: stored as an integer of at
// most 64 bits.
func IsIntKey(dt dtype.DataType) bool {
	switch dt.Physical().ID() {
	case dtype.TypeInt8, dtype.TypeInt16, dtype.TypeInt32, dtype.TypeInt64,
		dtype.TypeUint8, dtype.TypeUint16, dtype.TypeUint32, dtype.TypeUint64:
		return true
	}
	return false
}

// IntKeys is c's values as IntKeyTable's keys, widened to int64 by their bits: an
// Int64 column's own values, without a copy, and any other's widened into *scratch,
// which is grown as needed and kept for the next call. The result is read only.
//
// A null row's value is whatever its slot holds, and a payload-free column, all
// null, reads as zeros: a caller skips null rows by the column's validity. c must
// be IsIntKey.
func IntKeys(c *data.Column, scratch *[]int64) []int64 {
	if c.IsPayloadFree() {
		*scratch = slices.Grow((*scratch)[:0], c.Len())[:c.Len()]
		clear(*scratch)
		return *scratch
	}
	switch c.DType().Physical().ID() {
	case dtype.TypeInt64:
		return data.MustValues[int64](c)
	case dtype.TypeInt8:
		*scratch = widen(data.MustValues[int8](c), *scratch)
	case dtype.TypeInt16:
		*scratch = widen(data.MustValues[int16](c), *scratch)
	case dtype.TypeInt32:
		*scratch = widen(data.MustValues[int32](c), *scratch)
	case dtype.TypeUint8:
		*scratch = widen(data.MustValues[uint8](c), *scratch)
	case dtype.TypeUint16:
		*scratch = widen(data.MustValues[uint16](c), *scratch)
	case dtype.TypeUint32:
		*scratch = widen(data.MustValues[uint32](c), *scratch)
	case dtype.TypeUint64:
		*scratch = widen(data.MustValues[uint64](c), *scratch)
	default:
		panic(uerr.Internalf("kernel: IntKeys of a %s column", c.DType()))
	}
	return *scratch
}

func widen[T int8 | int16 | int32 | uint8 | uint16 | uint32 | uint64](v []T, dst []int64) []int64 {
	dst = slices.Grow(dst[:0], len(v))[:len(v)]
	for i, x := range v {
		dst[i] = int64(x)
	}
	return dst
}

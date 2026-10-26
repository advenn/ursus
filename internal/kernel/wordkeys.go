package kernel

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// PairKeyTable is IntKeyTable for a key of two words (step 381, G1 of
// v0.7-scope.md): a group-by's key of several integer columns, packed by a
// KeyPacker, nulls and all.
//
// A key of two Int32 columns went to KeyTable: GroupKeyEncoder wrote each row's
// validity tags and values through a closure per column, the table hashed the ten
// bytes and compared them through its arena. That was a third of h2o gb6's CPU. Here
// the key is two words, hashed with two Mix64s, compared with ==, and stored in a
// flat slice by id.
type PairKeyTable struct {
	slots  []uint64 // as KeyTable's: hash top half << 32 | id+1, 0 empty
	mask   uint64
	keys   []uint64 // by id, two words each
	hashes []uint64 // by id, so a doubling places each key without hashing it again
}

// PairHash is the hash PairKeyTable gives the key (a, b).
func PairHash(a, b uint64) uint64 { return Mix64(a ^ Mix64(b)) }

func NewPairKeyTable() *PairKeyTable {
	t := &PairKeyTable{}
	t.slots = make([]uint64, initialSlots)
	t.mask = initialSlots - 1
	return t
}

// Len is the number of keys, which is also the next id.
func (t *PairKeyTable) Len() int { return len(t.keys) / 2 }

// NBytes is what the table holds.
func (t *PairKeyTable) NBytes() int64 {
	return int64(cap(t.slots))*8 + int64(cap(t.keys))*8 + int64(cap(t.hashes))*8
}

// Reserve sizes the table for n keys, as KeyTable.Reserve does.
func (t *PairKeyTable) Reserve(n int) {
	want := 1
	for want < n*maxLoadDen/maxLoadNum {
		want *= 2
	}
	for len(t.slots) < want {
		t.grow()
	}
	if cap(t.keys) < 2*n {
		k := make([]uint64, len(t.keys), 2*n)
		copy(k, t.keys)
		t.keys = k
		h := make([]uint64, len(t.hashes), n)
		copy(h, t.hashes)
		t.hashes = h
	}
}

// KeyAt is id's key.
func (t *PairKeyTable) KeyAt(id int32) (a, b uint64) { return t.keys[2*id], t.keys[2*id+1] }

// HashAt is PairHash of id's key.
func (t *PairKeyTable) HashAt(id int32) uint64 { return t.hashes[id] }

// GetOrInsertMany is IntKeyTable.GetOrInsertMany for up to ManyChunk keys, the jth
// (a[j], b[j]): ids[j] and inserted[j] answer it. Every key's first slot is read
// before any is placed.
func (t *PairKeyTable) GetOrInsertMany(a, b []uint64, ids []int32, inserted []bool) {
	var hs [ManyChunk]uint64
	var sum uint64
	b = b[:len(a)]
	for j := range a {
		h := PairHash(a[j], b[j])
		hs[j] = h
		sum += t.slots[h&t.mask]
	}
	_ = sum
	for j := range a {
		ids[j], inserted[j] = t.getOrInsert(a[j], b[j], hs[j])
	}
}

func (t *PairKeyTable) getOrInsert(a, b, h uint64) (int32, bool) {
	tag := h >> 32
	for i := h & t.mask; ; i = (i + 1) & t.mask {
		s := t.slots[i]
		if s == 0 {
			id := int32(len(t.keys) / 2)
			t.keys = pushDoubling(t.keys, a)
			t.keys = append(t.keys, b)
			t.hashes = pushDoubling(t.hashes, h)
			t.slots[i] = slotOf(h, id)
			if t.Len()*maxLoadDen >= len(t.slots)*maxLoadNum {
				t.grow()
			}
			return id, true
		}
		if s>>32 == tag {
			if got := int32(uint32(s)) - 1; t.keys[2*got] == a && t.keys[2*got+1] == b {
				return got, false
			}
		}
	}
}

// GetMany is IntKeyTable.GetMany for up to ManyChunk keys: ids[j] is (a[j], b[j])'s
// id, or -1. It writes nothing.
func (t *PairKeyTable) GetMany(a, b []uint64, ids []int32) {
	var hs, first [ManyChunk]uint64
	b = b[:len(a)]
	for j := range a {
		h := PairHash(a[j], b[j])
		hs[j], first[j] = h, t.slots[h&t.mask]
	}
	for j := range a {
		ids[j] = t.getFrom(a[j], b[j], hs[j], first[j])
	}
}

// getFrom finds (a, b), of hash h, whose first slot s has been read.
func (t *PairKeyTable) getFrom(a, b, h, s uint64) int32 {
	tag := h >> 32
	for i := h & t.mask; ; {
		if s == 0 {
			return -1
		}
		if s>>32 == tag {
			if got := int32(uint32(s)) - 1; t.keys[2*got] == a && t.keys[2*got+1] == b {
				return got
			}
		}
		i = (i + 1) & t.mask
		s = t.slots[i]
	}
}

// grow doubles the slots, placing each key by its stored hash.
func (t *PairKeyTable) grow() {
	slots := make([]uint64, len(t.slots)*2)
	mask := uint64(len(slots) - 1)
	for id, h := range t.hashes {
		for i := h & mask; ; i = (i + 1) & mask {
			if slots[i] == 0 {
				slots[i] = slotOf(h, int32(id))
				break
			}
		}
	}
	t.slots, t.mask = slots, mask
}

// KeyPacker packs a key of several integer columns into two words, each column's
// value in a field of its type's width and its null in a bit of its own, so two
// rows' words are equal exactly when their keys are: every value present and equal,
// or null alike. A null's field is zero, whatever its slot holds.
type KeyPacker struct {
	fields []packField
	null   packField // the null bits, one a column from shift up
}

// packField is a column's place in the words: bits wide in word, from shift.
type packField struct {
	word, shift, bits uint
}

// NewKeyPacker is the packer of a key of these types, or false where a type is not
// IsIntKey or the fields and null bits do not fit two words: two Int64 columns and
// their nulls are 130 bits. Each field goes to the first word with room, in order.
func NewKeyPacker(types []dtype.DataType) (*KeyPacker, bool) {
	var used [2]uint
	place := func(bits uint) (packField, bool) {
		for w := range used {
			if used[w]+bits <= 64 {
				f := packField{word: uint(w), shift: used[w], bits: bits}
				used[w] += bits
				return f, true
			}
		}
		return packField{}, false
	}
	p := &KeyPacker{}
	for _, t := range types {
		if !IsIntKey(t) {
			return nil, false
		}
		f, ok := place(uint(t.Physical().BitWidth()))
		if !ok {
			return nil, false
		}
		p.fields = append(p.fields, f)
	}
	null, ok := place(uint(len(types)))
	if !ok {
		return nil, false
	}
	p.null = null
	return p, true
}

// Pack writes rows [lo, lo+len(a)) of cols, the packer's types, as keys (a[j], b[j]).
func (p *KeyPacker) Pack(cols []*data.Column, lo int, a, b []uint64) {
	clear(a)
	clear(b[:len(a)])
	words := [2][]uint64{a, b[:len(a)]}
	for c, col := range cols {
		f := p.fields[c]
		w := words[f.word]
		if !col.IsPayloadFree() {
			switch col.DType().Physical().ID() {
			case dtype.TypeInt8:
				packInto(data.MustValues[int8](col)[lo:], w, f)
			case dtype.TypeInt16:
				packInto(data.MustValues[int16](col)[lo:], w, f)
			case dtype.TypeInt32:
				packInto(data.MustValues[int32](col)[lo:], w, f)
			case dtype.TypeInt64:
				packInto(data.MustValues[int64](col)[lo:], w, f)
			case dtype.TypeUint8:
				packInto(data.MustValues[uint8](col)[lo:], w, f)
			case dtype.TypeUint16:
				packInto(data.MustValues[uint16](col)[lo:], w, f)
			case dtype.TypeUint32:
				packInto(data.MustValues[uint32](col)[lo:], w, f)
			case dtype.TypeUint64:
				packInto(data.MustValues[uint64](col)[lo:], w, f)
			default:
				panic(uerr.Internalf("kernel: a packed key of a %s column", col.DType()))
			}
		}
		free, valid := col.IsPayloadFree(), col.Validity()
		if !free && valid.IsAllSet() {
			continue
		}
		field := fieldMask(f.bits) << f.shift
		bit := uint64(1) << (p.null.shift + uint(c))
		nw := words[p.null.word]
		for j := range a {
			if free || !valid.Get(lo+j) {
				w[j] &^= field
				nw[j] |= bit
			}
		}
	}
}

// fieldMask is a field of bits ones.
func fieldMask(bits uint) uint64 {
	if bits >= 64 {
		return ^uint64(0)
	}
	return 1<<bits - 1
}

// packInto ors each value's low f.bits bits into w at f.shift: a signed value's are
// its two's complement, which its width's bits keep distinct.
func packInto[T int8 | int16 | int32 | int64 | uint8 | uint16 | uint32 | uint64](v []T, w []uint64, f packField) {
	m := fieldMask(f.bits)
	v = v[:len(w)]
	for j, x := range v {
		w[j] |= (uint64(x) & m) << f.shift
	}
}

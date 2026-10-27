package kernel

import (
	"math"
	"runtime"
	"sync"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// firstSeenPart is the fewest rows a part of FirstSeenIDs numbers in parallel.
const firstSeenPart = 1 << 16

// FirstSeenIDs numbers a column's distinct values in the order each first appears:
// ids[i] is row i's value's number, and firsts[k] the row value k first appears at.
// A null is a value, once. Values are equal as group keys are, so NaN is one value
// and -0.0 is +0.0 (step 320, for to_dummies).
//
// Each row is a key of one of a group-by's tables, given to it a chunk at a time: an
// integer column's values themselves, a float column's bits, made one for every NaN
// and for -0.0 and +0.0, a String or Binary column's own bytes, any other column's
// encoded keys. A column that cannot be a key is refused, op naming the operation.
//
// A long column is numbered in parts, in parallel, each in a table of its own. The
// parts' values are then numbered again in one table, part by part in order, each
// by the row it first appears at in its part, so a value's number is where it first
// appears in the whole column; and each part's ids are renumbered, in parallel.
func FirstSeenIDs(op string, c *data.Column) (ids, firsts []int32, _ error) {
	return FirstSeenKeys(op, []*data.Column{c})
}

// FirstSeenKeys is FirstSeenIDs of the keys columns make together, a row's values in
// each, as a group-by keys by several columns: encoded, so that they are equal as
// one column's values are (step 321, for PartitionBy). One column is FirstSeenIDs'.
func FirstSeenKeys(op string, cols []*data.Column) (ids, firsts []int32, _ error) {
	n := cols[0].Len()
	ids = make([]int32, n)
	if n == 0 {
		return ids, nil, nil
	}
	if c := cols[0]; len(cols) == 1 && (c.DType().IsNull() || c.IsPayloadFree()) {
		return ids, []int32{0}, nil // all null: one value, first at row 0
	}
	k, err := newSeenKeys(op, cols)
	if err != nil {
		return nil, nil, err
	}
	parts := min(runtime.GOMAXPROCS(0), n/firstSeenPart)
	if parts <= 1 {
		return ids, k.number(k.table(), 0, n, ids), nil
	}
	bounds := func(p int) (int, int) { return p * n / parts, (p + 1) * n / parts }
	local := make([][]int32, parts) // each part's firsts
	var (
		wg    sync.WaitGroup
		relay uerr.Relay
	)
	for p := range parts {
		relay.Go(&wg, func() {
			lo, hi := bounds(p)
			local[p] = k.number(k.table(), lo, hi, ids)
		})
	}
	wg.Wait()
	relay.Raise()
	all := k.table()
	renumber := make([][]int32, parts)
	for p, rows := range local {
		renumber[p] = make([]int32, len(rows))
		for j, r := range rows {
			id, inserted := k.one(all, int(r))
			renumber[p][j] = id
			if inserted {
				firsts = append(firsts, r)
			}
		}
	}
	for p := range parts {
		relay.Go(&wg, func() {
			lo, hi := bounds(p)
			to := renumber[p]
			for i := lo; i < hi; i++ {
				ids[i] = to[ids[i]]
			}
		})
	}
	wg.Wait()
	relay.Raise()
	return ids, firsts, nil
}

// seenKeys reads a column's rows as the keys of one kind of table: integers, a
// string's bytes, or encoded keys. It only reads, so parts share it.
type seenKeys struct {
	ints  []int64 // an integer or a float column's keys
	strs  data.StringAccessor
	raw   bool             // a String or Binary column: strs's bytes
	enc   *GroupKeyEncoder // any other column
	valid bitmap.View
	nulls bool // the column holds a null: a bitmap with every bit set need not say so
}

func newSeenKeys(op string, cols []*data.Column) (*seenKeys, error) {
	c := cols[0]
	k := &seenKeys{valid: c.Validity(), nulls: c.NullCount() > 0}
	switch dt := c.DType(); {
	case len(cols) > 1:
		// Several columns' keys are encoded, a null in any a key's part of its own.
		enc, err := NewGroupKeyEncoder(op, cols)
		if err != nil {
			return nil, err
		}
		k.enc, k.nulls = enc, false
	case dt.IsFloat():
		k.ints = floatKeys(c)
	case IsIntKey(dt):
		var scratch []int64
		k.ints = IntKeys(c, &scratch)
	case dt.HasStringStorage():
		k.strs, k.raw = c.Strings(), true
	default:
		enc, err := NewGroupKeyEncoder(op, []*data.Column{c})
		if err != nil {
			return nil, err
		}
		k.enc = enc
	}
	return k, nil
}

// seenTable is the table seenKeys' keys go to.
type seenTable struct {
	ints  *IntKeyTable
	bytes *KeyTable
}

func (k *seenKeys) table() seenTable {
	if k.ints != nil {
		return seenTable{ints: NewIntKeyTable()}
	}
	return seenTable{bytes: NewKeyTable()}
}

// number numbers rows lo to hi in t, ids[i] row i's, and is the rows each new value
// first appears at.
func (k *seenKeys) number(t seenTable, lo, hi int, ids []int32) (firsts []int32) {
	var (
		inserted [ManyChunk]bool
		keys     [ManyChunk][]byte
		ends     [ManyChunk]int
		buf      []byte
	)
	for at := lo; at < hi; at += ManyChunk {
		m := min(ManyChunk, hi-at)
		null := k.nullAt(at)
		switch {
		case t.ints != nil && !k.nulls:
			t.ints.GetOrInsertMany(k.ints[at:at+m], ids[at:at+m], inserted[:m])
		case t.ints != nil:
			t.ints.GetOrInsertNullable(k.ints[at:at+m], null, ids[at:at+m], inserted[:m])
		case k.raw && !k.nulls:
			for j := range m {
				keys[j] = k.strs.Bytes(at + j)
			}
			t.bytes.GetOrInsertMany(keys[:m], ids[at:at+m], inserted[:m])
		case k.raw:
			for j := range m {
				keys[j] = nil
				if k.valid.Get(at + j) {
					keys[j] = k.strs.Bytes(at + j)
				}
			}
			t.bytes.GetOrInsertNullable(keys[:m], nil, null, ids[at:at+m], inserted[:m])
		default:
			// A null encodes as a key of its own, so the table needs no placeholder.
			buf = buf[:0]
			for j := range m {
				buf = k.enc.AppendKey(buf, at+j)
				ends[j] = len(buf)
			}
			for j, start := 0, 0; j < m; j++ {
				keys[j], start = buf[start:ends[j]], ends[j]
			}
			t.bytes.GetOrInsertMany(keys[:m], ids[at:at+m], inserted[:m])
		}
		for j := range m {
			if inserted[j] {
				firsts = append(firsts, int32(at+j))
			}
		}
	}
	return firsts
}

// nullAt reports whether row at+j is null, or is nil when the column has no null.
func (k *seenKeys) nullAt(at int) func(j int) bool {
	if !k.nulls {
		return nil
	}
	return func(j int) bool { return !k.valid.Get(at + j) }
}

// one numbers row r in t.
func (k *seenKeys) one(t seenTable, r int) (int32, bool) {
	null := k.nulls && !k.valid.Get(r)
	switch {
	case t.ints != nil && null:
		return t.ints.GetOrInsertNull()
	case t.ints != nil:
		return t.ints.GetOrInsert(k.ints[r])
	case k.raw && null:
		return t.bytes.GetOrInsertNull()
	case k.raw:
		return t.bytes.GetOrInsert(k.strs.Bytes(r))
	}
	return t.bytes.GetOrInsert(k.enc.AppendKey(nil, r))
}

// floatKeys is a float column's values as integer keys equal where the values are
// equal as group keys: each one's float64 bits, every NaN's the same and -0.0's
// +0.0's. A Float32 widens to float64 exactly, so distinct values stay distinct.
func floatKeys(c *data.Column) []int64 {
	keys := make([]int64, c.Len())
	bits := func(f float64) int64 {
		switch {
		case f != f:
			return 0x7ff8_0000_0000_0000
		case f == 0:
			return 0
		}
		return int64(math.Float64bits(f))
	}
	if c.DType().ID() == dtype.TypeFloat32 {
		for i, f := range data.MustValues[float32](c) {
			keys[i] = bits(float64(f))
		}
		return keys
	}
	for i, f := range data.MustValues[float64](c) {
		keys[i] = bits(f)
	}
	return keys
}

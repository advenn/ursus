package physical

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/kernel"
)

// groupKeys is a group-by's table of keys: encoded, as a KeyTable holds them, or,
// for a key of one integer column, the integers themselves (step 160). One of the
// two is set, for the sink's life.
//
// The integer form is step 159's join table, with a null key that is a group: the
// sink reads the column's values and never encodes them, except to route a frozen
// sink's new key, where partitionOf takes the encoded key as it does on the encoded
// path. Its sub-sinks, its workers and the partitioned fold's partitions all come
// from the same key schema, so they share its form.
//
// A key of one String or Binary column is the third form (step 205): raw, a
// KeyTable of each value's own bytes, read in place from the column, and a null
// placeholder as the integers' is. Encoding copied every row's string behind a
// validity byte and a length, before the hash read it again: 14% of h2o gb1.
type groupKeys struct {
	bytes *kernel.KeyTable
	ints  *kernel.IntKeyTable
	raw   bool // bytes holds one string column's values, not encoded keys
}

// newGroupKeys is the table for a key of these fields: integers for one integer
// field, a string's own bytes for one String or Binary field, else encoded.
func newGroupKeys(keys *dtype.Schema) groupKeys {
	if keys.Len() == 1 {
		switch t := keys.Field(0).Type; {
		case kernel.IsIntKey(t):
			return groupKeys{ints: kernel.NewIntKeyTable()}
		case isRawKey(t):
			return groupKeys{bytes: kernel.NewKeyTable(), raw: true}
		}
	}
	return groupKeys{bytes: kernel.NewKeyTable()}
}

// isRawKey reports whether a group key of this one type is kept as its own bytes.
func isRawKey(t dtype.DataType) bool {
	return t.HasStringStorage() // a String, a Binary, a Categorical (step 351)
}

// empty is a new table of the same form.
func (g groupKeys) empty() groupKeys {
	if g.ints != nil {
		return groupKeys{ints: kernel.NewIntKeyTable()}
	}
	return groupKeys{bytes: kernel.NewKeyTable(), raw: g.raw}
}

func (g groupKeys) Len() int {
	if g.ints != nil {
		return g.ints.Len()
	}
	return g.bytes.Len()
}

func (g groupKeys) NBytes() int64 {
	if g.ints != nil {
		return g.ints.NBytes()
	}
	return g.bytes.NBytes()
}

func (g groupKeys) Reserve(n int) {
	if g.ints != nil {
		g.ints.Reserve(n)
		return
	}
	g.bytes.Reserve(n)
}

// HashAt is the hash the table keeps or computes for id's key, which the partitioned
// fold routes by.
func (g groupKeys) HashAt(id int32) uint64 {
	if g.ints != nil {
		return g.ints.HashAt(id)
	}
	return g.bytes.HashAt(id)
}

// insertFrom gives o's keys oids, up to kernel.ManyChunk of them, ids in g, in order,
// inserting those g lacks: ids[j] and inserted[j] answer oids[j]. o is g's form.
func (g groupKeys) insertFrom(o groupKeys, oids []int32, ids []int32, inserted []bool) {
	if g.ints != nil {
		var keys [kernel.ManyChunk]int64
		for j, oid := range oids {
			keys[j] = o.ints.KeyAt(oid)
		}
		null := o.ints.NullID()
		g.ints.GetOrInsertNullable(keys[:len(oids)], func(j int) bool { return oids[j] == null },
			ids[:len(oids)], inserted[:len(oids)])
		return
	}
	var (
		keys [kernel.ManyChunk][]byte
		hs   [kernel.ManyChunk]uint64
	)
	for j, oid := range oids {
		keys[j], hs[j] = o.bytes.KeyAt(oid), o.bytes.HashAt(oid)
	}
	if g.raw {
		// A raw key's null is a placeholder, whose empty key is the empty string's.
		null := o.bytes.NullID()
		g.bytes.GetOrInsertNullable(keys[:len(oids)], hs[:len(oids)],
			func(j int) bool { return oids[j] == null }, ids, inserted)
		return
	}
	g.bytes.GetOrInsertHashed(keys[:len(oids)], hs[:len(oids)], ids, inserted)
}

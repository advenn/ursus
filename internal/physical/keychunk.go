package physical

import "github.com/advenn/ursus/internal/kernel"

// keyChunk holds up to kernel.ManyChunk encoded keys at once, and which input row
// each came from, for KeyTable's batch methods: a chunk's slots are read together,
// so the processor waits on main memory once for many keys rather than once per
// key (step 126).
type keyChunk struct {
	buf   []byte
	keys  [][]byte
	rows  []int32
	ids   []int32
	flags []bool
}

func (c *keyChunk) reset() {
	c.buf, c.keys, c.rows = c.buf[:0], c.keys[:0], c.rows[:0]
}

// add appends row's key. A key slice keeps the backing array it was cut from, so
// appends that move buf leave earlier keys valid.
func (c *keyChunk) add(enc *kernel.GroupKeyEncoder, row int) {
	start := len(c.buf)
	c.buf = enc.AppendKey(c.buf, row)
	c.keys = append(c.keys, c.buf[start:len(c.buf):len(c.buf)])
	c.rows = append(c.rows, int32(row))
}

func (c *keyChunk) full() bool { return len(c.keys) == kernel.ManyChunk }

func (c *keyChunk) answers() ([]int32, []bool) {
	if cap(c.ids) < kernel.ManyChunk {
		c.ids, c.flags = make([]int32, kernel.ManyChunk), make([]bool, kernel.ManyChunk)
	}
	return c.ids[:len(c.keys)], c.flags[:len(c.keys)]
}

// insert is GetOrInsertMany over the chunk: ids[j] and inserted[j] answer row rows[j].
func (c *keyChunk) insert(t *kernel.KeyTable) (ids []int32, inserted []bool) {
	ids, inserted = c.answers()
	t.GetOrInsertMany(c.keys, ids, inserted)
	return ids, inserted
}

// lookup is GetMany over the chunk.
func (c *keyChunk) lookup(t joinKeys) (ids []int32, found []bool) {
	ids, found = c.answers()
	t.GetMany(c.keys, ids, found)
	return ids, found
}

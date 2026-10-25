package physical

import (
	"slices"
	"sync"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/uerr"
)

// uniqueKeys is a unique's set of the keys it has seen (step 362), in a group-by's
// forms: an integer key's values, a string key's own bytes, any other key encoded.
// It was a Go map of each encoded key, whose growth, hashing and the collector's
// walks of it were nine tenths of a unique of a million distinct integers.
//
// On several threads it is a table a thread, a key in the one its hash's
// PartitionOf names. A batch's keys are hashed a range of rows a goroutine, then
// inserted a table a goroutine, each table's rows in the batch's order: a key's
// first row is the one that inserts it, as with one table.
type uniqueKeys struct {
	op    string
	parts []groupKeys

	// The batch's, kept for the next.
	n     int
	ints  []int64  // an integer key's values
	keys  [][]byte // a string key's bytes, or the encoded keys
	hs    []uint64
	part  []uint8
	rows  []int32   // the rows of each table in turn, in order: rows[at[p]:at[p+1]]
	at    []int     // where each table's rows start in rows
	hist  [][]int32 // a range's count of rows a table, then its place in rows
	bufs  [][]byte  // a range's encoded keys
	scr   []int64
	valid bitmap.View
	free  bool // a payload-free column, all null
	nulls bool // a row may be null: valid is not all set, or free
	isNew []bool
}

// A batch's keys are hashed and inserted on several goroutines when it has
// uniqueParallelMin rows or more and the set holds uniqueParallelKeys keys or more;
// otherwise on the caller's, into the same tables. A table of fewer keys is found in
// cache, and a batch's goroutines cost more than they save there: a million rows of
// 1,000 keys took 16 ms on one thread and 20 to 30 on eight.
const (
	uniqueParallelMin  = 4096
	uniqueParallelKeys = 1 << 16
)

// newUniqueKeys is the set for the key of s's columns subset, a table a thread up
// to 32.
func newUniqueKeys(op string, s *dtype.Schema, subset []int, threads int) *uniqueKeys {
	types := make([]dtype.DataType, len(subset))
	for i, c := range subset {
		types[i] = s.Field(c).Type
	}
	d := &uniqueKeys{op: op, parts: make([]groupKeys, min(max(threads, 1), 32))}
	for p := range d.parts {
		d.parts[p] = groupKeysOf(types)
	}
	return d
}

// single is a new set of the same form, of one table. A spilled unique dedupes its
// files one at a time, a level deeper each, and a table a thread at each level put
// a kibibyte a table under the memory limit before a key (step 362).
func (d *uniqueKeys) single() *uniqueKeys {
	return &uniqueKeys{op: d.op, parts: []groupKeys{d.parts[0].empty()}}
}

func (d *uniqueKeys) Len() int {
	n := 0
	for _, g := range d.parts {
		n += g.Len()
	}
	return n
}

func (d *uniqueKeys) NBytes() int64 {
	var n int64
	for _, g := range d.parts {
		n += g.NBytes()
	}
	return n
}

// insert adds the keys of cols' n rows, and says which rows' keys were new: the
// first row of each key the set had not held. The answer is the set's until the
// next call.
func (d *uniqueKeys) insert(cols []*data.Column, n int) ([]bool, error) {
	workers := len(d.parts)
	if n < uniqueParallelMin || d.Len() < uniqueParallelKeys {
		workers = 1
	}
	if err := d.prepare(cols, n, workers); err != nil {
		return nil, err
	}
	d.isNew = slices.Grow(d.isNew[:0], n)[:n]
	d.eachPart(workers, func(p int) {
		var (
			ids [kernel.ManyChunk]int32
			ins [kernel.ManyChunk]bool
		)
		g := d.parts[p]
		d.chunks(p, func(rows []int32) {
			d.insertChunk(g, rows, ids[:len(rows)], ins[:len(rows)])
			for j, r := range rows {
				d.isNew[r] = ins[j]
			}
		})
	})
	return d.isNew, nil
}

// held says which of cols' n rows have a key the set holds, and adds none: a frozen
// unique routes the others.
func (d *uniqueKeys) held(cols []*data.Column, n int) ([]bool, error) {
	if err := d.prepare(cols, n, 1); err != nil {
		return nil, err
	}
	d.isNew = slices.Grow(d.isNew[:0], n)[:n]
	var (
		ids   [kernel.ManyChunk]int32
		found [kernel.ManyChunk]bool
		ks    [kernel.ManyChunk][]byte
		is    [kernel.ManyChunk]int64
	)
	for p, g := range d.parts {
		d.chunks(p, func(rows []int32) {
			switch {
			case g.ints != nil:
				for j, r := range rows {
					is[j] = d.ints[r]
				}
				g.ints.GetMany(is[:len(rows)], ids[:len(rows)])
				for j, r := range rows {
					if d.null(r) {
						ids[j] = g.ints.NullID()
					}
					d.isNew[r] = ids[j] >= 0
				}
			default:
				for j, r := range rows {
					ks[j] = d.keys[r]
				}
				g.bytes.GetMany(ks[:len(rows)], ids[:len(rows)], found[:len(rows)])
				for j, r := range rows {
					if g.raw && d.null(r) {
						found[j] = g.bytes.NullID() >= 0
					}
					d.isNew[r] = found[j]
				}
			}
		})
	}
	return d.isNew, nil
}

// null says whether row r is null, for a key of one column, an integer or a string.
func (d *uniqueKeys) null(r int32) bool {
	return d.nulls && (d.free || !d.valid.Get(int(r)))
}

// prepare reads the batch's keys, hashes them, and names each row's table, a range of
// rows a goroutine on workers.
func (d *uniqueKeys) prepare(cols []*data.Column, n, workers int) error {
	d.n = n
	d.hs = slices.Grow(d.hs[:0], n)[:n]
	d.part = slices.Grow(d.part[:0], n)[:n]
	parts, g := len(d.parts), d.parts[0]
	d.nulls = false
	if g.ints != nil || g.raw {
		c := cols[0]
		if g.ints != nil && !kernel.IsIntKey(c.DType()) || g.raw && !isRawKey(c.DType()) {
			return uerr.Internalf("physical: a %s key of %s's form", c.DType(), d.op)
		}
		d.valid, d.free = c.Validity(), c.IsPayloadFree()
		d.nulls = d.free || !d.valid.IsAllSet()
	}
	switch {
	case g.ints != nil:
		d.ints = kernel.IntKeys(cols[0], &d.scr)
		eachRange(n, workers, func(_, lo, hi int) {
			for r := lo; r < hi; r++ {
				h := uint64(0) // a null's, as the tables' HashAt gives it
				if !d.null(int32(r)) {
					h = kernel.IntHash(d.ints[r])
				}
				d.hs[r], d.part[r] = h, uint8(kernel.PartitionOf(h, parts))
			}
		})
	case g.raw:
		d.keys = slices.Grow(d.keys[:0], n)[:n]
		var strs data.StringAccessor
		if !d.free {
			strs = cols[0].Strings()
		}
		eachRange(n, workers, func(_, lo, hi int) {
			for r := lo; r < hi; r++ {
				k, h := []byte(nil), uint64(0)
				if !d.null(int32(r)) {
					k = strs.Bytes(r)
					h = kernel.KeyHash(k)
				}
				d.keys[r], d.hs[r], d.part[r] = k, h, uint8(kernel.PartitionOf(h, parts))
			}
		})
	default:
		// An encoder a range: one's writers read their columns alone, but a range's
		// own encoder costs nothing beside its rows.
		encs := make([]*kernel.GroupKeyEncoder, workers)
		for w := range encs {
			e, err := kernel.NewGroupKeyEncoder(d.op, cols)
			if err != nil {
				return err
			}
			encs[w] = e
		}
		d.keys = slices.Grow(d.keys[:0], n)[:n]
		if len(d.bufs) < workers {
			d.bufs = append(d.bufs, make([][]byte, workers-len(d.bufs))...)
		}
		eachRange(n, workers, func(w, lo, hi int) {
			enc, buf := encs[w], d.bufs[w][:0]
			for r := lo; r < hi; r++ {
				// A key keeps the array it was cut from, so a later append that moves buf
				// leaves it whole.
				start := len(buf)
				buf = enc.AppendKey(buf, r)
				k := buf[start:len(buf):len(buf)]
				h := kernel.KeyHash(k)
				d.keys[r], d.hs[r], d.part[r] = k, h, uint8(kernel.PartitionOf(h, parts))
			}
			d.bufs[w] = buf
		})
	}
	d.byTable(n, workers)
	return nil
}

// byTable lists each table's rows, in order, a range of rows a goroutine on workers:
// each range counts its rows a table, and lays them out after the rows of the tables
// before and of the ranges before it in theirs, as the radix passes lay out their
// buckets. Each table's goroutine then reads its own rows alone.
func (d *uniqueKeys) byTable(n, workers int) {
	parts := len(d.parts)
	if parts == 1 {
		return
	}
	d.rows = slices.Grow(d.rows[:0], n)[:n]
	d.at = slices.Grow(d.at[:0], parts+1)[:parts+1]
	for len(d.hist) < workers {
		d.hist = append(d.hist, make([]int32, parts))
	}
	eachRange(n, workers, func(w, lo, hi int) {
		h := d.hist[w]
		clear(h)
		for _, q := range d.part[lo:hi] {
			h[q]++
		}
	})
	sum := int32(0)
	for p := range parts {
		d.at[p] = int(sum)
		for _, h := range d.hist[:workers] {
			h[p], sum = sum, sum+h[p]
		}
	}
	d.at[parts] = n
	eachRange(n, workers, func(w, lo, hi int) {
		at := d.hist[w]
		for r := lo; r < hi; r++ {
			q := d.part[r]
			d.rows[at[q]] = int32(r)
			at[q]++
		}
	})
}

// insertChunk inserts rows' keys into g, a chunk of the batch's in order: ids[j] and
// ins[j] answer rows[j].
func (d *uniqueKeys) insertChunk(g groupKeys, rows []int32, ids []int32, ins []bool) {
	switch {
	case g.ints != nil:
		var ks [kernel.ManyChunk]int64
		for j, r := range rows {
			ks[j] = d.ints[r]
		}
		if d.nulls {
			g.ints.GetOrInsertNullable(ks[:len(rows)], func(j int) bool { return d.null(rows[j]) }, ids, ins)
		} else {
			g.ints.GetOrInsertMany(ks[:len(rows)], ids, ins)
		}
	default:
		var (
			ks [kernel.ManyChunk][]byte
			hs [kernel.ManyChunk]uint64
		)
		for j, r := range rows {
			ks[j], hs[j] = d.keys[r], d.hs[r]
		}
		if g.raw && d.nulls {
			g.bytes.GetOrInsertNullable(ks[:len(rows)], hs[:len(rows)],
				func(j int) bool { return d.null(rows[j]) }, ids, ins)
		} else {
			g.bytes.GetOrInsertHashed(ks[:len(rows)], hs[:len(rows)], ids, ins)
		}
	}
}

// chunks calls fn with the batch's rows of table p, in order, up to ManyChunk at a
// time.
func (d *uniqueKeys) chunks(p int, fn func(rows []int32)) {
	if len(d.parts) == 1 {
		var rows [kernel.ManyChunk]int32
		for lo := 0; lo < d.n; lo += kernel.ManyChunk {
			m := min(kernel.ManyChunk, d.n-lo)
			for j := range m {
				rows[j] = int32(lo + j)
			}
			fn(rows[:m])
		}
		return
	}
	mine := d.rows[d.at[p]:d.at[p+1]]
	for lo := 0; lo < len(mine); lo += kernel.ManyChunk {
		fn(mine[lo:min(lo+kernel.ManyChunk, len(mine))])
	}
}

// eachPart runs fn on each table, a goroutine apiece on several workers, and waits.
func (d *uniqueKeys) eachPart(workers int, fn func(p int)) {
	if workers <= 1 {
		for p := range d.parts {
			fn(p)
		}
		return
	}
	var wg sync.WaitGroup
	for p := range d.parts {
		wg.Go(func() { fn(p) })
	}
	wg.Wait()
}

// eachRange runs fn on workers contiguous ranges of [0, n), w numbering each, a
// goroutine apiece past one worker, and waits.
func eachRange(n, workers int, fn func(w, lo, hi int)) {
	if workers <= 1 {
		fn(0, 0, n)
		return
	}
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() { fn(w, w*n/workers, (w+1)*n/workers) })
	}
	wg.Wait()
}

package kernel

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// --- counting ----------------------------------------------------------------

// countMode selects which rows countAcc counts.
type countMode uint8

const (
	countValues countMode = iota // Count: non-null values
	countRows                    // Len: every row
	countNulls                   // NullCount: nulls only
)

// countAcc implements Count (non-null values), Len (rows) and NullCount.
//
// The first two are SQL's COUNT(col) and COUNT(*), and they must never be aliased:
// over a column with nulls they give different answers, and which one a user meant
// is not recoverable after the fact. NullCount is their difference, and counting it
// directly rather than subtracting keeps all three on one code path.
type countAcc struct {
	mode countMode
	n    []uint64
}

func (a *countAcc) Reserve(n int) { a.n = Extend(a.n, n, 0) }

func (a *countAcc) AddBatch(groups []int32, col *data.Column) error {
	valid := col.Validity()
	// The mode is hoisted out of the loop: it is fixed for the accumulator's
	// lifetime, and this is the innermost loop in the engine's cheapest aggregate.
	switch a.mode {
	case countRows:
		for _, g := range groups {
			a.n[g]++
		}
	case countValues:
		for i, g := range groups {
			if valid.Get(i) {
				a.n[g]++
			}
		}
	default: // countNulls
		for i, g := range groups {
			if !valid.Get(i) {
				a.n[g]++
			}
		}
	}
	return nil
}

func (a *countAcc) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*countAcc)
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into countAcc", other)
	}
	// Three aggregates now share this type, so identical Go types no longer imply
	// the same aggregate. Merging a null count into a row count would produce a
	// number with no meaning and no symptom.
	if a.mode != o.mode {
		return uerr.Internalf("kernel: cannot merge countAcc mode %d into mode %d",
			o.mode, a.mode)
	}
	a.Reserve(mergeCap(remap, len(o.n)))
	mergeEach(remap, len(o.n), func(dst, src int) { a.n[dst] += o.n[src] })
	return nil
}

// Finish is never null: an empty or all-null group still has a well-defined count
// of zero. This is the one aggregate family with no validity bitmap.
func (a *countAcc) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	return data.NewFixed(name, dtype.Uint64, a.n[:nGroups], bitmap.AllSet(nGroups)), nil
}

// --- n_unique ----------------------------------------------------------------

// nuniqueAcc counts DISTINCT non-null values per group.
//
// Nulls are SKIPPED, so n_unique([null,null]) is 0. Polars counts null as a
// distinct value and returns 1; every other engine, and every other aggregate in
// ursus, skips nulls. Internal consistency wins: an n_unique that treated nulls
// differently from sum, mean, min, max and count would be a permanent trap.
// nuniqueAcc counts the distinct values in each group.
//
// # One table, not one map per group
//
// It used to be `sets []map[string]struct{}` — a Go map for every group — which is
// the layout joinTable's doc rejected for the join and never had applied here:
// "a map to a slice costs a slice header plus a backing array per distinct key: at
// 5M keys that is ~120 MB of headers, 5M allocations, and 5M pointers for the GC to
// trace on every cycle". Measured on PDS-H q21, whose group-by on l_orderkey makes
// about 1.5 million groups, it was 74% of the query's live heap.
//
// So it is one KeyTable keyed by the group AND the value, and distinctness per group
// falls out of GetOrInsert reporting `inserted`. Same structure the six operators
// that identify groups already use.
//
// # The prefix is four raw bytes
//
// The group id has to be unambiguously separable from the value, and fixed width is
// the simplest thing that is.
//
// A weaker claim than it first appears, and worth stating accurately: a DECIMAL
// prefix would also be unambiguous here, because GroupKeyEncoder already frames its
// output — a validity byte, then a big-endian length for variable-width types — so
// the byte after the prefix is never a digit. The tooth aimed at this (spell the
// prefix with strconv.Itoa) could not be made to bite for exactly that reason.
//
// Fixed width is still the right choice: it makes this key's framing independent of
// how another type happens to frame its own, rather than correct by a coincidence
// between two decisions that nothing links.
//
// # An integer's value and group are two words
//
// Of an integer column the key is (value, group) in a WordKeyTable (step 381): the
// value widened as IntKeys widens it, compared with ==, where encoding it called a
// closure a row and the table hashed and compared its bytes. q16's count of distinct
// suppliers a part spent a fifth of its CPU there. The first rows decide which table
// holds the keys, by their column's type; a spilled run writes an integer's key as
// its eight bytes, big-endian, so the runs order by value as the table's entries do.
type nuniqueAcc struct {
	// tab holds group ++ value; the zero value is usable, since GetOrInsert inits
	// lazily and Len guards on a nil offs.
	tab    KeyTable
	counts []int32
	key    []byte // scratch, reused across rows

	// ints holds (value, group) instead, for an integer column, once decided.
	ints    *WordKeyTable
	decided bool
	reserve int     // the groups Reserve asked for before the table was decided
	scratch []int64 // IntKeys's

	// Past its sink's budget its keys go to sp, and counts are recounted from there
	// at Finish (keyspill.go, step 241).
	sp KeySpill
}

// groupPrefixLen is the width of the group id inside a key.
const groupPrefixLen = 4

// putGroup writes the fixed-width prefix and returns the buffer ready for a value.
func putGroup(dst []byte, g int32) []byte {
	u := uint32(g)
	return append(dst[:0], byte(u), byte(u>>8), byte(u>>16), byte(u>>24))
}

// takeGroup reads the prefix back out of a stored key.
func takeGroup(key []byte) int32 {
	return int32(uint32(key[0]) | uint32(key[1])<<8 | uint32(key[2])<<16 | uint32(key[3])<<24)
}

func (a *nuniqueAcc) Reserve(n int) {
	a.counts = Extend(a.counts, n, 0)
	// Size the table by the GROUP count. It is a lower bound on the distinct pairs —
	// every group contributes at least one — and it skips most of the doublings that
	// otherwise rehash the whole table on the way up from 64 slots.
	a.reserve = max(a.reserve, n)
	switch {
	case a.ints != nil:
		a.ints.Reserve(n)
	case a.decided:
		a.tab.Reserve(n)
	}
}

// decide takes the table for keys of ints or not, sized as Reserve asked.
func (a *nuniqueAcc) decide(ints bool) {
	a.decided = true
	if ints {
		a.ints = NewWordKeyTable(2)
		a.ints.Reserve(a.reserve)
		return
	}
	a.tab.Reserve(a.reserve)
}

// empty reports whether the accumulator holds no key and none spilled, so it may
// still take either table.
func (a *nuniqueAcc) empty() bool {
	return a.sp == nil && a.tab.Len() == 0 && (a.ints == nil || a.ints.Len() == 0)
}

func (a *nuniqueAcc) AddBatch(groups []int32, col *data.Column) error {
	ints := IsIntKey(col.DType())
	if !a.decided || a.ints != nil != ints && a.empty() {
		a.ints = nil
		a.decide(ints)
	}
	if a.ints != nil {
		if !ints {
			if col.NullCount() == col.Len() {
				return nil
			}
			return uerr.Internalf("kernel: n_unique of integers given a %s column", col.DType())
		}
		a.addInts(groups, col)
		return nil
	}
	// Reuse the group-key encoder, so distinctness uses ursus's grouping equality:
	// NaN equals NaN and -0.0 equals +0.0. Using IEEE equality here would make
	// n_unique disagree with group_by on the same data.
	enc, err := NewGroupKeyEncoder("n_unique", []*data.Column{col})
	if err != nil {
		return err
	}
	valid := col.Validity()
	for i, g := range groups {
		if !valid.Get(i) {
			continue
		}
		a.key = append(putGroup(a.key, g), enc.Encode(i)...)
		if _, inserted := a.tab.GetOrInsert(a.key); inserted {
			a.counts[g]++
		}
	}
	return nil
}

// addInts is AddBatch for an integer column: each present row's (value, group), a
// chunk at a time.
func (a *nuniqueAcc) addInts(groups []int32, col *data.Column) {
	keys := IntKeys(col, &a.scratch)
	free, valid := col.IsPayloadFree(), col.Validity()
	if free {
		return
	}
	all := valid.IsAllSet()
	var (
		va, vb [ManyChunk]uint64
		gs     [ManyChunk]int32
		ids    [ManyChunk]int32
		ins    [ManyChunk]bool
	)
	m := 0
	for i, g := range groups {
		if !all && !valid.Get(i) {
			continue
		}
		va[m], vb[m], gs[m] = uint64(keys[i]), uint64(uint32(g)), g
		if m++; m == ManyChunk {
			a.insertInts(va[:m], vb[:m], gs[:m], ids[:m], ins[:m])
			m = 0
		}
	}
	a.insertInts(va[:m], vb[:m], gs[:m], ids[:m], ins[:m])
}

// insertInts inserts the keys (va[j], vb[j]), counting a new one in group gs[j].
func (a *nuniqueAcc) insertInts(va, vb []uint64, gs, ids []int32, ins []bool) {
	if len(va) == 0 {
		return
	}
	a.ints.GetOrInsertMany(va, vb, nil, ids, ins)
	for j, g := range gs {
		if ins[j] {
			a.counts[g]++
		}
	}
}

// Merge walks the other accumulator's ENTRIES rather than its groups.
//
// There is no per-group set to union any more, so this reads each stored key, swaps
// its group prefix for this accumulator's id, and re-inserts. Sources at or past
// len(remap) are dropped, which is what mergeEach does and what this has to
// reproduce by hand now that it cannot use it.
func (a *nuniqueAcc) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*nuniqueAcc)
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into nuniqueAcc", other)
	}
	if a.sp != nil || o.sp != nil {
		// Only a serial sink spills its keys: parallel workers go serial first.
		return uerr.Internalf("kernel: cannot merge an n_unique whose keys spilled")
	}
	if o.empty() {
		a.Reserve(mergeCap(remap, len(o.counts)))
		return nil
	}
	if oInts := o.ints != nil; !a.decided || a.ints != nil != oInts {
		if !a.empty() {
			return uerr.Internalf("kernel: cannot merge an n_unique of integers with one of encoded keys")
		}
		a.ints = nil
		a.decide(oInts)
	}
	a.Reserve(mergeCap(remap, len(o.counts)))
	if isSparse(remap) {
		// Its lookup is by source group, which a list of pairs is not.
		dense := make([]int32, len(o.counts))
		for i := range dense {
			dense[i] = -1
		}
		for j := 1; j+1 < len(remap); j += 2 {
			if int(remap[j]) < len(dense) {
				dense[remap[j]] = remap[j+1]
			}
		}
		remap = dense
	}
	if o.ints != nil {
		a.mergeInts(o, remap)
		return nil
	}
	for id := int32(0); id < int32(o.tab.Len()); id++ {
		k := o.tab.KeyAt(id)
		if len(k) < groupPrefixLen {
			return uerr.Internalf("kernel: n_unique key of %d bytes", len(k))
		}
		dst := takeGroup(k)
		if remap != nil {
			if int(dst) >= len(remap) || remap[dst] < 0 {
				continue // another partition's group (mergeEach)
			}
			dst = remap[dst]
		}
		if int(dst) >= len(a.counts) {
			continue
		}
		a.key = append(putGroup(a.key, dst), k[groupPrefixLen:]...)
		if _, inserted := a.tab.GetOrInsert(a.key); inserted {
			a.counts[dst]++
		}
	}
	return nil
}

// mergeInts is Merge's walk of o's keys of integers, through a dense remap or none.
func (a *nuniqueAcc) mergeInts(o *nuniqueAcc, remap []int32) {
	var (
		va, vb [ManyChunk]uint64
		gs     [ManyChunk]int32
		ids    [ManyChunk]int32
		ins    [ManyChunk]bool
	)
	m := 0
	for id := range int32(o.ints.Len()) {
		v, g, _ := o.ints.KeyAt(id)
		dst := int32(uint32(g))
		if remap != nil {
			if int(dst) >= len(remap) || remap[dst] < 0 {
				continue // another partition's group (mergeEach)
			}
			dst = remap[dst]
		}
		if int(dst) >= len(a.counts) {
			continue
		}
		va[m], vb[m], gs[m] = v, uint64(uint32(dst)), dst
		if m++; m == ManyChunk {
			a.insertInts(va[:m], vb[:m], gs[:m], ids[:m], ins[:m])
			m = 0
		}
	}
	a.insertInts(va[:m], vb[:m], gs[:m], ids[:m], ins[:m])
}

func (a *nuniqueAcc) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	out := make([]uint64, nGroups)
	if a.sp != nil {
		if err := a.finishSpilled(out); err != nil {
			return nil, err
		}
		return data.NewFixed(name, dtype.Uint64, out, bitmap.AllSet(nGroups)), nil
	}
	for i := range nGroups {
		out[i] = uint64(a.counts[i])
	}
	return data.NewFixed(name, dtype.Uint64, out, bitmap.AllSet(nGroups)), nil
}

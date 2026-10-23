package kernel

import (
	"bytes"
	"math"
	"slices"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// Mode in an aggregation (step 340).
//
// Polars' mode is a group's most frequent values, each once, null a value: in the
// order they first appear with maintain_order, ascending without, where Polars'
// order is its hash table's. It was the list kernel over the group's values
// imploded, which kept every value of every group and refused past a limit.
//
// Now each distinct value is numbered as it comes, by its bits where its type is
// stored as an integer or a float, a float's made one value of every NaN and of ±0 as
// grouping makes them (IntKeyTable), or else by the group-key encoder's bytes
// (KeyTable): a table of the distinct values, small where a mode is asked. A row is
// then its group and its value's number, eight bytes, and a float's own bits besides,
// whose first in a group is the answer's: Polars answers −0 where −0 came first. At
// Finish the rows are sorted by group, by counting, and each group's values counted
// in an array by number, which a group's rows read in turn: a hash table keyed by
// group and value was a cache miss a row, and slower than the implode it replaced.
//
// Past its sink's budget the rows are counted so, a group at a time, into an entry
// for each distinct value of each group: its rows, its first row and its first value.
// The entries go to a ModeSpill as a run, sorted by group and key, and the rows and
// tables start afresh. At Finish the rest go too, and the runs are read back twice:
// once to find each group's modes, the keys of the most rows, by their first rows,
// and once to take their values.

// ModeSpill holds a mode's entries past a sink's budget, in runs, and gives them back
// ordered by group, then by key. The kernel touches no file.
type ModeSpill interface {
	// Put adds entries to the open run, opening one if none is: each one's group,
	// key, rows, first row and value. Within a run they come in order of group, then
	// of key.
	Put(gids []int32, keys [][]byte, rows, first []int64, vals *data.Column) error
	// EndRun closes the open run, if there is one.
	EndRun() error
	// Scan gives back every entry put, merged across the runs, a batch at a time,
	// and keeps them for another read; Ordered does too, and lets them go.
	Scan(fn func(gids []int32, keys data.StringAccessor, rows, first []int64, vals *data.Column) error) error
	Ordered(fn func(gids []int32, keys data.StringAccessor, rows, first []int64, vals *data.Column) error) error
}

// ModeSpiller is an accumulator whose entries can go to a ModeSpill.
type ModeSpiller interface {
	// SpillModes hands every entry held to sp and lets them go, and reports whether
	// it held any.
	SpillModes(sp ModeSpill) (bool, error)
}

// modeAcc is each group's most frequent values: see above.
type modeAcc struct {
	elem     dtype.DataType
	maintain bool
	byInt    bool // values numbered by their bits, in ints; else by their bytes, in keys
	raw      bool // a text's bytes its own, its null numbered apart; else the encoder's
	float    bool // a row keeps its own bits: a number holds every NaN, and ±0
	null     int32

	ints *IntKeyTable
	keys KeyTable
	vals []*data.Column // each number's first value, in number order
	nv   int            // values numbered

	gid  []int32  // a row's group, every row held in order
	vid  []int32  // its value's number
	bits []uint64 // and, for a float, its own bits

	seen    int64   // rows already spilled, which the held rows' numbers follow
	scratch []int64 // a batch's values as keys

	// Past its sink's budget its rows go to sp as entries, and every answer is read
	// back from there at Finish.
	sp ModeSpill
}

func newModeAcc(in dtype.DataType, maintain bool) *modeAcc {
	phys := in.Physical().ID()
	float := phys == dtype.TypeFloat64 || phys == dtype.TypeFloat32
	return &modeAcc{elem: in, maintain: maintain, byInt: IsIntKey(in) || float, float: float,
		raw: phys == dtype.TypeString || phys == dtype.TypeBinary, null: -1}
}

// nullNumber is a text's null's number, the next one if it has none.
func (a *modeAcc) nullNumber() int32 {
	if a.null < 0 {
		a.null = int32(a.keys.Len())
	}
	return a.null
}

// byteNumber is the number of a text whose key is id in keys: ids from the null's on
// were given after it, so are one past.
func (a *modeAcc) byteNumber(id int32) int32 {
	if a.null >= 0 && id >= a.null {
		return id + 1
	}
	return id
}

// byteKey is the key in keys of a text's number v, not the null's.
func (a *modeAcc) byteKey(v int32) []byte {
	if a.null >= 0 && v > a.null {
		v--
	}
	return a.keys.KeyAt(v)
}

func (a *modeAcc) Reserve(int) {}

// number numbers each of col's values into a.vid, a null among them, and keeps the
// first value of each new number.
func (a *modeAcc) number(col *data.Column) error {
	n := col.Len()
	base := len(a.vid)
	a.vid = Extend(a.vid, base+n, 0)
	vids, valid := a.vid[base:], col.Validity()
	switch {
	case a.raw:
		// A text's own bytes are its key, a null numbered apart.
		strs := col.Strings()
		for i := range n {
			if !valid.Get(i) {
				vids[i] = a.nullNumber()
				continue
			}
			id, _ := a.keys.GetOrInsert(strs.Bytes(i))
			vids[i] = a.byteNumber(id)
		}
	case !a.byInt:
		enc, err := NewGroupKeyEncoder("mode", []*data.Column{col})
		if err != nil {
			return err
		}
		for i := range n {
			vids[i], _ = a.keys.GetOrInsert(enc.Encode(i))
		}
	default:
		if a.ints == nil {
			a.ints = NewIntKeyTable()
		}
		if err := a.numberInts(col, vids, valid); err != nil {
			return err
		}
	}
	// The values numbered here first: a number's values are one value bit for bit,
	// but a float's, whose rows keep their own.
	var fresh []int32
	for i, v := range vids {
		if int(v) == a.nv {
			fresh = append(fresh, int32(i))
			a.nv++
		}
	}
	if len(fresh) == 0 || a.float {
		return nil
	}
	part, err := Take(col, fresh)
	if err != nil {
		return err
	}
	a.vals = append(a.vals, part)
	return nil
}

// numberInts numbers col's values by their bits, a float's made one value of every
// NaN and of ±0.
func (a *modeAcc) numberInts(col *data.Column, vids []int32, valid bitmap.View) error {
	n := col.Len()
	if col.IsPayloadFree() {
		id, _ := a.ints.GetOrInsertNull()
		for i := range vids {
			vids[i] = id
		}
		return nil
	}
	var keys []int64
	switch col.DType().Physical().ID() {
	case dtype.TypeFloat64:
		a.scratch = Extend(a.scratch[:0], n, 0)
		for i, v := range data.MustValues[float64](col) {
			a.scratch[i] = int64(OrderKeyF64(v))
		}
		keys = a.scratch
	case dtype.TypeFloat32:
		a.scratch = Extend(a.scratch[:0], n, 0)
		for i, v := range data.MustValues[float32](col) {
			a.scratch[i] = int64(OrderKeyF32(v))
		}
		keys = a.scratch
	default:
		keys = IntKeys(col, &a.scratch)
	}
	var inserted [ManyChunk]bool
	for lo := 0; lo < n; lo += ManyChunk {
		hi := min(lo+ManyChunk, n)
		if valid.IsAllSet() {
			a.ints.GetOrInsertMany(keys[lo:hi], vids[lo:hi], inserted[:hi-lo])
			continue
		}
		a.ints.GetOrInsertNullable(keys[lo:hi], func(j int) bool { return !valid.Get(lo + j) },
			vids[lo:hi], inserted[:hi-lo])
	}
	return nil
}

func (a *modeAcc) AddBatch(groups []int32, col *data.Column) error {
	if err := a.number(col); err != nil {
		return err
	}
	a.gid = append(a.gid, groups...)
	if !a.float || col.IsPayloadFree() {
		if a.float {
			a.bits = Extend(a.bits, len(a.gid), 0)
		}
		return nil
	}
	switch col.DType().Physical().ID() {
	case dtype.TypeFloat64:
		for _, v := range data.MustValues[float64](col) {
			a.bits = append(a.bits, math.Float64bits(v))
		}
	default:
		for _, v := range data.MustValues[float32](col) {
			a.bits = append(a.bits, uint64(math.Float32bits(v)))
		}
	}
	return nil
}

// Merge folds in other's rows, which must have seen a later portion of the input, as
// implodeAcc.Merge's must: they follow this one's, their values numbered here.
// IsOrderDependent declares mode, so nothing calls it today.
func (a *modeAcc) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*modeAcc)
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into modeAcc", other)
	}
	if a.sp != nil || o.sp != nil {
		return uerr.Internalf("kernel: cannot merge a mode whose rows spilled")
	}
	if isSparse(remap) {
		// Its lookup is by source group, which a list of pairs is not.
		var dense []int32
		for j := 1; j+1 < len(remap); j += 2 {
			if int(remap[j]) >= len(dense) {
				dense = Extend(dense, int(remap[j])+1, -1)
			}
			dense[remap[j]] = remap[j+1]
		}
		remap = dense
	}
	// Other's numbers here, a new one keeping other's first value.
	to := make([]int32, o.nv)
	var fresh []int32
	for v := range o.nv {
		var id int32
		switch {
		case a.raw && int32(v) == o.null:
			id = a.nullNumber()
		case a.raw:
			k, _ := a.keys.GetOrInsert(o.byteKey(int32(v)))
			id = a.byteNumber(k)
		case !a.byInt:
			id, _ = a.keys.GetOrInsert(o.keys.KeyAt(int32(v)))
		default:
			if a.ints == nil {
				a.ints = NewIntKeyTable()
			}
			if int32(v) == o.ints.NullID() {
				id, _ = a.ints.GetOrInsertNull()
			} else {
				id, _ = a.ints.GetOrInsert(o.ints.KeyAt(int32(v)))
			}
		}
		to[v] = id
		if int(id) == a.nv {
			fresh = append(fresh, int32(v))
			a.nv++
		}
	}
	for i, g := range o.gid {
		if remap != nil {
			if int(g) >= len(remap) || remap[g] < 0 {
				continue // another partition's group (mergeEach)
			}
			g = remap[g]
		}
		a.gid = append(a.gid, g)
		a.vid = append(a.vid, to[o.vid[i]])
		if a.float {
			a.bits = append(a.bits, o.bits[i])
		}
	}
	if len(fresh) == 0 || a.float {
		return nil
	}
	all, err := concatColumn(o.vals, o.nv)
	if err != nil {
		return err
	}
	part, err := Take(all, fresh)
	if err != nil {
		return err
	}
	a.vals = append(a.vals, part)
	return nil
}

// modeCount is one distinct value of one group, counted: its value's number, its rows
// and its first row among the held ones.
type modeCount struct {
	vid   int32
	first int32
	rows  int64
}

// counted counts the held rows a group at a time: each group's distinct values in the
// order they first appear, the group's own from at[g] to at[g+1]. With modes, only
// each group's values of the most rows.
func (a *modeAcc) counted(nGroups int, modes bool) (at []int32, out []modeCount, err error) {
	n := len(a.gid)
	at = make([]int32, nGroups+1)
	for _, g := range a.gid {
		if int(g) >= nGroups || g < 0 {
			return nil, nil, uerr.Internalf("kernel: a mode's group %d of %d", g, nGroups)
		}
		at[g+1]++
	}
	for g := range nGroups {
		at[g+1] += at[g]
	}
	// The rows' numbers by group, each group's in order, and their rows: a counting
	// sort that reads the rows in turn, where gathering each group's would read them
	// at random. One group's are the rows as they are.
	var vids, rows []int32
	if nGroups == 1 {
		vids, rows = a.vid, nil
	} else {
		vids, rows = make([]int32, n), make([]int32, n)
		next := slices.Clone(at[:nGroups])
		for i, g := range a.gid {
			vids[next[g]], rows[next[g]] = a.vid[i], int32(i)
			next[g]++
		}
	}
	// Each group's values counted by number, the counts kept at zero between groups.
	count := make([]int64, a.nv)
	firstAt := make([]int32, a.nv)
	var seen []int32
	starts := make([]int32, nGroups+1)
	for g := range nGroups {
		seen = seen[:0]
		best := int64(0)
		for j := at[g]; j < at[g+1]; j++ {
			v := vids[j]
			if count[v] == 0 {
				seen = append(seen, v)
				firstAt[v] = j
				if rows != nil {
					firstAt[v] = rows[j]
				}
			}
			count[v]++
			best = max(best, count[v])
		}
		for _, v := range seen {
			if !modes || count[v] == best {
				out = append(out, modeCount{vid: v, first: firstAt[v], rows: count[v]})
			}
			count[v] = 0
		}
		starts[g+1] = int32(len(out))
	}
	return starts, out, nil
}

// valuesOf is the values of counts, each its first row's for a float, else its
// number's.
func (a *modeAcc) valuesOf(name string, counts []modeCount) (*data.Column, error) {
	if !a.float {
		if a.nv == 0 {
			return NullColumn(name, a.elem, 0)
		}
		all, err := concatColumn(a.vals, a.nv)
		if err != nil {
			return nil, err
		}
		sel := make([]int32, len(counts))
		for i, c := range counts {
			sel[i] = c.vid
		}
		return Take(all, sel)
	}
	null := int32(-1)
	if a.ints != nil {
		null = a.ints.NullID()
	}
	valid := bitmap.NewBuilder(len(counts))
	if a.elem.Physical().ID() == dtype.TypeFloat32 {
		vs := make([]float32, len(counts))
		for i, c := range counts {
			vs[i] = math.Float32frombits(uint32(a.bits[c.first]))
			valid.Append(c.vid != null)
		}
		return data.NewFixed(name, a.elem, vs, valid.Finish()), nil
	}
	vs := make([]float64, len(counts))
	for i, c := range counts {
		vs[i] = math.Float64frombits(a.bits[c.first])
		valid.Append(c.vid != null)
	}
	return data.NewFixed(name, a.elem, vs, valid.Finish()), nil
}

func (a *modeAcc) Finish(name string, nGroups int) (*data.Column, error) {
	if a.sp != nil {
		return a.finishSpilled(name, nGroups)
	}
	offs, counts, err := a.counted(nGroups, true)
	if err != nil {
		return nil, err
	}
	child, err := a.valuesOf(name, counts)
	if err != nil {
		return nil, err
	}
	return a.list(name, offs, child, nil)
}

// list is the answer: each group's modes, child's values from offs[g] to offs[g+1],
// in the order their values first appear with maintain, ascending, nulls first,
// without. firsts, where given, are their first rows, which order them; else they
// are in that order already.
func (a *modeAcc) list(name string, offs []int32, child *data.Column, firsts []int64) (*data.Column, error) {
	if !a.maintain || firsts != nil {
		perm := make([]int32, child.Len())
		for i := range perm {
			perm[i] = int32(i)
		}
		var by func(x, y int32) int
		if a.maintain {
			by = func(x, y int32) int { return compareInt64(firsts[x], firsts[y]) }
		} else {
			cmp, err := NewComparator([]*data.Column{child}, []SortSpec{{}})
			if err != nil {
				return nil, err
			}
			by = func(x, y int32) int { return cmp(int(x), int(y)) }
		}
		for g := 0; g+1 < len(offs); g++ {
			slices.SortStableFunc(perm[offs[g]:offs[g+1]], by)
		}
		var err error
		if child, err = Take(child, perm); err != nil {
			return nil, err
		}
	}
	// Every group has a row, so a value; an empty list is a window's of none.
	return data.NewList(name, offs, child, bitmap.View{}), nil
}

func compareInt64(x, y int64) int {
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	}
	return 0
}

func (a *modeAcc) NBytes() int64 {
	b := a.keys.NBytes() + int64(cap(a.gid)+cap(a.vid))*4 + int64(cap(a.bits)+cap(a.scratch))*8 +
		colBytes(a.vals)
	if a.ints != nil {
		b += a.ints.NBytes()
	}
	return b
}

// SpillModes counts the held rows into entries, a group at a time, and hands them to
// sp as one run, by group and key: see ModeSpiller. A key is the group-key encoder's
// bytes of the entry's value, which are equal where the values are one value to
// grouping, as the numbers are.
func (a *modeAcc) SpillModes(sp ModeSpill) (bool, error) {
	if a.sp != nil && a.sp != sp {
		return false, uerr.Internalf("kernel: a mode's rows spilled to two places")
	}
	if len(a.gid) == 0 {
		return false, nil
	}
	nGroups := int(slices.Max(a.gid)) + 1
	starts, counts, err := a.counted(nGroups, false)
	if err != nil {
		return false, err
	}
	vals, err := a.valuesOf("v", counts)
	if err != nil {
		return false, err
	}
	enc, err := NewGroupKeyEncoder("mode", []*data.Column{vals})
	if err != nil {
		return false, err
	}
	var arena []byte
	offs := make([]int, len(counts)+1)
	for i := range counts {
		arena = enc.AppendKey(arena, i)
		offs[i+1] = len(arena)
	}
	key := func(i int32) []byte { return arena[offs[i]:offs[i+1]] }
	// Each group's entries sorted by key; the groups are in order already.
	ids := make([]int32, len(counts))
	gids := make([]int32, len(counts))
	for g := range nGroups {
		for i := starts[g]; i < starts[g+1]; i++ {
			ids[i], gids[i] = i, int32(g)
		}
		slices.SortFunc(ids[starts[g]:starts[g+1]], func(x, y int32) int { return bytes.Compare(key(x), key(y)) })
	}
	for lo := 0; lo < len(ids); lo += spillChunk {
		chunk := ids[lo:min(lo+spillChunk, len(ids))]
		keys := make([][]byte, len(chunk))
		rows, first := make([]int64, len(chunk)), make([]int64, len(chunk))
		for i, id := range chunk {
			c := counts[id]
			keys[i], rows[i], first[i] = key(id), c.rows, a.seen+int64(c.first)
		}
		part, err := Take(vals, chunk)
		if err != nil {
			return false, err
		}
		if err := sp.Put(gids[lo:lo+len(chunk)], keys, rows, first, part); err != nil {
			return false, err
		}
	}
	a.seen += int64(len(a.gid))
	a.ints, a.keys, a.vals, a.nv, a.null = nil, KeyTable{}, nil, 0, -1
	a.gid, a.vid, a.bits, a.scratch = nil, nil, nil, nil
	a.sp = sp
	return true, sp.EndRun()
}

// finishSpilled is Finish for a mode whose entries spilled: the rest are put too, and
// the runs read back, once to find each group's modes and once to take their values.
func (a *modeAcc) finishSpilled(name string, nGroups int) (*data.Column, error) {
	if _, err := a.SpillModes(a.sp); err != nil {
		return nil, err
	}
	// First: each group's most rows, and its modes' first rows, in order of key. A
	// key's entries are a run; its first row is the least of theirs.
	best := make([]int64, nGroups)
	var (
		modeGid   []int32
		modeFirst []int64
		mark      int // where the open group's modes start
		cur       = int32(-1)
		prev      []byte
		run, at   int64
	)
	closeRun := func() {
		if cur < 0 {
			return
		}
		if run > best[cur] {
			best[cur] = run
			modeGid, modeFirst = modeGid[:mark], modeFirst[:mark]
		}
		if run == best[cur] {
			modeGid, modeFirst = append(modeGid, cur), append(modeFirst, at)
		}
	}
	err := a.sp.Scan(func(gids []int32, keys data.StringAccessor, rows, first []int64, _ *data.Column) error {
		for i, g := range gids {
			if int(g) >= nGroups {
				return uerr.Internalf("kernel: a spilled mode's group %d of %d", g, nGroups)
			}
			k := keys.Bytes(i)
			if g != cur || !bytes.Equal(k, prev) {
				closeRun()
				if g != cur {
					cur, mark = g, len(modeGid)
				}
				prev = append(prev[:0], k...)
				run, at = 0, first[i]
			}
			run += rows[i]
			at = min(at, first[i])
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	closeRun()
	// Second: the modes' values, from the entries whose first rows they are, which
	// come in the same order.
	next := 0
	var parts []*data.Column
	err = a.sp.Ordered(func(gids []int32, _ data.StringAccessor, _, first []int64, vals *data.Column) error {
		var take []int32
		for i, g := range gids {
			if next < len(modeGid) && modeGid[next] == g && modeFirst[next] == first[i] {
				take = append(take, int32(i))
				next++
			}
		}
		if len(take) == 0 {
			return nil
		}
		part, err := Take(vals, take)
		if err != nil {
			return err
		}
		parts = append(parts, part)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if next != len(modeGid) {
		return nil, uerr.Internalf("kernel: a spilled mode found %d of its %d values", next, len(modeGid))
	}
	offs := make([]int32, nGroups+1)
	for _, g := range modeGid {
		offs[g+1]++
	}
	for g := range nGroups {
		offs[g+1] += offs[g]
	}
	var child *data.Column
	if len(parts) == 0 {
		child, err = NullColumn(name, a.elem, 0)
	} else {
		child, err = concatColumn(parts, len(modeGid))
	}
	if err != nil {
		return nil, err
	}
	return a.list(name, offs, child, modeFirst)
}

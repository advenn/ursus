package kernel

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// The k-way merge core: comparing rows that live in DIFFERENT column sets, and
// the heap that walks several sorted runs at once.
//
// # Why NewComparator cannot do this
//
// NewComparator builds a func(i, j int) int over ONE column set: withNulls closes
// over a single column's validity and valueComparator over a single column's
// accessor, and both index the same columns. A merge has to compare row i of run
// A against row j of run B, which that shape cannot express at all.
//
// The rules the two share — null placement before direction, ursus's total float
// order — live in nullRule, direct and OrderKeyF64 rather than being restated
// here. What is genuinely new is only the indirection through a run index.

// CrossComparator orders row ia of run ra against row ib of run rb.
type CrossComparator func(ra, ia, rb, ib int) int

// RunSet holds the key columns each run of a merge currently has resident.
//
// A run's columns can be REPLACED, which is the whole reason this is a type and
// not a closure: a merge holds one batch per run, and when a run's batch is
// exhausted the caller loads the next one and calls Set again. Accessors are
// resolved once per Set — once per batch — rather than once per comparison, so
// comparison stays O(keys).
type RunSet struct {
	specs []SortSpec
	types []dtype.DataType

	keys   []crossKey      // one per sort key, holding every run's values
	valid  [][]bitmap.View // [run][key]
	first  []int           // per key: the "null vs value" answer
	second []int           // per key: the "value vs null" answer
}

// NewRunSet builds a run set for nruns runs ordered by the given key types.
//
// The types are required up front rather than inferred from the first Set
// because a run can be empty, and a comparator that only exists once some run has
// delivered a batch would be a lifetime puzzle for no benefit.
func NewRunSet(nruns int, types []dtype.DataType, specs []SortSpec) (*RunSet, error) {
	if len(types) != len(specs) {
		return nil, uerr.Internalf("kernel: %d key types but %d sort specs",
			len(types), len(specs))
	}
	s := &RunSet{
		specs:  specs,
		types:  types,
		keys:   make([]crossKey, len(types)),
		valid:  make([][]bitmap.View, nruns),
		first:  make([]int, len(specs)),
		second: make([]int, len(specs)),
	}
	for k, t := range types {
		ck, err := newCrossKey(nruns, t)
		if err != nil {
			return nil, err
		}
		s.keys[k] = ck
		s.first[k], s.second[k] = nullRule(specs[k])
	}
	for r := range s.valid {
		s.valid[r] = make([]bitmap.View, len(types))
	}
	return s, nil
}

// Set installs run's currently resident key columns.
func (s *RunSet) Set(run int, cols []*data.Column) error {
	if len(cols) != len(s.keys) {
		return uerr.Internalf("kernel: run %d has %d key columns, want %d",
			run, len(cols), len(s.keys))
	}
	for k, c := range cols {
		if c.DType() != s.types[k] {
			return uerr.Internalf("kernel: run %d key %d is %s, want %s",
				run, k, c.DType(), s.types[k])
		}
		if err := s.keys[k].set(run, c); err != nil {
			return err
		}
		s.valid[run][k] = c.Validity()
	}
	return nil
}

// Compare orders row ia of run ra against row ib of run rb, lexicographically
// over the keys.
func (s *RunSet) Compare(ra, ia, rb, ib int) int {
	for k := range s.keys {
		va := s.valid[ra][k].Get(ia)
		vb := s.valid[rb][k].Get(ib)
		if !va || !vb {
			switch {
			case !va && !vb:
				continue // equal on this key; fall through to the next
			case !va:
				return s.first[k]
			default:
				return s.second[k]
			}
		}
		if r := s.keys[k].cmp(ra, ia, rb, ib); r != 0 {
			return direct(s.specs[k], r)
		}
	}
	return 0
}

// NewCrossComparator builds a comparator over fixed run column sets.
//
// This is the shape a test can drive: comparing two rows through it must agree
// with NewComparator over the concatenation of the runs, which is what pins the
// direction and null-placement rules to the ones the in-memory sort already uses.
func NewCrossComparator(runs [][]*data.Column, specs []SortSpec) (CrossComparator, error) {
	if len(runs) == 0 {
		return nil, uerr.Internalf("kernel: a cross comparator needs at least one run")
	}
	types := make([]dtype.DataType, len(runs[0]))
	for k, c := range runs[0] {
		types[k] = c.DType()
	}
	s, err := NewRunSet(len(runs), types, specs)
	if err != nil {
		return nil, err
	}
	for r, cols := range runs {
		if err := s.Set(r, cols); err != nil {
			return nil, err
		}
	}
	return s.Compare, nil
}

// --- per-key value access ------------------------------------------------------

// crossKey holds one sort key's values for every run.
//
// It is an interface rather than a closure because the value slices have to be
// REPLACED per run when a run advances to its next batch, and a closure would
// have captured them.
type crossKey interface {
	set(run int, c *data.Column) error
	cmp(ra, ia, rb, ib int) int // value comparison only: no nulls, no direction
}

func newCrossKey(nruns int, dt dtype.DataType) (crossKey, error) {
	// The dispatch order mirrors valueComparator exactly: Bool has its own
	// representation, then anything with string storage, then the physical type.
	if dt.ID() == dtype.TypeBool {
		return &boolKey{v: make([]bitmap.View, nruns)}, nil
	}
	if dt.HasStringStorage() {
		return &strKey{v: make([]data.StringAccessor, nruns)}, nil
	}
	switch dt.Physical().ID() {
	case dtype.TypeInt8:
		return newPrimKey[int8](nruns), nil
	case dtype.TypeInt16:
		return newPrimKey[int16](nruns), nil
	case dtype.TypeInt32:
		return newPrimKey[int32](nruns), nil
	case dtype.TypeInt64:
		return newPrimKey[int64](nruns), nil
	case dtype.TypeUint8:
		return newPrimKey[uint8](nruns), nil
	case dtype.TypeUint16:
		return newPrimKey[uint16](nruns), nil
	case dtype.TypeUint32:
		return newPrimKey[uint32](nruns), nil
	case dtype.TypeUint64:
		return newPrimKey[uint64](nruns), nil
	case dtype.TypeFloat32:
		return &f32Key{v: make([][]float32, nruns)}, nil
	case dtype.TypeFloat64:
		return &f64Key{v: make([][]float64, nruns)}, nil
	case dtype.TypeInt128:
		return &i128Key{v: make([][]i128.Int128, nruns)}, nil
	default:
		return nil, uerr.New(uerr.KindUnsupported, "sort",
			"cannot order a %s column", dt)
	}
}

type primKey[T data.Primitive] struct{ v [][]T }

func newPrimKey[T data.Primitive](n int) *primKey[T] {
	return &primKey[T]{v: make([][]T, n)}
}

func (k *primKey[T]) set(run int, c *data.Column) error {
	v, err := data.Values[T](c)
	if err != nil {
		return err
	}
	k.v[run] = v
	return nil
}

func (k *primKey[T]) cmp(ra, ia, rb, ib int) int {
	a, b := k.v[ra][ia], k.v[rb][ib]
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

type f64Key struct{ v [][]float64 }

func (k *f64Key) set(run int, c *data.Column) error {
	v, err := data.Values[float64](c)
	if err != nil {
		return err
	}
	k.v[run] = v
	return nil
}

// cmp compares ORDER KEYS, not floats, so NaN is orderable and -0.0 is not
// distinguishable from +0.0 — the same total order the within-run comparator uses.
func (k *f64Key) cmp(ra, ia, rb, ib int) int {
	a, b := OrderKeyF64(k.v[ra][ia]), OrderKeyF64(k.v[rb][ib])
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

type f32Key struct{ v [][]float32 }

func (k *f32Key) set(run int, c *data.Column) error {
	v, err := data.Values[float32](c)
	if err != nil {
		return err
	}
	k.v[run] = v
	return nil
}

func (k *f32Key) cmp(ra, ia, rb, ib int) int {
	a, b := OrderKeyF32(k.v[ra][ia]), OrderKeyF32(k.v[rb][ib])
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

type i128Key struct{ v [][]i128.Int128 }

func (k *i128Key) set(run int, c *data.Column) error {
	v, err := data.Values[i128.Int128](c)
	if err != nil {
		return err
	}
	k.v[run] = v
	return nil
}

func (k *i128Key) cmp(ra, ia, rb, ib int) int { return k.v[ra][ia].Cmp(k.v[rb][ib]) }

type strKey struct{ v []data.StringAccessor }

func (k *strKey) set(run int, c *data.Column) error {
	k.v[run] = c.Strings()
	return nil
}

func (k *strKey) cmp(ra, ia, rb, ib int) int {
	a, b := k.v[ra].Get(ia), k.v[rb].Get(ib)
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

type boolKey struct{ v []bitmap.View }

func (k *boolKey) set(run int, c *data.Column) error {
	k.v[run] = c.Bools()
	return nil
}

func (k *boolKey) cmp(ra, ia, rb, ib int) int {
	a, b := k.v[ra].Get(ia), k.v[rb].Get(ib)
	switch {
	case a == b:
		return 0
	case !a:
		return -1 // false < true
	default:
		return 1
	}
}

// --- the merge heap ------------------------------------------------------------

// Merger emits (run, row) pairs in sorted order across several sorted runs.
//
// # It never reads a run itself
//
// The caller pushes a run's current head, and when that head is popped the caller
// decides what comes next — the next row of the resident batch, the first row of
// a batch loaded from disk, or nothing because the run is finished. Keeping the
// disk out of the kernel layer is what lets the merge be tested against ArgSort
// with no files involved at all.
//
// # Stability
//
// ArgSort is stable and the sort's doc calls that load-bearing rather than a
// nicety. A k-way merge is stable only if a tie between two runs takes from the
// run holding the EARLIER input rows — so ties break on the run index, ascending,
// and runs must be created in input order. Within a run, rows are already in
// stable order and the caller always advances a run's head by one, so intra-run
// order is preserved for free.
//
// This is the same hazard ArgTopK needed two separate fixes for, and it is
// invisible on data without ties, which is why the merge is tested against inputs
// that are mostly ties.
type Merger struct {
	rs *RunSet
	h  []MergeCursor
}

// MergeCursor is one position in one run.
type MergeCursor struct {
	Run int
	Row int
}

// NewMerger returns an empty merger over rs. Seed it with Push.
func NewMerger(rs *RunSet) *Merger { return &Merger{rs: rs} }

// Len returns how many cursors are live.
func (m *Merger) Len() int { return len(m.h) }

// Push adds a cursor.
func (m *Merger) Push(run, row int) {
	m.h = append(m.h, MergeCursor{Run: run, Row: row})
	m.up(len(m.h) - 1)
}

// Pop removes and returns the smallest cursor, or ok=false when none is left.
func (m *Merger) Pop() (MergeCursor, bool) {
	if len(m.h) == 0 {
		return MergeCursor{}, false
	}
	top := m.h[0]
	last := len(m.h) - 1
	m.h[0] = m.h[last]
	m.h = m.h[:last]
	if last > 0 {
		m.down(0)
	}
	return top, true
}

func (m *Merger) less(i, j int) bool {
	a, b := m.h[i], m.h[j]
	if c := m.rs.Compare(a.Run, a.Row, b.Run, b.Row); c != 0 {
		return c < 0
	}
	return a.Run < b.Run // the stability tie-break
}

func (m *Merger) up(i int) {
	for i > 0 {
		p := (i - 1) / 2
		if !m.less(i, p) {
			return
		}
		m.h[i], m.h[p] = m.h[p], m.h[i]
		i = p
	}
}

func (m *Merger) down(i int) {
	n := len(m.h)
	for {
		l, r := 2*i+1, 2*i+2
		small := i
		if l < n && m.less(l, small) {
			small = l
		}
		if r < n && m.less(r, small) {
			small = r
		}
		if small == i {
			return
		}
		m.h[i], m.h[small] = m.h[small], m.h[i]
		i = small
	}
}

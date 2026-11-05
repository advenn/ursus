package kernel

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// --- first / last -------------------------------------------------------------

// positionAcc implements First and Last.
//
// POSITIONAL: it returns the first (or last) value in the group, which may itself
// be null. First is a selection, not a reduction — skipping nulls would mean
// First(a) and First(b) could come from different rows, which is precisely what
// people use them together for. DuckDB and Polars both agree on this default.
//
// # A row index a group, and one gather a batch (step 168)
//
// It kept a one-row Column per group: a map and a Take per group each batch, and a
// Finish that concatenated one part per group. That is the shape implodeAcc records
// costing 93 seconds on h2o gb7 at step 20; First and Last had kept it, at 0.8 and
// 1.2 s over 182,000 groups where Sum took 33 ms.
//
// Now each batch gathers, in one Take, the rows it decides: a group's first row for
// First, and a group's last row in the batch for Last. Each group keeps the index of
// its row in the concatenation of those gathers, and Finish gathers once. Take keeps
// the dtype, so every type works as it does for implode.
//
// Last supersedes a group's row in every batch the group appears in, and a
// superseded row is garbage in parts. When the garbage passes twice the live rows,
// and 64k, the live rows are gathered into one part again, so what Last holds stays
// O(groups).
type positionAcc struct {
	out  dtype.DataType
	last bool

	parts []*data.Column // the rows gathered, a part a batch, in order
	at    []int32        // per group: its row in the concatenation of parts, or NullIndex
	base  int32          // rows in parts, which is where the next part starts
	live  int            // groups with a row
	dead  int            // rows in parts no group points at (Last only)

	// seen[g] is the number of the batch that last picked group g, so a batch picks
	// a group once; batch counts the batches.
	seen  []int32
	batch int32
}

func (a *positionAcc) Reserve(n int) {
	a.at = Extend(a.at, n, NullIndex)
	a.seen = Extend(a.seen, n, 0)
}

func (a *positionAcc) AddBatch(groups []int32, col *data.Column) error {
	a.batch++
	var pick, who []int32 // rows of col to gather, and the group each is for
	if a.last {
		// Walked backwards, so a group's first sighting is its last row.
		for i := len(groups) - 1; i >= 0; i-- {
			if g := groups[i]; a.seen[g] != a.batch {
				a.seen[g] = a.batch
				pick, who = append(pick, int32(i)), append(who, g)
			}
		}
	} else {
		for i, g := range groups {
			if a.at[g] == NullIndex && a.seen[g] != a.batch {
				a.seen[g] = a.batch
				pick, who = append(pick, int32(i)), append(who, g)
			}
		}
	}
	return a.place(col, pick, who)
}

// place gathers col's rows pick as a part, each the row of the group who names at
// the same index, superseding any row the group had. MinBy and MaxBy place theirs
// through it too (byAcc).
func (a *positionAcc) place(col *data.Column, pick, who []int32) error {
	if len(pick) == 0 {
		return nil
	}
	part, err := Take(col, pick)
	if err != nil {
		return err
	}
	a.parts = append(a.parts, part)
	for j, g := range who {
		if a.at[g] == NullIndex {
			a.live++
		} else {
			a.dead++
		}
		a.at[g] = a.base + int32(j)
	}
	a.base += int32(len(pick))
	if a.dead > 1<<16 && a.dead > 2*a.live {
		return a.compact()
	}
	return nil
}

// compact gathers the live rows into one part, in group order.
func (a *positionAcc) compact() error {
	all, err := concatColumn(a.parts, int(a.base))
	if err != nil {
		return err
	}
	sel := make([]int32, 0, a.live)
	for g, r := range a.at {
		if r != NullIndex {
			a.at[g] = int32(len(sel))
			sel = append(sel, r)
		}
	}
	part, err := Take(all, sel)
	if err != nil {
		return err
	}
	a.parts, a.base, a.dead = []*data.Column{part}, int32(len(sel)), 0
	return nil
}

// Merge folds another accumulator's groups in, its rows after this one's.
//
// Merge assumes `other` saw a LATER slice of the input than this one. First keeps
// what it has; Last takes the newer value.
//
// That assumption is why First and Last are ORDER-DEPENDENT and why the parallel
// aggregation driver refuses to use it: its dispatcher is round-robin, so worker 1's
// second batch precedes worker 0's Nth and neither sink holds a contiguous portion.
// See expr.AggOp.IsOrderDependent and physical.aggCanParallelise.
func (a *positionAcc) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*positionAcc)
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into positionAcc", other)
	}
	shift := a.adopt(o)
	a.Reserve(mergeCap(remap, len(o.at)))
	mergeEach(remap, len(o.at), func(dst, src int) {
		r := o.at[src]
		if r == NullIndex {
			return
		}
		switch {
		case a.at[dst] == NullIndex:
			a.live++
			a.at[dst] = r + shift
		case a.last:
			a.dead++
			a.at[dst] = r + shift
		default:
			a.dead++ // First keeps its own; o's row is never read
		}
	})
	return nil
}

// adopt appends o's parts after this one's, with the rows none of o's groups point
// at, and answers the shift that turns o's row indices into this one's. Which of
// o's rows a group then points at is the caller's choice.
func (a *positionAcc) adopt(o *positionAcc) int32 {
	shift := a.base
	a.parts = append(a.parts, o.parts...)
	a.base += o.base
	a.dead += o.dead
	return shift
}

func (a *positionAcc) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	if len(a.parts) == 0 {
		return NullColumn(name, a.out, nGroups)
	}
	all, err := concatColumn(a.parts, int(a.base))
	if err != nil {
		return nil, err
	}
	// A group with no row gathers NullIndex, a null.
	col, err := Take(all, a.at[:nGroups])
	if err != nil {
		return nil, err
	}
	return col.Rename(name).WithDType(a.out), nil
}

// --- implode ---------------------------------------------------------------------

// implodeAcc collects every value of a group into a list.
//
// # It stores ROW INDICES, not values, and that is the whole design
//
// The obvious shape is quantileAcc's — one typed slice per group — and it cannot be
// written once. `[][]float64` serves quantile because a quantile is always a float;
// implode is defined for every type, so a per-group typed slice means the four-way
// split newExtremum spends thirty lines justifying, and six or seven arms rather
// than four.
//
// So: retain the input column ONCE PER BATCH, and remember which row of the
// concatenation each group took. Finish is then one concatColumn and one Take, and
// listRebuild's sentence applies verbatim — "THE ELEMENT TYPE NEVER APPEARS. Take
// does the gather, so List(String) works for exactly the reason List(Int64) does."
//
// This is not step 20's per-group *data.Column in new clothes. That one allocated a
// map per batch and a Take per group PER BATCH — millions of one-row Columns, and 93
// seconds on h2o gb7. Here the Columns are one per batch and the Takes are one in
// total; the per-group state is four bytes a row, less than quantileAcc's eight.
//
// # And it is the only shape that is correct for Enum
//
// data.NewList DERIVES its element type from the child and offers no WithDType
// override, deliberately: a declared type that disagrees with the data "would be a
// lie no caller could detect". extremumStr needs exactly that override to keep an
// Enum an Enum over string storage, and an implode built on typed slices would need
// it too and could not have it. Take preserves the dtype on every arm, so Enum,
// Decimal, Datetime, Struct and List-of-List come out right without a case each.
type implodeAcc struct {
	// elem is the ELEMENT type, and it is needed in exactly one place: an
	// accumulator that saw no batch at all has nothing to derive a child column
	// from, and data.NewList takes its type from the child. Every other path gets
	// the type from the data, which is the whole point of gathering with Take.
	elem  dtype.DataType
	parts []*data.Column // one per BATCH
	rows  [][]int32      // per group: indices into the concatenation of parts
	base  int32          // rows retained so far, which is where the next batch starts
}

func (a *implodeAcc) Reserve(n int) { a.rows = Extend(a.rows, n, nil) }

// AddBatch is the one place implode deviates from every other aggregate: it does
// NOT skip nulls.
//
// Every other accumulator drops them, which is the SQL rule and is right for a
// reduction — the sum of an all-null group is null, not zero. Implode is a
// SELECTION, like First and Last, so a null is a value that occupies a slot. An
// all-null group implodes to [null, null, null]: a non-null list OF nulls, which is
// neither the null list nor the empty one. listfn.go names that third state —
// "Null ELEMENTS are a third thing again: they occupy a slot, so len counts them."
func (a *implodeAcc) AddBatch(groups []int32, col *data.Column) error {
	a.parts = append(a.parts, col)
	for i, g := range groups {
		a.rows[g] = append(a.rows[g], a.base+int32(i))
	}
	a.base += int32(col.Len())
	return nil
}

// Merge folds another accumulator's groups in, shifting its indices past this one's
// retained rows.
//
// The shift is argExtremumAcc's, and so is the precondition: `other` must have seen
// a strictly LATER portion of the input, because a list's order is its data and this
// appends other's elements after this one's. IsOrderDependent declares implode, so
// the parallel driver never produces two accumulators over interleaved portions —
// and spilling partitions by key, so one sink sees a whole group in arrival order.
//
// Nothing calls it today, therefore. It is written and tested anyway, for the reason
// the Accumulator doc gives: "a Merge written later against forgotten invariants is
// a Merge that is wrong."
func (a *implodeAcc) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*implodeAcc)
	if !ok {
		return uerr.Internalf("kernel: implode merge got %T", other)
	}
	shift := a.base
	a.parts = append(a.parts, o.parts...)
	a.base += o.base

	a.Reserve(mergeCap(remap, len(o.rows)))
	mergeEach(remap, len(o.rows), func(dst, src int) {
		for _, r := range o.rows[src] {
			a.rows[dst] = append(a.rows[dst], r+shift)
		}
	})
	return nil
}

// Finish gathers once.
//
// One concatColumn over the batches and one Take over the per-group indices laid end
// to end, which is also the CSR the offsets describe. A group with no rows cannot
// arise — an id exists because a row created it — but the zero-group case does, and
// an empty child with a single zero offset is the right answer for it.
func (a *implodeAcc) Finish(name string, nGroups int) (*data.Column, error) {
	sel := make([]int32, 0, a.base)
	offs := make([]int32, 0, nGroups+1)
	offs = append(offs, 0)
	for g := range nGroups {
		if g < len(a.rows) {
			sel = append(sel, a.rows[g]...)
		}
		offs = append(offs, int32(len(sel)))
	}

	// No batch at all: a global aggregate over an empty frame. There is nothing to
	// concatenate and nothing to gather, and NullColumn builds the typed empty
	// child that data.NewList needs in order to know what the list is a list OF.
	var child *data.Column
	if len(a.parts) == 0 {
		var err error
		if child, err = NullColumn(name, a.elem, 0); err != nil {
			return nil, err
		}
	} else {
		all, err := concatColumn(a.parts, int(a.base))
		if err != nil {
			return nil, err
		}
		if child, err = Take(all, sel); err != nil {
			return nil, err
		}
	}
	// A zero validity view means all-set, which is what implode wants: every group
	// has at least one row, so no list is ever null. An EMPTY list is still not a
	// null one — the global aggregate over an empty frame is the only row that can
	// be empty, and empty is the right answer there.
	return data.NewList(name, offs, child, bitmap.View{}), nil
}

// NBytes reports the retained columns as well as the index slices.
//
// The columns are the larger term by far — four bytes an index against the whole
// value — and leaving them out would put implode's state at a twentieth of its real
// size, so the O(rows) refusal would fire far too late or not at all. Nothing in a
// correctness test would notice.
func (a *implodeAcc) NBytes() int64 {
	n := int64(cap(a.rows)) * 24 // one slice header per group
	for _, r := range a.rows {
		n += int64(cap(r)) * 4
	}
	return n + colBytes(a.parts)
}

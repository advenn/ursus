package data

import (
	"iter"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// Batch is a horizontal slice of a frame: N equal-length columns with a schema.
//
// It is the unit that flows between physical operators. Batch size is a tuning
// knob rather than a semantic one — the same query must produce the same answer
// at any batch size, and the test suite checks that by varying it.
type Batch struct {
	schema *dtype.Schema
	cols   []*Column
	rows   int
}

// NewBatch builds a batch, checking that the columns match the schema in count,
// order, name and type.
//
// The check is not paranoia: a mismatch here means the evaluator produced
// something other than what the plan's schema resolution promised, which is
// exactly the class of bug that would otherwise surface as corrupted output far
// downstream. Failing at the boundary makes it a one-line diagnosis.
// CheckNonNullable turns on the fifth boundary check: a column whose field says
// Nullable: false must actually hold no nulls.
//
// # Why it is a toggle rather than one of the other four
//
// The four checks in NewBatch are unconditional because they are O(columns) on
// values already in registers. This one may cost a popcount per column per batch —
// bitmap.Builder.Finish never returns the no-storage all-set form, so a scanned or
// concatenated column carries a materialised bitmap even when nothing is null, and
// the O(1) fast path misses exactly there.
//
// It is a package var rather than a CollectOption because the option would have to
// reach here from the root package, and nothing does: WithVerify stops at
// Optimizer.Verify, and physical.Options carries only BatchSize, Threads and Budget.
// Threading a flag to forty-six construction sites — several of them in kernel and
// source, which never see Options — would be a large change to buy a check that
// only tests run. Set it once from a test binary's init, never during a query.
//
// # What a violation means
//
// Field.Nullable is a permission — "a non-nullable field is one where the engine
// may skip validity handling entirely" — and the optimizer already acts on it:
// rule_simplify's boolIdentity folds `x AND lit(false)` to `lit(false)` EXACTLY
// when x is declared non-nullable. So a lie here is a potentially unsound rewrite,
// not a cosmetic mismatch, and it will not show up in the final frame of the query
// that told it.
var CheckNonNullable bool

// checkNonNullable is the fifth clause, shared by both constructors.
func checkNonNullable(schema *dtype.Schema, cols []*Column) error {
	for i, c := range cols {
		if c == nil || i >= schema.Len() {
			continue
		}
		f := schema.Field(i)
		// IsAllSet is O(1) and answers "definitely no nulls"; it is a fast path and
		// never a decision, which is what its own doc insists on. A materialised
		// bitmap that happens to be all ones returns false here and needs the count.
		if f.Nullable || c.Validity().IsAllSet() {
			continue
		}
		if n := c.NullCount(); n > 0 {
			return uerr.Internalf(
				"data: column %q (position %d) is declared non-nullable and holds %d nulls of %d",
				f.Name, i, n, c.Len())
		}
	}
	return nil
}

func NewBatch(schema *dtype.Schema, cols []*Column) (*Batch, error) {
	if schema.Len() != len(cols) {
		return nil, uerr.Internalf("data: batch has %d columns but schema has %d",
			len(cols), schema.Len())
	}
	rows := -1
	for i, c := range cols {
		f := schema.Field(i)
		if c.Name() != f.Name {
			return nil, uerr.Internalf("data: batch column %d is named %q, schema says %q",
				i, c.Name(), f.Name)
		}
		if c.DType() != f.Type {
			return nil, uerr.Internalf("data: batch column %q is %s, schema says %s",
				c.Name(), c.DType(), f.Type)
		}
		if rows == -1 {
			rows = c.Len()
		} else if c.Len() != rows {
			return nil, uerr.Internalf(
				"data: batch column %q has %d rows, but column %q has %d",
				c.Name(), c.Len(), cols[0].Name(), rows)
		}
	}
	if rows == -1 {
		rows = 0
	}
	if CheckNonNullable {
		if err := checkNonNullable(schema, cols); err != nil {
			return nil, err
		}
	}
	return &Batch{schema: schema, cols: cols, rows: rows}, nil
}

// NewBatchRows builds a batch with an explicit row count.
//
// It exists for the zero-column case — a frame can legitimately have rows but no
// columns after `Select()` with nothing — but six operators also use it with
// populated columns, because it is the constructor that takes a row count. Those
// six therefore skip the four checks NewBatch performs, which is worth knowing:
// explode, unnest, unpivot, hstack, the nothing-selected filter path and the
// concat-of-zero-batches path are all unvalidated on names and types.
//
// The nullability check is applied here anyway, because leaving it out would make
// the instrument blind to a third of the engine and blind in exactly the newest
// operators. It PANICS rather than returning an error because this signature has no
// error to return and an invariant violation is not a condition a caller can
// handle — and because it only ever runs when CheckNonNullable is on.
func NewBatchRows(schema *dtype.Schema, cols []*Column, rows int) *Batch {
	if CheckNonNullable {
		if err := checkNonNullable(schema, cols); err != nil {
			panic(err)
		}
	}
	return &Batch{schema: schema, cols: cols, rows: rows}
}

// Schema returns the batch's schema.
func (b *Batch) Schema() *dtype.Schema { return b.schema }

// Rows returns the number of rows.
func (b *Batch) Rows() int { return b.rows }

// NumCols returns the number of columns.
func (b *Batch) NumCols() int { return len(b.cols) }

// Column returns the i'th column.
func (b *Batch) Column(i int) *Column { return b.cols[i] }

// Columns returns all columns. The slice is shared and must not be mutated.
func (b *Batch) Columns() []*Column { return b.cols }

// ByName returns the named column.
func (b *Batch) ByName(name string) (*Column, bool) {
	i := b.schema.IndexOf(name)
	if i < 0 {
		return nil, false
	}
	return b.cols[i], true
}

// All iterates the columns in order.
func (b *Batch) All() iter.Seq2[int, *Column] {
	return func(yield func(int, *Column) bool) {
		for i, c := range b.cols {
			if !yield(i, c) {
				return
			}
		}
	}
}

// Slice returns rows [offset, offset+length) of every column.
func (b *Batch) Slice(offset, length int) *Batch {
	cols := make([]*Column, len(b.cols))
	for i, c := range b.cols {
		cols[i] = c.Slice(offset, length)
	}
	return &Batch{schema: b.schema, cols: cols, rows: length}
}

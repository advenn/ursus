package kernel

import (
	"context"

	"github.com/advenn/ursus/internal/data"
)

// ColumnUDF is a user-supplied kernel with its type parameters erased.
//
// It lives here rather than in internal/expr for one reason: internal/expr sits at
// the same import level as internal/data and so cannot name *data.Column at all.
// expr.UDFImpl is the marker that lets the node hold one of these without seeing
// it; this package (level 30) may import data, so it can declare the concrete type.
// internal/physical asserts it back out to call it.
//
// The signature takes the output NAME rather than deriving it, because the column
// a UDF produces is named after the column it consumed — leftmost-column-wins — and
// expr.UDF.Field has already committed to that name in the schema.
//
// A ColumnUDF must be safe to call from several goroutines at once. That is not a
// preference: parallelise hands any Select or Filter to runtime.NumCPU() workers by
// structure, and there is no operator-level flag to decline. The public MapElements
// and MapBatches say so in their own docs, where a user will see it.
type ColumnUDF func(ctx context.Context, in *data.Column, name string) (*data.Column, error)

// UDFKernel marks this as an expr.UDFImpl. Exported because the marker cannot be
// sealed: expr may not import data, so the implementation has to live out here.
func (ColumnUDF) UDFKernel() {}

// WindowMap is rolling_map's Go function with its type parameters erased (step 338):
// it answers each row of col from its window. parts yields each partition's rows in
// the window's order and each row's window, rows[lo[i]:hi[i]] for rows[i]; a window
// of fewer than minN values is null. The answer is a column of col's length, named
// name. As a ColumnUDF it may be called from several goroutines at once.
type WindowMap func(ctx context.Context, col *data.Column, name string,
	parts func(yield func(rows, lo, hi []int32) bool), minN int) (*data.Column, error)

// UDFKernel marks this as an expr.UDFImpl, as ColumnUDF's does.
func (WindowMap) UDFKernel() {}

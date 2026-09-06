package kernel

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// Struct kernels: the `.struct` namespace.
//
// # A null struct is not a struct of nulls
//
// The two hold the same field values — none — and differ only in the struct's own
// validity bit, the same shape of distinction an empty list has from a null list.
// Reading a field answers null for both, and correctly so: there is no age either
// way. What separates them is visible on the struct itself, which is why the
// difference has to survive the read rather than be reconstructed here.
//
// # field is a projection, not a gather
//
// A struct's fields ARE columns, each already as long as the struct and addressed by
// the same row number. So `field(name)` hands one back rather than building anything,
// and costs one rename.
//
// The consequence worth stating: the returned column carries the FIELD's validity,
// not the struct's. For a null struct row both are false — the reader sets the field
// null wherever the group is absent, because a definition level below the group's is
// below the field's too — so no post-mask is needed. That is a property of how the
// levels work rather than an accident, and TestStructSemantics pins the row.

// StructCall applies a struct function to a column.
//
// The output type is not passed in, unlike ListCall's: a field's type comes from the
// field itself, and expr.ResolveCall has already refused a name the struct does not
// have. Taking it as an argument would be a second copy of that decision.
func StructCall(fn expr.CallFn, name string, c *data.Column, args []any) (*data.Column, error) {
	if c.DType().ID() != dtype.TypeStruct {
		return nil, uerr.Internalf("kernel: %s on a %s column", fn, c.DType())
	}
	switch fn {
	case expr.FnStructField:
		want, ok := argString(args, 0)
		if !ok {
			return nil, uerr.Internalf("kernel: struct.field has no name argument")
		}
		f, ok := c.Field(want)
		if !ok {
			// Unreachable through the planner, which resolves the name against the
			// schema first. Reachable if a column's fields ever disagree with its
			// declared type, which is worth naming rather than panicking on.
			return nil, uerr.Internalf("kernel: no field %q in %s", want, c.DType())
		}
		return f.Rename(name), nil
	}
	return nil, uerr.Internalf("kernel: unknown struct call %s", fn)
}

package kernel

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// ArrCall evaluates an `.arr` call (step 341). An Array is held as a List is, each
// of its rows its size, so to_list is the same column under the List's type.
func ArrCall(fn expr.CallFn, name string, out dtype.DataType, c *data.Column) (*data.Column, error) {
	if c.DType().ID() != dtype.TypeArray {
		return nil, uerr.Internalf("kernel: %s on a %s column", fn, c.DType())
	}
	switch fn {
	case expr.FnArrToList:
		return c.WithDType(out).Rename(name), nil
	}
	return nil, uerr.Internalf("kernel: no kernel for %s", fn)
}

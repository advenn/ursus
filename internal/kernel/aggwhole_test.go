package kernel_test

import (
	"fmt"
	"math"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
)

// wholeBatches are two sets of batches of t, each fed to accumulators of its own:
// ties, and one with nulls and values at its type's ends, one every row of which is
// null, one with no null and the ends, one plain, and one empty.
func wholeBatches(t *testing.T, typ dtype.DataType) [][]*data.Column {
	t.Helper()
	const n = 1000
	vb := bitmap.NewBuilder(n)
	for i := range n {
		vb.Append(i%11 != 4)
	}
	valid := vb.Finish()
	ints := make([]int64, n)
	for i := range ints {
		ints[i] = int64(i*7919%2001) - 1000
	}
	mk := func(name string, valid bitmap.View, edge bool) *data.Column {
		switch typ.Physical().ID() {
		case dtype.TypeBool:
			b := bitmap.NewBuilder(n)
			for i := range n {
				b.Append(ints[i]%3 == 0)
			}
			return data.NewBool(name, b.Finish(), valid)
		case dtype.TypeFloat64:
			v := make([]float64, n)
			for i, x := range ints {
				v[i] = float64(x) * 0.1
			}
			if edge {
				// Sums that round by their order: a float's must add as a row at a time.
				v[3], v[5], v[7], v[9] = 1e16, math.Copysign(0, -1), 3.3e-5, -1e16+1
			}
			return data.NewFixed(name, typ, v, valid)
		case dtype.TypeFloat32:
			v := make([]float32, n)
			for i, x := range ints {
				v[i] = float32(x) * 0.1
			}
			return data.NewFixed(name, typ, v, valid)
		case dtype.TypeInt64:
			v := make([]int64, n)
			copy(v, ints)
			if edge {
				// Sums past int64, both ways, exact in 128 bits.
				for i := range 40 {
					v[i] = math.MaxInt64 - int64(i)
				}
				for i := 40; i < 60; i++ {
					v[i] = math.MinInt64 + int64(i)
				}
			}
			return data.NewFixed(name, typ, v, valid)
		case dtype.TypeInt32:
			v := make([]int32, n)
			for i, x := range ints {
				v[i] = int32(x) * 1000003
			}
			return data.NewFixed(name, typ, v, valid)
		case dtype.TypeInt8:
			v := make([]int8, n)
			for i, x := range ints {
				v[i] = int8(x)
			}
			return data.NewFixed(name, typ, v, valid)
		case dtype.TypeUint64:
			v := make([]uint64, n)
			for i, x := range ints {
				v[i] = uint64(x + 1000)
			}
			if edge {
				for i := range 30 {
					v[i] = math.MaxUint64 - uint64(i)
				}
			}
			return data.NewFixed(name, typ, v, valid)
		case dtype.TypeUint16:
			v := make([]uint16, n)
			for i, x := range ints {
				v[i] = uint16(x + 1000)
			}
			return data.NewFixed(name, typ, v, valid)
		}
		t.Fatalf("no batches of %s", typ)
		return nil
	}
	none := bitmap.NewBuilder(n)
	none.AppendMany(false, n)
	return [][]*data.Column{ties(typ), {
		mk("x", valid, true),
		mk("x", none.Finish(), false),
		mk("x", bitmap.View{}, true),
		mk("x", bitmap.View{}, false),
		mk("x", bitmap.View{}, false).Slice(0, 0),
	}}
}

// ties is a float type's batch of -0 and +0 alone, equal keys of which the first is
// kept as the minimum and the maximum; none for another type.
func ties(typ dtype.DataType) []*data.Column {
	switch typ.Physical().ID() {
	case dtype.TypeFloat64:
		return []*data.Column{data.NewFixed("x", typ, []float64{math.Copysign(0, -1), 0}, bitmap.View{})}
	case dtype.TypeFloat32:
		return []*data.Column{data.NewFixed("x", typ, []float32{float32(math.Copysign(0, -1)), 0}, bitmap.View{})}
	}
	return nil
}

// TestAddAllIsAddBatchOfOneGroup: an accumulator fed whole columns answers as one fed
// them a row at a time into group 0, to the bit, for every aggregate and input type
// that takes them so (step 372): a float's sum and mean in the same order, an
// integer's past 64 bits exactly, a count by the null count.
func TestAddAllIsAddBatchOfOneGroup(t *testing.T) {
	types := []dtype.DataType{dtype.Bool, dtype.Int8, dtype.Int32, dtype.Int64, dtype.Uint16, dtype.Uint64,
		dtype.Float32, dtype.Float64, dtype.Datetime(dtype.Micro, ""), dtype.Duration(dtype.Nano)}
	ops := []expr.AggOp{expr.AggLen, expr.AggCount, expr.AggNullCount, expr.AggSum, expr.AggMean,
		expr.AggMin, expr.AggMax, expr.AggNanMin, expr.AggFirst, expr.AggLast}
	took := 0
	for _, typ := range types {
		for _, op := range ops {
			bind, err := expr.ResolveAggBinding(op, typ)
			if err != nil {
				continue // not an aggregate of this type
			}
			for _, set := range wholeBatches(t, typ) {
				if len(set) == 0 {
					continue
				}
				byRow, err := kernel.NewAccumulator(op, typ, bind, expr.AggParams{})
				if err != nil {
					continue
				}
				whole, _ := kernel.NewAccumulator(op, typ, bind, expr.AggParams{})
				w, ok := whole.(kernel.WholeAdder)
				if !ok || !w.Whole() {
					continue
				}
				took++
				for _, col := range set {
					groups := make([]int32, col.Len())
					byRow.Reserve(1)
					if err := byRow.AddBatch(groups, col); err != nil {
						t.Fatal(op, typ, err)
					}
					if err := w.AddAll(col); err != nil {
						t.Fatal(op, typ, err)
					}
				}
				want, werr := byRow.Finish("v", 1)
				got, gerr := whole.Finish("v", 1)
				if werr != nil || gerr != nil {
					// A Duration's sum past its int64, refused alike.
					if fmt.Sprint(werr) != fmt.Sprint(gerr) {
						t.Errorf("%s of %s: %v whole, %v a row at a time", op, typ, gerr, werr)
					}
					continue
				}
				if g, w := cellBits(t, got), cellBits(t, want); g != w || got.DType() != want.DType() {
					t.Errorf("%s of %s: %s %s whole, %s %s a row at a time", op, typ, g, got.DType(), w, want.DType())
				}
			}
		}
	}
	if took < 60 {
		t.Errorf("only %d aggregates took whole columns", took)
	}
	// A count of rows told their number, and one of values not.
	acc, _ := kernel.NewAccumulator(expr.AggLen, dtype.Int64, expr.AggBinding{Out: dtype.Uint64}, expr.AggParams{})
	if !acc.(kernel.RowAdder).AddRows(5) || !acc.(kernel.RowAdder).AddRows(7) {
		t.Fatal("a count of rows refuses to be told them")
	}
	if c, _ := acc.Finish("n", 1); cellBits(t, c) != "12" {
		t.Errorf("12 rows told, counted %s", cellBits(t, c))
	}
	acc, _ = kernel.NewAccumulator(expr.AggCount, dtype.Int64, expr.AggBinding{Out: dtype.Uint64}, expr.AggParams{})
	if acc.(kernel.RowAdder).AddRows(5) {
		t.Error("a count of values took a number of rows")
	}
}

// cellBits is row 0 of c, a float by its bits.
func cellBits(t *testing.T, c *data.Column) string {
	t.Helper()
	if !c.IsValid(0) {
		return "null"
	}
	switch c.DType().Physical().ID() {
	case dtype.TypeFloat64:
		return fmt.Sprintf("%#x", math.Float64bits(data.MustValues[float64](c)[0]))
	case dtype.TypeFloat32:
		return fmt.Sprintf("%#x", math.Float32bits(data.MustValues[float32](c)[0]))
	case dtype.TypeBool:
		return fmt.Sprint(c.Bools().Get(0))
	case dtype.TypeInt128:
		return data.MustValues[i128.Int128](c)[0].String()
	case dtype.TypeInt8:
		return fmt.Sprint(data.MustValues[int8](c)[0])
	case dtype.TypeInt16:
		return fmt.Sprint(data.MustValues[int16](c)[0])
	case dtype.TypeInt32:
		return fmt.Sprint(data.MustValues[int32](c)[0])
	case dtype.TypeInt64:
		return fmt.Sprint(data.MustValues[int64](c)[0])
	case dtype.TypeUint8:
		return fmt.Sprint(data.MustValues[uint8](c)[0])
	case dtype.TypeUint16:
		return fmt.Sprint(data.MustValues[uint16](c)[0])
	case dtype.TypeUint32:
		return fmt.Sprint(data.MustValues[uint32](c)[0])
	case dtype.TypeUint64:
		return fmt.Sprint(data.MustValues[uint64](c)[0])
	}
	t.Fatalf("no rendering of %s", c.DType())
	return ""
}

package kernel

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// SelectionFromMask turns a Boolean column into the list of row indices to keep.
//
// # Filter's null semantics, in one line of code
//
// A row survives iff its predicate is VALID AND TRUE. A null predicate drops the
// row, matching SQL's WHERE. This is worth being explicit about because the
// alternative — treating null as true — is a plausible-looking choice that
// silently changes results, and because it means Filter(p) and Filter(Not(p)) do
// NOT partition the input: a row where p is null is dropped by both.
func SelectionFromMask(mask *data.Column) ([]int32, error) {
	if mask.DType().ID() != dtype.TypeBool {
		return nil, uerr.Internalf("kernel: selection mask must be Bool, got %s", mask.DType())
	}
	bits, valid := mask.Bools(), mask.Validity()
	n := mask.Len()

	sel := make([]int32, 0, n)
	for i := range n {
		if valid.Get(i) && bits.Get(i) {
			sel = append(sel, int32(i))
		}
	}
	return sel, nil
}

// Take gathers rows by index, producing a compacted column.
//
// This kernel is named in none of the design documents and is needed by every
// operator that changes row count — filter, join, sort, top-k, explode. It is
// the single most reused primitive in the engine.
//
// A NullIndex entry produces a null, which is what outer joins and out-of-bounds
// gathers need.
func Take(c *data.Column, sel []int32) (*data.Column, error) {
	n := len(sel)
	srcValid := c.Validity()

	// Output validity: null if the index is null, or if the source row was null.
	valid := bitmap.NewBuilder(n)
	anyNull := false
	for _, i := range sel {
		ok := i != NullIndex && srcValid.Get(int(i))
		valid.Append(ok)
		anyNull = anyNull || !ok
	}
	outValid := valid.Finish()
	if !anyNull {
		// Drop the buffer entirely when nothing is null: Arrow permits omitting
		// the validity bitmap, and the no-storage form makes downstream kernels
		// take their fast path.
		outValid = bitmap.AllSet(n)
	}

	switch {
	case c.DType().ID() == dtype.TypeBool:
		bits := c.Bools()
		out := bitmap.NewBuilder(n)
		for _, i := range sel {
			out.Append(i != NullIndex && bits.Get(int(i)))
		}
		return data.NewBool(c.Name(), out.Finish(), outValid), nil

	case c.DType().ID() == dtype.TypeList:
		return takeList(c, sel, outValid)

	case c.DType().IsString() || c.DType().ID() == dtype.TypeBinary:
		acc := c.Strings()
		vals := make([]string, n)
		for j, i := range sel {
			if i != NullIndex {
				vals[j] = acc.Get(int(i))
			}
		}
		col := data.NewString(c.Name(), vals, outValid)
		return col.WithDType(c.DType()), nil

	default:
		return takeFixed(c, sel, outValid)
	}
}

// NullIndex marks a gather slot that should produce a null.
const NullIndex = int32(-1)

// NullColumn builds an all-null column of dt with n rows and a REAL payload
// buffer.
//
// # Why this is not data.NewNull
//
// data.NewNull carries no payload at all — "which makes a typed null literal
// free". That is right for a value nobody reads, and wrong for anything a kernel
// then gathers from or concatenates. Take and Concat both route fixed-width
// columns through data.Values, which refuses a column with no buffer, and the
// string path builds a zero-length StringAccessor whose Get PANICS on any index.
//
// Both are reachable from ordinary queries. Min over a group whose values are all
// null produced an unread single-row null that assembleRows then concatenated —
// so `GroupBy(g).Agg(Col("v").Min())` failed with "column has no fixed-width
// payload" whenever one group was entirely null. An outer join has the same
// shape: the unmatched side is gathered from padding on every row.
//
// The rule this establishes: data.NewNull is for a column that is the ANSWER;
// NullColumn is for a column that is an OPERAND.
func NullColumn(name string, dt dtype.DataType, n int) (*data.Column, error) {
	valid := bitmap.Zeros(n)

	switch {
	case dt.ID() == dtype.TypeBool:
		return data.NewBool(name, bitmap.Zeros(n), valid), nil

	case dt.HasStringStorage() || dt.IsString():
		// A real offsets buffer of n+1 zeros and no character data, which is what
		// StringAccessor.Get needs in order to return "" rather than panic.
		return data.NewString(name, make([]string, n), valid).WithDType(dt), nil

	default:
		return nullFixed(name, dt, n, valid)
	}
}

func nullFixed(name string, dt dtype.DataType, n int, valid bitmap.View) (*data.Column, error) {
	switch dt.Physical().ID() {
	case dtype.TypeInt8:
		return zeros[int8](name, dt, n, valid), nil
	case dtype.TypeInt16:
		return zeros[int16](name, dt, n, valid), nil
	case dtype.TypeInt32:
		return zeros[int32](name, dt, n, valid), nil
	case dtype.TypeInt64:
		return zeros[int64](name, dt, n, valid), nil
	case dtype.TypeUint8:
		return zeros[uint8](name, dt, n, valid), nil
	case dtype.TypeUint16:
		return zeros[uint16](name, dt, n, valid), nil
	case dtype.TypeUint32:
		return zeros[uint32](name, dt, n, valid), nil
	case dtype.TypeUint64:
		return zeros[uint64](name, dt, n, valid), nil
	case dtype.TypeFloat32:
		return zeros[float32](name, dt, n, valid), nil
	case dtype.TypeFloat64:
		return zeros[float64](name, dt, n, valid), nil
	case dtype.TypeInt128:
		return zeros[i128.Int128](name, dt, n, valid), nil
	default:
		// Null itself has no payload and needs none: nothing can gather a value out
		// of a column whose type says there are none.
		if dt.IsNull() {
			return data.NewNull(name, dt, n), nil
		}
		return nil, uerr.New(uerr.KindUnsupported, "",
			"cannot build a null column of %s", dt)
	}
}

func zeros[T data.Fixed](name string, dt dtype.DataType, n int, valid bitmap.View) *data.Column {
	return data.NewFixed(name, dt, make([]T, n), valid)
}

func takeFixed(c *data.Column, sel []int32, valid bitmap.View) (*data.Column, error) {
	switch c.DType().Physical().ID() {
	case dtype.TypeInt8:
		return takeT[int8](c, sel, valid)
	case dtype.TypeInt16:
		return takeT[int16](c, sel, valid)
	case dtype.TypeInt32:
		return takeT[int32](c, sel, valid)
	case dtype.TypeInt64:
		return takeT[int64](c, sel, valid)
	case dtype.TypeUint8:
		return takeT[uint8](c, sel, valid)
	case dtype.TypeUint16:
		return takeT[uint16](c, sel, valid)
	case dtype.TypeUint32:
		return takeT[uint32](c, sel, valid)
	case dtype.TypeUint64:
		return takeT[uint64](c, sel, valid)
	case dtype.TypeFloat32:
		return takeT[float32](c, sel, valid)
	case dtype.TypeFloat64:
		return takeT[float64](c, sel, valid)
	case dtype.TypeInt128:
		return takeT[i128.Int128](c, sel, valid)
	default:
		return nil, uerr.New(uerr.KindUnsupported, "",
			"take is not implemented for %s", c.DType())
	}
}

func takeT[T data.Fixed](c *data.Column, sel []int32, valid bitmap.View) (*data.Column, error) {
	src, err := data.Values[T](c)
	if err != nil {
		return nil, err
	}
	buf, dst := newValuesBuffer[T](len(sel))
	for j, i := range sel {
		if i != NullIndex {
			dst[j] = src[i]
		}
	}
	return data.NewFixedBuffer(c.Name(), c.DType(), buf, len(sel), valid), nil
}

// FilterBatch applies a Boolean mask to every column of a batch.
func FilterBatch(b *data.Batch, mask *data.Column) (*data.Batch, error) {
	sel, err := SelectionFromMask(mask)
	if err != nil {
		return nil, err
	}
	// Nothing selected: return an empty batch rather than allocating per column.
	if len(sel) == 0 {
		return data.NewBatchRows(b.Schema(), emptyCols(b), 0), nil
	}
	// Everything selected: skip the copy entirely. This is the common case for a
	// predicate that matches a whole batch, and it is free to detect.
	if len(sel) == b.Rows() {
		return b, nil
	}

	cols := make([]*data.Column, b.NumCols())
	for i, c := range b.Columns() {
		out, err := Take(c, sel)
		if err != nil {
			return nil, err
		}
		cols[i] = out
	}
	return data.NewBatch(b.Schema(), cols)
}

func emptyCols(b *data.Batch) []*data.Column {
	out := make([]*data.Column, b.NumCols())
	for i, c := range b.Columns() {
		out[i] = c.Slice(0, 0)
	}
	return out
}

// Concat stacks batches vertically. Used by Collect to assemble the final frame.
func Concat(schema *dtype.Schema, batches []*data.Batch) (*data.Batch, error) {
	if len(batches) == 0 {
		// One EMPTY COLUMN per field, not a nil column slice.
		//
		// A nil slice makes a batch whose schema promises N columns and whose
		// storage has none, and Batch.ByName indexes the storage by the schema's
		// position — so `Head(0).Collect()` then `df.Column[int64]("v")` panicked
		// with an index out of range. Reaching this path is ordinary: a Head(0), a
		// filter that matches nothing, a slice past the end.
		//
		// NullColumn, not data.NewNull. A zero-row column still has to be READABLE:
		// `df.Column[int64]("v")` on an empty result should hand back an empty
		// series, not fail because the column has no payload buffer. That is the
		// same operand-versus-answer distinction take.go records above, and an empty
		// frame is the one case where it costs nothing to get right.
		cols := make([]*data.Column, schema.Len())
		for i, f := range schema.All() {
			c, err := NullColumn(f.Name, f.Type, 0)
			if err != nil {
				return nil, err
			}
			cols[i] = c
		}
		return data.NewBatchRows(schema, cols, 0), nil
	}
	if len(batches) == 1 {
		return batches[0], nil
	}

	total := 0
	for _, b := range batches {
		total += b.Rows()
	}

	if schema.Len() == 0 {
		// A frame can legitimately have rows and no columns, after Select() with
		// nothing — which is why NewBatchRows exists. NewBatch infers the row count
		// FROM the columns and answers zero when there are none, so without this arm
		// concatenating such batches silently returned an empty frame.
		return data.NewBatchRows(schema, nil, total), nil
	}

	cols := make([]*data.Column, schema.Len())
	for ci := range schema.Len() {
		parts := make([]*data.Column, len(batches))
		for bi, b := range batches {
			parts[bi] = b.Column(ci)
		}
		out, err := concatColumn(parts, total)
		if err != nil {
			return nil, err
		}
		cols[ci] = out
	}
	return data.NewBatch(schema, cols)
}

func concatColumn(parts []*data.Column, total int) (*data.Column, error) {
	first := parts[0]

	valid := bitmap.NewBuilder(total)
	for _, p := range parts {
		valid.AppendView(p.Validity())
	}
	outValid := valid.Finish()

	switch {
	case first.DType().ID() == dtype.TypeBool:
		bits := bitmap.NewBuilder(total)
		for _, p := range parts {
			bits.AppendView(p.Bools())
		}
		return data.NewBool(first.Name(), bits.Finish(), outValid), nil

	case first.DType().ID() == dtype.TypeList:
		return concatList(parts, total, outValid)

	case first.DType().IsString() || first.DType().ID() == dtype.TypeBinary:
		vals := make([]string, 0, total)
		for _, p := range parts {
			acc := p.Strings()
			for i := range p.Len() {
				vals = append(vals, acc.Get(i))
			}
		}
		return data.NewString(first.Name(), vals, outValid).WithDType(first.DType()), nil

	default:
		return concatFixed(parts, total, outValid)
	}
}

func concatFixed(parts []*data.Column, total int, valid bitmap.View) (*data.Column, error) {
	switch parts[0].DType().Physical().ID() {
	case dtype.TypeInt8:
		return concatT[int8](parts, total, valid)
	case dtype.TypeInt16:
		return concatT[int16](parts, total, valid)
	case dtype.TypeInt32:
		return concatT[int32](parts, total, valid)
	case dtype.TypeInt64:
		return concatT[int64](parts, total, valid)
	case dtype.TypeUint8:
		return concatT[uint8](parts, total, valid)
	case dtype.TypeUint16:
		return concatT[uint16](parts, total, valid)
	case dtype.TypeUint32:
		return concatT[uint32](parts, total, valid)
	case dtype.TypeUint64:
		return concatT[uint64](parts, total, valid)
	case dtype.TypeFloat32:
		return concatT[float32](parts, total, valid)
	case dtype.TypeFloat64:
		return concatT[float64](parts, total, valid)
	case dtype.TypeInt128:
		return concatT[i128.Int128](parts, total, valid)
	default:
		return nil, uerr.New(uerr.KindUnsupported, "",
			"concat is not implemented for %s", parts[0].DType())
	}
}

func concatT[T data.Fixed](parts []*data.Column, total int, valid bitmap.View) (*data.Column, error) {
	buf, dst := newValuesBuffer[T](total)
	pos := 0
	for _, p := range parts {
		src, err := data.Values[T](p)
		if err != nil {
			return nil, err
		}
		copy(dst[pos:], src)
		pos += p.Len()
	}
	return data.NewFixedBuffer(parts[0].Name(), parts[0].DType(), buf, total, valid), nil
}

// takeList gathers a List column: rebuild the offsets from the selection, and
// gather the child rows those offsets name.
//
// The child selection is built first and gathered ONCE, rather than a slice per
// row. A row's elements are contiguous in the child, so the whole gather is one
// Take over a flat index list — which also means every element type the child can
// be is handled by the recursion rather than by another type switch here.
func takeList(c *data.Column, sel []int32, outValid bitmap.View) (*data.Column, error) {
	acc := c.Lists()

	offs := make([]int32, 1, len(sel)+1)
	var childSel []int32
	for _, i := range sel {
		if i != NullIndex {
			// A null ROW still contributes an offset entry and no elements, which is
			// what makes it indistinguishable from an empty list except by validity —
			// the distinction outValid already carries.
			if start, end, ok := acc.Get(int(i)); ok {
				for e := start; e < end; e++ {
					childSel = append(childSel, e)
				}
			}
		}
		offs = append(offs, int32(len(childSel)))
	}

	child, err := Take(acc.Child(), childSel)
	if err != nil {
		return nil, err
	}
	return data.NewList(c.Name(), offs, child, outValid), nil
}

// concatList concatenates List columns.
//
// Every part after the first has offsets relative to ITS OWN child, so they are
// shifted by the number of elements already accumulated. Forgetting that shift
// does not crash: part two's rows read part one's elements, which is data that
// looks entirely plausible.
func concatList(parts []*data.Column, total int, outValid bitmap.View) (*data.Column, error) {
	offs := make([]int32, 1, total+1)
	children := make([]*data.Column, 0, len(parts))
	base := int32(0)

	for _, p := range parts {
		acc := p.Lists()
		for i := range p.Len() {
			// The END offset of each row, shifted. Validity is carried separately in
			// outValid, and a null row's range is empty, so this is right for every
			// row state without a branch.
			_, end, _ := acc.Get(i)
			offs = append(offs, base+end)
		}
		base += int32(acc.Child().Len())
		children = append(children, acc.Child())
	}

	child, err := concatChild(children)
	if err != nil {
		return nil, err
	}
	return data.NewList(parts[0].Name(), offs, child, outValid), nil
}

// concatChild concatenates the element columns, reusing concatColumn so the
// element type needs no separate dispatch.
func concatChild(children []*data.Column) (*data.Column, error) {
	if len(children) == 1 {
		return children[0], nil
	}
	n := 0
	for _, c := range children {
		n += c.Len()
	}
	return concatColumn(children, n)
}

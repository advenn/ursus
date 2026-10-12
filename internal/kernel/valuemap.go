package kernel

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// ValueMap is Replace, ReplaceStrict and ReplaceOr compiled (step 226): the old
// values encoded as group_by keys at the type they meet the column at, each the
// position of its new value in Values, and those cast to the answer's type.
type ValueMap struct {
	Fn      expr.CallFn
	Out     dtype.DataType
	Keys    map[string]int32
	Values  *data.Column // the new values, then the default; nil when there are none
	Default int32        // the default's position in Values, or -1
}

// Apply maps each row of look, the column cast to the type the old values meet it
// at. recv is the column as it is, which Replace keeps where no old value matches.
//
//   - Replace: a matched row is its new value, any other row itself, a null null;
//   - ReplaceStrict: a matched row is its new value, a null null, and any other row
//     is refused, naming it;
//   - ReplaceOr: a matched row is its new value, and any other row, a null too, the
//     default, as Polars' replace_strict with a default.
func (m *ValueMap) Apply(name string, look, recv *data.Column) (*data.Column, error) {
	n := look.Len()
	enc, err := NewGroupKeyEncoder(m.Fn.String(), []*data.Column{look})
	if err != nil {
		return nil, err
	}
	pick := make([]int32, n)
	src := m.Values
	base := int32(0)
	if m.Fn == expr.FnReplace {
		// The column, as the answer's type, then the new values after it.
		keep := recv
		if keep.DType() != m.Out {
			if keep, err = Cast(recv.Name(), m.Out, true, recv); err != nil {
				return nil, err
			}
		}
		if src == nil {
			return keep.Rename(name), nil
		}
		if src, err = ConcatColumns(name, []*data.Column{keep, m.Values}); err != nil {
			return nil, err
		}
		base = int32(n)
	}
	for i := range n {
		j, hit := int32(0), false
		if look.IsValid(i) {
			j, hit = m.Keys[string(enc.Encode(i))]
		}
		switch {
		case hit:
			pick[i] = base + j
		case m.Fn == expr.FnReplace:
			pick[i] = int32(i)
		case m.Fn == expr.FnReplaceOr:
			pick[i] = m.Default
		case !look.IsValid(i):
			pick[i] = NullIndex
		default:
			v, _ := Cast("v", dtype.String, false, look.Slice(i, 1))
			shown := "a value"
			if v != nil && v.IsValid(0) {
				shown = v.Strings().Get(0)
			}
			return nil, uerr.New(uerr.KindValue, m.Fn.String(),
				"%s found %s in row %d, which its old values do not hold", m.Fn, shown, i).
				Hint("map every value, or use ReplaceOr to give the others a default")
		}
	}
	if src == nil {
		// ReplaceStrict with nothing to answer: every row was null.
		return NullColumn(name, m.Out, n)
	}
	out, err := Take(src, pick)
	if err != nil {
		return nil, err
	}
	return out.Rename(name), nil
}

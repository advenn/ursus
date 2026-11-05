package kernel

import (
	"slices"
	"strconv"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// castEnum is every cast to or from an Enum, which is text with a fixed vocabulary
// stored as indices into it.
//
//	Enum -> String      decodes
//	String -> Enum      encodes; a value outside the categories is refused under a
//	                    strict cast, naming it and them, and null under a lossy one
//	Enum -> Enum        decodes and encodes, so the text is kept and the index is the
//	                    new type's
//	Enum -> anything    decodes and casts the text: Enum("1", "2") -> Int64 is 1, 2
//
// Polars casts an Enum to an integer as its index, and has deprecated that; the
// index is storage, and the text is what the user wrote.
func castEnum(name string, to dtype.DataType, strict bool, c *data.Column) (*data.Column, error) {
	from := c.DType()
	switch {
	case from.ID() == dtype.TypeEnum:
		text, err := decodeEnum(name, c)
		if err != nil || to.ID() == dtype.TypeString {
			return text, err
		}
		return Cast(name, to, strict, text)
	case from.ID() == dtype.TypeString && to.ID() == dtype.TypeEnum:
		return encodeEnum(name, to, strict, c)
	}
	return nil, uerr.New(uerr.KindType, "cast", "cannot cast %s to %s", from, to).
		Hint("an Enum is built from text: cast to ursus.String first, then to the Enum")
}

// castCategorical casts to or from a Categorical, which is stored as its text (step
// 351): to a String and from one it is a relabel; to an Enum, its text encoded; from
// one, the Enum's text relabelled. Anything else is refused, as dtype.CanCast refuses
// it: Polars reads a number as a category's code, the order of a process's strings.
func castCategorical(name string, to dtype.DataType, strict bool, c *data.Column) (*data.Column, error) {
	from := c.DType()
	switch {
	case from.ID() == dtype.TypeCategorical && to.ID() == dtype.TypeString,
		from.ID() == dtype.TypeString && to.ID() == dtype.TypeCategorical:
		return c.Rename(name).WithDType(to), nil
	case from.ID() == dtype.TypeCategorical && to.ID() == dtype.TypeEnum:
		return castEnum(name, to, strict, c.WithDType(dtype.String))
	case from.ID() == dtype.TypeEnum && to.ID() == dtype.TypeCategorical:
		text, err := castEnum(name, dtype.String, strict, c)
		if err != nil {
			return nil, err
		}
		return text.WithDType(to), nil
	}
	return nil, uerr.New(uerr.KindType, "cast", "cannot cast %s to %s", from, to).
		Hint("a Categorical's codes are Cat().Physical(), and its text .Cast(ursus.String)")
}

// decodeEnum is an Enum column as its category text.
func decodeEnum(name string, c *data.Column) (*data.Column, error) {
	idx, err := data.Values[uint32](c)
	if err != nil {
		return nil, err
	}
	cats := c.DType().Categories()
	valid := c.Validity()
	out := make([]string, c.Len())
	for i := range out {
		if !valid.Get(i) {
			continue
		}
		j := int(idx[i])
		if j >= len(cats) {
			return nil, uerr.Internalf("kernel: Enum index %d at row %d is past its %d categories",
				j, i, len(cats))
		}
		out[i] = cats[j]
	}
	return data.NewString(name, out, valid), nil
}

// encodeEnum is a String column as indices into to's categories.
func encodeEnum(name string, to dtype.DataType, strict bool, c *data.Column) (*data.Column, error) {
	cats := to.Categories()
	pos := make(map[string]uint32, len(cats))
	for i, s := range cats {
		pos[s] = uint32(i)
	}
	acc := c.Strings()
	valid := c.Validity()
	n := c.Len()
	idx := make([]uint32, n)
	ok := bitmap.NewBuilder(n)
	for i := range n {
		if !valid.Get(i) {
			ok.Append(false)
			continue
		}
		s := acc.Get(i)
		j, found := pos[s]
		if !found {
			if strict {
				return nil, uerr.New(uerr.KindValue, "cast",
					"value %s at row %d is not a category of %s", strconv.Quote(s), i, to).
					Hint("the categories are %q", cats).
					Hint("use CastLossy to turn values outside them into nulls")
			}
			ok.Append(false)
			continue
		}
		idx[i] = j
		ok.Append(true)
	}
	return data.NewFixed(name, to, idx, ok.Finish()), nil
}

// EnumPhysical is an Enum's indices at out's width, Polars' cat.physical: each index
// is below the category count, which out holds (step 273).
// ToPhysical is c as stored, under its physical type out, Polars' to_physical (step
// 324): a relabel of the values, but for an Enum, whose categories are narrowed to
// out as cat.physical narrows them.
func ToPhysical(name string, out dtype.DataType, c *data.Column) (*data.Column, error) {
	if c.DType().ID() == dtype.TypeEnum {
		return EnumPhysical(name, out, c)
	}
	if c.DType().ID() == dtype.TypeCategorical {
		// Its codes are the frame's, a window the plan writes out (step 351): a
		// batch's own would number its values apart from the next's.
		return nil, uerr.Internalf("kernel: to_physical of a Categorical reached a batch")
	}
	return c.WithDType(out).Rename(name), nil
}

func EnumPhysical(name string, out dtype.DataType, c *data.Column) (*data.Column, error) {
	if c.DType().ID() != dtype.TypeEnum {
		return nil, uerr.Internalf("kernel: cat.physical of %s", c.DType())
	}
	idx, err := data.Values[uint32](c)
	if err != nil {
		return nil, err
	}
	valid := c.Validity()
	switch out {
	case dtype.Uint32:
		return data.NewFixed(name, out, slices.Clone(idx), valid), nil
	case dtype.Uint16:
		res := make([]uint16, len(idx))
		for i, v := range idx {
			res[i] = uint16(v)
		}
		return data.NewFixed(name, out, res, valid), nil
	case dtype.Uint8:
		res := make([]uint8, len(idx))
		for i, v := range idx {
			res[i] = uint8(v)
		}
		return data.NewFixed(name, out, res, valid), nil
	}
	return nil, uerr.Internalf("kernel: cat.physical to %s", out)
}

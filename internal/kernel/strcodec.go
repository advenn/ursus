package kernel

import (
	"encoding/base64"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// Encodings, integers in a base and concat_str past nulls (step 271, S of
// v0.6.6-scope.md).

// strEncode is each value's bytes as lowercase hex or padded base64: Polars'
// str.encode.
func strEncode(name string, c *data.Column, enc string) (*data.Column, error) {
	acc := c.Strings()
	res := make([]string, c.Len())
	for i := range c.Len() {
		if !c.IsValid(i) {
			continue
		}
		b := []byte(acc.Get(i))
		if enc == "hex" {
			res[i] = hex.EncodeToString(b)
		} else {
			res[i] = base64.StdEncoding.EncodeToString(b)
		}
	}
	return data.NewString(name, res, c.Validity()), nil
}

// strDecode is each value's hex or base64 as Binary: Polars' str.decode. Text that
// is not of the encoding is refused, naming its row, or with strict false null, as
// Polars has it; base64 must be padded.
func strDecode(name string, c *data.Column, enc string, strict bool) (*data.Column, error) {
	acc := c.Strings()
	n := c.Len()
	res := make([]string, n)
	ok := bitmap.NewBuilder(n)
	for i := range n {
		if !c.IsValid(i) {
			ok.Append(false)
			continue
		}
		var b []byte
		var err error
		if enc == "hex" {
			b, err = hex.DecodeString(acc.Get(i))
		} else {
			b, err = base64.StdEncoding.DecodeString(acc.Get(i))
		}
		if err != nil {
			if strict {
				return nil, uerr.Wrap(err, uerr.KindValue, "str.decode", "row %d is not %s: %q", i, enc, acc.Get(i)).
					Hint("strict false makes it null")
			}
			ok.Append(false)
			continue
		}
		res[i] = string(b)
		ok.Append(true)
	}
	return data.NewString(name, res, ok.Finish()).WithDType(dtype.Binary), nil
}

// strParseInt is to_integer: each value read as an integer in its row's base, signed
// as Int64 or unsigned as UInt64, which the root package casts to the type asked for.
// A sign is read, and nothing else but digits of the base, as Polars reads it: not a
// space, a 0x or an underscore. A value that does not read, or a base outside 2 to
// 36, is null, or with strict refused, naming the values, as Polars refuses them.
func strParseInt(fn expr.CallFn, name string, ops []*data.Column, n int) (*data.Column, error) {
	s, base := ops[0], ops[1]
	strict := ops[2].Bools().Get(0)
	bc, err := asInt64(base)
	if err != nil {
		return nil, err
	}
	unsigned := fn == expr.FnStrParseUint
	ints := make([]int64, n)
	uints := make([]uint64, n)
	ok := bitmap.NewBuilder(n)
	var failed []string
	for i := range n {
		r := rowOf(s, i)
		b, bok := int64At(bc, i)
		if !s.IsValid(r) || !bok {
			ok.Append(false)
			continue
		}
		v := s.Strings().Get(r)
		var perr error
		switch {
		case b < 2 || b > 36:
			perr = strconv.ErrSyntax
		case unsigned:
			uints[i], perr = strconv.ParseUint(v, int(b), 64)
		default:
			ints[i], perr = strconv.ParseInt(v, int(b), 64)
		}
		if perr != nil {
			if len(failed) < 3 {
				failed = append(failed, strconv.Quote(v))
			}
			ok.Append(false)
			continue
		}
		ok.Append(true)
	}
	if strict && len(failed) > 0 {
		return nil, uerr.New(uerr.KindValue, "str.to_integer",
			"to_integer could not read %s as integers", strings.Join(failed, ", ")).
			Hint("strict false makes them null")
	}
	if unsigned {
		return data.NewFixed(name, dtype.Uint64, uints, ok.Finish()), nil
	}
	return data.NewFixed(name, dtype.Int64, ints, ok.Finish()), nil
}

// concatStrSkipping is concat_str with ignore_nulls: each row's values that are not
// null, formatted as text and joined by the separator, the last operand, so the first
// names the column; a row of nothing but nulls is "", as Polars makes it.
func concatStrSkipping(name string, ops []*data.Column, n int) (*data.Column, error) {
	sep := ops[len(ops)-1].Strings().Get(0)
	vals := slices.Clone(ops[:len(ops)-1])
	strs := make([]data.StringAccessor, len(vals))
	for k, c := range vals {
		if c.DType().ID() != dtype.TypeString {
			var err error
			if c, err = Cast(c.Name(), dtype.String, true, c); err != nil {
				return nil, err
			}
			vals[k] = c
		}
		strs[k] = c.Strings()
	}
	res := make([]string, n)
	var b strings.Builder
	for i := range n {
		b.Reset()
		first := true
		for k, c := range vals {
			r := rowOf(c, i)
			if !c.IsValid(r) {
				continue
			}
			if !first {
				b.WriteString(sep)
			}
			b.WriteString(strs[k].Get(r))
			first = false
		}
		res[i] = b.String()
	}
	return data.NewString(name, res, bitmap.AllSet(n)), nil
}

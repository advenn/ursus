package ursus

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// BinExpr is the binary namespace, Polars' .bin, on a Binary column:
// `Col("payload").Bin().Head(4)`. Every function counts bytes. A String is refused,
// as Polars refuses one; .Cast(ursus.Binary) is its text's bytes.
type BinExpr struct{ e Expr }

// Bin opens the binary namespace.
func (e Expr) Bin() BinExpr { return BinExpr{e} }

// binPattern is a pattern's operand: bytes, text, or an expression of either.
func binPattern(v any) expr.Node {
	if x, ok := v.(Expr); ok {
		return x.node()
	}
	return litNode(v)
}

// binInt is an integer argument's operand: a Go int or an expression.
func binInt(v any) expr.Node {
	if x, ok := v.(Expr); ok {
		return x.node()
	}
	return litNode(int64(v.(int)))
}

// Contains, StartsWith and EndsWith are whether each value holds, starts or ends
// with a pattern: bytes, text, or an expression of either, a row's own; a null
// pattern is null. Polars' bin.contains, starts_with and ends_with.
func (b BinExpr) Contains[P []byte | string | Expr](pattern P) Expr {
	return wrap(horizontal(expr.FnBinContains, b.e.node(), binPattern(pattern)))
}
func (b BinExpr) StartsWith[P []byte | string | Expr](prefix P) Expr {
	return wrap(horizontal(expr.FnBinStartsWith, b.e.node(), binPattern(prefix)))
}
func (b BinExpr) EndsWith[P []byte | string | Expr](suffix P) Expr {
	return wrap(horizontal(expr.FnBinEndsWith, b.e.node(), binPattern(suffix)))
}

// Get is each value's byte at index, a constant or an expression, from the end where
// negative, a UInt8: Polars' bin.get. An index past a value is refused, or with
// nullOnOOB null.
func (b BinExpr) Get[I int | Expr](index I, nullOnOOB bool) Expr {
	return wrap(horizontal(expr.FnBinGet, b.e.node(), binInt(index), litNode(nullOnOOB)))
}

// Head is each value's first n bytes and Tail its last, where n is negative all but
// |n| from the other end: Polars' bin.head and bin.tail. n is a constant or an
// expression, whose null is null.
func (b BinExpr) Head[N int | Expr](n N) Expr { return b.slice("head", binInt(n), nil) }
func (b BinExpr) Tail[N int | Expr](n N) Expr { return b.slice("tail", binInt(n), nil) }

// Slice is length bytes of each value from offset, which counts from the end where
// negative; with no length, or a null one, the rest: Polars' bin.slice. Each is an
// int or an Expr. An offset before the start shortens the length by as much, as
// Polars has it: Slice(-6, 3) of "hello" is "he".
func (b BinExpr) Slice[O int | Expr](offset O, length ...any) Expr {
	switch len(length) {
	case 0:
		return b.slice("slice", binInt(offset), nil)
	case 1:
		switch length[0].(type) {
		case int, Expr:
			return b.slice("slice", binInt(offset), binInt(length[0]))
		}
	}
	return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "bin.slice", "bin.slice takes one length, an int or an Expr")})
}

func (b BinExpr) slice(mode string, a, length expr.Node) Expr {
	if length == nil {
		length = &expr.Lit{DT: dtype.Null}
	}
	return wrap(horizontal(expr.FnBinSlice, b.e.node(), a, length, litNode(mode)))
}

// SizeUnit is a unit for Size.
type SizeUnit int64

const (
	SizeB  SizeUnit = 1
	SizeKB SizeUnit = 1 << 10
	SizeMB SizeUnit = 1 << 20
	SizeGB SizeUnit = 1 << 30
	SizeTB SizeUnit = 1 << 40
)

// Size is each value's length: in bytes, a UInt32, or in kilobytes to terabytes of
// 1024, a Float64. Polars' bin.size.
func (b BinExpr) Size(unit SizeUnit) Expr {
	switch unit {
	case SizeB, SizeKB, SizeMB, SizeGB, SizeTB:
		return b.call(expr.FnBinSize, int64(unit))
	}
	return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "bin.size", "%d bytes is not a size unit", int64(unit)).
		Hint("SizeB, SizeKB, SizeMB, SizeGB or SizeTB")})
}

// Encode is each value's bytes as lowercase hex or padded base64, a String; Decode
// is each value's hex or base64 text as its bytes, a Binary, refused where it is not
// of the encoding, or with strict false null. Polars' bin.encode and bin.decode.
func (b BinExpr) Encode(enc Encoding) Expr {
	if enc != Hex && enc != Base64 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "bin.encode", "%q is not an encoding", enc).Hint("Hex or Base64")})
	}
	return b.call(expr.FnBinEncode, string(enc))
}
func (b BinExpr) Decode(enc Encoding, strict bool) Expr {
	if enc != Hex && enc != Base64 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "bin.decode", "%q is not an encoding", enc).Hint("Hex or Base64")})
	}
	return b.call(expr.FnBinDecode, string(enc), strict)
}

// Endianness is the byte order Reinterpret reads.
type Endianness string

const (
	LittleEndian Endianness = "little"
	BigEndian    Endianness = "big"
)

// Reinterpret reads each value's bytes as one value of dt: an integer, a float, a
// Date, a Datetime, a Duration or a Time, in the byte order given. A value of other
// than dt's width is null. Polars' bin.reinterpret.
func (b BinExpr) Reinterpret(dt DataType, endianness Endianness) Expr {
	if endianness != LittleEndian && endianness != BigEndian {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "bin.reinterpret", "%q is not a byte order", endianness).
			Hint("LittleEndian or BigEndian")})
	}
	return wrap(&expr.Call{Fn: expr.FnBinReinterpret,
		Args: []expr.Node{b.e.node(), litNode(string(endianness)), &expr.Lit{DT: dt}}})
}

func (b BinExpr) call(fn expr.CallFn, args ...any) Expr {
	nodes := []expr.Node{b.e.node()}
	for _, a := range args {
		nodes = append(nodes, litNode(a))
	}
	return wrap(&expr.Call{Fn: fn, Args: nodes})
}

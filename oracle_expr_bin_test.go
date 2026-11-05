package ursus_test

// The expr_bin group (step 499, from expr_bin_test.go's hand tables): the binary
// namespace and bin.reinterpret, each spelled as bench/scripts/oracle/expr_bin.py
// asks Polars.

import (
	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
)

func init() {
	c := ursus.Col
	b := c("b").Bin()
	le, be := ursus.LittleEndian, ursus.BigEndian
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	one := func(e ursus.Expr) oracleQuery { return sel(e.Alias("r")) }
	oracleGroups["expr_bin"] = map[string]oracleQuery{
		"contains": one(b.Contains([]byte("ll"))), "contains_nothing": one(b.Contains([]byte{})), "contains_row": one(b.Contains(c("p"))),
		"starts_with": one(b.StartsWith([]byte("he"))), "starts_with_row": one(b.StartsWith(c("p"))),
		"ends_with": one(b.EndsWith([]byte("ab"))), "ends_with_text": one(b.EndsWith("ab")),
		"get": one(b.Get(0, true)), "get_end": one(b.Get(-1, true)), "get_row": one(b.Get(c("i"), true)),
		"head": one(b.Head(2)), "head_all_but": one(b.Head(-2)), "tail": one(b.Tail(2)), "tail_all_but": one(b.Tail(-2)),
		"head_row": one(b.Head(c("n"))),
		"slice":    one(b.Slice(1, 2)), "slice_end": one(b.Slice(-2)), "slice_before_start": one(b.Slice(-10, 3)),
		"slice_row": one(b.Slice(c("i"), c("n"))),
		"size":      one(b.Size(ursus.SizeB)), "size_kb": one(b.Size(ursus.SizeKB)),
		"encode_hex": one(b.Encode(ursus.Hex)), "encode_base64": one(b.Encode(ursus.Base64)),
		"hello_slices": sel(b.Slice(-6, 3).Alias("m6_3"), b.Slice(-5, 2).Alias("m5_2"), b.Slice(-7, 3).Alias("m7_3"), b.Slice(3, 10).Alias("p3_10"),
			b.Slice(5, 1).Alias("p5_1"), b.Slice(6, 1).Alias("p6_1"), b.Slice(0, 0).Alias("p0_0"), b.Slice(2).Alias("p2"),
			b.Slice(-1).Alias("m1"), b.Slice(-6).Alias("m6"), b.Head(0).Alias("h0"), b.Tail(0).Alias("t0"), b.Head(10).Alias("h10"),
			b.Tail(10).Alias("t10"), b.Head(-10).Alias("hm10"), b.Tail(-10).Alias("tm10"), b.Head(-5).Alias("hm5"), b.Tail(-5).Alias("tm5")),
		"null_arguments":    sel(b.Slice(c("o"), c("l")).Alias("slice"), b.Head(c("o")).Alias("head"), b.Get(c("o"), false).Alias("get")),
		"get_past_the_ends": one(b.Get(c("i"), true)),
		"decode_hex":        one(b.Decode(ursus.Hex, false)),
		"decode_base64":     one(b.Decode(ursus.Base64, false)),
		"reinterpret": sel(b.Reinterpret(ursus.Int32, le).Alias("i32"), b.Reinterpret(ursus.Uint32, le).Alias("u32"),
			b.Reinterpret(ursus.Int16, le).Alias("i16"), b.Reinterpret(ursus.Int64, le).Alias("i64"), b.Reinterpret(ursus.Uint8, le).Alias("u8"),
			b.Reinterpret(ursus.Int32, be).Alias("be"), b.Reinterpret(ursus.Date, le).Alias("date"),
			b.Reinterpret(ursus.Float64, le).Alias("f64"), b.Reinterpret(ursus.Float32, le).Alias("f32")),
		"reinterpret_one":  one(b.Reinterpret(ursus.Float64, le)),
		"reinterpret_i128": sel(b.Reinterpret(ursus.Int128, le).Alias("le"), b.Reinterpret(ursus.Int128, be).Alias("be")),

		"bin_gen": sel(b.Contains([]byte{0}).Alias("c0"), b.Contains(c("p")).Alias("cp"), b.StartsWith(c("p")).Alias("sw"),
			b.EndsWith(c("p")).Alias("ew"), b.Get(c("i"), true).Alias("gi"), b.Head(c("n")).Alias("hn"), b.Tail(2).Alias("t2"),
			b.Slice(c("i"), c("n")).Alias("sl"), b.Slice(-2).Alias("sm2"), b.Size(ursus.SizeB).Alias("sz"), b.Size(ursus.SizeKB).Alias("kb"),
			b.Encode(ursus.Hex).Alias("hex"), b.Encode(ursus.Base64).Alias("b64")),
		"reinterpret_4_gen": sel(b.Reinterpret(ursus.Int32, le).Alias("i32"), b.Reinterpret(ursus.Uint32, be).Alias("u32be"),
			b.Reinterpret(ursus.Float32, le).Alias("f32"), b.Reinterpret(ursus.Date, le).Alias("date"), b.Reinterpret(ursus.Int16, le).Alias("i16")),
		"reinterpret_8_gen": sel(b.Reinterpret(ursus.Int64, le).Alias("i64"), b.Reinterpret(ursus.Uint64, be).Alias("u64be"),
			b.Reinterpret(ursus.Float64, le).Alias("f64"), b.Reinterpret(dtype.Datetime(dtype.Micro, ""), le).Alias("dt")),
		"decode_gen": sel(b.Decode(ursus.Hex, false).Alias("hex"), b.Decode(ursus.Base64, false).Alias("b64")),
	}
}

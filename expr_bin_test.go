package ursus_test

import (
	"math"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// binFrame is Polars' frame: b [b"hello", None, b"", b"\x00\xffab", b"ell"], p
// [b"he", b"x", None, b"\x00", b"ll"], i [1, 0, 0, -1, 5] and n [2, 1, 1, 3, None].
func binFrame() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.ValuesNullable("b", [][]byte{[]byte("hello"), nil, {}, {0, 0xff, 'a', 'b'}, []byte("ell")},
			[]bool{true, false, true, true, true}),
		ursus.ValuesNullable("p", [][]byte{[]byte("he"), []byte("x"), nil, {0}, []byte("ll")},
			[]bool{true, true, false, true, true}),
		ursus.Values("i", []int64{1, 0, 0, -1, 5}),
		ursus.ValuesNullable("n", []int64{2, 1, 1, 3, 0}, []bool{true, true, true, true, false}))
}

// hexed is a Binary answer as its hex, which CSV can write.
func hexed(e ursus.Expr) ursus.Expr { return e.Bin().Encode(ursus.Hex) }

// TestBinMethods is B of v0.6.6-scope.md (step 274): the binary namespace. Every
// answer is Polars 1.44.1's.
func TestBinMethods(t *testing.T) {
	c := ursus.Col
	b := c("b").Bin()
	lf := binFrame()
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want string
	}{
		{"contains", b.Contains([]byte("ll")), "true\n\nfalse\nfalse\ntrue"},
		{"contains nothing", b.Contains([]byte{}), "true\n\ntrue\ntrue\ntrue"},
		{"contains a row's", b.Contains(c("p")), "true\n\n\ntrue\ntrue"},
		{"starts_with", b.StartsWith([]byte("he")), "true\n\nfalse\nfalse\nfalse"},
		{"starts_with a row's", b.StartsWith(c("p")), "true\n\n\ntrue\nfalse"},
		{"ends_with", b.EndsWith([]byte("ab")), "false\n\nfalse\ntrue\nfalse"},
		{"ends_with text", b.EndsWith("ab"), "false\n\nfalse\ntrue\nfalse"},
		{"get", b.Get(0, true), "104\n\n\n0\n101"},
		{"get from the end", b.Get(-1, true), "111\n\n\n98\n108"},
		{"get a row's", b.Get(c("i"), true), "101\n\n\n98\n"},
		{"head", hexed(b.Head(2)), "6865\n\n\"\"\n00ff\n656c"},
		{"head of all but", hexed(b.Head(-2)), "68656c\n\n\"\"\n00ff\n65"},
		{"tail", hexed(b.Tail(2)), "6c6f\n\n\"\"\n6162\n6c6c"},
		{"tail of all but", hexed(b.Tail(-2)), "6c6c6f\n\n\"\"\n6162\n6c"},
		{"head a row's", hexed(b.Head(c("n"))), "6865\n\n\"\"\n00ff61\n"},
		{"slice", hexed(b.Slice(1, 2)), "656c\n\n\"\"\nff61\n6c6c"},
		{"slice from the end", hexed(b.Slice(-2)), "6c6f\n\n\"\"\n6162\n6c6c"},
		{"slice before the start", hexed(b.Slice(-10, 3)), "\"\"\n\n\"\"\n\"\"\n\"\""},
		{"slice a row's", hexed(b.Slice(c("i"), c("n"))), "656c\n\n\"\"\n62\n\"\""},
		{"size", b.Size(ursus.SizeB), "5\n\n0\n4\n3"},
		{"encode", b.Encode(ursus.Hex), "68656c6c6f\n\n\"\"\n00ff6162\n656c6c"},
		{"encode base64", b.Encode(ursus.Base64), "aGVsbG8=\n\n\"\"\nAP9hYg==\nZWxs"},
	} {
		if got, want := csvOf(t, lf.Select(tc.e.Alias("r"))), "r\n"+tc.want+"\n"; got != want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, want)
		}
	}
	if got := floatsOf(t, lf.Select(b.Size(ursus.SizeKB))); !sameFloats(got,
		[]float64{0.0048828125, math.NaN(), 0.0, 0.00390625, 0.0029296875}) {
		t.Errorf("size in kilobytes: %v", got)
	}

	// Polars' slices, heads and tails of "hello".
	hello := ursus.Frame(ursus.Values("b", [][]byte{[]byte("hello")}))
	for _, tc := range []struct {
		e    ursus.Expr
		want string
	}{
		{b.Slice(-6, 3), "he"}, {b.Slice(-5, 2), "he"}, {b.Slice(-7, 3), "h"}, {b.Slice(3, 10), "lo"},
		{b.Slice(5, 1), ""}, {b.Slice(6, 1), ""}, {b.Slice(0, 0), ""}, {b.Slice(2), "llo"},
		{b.Slice(-1), "o"}, {b.Slice(-6), "hello"},
		{b.Head(0), ""}, {b.Tail(0), ""}, {b.Head(10), "hello"}, {b.Tail(10), "hello"},
		{b.Head(-10), ""}, {b.Tail(-10), ""}, {b.Head(-5), ""}, {b.Tail(-5), ""},
	} {
		got := csvOf(t, hello.Select(tc.e.Cast(ursus.String)))
		if want := "b\n" + tc.want + "\n"; tc.want == "" && got != "b\n\"\"\n" || tc.want != "" && got != want {
			t.Errorf("%s: got %q, want %q", tc.e, got, tc.want)
		}
	}
	// A null offset or n is null; a null length is the rest.
	rows := ursus.Frame(ursus.Values("b", [][]byte{[]byte("hello"), []byte("hello"), []byte("hello"), []byte("hello")}),
		ursus.ValuesNullable("o", []int64{1, 0, 1, 0}, []bool{true, false, true, true}),
		ursus.ValuesNullable("l", []int64{0, 2, 0, 2}, []bool{false, true, true, true}))
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want string
	}{
		{"slice", b.Slice(c("o"), c("l")).Cast(ursus.String), "ello\n\n\"\"\nhe"},
		{"head", b.Head(c("o")).Cast(ursus.String), "h\n\nh\n\"\""},
		{"get", b.Get(c("o"), false), "101\n\n101\n104"},
	} {
		if got, want := csvOf(t, rows.Select(tc.e.Alias("r"))), "r\n"+tc.want+"\n"; got != want {
			t.Errorf("%s of nulls:\ngot  %q\nwant %q", tc.name, got, want)
		}
	}
	ab := ursus.Frame(ursus.Values("b", [][]byte{[]byte("ab"), []byte("ab")}), ursus.Values("i", []int64{5, -3}))
	if got := csvOf(t, ab.Select(b.Get(c("i"), true))); got != "b\n\n\n" {
		t.Errorf("get past the ends: %q", got)
	}

	// Decode reads text's bytes as hex or base64.
	text := ursus.Frame(ursus.ValuesNullable("b", [][]byte{[]byte("6869"), []byte("zz"), nil}, []bool{true, true, false}))
	if got := csvOf(t, text.Select(b.Decode(ursus.Hex, false).Cast(ursus.String))); got != "b\nhi\n\n\n" {
		t.Errorf("decode: %q", got)
	}
	got, err := text.Select(b.Decode(ursus.Hex, false)).Collect(t.Context())
	if err != nil || got.Schema().Field(0).Type != ursus.Binary {
		t.Errorf("decode is Binary: %v, %v", got, err)
	}
	b64 := ursus.Frame(ursus.Values("b", [][]byte{[]byte("aGk="), []byte("aGk")}))
	if got := csvOf(t, b64.Select(b.Decode(ursus.Base64, false).Cast(ursus.String))); got != "b\nhi\n\n" {
		t.Errorf("decode base64: %q", got)
	}

	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		// Polars: "get index is out of bounds".
		"get past an empty value": {lf.Select(b.Get(0, false)), "out of row 2's 0 bytes"},
		"a negative length":       {lf.Select(b.Slice(0, -1)), "negative"},
		"strict decode":           {text.Select(b.Decode(ursus.Hex, true)), "not hex"},
		"a String":                {lf.Select(c("b").Cast(ursus.String).Bin().Head(1)), "requires a Binary"},
		"Str of a Binary":         {lf.Select(c("b").Str().Slice(0, 1)), "Bin()"},
		"a length of text":        {lf.Select(b.Slice(0, "x")), "one length"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}

// TestBinReinterpret: bytes read as numbers and instants, little- and big-endian; a
// value of another width is null. Every answer is Polars 1.44.1's.
func TestBinReinterpret(t *testing.T) {
	c := ursus.Col
	b := c("b").Bin()
	r := ursus.Frame(ursus.ValuesNullable("b", [][]byte{{1, 0, 0, 0}, {0xff, 0xff, 0xff, 0xff}, nil, {1, 0},
		{1, 0, 0, 0, 2, 0, 0, 0}}, []bool{true, true, false, true, true}))
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want string
	}{
		{"Int32", b.Reinterpret(ursus.Int32, ursus.LittleEndian), "1\n-1\n\n\n"},
		{"UInt32", b.Reinterpret(ursus.Uint32, ursus.LittleEndian), "1\n4294967295\n\n\n"},
		{"Int16", b.Reinterpret(ursus.Int16, ursus.LittleEndian), "\n\n\n1\n"},
		{"Int64", b.Reinterpret(ursus.Int64, ursus.LittleEndian), "\n\n\n\n8589934593"},
		{"UInt8", b.Reinterpret(ursus.Uint8, ursus.LittleEndian), "\n\n\n\n"},
		{"big-endian", b.Reinterpret(ursus.Int32, ursus.BigEndian), "16777216\n-1\n\n\n"},
		{"Date", b.Reinterpret(ursus.Date, ursus.LittleEndian), "1970-01-02\n1969-12-31\n\n\n"},
	} {
		if got, want := csvOf(t, r.Select(tc.e.Alias("r"))), "r\n"+tc.want+"\n"; got != want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, want)
		}
	}
	if got := floatsOf(t, r.Select(b.Reinterpret(ursus.Float64, ursus.LittleEndian))); !sameFloats(got,
		[]float64{math.NaN(), math.NaN(), math.NaN(), math.NaN(), math.Float64frombits(0x0000000200000001)}) {
		t.Errorf("Float64: %v", got)
	}
	one := ursus.Frame(ursus.Values("b", [][]byte{{0, 0, 0, 0, 0, 0, 0xf0, 0x3f}}))
	if got := floatsOf(t, one.Select(b.Reinterpret(ursus.Float64, ursus.LittleEndian))); !sameFloats(got, []float64{1}) {
		t.Errorf("1.0: %v", got)
	}
	f32 := csvOf(t, r.Head(2).Select(b.Reinterpret(ursus.Float32, ursus.LittleEndian).IsNan().Alias("nan"),
		b.Reinterpret(ursus.Float32, ursus.LittleEndian).Cast(ursus.Float64).Eq(math.Float64frombits(0x36a0000000000000)).Alias("min")))
	if f32 != "nan,min\nfalse,true\ntrue,false\n" {
		t.Errorf("Float32, the least denormal and a NaN: %q", f32)
	}
	i128 := ursus.Frame(ursus.Values("b", [][]byte{append([]byte{1}, make([]byte, 15)...)}))
	if got := csvOf(t, i128.Select(b.Reinterpret(ursus.Int128, ursus.LittleEndian))); got != "b\n1\n" {
		t.Errorf("Int128: %q", got)
	}
	if got := csvOf(t, i128.Select(b.Reinterpret(ursus.Int128, ursus.BigEndian))); got != "b\n1329227995784915872903807060280344576\n" {
		t.Errorf("Int128 big-endian, 2^120: %q", got)
	}
	for name, dt := range map[string]ursus.DataType{"Boolean": ursus.Bool, "String": ursus.String} {
		if _, err := r.Select(b.Reinterpret(dt, ursus.LittleEndian)).Collect(t.Context()); err == nil ||
			!strings.Contains(err.Error(), "number or a temporal value") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

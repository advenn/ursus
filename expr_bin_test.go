package ursus_test

import (
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

// TestBinMethods is B of v0.6.6-scope.md (step 274): the binary namespace. Every
// answer over binFrame and the frames of "hello" is Polars 2.1.0's, recorded in the
// expr_bin group of testdata/oracle and spelled in oracle_expr_bin_test.go since step
// 499, which took this file's hand tables; what stays asserts the refusals.
func TestBinMethods(t *testing.T) {
	c := ursus.Col
	b := c("b").Bin()
	lf := binFrame()
	text := ursus.Frame(ursus.ValuesNullable("b", [][]byte{[]byte("6869"), []byte("zz"), nil}, []bool{true, true, false}))
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

// TestBinReinterpret: bytes read as numbers and instants, little- and big-endian,
// the oracle's expr_bin/reinterpret cases; a Boolean or a String is refused.
func TestBinReinterpret(t *testing.T) {
	b := ursus.Col("b").Bin()
	r := ursus.Frame(ursus.Values("b", [][]byte{{1, 0, 0, 0}, {0xff, 0xff, 0xff, 0xff}}))
	for name, dt := range map[string]ursus.DataType{"Boolean": ursus.Bool, "String": ursus.String} {
		if _, err := r.Select(b.Reinterpret(dt, ursus.LittleEndian)).Collect(t.Context()); err == nil ||
			!strings.Contains(err.Error(), "number or a temporal value") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

package ursus_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// TestJsonEncodeAnswersAsPolars: struct.json_encode (step 231) against Polars
// 1.44.1's text for the same struct: integers, a UInt64 past Int64, floats whole,
// huge, tiny, inexact, −0, NaN and infinite, a Float32, a Bool, a String with every
// kind of escape and a non-ASCII letter, a List with a null, a nested Struct, a
// Date and a Decimal. A Datetime is the string its Cast to String gives, which
// Polars spells otherwise; a null struct is null; a Binary is refused.
func TestJsonEncodeAnswersAsPolars(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	// At run time: a constant 0.1 + 0.2 is folded exactly, to 0.3.
	tenth, fifth := 0.1, 0.2
	frame := ursus.Frame(
		ursus.Values("i", []int64{1, 2}),
		ursus.Values("f", []float64{1, 0}),
		ursus.Values("g", []float64{1e20, 0}),
		ursus.Values("h", []float64{tenth + fifth, 0}),
		ursus.Values("n", []float64{math.NaN(), 0}),
		ursus.Values("inf", []float64{math.Inf(1), 0}),
		ursus.Values("b", []bool{true, false}),
		ursus.Values("str", []string{"q\"\\\n\té\u0001", ""}),
		ursus.Values("u", []uint64{1 << 63, 0}),
		ursus.Values("neg", []float64{math.Copysign(0, -1), 0}),
		ursus.Values("small", []float64{1e-7, 0}),
		ursus.Values("f32", []float32{0.1, 0}),
		ursus.Values("d", []time.Time{time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), {}}),
	).WithColumns(c("d").Cast(ursus.Date).Alias("d"))
	enc := ursus.Struct(c("i"), c("f"), c("g"), c("h"), c("n"), c("inf"), c("b"), c("str"),
		ursus.ConcatStr("", c("i")).Cast(ursus.Int64).Alias("l0"),
		c("u"), c("neg"), c("small"), c("f32"), c("d"),
		c("f").Add(12.3).Cast(dtype.Decimal(10, 2)).Alias("dec")).
		Struct().JsonEncode().Alias("j")
	got, err := frame.Select(enc).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"i":1,"f":1.0,"g":1e+20,"h":0.30000000000000004,"n":null,"inf":null,"b":true,` +
		`"str":"q\"\\\n\té\u0001","l0":1,"u":9223372036854775808,"neg":-0.0,"small":1e-7,` +
		`"f32":0.1,"d":"2024-01-02","dec":"13.30"}`
	if v, _, err := got.At[string](0, "j"); err != nil || v != want {
		t.Errorf("encoded\n%s\nwant\n%s: %v", v, want, err)
	}

	// A List with a null element and a nested Struct with a null field, as Polars'.
	nested, err := ursus.Frame(ursus.ValuesNullable("x", []int64{1, 0}, []bool{true, false})).
		GroupBy().Agg(c("x").Implode().Alias("l"), c("x").Last().Alias("x")).
		Select(ursus.Struct(c("l"), ursus.Struct(c("x")).Alias("st")).Struct().JsonEncode().Alias("j")).
		Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v, _, err := nested.At[string](0, "j"); err != nil || v != `{"l":[1,null],"st":{"x":null}}` {
		t.Errorf(`a list and a nested struct encode as %s, want {"l":[1,null],"st":{"x":null}}: %v`, v, err)
	}

	// A Datetime is the string a Cast to String gives it.
	ts := ursus.Frame(ursus.Values("ts", []time.Time{time.Date(2024, 1, 2, 3, 4, 5, 120000000, time.UTC)}))
	dt, err := ts.Select(ursus.Struct(c("ts")).Struct().JsonEncode().Alias("j"),
		ursus.ConcatStr("", ursus.Lit(`{"ts":"`), c("ts").Cast(ursus.String), ursus.Lit(`"}`)).Alias("w")).
		Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	j, _, _ := dt.At[string](0, "j")
	w, _, _ := dt.At[string](0, "w")
	if j != w {
		t.Errorf("a Datetime encodes as %s, want its cast's %s", j, w)
	}

	// A null struct is null.
	nul, err := frameOf(t, data.NewStruct("s", []*data.Column{
		data.NewFixed("a", dtype.Int64, []int64{1, 2}, bitmap.View{})}, validOf([]bool{true, false}))).
		Select(c("s").Struct().JsonEncode().Alias("j")).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := nul.At[string](1, "j"); err != nil || ok {
		t.Errorf("a null struct should encode as null: %v", err)
	}

	_, err = ursus.Frame(ursus.Values("bin", [][]byte{{0, 1}})).
		Select(ursus.Struct(c("bin")).Struct().JsonEncode()).Collect(ctx)
	if !errors.Is(err, uerr.ErrType) {
		t.Errorf("a Binary field should be refused: %v", err)
	}
}

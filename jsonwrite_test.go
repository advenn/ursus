package ursus_test

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestWriteJSON is step 333: Polars' write_ndjson, write_json and sink_ndjson. The
// text is Polars 1.44.1's, to the byte, of one frame holding every kind of value.
func TestWriteJSON(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	type st struct {
		X *int64 `ursus:"x"`
		Y string `ursus:"y"`
	}
	type nested struct {
		L  []*int64 `ursus:"l"`
		St *st      `ursus:"st"`
	}
	one, x := int64(1), int64(1)
	lf := ursus.Frame(i64s("i", 1, nil, 3), f64s("f", 1.0, nil, 1e20),
		ursus.Values("g", []float64{math.NaN(), math.Copysign(0, -1), 1e-7}),
		strs("s", `a"b`, nil, "é\n</x>\\"),
		ursus.ValuesNullable("b", []bool{true, false, false}, []bool{true, false, true}),
		ursus.ValuesNullable("d", []int32{19724, 0, -719162}, []bool{true, false, true}),
		ursus.ValuesNullable("t", []int64{1704164645123456, 0, 1704153600000000}, []bool{true, false, true}),
		ursus.ValuesNullable("tz", []int64{1704164645123456, 0, 1719889445000000}, []bool{true, false, true}),
		ursus.ValuesNullable("du", []int64{86403000005, 0, -500000}, []bool{true, false, true}),
		ursus.ValuesNullable("tm", []int64{11045000006000, 0, 0}, []bool{true, false, true}),
		f64s("dec", 1.25, nil, -3.5)).
		Select(c("i"), c("f"), c("g"), c("s"), c("b"), c("d").Cast(ursus.Date),
			c("t").Cast(ursus.Datetime(ursus.Micro, "")), c("tz").Cast(ursus.Datetime(ursus.Micro, "Asia/Kolkata")),
			c("du").Cast(ursus.Duration(ursus.Micro)), c("tm").Cast(ursus.TimeOf(ursus.Nano)),
			c("dec").Cast(ursus.Decimal(10, 2))).
		HStack(ursus.FromRows([]nested{{L: []*int64{&one, nil}, St: &st{X: &x, Y: "p"}}, {L: []*int64{}}, {St: &st{Y: "q"}}}))

	lines := []string{
		`{"i":1,"f":1.0,"g":null,"s":"a\"b","b":true,"d":"2024-01-02","t":"2024-01-02 03:04:05.123456","tz":"2024-01-02T08:34:05.123456+05:30","du":"PT86403.000005S","tm":"03:04:05.000006","dec":"1.25","l":[1,null],"st":{"x":1,"y":"p"}}`,
		`{"i":null,"f":null,"g":-0.0,"s":null,"b":null,"d":null,"t":null,"tz":null,"du":null,"tm":null,"dec":null,"l":[],"st":null}`,
		`{"i":3,"f":1e+20,"g":1e-7,"s":"é\n</x>\\","b":false,"d":"0001-01-01","t":"2024-01-02 00:00:00","tz":"2024-07-02T08:34:05+05:30","du":"-PT0.5S","tm":"00:00:00","dec":"-3.50","l":null,"st":{"x":null,"y":"q"}}`,
	}
	var nd, js bytes.Buffer
	if err := lf.WriteNDJSON(ctx, &nd); err != nil {
		t.Fatal(err)
	}
	if want := strings.Join(lines, "\n") + "\n"; nd.String() != want {
		t.Errorf("ndjson:\n%s\nwant:\n%s", nd.String(), want)
	}
	if err := lf.WriteJSON(ctx, &js); err != nil {
		t.Fatal(err)
	}
	if want := "[" + strings.Join(lines, ",") + "]"; js.String() != want {
		t.Errorf("json:\n%s\nwant:\n%s", js.String(), want)
	}

	// Batches of one row, encoded at once, write the same text.
	nd.Reset()
	js.Reset()
	if err := lf.WriteNDJSON(ctx, &nd, ursus.WithBatchSize(1)); err != nil || nd.String() != strings.Join(lines, "\n")+"\n" {
		t.Errorf("a row a batch: %v\n%s", err, nd.String())
	}
	if err := lf.WriteJSON(ctx, &js, ursus.WithBatchSize(1)); err != nil || js.String() != "["+strings.Join(lines, ",")+"]" {
		t.Errorf("a row a batch, an array: %v\n%s", err, js.String())
	}

	// No rows: "[]", and nothing a line.
	nd.Reset()
	js.Reset()
	if err := lf.Head(0).WriteNDJSON(ctx, &nd); err != nil || nd.Len() != 0 {
		t.Errorf("no rows, ndjson: %v %q", err, nd.String())
	}
	if err := lf.Head(0).WriteJSON(ctx, &js); err != nil || js.String() != "[]" {
		t.Errorf("no rows, json: %v %q", err, js.String())
	}

	// A year past 9999, and before 1: signed, as chrono writes it.
	nd.Reset()
	far := ursus.Frame(ursus.Values("d", []int32{2932897, -719529, -719528})).Select(c("d").Cast(ursus.Date))
	if err := far.WriteNDJSON(ctx, &nd); err != nil ||
		nd.String() != "{\"d\":\"+10000-01-01\"}\n{\"d\":\"-0001-12-31\"}\n{\"d\":\"0000-01-01\"}\n" {
		t.Errorf("far years: %v %q", err, nd.String())
	}

	// No length is "P0D"; a minute of milliseconds "PT60S".
	nd.Reset()
	durs := ursus.Frame(ursus.Values("du", []int64{0, 60_000})).Select(c("du").Cast(ursus.Duration(ursus.Milli)))
	if err := durs.WriteNDJSON(ctx, &nd); err != nil || nd.String() != "{\"du\":\"P0D\"}\n{\"du\":\"PT60S\"}\n" {
		t.Errorf("durations: %v %q", err, nd.String())
	}

	// SinkNDJSON writes the file whole, or nothing.
	dir := t.TempDir()
	path := filepath.Join(dir, "rows.ndjson")
	if err := lf.SinkNDJSON(ctx, path); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != strings.Join(lines, "\n")+"\n" {
		t.Errorf("sunk: %v\n%s", err, b)
	}
	if err := lf.Select(c("nope")).SinkNDJSON(ctx, filepath.Join(dir, "never.ndjson")); err == nil {
		t.Error("a failed query wrote a file")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*never*")); len(left) > 0 {
		t.Errorf("a failed sink left %v", left)
	}

	// Binary has no text, as Polars has none.
	bin := ursus.Frame(strs("s", "x")).Select(c("s").Cast(ursus.Binary))
	if err := bin.WriteNDJSON(ctx, &nd); err == nil || !strings.Contains(err.Error(), "Binary") {
		t.Errorf("binary: %v", err)
	}
}

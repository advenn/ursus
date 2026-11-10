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

// TestWriteJSON is step 333: Polars' write_ndjson, write_json and sink_ndjson. Each
// text is Polars 2.1.0's since step 502, to the byte, recorded in testdata/oracle/
// jsonwrite.json and spelled in oracle_jsonwrite_test.go; here stay the batches, the
// sink's file, and a Binary column, which Polars 2.1 panics on where ursus refuses.
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

	var nd, js bytes.Buffer
	if err := lf.WriteNDJSON(ctx, &nd); err != nil {
		t.Fatal(err)
	}
	if err := lf.WriteJSON(ctx, &js); err != nil {
		t.Fatal(err)
	}
	if nd.Len() == 0 || js.Len() == 0 {
		t.Fatalf("nothing written: %d and %d bytes", nd.Len(), js.Len())
	}

	// Batches of one row, encoded at once, write the same text.
	var nd1, js1 bytes.Buffer
	if err := lf.WriteNDJSON(ctx, &nd1, ursus.WithBatchSize(1)); err != nil || nd1.String() != nd.String() {
		t.Errorf("a row a batch: %v\n%s", err, nd1.String())
	}
	if err := lf.WriteJSON(ctx, &js1, ursus.WithBatchSize(1)); err != nil || js1.String() != js.String() {
		t.Errorf("a row a batch, an array: %v\n%s", err, js1.String())
	}

	// SinkNDJSON writes the file whole, or nothing.
	dir := t.TempDir()
	path := filepath.Join(dir, "rows.ndjson")
	if err := lf.SinkNDJSON(ctx, path); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != nd.String() {
		t.Errorf("sunk: %v\n%s", err, b)
	}
	if err := lf.Select(c("nope")).SinkNDJSON(ctx, filepath.Join(dir, "never.ndjson")); err == nil {
		t.Error("a failed query wrote a file")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*never*")); len(left) > 0 {
		t.Errorf("a failed sink left %v", left)
	}

	// Binary has no text, as Polars has none (it panics on one).
	bin := ursus.Frame(strs("s", "x")).Select(c("s").Cast(ursus.Binary))
	if err := bin.WriteNDJSON(ctx, &nd); err == nil || !strings.Contains(err.Error(), "Binary") {
		t.Errorf("binary: %v", err)
	}
}

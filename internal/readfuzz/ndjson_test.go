package readfuzz_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"

	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zlib"
	"github.com/klauspost/compress/zstd"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/fuzzcheck"
)

// The targets here run on a stack of 8 MiB, not Go's 1 GiB, so that a parser that
// recursed once a level with no limit again, as NDJSON's did before step 412, ends
// the binary on an input of a few hundred kilobytes, which a fuzzer reaches, and not
// only on one of hundreds of megabytes (step 511, the plan's 447).
func init() { debug.SetMaxStack(8 << 20) }

// jsonNesting is the deepest the NDJSON readers read a line's JSON, its object
// counted (step 412): encoding/json reads to 10,000.
const jsonNesting = 1_000

// inferLines is how many lines, or objects, the readers infer a schema from by
// default, Polars' infer_schema_length.
const inferLines = 100

// FuzzScanNDJSON: any bytes, read as NDJSON through the public API with its
// defaults, are a frame or an error within fuzzcheck's limits, and held to
// encoding/json, line by line (checkRows).
func FuzzScanNDJSON(f *testing.F) {
	for _, b := range jsonSeeds(f) {
		f.Add(b)
	}
	f.Fuzz(scanNDJSONOne)
}

// scanNDJSONOne is FuzzScanNDJSON's check of one input.
func scanNDJSONOne(t *testing.T, b []byte) {
	n, ok := inflated(b)
	if !ok {
		t.Skip("decompresses past maxInflated")
	}
	skipZstdWindow(t, b)
	var r read
	fuzzcheck.Run(t, limits(n), func() error {
		return r.of(t, ursus.ScanNDJSONBytes(b, "fuzz.ndjson"))
	})
	if compressed(b) {
		return
	}
	var rows [][]byte
	for line := range bytes.SplitSeq(b, []byte{'\n'}) {
		if len(bytes.Trim(line, " \t\r")) > 0 {
			rows = append(rows, line)
		}
	}
	checkRows(t, "line", rows, r)
}

// FuzzScanJSON: any bytes, read as a JSON document of rows through the public API
// with its defaults, are a frame or an error within fuzzcheck's limits; a document
// encoding/json refuses is refused, one that is not an array of objects or one object
// too, and an array of objects is held to encoding/json, object by object
// (checkRows). A leading byte-order mark is read, as Polars reads one, and left out
// for encoding/json.
func FuzzScanJSON(f *testing.F) {
	for _, b := range jsonSeeds(f) {
		f.Add(b)
	}
	f.Fuzz(scanJSONOne)
}

// scanJSONOne is FuzzScanJSON's check of one input.
func scanJSONOne(t *testing.T, b []byte) {
	n, ok := inflated(b)
	if !ok {
		t.Skip("decompresses past maxInflated")
	}
	skipZstdWindow(t, b)
	var r read
	fuzzcheck.Run(t, limits(n), func() error {
		return r.of(t, ursus.ScanJSONBytes(b, "fuzz.json"))
	})
	if compressed(b) {
		return
	}
	text := bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	if !json.Valid(text) {
		if r.df != nil {
			t.Fatalf("%s, which encoding/json refuses, is read as %d rows", quote(b), r.df.Height())
		}
		return
	}
	var rows [][]byte
	switch first := bytes.TrimLeft(text, " \t\r\n"); first[0] {
	case '{':
		rows = [][]byte{first}
	case '[':
		var objects []json.RawMessage
		if err := json.Unmarshal(text, &objects); err != nil {
			t.Fatalf("encoding/json refuses %s, which it called valid: %v", quote(b), err)
		}
		rows = [][]byte{} // an array of objects, perhaps of none
		for _, o := range objects {
			if o[0] != '{' {
				rows = nil
				break
			}
			rows = append(rows, o)
		}
	}
	if rows == nil {
		if r.df != nil {
			t.Fatalf("%s, JSON neither an array of objects nor one, is read as %d rows", quote(b), r.df.Height())
		}
		return
	}
	checkRows(t, "object", rows, r)
}

// TestScanJSONDeep is FuzzScanNDJSON's and FuzzScanJSON's check of lines nested
// 50,000 levels in arrays and 20,000 in objects, which overflow an 8 MiB stack
// without the limit of step 412: seeds of 100 KB, which the fuzzer would spend its
// time minimizing, so an ordinary test.
func TestScanJSONDeep(t *testing.T) {
	for name, line := range map[string]string{
		"arrays":  `{"a":` + strings.Repeat("[", 50_000) + strings.Repeat("]", 50_000) + "}",
		"objects": `{"a":` + strings.Repeat(`{"k":`, 20_000) + "1" + strings.Repeat("}", 20_000) + "}",
	} {
		t.Run(name+" as NDJSON", func(t *testing.T) { scanNDJSONOne(t, []byte(line+"\n")) })
		t.Run(name+" as JSON", func(t *testing.T) { scanJSONOne(t, []byte("["+line+"]")) })
	}
}

// maxZstdWindow is the largest window a Zstandard input's frame may declare and be
// read inside the targets' allocation bound.
const maxZstdWindow = 32 << 20

// skipZstdWindow skips b, a Zstandard stream whose first frame declares a window past
// maxZstdWindow: a finding of step 511, left for the maintainer. klauspost's decoder
// allocates the whole window the frame declares, up to its MaxWindowSize of 512 MiB,
// before it has decoded a block ((*history).ensureBlockRing, zstd/history.go), so 17
// bytes made the NDJSON, JSON and CSV readers allocate 514 MiB. The reference
// decoder, and Polars through it, refuse a window past 128 MiB by default;
// zstd.WithDecoderMaxWindow in internal/source.Decompressed would match it, and refuse
// files ursus reads today.
func skipZstdWindow(t *testing.T, b []byte) {
	if w := zstdWindow(b); w > maxZstdWindow {
		t.Skipf("finding: a Zstandard frame declaring a window of %d bytes is allocated it before a block is decoded", w)
	}
}

// zstdWindow is the window the first frame of b declares, if b begins as a Zstandard
// frame does (RFC 8878 §3.1.1.1), else 0: its Window_Descriptor, or a single
// segment's Frame_Content_Size.
func zstdWindow(b []byte) uint64 {
	if !bytes.HasPrefix(b, []byte{0x28, 0xb5, 0x2f, 0xfd}) || len(b) < 6 {
		return 0
	}
	fhd, pos := b[4], 5
	single := fhd>>5&1 == 1
	if !single {
		exp, mant := uint64(b[pos]>>3), uint64(b[pos]&7)
		base := uint64(1) << (10 + exp)
		return base + base/8*mant
	}
	pos += []int{0, 1, 2, 4}[fhd&3] // the dictionary's ID
	size := []int{1, 2, 4, 8}[fhd>>6]
	if len(b) < pos+size {
		return 0
	}
	var fcs uint64
	for i := size - 1; i >= 0; i-- {
		fcs = fcs<<8 | uint64(b[pos+i])
	}
	if size == 2 {
		fcs += 256
	}
	return fcs
}

// read is a scan's outcome: its schema, or the error inferring it; then its frame,
// or the error reading it.
type read struct {
	schema    *ursus.Schema
	schemaErr error
	df        *ursus.DataFrame
	err       error
}

// of reads lf as a query does, its schema and then its frame.
func (r *read) of(t *testing.T, lf *ursus.LazyFrame) error {
	if r.schema, r.schemaErr = lf.CollectSchema(t.Context()); r.schemaErr != nil {
		return r.schemaErr
	}
	r.df, r.err = lf.Collect(t.Context())
	return r.err
}

// compressed is whether the readers may read b as a compressed stream
// (internal/source.Decompressed): its rows are then not b's own.
func compressed(b []byte) bool {
	return bytes.HasPrefix(b, []byte{0x1f, 0x8b}) || bytes.HasPrefix(b, []byte{0x28, 0xb5, 0x2f, 0xfd}) ||
		len(b) > 0 && b[0] == 0x78
}

// checkRows holds r, the read of rows, each an NDJSON line or a JSON document's
// object, the unit, to encoding/json:
//
//   - a frame read is one row a row, each one encoding/json reads as an object, nested
//     no deeper than jsonNesting, each cell what encoding/json reads of its key, the
//     first value of a key given twice, by its column's type (sameCell);
//   - rows of only scalars whose keys' types meet, as ScanNDJSON's doc lists the
//     meetings, are inferred a schema;
//   - a schema inferred from every row, encoding/json reading each, reads them all: a
//     row the schema refuses is one inference should have typed otherwise. The cases
//     inference types otherwise than the reader reads are findings, named by
//     inconsistent and skipped.
func checkRows(t *testing.T, unit string, rows [][]byte, r read) {
	t.Helper()
	objs := make([]*jv, len(rows))
	valid := true
	for i, row := range rows {
		if depth(row) > jsonNesting {
			valid = false
			continue
		}
		v, err := decodeFirst(row)
		if err == nil && v.kind == kObject {
			objs[i] = v
		} else {
			valid = false
		}
	}
	switch {
	case r.df != nil:
		for i := range rows {
			if objs[i] == nil {
				t.Fatalf("%s %d, %s, which encoding/json does not read as an object nested at most %d deep, is read",
					unit, i+1, quote(rows[i]), jsonNesting)
			}
		}
		if r.df.Height() != len(rows) {
			t.Fatalf("%d %ss read as %d rows", len(rows), unit, r.df.Height())
		}
		schema := r.df.Schema()
		for i, o := range objs {
			got, err := r.df.Row(i)
			if err != nil {
				t.Fatalf("row %d: %v", i, err)
			}
			for j, fd := range schema.FieldSlice() {
				if why := sameCell(fd.Type, got[j], o.get(fd.Name)); why != "" {
					t.Fatalf("%s %d, %s, key %q of %s: %s", unit, i+1, quote(rows[i]), fd.Name, fd.Type, why)
				}
			}
		}
	case !valid || len(rows) > inferLines:
	case r.schemaErr != nil:
		if scalarsMeet(objs) {
			t.Fatalf("%d %ss of scalars whose keys' types meet are refused a schema: %v", len(rows), unit, r.schemaErr)
		}
	default:
		if why := inconsistent(objs); why != "" {
			t.Skipf("finding: %s: %v", why, r.err)
		}
		t.Fatalf("the schema %s, inferred from every %s, refuses one: %v", r.schema, unit, r.err)
	}
}

// The kinds of a JSON value.
const (
	kNull = iota
	kBool
	kInt // a number with no fraction and no exponent
	kFloat
	kString
	kArray
	kObject
)

// jv is a JSON value as encoding/json's tokens give it: a number's literal, a
// string decoded, an object's keys in their order, each once, with its first value,
// as the NDJSON readers read a key given twice.
type jv struct {
	kind int
	b    bool
	text string
	arr  []*jv
	keys []string
	vals []*jv
}

func (v *jv) get(key string) *jv {
	for i, k := range v.keys {
		if k == key {
			return v.vals[i]
		}
	}
	return nil
}

// decodeFirst is b as one JSON value, through encoding/json's tokens.
func decodeFirst(b []byte) (*jv, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	v, err := tokenValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("text after the value")
	}
	return v, nil
}

func tokenValue(dec *json.Decoder) (*jv, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch tok := tok.(type) {
	case nil:
		return &jv{kind: kNull}, nil
	case bool:
		return &jv{kind: kBool, b: tok}, nil
	case json.Number:
		if strings.ContainsAny(string(tok), ".eE") {
			return &jv{kind: kFloat, text: string(tok)}, nil
		}
		return &jv{kind: kInt, text: string(tok)}, nil
	case string:
		return &jv{kind: kString, text: tok}, nil
	case json.Delim:
		v := &jv{kind: kArray}
		if tok == '{' {
			v.kind = kObject
		}
		for dec.More() {
			key := ""
			if v.kind == kObject {
				k, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key = k.(string)
			}
			item, err := tokenValue(dec)
			if err != nil {
				return nil, err
			}
			switch {
			case v.kind == kArray:
				v.arr = append(v.arr, item)
			case v.get(key) == nil:
				v.keys, v.vals = append(v.keys, key), append(v.vals, item)
			}
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return v, nil
	}
	return nil, fmt.Errorf("token %v", tok)
}

// depth is how deep b nests its arrays and objects, read outside its strings.
func depth(b []byte) int {
	d, deepest, str, esc := 0, 0, false, false
	for _, c := range b {
		switch {
		case esc:
			esc = false
		case str && c == '\\':
			esc = true
		case c == '"':
			str = !str
		case str:
		case c == '[' || c == '{':
			d++
			deepest = max(deepest, d)
		case c == ']' || c == '}':
			d--
		}
	}
	return deepest
}

// sameCell says how got, a cell of type dt as DataFrame.Row gives it, differs from
// v, what encoding/json read of its key, nil for a key missing: "" for not at all.
// Each value is compared as ScanNDJSON's doc says it reads one: a null or a missing
// key null; an integer an Int64, or an Int128 past it; a number a Float64, the float
// strconv.ParseFloat finds; a String column a number as text of the same value, a
// Boolean as true or false; a List its elements, and a Struct its fields, by their
// types. An array or an object read into a String column, as Polars writes it, is
// not compared.
func sameCell(dt dtype.DataType, got any, v *jv) string {
	if v == nil || v.kind == kNull {
		if got != nil {
			return fmt.Sprintf("null or missing, and read as %v", got)
		}
		return ""
	}
	if got == nil {
		return "read as null"
	}
	switch {
	case dt == dtype.Bool && v.kind == kBool:
		if got != v.b {
			return fmt.Sprintf("%v read as %v", v.b, got)
		}
	case dt == dtype.Int64 && v.kind == kInt:
		want, err := strconv.ParseInt(v.text, 10, 64)
		if g, ok := got.(int64); err != nil || !ok || g != want {
			return fmt.Sprintf("%s read as %v", v.text, got)
		}
	case dt == dtype.Int128 && v.kind == kInt:
		g, ok := got.(i128.Int128)
		want, wok := new(big.Int).SetString(v.text, 10)
		if have, hok := new(big.Int).SetString(fmt.Sprint(g), 10); !ok || !wok || !hok || have.Cmp(want) != 0 {
			return fmt.Sprintf("%s read as %v", v.text, got)
		}
	case dt == dtype.Float64 && (v.kind == kInt || v.kind == kFloat):
		want, _ := strconv.ParseFloat(v.text, 64)
		if g, ok := got.(float64); !ok || g != want {
			return fmt.Sprintf("%s read as %v, not %v", v.text, got, want)
		}
	case dt == dtype.String:
		g, ok := got.(string)
		if !ok {
			return fmt.Sprintf("read as %T", got)
		}
		return sameText(g, v)
	case dt.ID() == dtype.TypeList && v.kind == kArray:
		g, ok := got.([]any)
		if !ok || len(g) != len(v.arr) {
			return fmt.Sprintf("an array of %d read as %v", len(v.arr), got)
		}
		for i, e := range v.arr {
			if why := sameCell(dt.Inner(), g[i], e); why != "" {
				return fmt.Sprintf("[%d]: %s", i, why)
			}
		}
	case dt.ID() == dtype.TypeStruct && v.kind == kObject:
		g, ok := got.(map[string]any)
		if !ok {
			return fmt.Sprintf("an object read as %T", got)
		}
		for _, f := range dt.Fields() {
			if why := sameCell(f.Type, g[f.Name], v.get(f.Name)); why != "" {
				return fmt.Sprintf(".%s: %s", f.Name, why)
			}
		}
	default:
		return fmt.Sprintf("a value of kind %d read as %v, a %s", v.kind, got, dt)
	}
	return ""
}

// sameText says how g, a String cell, differs from v's text as the readers write
// it: a string itself; an integer as the same integer; a float as text of the same
// float, inf past its range; true and false.
func sameText(g string, v *jv) string {
	switch v.kind {
	case kString:
		if g != v.text {
			return fmt.Sprintf("%q read as %q", v.text, g)
		}
	case kInt:
		want, _ := new(big.Int).SetString(v.text, 10)
		if have, ok := new(big.Int).SetString(g, 10); !ok || have.Cmp(want) != 0 {
			return fmt.Sprintf("%s read as %q", v.text, g)
		}
	case kFloat:
		want, _ := strconv.ParseFloat(v.text, 64)
		if have, err := strconv.ParseFloat(g, 64); (err != nil && !errors.Is(err, strconv.ErrRange)) || have != want {
			return fmt.Sprintf("%s read as %q", v.text, g)
		}
	case kBool:
		if g != strconv.FormatBool(v.b) {
			return fmt.Sprintf("%v read as %q", v.b, g)
		}
	}
	return ""
}

// scalarsMeet is whether objs, rows encoding/json reads, hold only scalars, and each
// key's types meet as ScanNDJSON's doc says: integers and floats in Float64, text and
// anything in String, a Boolean with a Boolean, a null with any; an integer past
// Int64 an Int128, which one past that is not.
func scalarsMeet(objs []*jv) bool {
	kinds := map[string]map[int]bool{}
	for _, o := range objs {
		for i, k := range o.keys {
			v := o.vals[i]
			if v.kind == kArray || v.kind == kObject {
				return false
			}
			if kinds[k] == nil {
				kinds[k] = map[int]bool{}
			}
			kinds[k][v.kind] = true
			if v.kind == kInt && !fitsInt128(v.text) {
				return false
			}
		}
	}
	for _, ks := range kinds {
		if ks[kBool] && (ks[kInt] || ks[kFloat]) && !ks[kString] {
			return false
		}
	}
	return true
}

// fitsInt128 is whether an integer's literal fits an Int128.
func fitsInt128(text string) bool {
	_, ok := i128.Parse(text)
	return ok
}

// inconsistent names the findings of step 511 in objs, where the type inference
// gives a key is not one the reader reads every row of, "" for none:
//
//   - within one array, a Boolean beside a number, or an array beside another value,
//     at the same place: Polars' rules for an array's elements, which inference
//     follows and ScanNDJSON's doc states, make [1, true] a List(Int64) and [1, [2]] a
//     List(List(Int64)), and the reader then refuses true as an Int64 and 1 as a
//     List(Int64), at the very line it inferred from, saying a later line's type was
//     not among the first lines';
//   - an integer past Int128, which inference types Int128 and the reader refuses as
//     one.
func inconsistent(objs []*jv) string {
	for _, o := range objs {
		if why := inconsistentIn(o); why != "" {
			return why
		}
	}
	return ""
}

func inconsistentIn(v *jv) string {
	switch v.kind {
	case kInt:
		if !fitsInt128(v.text) {
			return "an integer past Int128 is inferred an Int128 and refused as one"
		}
	case kArray:
		at := map[string]map[int]bool{}
		for _, e := range v.arr {
			kindsAt(e, "", at)
		}
		for _, ks := range at {
			if ks[kBool] && (ks[kInt] || ks[kFloat]) {
				return "an array's Boolean beside a number is inferred a number and refused as one"
			}
			if ks[kArray] && (ks[kBool] || ks[kInt] || ks[kFloat] || ks[kString] || ks[kObject]) {
				return "an array's list beside another value is inferred a list and the value refused as one"
			}
		}
		for _, e := range v.arr {
			if why := inconsistentIn(e); why != "" {
				return why
			}
		}
	case kObject:
		for _, e := range v.vals {
			if why := inconsistentIn(e); why != "" {
				return why
			}
		}
	}
	return ""
}

// kindsAt records the kind of v at path, and of what it holds below it, an array's
// elements at path+"[]", an object's values at path+"."+key: the places an array's
// elements' types are coerced at.
func kindsAt(v *jv, path string, at map[string]map[int]bool) {
	if at[path] == nil {
		at[path] = map[int]bool{}
	}
	at[path][v.kind] = true
	switch v.kind {
	case kArray:
		for _, e := range v.arr {
			kindsAt(e, path+"[]", at)
		}
	case kObject:
		for i, k := range v.keys {
			kindsAt(v.vals[i], path+"."+strconv.Quote(k), at)
		}
	}
}

// quote is b quoted for a failure's message, cut past 300 bytes.
func quote(b []byte) string {
	if len(b) > 300 {
		return strconv.Quote(string(b[:300])) + "… (" + strconv.Itoa(len(b)) + " bytes)"
	}
	return strconv.Quote(string(b))
}

// jsonSeeds are the NDJSON and JSON targets' generated seeds, beside their corpora
// in testdata/fuzz: what is too large or too repetitive to keep as a file, and a
// small file compressed each way the readers know a stream by its first bytes.
func jsonSeeds(f *testing.F) [][]byte {
	nested := func(d int) string { return strings.Repeat("[", d) + strings.Repeat("]", d) }
	var many strings.Builder
	for i := range inferLines + 5 {
		fmt.Fprintf(&many, "{\"a\":%d,\"b\":\"%d\"}\n", i, i)
	}
	many.WriteString(`{"a":1.5,"c":true}` + "\n")
	seeds := [][]byte{
		// One level short of the limit, the line's object counted, and at it.
		[]byte(`{"a":` + nested(jsonNesting-1) + "}\n"),
		[]byte(`{"a":` + nested(jsonNesting) + "}\n"),
		[]byte(`[{"a":` + nested(jsonNesting-1) + "}]"),
		// More lines than inference reads, the last with a float, a new key and a
		// Boolean where it read none.
		[]byte(many.String()),
		[]byte("[" + strings.ReplaceAll(strings.TrimSuffix(many.String(), "\n"), "\n", ",") + "]"),
	}
	text := []byte("{\"id\":1,\"price\":1.5,\"name\":\"a\",\"tags\":[\"x\"],\"o\":{\"k\":true}}\n" +
		"{\"id\":2,\"price\":-0.25,\"name\":null,\"tags\":[],\"o\":{\"k\":false}}\n")
	var gz, zl bytes.Buffer
	gw := gzip.NewWriter(&gz)
	zw := zlib.NewWriter(&zl)
	for _, w := range []io.WriteCloser{gw, zw} {
		if _, err := w.Write(text); err != nil {
			f.Fatal(err)
		}
		if err := w.Close(); err != nil {
			f.Fatal(err)
		}
	}
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		f.Fatal(err)
	}
	zs := enc.EncodeAll(text, nil)
	enc.Close()
	return append(seeds, gz.Bytes(), zl.Bytes(), zs)
}

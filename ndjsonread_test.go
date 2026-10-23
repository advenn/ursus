package ursus_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/ursustest"
)

// TestReadNDJSON is step 334: Polars' read_ndjson and scan_ndjson. Every answer is
// Polars 1.44.1's.
func TestReadNDJSON(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	read := func(t *testing.T, text string, opts ...ursus.NDJSONOption) *ursus.DataFrame {
		t.Helper()
		got, err := ursus.ScanNDJSONBytes([]byte(text), "t.ndjson", opts...).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	schemaOf := func(df *ursus.DataFrame) string { return df.Schema().String() }

	t.Run("a column a key", func(t *testing.T) {
		got := read(t, "{\"a\":1,\"b\":\"x\",\"c\":1.5}\n{\"a\":2,\"b\":null,\"c\":2}\n{\"a\":null,\"b\":\"z\"}\n"+
			"{\"a\":4,\"b\":\"w\",\"c\":3.25,\"d\":true}\n")
		ursustest.AssertFrameEqual(t, got, dfOf(t, i64s("a", 1, 2, nil, 4), strs("b", "x", nil, "z", "w"),
			f64s("c", 1.5, 2.0, nil, 3.25), ursus.ValuesNullable("d", []bool{false, false, false, true},
				[]bool{false, false, false, true})))
		// The keys in the order first seen; a key twice, its first value.
		got = read(t, "{\"b\":1,\"a\":2}\n{\"a\":3,\"c\":4}\n{\"a\":5,\"a\":6}\n")
		ursustest.AssertFrameEqual(t, got, dfOf(t, i64s("b", 1, nil, nil), i64s("a", 2, 3, 5), i64s("c", nil, 4, nil)))
	})

	t.Run("types met", func(t *testing.T) {
		for _, tc := range []struct {
			name, text, schema string
		}{
			{"integer then float", "{\"a\":1}\n{\"a\":2.5}\n", "{a: Float64}"},
			{"integer and text", "{\"a\":1}\n{\"a\":\"x\"}\n", "{a: String}"},
			{"a Boolean and text", "{\"a\":true}\n{\"a\":\"x\"}\n", "{a: String}"},
			{"only nulls", "{\"a\":null}\n{\"a\":null}\n", "{a: Null}"},
			{"a null then an integer", "{\"a\":null}\n{\"a\":1}\n", "{a: Int64}"},
			{"past Int64", "{\"a\":9223372036854775807}\n{\"a\":18446744073709551615}\n", "{a: Int128}"},
			{"an exponent", "{\"a\":1e3}\n", "{a: Float64}"},
			{"a list of integers and a float", "{\"a\":[1,2.5]}\n", "{a: List(Float64)}"},
			{"a list of integers and text", "{\"a\":[1,\"x\"]}\n", "{a: List(String)}"},
			{"a list of objects", "{\"a\":[{\"x\":1},{\"y\":\"z\"}]}\n", "{a: List(Struct(x: Int64, y: String))}"},
			{"two objects", "{\"s\":{\"x\":1}}\n{\"s\":{\"y\":\"q\"}}\n", "{s: Struct(x: Int64, y: String)}"},
			{"a list and text", "{\"a\":\"s\"}\n{\"a\":[1]}\n", "{a: String}"},
			{"text and an object", "{\"a\":{\"x\":1}}\n{\"a\":\"s\"}\n", "{a: String}"},
			// An array's elements meet by rules of their own.
			{"a float and an object", `{"a":[1.0,{"b":"q"}]}`, "{a: List(String)}"},
			{"a float, a Boolean, an integer", `{"a":[2.5,true,1]}`, "{a: List(String)}"},
			{"an object and a number", `{"a":[{"x":1},1]}`, "{a: List(String)}"},
			{"objects", `{"a":[{"y":1},{"x":true},{"y":"s","z":null}]}`, "{a: List(Struct(y: String, x: Bool, z: Null))}"},
			{"past Int64 and an integer", `{"a":[18446744073709551615,1]}`, "{a: List(String)}"},
			{"past Int64 alone", `{"a":[18446744073709551615]}`, "{a: List(Int128)}"},
			{"an empty list and a list", `{"a":[[],[1]]}`, "{a: List(List(Int64))}"},
		} {
			got := read(t, tc.text)
			if s := schemaOf(got); s != tc.schema {
				t.Errorf("%s: %s, want %s", tc.name, s, tc.schema)
			}
		}
		// The text of a number beside text, as Polars writes it.
		got := read(t, "{\"a\":1}\n{\"a\":\"x\"}\n{\"a\":2.5}\n{\"a\":true}\n")
		ursustest.AssertFrameEqual(t, got, dfOf(t, ursus.Values("a", []string{"1", "x", "2.5", "true"})))
		// A number as Rust prints it, an array or an object as JSON of Polars'
		// spacing, its strings' quotes, newlines and carriage returns escaped and
		// nothing else, its keys not at all.
		got = read(t, `{"a":"s"}`+"\n"+`{"a":1.50}`+"\n"+`{"a":1e3}`+"\n"+`{"a":-0}`+"\n"+`{"a":1E-7}`+"\n"+
			`{"a":-0.0}`+"\n"+`{"a":[1.0,{"b":"q\"\n\t\\"},null,true]}`+"\n"+`{"a":{"k\"":[]}}`+"\n"+
			`{"a":12345678901234567890}`+"\n"+`{"a":  [ [ ] ,  { } ] }`+"\n")
		ursustest.AssertFrameEqual(t, got, dfOf(t, ursus.Values("a", []string{"s", "1.5", "1000", "0", "0.0000001",
			"-0", "[1, {\"b\": \"q\\\"\\n\t\\\"}, null, true]", "{\"k\"\": []}", "12345678901234567890", "[[], {}]"})))
		got = read(t, `{"a":[1.0,{"b":"q"}]}`+"\n"+`{"a":[2.5,true,1]}`+"\n")
		if rows, err := got.ToMaps(); err != nil || fmt.Sprint(rows) != `[map[a:[1 {"b": "q"}]] map[a:[2.5 true 1]]]` {
			t.Errorf("elements as text: %v, %v", rows, err)
		}
		got = read(t, "{\"a\":[1,2.5]}\n{\"a\":[]}\n{\"a\":null}\n")
		if s := got.String(); !strings.Contains(s, "[1, 2.5]") || !strings.Contains(s, "[]") || !strings.Contains(s, "null") {
			t.Errorf("a list:\n%s", s)
		}
		got = read(t, "{\"s\":{\"x\":1}}\n{\"s\":{\"y\":\"q\"}}\n{\"s\":null}\n")
		if s := got.String(); !strings.Contains(s, `{x: 1, y: null}`) || !strings.Contains(s, `{x: null, y: "q"}`) {
			t.Errorf("a struct:\n%s", s)
		}
	})

	t.Run("text", func(t *testing.T) {
		got := read(t, "{\"a\":\"\\u00e9\\ud83d\\ude00\",\"b\":\"a\\\"b\\\\c\\n\"}\n  {\"a\" : \"x\" , \"b\":\"y\" }  \r\n\n")
		ursustest.AssertFrameEqual(t, got, dfOf(t, ursus.Values("a", []string{"é😀", "x"}),
			ursus.Values("b", []string{"a\"b\\c\n", "y"})))
	})

	t.Run("refused", func(t *testing.T) {
		for _, tc := range []struct {
			name, text, want string
			opts             []ursus.NDJSONOption
		}{
			{"a Boolean and a number", "{\"a\":true}\n{\"a\":1}\n", "no type in common", nil},
			{"an object and a number", "{\"s\":{\"x\":1}}\n{\"s\":5}\n", "no type in common", nil},
			{"a later line's type", "{\"a\":1}\n{\"a\":\"x\"}\n", "cannot parse 'x' (a string) as Int64",
				[]ursus.NDJSONOption{ursus.WithInferSchemaLines(1)}},
			{"a line not an object", "[1,2]\n", "holds an array", nil},
			{"a malformed line", "{\"a\":1}\n{\"a\":\n", "malformed JSON", nil},
			{"a leading zero", "{\"a\":01}\n", "leading zero", nil},
			{"a leading zero in a float's column", "{\"a\":1.5}\n{\"a\":-01.5}\n", "leading zero",
				[]ursus.NDJSONOption{ursus.WithInferSchemaLines(1)}},
			{"a key's types, by line", "{\"a\":1}\n\n{\"a\":true}\n", "at line 3, key a", nil},
			{"a line's place, past a blank one", "{\"a\":1}\n \n{\"a\":\"x\"}\n", "at line 3, key a",
				[]ursus.NDJSONOption{ursus.WithInferSchemaLines(1)}},
			// Elements a Float64 inferred for, and a Boolean among them.
			{"a Boolean among numbers", `{"a":[1,true,2.5]}`, "cannot parse 'true' (a Boolean) as Float64", nil},
		} {
			_, err := ursus.ScanNDJSONBytes([]byte(tc.text), "t.ndjson", tc.opts...).Collect(ctx)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: %v, want an error saying %q", tc.name, err, tc.want)
			}
		}
	})

	t.Run("a schema given", func(t *testing.T) {
		s, err := ursus.NewSchema(ursus.Field{Name: "a", Type: ursus.Float64}, ursus.Field{Name: "d", Type: ursus.Date})
		if err != nil {
			t.Fatal(err)
		}
		got := read(t, "{\"a\":1,\"b\":\"x\",\"d\":\"2024-01-02\"}\n{\"a\":2.5,\"d\":null}\n", ursus.WithNDJSONSchema(s))
		want := collect(t, ursus.Frame(f64s("a", 1.0, 2.5), ursus.ValuesNullable("d", []int32{19724, 0}, []bool{true, false})).
			Select(c("a"), c("d").Cast(ursus.Date)))
		ursustest.AssertFrameEqual(t, got, want)

		// Each type Polars reads, as it reads it; a value refused as the schema's
		// type, without inference's hint.
		s = mustSchema(ursus.Field{Name: "i", Type: ursus.Int32}, ursus.Field{Name: "u", Type: ursus.Uint64},
			ursus.Field{Name: "f", Type: ursus.Float32}, ursus.Field{Name: "c", Type: dtype.Enum("a", "2024-01-02")})
		got = read(t, `{"i":1,"u":1,"f":1,"c":"a"}`+"\n"+`{"i":300,"u":18446744073709551615,"f":300,"c":"2024-01-02"}`+"\n",
			ursus.WithNDJSONSchema(s))
		want = collect(t, ursus.Frame(ursus.Values("i", []int32{1, 300}), ursus.Values("u", []uint64{1, 18446744073709551615}),
			ursus.Values("f", []float32{1, 300}), strs("c", "a", "2024-01-02")).Select(c("i"), c("u"), c("f"),
			c("c").Cast(dtype.Enum("a", "2024-01-02"))))
		ursustest.AssertFrameEqual(t, got, want)
		for _, tc := range []struct{ text, want string }{
			{`{"i":"a"}`, "cannot parse 'a' (a string) as Int32"},
			{`{"i":3000000000}`, "cannot parse '3000000000' (a number) as Int32"},
			{`{"u":-1}`, "cannot parse '-1' (a number) as Uint64"},
		} {
			_, err := ursus.ScanNDJSONBytes([]byte(`{"i":1}`+"\n"+tc.text+"\n"), "t.ndjson", ursus.WithNDJSONSchema(s)).Collect(ctx)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "at line 2") ||
				strings.Contains(err.Error(), "first lines") {
				t.Errorf("%s: %v, want %q at line 2", tc.text, err, tc.want)
			}
		}
	})

	t.Run("errors ignored", func(t *testing.T) {
		one := []ursus.NDJSONOption{ursus.WithInferSchemaLines(1), ursus.WithNDJSONIgnoreErrors(true)}
		// Polars 1.44.1's answers, ignore_errors=True.
		got := read(t, `{"a":1}`+"\n"+`{"a":"x"}`+"\n"+`{"a":2.5}`+"\n"+`{"a":3}`+"\n"+`{"a":[1]}`+"\n", one...)
		ursustest.AssertFrameEqual(t, got, dfOf(t, i64s("a", 1, nil, nil, 3, nil)))
		got = read(t, `{"b":true}`+"\n"+`{"b":1}`+"\n"+`{"b":"t"}`+"\n", one...)
		ursustest.AssertFrameEqual(t, got, dfOf(t, ursus.ValuesNullable("b", []bool{true, false, false}, []bool{true, false, false})))
		got = read(t, `{"a":[1,2]}`+"\n"+`{"a":[1,"x",{"y":1}]}`+"\n"+`{"a":"s"}`+"\n", one...)
		if rows, err := got.ToMaps(); err != nil || fmt.Sprint(rows) != "[map[a:[1 2]] map[a:[1 <nil> <nil>]] map[a:<nil>]]" {
			t.Errorf("a list: %v, %v", rows, err)
		}
		// A value that is not an object, in a Struct column, is one of no keys.
		got = read(t, `{"o":{"x":1,"y":"q"}}`+"\n"+`{"o":5}`+"\n"+`{"o":null}`+"\n"+`{"o":{"x":"bad","y":"r"}}`+"\n", one...)
		rows, err := got.ToMaps()
		if err != nil || fmt.Sprint(rows) != "[map[o:map[x:1 y:q]] map[o:map[x:<nil> y:<nil>]] map[o:<nil>] map[o:map[x:<nil> y:r]]]" {
			t.Errorf("a struct: %v, %v", rows, err)
		}
		date := mustSchema(ursus.Field{Name: "d", Type: ursus.Date})
		got = read(t, `{"d":"2024-01-02"}`+"\n"+`{"d":"nope"}`+"\n", ursus.WithNDJSONSchema(date), ursus.WithNDJSONIgnoreErrors(true))
		ursustest.AssertFrameEqual(t, got, collect(t, ursus.Frame(ursus.ValuesNullable("d", []int32{19724, 0},
			[]bool{true, false})).Select(c("d").Cast(ursus.Date))))
		// A line that is not JSON is refused all the same.
		_, err = ursus.ScanNDJSONBytes([]byte(`{"a":1}`+"\n"+`{"a":`+"\n"), "t.ndjson", one...).Collect(ctx)
		if err == nil || !strings.Contains(err.Error(), "malformed JSON") {
			t.Errorf("malformed: %v", err)
		}
	})

	t.Run("several files", func(t *testing.T) {
		dir := t.TempDir()
		write := func(name, text string) string {
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			return path
		}
		a := write("a.ndjson", `{"a":1,"b":"x"}`+"\n"+`{"a":2,"b":"y"}`+"\n")
		b := write("b.ndjson", `{"a":3,"b":"z","c":true}`+"\n"+`{"b":"w","a":4}`+"\n")
		bad := write("c.ndjson", "\n"+`{"a":"text","b":"q"}`+"\n")
		// Polars' answer: the first file's schema, a key a later file adds skipped.
		want := dfOf(t, i64s("a", 1, 2, 3, 4), strs("b", "x", "y", "z", "w"))
		open := func(path string) func(context.Context) (io.ReadCloser, error) {
			return func(context.Context) (io.ReadCloser, error) { return os.Open(path) }
		}
		for name, lf := range map[string]*ursus.LazyFrame{
			"files": ursus.ScanNDJSONFiles([]string{a, b}),
			"glob":  ursus.ScanNDJSONGlob(filepath.Join(dir, "[ab].ndjson")),
			"from":  ursus.ScanNDJSONFrom([]ursus.NDJSONFile{{Name: "a", Open: open(a)}, {Name: "b", Open: open(b)}}),
		} {
			got, err := lf.Collect(ctx)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			ursustest.AssertFrameEqual(t, got, want)
		}
		if plan, err := ursus.ScanNDJSONFiles([]string{a, b}).Explain(ctx); err != nil || !strings.Contains(plan, "a.ndjson and 1 more") {
			t.Errorf("the plan: %v\n%s", err, plan)
		}
		_, err := ursus.ScanNDJSONFiles([]string{a, bad}).Collect(ctx)
		if err == nil || !strings.Contains(err.Error(), "cannot parse 'text' (a string) as Int64") ||
			!strings.Contains(err.Error(), "at line 2 of "+bad+", key a") {
			t.Errorf("a later file's error: %v", err)
		}
		for _, lf := range []*ursus.LazyFrame{ursus.ScanNDJSONFiles(nil), ursus.ScanNDJSONGlob(filepath.Join(dir, "none*")),
			ursus.ScanNDJSONFrom([]ursus.NDJSONFile{{Name: "x"}})} {
			if _, err := lf.Collect(ctx); err == nil {
				t.Errorf("no files: no error")
			}
		}
	})

	t.Run("a file, in batches, its columns pushed down", func(t *testing.T) {
		var b bytes.Buffer
		for i := range 5000 {
			b.WriteString("{\"i\":")
			b.WriteString(strings.Repeat("1", 1+i%5))
			b.WriteString(",\"s\":\"v\",\"skip\":[1,2,3]}\n")
		}
		path := filepath.Join(t.TempDir(), "rows.ndjson")
		if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		lf := ursus.ScanNDJSON(path).Select(c("i").Sum().Alias("total"), c("s").Len().Alias("n"))
		got, err := lf.Collect(ctx, ursus.WithBatchSize(256))
		if err != nil {
			t.Fatal(err)
		}
		// 1000 rows each of 1, 11, 111, 1111 and 11111.
		if v, err := got.ItemAt(0, "total"); err != nil || v == nil || fmt.Sprint(v) != "12345000" {
			t.Errorf("the total: %v, %v", v, err)
		}
		plan, err := lf.Explain(ctx)
		if err != nil || !strings.Contains(plan, "ndjson") || strings.Contains(plan, "skip") {
			t.Errorf("the plan:\n%s", plan)
		}
		head, err := ursus.ScanNDJSON(path).Head(3).Collect(ctx)
		if err != nil || head.Height() != 3 {
			t.Errorf("head: %v, %v", head, err)
		}
	})
}

// ndjsonGen writes random NDJSON lines for TestReadNDJSONInParts: every key's values
// of one type family, in any order, any key missing, with escapes, text that is not
// UTF-8, space anywhere JSON allows it, CRLF endings and blank lines; and, past the
// lines inference reads, keys it never saw, holding any JSON at all.
type ndjsonGen struct{ rng *rand.Rand }

func (g ndjsonGen) space() string {
	switch g.rng.IntN(8) {
	case 0:
		return " "
	case 1:
		return "\t "
	}
	return ""
}

var ndjsonPieces = []string{"a", "xyz", "é", "😀", "日本", " ", `\"`, `\\`, `\/`, `\b`, `\f`, `\n`, `\r`, `\t`,
	`\u00e9`, `\ud83d\ude00`, `\ud800`, `\udc00x`, "\xff", "\xc3", `\u0041`}

func (g ndjsonGen) str() string {
	var b strings.Builder
	b.WriteByte('"')
	for range g.rng.IntN(6) {
		b.WriteString(ndjsonPieces[g.rng.IntN(len(ndjsonPieces))])
	}
	b.WriteByte('"')
	return b.String()
}

func (g ndjsonGen) integer() string {
	if g.rng.IntN(3) == 0 {
		return strconv.FormatInt(g.rng.Int64N(2e18)-1e18, 10)
	}
	return strconv.Itoa(g.rng.IntN(2000) - 1000)
}

func (g ndjsonGen) float() string {
	whole := strconv.Itoa(g.rng.IntN(20000) - 10000)
	switch g.rng.IntN(5) {
	case 0:
		return whole + "e" + strconv.Itoa(g.rng.IntN(40)-20)
	case 1:
		return whole + ".0" + strconv.Itoa(g.rng.IntN(1000)) + "E+" + strconv.Itoa(g.rng.IntN(5))
	case 2:
		return "-0.0"
	case 3:
		return whole // an integer among the floats
	}
	return whole + "." + strconv.Itoa(g.rng.IntN(1_000_000))
}

func (g ndjsonGen) list(n int, elem func() string) string {
	parts := make([]string, g.rng.IntN(n))
	for i := range parts {
		parts[i] = g.space() + elem() + g.space()
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func (g ndjsonGen) object(keys []string, vals []func() string) string {
	order := g.rng.Perm(len(keys))
	var parts []string
	for _, i := range order {
		if g.rng.IntN(4) > 0 {
			parts = append(parts, g.space()+`"`+keys[i]+`"`+g.space()+":"+g.space()+vals[i]()+g.space())
		}
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func (g ndjsonGen) orNull(f func() string) func() string {
	return func() string {
		if g.rng.IntN(6) == 0 {
			return "null"
		}
		return f()
	}
}

// anyJSON is any JSON value, nested up to depth.
func (g ndjsonGen) anyJSON(depth int) string {
	switch k := g.rng.IntN(7); {
	case k == 0 && depth > 0:
		return g.list(4, func() string { return g.anyJSON(depth - 1) })
	case k == 1 && depth > 0:
		return g.object([]string{"p", "q", `\u0071`}, []func() string{
			func() string { return g.anyJSON(depth - 1) }, func() string { return g.anyJSON(depth - 1) },
			func() string { return g.anyJSON(depth - 1) }})
	case k == 2:
		return g.str()
	case k == 3:
		return g.float()
	case k == 4:
		return []string{"true", "false", "null"}[g.rng.IntN(3)]
	}
	return g.integer()
}

// line is line k of the file; the first holds every key, of its widest type.
func (g ndjsonGen) line(k int) string {
	if k == 0 {
		return `{"i":1,"f":1.5,"s":"a","b":true,"l":[1],"ll":[[1.5]],"lo":[{"x":1,"y":"a"}],` +
			`"o":{"x":1,"y":"q"},"m":"text"}`
	}
	keys := []string{"i", "f", "s", "b", "l", "ll", "lo", "o", "m"}
	xy := func() string {
		return g.object([]string{"x", "y"}, []func() string{g.orNull(g.integer), g.orNull(g.str)})
	}
	vals := []func() string{
		g.orNull(g.integer), g.orNull(g.float), g.orNull(g.str),
		g.orNull(func() string { return []string{"true", "false"}[g.rng.IntN(2)] }),
		g.orNull(func() string { return g.list(5, g.orNull(g.integer)) }),
		g.orNull(func() string { return g.list(4, g.orNull(func() string { return g.list(3, g.float) })) }),
		g.orNull(func() string { return g.list(3, g.orNull(xy)) }),
		g.orNull(xy),
		g.orNull(func() string {
			return []func() string{g.str, g.integer, g.float,
				func() string { return []string{"true", "false"}[g.rng.IntN(2)] }}[g.rng.IntN(4)]()
		}),
	}
	if k > 200 && g.rng.IntN(5) == 0 {
		keys = append(keys, "late", `l\u0061te2`)
		vals = append(vals, func() string { return g.anyJSON(3) }, func() string { return g.anyJSON(2) })
	}
	return g.space() + g.object(keys, vals) + g.space()
}

// ndjsonWant is what a line's value of a column of type t reads as, from encoding/json's
// reading of the line, in ToMaps' Go types.
func ndjsonWant(v any, t string) any {
	if v == nil {
		return nil
	}
	switch t {
	case "Int64":
		n, err := v.(json.Number).Int64()
		if err != nil {
			panic(err)
		}
		return n
	case "Float64":
		f, err := v.(json.Number).Float64()
		if err != nil {
			panic(err)
		}
		return f
	case "Boolean":
		return v.(bool)
	case "String":
		switch x := v.(type) {
		case string:
			return x
		case json.Number:
			// As Rust prints it: an integer in its digits, a float in its
			// shortest and no exponent.
			if !strings.ContainsAny(string(x), ".eE") {
				if n, err := x.Int64(); err == nil {
					return strconv.FormatInt(n, 10)
				}
				return string(x)
			}
			f, err := x.Float64()
			if err != nil {
				panic(err)
			}
			return strconv.FormatFloat(f, 'f', -1, 64)
		case bool:
			return strconv.FormatBool(x)
		}
	case "XY":
		m := v.(map[string]any)
		return map[string]any{"x": ndjsonWant(m["x"], "Int64"), "y": ndjsonWant(m["y"], "String")}
	}
	if elem, ok := strings.CutPrefix(t, "List "); ok {
		var out []any
		for _, x := range v.([]any) {
			out = append(out, ndjsonWant(x, elem))
		}
		return append([]any{}, out...)
	}
	panic("ndjsonWant: " + t)
}

// TestReadNDJSONInParts: a file of many blocks, each cut into parts parsed at once,
// reads as encoding/json reads its lines, on one thread and on eight, in batches of
// any size; and an error, in a part past the first, names its line in the file.
func TestReadNDJSONInParts(t *testing.T) {
	ctx := t.Context()
	g := ndjsonGen{rand.New(rand.NewPCG(334, 1))}
	types := map[string]string{"i": "Int64", "f": "Float64", "s": "String", "b": "Boolean", "l": "List Int64",
		"ll": "List List Float64", "lo": "List XY", "o": "XY", "m": "String"}
	var text bytes.Buffer
	var want []map[string]any
	for k := 0; text.Len() < 3<<20; k++ {
		line := g.line(k)
		var m map[string]any
		dec := json.NewDecoder(strings.NewReader(line))
		dec.UseNumber()
		if err := dec.Decode(&m); err != nil {
			t.Fatalf("line %d, %s: %v", k+1, line, err)
		}
		row := map[string]any{}
		for key, typ := range types {
			row[key] = ndjsonWant(m[key], typ)
		}
		want = append(want, row)
		text.WriteString(line)
		switch {
		case k%7 == 3:
			text.WriteString("\r\n")
		case k%1000 == 999:
			text.WriteString("\n \t\n\n") // blank lines between
		default:
			text.WriteByte('\n')
		}
	}
	path := filepath.Join(t.TempDir(), "parts.ndjson")
	if err := os.WriteFile(path, text.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	var first *ursus.DataFrame
	for _, opts := range [][]ursus.CollectOption{
		{ursus.WithThreads(1)}, {ursus.WithThreads(8)}, {ursus.WithThreads(8), ursus.WithBatchSize(777)},
	} {
		got, err := ursus.ScanNDJSON(path).Collect(ctx, opts...)
		if err != nil {
			t.Fatal(err)
		}
		if s := got.Schema().String(); s != "{i: Int64, f: Float64, s: String, b: Bool, l: List(Int64), "+
			"ll: List(List(Float64)), lo: List(Struct(x: Int64, y: String)), o: Struct(x: Int64, y: String), m: String}" {
			t.Fatalf("schema %s", s)
		}
		rows, err := got.ToMaps()
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != len(want) {
			t.Fatalf("%d rows, want %d", len(rows), len(want))
		}
		for i := range rows {
			if !reflect.DeepEqual(rows[i], want[i]) {
				t.Fatalf("row %d:\n got %v\nwant %v", i, rows[i], want[i])
			}
		}
		if first == nil {
			first = got
		} else {
			ursustest.AssertFrameEqual(t, got, first)
		}
	}
	head, err := ursus.ScanNDJSON(path).Head(3).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	firstThree, err := first.Head(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, head, firstThree)

	// Errors deep in the file, in a part past the first, by their line in it.
	lines := strings.SplitAfter(text.String(), "\n")
	at := len(lines) * 3 / 4
	for _, tc := range []struct {
		name, line, want string
		schema           *ursus.Schema
	}{
		{"malformed", `{"i":`, "malformed JSON", nil},
		{"a type not inferred", `{"i":"x"}`, "cannot parse 'x' (a string) as Int64", nil},
		{"not an object", `[1]`, "holds an array", nil},
		{"a value the schema's type refuses", `{"d":"nope"}`, "cannot parse 'nope' (a string) as Date",
			mustSchema(ursus.Field{Name: "d", Type: ursus.Date})},
	} {
		bad := slices.Clone(lines)
		bad[at] = tc.line + "\n"
		file := filepath.Join(t.TempDir(), "bad.ndjson")
		if err := os.WriteFile(file, []byte(strings.Join(bad, "")), 0o644); err != nil {
			t.Fatal(err)
		}
		var opts []ursus.NDJSONOption
		if tc.schema != nil {
			opts = append(opts, ursus.WithNDJSONSchema(tc.schema))
		}
		_, err := ursus.ScanNDJSON(file, opts...).Collect(ctx, ursus.WithThreads(8))
		if err == nil || !strings.Contains(err.Error(), tc.want) ||
			!strings.Contains(err.Error(), "line "+strconv.Itoa(at+1)) {
			t.Errorf("%s: %v, want %q at line %d", tc.name, err, tc.want, at+1)
		}
	}

	// A line longer than any block.
	long := `{"s":"` + strings.Repeat("w", 9<<20) + `"}`
	got, err := ursus.ScanNDJSONBytes([]byte("{\"s\":\"a\"}\n"+long+"\n{\"s\":\"b\"}\n"), "long.ndjson").Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := got.ItemAt(1, "s"); got.Height() != 3 || len(fmt.Sprint(s)) != 9<<20 {
		t.Errorf("a long line: %d rows", got.Height())
	}
}

package ursus_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/ursustest"
)

// TestReadJSON is step 345: Polars' read_json, a JSON document of an array of
// objects, or of one object. Every answer is Polars 1.44.1's but where noted.
func TestReadJSON(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	read := func(t *testing.T, doc string, opts ...ursus.NDJSONOption) *ursus.DataFrame {
		t.Helper()
		got, err := ursus.ScanJSONBytes([]byte(doc), "t.json", opts...).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	refused := func(t *testing.T, doc string, want []string, opts ...ursus.NDJSONOption) {
		t.Helper()
		_, err := ursus.ScanJSONBytes([]byte(doc), "t.json", opts...).Collect(ctx)
		if err == nil {
			t.Fatalf("%.60q read, want an error saying %q", doc, want)
		}
		for _, w := range append(want, "read_json") {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%.60q: %v, want it to say %q", doc, err, w)
			}
		}
	}
	two := `[{"a":1,"b":"x"},{"a":2,"c":true}]`

	t.Run("an array of objects", func(t *testing.T) {
		// A column a key, in the order first seen, a key missing a null.
		ursustest.AssertFrameEqual(t, read(t, two), dfOf(t, i64s("a", 1, 2), strs("b", "x", nil),
			ursus.ValuesNullable("c", []bool{false, true}, []bool{false, true})))
		// Spread over lines, its objects' newlines among their spaces.
		pretty := "[\n  {\n    \"a\": 1,\n    \"b\": \"x\"\n  }\n  ,\n  {\"a\":\n 2}\n]\n"
		ursustest.AssertFrameEqual(t, read(t, pretty), dfOf(t, i64s("a", 1, 2), strs("b", "x", nil)))
		// A byte-order mark is passed over.
		ursustest.AssertFrameEqual(t, read(t, "\xef\xbb\xbf"+`[{"a":1}]`), dfOf(t, i64s("a", 1)))
		// A key twice, its first value; text beside a number, String.
		ursustest.AssertFrameEqual(t, read(t, `[{"a":1,"a":2}]`), dfOf(t, i64s("a", 1)))
		ursustest.AssertFrameEqual(t, read(t, `[{"a":1},{"a":"x"}]`), dfOf(t, strs("a", "1", "x")))
		ursustest.AssertFrameEqual(t, read(t, `[{},{"a":1}]`), dfOf(t, i64s("a", nil, 1)))
		// A column alone, the others' keys passed over.
		if got := cells(t, ursus.ScanJSONBytes([]byte(two), "t.json").Select(c("c")), "c"); got != "<nil> true Bool" {
			t.Errorf("one column: %s", got)
		}
	})

	t.Run("one object, one row", func(t *testing.T) {
		got := read(t, `{"a":[1,2],"b":{"x":1}}`)
		if s := got.Schema().String(); s != "{a: List(Int64), b: Struct(x: Int64)}" || got.Height() != 1 {
			t.Errorf("one object: %s, %d rows", s, got.Height())
		}
	})

	t.Run("no object", func(t *testing.T) {
		for _, doc := range []string{"[]", " [\n] "} {
			if got := read(t, doc); got.Height() != 0 || got.Width() != 0 {
				t.Errorf("%q: %d rows, %d columns", doc, got.Height(), got.Width())
			}
		}
		// Objects of no key are rows of no column: two, as Polars reads [{}, {}].
		// Polars reads [{}] and {} as no row; ursus as one (feature-gaps §9).
		if got := read(t, `[{},{}]`); got.Height() != 2 || got.Width() != 0 {
			t.Errorf("[{}, {}]: %d rows, %d columns", got.Height(), got.Width())
		}
	})

	t.Run("refused as Polars refuses", func(t *testing.T) {
		for _, tc := range []struct {
			doc  string
			want []string
		}{
			// Polars: "Eof at character 0".
			{"", []string{"the document is empty"}},
			// Polars: "can only deserialize json objects".
			{"3", []string{"can only read JSON objects as rows", "starts with '3'"}},
			{`[[1,2],[3]]`, []string{"element 1 of the array starts with '['"}},
			// Polars: "error deserializing value "Static(U64(2))" as struct".
			{`[{"a":1},2]`, []string{"element 2 of the array starts with '2'"}},
			// Polars: "cannot deserialize struct with nulls into a DataFrame".
			{`[{"a":1},null]`, []string{"element 2 of the array is null"}},
			// Polars: "InternalError(TapeError) at character 9 (',')", and so on.
			{`[{"a":1},]`, []string{"a comma after object 1"}},
			{`[{"a":1}] x`, []string{"text after the document's end"}},
			{`{"a":1}{"a":2}`, []string{"text after the document's end"}},
			{`[{"a":1}`, []string{"ends inside its array"}},
			{`[{"a":1`, []string{"ends inside object 1"}},
			{`[{"a":1},{"a":}]`, []string{"malformed JSON", "object 2"}},
		} {
			refused(t, tc.doc, tc.want)
		}
	})

	// 150 objects of a, then one of b: past the first 100, the schema's.
	late := func(last string) string {
		return "[" + strings.Repeat(`{"a":1},`, 150) + last + "]"
	}
	t.Run("a key the inferred schema has not", func(t *testing.T) {
		// Polars: "extra field in struct data: b, consider increasing
		// infer_schema_length, or manually specifying the full schema to ignore
		// extra fields". Projected away, it is refused still; within a struct too.
		want := []string{`key "b" is not in the schema`, "object 151", "WithInferSchemaLines"}
		refused(t, late(`{"a":1,"b":2}`), want)
		_, err := ursus.ScanJSONBytes([]byte(late(`{"a":1,"b":2}`)), "t.json").Select(c("a")).Collect(ctx)
		if err == nil || !strings.Contains(err.Error(), `key "b"`) {
			t.Errorf("projected: %v", err)
		}
		refused(t, "["+strings.Repeat(`{"s":{"a":1}},`, 150)+`{"s":{"a":1,"b":2}}]`,
			[]string{`key "b" is not in the schema`, "object 151, key s"})
		// Inferred from every object, it is a column; a given schema skips it.
		got := read(t, late(`{"a":1,"b":2}`), ursus.WithInferSchemaLines(0))
		if s := cells(t, got.Lazy().Tail(2), "b"); s != "<nil> 2 Int64" {
			t.Errorf("inferred from every object: %s", s)
		}
		got = read(t, late(`{"a":7,"b":2}`), ursus.WithNDJSONSchema(mustSchema(ursus.Field{Name: "a", Type: ursus.Int64})))
		if s := cells(t, got.Lazy().Tail(1), "a"); s != "7 Int64" || got.Width() != 1 {
			t.Errorf("a given schema: %s, %d columns", s, got.Width())
		}
	})

	t.Run("a value its column cannot take", func(t *testing.T) {
		// Polars: "error deserializing value "String("x")" as numeric". Polars
		// reads a later true as 1 and 1.5 as 1 in an Int64 column; ursus refuses
		// them, as both refuse them in NDJSON (feature-gaps §9).
		refused(t, late(`{"a":"x"}`), []string{"cannot parse 'x' (a string) as Int64", "object 151, key a"})
		refused(t, late(`{"a":true}`), []string{"as Int64", "object 151"})
	})

	t.Run("schema overrides", func(t *testing.T) {
		got := read(t, two, ursus.WithNDJSONSchemaOverrides(map[string]dtype.DataType{"a": ursus.Float32}))
		if s := got.Schema().String(); s != "{a: Float32, b: String, c: Bool}" {
			t.Errorf("a Float32: %s", s)
		}
		got = read(t, two, ursus.WithNDJSONSchemaOverrides(map[string]dtype.DataType{"a": ursus.String}))
		ursustest.AssertFrameEqual(t, collect(t, got.Lazy().Select(c("a"))), dfOf(t, strs("a", "1", "2")))
		got = read(t, `[{"d":"2024-01-02"}]`, ursus.WithNDJSONSchemaOverrides(map[string]dtype.DataType{"d": ursus.Date}))
		if s := cells(t, got.Lazy(), "d"); s != "2024-01-02 00:00:00 +0000 UTC Date" {
			t.Errorf("a Date: %s", s)
		}
		got = read(t, `[{"l":[1,2],"s":{"x":1}}]`, ursus.WithNDJSONSchemaOverrides(map[string]dtype.DataType{
			"l": ursus.List(ursus.Float64), "s": ursus.StructOf(ursus.Field{Name: "x", Type: ursus.Float64, Nullable: true})}))
		if s := got.Schema().String(); s != "{l: List(Float64), s: Struct(x: Float64)}" {
			t.Errorf("nested: %s", s)
		}
		// Over a given schema, as Polars' read_json: its read_ndjson keeps the
		// schema's type.
		got = read(t, two, ursus.WithNDJSONSchema(mustSchema(ursus.Field{Name: "a", Type: ursus.Int64}, ursus.Field{Name: "b", Type: ursus.String})),
			ursus.WithNDJSONSchemaOverrides(map[string]dtype.DataType{"a": ursus.Float64}))
		if s := got.Schema().String(); s != "{a: Float64, b: String}" {
			t.Errorf("over a given schema: %s", s)
		}
		// Polars: SchemaFieldNotFoundError: z.
		refused(t, two, []string{`unknown column "z"`}, ursus.WithNDJSONSchemaOverrides(map[string]dtype.DataType{"z": ursus.Int64}))
		// And NDJSON's.
		nd, err := ursus.ScanNDJSONBytes([]byte("{\"a\":1}\n{\"a\":2,\"b\":\"x\"}\n"), "t.ndjson",
			ursus.WithNDJSONSchemaOverrides(map[string]dtype.DataType{"a": ursus.Int32})).Collect(ctx)
		if err != nil || nd.Schema().String() != "{a: Int32, b: String}" {
			t.Errorf("NDJSON's: %v, %v", nd, err)
		}
		_, err = ursus.ScanNDJSONBytes([]byte("{\"a\":1}\n"), "t.ndjson",
			ursus.WithNDJSONSchemaOverrides(map[string]dtype.DataType{"z": ursus.Int32})).Collect(ctx)
		if err == nil || !strings.Contains(err.Error(), `unknown column "z"`) || !strings.Contains(err.Error(), "read_ndjson") {
			t.Errorf("NDJSON's of no column: %v", err)
		}
	})

	t.Run("a file, and Explain", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "rows.json")
		if err := os.WriteFile(path, []byte(two), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := ursus.ScanJSON(path).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, read(t, two))
		plan, err := ursus.ScanJSON(path).Explain(ctx)
		if err != nil || !strings.Contains(plan, "JSON") || strings.Contains(plan, "NDJSON") {
			t.Errorf("Explain: %s, %v", plan, err)
		}
		_, err = ursus.ScanJSON(filepath.Join(t.TempDir(), "none.json")).Collect(ctx)
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("a missing file: %v", err)
		}
	})

	t.Run("WriteJSON's document read back", func(t *testing.T) {
		src := dfOf(t, i64s("a", 1, nil, 3), strs("b", "x", "y\n\"z\"", nil), f64s("f", 1.5, nil, -0.25))
		var buf bytes.Buffer
		if err := src.Lazy().WriteJSON(ctx, &buf); err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, read(t, buf.String()), src)
	})
}

// TestReadJSONInParts: a document of random objects, each spread over lines, read
// as the NDJSON of the same objects is, in parts a thread, and its first rows read
// alone; an object deep in it, in a part past the first, refused at its number: one
// with a key the first objects have not, which a given schema's reading skips, and one
// that is not JSON.
func TestReadJSONInParts(t *testing.T) {
	ctx := t.Context()
	g := ndjsonGen{rand.New(rand.NewPCG(345, 1))}
	var lines []string
	size, wantSize := 0, 3<<20
	if raceDetector {
		wantSize = 1 << 20 // a pretty document of it still spans several blocks
	}
	for k := 0; size < wantSize; k++ {
		// Past line 200 the generator adds keys the first lines have not, which a
		// JSON document's reading refuses: its lines are of the first 200's kinds.
		line := g.line(min(k, 1+k%200))
		lines = append(lines, line)
		size += len(line)
	}
	doc := func(lines []string) []byte {
		var b bytes.Buffer
		b.WriteString("[\n")
		for k, line := range lines {
			if k > 0 {
				b.WriteString(",\n")
			}
			if json.Indent(&b, []byte(line), "  ", "  ") != nil {
				b.WriteString(line)
			}
		}
		b.WriteString("\n]\n")
		return b.Bytes()
	}
	want, err := ursus.ScanNDJSONBytes([]byte(strings.Join(lines, "\n")), "t.ndjson").Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, opts := range [][]ursus.CollectOption{
		{ursus.WithThreads(1)}, {ursus.WithThreads(8)}, {ursus.WithThreads(8), ursus.WithBatchSize(777)},
	} {
		got, err := ursus.ScanJSONBytes(doc(lines), "t.json").Collect(ctx, opts...)
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, want)
	}
	head, err := ursus.ScanJSONBytes(doc(lines), "t.json").Head(3).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	firstThree, err := want.Head(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, head, firstThree)

	at := len(lines) * 3 / 4
	for _, tc := range []struct{ name, object, want string }{
		{"a key the schema has not", `{"i":1,"zz":2}`, `key "zz" is not in the schema`},
		{"malformed", `{"i":}`, "malformed JSON"},
	} {
		bad := slices.Clone(lines)
		bad[at] = tc.object
		_, err := ursus.ScanJSONBytes(doc(bad), "t.json").Collect(ctx, ursus.WithThreads(8))
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "object "+strconv.Itoa(at+1)) {
			t.Errorf("%s: %v, want %q at object %d", tc.name, err, tc.want, at+1)
		}
	}
	bad := slices.Clone(lines)
	bad[at] = `{"i":1,"zz":2}`
	got, err := ursus.ScanJSONBytes(doc(bad), "t.json", ursus.WithNDJSONSchema(want.Schema())).Collect(ctx, ursus.WithThreads(8))
	if err != nil || got.Height() != want.Height() {
		t.Errorf("a key a given schema has not: %v", err)
	}
}

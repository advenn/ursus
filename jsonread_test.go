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
	"github.com/advenn/ursus/ursustest"
)

// TestReadJSON is step 345: Polars' read_json, a JSON document of an array of objects,
// or of one object. Each answer is Polars 2.1.0's since step 502, recorded in
// testdata/oracle/jsonread.json and spelled in oracle_jsonread_test.go; here stay a
// document of no object, a frame of no columns, the rules ursus keeps where Polars 2
// reads otherwise (feature-gaps.md §9), a refusal a projection does not hide, a file
// and its plan.
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
	// 150 objects of a, then one of b: past the first 100, the schema's.
	late := func(last string) string {
		return "[" + strings.Repeat(`{"a":1},`, 150) + last + "]"
	}

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

	t.Run("a key the inferred schema has not, projected away", func(t *testing.T) {
		// Refused still (the oracle holds the refusal of the whole document).
		_, err := ursus.ScanJSONBytes([]byte(late(`{"a":1,"b":2}`)), "t.json").Select(c("a")).Collect(ctx)
		if err == nil || !strings.Contains(err.Error(), `key "b"`) {
			t.Errorf("projected: %v", err)
		}
	})

	t.Run("a value its column cannot take", func(t *testing.T) {
		// Polars reads a later true as 1 and 1.5 as 1 in an Int64 column; ursus refuses
		// them, as both refuse them in NDJSON (feature-gaps §9).
		refused(t, late(`{"a":true}`), []string{"as Int64", "object 151"})
		refused(t, late(`{"a":1.5}`), []string{"as Int64", "object 151"})
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

package ursus_test

// Step 412: a JSON value nested past 1,000 levels is an error, where it overflowed
// the goroutine's stack, a fatal error no recover catches, and ended the process.

import (
	"errors"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
)

// jsonNestingLimit is the deepest a JSON value may nest, the line's object counted.
const jsonNestingLimit = 1_000

// nested is a line whose key "a" holds arrays nested depth deep, and "b" a 1.
func nested(depth int) string {
	return `{"a":` + strings.Repeat("[", depth) + strings.Repeat("]", depth) + `,"b":1}`
}

// nestedObjects is nested's line with objects, each the next's key "k".
func nestedObjects(depth int) string {
	return `{"a":` + strings.Repeat(`{"k":`, depth) + "1" + strings.Repeat("}", depth) + `,"b":1}`
}

// TestJSONNestedPastTheLimitIsAnError: two million levels, a 4 MB line, overflowed a
// 64 MB stack and ended the test binary; each way a line is read now gives an error,
// of kind ErrValue, naming the limit: inferred, a key no column reads, a String
// column, a List column given, ignoring errors, and a JSON document. One level short
// of the limit reads.
func TestJSONNestedPastTheLimitIsAnError(t *testing.T) {
	defer debug.SetMaxStack(debug.SetMaxStack(64 << 20))
	schema := func(fields ...ursus.Field) ursus.NDJSONOption {
		return ursus.WithNDJSONSchema(ursus.MustSchema(fields...))
	}
	cases := []struct {
		name string
		opts []ursus.NDJSONOption
	}{
		{"inferred", nil},
		{"a key no column reads", []ursus.NDJSONOption{schema(ursus.Field{Name: "b", Type: ursus.Int64})}},
		{"a String column", []ursus.NDJSONOption{schema(ursus.Field{Name: "a", Type: ursus.String})}},
		{"a List column given", []ursus.NDJSONOption{schema(ursus.Field{Name: "a", Type: dtype.List(ursus.Int64)})}},
		{"ignoring errors", []ursus.NDJSONOption{schema(ursus.Field{Name: "a", Type: ursus.Int64}),
			ursus.WithNDJSONIgnoreErrors(true)}},
	}
	for _, shape := range []struct {
		name string
		line func(int) string
	}{{"arrays", nested}, {"objects", nestedObjects}} {
		deep := shape.line(2_000_000)
		for _, c := range cases {
			_, err := ursus.ScanNDJSONBytes([]byte(`{"a":null,"b":0}`+"\n"+deep+"\n"), "deep.ndjson", c.opts...).Collect(t.Context())
			checkTooDeep(t, shape.name+", "+c.name, err)
		}
		path := filepath.Join(t.TempDir(), "deep.json")
		if err := os.WriteFile(path, []byte("["+deep+"]"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := ursus.ScanJSON(path).Collect(t.Context())
		checkTooDeep(t, shape.name+", a JSON document", err)
	}

	// The line's object is one level, so a limit's worth of arrays is one too many.
	_, err := ursus.ScanNDJSONBytes([]byte(nested(jsonNestingLimit)+"\n"), "edge.ndjson").Collect(t.Context())
	checkTooDeep(t, "one level past the limit", err)
	df, err := ursus.ScanNDJSONBytes([]byte(nested(jsonNestingLimit-1)+"\n"), "edge.ndjson").Collect(t.Context())
	if err != nil || df.Height() != 1 {
		t.Fatalf("a line nested to the limit: %v", err)
	}
}

func checkTooDeep(t *testing.T, name string, err error) {
	t.Helper()
	if err == nil || !errors.Is(err, ursus.ErrValue) || !strings.Contains(err.Error(), "nested deeper than 1000 levels") {
		t.Errorf("%s: got %v, want an ErrValue naming the 1000-level limit", name, err)
	}
}

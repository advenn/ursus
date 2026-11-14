package kernel

import (
	"encoding/json"
	"math"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/fuzzcheck"
	_ "github.com/advenn/ursus/internal/fuzzcheck/invariants"
)

// FuzzJSONDecode: json_decode's and json_path_match's parser against encoding/json
// (step 511, the plan's 447): parseJSON accepts what json.Valid accepts; what it
// reads is what json.Unmarshal reads, an object's last value of a repeated key for
// json_path_match; and what writeJSON writes of it is JSON that reads back as the
// same value, a number as the same float. json_decode into a sweep of types and
// json_path_match of a few paths answer or refuse within fuzzcheck's limits.
//
// readJSON recurses once a level, through encoding/json's Token, which refuses a
// value nested past encoding/json's 10,000 levels (the plan's 419 supposed it did
// not: Go 1.27's does). The stack is 8 MiB while the target runs, so a Token that
// stopped counting would end the binary on TestJSONDecodeDeep's 100,000 levels.
func FuzzJSONDecode(f *testing.F) {
	f.Fuzz(jsonDecodeOne)
}

// TestJSONDecodeDeep is FuzzJSONDecode's check of values nested at encoding/json's
// limit and past it, and 100,000 levels deep, unclosed: seeds of 20 to 100 KB, which
// the fuzzer would spend its time minimizing, so an ordinary test.
func TestJSONDecodeDeep(t *testing.T) {
	for name, s := range map[string]string{
		"at the limit":         strings.Repeat("[", jsonMaxDepth) + strings.Repeat("]", jsonMaxDepth),
		"past the limit":       strings.Repeat("[", jsonMaxDepth+1) + strings.Repeat("]", jsonMaxDepth+1),
		"objects at the limit": strings.Repeat(`{"a":`, jsonMaxDepth) + "1" + strings.Repeat("}", jsonMaxDepth),
		"100,000 levels, open": strings.Repeat("[", 100_000),
	} {
		t.Run(name, func(t *testing.T) { jsonDecodeOne(t, s) })
	}
}

// jsonDecodeOne is FuzzJSONDecode's check of one input.
func jsonDecodeOne(t *testing.T, s string) {
	defer debug.SetMaxStack(debug.SetMaxStack(8 << 20))
	lim := fuzzcheck.Limits{Wall: 10 * time.Second, Alloc: 64<<20 + 1024*int64(len(s))}
	fuzzcheck.Run(t, lim, func() error {
		checkParseJSON(t, s)
		decodeSweep(t, s)
		return nil
	})
}

// jsonMaxDepth is encoding/json's limit on nesting, its scanner's maxNestingDepth.
const jsonMaxDepth = 10_000

// checkParseJSON holds parseJSON to encoding/json.
func checkParseJSON(t *testing.T, s string) {
	v, err := parseJSON(s, false)
	valid := json.Valid([]byte(s))
	switch {
	case err == nil && !valid:
		t.Fatalf("parseJSON reads %s, which encoding/json refuses", short(s))
	case err != nil && valid:
		t.Fatalf("parseJSON refuses %s, which encoding/json reads: %v", short(s), err)
	case err != nil:
		return
	}
	var b strings.Builder
	writeJSON(&b, v)
	if !json.Valid([]byte(b.String())) {
		t.Fatalf("writeJSON of %s wrote %s, which is not JSON", short(s), short(b.String()))
	}
	if jsonDepth(s) > oracleDepth {
		return
	}
	want := unmarshal(t, s)
	if why := sameJSON(v, want); why != "" {
		t.Fatalf("parseJSON(%s) differs from json.Unmarshal's: %s", short(s), why)
	}
	first, err := parseJSON(s, true)
	if err != nil {
		t.Fatalf("parseJSON(%s) keeping a repeated key's first value refuses what keeping its last reads: %v", short(s), err)
	}
	if why := sameShape(first, v); why != "" {
		t.Fatalf("parseJSON(%s) keeping the first value of a repeated key is shaped otherwise: %s", short(s), why)
	}
	if why := sameValue(unmarshal(t, b.String()), want); why != "" {
		t.Fatalf("writeJSON of %s wrote %s, which reads back otherwise: %s", short(s), short(b.String()), why)
	}
}

// oracleDepth is the deepest value the oracle's own comparisons walk, which recurse
// in frames larger than readJSON's: deeper ones are held to json.Valid alone.
const oracleDepth = 1_000

// jsonDepth is how deep s nests its arrays and objects, read outside its strings.
func jsonDepth(s string) int {
	depth, deepest, str, esc := 0, 0, false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case str && c == '\\':
			esc = true
		case c == '"':
			str = !str
		case str:
		case c == '[' || c == '{':
			depth++
			deepest = max(deepest, depth)
		case c == ']' || c == '}':
			depth--
		}
	}
	return deepest
}

// short is s quoted for a failure's message, cut past 200 bytes.
func short(s string) string {
	if len(s) > 200 {
		return strconv.Quote(s[:200]) + "… (" + strconv.Itoa(len(s)) + " bytes)"
	}
	return strconv.Quote(s)
}

// decodeSweep runs json_decode into each of a sweep of types and json_path_match of a
// few paths over s: an error is a refusal, judged by fuzzcheck, and what $ matches
// is s's own value.
func decodeSweep(t *testing.T, s string) {
	col := data.NewString("s", []string{s}, bitmap.AllSet(1))
	for _, dt := range []dtype.DataType{
		dtype.String, dtype.Int64, dtype.Uint8, dtype.Float32, dtype.Bool, dtype.Null,
		dtype.List(dtype.Int64), dtype.List(dtype.String),
		dtype.Struct(dtype.Field{Name: "a", Type: dtype.Int64, Nullable: true},
			dtype.Field{Name: "b", Type: dtype.List(dtype.String), Nullable: true}),
	} {
		if c, err := jsonDecode("s", dt, col); err == nil && c.Len() != 1 {
			t.Fatalf("json_decode of %s as %s: %d rows", short(s), dt, c.Len())
		}
	}
	for _, path := range []string{"$..a", "$[0]", "$.a[-1]"} {
		if _, err := jsonPathMatch("s", col, path); err != nil {
			t.Fatalf("json_path_match(%q) of %s: %v", path, short(s), err)
		}
	}
	m, err := jsonPathMatch("s", col, "$")
	if err != nil {
		t.Fatalf("json_path_match($) of %s: %v", short(s), err)
	}
	v, perr := parseJSON(s, false)
	switch {
	case perr != nil || v.kind == 'n':
		if m.IsValid(0) {
			t.Fatalf("json_path_match($) of %s, not JSON or null, is %s", short(s), short(m.Strings().Get(0)))
		}
	case !m.IsValid(0):
		t.Fatalf("json_path_match($) of %s is null", short(s))
	case v.kind == 's':
		if got := m.Strings().Get(0); got != unmarshal(t, s) {
			t.Fatalf("json_path_match($) of %s is %s, not the string", short(s), short(got))
		}
	case jsonDepth(s) > oracleDepth:
		if got := m.Strings().Get(0); !json.Valid([]byte(got)) {
			t.Fatalf("json_path_match($) of %s is %s, which is not JSON", short(s), short(got))
		}
	default:
		got := m.Strings().Get(0)
		if !json.Valid([]byte(got)) {
			t.Fatalf("json_path_match($) of %s is %s, which is not JSON", short(s), short(got))
		}
		if why := sameValue(unmarshal(t, got), unmarshal(t, s)); why != "" {
			t.Fatalf("json_path_match($) of %s is %s, another value: %s", short(s), short(got), why)
		}
	}
}

// unmarshal is s as encoding/json reads it, numbers as json.Number.
func unmarshal(t *testing.T, s string) any {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("encoding/json refuses %s, which it called valid: %v", short(s), err)
	}
	return v
}

// sameJSON says how v differs from o, encoding/json's reading of the same text, ""
// for not at all: a number's literal kept, an object's keys each once, its last
// value.
func sameJSON(v *jsonValue, o any) string {
	switch o := o.(type) {
	case nil:
		if v.kind != 'n' {
			return "null read as " + v.describe()
		}
	case bool:
		if v.kind != 'b' || v.b != o {
			return strconv.FormatBool(o) + " read as " + v.describe()
		}
	case json.Number:
		if v.kind != '0' || v.text != string(o) {
			return "the number " + string(o) + " read as " + v.describe() + " " + strconv.Quote(v.text)
		}
	case string:
		if v.kind != 's' || v.text != o {
			return strconv.Quote(o) + " read as " + v.describe() + " " + strconv.Quote(v.text)
		}
	case []any:
		if v.kind != '[' || len(v.items) != len(o) {
			return "an array of " + strconv.Itoa(len(o)) + " read as " + v.describe()
		}
		for i := range o {
			if why := sameJSON(v.items[i], o[i]); why != "" {
				return "[" + strconv.Itoa(i) + "]: " + why
			}
		}
	case map[string]any:
		if v.kind != '{' || len(v.keys) != len(o) {
			return "an object of " + strconv.Itoa(len(o)) + " keys read as " + v.describe() + " of " + strconv.Itoa(len(v.keys))
		}
		for k, key := range v.keys {
			ov, ok := o[key]
			if !ok {
				return "key " + strconv.Quote(key) + " is not encoding/json's"
			}
			if why := sameJSON(v.items[k], ov); why != "" {
				return "." + key + ": " + why
			}
		}
	}
	return ""
}

// sameShape says how a, a parse keeping a repeated key's first value, differs from
// b, one keeping its last, but in a value of a key repeated: the same kinds, the same
// keys in the same order.
func sameShape(a, b *jsonValue) string {
	if a.kind != b.kind || len(a.items) != len(b.items) || !slices.Equal(a.keys, b.keys) {
		return a.describe() + " against " + b.describe()
	}
	if a.kind == '[' {
		for i := range a.items {
			if why := sameShape(a.items[i], b.items[i]); why != "" {
				return "[" + strconv.Itoa(i) + "]: " + why
			}
		}
	}
	return ""
}

// sameValue says how a differs from b, two readings by encoding/json, a number by
// its float value, as writeJSON reprints one.
func sameValue(a, b any) string {
	switch b := b.(type) {
	case json.Number:
		an, ok := a.(json.Number)
		if !ok {
			return "the number " + string(b) + " against another value"
		}
		x, _ := strconv.ParseFloat(string(an), 64)
		y, _ := strconv.ParseFloat(string(b), 64)
		if x != y && !(math.IsNaN(x) && math.IsNaN(y)) {
			return "the number " + string(an) + " against " + string(b)
		}
	case []any:
		aa, ok := a.([]any)
		if !ok || len(aa) != len(b) {
			return "an array of " + strconv.Itoa(len(b)) + " against another value"
		}
		for i := range b {
			if why := sameValue(aa[i], b[i]); why != "" {
				return "[" + strconv.Itoa(i) + "]: " + why
			}
		}
	case map[string]any:
		am, ok := a.(map[string]any)
		if !ok || len(am) != len(b) {
			return "an object of " + strconv.Itoa(len(b)) + " keys against another value"
		}
		for k, bv := range b {
			av, ok := am[k]
			if !ok {
				return "key " + strconv.Quote(k) + " missing"
			}
			if why := sameValue(av, bv); why != "" {
				return "." + k + ": " + why
			}
		}
	default:
		if a != b {
			return "a value against another"
		}
	}
	return ""
}

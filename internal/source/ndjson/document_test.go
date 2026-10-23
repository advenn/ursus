package ndjson

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"strings"
	"testing"
)

// chunked hands its bytes back at most n a read.
type chunked struct {
	b []byte
	n int
}

func (c *chunked) Read(p []byte) (int, error) {
	if len(c.b) == 0 {
		return 0, io.EOF
	}
	k := copy(p[:min(len(p), c.n)], c.b)
	c.b = c.b[k:]
	return k, nil
}

// lines is doc read as NDJSON, its reads n bytes at most, into buffers of size bytes,
// each line's object without the spaces around it. A line between two objects, or
// an object's line, that holds no object is an error.
func lines(doc string, n, size int) (string, error) {
	o := &objectLines{r: &chunked{b: []byte(doc), n: n}}
	var out bytes.Buffer
	buf := make([]byte, size)
	for {
		k, err := o.Read(buf)
		out.Write(buf[:k])
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return out.String(), err
		}
	}
	if n := out.Len() - len(doc); n != 0 && (n != 1 || doc[0] != '{') {
		return out.String(), fmt.Errorf("%d bytes read as %d", len(doc), out.Len())
	}
	ls := strings.Split(out.String(), "\n")
	var objs strings.Builder
	for i, l := range ls {
		l = strings.Trim(l, " ")
		switch {
		case i == len(ls)-1 && l != "":
			return out.String(), fmt.Errorf("the last line holds %q", l)
		case i < len(ls)-1 && (l == "" || l[0] != '{' || l[len(l)-1] != '}'):
			return out.String(), fmt.Errorf("line %d holds %q", i+1, l)
		case l != "":
			objs.WriteString(l)
			objs.WriteByte('\n')
		}
	}
	return objs.String(), nil
}

// TestObjectLines: step 345. A JSON document of rows is its objects a line each,
// their newlines spaces, whatever a read hands back: a string's brackets, quotes and
// backslashes are its own, a byte-order mark is passed over, and an empty array is
// no line.
func TestObjectLines(t *testing.T) {
	for _, tc := range []struct{ doc, want string }{
		{`[{"a":1},{"b":2}]`, "{\"a\":1}\n{\"b\":2}\n"},
		{" [ {\"a\" : [1, {\"x\": \"}]\"}]} ,\n {\"b\":\"a\\\"b\"} ] \n", "{\"a\" : [1, {\"x\": \"}]\"}]}\n{\"b\":\"a\\\"b\"}\n"},
		{"{\"a\":\n1,\r\n\"b\":[\n2]}", "{\"a\": 1,\r \"b\":[ 2]}\n"},
		{"{\"a\":1}  \n", "{\"a\":1}\n"},
		{`[{"a":"x\\"},{"b":"\\\""},{"c":"\\\\\"}"}]`, "{\"a\":\"x\\\\\"}\n{\"b\":\"\\\\\\\"\"}\n{\"c\":\"\\\\\\\\\\\"}\"}\n"},
		{"\xef\xbb\xbf[{\"a\":1}]", "{\"a\":1}\n"},
		{"[]", ""},
		{" [\n] ", ""},
		{"[{}, {}]", "{}\n{}\n"},
		{"{}", "{}\n"},
	} {
		for _, n := range []int{1, 2, 3, 5, 7, 1 << 20} {
			for _, size := range []int{1, 2, 3, 64} {
				got, err := lines(tc.doc, n, size)
				if err != nil || got != tc.want {
					t.Errorf("%q, reads of %d into %d: %q, %v, want %q", tc.doc, n, size, got, err, tc.want)
				}
			}
		}
	}
}

// TestObjectLinesRefuses: what Polars' read_json refuses is refused, saying what and
// where.
func TestObjectLinesRefuses(t *testing.T) {
	for _, tc := range []struct{ doc, want string }{
		{"", "the document is empty"},
		{" \n ", "the document is empty"},
		{"3", "can only read JSON objects as rows: the document is an array of them or one, and starts with '3'"},
		{`"x"`, "starts with '\"'"},
		{"null", "starts with 'n'"},
		{`[{"a":1}, null]`, "element 2 of the array is null"},
		{`[[1]]`, "element 1 of the array starts with '['"},
		{`[{"a":1}, 2]`, "element 2 of the array starts with '2'\n  at byte 11"},
		{`[,{"a":1}]`, "element 1 of the array starts with ','"},
		{`[{"a":1},]`, "a comma after object 1, before the array's end"},
		{`[{"a":1},,{"a":2}]`, "element 2 of the array starts with ','"},
		{`[{"a":1} {"a":2}]`, "after object 1 comes ',' or the array's end, not '{'"},
		{`[{"a":1}`, "the document ends inside its array, after object 1"},
		{`[{"a":1},`, "the document ends inside its array, after object 1"},
		{`[{"a":"}`, "the document ends inside object 1"},
		{`{"a":1}{"a":2}`, "text after the document's end, starting with '{'"},
		{`[{"a":1}] x`, "text after the document's end, starting with 'x'\n  at byte 11"},
		{`[{"a":1}][{"a":2}]`, "text after the document's end, starting with '['"},
		{"\x00", "starts with byte 0x00"},
	} {
		for _, n := range []int{1, 3, 1 << 20} {
			_, err := lines(tc.doc, n, 64)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "read_json") {
				t.Errorf("%q, reads of %d: %v, want an error saying %q", tc.doc, n, err, tc.want)
			}
		}
	}
}

// TestObjectLinesRandom: random objects, their strings holding brackets, quotes and
// runs of backslashes, their space newlines, come back a line each, as they were but
// for the newlines, under random reads.
func TestObjectLinesRandom(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 45))
	space := func(b *strings.Builder) {
		for range rng.IntN(3) {
			b.WriteByte(" \n\t\r"[rng.IntN(4)])
		}
	}
	var value func(b *strings.Builder, depth int)
	value = func(b *strings.Builder, depth int) {
		space(b)
		switch k := rng.IntN(6); {
		case k == 0 && depth < 4:
			b.WriteByte('[')
			for i := range rng.IntN(4) {
				if i > 0 {
					b.WriteByte(',')
				}
				value(b, depth+1)
			}
			b.WriteByte(']')
		case k == 1 && depth < 4:
			object(rng, b, depth+1, value, space)
		case k <= 3:
			b.WriteByte('"')
			for range rng.IntN(8) {
				switch rng.IntN(6) {
				case 0:
					b.WriteString(strings.Repeat(`\\`, 1+rng.IntN(3)))
				case 1:
					b.WriteString(`\"`)
				default:
					b.WriteByte("{}[]x,:é"[rng.IntN(8)])
				}
			}
			b.WriteByte('"')
		default:
			b.WriteString([]string{"1", "-2.5e3", "true", "null"}[rng.IntN(4)])
		}
		space(b)
	}
	for range 200 {
		var doc, want strings.Builder
		doc.WriteByte('[')
		count := rng.IntN(6)
		for i := range count {
			if i > 0 {
				doc.WriteByte(',')
			}
			space(&doc)
			var o strings.Builder
			object(rng, &o, 0, value, space)
			doc.WriteString(o.String())
			space(&doc)
			want.WriteString(strings.ReplaceAll(o.String(), "\n", " "))
			want.WriteByte('\n')
		}
		doc.WriteByte(']')
		for _, n := range []int{1, 1 + rng.IntN(16), 1 << 20} {
			got, err := lines(doc.String(), n, 1+rng.IntN(64))
			if err != nil || got != want.String() {
				t.Fatalf("%q, reads of %d:\n%q, %v, want\n%q", doc.String(), n, got, err, want.String())
			}
		}
	}
}

func object(rng *rand.Rand, b *strings.Builder, depth int, value func(*strings.Builder, int), space func(*strings.Builder)) {
	b.WriteByte('{')
	for i := range rng.IntN(4) {
		if i > 0 {
			b.WriteByte(',')
		}
		space(b)
		b.WriteString(`"k\"]`)
		b.WriteByte(byte('a' + i))
		b.WriteString(`"`)
		space(b)
		b.WriteByte(':')
		value(b, depth)
	}
	b.WriteByte('}')
}

// TestObjectLinesVector: the vector scan reads as the byte scan does, byte for byte,
// documents of long strings, escapes, brackets in strings, nesting and newlines, read
// whole and in reads of random sizes; a newline in a string too, which JSON refuses.
func TestObjectLinesVector(t *testing.T) {
	if !vectorScan {
		t.Skip("no vector scan in this build or at this width")
	}
	defer func() { vectorScan = true }()
	rng := rand.New(rand.NewPCG(345, 2))
	str := func(b *strings.Builder) {
		b.WriteByte('"')
		escapes := rng.IntN(3) == 0
		for range rng.IntN(150) {
			switch k := rng.IntN(20); {
			case k == 0 && escapes:
				b.WriteString([]string{`\\`, `\"`, `\n`, `\u00e9`, `\\\"`}[rng.IntN(5)])
			case k <= 2:
				b.WriteByte("{}[],:"[rng.IntN(6)])
			case k == 3 && rng.IntN(20) == 0:
				// A newline in a string, which JSON refuses and a line's parse
				// will: both scans leave it be.
				b.WriteByte('\n')
			default:
				b.WriteByte(byte('a' + rng.IntN(26)))
			}
		}
		b.WriteByte('"')
	}
	space := func(b *strings.Builder) {
		for range rng.IntN(4) {
			b.WriteByte(" \n\t\r"[rng.IntN(4)])
		}
	}
	var value func(b *strings.Builder, depth int)
	value = func(b *strings.Builder, depth int) {
		space(b)
		switch k := rng.IntN(8); {
		case k == 0 && depth < 6:
			b.WriteByte('[')
			for i := range rng.IntN(5) {
				if i > 0 {
					b.WriteByte(',')
				}
				value(b, depth+1)
			}
			b.WriteByte(']')
		case k == 1 && depth < 6:
			b.WriteByte('{')
			for i := range rng.IntN(5) {
				if i > 0 {
					b.WriteByte(',')
				}
				space(b)
				str(b)
				space(b)
				b.WriteByte(':')
				value(b, depth+1)
			}
			b.WriteByte('}')
		case k <= 4:
			str(b)
		default:
			b.WriteString([]string{"1", "-2.5e3", "true", "null", "0.125"}[rng.IntN(5)])
		}
		space(b)
	}
	for range 60 {
		var doc strings.Builder
		doc.WriteByte('[')
		for i := range 1 + rng.IntN(200) {
			if i > 0 {
				doc.WriteByte(',')
			}
			space(&doc)
			doc.WriteByte('{')
			for k := range rng.IntN(6) {
				if k > 0 {
					doc.WriteByte(',')
				}
				space(&doc)
				str(&doc)
				space(&doc)
				doc.WriteByte(':')
				value(&doc, 0)
			}
			doc.WriteByte('}')
			space(&doc)
		}
		doc.WriteByte(']')
		for _, n := range []int{1 << 20, 64 + rng.IntN(512), 1 + rng.IntN(100)} {
			raw := func(vector bool) string {
				vectorScan = vector
				o := &objectLines{r: &chunked{b: []byte(doc.String()), n: n}}
				var out bytes.Buffer
				buf := make([]byte, 1+rng.IntN(4096))
				for {
					k, err := o.Read(buf)
					out.Write(buf[:k])
					if err != nil {
						if !errors.Is(err, io.EOF) {
							t.Fatalf("%v", err)
						}
						return out.String()
					}
				}
			}
			if v, s := raw(true), raw(false); v != s {
				i := 0
				for i < len(v) && i < len(s) && v[i] == s[i] {
					i++
				}
				t.Fatalf("reads of %d: the vector scan differs from the byte scan at byte %d:\n%q\n%q",
					n, i, v[max(0, i-40):min(len(v), i+40)], s[max(0, i-40):min(len(s), i+40)])
			}
		}
	}
}

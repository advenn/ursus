package ndjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/advenn/ursus/internal/fuzzcheck"
)

// FuzzObjectLines: a JSON document read as NDJSON (step 511, the plan's 447) is the
// same bytes and the same error whatever its reads' sizes and its buffer's, and read
// sixty-four bytes at a time as a byte at a time; and held to encoding/json: a
// document json.Valid calls an array of objects, or one object, is its objects a line
// each, as json.Unmarshal finds them, their newlines spaces, and any other document
// json.Valid accepts is refused. A document json.Valid refuses may be read: the
// objects' insides are parsed later, as NDJSON lines.
func FuzzObjectLines(f *testing.F) {
	f.Fuzz(func(t *testing.T, doc []byte, n, size uint16) {
		lim := fuzzcheck.Limits{Wall: 10 * time.Second, Alloc: 64<<20 + 64*int64(len(doc))}
		fuzzcheck.Run(t, lim, func() error {
			whole, werr := readLines(doc, len(doc)+1, len(doc)+1)
			chunked, cerr := readLines(doc, 1+int(n)%257, 1+int(size)%4099)
			if !bytes.Equal(whole, chunked) || errText(werr) != errText(cerr) {
				t.Fatalf("%s read whole is\n%q, %v\nand in reads of %d into %d bytes\n%q, %v",
					shortDoc(doc), whole, werr, 1+int(n)%257, 1+int(size)%4099, chunked, cerr)
			}
			if vectorScan {
				vectorScan = false
				scalar, serr := readLines(doc, 1+int(n)%257, 1+int(size)%4099)
				vectorScan = true
				if !bytes.Equal(scalar, chunked) || errText(serr) != errText(cerr) {
					t.Fatalf("%s read a byte at a time is\n%q, %v\nand sixty-four at a time\n%q, %v",
						shortDoc(doc), scalar, serr, chunked, cerr)
				}
			}
			checkAgainstEncodingJSON(t, doc, whole, werr)
			return nil
		})
	})
}

// readLines is doc read through objectLines, its stream handing back n bytes a read
// at most, into a buffer of size bytes, to its end or its error.
func readLines(doc []byte, n, size int) ([]byte, error) {
	o := &objectLines{r: &chunked{b: bytes.Clone(doc), n: n}}
	var out bytes.Buffer
	buf := make([]byte, size)
	for {
		k, err := o.Read(buf)
		out.Write(buf[:k])
		if errors.Is(err, io.EOF) {
			return out.Bytes(), nil
		}
		if err != nil {
			return out.Bytes(), err
		}
	}
}

// checkAgainstEncodingJSON holds out and err, doc read whole, to what encoding/json
// makes of doc, a leading byte-order mark left out, which Polars' read_json reads and
// json.Valid does not.
func checkAgainstEncodingJSON(t *testing.T, doc, out []byte, err error) {
	text := bytes.TrimPrefix(doc, []byte("\xef\xbb\xbf"))
	if !json.Valid(text) {
		return
	}
	var objects []json.RawMessage
	rows := true
	switch first := bytes.TrimLeft(text, " \t\r\n"); first[0] {
	case '{':
		objects = []json.RawMessage{bytes.TrimRight(first, " \t\r\n")}
	case '[':
		if e := json.Unmarshal(text, &objects); e != nil {
			t.Fatalf("encoding/json refuses %s, which it called valid: %v", shortDoc(doc), e)
		}
		for _, o := range objects {
			rows = rows && o[0] == '{'
		}
	default:
		rows = false
	}
	if !rows {
		if err == nil {
			t.Fatalf("%s, JSON that is not an array of objects or one, is read as\n%q", shortDoc(doc), out)
		}
		return
	}
	if err != nil {
		t.Fatalf("%s, JSON of %d objects, is refused: %v", shortDoc(doc), len(objects), err)
	}
	lines := bytes.Split(out, []byte{'\n'})
	if last := bytes.Trim(lines[len(lines)-1], " "); len(last) != 0 {
		t.Fatalf("%s is read as\n%q, its last line holding %q", shortDoc(doc), out, last)
	}
	lines = lines[:len(lines)-1]
	if len(lines) != len(objects) {
		t.Fatalf("%s, JSON of %d objects, is read as %d lines:\n%q", shortDoc(doc), len(objects), len(lines), out)
	}
	for i, o := range objects {
		want := bytes.ReplaceAll(o, []byte{'\n'}, []byte{' '})
		if got := bytes.Trim(lines[i], " "); !bytes.Equal(got, want) {
			t.Fatalf("%s: line %d is %q, and encoding/json's object %d %q", shortDoc(doc), i+1, got, i+1, want)
		}
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// shortDoc is doc quoted for a failure's message, cut past 300 bytes.
func shortDoc(doc []byte) string {
	if len(doc) > 300 {
		return strconv.Quote(string(doc[:300])) + "… (" + strconv.Itoa(len(doc)) + " bytes)"
	}
	return strconv.Quote(string(doc))
}

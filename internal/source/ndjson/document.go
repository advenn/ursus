package ndjson

import (
	"errors"
	"io"

	"github.com/advenn/ursus/internal/uerr"
)

// A JSON document of rows (step 345).
//
// Polars' read_json reads a JSON document of an array of objects, a row an object, or
// of one object, one row. objectLines makes each object of one a line of NDJSON, so it
// is read as NDJSON is, in blocks of lines parsed a part a thread, with one schema's
// inference and casts. It rewrites the bytes in place, in the buffer the reader reads
// into, and moves none: what lies between the objects, the array's brackets, its
// commas and its space, becomes space, but for a newline after each object, and an
// object's newlines, which only its space can hold, become spaces. Each line is then
// one object between spaces, which a line's reading passes over, and the document's
// n-th object is its n-th line. The scan is the read's one serial part, so an object is
// read sixty-four bytes at a time where the CPU can (document_vector.go), faster than
// the lines are parsed.

// The states of a document's top level, between its objects.
const (
	docStart = iota // before the document
	docElem         // after the array's '[', an object or ']' next
	docAfter        // after an object of the array, ',' or ']' next
	docNext         // after a ',', an object next
	docEnd          // after the document, only space
)

// objectLines reads a JSON document as NDJSON, a line an object.
type objectLines struct {
	r     io.Reader
	state uint8
	bom   uint8 // bytes of a byte-order mark read at the document's start
	nl    bool  // an object ended and its newline is not yet written
	str   bool  // in a string of the object
	esc   bool  // the last read ended with a string's backslash, which escapes the next byte
	depth int   // brackets open in the object, 0 between objects
	n     int   // objects begun
	at    int64 // the stream's bytes read before this read's
	err   error // the stream's error, or the document's, once met
}

// stop marks the bytes an object's scan stops at: a string's quote or backslash, or a
// bracket or a newline outside one.
var stop = [256]bool{'"': true, '\\': true, '{': true, '[': true, '}': true, ']': true, '\n': true}

func (o *objectLines) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if o.err != nil {
			if o.nl {
				o.nl = false
				p[0] = '\n'
				return 1, nil
			}
			return 0, o.err
		}
		n, err := o.r.Read(p)
		k, bad := o.scan(p[:n])
		switch {
		case bad != nil:
			o.err = bad
		case errors.Is(err, io.EOF):
			o.err = o.end()
		case err != nil:
			o.err = err
		}
		if k > 0 {
			return k, nil
		}
	}
}

// scan rewrites b, a read's bytes, as NDJSON in place, and gives how many of them are
// read: all, or those before the document's error.
func (o *objectLines) scan(b []byte) (int, error) {
	r := 0
	for r < len(b) {
		if o.depth > 0 {
			r = o.object(b, r)
			continue
		}
		c := b[r]
		switch {
		case o.nl:
			b[r] = '\n'
			o.nl = false
		case c != '{':
			b[r] = ' '
		}
		r++
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		switch o.state {
		case docStart:
			// A UTF-8 byte-order mark may start the document, as Polars reads one.
			if pos := o.at + int64(r) - 1; pos < 3 && int64(o.bom) == pos && c == "\xef\xbb\xbf"[pos] {
				o.bom++
				continue
			}
			switch {
			case o.bom%3 != 0:
			case c == '[':
				o.state = docElem
				continue
			case c == '{':
				o.state, o.depth, o.n = docEnd, 1, 1
				continue
			}
			return r, o.refuse(r, "can only read JSON objects as rows: the document is an array of them or one, and starts with %s", shown(c))
		case docElem, docNext:
			switch {
			case c == '{':
				o.state, o.depth = docAfter, 1
				o.n++
				continue
			case c == ']' && o.state == docElem:
				o.state = docEnd
				continue
			case c == ']':
				return r, o.refuse(r, "a comma after object %d, before the array's end", o.n)
			case c == 'n':
				// Polars: "cannot deserialize struct with nulls into a DataFrame".
				return r, o.refuse(r, "can only read JSON objects as rows, and element %d of the array is null", o.n+1)
			}
			return r, o.refuse(r, "can only read JSON objects as rows, and element %d of the array starts with %s", o.n+1, shown(c))
		case docAfter:
			switch c {
			case ',':
				o.state = docNext
				continue
			case ']':
				o.state = docEnd
				continue
			}
			return r, o.refuse(r, "after object %d comes ',' or the array's end, not %s", o.n, shown(c))
		}
		return r, o.refuse(r, "text after the document's end, starting with %s", shown(c))
	}
	o.at += int64(len(b))
	return len(b), nil
}

// object reads the object begun from b[r], to its end or b's, and gives where it
// stopped: sixty-four bytes at a time where the CPU can (objectVector), and a byte at
// a time where it cannot, past a block it leaves, and at b's end.
func (o *objectLines) object(b []byte, r int) int {
	if o.esc {
		o.esc = false
		r++
	}
	for r < len(b) {
		lim := len(b)
		if vectorScan {
			var ended bool
			if r, ended = o.objectVector(b, r); ended {
				return r
			}
			lim = min(len(b), r+64)
		}
		var ended bool
		if r, ended = o.objectBytes(b, r, lim); ended {
			return r
		}
	}
	return min(r, len(b))
}

// objectBytes reads the object's bytes from b[r] to b[lim], or to its end, and gives
// where it stopped and whether the object ended there.
func (o *objectLines) objectBytes(b []byte, r, lim int) (int, bool) {
	for ; r < lim; r++ {
		c := b[r]
		if !stop[c] {
			continue
		}
		if o.str {
			switch c {
			case '"':
				o.str = false
			case '\\':
				// The byte after it is escaped, in this read or the next.
				if r++; r == len(b) {
					o.esc = true
				}
			}
			continue
		}
		switch c {
		case '"':
			o.str = true
		case '{', '[':
			o.depth++
		case '}', ']':
			if o.depth--; o.depth == 0 {
				o.nl = true
				return r + 1, true
			}
		case '\n':
			b[r] = ' '
		}
	}
	return r, false
}

// end is the error for the stream's end: io.EOF after a whole document.
func (o *objectLines) end() error {
	switch {
	case o.state == docEnd && o.depth == 0:
		return io.EOF
	case o.state == docStart:
		// Polars: "Eof at character 0".
		return uerr.New(uerr.KindValue, "read_json", "the document is empty: it is an array of objects or one object")
	case o.depth > 0:
		return uerr.New(uerr.KindValue, "read_json", "the document ends inside object %d", o.n)
	}
	return uerr.New(uerr.KindValue, "read_json", "the document ends inside its array, after object %d", o.n)
}

// refuse is the document's error at b[r-1] of this read.
func (o *objectLines) refuse(r int, format string, args ...any) error {
	return uerr.New(uerr.KindValue, "read_json", format, args...).Hint("at byte %d", o.at+int64(r))
}

// shown is c for an error: a printable byte quoted, another as its code.
func shown(c byte) string {
	if c >= ' ' && c < 0x7f {
		return "'" + string(c) + "'"
	}
	return "byte 0x" + string("0123456789abcdef"[c>>4]) + string("0123456789abcdef"[c&15])
}

// documentReader is a JSON document's stream read as NDJSON, closed as the stream.
type documentReader struct {
	objectLines
	c io.Closer
}

func (d *documentReader) Close() error { return d.c.Close() }

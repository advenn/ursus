package csv

import (
	"bytes"
	"io"

	"ursus/internal/uerr"
)

// scanner splits a CSV stream into records and fields WITHOUT materialising
// values.
//
// # Why not encoding/csv
//
// Three structural reasons, none of them a micro-benchmark.
//
// First, csv.Reader.Read returns every field of every row as a []string, having
// already copied each field's bytes into an internal record buffer. On a
// 100-column file where the query wants two columns that is 98 copies and 98
// string headers per row — the exact work projection pushdown exists to avoid.
// Splitting is unavoidable (field 7 cannot be found without walking past 1–6);
// MATERIALISING is not, and Read has already done it before we get a say.
//
// Second, Read hands back one row at a time in row-major form. Building an
// 8192-row batch would mean materialising 8192 × ncols string headers and then
// transposing, which defeats the columnar layout at the boundary where it matters.
//
// Third, error quality is a stated feature. ursus promises that a bad value on row
// 10,001 names the row, the column BY NAME, the text found and the type expected.
// csv.ParseError carries a line and a byte offset, and by the time it or a strconv
// error surfaces the raw bytes are already a detached string. A physical line
// distinct from a logical record, a multi-byte comment prefix, a configurable
// quote, and a record-size bound are all needed too, and none are available.
//
// The cost is the risk of getting quoting wrong, and it is paid down the same way
// the SIMD kernels pay theirs: scanner_diff_test.go runs a differential test
// against encoding/csv over a generated corpus, so a hand-written implementation
// is only allowed here because something independent checks it.
//
// # Field lifetime
//
// Field(i) aliases the scanner's buffer and is valid only until the next call to
// Next. Callers parse values immediately; nothing retains a field.
type scanner struct {
	r   io.Reader
	buf []byte
	beg int // start of unconsumed data
	end int // end of valid data
	eof bool

	sep     byte
	quote   byte
	comment []byte
	maxRec  int

	fields []fieldRef
	unesc  []byte // unescaped copies of fields containing doubled quotes

	line int // physical lines consumed, 1-based on the current record
	rec  int // logical records returned, 1-based on the current record
	err  error

	// skipped reports that the last parse consumed a comment or a blank line
	// rather than producing a record.
	skipped bool
}

// fieldRef locates one field. A field lands in unesc only when it contained a
// doubled quote, which is rare, so the common path stays copy-free.
type fieldRef struct {
	off, n int
	esc    bool
}

const (
	defaultBufSize = 64 << 10
	// defaultMaxRecord bounds a single record. An unterminated quote makes the
	// rest of the file one record, so without a bound a malformed 10 GB file is an
	// out-of-memory kill rather than an error message.
	defaultMaxRecord = 1 << 24 // 16 MiB
)

func newScanner(r io.Reader, o Options) *scanner {
	maxRec := o.MaxRecordSize
	if maxRec <= 0 {
		maxRec = defaultMaxRecord
	}
	size := min(defaultBufSize, maxRec)
	return &scanner{
		r:       r,
		buf:     make([]byte, size),
		sep:     o.Separator,
		quote:   o.Quote,
		comment: o.Comment,
		maxRec:  maxRec,
	}
}

// Line and Record report the position of the record just returned, both 1-based.
// They differ whenever a field contains a newline, a line is a comment, or rows
// were skipped — which is exactly when a "line 3" in an error message would point
// at the wrong row.
func (s *scanner) Line() int   { return s.line }
func (s *scanner) Record() int { return s.rec }

func (s *scanner) Err() error {
	if s.err == io.EOF {
		return nil
	}
	return s.err
}

// NumFields and Field expose the current record.
func (s *scanner) NumFields() int { return len(s.fields) }

func (s *scanner) Field(i int) []byte {
	f := s.fields[i]
	if f.esc {
		return s.unesc[f.off : f.off+f.n]
	}
	return s.buf[f.off : f.off+f.n]
}

// Next advances to the next record, skipping comment lines and empty lines.
// It reports false at end of input or on error; check Err.
func (s *scanner) Next() bool {
	if s.err != nil {
		return false
	}
	for {
		ok, err := s.readRecord()
		if err != nil {
			s.err = err
			return false
		}
		if !ok {
			s.err = io.EOF
			return false
		}
		// A comment or a blank line is consumed and does not become a record.
		if s.skipped {
			continue
		}
		s.rec++
		return true
	}
}

// readRecord parses one record, refilling as needed.
//
// The scan restarts from the record's first byte after every refill rather than
// resuming mid-parse. That is O(record) extra work per refill, and a refill
// happens once per buffer-full rather than once per record, so the total stays
// linear — while removing the entire class of bug that comes from suspending and
// resuming a quote-state machine across a buffer boundary.
func (s *scanner) readRecord() (bool, error) {
	for {
		if s.beg < s.end {
			n, nl, ok, err := s.parse(s.buf[s.beg:s.end], s.eof)
			if err != nil {
				return false, err
			}
			if ok {
				// parse indexed into the sub-slice; shift to buffer coordinates.
				for i := range s.fields {
					if !s.fields[i].esc {
						s.fields[i].off += s.beg
					}
				}
				s.beg += n
				// Line numbers are committed ONLY here, once the record is known to
				// be complete. parse can run several times over the same record as
				// the buffer refills, and incrementing s.line inside it counted the
				// newlines of every already-parsed field once per attempt.
				s.line += nl
				return true, nil
			}
		}
		if s.eof {
			return false, nil // no complete record and nothing more is coming
		}
		if err := s.fill(); err != nil {
			return false, err
		}
	}
}

// fill compacts and refills the buffer, growing it if the current record already
// spans everything held.
func (s *scanner) fill() error {
	if s.beg > 0 {
		copy(s.buf, s.buf[s.beg:s.end])
		s.end -= s.beg
		s.beg = 0
	}
	if s.end == len(s.buf) {
		if len(s.buf) >= s.maxRec {
			return uerr.New(uerr.KindValue, "scan_csv",
				"a single record exceeds the %d-byte limit near line %d",
				s.maxRec, s.line+1).
				Hint("an unterminated quote makes the rest of the file one record").
				Hint("raise it with WithMaxRecordSize if the data really is this wide")
		}
		grown := min(len(s.buf)*2, s.maxRec)
		next := make([]byte, grown)
		copy(next, s.buf[:s.end])
		s.buf = next
	}
	n, err := s.r.Read(s.buf[s.end:])
	s.end += n
	if err == io.EOF {
		s.eof = true
		return nil
	}
	if err != nil {
		return uerr.Wrap(err, uerr.KindIO, "scan_csv", "reading near line %d", s.line+1)
	}
	if n == 0 {
		// A reader that returns (0, nil) forever would spin. Treat a second empty
		// read as end of input rather than hanging.
		s.eof = true
	}
	return nil
}

// parse reads one record from b. ok=false means b does not hold a complete record
// and more input is needed. n is the bytes consumed and nl the physical lines they
// spanned; neither is applied unless ok.
func (s *scanner) parse(b []byte, atEOF bool) (n, nl int, ok bool, err error) {
	s.fields = s.fields[:0]
	s.unesc = s.unesc[:0]
	s.skipped = false

	// A comment line is consumed whole, up to and including its newline.
	if len(s.comment) > 0 && bytes.HasPrefix(b, s.comment) {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			if !atEOF {
				return 0, 0, false, nil
			}
			s.skipped = true
			return len(b), 1, true, nil
		}
		s.skipped = true
		return i + 1, 1, true, nil
	}
	// A blank line is consumed and produces no record, matching encoding/csv.
	if b[0] == '\n' {
		s.skipped = true
		return 1, 1, true, nil
	}
	if b[0] == '\r' && len(b) > 1 && b[1] == '\n' {
		s.skipped = true
		return 2, 1, true, nil
	}

	i := 0
	for {
		// --- one field ---
		if i < len(b) && b[i] == s.quote {
			consumed, endOfRec, lines, e := s.quotedField(b, i, atEOF, nl)
			if e != nil {
				return 0, 0, false, e
			}
			if consumed < 0 {
				return 0, 0, false, nil // incomplete
			}
			i = consumed
			nl += lines
			if endOfRec {
				return i, nl + 1, true, nil
			}
			continue
		}

		start := i
		for i < len(b) && b[i] != s.sep && b[i] != '\n' {
			i++
		}
		if i == len(b) && !atEOF {
			return 0, 0, false, nil // the field may continue past the buffer
		}

		fieldEnd := i
		// A \r immediately before the \n is a line terminator, not data. A lone \r
		// elsewhere IS data, which is what encoding/csv does.
		if i < len(b) && b[i] == '\n' && fieldEnd > start && b[fieldEnd-1] == '\r' {
			fieldEnd--
		}
		s.fields = append(s.fields, fieldRef{off: start, n: fieldEnd - start})

		if i >= len(b) { // atEOF: the last record has no trailing newline
			return i, nl + 1, true, nil
		}
		if b[i] == '\n' {
			return i + 1, nl + 1, true, nil
		}
		i++ // step over the separator and read the next field
	}
}

// quotedField consumes a quoted field starting at b[i] (which is the quote).
// It returns the index just past the field's terminator, whether that terminator
// ended the record, and how many newlines the field spanned. consumed < 0 means
// the record is incomplete.
func (s *scanner) quotedField(b []byte, i int, atEOF bool, nlSoFar int) (consumed int, endOfRec bool, lines int, err error) {
	open := i
	i++ // step over the opening quote

	start := i
	esc := false
	for {
		j := bytes.IndexByte(b[i:], s.quote)
		if j < 0 {
			if atEOF {
				return 0, false, 0, uerr.New(uerr.KindValue, "scan_csv",
					"unterminated quoted field starting at line %d", s.line+nlSoFar+1).
					Hint("a quote inside a quoted field must be doubled: \"\"")
			}
			return -1, false, 0, nil
		}
		i += j
		if i+1 < len(b) && b[i+1] == s.quote {
			esc = true
			i += 2
			continue
		}
		if i+1 >= len(b) && !atEOF {
			return -1, false, 0, nil // cannot tell a closing quote from a doubled one
		}
		break
	}
	content := b[start:i]
	i++ // step over the closing quote

	// Count the newlines the field spanned, so line numbers stay physical.
	lines = bytes.Count(content, []byte{'\n'})

	// A CRLF inside a quoted field is normalised to LF, matching encoding/csv
	// ("carriage returns before newline characters are silently removed"). The
	// differential test caught the divergence: RFC 4180 would call the \r data, but
	// a Windows-authored file read by every other Go program yields \n, and a CSV
	// reader that disagrees with encoding/csv about the contents of a field is a
	// worse trap than the rare field that genuinely wants a literal CRLF.
	if esc || bytes.Contains(content, crlf) {
		off := len(s.unesc)
		s.unesc = appendNormalised(s.unesc, content, s.quote)
		s.fields = append(s.fields, fieldRef{off: off, n: len(s.unesc) - off, esc: true})
	} else {
		s.fields = append(s.fields, fieldRef{off: start, n: len(content)})
	}

	// What follows the closing quote must be a separator or a line terminator.
	if i >= len(b) {
		if !atEOF {
			return -1, false, 0, nil
		}
		return i, true, lines, nil
	}
	switch {
	case b[i] == s.sep:
		return i + 1, false, lines, nil
	case b[i] == '\n':
		return i + 1, true, lines, nil
	case b[i] == '\r':
		if i+1 >= len(b) && !atEOF {
			return -1, false, 0, nil
		}
		if i+1 < len(b) && b[i+1] == '\n' {
			return i + 2, true, lines, nil
		}
		fallthrough
	default:
		return 0, false, 0, uerr.New(uerr.KindValue, "scan_csv",
			"unexpected %q after a closing quote at line %d", b[i], s.line+nlSoFar+1).
			Hint("a quote inside a quoted field must be doubled: \"\"").
			Hint("field started at byte offset %d of this record", open)
	}
}

var crlf = []byte{'\r', '\n'}

// appendNormalised copies src to dst, collapsing each doubled quote to one and
// each CRLF to LF. Both transformations shrink the input, so one pass suffices.
func appendNormalised(dst, src []byte, quote byte) []byte {
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case c == quote && i+1 < len(src) && src[i+1] == quote:
			dst = append(dst, quote)
			i++ // skip the second quote of the pair
		case c == '\r' && i+1 < len(src) && src[i+1] == '\n':
			dst = append(dst, '\n')
			i++ // skip the \n; it was just written
		default:
			dst = append(dst, c)
		}
	}
	return dst
}

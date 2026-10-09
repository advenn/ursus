package csv

import (
	"bytes"
	"io"

	"github.com/advenn/ursus/internal/uerr"
)

// splitter cuts a stream into blocks of whole records, so that several goroutines
// can parse one file at once (step 144).
//
// # Why
//
// The scanner runs on one goroutine, behind the reader's lock, and it was the
// query: h2o gb1 over CSV kept 1.4 cores busy, its scan 56% of the CPU (step 141).
// Converting fields was already split by column, which for a two-column read is two
// ways at most.
//
// # Exact, or not at all
//
// A block must end where a record ends, and a newline inside a quoted field is not
// one. Counting quotes is not enough: the scanner reads a quote in the middle of an
// unquoted field as data, so a stray one flips any parity. The splitter follows the
// scanner's own rule instead: a quote opens a quoted field only where a field
// starts, a doubled quote inside one is data, and after its closing quote comes a
// separator or a line ending. It skips comment lines whole, as the scanner does,
// and counts the data rows each block holds, so that a block's first row is known
// before it is parsed and its errors name the same rows the serial path would.
//
// Anything it does not fully understand — a character after a closing quote, a
// quote still open at the end of the stream, a record longer than MaxRecordSize —
// it does not guess at. It stops before that record, and the reader hands the rest
// of the stream to the serial scanner, which reports it exactly as it always has.
type splitter struct {
	r     io.Reader
	carry []byte // the start of a record the last block did not reach the end of
	eof   bool

	sep, quote byte
	comment    []byte
	maxRec     int
	width      int // the scanner's: a blank line is a record only in a one-column file

	line int // physical lines before the next block, the scanner's count
	row  int // data rows before the next block

	stopped bool // an anomaly was met: the rest of the stream is the serial path's
}

// block is a run of whole records, and where it sits in its stream.
type block struct {
	data      []byte
	firstLine int // physical lines before data[0]
	firstRow  int // data rows before the block, in this query
	rows      int // data rows in the block
	wanted    []int
	part      string // the stream's name, for errors
	width     int    // the scanner's width for the stream
}

// splitBlockSize is how much a block holds, give or take a record. A variable so a
// test can make it small enough to put block boundaries everywhere.
var splitBlockSize = 1 << 20

// newSplitter takes over sc's stream after its header: what sc has buffered, and
// what is still to be read. row is the data rows the query has read before it.
func newSplitter(sc *scanner, row int) *splitter {
	return &splitter{
		r:       sc.r,
		carry:   append([]byte(nil), sc.buf[sc.beg:sc.end]...),
		eof:     sc.eof,
		sep:     sc.sep,
		quote:   sc.quote,
		comment: sc.comment,
		maxRec:  sc.maxRec,
		width:   sc.width,
		line:    sc.line,
		row:     row,
	}
}

// next returns the next block. ok is false when there is none: at the end of the
// stream, or when the splitter has stopped and rest holds the serial path's start.
func (sp *splitter) next() (blk block, ok bool, err error) {
	if sp.stopped {
		return block{}, false, nil
	}
	buf := sp.carry
	sp.carry = nil
	for !sp.eof && len(buf) < splitBlockSize {
		if buf, err = sp.read(buf, splitBlockSize); err != nil {
			return block{}, false, err
		}
	}
	for {
		cut, rows, anomaly := sp.boundary(buf, sp.eof, splitBlockSize)
		if cut > 0 {
			blk = block{data: buf[:cut], firstLine: sp.line, firstRow: sp.row, rows: rows,
				width: sp.width}
			sp.carry = append([]byte(nil), buf[cut:]...)
			sp.line += bytes.Count(blk.data, newline)
			sp.row += rows
			if anomaly {
				sp.stopped = true
			}
			return blk, true, nil
		}
		if anomaly || (sp.eof && len(buf) > 0) {
			// The first record is one the splitter will not cut: the serial path's.
			sp.carry, sp.stopped = buf, true
			return block{}, false, nil
		}
		if sp.eof {
			return block{}, false, nil
		}
		if len(buf) >= sp.maxRec {
			sp.carry, sp.stopped = buf, true // the serial scanner names the limit
			return block{}, false, nil
		}
		if buf, err = sp.read(buf, 2*len(buf)); err != nil {
			return block{}, false, err
		}
	}
}

// rest is the stream from where the splitter stopped: what it holds, then what is
// still to be read. The serial scanner continues from it.
func (sp *splitter) rest() io.Reader {
	if sp.eof {
		return bytes.NewReader(sp.carry)
	}
	return io.MultiReader(bytes.NewReader(sp.carry), sp.r)
}

var newline = []byte{'\n'}

// read appends to buf until it holds at least want bytes or the stream ends.
func (sp *splitter) read(buf []byte, want int) ([]byte, error) {
	if cap(buf) < want {
		grown := make([]byte, len(buf), want)
		copy(grown, buf)
		buf = grown
	}
	for !sp.eof && len(buf) < want {
		n, err := sp.r.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		if err == io.EOF || (err == nil && n == 0) {
			sp.eof = true
			break
		}
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindIO, "scan_csv", "reading near line %d", sp.line+1)
		}
	}
	return buf, nil
}

// boundary returns the end of the last whole record in b, which starts at a record,
// and the data rows before it, stopping at the first record end at or past limit.
// anomaly reports that the record after cut is one the splitter will not follow;
// cut is then where the serial path must start.
//
// The limit is what keeps a block near its size when b holds more: the scanner may
// have buffered much of a stream while it read the header.
func (sp *splitter) boundary(b []byte, atEOF bool, limit int) (cut, rows int, anomaly bool) {
	pos := 0
	for pos < len(b) && cut < limit {
		// A comment line, whole, up to and including its newline.
		if len(sp.comment) > 0 && bytes.HasPrefix(b[pos:], sp.comment) {
			nl := bytes.IndexByte(b[pos:], '\n')
			if nl < 0 {
				if atEOF {
					return len(b), rows, false
				}
				return cut, rows, false
			}
			pos += nl + 1
			cut = pos
			continue
		}
		// A blank line: a record only in a one-column file.
		blank := 0
		switch {
		case b[pos] == '\n':
			blank = 1
		case b[pos] == '\r' && pos+1 < len(b) && b[pos+1] == '\n':
			blank = 2
		}
		if blank > 0 {
			pos += blank
			cut = pos
			if sp.width == 1 {
				rows++
			}
			continue
		}
		end, state := sp.recordEnd(b, pos, atEOF)
		switch state {
		case recordWhole:
			pos, cut = end, end
			rows++
		case recordShort:
			return cut, rows, false
		default:
			return cut, rows, true
		}
	}
	return cut, rows, false
}

type recordState uint8

const (
	recordWhole recordState = iota // ends within b
	recordShort                    // needs more input
	recordOdd                      // the serial scanner's to read, and likely to refuse
)

// recordEnd follows the record starting at pos, field by field, as scanner.parse
// does, and returns the index just past its line ending.
func (sp *splitter) recordEnd(b []byte, pos int, atEOF bool) (int, recordState) {
	i := pos // always at the start of a field
	for {
		if i < len(b) && b[i] == sp.quote {
			// A quoted field: a doubled quote in it is data, and its closing quote is
			// followed by a separator or a line ending, or the record is the scanner's.
			j := i + 1
			for {
				k := bytes.IndexByte(b[j:], sp.quote)
				if k < 0 {
					if atEOF {
						return 0, recordOdd // unterminated: the scanner's error
					}
					return 0, recordShort
				}
				j += k
				if j+1 < len(b) && b[j+1] == sp.quote {
					j += 2
					continue
				}
				if j+1 >= len(b) && !atEOF {
					return 0, recordShort // a closing quote or a doubled one
				}
				break
			}
			i = j + 1 // past the closing quote
			switch {
			case i >= len(b):
				if !atEOF {
					return 0, recordShort
				}
				return i, recordWhole
			case b[i] == sp.sep:
				i++
				continue
			case b[i] == '\n':
				return i + 1, recordWhole
			case b[i] == '\r':
				if i+1 >= len(b) && !atEOF {
					return 0, recordShort
				}
				if i+1 < len(b) && b[i+1] == '\n' {
					return i + 2, recordWhole
				}
				return 0, recordOdd
			default:
				return 0, recordOdd
			}
		}
		// An unquoted field, and every field after it until a quote starts one. A
		// quote inside an unquoted field is data, and an unquoted field cannot hold a
		// separator, so a quote starts a field exactly when a separator precedes it.
		nl := bytes.IndexByte(b[i:], '\n')
		lineEnd := len(b)
		if nl >= 0 {
			lineEnd = i + nl
		}
		j := i
		for {
			q := bytes.IndexByte(b[j:lineEnd], sp.quote)
			if q < 0 {
				if nl < 0 {
					if !atEOF {
						return 0, recordShort
					}
					return len(b), recordWhole
				}
				return lineEnd + 1, recordWhole
			}
			qp := j + q // > i: the field at i does not start with a quote
			if b[qp-1] == sp.sep {
				i = qp
				break
			}
			j = qp + 1
		}
	}
}

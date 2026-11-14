package readfuzz_test

import (
	"encoding/binary"
)

// Forged Parquet files (step 510): what no writer makes, a page header or a footer
// claiming what the bytes do not hold. A claim larger than the field it replaces
// takes more varint bytes, so a written file cannot be patched in place; these are
// built whole, with a writer of Thrift's compact protocol and a file of one REQUIRED
// column, uncompressed, in one row group.

// The compact protocol's types, as they are written in a field's header.
const (
	thriftI32    = 5
	thriftI64    = 6
	thriftBinary = 8
	thriftList   = 9
	thriftStruct = 12
)

// thriftCompact writes Thrift's compact protocol: enough of it for a Parquet page
// header and footer.
type thriftCompact struct {
	b    []byte
	last []int16 // each open struct's last field id, the innermost last
}

func (w *thriftCompact) uvarint(v uint64) { w.b = binary.AppendUvarint(w.b, v) }
func (w *thriftCompact) zigzag(v int64)   { w.uvarint(uint64(v<<1) ^ uint64(v>>63)) }

// field writes a field's header: its id as a delta from the last where it fits.
func (w *thriftCompact) field(id int16, typ byte) {
	last := &w.last[len(w.last)-1]
	if d := id - *last; d > 0 && d <= 15 {
		w.b = append(w.b, byte(d)<<4|typ)
	} else {
		w.b = append(w.b, typ)
		w.zigzag(int64(id))
	}
	*last = id
}

func (w *thriftCompact) i32(id int16, v int32) { w.field(id, thriftI32); w.zigzag(int64(v)) }
func (w *thriftCompact) i64(id int16, v int64) { w.field(id, thriftI64); w.zigzag(v) }

func (w *thriftCompact) str(id int16, s string) {
	w.field(id, thriftBinary)
	w.uvarint(uint64(len(s)))
	w.b = append(w.b, s...)
}

// begin opens a struct value: the file's, a list's element, or after a field header.
func (w *thriftCompact) begin() { w.last = append(w.last, 0) }

// sub opens a struct-valued field.
func (w *thriftCompact) sub(id int16) { w.field(id, thriftStruct); w.begin() }

// end closes the innermost struct.
func (w *thriftCompact) end() {
	w.b = append(w.b, 0)
	w.last = w.last[:len(w.last)-1]
}

// list writes a list field's header, saying it holds n elements of type et; the
// elements follow.
func (w *thriftCompact) list(id int16, et byte, n int) {
	w.field(id, thriftList)
	if n < 15 {
		w.b = append(w.b, byte(n)<<4|et)
		return
	}
	w.b = append(w.b, 0xf0|et)
	w.uvarint(uint64(n))
}

// Parquet's enums, as format/parquet.thrift numbers them.
const (
	pqInt64     = 2
	pqByteArray = 6

	pqPageData = 0
	pqPageDict = 2

	pqTimeMicros = 8 // a converted_type

	pqPlain       = 0
	pqRLE         = 3
	pqDeltaBinary = 5
	pqDeltaLength = 6
	pqRLEDict     = 8
)

// pqPage is one page of a forged file: its header's claims and its body.
type pqPage struct {
	typ          int32 // pqPageData or pqPageDict
	values       int32 // the header's count of values
	encoding     int32
	uncompressed int32 // the header's sizes; zero is the body's length
	compressed   int32
	body         []byte
}

// pqForged is a file of one REQUIRED column "v" of physical type phys, its pages as
// given, uncompressed, in one row group of rows rows. Its footer says what a writer
// would of those pages, except that its list of row groups claims groups of them,
// zero being one: the footer holds one either way.
type pqForged struct {
	phys   int32
	rows   int64
	pages  []pqPage
	groups int
	chunk  int64   // the column chunk's total_compressed_size; zero is its bytes'
	encs   []int32 // encodings the footer lists beside its pages', as a writer's own
	conv   int32   // the column's converted_type, zero for none
}

func (f pqForged) bytes() []byte {
	b := []byte("PAR1")
	start := int64(len(b))
	dataAt, dictAt := int64(-1), int64(-1)
	for _, p := range f.pages {
		at := int64(len(b))
		size := func(claim int32) int32 {
			if claim == 0 {
				return int32(len(p.body))
			}
			return claim
		}
		var h thriftCompact
		h.begin()
		h.i32(1, p.typ)
		h.i32(2, size(p.uncompressed))
		h.i32(3, size(p.compressed))
		if p.typ == pqPageDict {
			h.sub(7)
			h.i32(1, p.values)
			h.i32(2, p.encoding)
			h.end()
			if dictAt < 0 {
				dictAt = at
			}
		} else {
			h.sub(5)
			h.i32(1, p.values)
			h.i32(2, p.encoding)
			h.i32(3, pqRLE) // the definition levels'
			h.i32(4, pqRLE) // the repetition levels'
			h.end()
			if dataAt < 0 {
				dataAt = at
			}
		}
		h.end()
		b = append(b, h.b...)
		b = append(b, p.body...)
	}
	chunk := int64(len(b)) - start
	encodings := []int32{pqPlain, pqRLE}
	if dictAt >= 0 {
		encodings = append(encodings, pqRLEDict)
	}
	encodings = append(encodings, f.encs...)

	var w thriftCompact
	w.begin()
	w.i32(1, 1) // version
	w.list(2, thriftStruct, 2)
	w.begin()
	w.str(4, "schema")
	w.i32(5, 1) // num_children
	w.end()
	w.begin()
	w.i32(1, f.phys)
	w.i32(3, 0) // REQUIRED
	w.str(4, "v")
	if f.conv != 0 {
		w.i32(6, f.conv)
	}
	w.end()
	w.i64(3, f.rows)
	w.list(4, thriftStruct, max(f.groups, 1))
	w.begin() // the row group
	w.list(1, thriftStruct, 1)
	w.begin() // its column chunk
	w.i64(2, start)
	w.sub(3) // its metadata
	w.i32(1, f.phys)
	w.list(2, thriftI32, len(encodings))
	for _, e := range encodings {
		w.zigzag(int64(e))
	}
	w.list(3, thriftBinary, 1)
	w.uvarint(1)
	w.b = append(w.b, 'v')
	w.i32(4, 0) // UNCOMPRESSED
	w.i64(5, f.rows)
	w.i64(6, chunk) // total_uncompressed_size
	if f.chunk != 0 {
		w.i64(7, f.chunk) // total_compressed_size
	} else {
		w.i64(7, chunk)
	}
	w.i64(9, dataAt)
	if dictAt >= 0 {
		w.i64(11, dictAt)
	}
	w.end()         // the metadata
	w.end()         // the column chunk
	w.i64(2, chunk) // total_byte_size
	w.i64(3, f.rows)
	w.end() // the row group
	w.end() // the file's metadata

	b = append(b, w.b...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(w.b)))
	return append(b, "PAR1"...)
}

// pqInt64Page is a PLAIN data page of vals.
func pqInt64Page(vals []int64) pqPage {
	body := make([]byte, 0, 8*len(vals))
	for _, v := range vals {
		body = binary.LittleEndian.AppendUint64(body, uint64(v))
	}
	return pqPage{typ: pqPageData, values: int32(len(vals)), encoding: pqPlain, body: body}
}

// pqDictPages are a PLAIN dictionary page of dict and a data page of rows indices
// into it, all 0: one RLE run.
func pqDictPages(dict []string, rows int) []pqPage {
	var body []byte
	for _, s := range dict {
		body = binary.LittleEndian.AppendUint32(body, uint32(len(s)))
		body = append(body, s...)
	}
	// The indices' bit width, then one run of rows zeros: its header rows<<1, its
	// value a byte.
	idx := binary.AppendUvarint([]byte{1}, uint64(rows)<<1)
	idx = append(idx, 0)
	return []pqPage{
		{typ: pqPageDict, values: int32(len(dict)), encoding: pqPlain, body: body},
		{typ: pqPageData, values: int32(rows), encoding: pqRLEDict, body: idx},
	}
}

// pqForgedSeeds are the forged files the Parquet target starts from, each honest
// version among them: a data page claiming 2^28 bytes uncompressed, and one
// claiming 2^26 compressed, arrow-go's limits (D14 of v0.7.2-plan.md); a dictionary
// page claiming 2^31−1 values, read by the reader's own pages and, its footer listing
// an encoding they do not read, by arrow-go's; a footer whose list of row groups
// claims 10^8; a column chunk claiming 2^40 bytes.
func pqForgedSeeds() [][]byte {
	vals := []int64{3, 1, 4, 1, 5, 9, 2, 6}
	page := pqInt64Page(vals)
	claims := func(mut func(*pqPage)) []byte {
		p := page
		mut(&p)
		return pqForged{phys: pqInt64, rows: int64(len(vals)), pages: []pqPage{p}}.bytes()
	}
	dict := pqDictPages([]string{"ab", "cd"}, 5)
	counted := append([]pqPage(nil), dict...)
	counted[0].values = 1<<31 - 1
	return [][]byte{
		pqForged{phys: pqInt64, rows: int64(len(vals)), pages: []pqPage{page}}.bytes(),
		claims(func(p *pqPage) { p.uncompressed = 1 << 28 }),
		claims(func(p *pqPage) { p.compressed = 1 << 26 }),
		pqForged{phys: pqByteArray, rows: 5, pages: dict}.bytes(),
		pqForged{phys: pqByteArray, rows: 5, pages: counted}.bytes(),
		pqForged{phys: pqByteArray, rows: 5, pages: counted, encs: []int32{pqDeltaLength}}.bytes(),
		pqForged{phys: pqInt64, rows: int64(len(vals)), pages: []pqPage{page}, groups: 100_000_000}.bytes(),
		pqForged{phys: pqInt64, rows: int64(len(vals)), pages: []pqPage{page}, chunk: 1 << 40}.bytes(),
	}
}

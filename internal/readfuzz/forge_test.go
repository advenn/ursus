package readfuzz_test

import (
	"encoding/binary"
	"slices"
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
	pqInt96     = 3
	pqByteArray = 6
	pqFixed     = 7 // FIXED_LEN_BYTE_ARRAY

	pqPageData = 0
	pqPageDict = 2

	pqListType   = 3 // a group's converted_type
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

// Where a forged file's one leaf sits (step 517): the column "v" itself, the
// element of a List "v" of the three-level layout (a REQUIRED group annotated LIST,
// a REPEATED group "list", a REQUIRED leaf "element"), or the one field "x" of a
// REQUIRED Struct "v". A leaf in a List has a repetition level and a definition
// level, so its data pages begin with both, each RLE with its 4-byte length.
const (
	pqFlat = iota
	pqInList
	pqInStruct
)

// pqForged is a file of one column "v" of physical type phys, REQUIRED unless
// optional, its leaf where nest puts it, its pages as given, uncompressed, in one
// row group of rows rows. Its footer says what a writer would of those pages, except
// that its list of row groups claims groups of them, zero being one: the footer
// holds one either way.
type pqForged struct {
	phys     int32
	rows     int64
	pages    []pqPage
	groups   int
	chunk    int64   // the column chunk's total_compressed_size; zero is its bytes'
	encs     []int32 // encodings the footer lists beside its pages', as a writer's own
	conv     int32   // the column's converted_type, zero for none
	typeLen  int32   // a FIXED_LEN_BYTE_ARRAY's length
	nest     int     // pqFlat, pqInList or pqInStruct
	optional bool    // the leaf is OPTIONAL: a flat column's pages begin with its definition levels
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
	// group writes a group's SchemaElement: its repetition, name, children and
	// converted_type, zero for none; leaf writes the leaf's.
	group := func(rep int32, name string, conv int32) {
		w.begin()
		w.i32(3, rep)
		w.str(4, name)
		w.i32(5, 1) // num_children
		if conv != 0 {
			w.i32(6, conv)
		}
		w.end()
	}
	leaf := func(name string) {
		w.begin()
		w.i32(1, f.phys)
		if f.typeLen != 0 {
			w.i32(2, f.typeLen)
		}
		if f.optional {
			w.i32(3, 1) // OPTIONAL
		} else {
			w.i32(3, 0) // REQUIRED
		}
		w.str(4, name)
		if f.conv != 0 {
			w.i32(6, f.conv)
		}
		w.end()
	}
	path := []string{"v"}
	w.begin()
	w.i32(1, 1) // version
	switch f.nest {
	case pqInList:
		w.list(2, thriftStruct, 4)
		w.begin()
		w.str(4, "schema")
		w.i32(5, 1) // num_children
		w.end()
		group(0, "v", pqListType)
		group(2, "list", 0) // REPEATED
		leaf("element")
		path = []string{"v", "list", "element"}
	case pqInStruct:
		w.list(2, thriftStruct, 3)
		w.begin()
		w.str(4, "schema")
		w.i32(5, 1) // num_children
		w.end()
		group(0, "v", 0)
		leaf("x")
		path = []string{"v", "x"}
	default:
		w.list(2, thriftStruct, 2)
		w.begin()
		w.str(4, "schema")
		w.i32(5, 1) // num_children
		w.end()
		leaf("v")
	}
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
	w.list(3, thriftBinary, len(path))
	for _, p := range path {
		w.uvarint(uint64(len(p)))
		w.b = append(w.b, p...)
	}
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

// pqIndexKind is a kind of column chunk arrow-go's column reader decodes and the
// reader's own pages do not (pages.go, ownable), for step 517's forged dictionary
// indices: its file's shape, two values of its physical type as PLAIN lays them
// out, the levels each of its data pages of pqIndexRows rows begins with, and how
// many of those rows hold a value.
type pqIndexKind struct {
	name    string
	shape   pqForged // its rows and pages are file's
	values  [2][]byte
	levels  []byte
	present int
}

// pqIndexRows is the rows of each data page of a pqIndexKind's file.
const pqIndexRows = 5

// pqIndexKinds are the kinds: an INT64 whose footer lists an encoding the own pages
// do not read, REQUIRED and OPTIONAL with a null among its rows; the element of a
// List; the field of a Struct; INT96; FIXED_LEN_BYTE_ARRAY; and a BYTE_ARRAY whose
// footer lists an encoding the own pages do not read.
func pqIndexKinds() []pqIndexKind {
	le64 := func(v uint64) []byte { return binary.LittleEndian.AppendUint64(nil, v) }
	int96 := func(nanos uint64, day uint32) []byte { return binary.LittleEndian.AppendUint32(le64(nanos), day) }
	text := func(s string) []byte { return append(binary.LittleEndian.AppendUint32(nil, uint32(len(s))), s...) }
	// levels is one data page's levels, each an RLE level run of its 4-byte length.
	levels := func(runs ...[]byte) []byte {
		var b []byte
		for _, r := range runs {
			b = append(binary.LittleEndian.AppendUint32(b, uint32(len(r))), r...)
		}
		return b
	}
	all := func(level byte) []byte { return []byte{pqIndexRows << 1, level} } // one RLE run of the rows
	// A null third row: one bit-packed group of definition levels, 1 1 0 1 1, and
	// three of padding.
	thirdNull := []byte{1<<1 | 1, 0b11011}
	i64 := [2][]byte{le64(7), le64(9)}
	return []pqIndexKind{
		{name: "INT64, the footer listing DELTA_BINARY_PACKED",
			shape: pqForged{phys: pqInt64, encs: []int32{pqDeltaBinary}}, values: i64, present: pqIndexRows},
		{name: "an OPTIONAL INT64 with a null, the footer listing DELTA_BINARY_PACKED",
			shape:  pqForged{phys: pqInt64, encs: []int32{pqDeltaBinary}, optional: true},
			values: i64, levels: levels(thirdNull), present: pqIndexRows - 1},
		{name: "the INT64 element of a List", shape: pqForged{phys: pqInt64, nest: pqInList}, values: i64,
			levels: levels(all(0), all(1)), present: pqIndexRows},
		{name: "the INT64 field of a Struct", shape: pqForged{phys: pqInt64, nest: pqInStruct}, values: i64,
			present: pqIndexRows},
		{name: "INT96", shape: pqForged{phys: pqInt96},
			values: [2][]byte{int96(0, 2_440_588), int96(1, 2_440_589)}, present: pqIndexRows},
		{name: "FIXED_LEN_BYTE_ARRAY(4)", shape: pqForged{phys: pqFixed, typeLen: 4},
			values: [2][]byte{[]byte("abcd"), []byte("wxyz")}, present: pqIndexRows},
		{name: "BYTE_ARRAY, the footer listing DELTA_LENGTH_BYTE_ARRAY",
			shape:  pqForged{phys: pqByteArray, encs: []int32{pqDeltaLength}},
			values: [2][]byte{text("ab"), text("cd")}, present: pqIndexRows},
	}
}

// file is k's file of pages, pqIndexRows rows for each data page among them.
func (k pqIndexKind) file(pages ...pqPage) []byte {
	f := k.shape
	f.pages = pages
	for _, p := range pages {
		if p.typ == pqPageData {
			f.rows += pqIndexRows
		}
	}
	return f.bytes()
}

// dict is a PLAIN dictionary page of k's two values.
func (k pqIndexKind) dict() pqPage {
	return pqPage{typ: pqPageDict, values: 2, encoding: pqPlain, body: append(slices.Clone(k.values[0]), k.values[1]...)}
}

// data is a data page of k's levels and then vals, encoded as enc.
func (k pqIndexKind) data(enc int32, vals []byte) pqPage {
	return pqPage{typ: pqPageData, values: pqIndexRows, encoding: enc, body: append(slices.Clone(k.levels), vals...)}
}

// plain is a PLAIN data page of k's levels and its value v in each row it has one.
func (k pqIndexKind) plain(v []byte) pqPage {
	var vals []byte
	for range k.present {
		vals = append(vals, v...)
	}
	return k.data(pqPlain, vals)
}

// pqIndexRun is a data page's dictionary indices: their bit width, 31, wider than a
// dictionary of two needs, and one RLE run of n copies of idx.
func pqIndexRun(n int, idx uint32) []byte {
	return binary.LittleEndian.AppendUint32(binary.AppendUvarint([]byte{31}, uint64(n)<<1), idx)
}

// pqIndexPacked is a data page's dictionary indices: their bit width, 31, and one
// bit-packed group of the eight ids, the least significant bit first.
func pqIndexPacked(ids [8]uint32) []byte {
	b := []byte{31, 1<<1 | 1}
	var acc uint64
	bits := 0
	for _, id := range ids {
		acc |= uint64(id) << bits
		for bits += 31; bits >= 8; bits -= 8 {
			b = append(b, byte(acc))
			acc >>= 8
		}
	}
	return b
}

// past are k's forged files of step 517: a dictionary of two values and a data
// page whose indices say 2^30, in an RLE run, and in a bit-packed run at the last
// row with a value.
func (k pqIndexKind) past() (rle, packed []byte) {
	var ids [8]uint32
	for i := range k.present {
		ids[i] = 1
	}
	ids[k.present-1] = 1 << 30
	return k.file(k.dict(), k.data(pqRLEDict, pqIndexRun(k.present, 1<<30))),
		k.file(k.dict(), k.data(pqRLEDict, pqIndexPacked(ids)))
}

// pqForgedSeeds are the forged files the Parquet target starts from, each honest
// version among them: a data page claiming 2^28 bytes uncompressed, and one
// claiming 2^26 compressed, arrow-go's limits (D14 of v0.7.2-plan.md); a dictionary
// page claiming 2^31−1 values, read by the reader's own pages and, its footer listing
// an encoding they do not read, by arrow-go's; a footer whose list of row groups
// claims 10^8; a column chunk claiming 2^40 bytes; and step 517's, a dictionary
// index of 2^30 in an RLE run and in a bit-packed one, in each kind of chunk
// arrow-go decodes (pqIndexKinds).
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
	seeds := [][]byte{
		pqForged{phys: pqInt64, rows: int64(len(vals)), pages: []pqPage{page}}.bytes(),
		claims(func(p *pqPage) { p.uncompressed = 1 << 28 }),
		claims(func(p *pqPage) { p.compressed = 1 << 26 }),
		pqForged{phys: pqByteArray, rows: 5, pages: dict}.bytes(),
		pqForged{phys: pqByteArray, rows: 5, pages: counted}.bytes(),
		pqForged{phys: pqByteArray, rows: 5, pages: counted, encs: []int32{pqDeltaLength}}.bytes(),
		pqForged{phys: pqInt64, rows: int64(len(vals)), pages: []pqPage{page}, groups: 100_000_000}.bytes(),
		pqForged{phys: pqInt64, rows: int64(len(vals)), pages: []pqPage{page}, chunk: 1 << 40}.bytes(),
	}
	for _, k := range pqIndexKinds() {
		rle, packed := k.past()
		seeds = append(seeds, rle, packed)
	}
	return seeds
}

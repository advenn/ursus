package readfuzz_test

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/bits"
	"slices"
	"strings"
)

// Forged Parquet files (step 510): what no writer makes, a page header or a footer
// claiming what the bytes do not hold. A claim larger than the field it replaces
// takes more varint bytes, so a written file cannot be patched in place; these are
// built whole, with a writer of Thrift's compact protocol and a file of one REQUIRED
// column, uncompressed unless a codec is given (step 520), in one row group.

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
	pqInt32     = 1
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
	pqDeltaBytes  = 7 // DELTA_BYTE_ARRAY
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
	empty        bool   // the header declares 0 bytes uncompressed, whatever uncompressed says (step 520)
	count        []byte // a dictionary header's count as written, in place of values' varint (step 520)
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
// optional, its leaf where nest puts it, its pages as given, under codec, in one
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
	codec    int32   // the chunk's codec, zero for UNCOMPRESSED: each page's body is as it compresses (step 520)

	// children are num_children claims by a schema element's place in the footer's
	// list, the root 0, in place of what the elements hold, a leaf's none (step 533).
	children map[int]int32
}

func (f pqForged) bytes() []byte {
	b := []byte("PAR1")
	start := int64(len(b))
	dataAt, dictAt := int64(-1), int64(-1)
	var unc int64 // the chunk's bytes uncompressed: its headers and what each declares
	for _, p := range f.pages {
		at := int64(len(b))
		size := func(claim int32) int32 {
			if claim == 0 {
				return int32(len(p.body))
			}
			return claim
		}
		declared := size(p.uncompressed)
		if p.empty {
			declared = 0
		}
		var h thriftCompact
		h.begin()
		h.i32(1, p.typ)
		h.i32(2, declared)
		h.i32(3, size(p.compressed))
		if p.typ == pqPageDict {
			h.sub(7)
			if p.count != nil {
				h.field(1, thriftI32)
				h.b = append(h.b, p.count...)
			} else {
				h.i32(1, p.values)
			}
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
		unc += int64(len(h.b)) + int64(declared)
	}
	chunk := int64(len(b)) - start
	if f.codec == 0 {
		unc = chunk
	}
	encodings := []int32{pqPlain, pqRLE}
	if dictAt >= 0 {
		encodings = append(encodings, pqRLEDict)
	}
	encodings = append(encodings, f.encs...)

	var w thriftCompact
	// children writes the num_children of the schema's next element, n of a group, or
	// what f.children claims for it.
	el := 0
	children := func(n int32) {
		if c, ok := f.children[el]; ok {
			n = c
		}
		if n != 0 {
			w.i32(5, n)
		}
		el++
	}
	// root writes the schema's root, of one child; group writes a group's
	// SchemaElement: its repetition, name, child and converted_type, zero for none;
	// leaf writes the leaf's.
	root := func() {
		w.begin()
		w.str(4, "schema")
		children(1)
		w.end()
	}
	group := func(rep int32, name string, conv int32) {
		w.begin()
		w.i32(3, rep)
		w.str(4, name)
		children(1)
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
		children(0)
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
		root()
		group(0, "v", pqListType)
		group(2, "list", 0) // REPEATED
		leaf("element")
		path = []string{"v", "list", "element"}
	case pqInStruct:
		w.list(2, thriftStruct, 3)
		root()
		group(0, "v", 0)
		leaf("x")
		path = []string{"v", "x"}
	default:
		w.list(2, thriftStruct, 2)
		root()
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
	w.i32(4, f.codec)
	w.i64(5, f.rows)
	w.i64(6, unc) // total_uncompressed_size
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

// pqFooterOf is file b's footer, the Thrift of its FileMetaData.
func pqFooterOf(b []byte) []byte {
	n := int(binary.LittleEndian.Uint32(b[len(b)-8:]))
	return b[len(b)-8-n : len(b)-8]
}

// pqMagicless is a file of footer f after the first j bytes of the leading magic, f's
// length and the magic after it. Below 4, j leaves the magic no room, as no writer
// does, and the footer's length is still within the file's less 8 (step 521).
func pqMagicless(f []byte, j int) []byte {
	b := append([]byte("PAR1")[:j:j], f...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(f)))
	return append(b, "PAR1"...)
}

// pqHiddenGroups is a file of no column chunk whose footer's list of row groups says
// it holds a binary (step 521). To a walk by the types a list's header gives, its one
// element is a binary of 25 bytes; to a decoder that reads a list's elements as the
// types parquet.thrift gives them, the element is a row group whose first byte, 0x19,
// the binary's length, begins its field 1, its list of column chunks, claiming 10^8.
func pqHiddenGroups() []byte {
	var w thriftCompact
	w.begin()
	w.i32(1, 1) // version
	w.list(2, thriftStruct, 2)
	w.begin()
	w.str(4, "schema")
	w.i32(5, 1) // num_children
	w.end()
	w.begin()
	w.i32(1, pqInt64)
	w.i32(3, 0) // REQUIRED
	w.str(4, "v")
	w.end()
	w.i64(3, 0) // num_rows
	w.list(4, thriftBinary, 1)
	group := binary.AppendUvarint([]byte{0x19, 0xf0 | thriftStruct}, 100_000_000)
	w.b = append(append(w.b, group...), make([]byte, 1+0x19-len(group))...)
	w.end()
	return pqMagicless(w.b, 4)
}

// pqUnclosedSchema is a file of no row groups whose schema does not close (step 533):
// the root and groups−1 groups each claiming every element after it, each the next's
// parent, then leaves REQUIRED INT64 leaves, the last group's children. Each claim is
// within the elements after it, and the group above the last is owed a child more,
// which only a walk to the schema's end finds.
func pqUnclosedSchema(groups, leaves int) []byte {
	n := groups + leaves
	var w thriftCompact
	w.begin()
	w.i32(1, 1) // version
	w.list(2, thriftStruct, n)
	for i := range groups {
		w.begin()
		w.str(4, "g")
		w.i32(5, int32(n-1-i)) // num_children
		w.end()
	}
	for range leaves {
		w.begin()
		w.i32(1, pqInt64)
		w.str(4, "v")
		w.end()
	}
	w.i64(3, 0)                // num_rows
	w.list(4, thriftStruct, 0) // row_groups
	w.end()
	return pqMagicless(w.b, 4)
}

// pqForgedSeeds are the forged files the Parquet target starts from, each honest
// version among them: a data page claiming 2^28 bytes uncompressed, and one claiming
// 2^26 compressed (D14 of v0.7.2-plan.md); a dictionary page claiming 2^31−1 values,
// read by the reader's own pages and, its footer listing an encoding they do not
// read, by arrow-go's; a footer whose list of row groups
// claims 10^8; a column chunk claiming 2^40 bytes; step 517's, a dictionary index
// of 2^30 in an RLE run and in a bit-packed one, in each kind of chunk arrow-go
// decodes (pqIndexKinds); step 520's, a dictionary page's count in a varint of 10
// bytes, read by each decoder (pqLongCounts), pages whose codecs' streams claim or
// hold more than they declare, and honest pages under each codec (pqCodecSeeds); and
// step 521's, the footer of 10^8 row groups leaving the leading magic no room, and a
// list of row groups said to hold a binary, hiding a list of 10^8 column chunks in it
// (pqHiddenGroups); step 531's, each kind of delta-encoded page honest and with a
// count of 0 (pqDeltaKinds); and step 533's, a schema's root claiming 2^31−1
// children, a Struct claiming as many, and a List whose root claims a child more than
// its tree holds.
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
	groups := pqForged{phys: pqInt64, rows: int64(len(vals)), pages: []pqPage{page}, groups: 100_000_000}.bytes()
	seeds := [][]byte{
		pqForged{phys: pqInt64, rows: int64(len(vals)), pages: []pqPage{page}}.bytes(),
		claims(func(p *pqPage) { p.uncompressed = 1 << 28 }),
		claims(func(p *pqPage) { p.compressed = 1 << 26 }),
		pqForged{phys: pqByteArray, rows: 5, pages: dict}.bytes(),
		pqForged{phys: pqByteArray, rows: 5, pages: counted}.bytes(),
		pqForged{phys: pqByteArray, rows: 5, pages: counted, encs: []int32{pqDeltaLength}}.bytes(),
		groups,
		pqForged{phys: pqInt64, rows: int64(len(vals)), pages: []pqPage{page}, chunk: 1 << 40}.bytes(),
	}
	for _, k := range pqIndexKinds() {
		rle, packed := k.past()
		seeds = append(seeds, rle, packed)
	}
	past, _ := pqLongCounts()
	for _, encs := range [][]int32{nil, {pqDeltaLength}} {
		for _, count := range past {
			pages := pqDictPages([]string{"ab", "cd"}, 5)
			pages[0].count = count
			seeds = append(seeds, pqForged{phys: pqByteArray, rows: 5, pages: pages, encs: encs}.bytes())
		}
	}
	seeds = append(seeds, pqCodecSeeds()...)
	seeds = append(seeds, pqMagicless(pqFooterOf(groups), 0), pqHiddenGroups())
	for _, k := range pqDeltaKinds() {
		for _, c := range k.forged() {
			if c.name == "honest" || c.name == "prefix lengths, honest" || strings.HasSuffix(c.name, "a count of 0") {
				seeds = append(seeds, c.b)
			}
		}
	}
	for _, f := range []pqForged{
		{nest: pqFlat, children: map[int]int32{0: math.MaxInt32}},
		{nest: pqInStruct, children: map[int]int32{1: math.MaxInt32}},
		{nest: pqInList, children: map[int]int32{0: 2}},
	} {
		f.phys, f.rows, f.pages = pqInt64, int64(len(vals)), []pqPage{page}
		seeds = append(seeds, f.bytes())
	}
	return seeds
}

// pqDeltaRun is a delta-packed run as DELTA_BINARY_PACKED lays it out, which a
// DELTA_LENGTH_BYTE_ARRAY page's lengths and a DELTA_BYTE_ARRAY page's prefix and
// suffix lengths take too (step 531): its header's claims, and its blocks as written.
type pqDeltaRun struct {
	block, minis, count uint64 // the header's block size, miniblocks a block, and count of values
	first               int64
	blocks              []pqDeltaBlock
}

// pqDeltaBlock is a block of a run: its least delta, its miniblocks' bit widths, and
// the bodies of the miniblocks holding values, each padded to its full count, and how
// many values each holds.
type pqDeltaBlock struct {
	least  int64
	widths []byte
	bodies [][]byte
	held   []int
}

func (r pqDeltaRun) bytes() []byte {
	b := binary.AppendUvarint(nil, r.block)
	b = binary.AppendUvarint(b, r.minis)
	b = binary.AppendUvarint(b, r.count)
	b = binary.AppendVarint(b, r.first) // Go's varint is the format's zigzag
	for _, k := range r.blocks {
		b = binary.AppendVarint(b, k.least)
		b = append(b, k.widths...)
		for _, body := range k.bodies {
			b = append(b, body...)
		}
	}
	return b
}

// pqDeltaOf is vals delta-packed as the format lays them out, in blocks of block
// values in minis miniblocks: each delta taken with wraparound, less its block's
// least, at the width its miniblock's greatest takes, the least significant bit
// first; a miniblock's values past the run's last are 0s, and a miniblock past them
// has a width of 0 and no bytes.
func pqDeltaOf(vals []int64, block, minis int) pqDeltaRun {
	r := pqDeltaRun{block: uint64(block), minis: uint64(minis), count: uint64(len(vals))}
	if len(vals) == 0 {
		return r
	}
	r.first = vals[0]
	per := block / minis
	for at := 1; at < len(vals); at += block {
		deltas := make([]int64, 0, block)
		for i := at; i < min(at+block, len(vals)); i++ {
			deltas = append(deltas, vals[i]-vals[i-1])
		}
		k := pqDeltaBlock{least: slices.Min(deltas), widths: make([]byte, minis)}
		for m := 0; m*per < len(deltas); m++ {
			held := deltas[m*per : min((m+1)*per, len(deltas))]
			packed := make([]uint64, per)
			var top uint64
			for i, d := range held {
				packed[i] = uint64(d - k.least)
				top = max(top, packed[i])
			}
			k.widths[m] = byte(bits.Len64(top))
			k.bodies = append(k.bodies, pqPack(packed, bits.Len64(top)))
			k.held = append(k.held, len(held))
		}
		r.blocks = append(r.blocks, k)
	}
	return r
}

// pqPack lays vals out width bits each, the least significant bit first.
func pqPack(vals []uint64, width int) []byte {
	b := make([]byte, (len(vals)*width+7)/8)
	for i, v := range vals {
		for k := range width {
			if v>>k&1 != 0 {
				at := i*width + k
				b[at/8] |= 1 << (at % 8)
			}
		}
	}
	return b
}

// cut is the length of run r cut short as how says: inside its last block's bit
// widths, after the first; inside the bits of its last miniblock's values, at half
// their bytes; or after them, inside the miniblock's padding.
func (r pqDeltaRun) cut(how string) int {
	full := r.bytes()
	last := r.blocks[len(r.blocks)-1]
	body := last.bodies[len(last.bodies)-1]
	need := (last.held[len(last.held)-1]*int(last.widths[len(last.bodies)-1]) + 7) / 8
	switch how {
	case "widths":
		at := len(full) - len(last.widths)
		for _, b := range last.bodies {
			at -= len(b)
		}
		return at + 1
	case "values":
		return len(full) - len(body) + need/2
	case "padding":
		return len(full) - len(body) + need
	}
	panic("cut: " + how)
}

// pqDeltaRows is the rows of a pqDeltaKind's page: 199 deltas after its first value,
// a block of 128 and one of 71, whose last miniblock holds 7 values and pads 25, and
// after which one holds none.
const pqDeltaRows = 200

// pqDeltaKind is a kind of column chunk arrow-go's column reader decodes a
// delta-encoded data page of (step 531): its file's shape, the encoding of its page
// of pqDeltaRows rows, the levels the page begins with, and the values of its rows
// holding one, integers or strings.
type pqDeltaKind struct {
	name   string
	shape  pqForged
	enc    int32
	levels []byte
	ints   []int64
	strs   []string
	wide   bool // an INT32 whose deltas span more than 32 bits, forged honest only
}

// pqDeltaKinds are an INT64, REQUIRED and OPTIONAL with a null in seven rows; an
// INT32, and one whose rows 40 and 41 are its least and greatest, so its deltas, taken
// as an int64's, span more than 32 bits (deltapage.go); the INT64
// element of a List; the INT64 field of a Struct; a BYTE_ARRAY under
// DELTA_LENGTH_BYTE_ARRAY, REQUIRED and OPTIONAL; and one under DELTA_BYTE_ARRAY,
// whose values share prefixes. Each footer lists its page's encoding, as a writer's
// does: the reader's own pages read none of them.
func pqDeltaKinds() []pqDeltaKind {
	const rows = pqDeltaRows
	// levels is a V1 page's levels, each RLE, its runs after their 4-byte length.
	levels := func(runs ...[]byte) []byte {
		var b []byte
		for _, r := range runs {
			b = append(binary.LittleEndian.AppendUint32(b, uint32(len(r))), r...)
		}
		return b
	}
	run := func(n int, level byte) []byte { return append(binary.AppendUvarint(nil, uint64(n)<<1), level) }
	nulls := func(i int) bool { return i%7 != 3 }
	var defs []byte // an OPTIONAL column's definition levels, a run each stretch of nulls or values
	for i := 0; i < rows; {
		j := i
		for j < rows && nulls(j) == nulls(i) {
			j++
		}
		defs = append(defs, run(j-i, map[bool]byte{true: 1}[nulls(i)])...)
		i = j
	}
	// The integers spread over their type's range at a width that changes every 16
	// rows, and an INT64's rows 40 and 41 its least and greatest, a delta 64 bits wide.
	ints, int32s := make([]int64, rows), make([]int64, rows)
	x := uint64(531)
	for i := range ints {
		x = x*6364136223846793005 + 1442695040888963407
		ints[i], int32s[i] = int64(x)>>(i/16%64), int64(int32(x>>40))
	}
	ints[40], ints[41] = math.MinInt64, math.MaxInt64
	span := slices.Clone(int32s)
	span[40], span[41] = math.MinInt32, math.MaxInt32
	words := []string{"", "a", "MAIL", "TRUCK", "é漢字", "DELIVER IN PERSON"}
	strs, keys := make([]string, rows), make([]string, rows)
	for i := range strs {
		strs[i] = fmt.Sprint(words[i%len(words)], i*i%997)
		keys[i] = fmt.Sprintf("key/%03d/%s", i/9, words[i%len(words)])
	}
	strs[13] = ""
	some := func(vals []int64) []int64 {
		var out []int64
		for i, v := range vals {
			if nulls(i) {
				out = append(out, v)
			}
		}
		return out
	}
	someStrs := func(vals []string) []string {
		var out []string
		for i, v := range vals {
			if nulls(i) {
				out = append(out, v)
			}
		}
		return out
	}
	dbp, dlba, dba := []int32{pqDeltaBinary}, []int32{pqDeltaLength}, []int32{pqDeltaBytes}
	return []pqDeltaKind{
		{name: "INT64", shape: pqForged{phys: pqInt64, encs: dbp}, enc: pqDeltaBinary, ints: ints},
		{name: "an OPTIONAL INT64 with nulls", shape: pqForged{phys: pqInt64, encs: dbp, optional: true},
			enc: pqDeltaBinary, levels: levels(defs), ints: some(ints)},
		{name: "INT32", shape: pqForged{phys: pqInt32, encs: dbp}, enc: pqDeltaBinary, ints: int32s},
		{name: "an INT32 spanning its range", shape: pqForged{phys: pqInt32, encs: dbp}, enc: pqDeltaBinary,
			ints: span, wide: true},
		{name: "the INT64 element of a List", shape: pqForged{phys: pqInt64, encs: dbp, nest: pqInList},
			enc: pqDeltaBinary, levels: levels(run(rows, 0), run(rows, 1)), ints: ints},
		{name: "the INT64 field of a Struct", shape: pqForged{phys: pqInt64, encs: dbp, nest: pqInStruct},
			enc: pqDeltaBinary, ints: ints},
		{name: "BYTE_ARRAY, DELTA_LENGTH_BYTE_ARRAY", shape: pqForged{phys: pqByteArray, encs: dlba},
			enc: pqDeltaLength, strs: strs},
		{name: "an OPTIONAL BYTE_ARRAY with nulls, DELTA_LENGTH_BYTE_ARRAY",
			shape: pqForged{phys: pqByteArray, encs: dlba, optional: true}, enc: pqDeltaLength,
			levels: levels(defs), strs: someStrs(strs)},
		{name: "BYTE_ARRAY, DELTA_BYTE_ARRAY", shape: pqForged{phys: pqByteArray, encs: dba}, enc: pqDeltaBytes,
			strs: keys},
	}
}

// present is the values of k's page, its rows that hold one.
func (k pqDeltaKind) present() int { return len(k.ints) + len(k.strs) }

// file is k's file of one data page.
func (k pqDeltaKind) file(p pqPage) []byte {
	f := k.shape
	f.rows, f.pages = pqDeltaRows, []pqPage{p}
	return f.bytes()
}

// plain is k's page of its values PLAIN.
func (k pqDeltaKind) plain() pqPage {
	body := slices.Clone(k.levels)
	for _, v := range k.ints {
		if k.shape.phys == pqInt32 {
			body = binary.LittleEndian.AppendUint32(body, uint32(v))
		} else {
			body = binary.LittleEndian.AppendUint64(body, uint64(v))
		}
	}
	for _, s := range k.strs {
		body = append(binary.LittleEndian.AppendUint32(body, uint32(len(s))), s...)
	}
	return pqPage{typ: pqPageData, values: pqDeltaRows, encoding: pqPlain, body: body}
}

// runs are the values each of k's delta-packed runs packs, in the order its page
// holds them, and the bytes after them: a DELTA_BINARY_PACKED page's integers; a
// DELTA_LENGTH_BYTE_ARRAY page's lengths, then its strings; a DELTA_BYTE_ARRAY page's
// lengths of the prefix each string shares with the one before, then of the suffix
// after it, then the suffixes.
func (k pqDeltaKind) runs() (runs [][]int64, tail []byte) {
	switch k.enc {
	case pqDeltaBinary:
		return [][]int64{k.ints}, nil
	case pqDeltaLength:
		var lens []int64
		for _, s := range k.strs {
			lens, tail = append(lens, int64(len(s))), append(tail, s...)
		}
		return [][]int64{lens}, tail
	}
	var prefixes, suffixes []int64
	prev := ""
	for _, s := range k.strs {
		p := 0
		for p < min(len(s), len(prev)) && s[p] == prev[p] {
			p++
		}
		prefixes, suffixes, tail = append(prefixes, int64(p)), append(suffixes, int64(len(s)-p)), append(tail, s[p:]...)
		prev = s
	}
	return [][]int64{prefixes, suffixes}, tail
}

// pqDeltaForgery is a change to one run of a delta-encoded page (step 531): the
// layout its values are packed in, a change to its header or its bit widths, or a
// cut, the page ending inside the run; and whether the reader refuses the page, of n
// values of which present are not null.
type pqDeltaForgery struct {
	name         string
	block, minis int // the layout; zero is the one arrow-go and pyarrow write, 4 miniblocks of 32
	change       func(r *pqDeltaRun, n, present int)
	cut          string // "widths", "values" or "padding": pqDeltaRun.cut
	pageValues   int32  // the page header's count of values, past its column chunk's; zero is the rows'
	refused      func(n, present int) bool
}

// pqDeltaForgeries are the changes: layouts the format allows; a miniblock past the
// last value giving a width of 255, which the format says readers must accept; each
// field of the header wrong, a block of 31 values among them; a block in miniblocks
// of 16 values and one of 64 values, which the format does not allow; a miniblock
// holding values 65 bits wide; a count of 0, short by one, past the values by one,
// past the page's values, and 2^40; and the page ending inside a block's bit widths,
// inside a miniblock's values, and inside its padding. Then two runs the format allows
// whose claims no page's bytes bound, which the reader refuses: one miniblock of 2^31
// values 0 bits wide, and a page claiming 2^26 values, past its column chunk's, its
// run one such miniblock of them all.
func pqDeltaForgeries() []pqDeltaForgery {
	yes := func(int, int) bool { return true }
	no := func(int, int) bool { return false }
	header := func(name string, change func(r *pqDeltaRun, n, present int)) pqDeltaForgery {
		return pqDeltaForgery{name: name, change: change, refused: yes}
	}
	return []pqDeltaForgery{
		{name: "honest", refused: no},
		{name: "honest, one miniblock of 128", block: 128, minis: 1, refused: no},
		{name: "honest, blocks of 256 in 8 miniblocks", block: 256, minis: 8, refused: no},
		{name: "honest, a miniblock holding no value 255 bits wide", refused: no,
			change: func(r *pqDeltaRun, _, _ int) {
				last := &r.blocks[len(r.blocks)-1]
				for m := len(last.bodies); m < len(last.widths); m++ {
					last.widths[m] = 255
				}
			}},
		header("a block of 31 values", func(r *pqDeltaRun, _, _ int) { r.block = 31 }),
		header("a block of 0 values", func(r *pqDeltaRun, _, _ int) { r.block = 0 }),
		header("a block of 2^32 values", func(r *pqDeltaRun, _, _ int) { r.block = 1 << 32 }),
		{name: "blocks of 64 in 2 miniblocks", block: 64, minis: 2, refused: yes},
		header("no miniblock", func(r *pqDeltaRun, _, _ int) { r.minis = 0 }),
		header("3 miniblocks to a block of 128", func(r *pqDeltaRun, _, _ int) { r.minis = 3 }),
		{name: "8 miniblocks of 16 values", block: 128, minis: 8, refused: yes},
		header("256 miniblocks to a block of 128", func(r *pqDeltaRun, _, _ int) { r.minis = 256 }),
		header("a miniblock 65 bits wide", func(r *pqDeltaRun, _, _ int) { r.blocks[0].widths[0] = 65 }),
		header("a count of 0", func(r *pqDeltaRun, _, _ int) { r.count = 0 }),
		header("a count one short", func(r *pqDeltaRun, _, present int) { r.count = uint64(present - 1) }),
		{name: "a count one past the values", refused: func(n, present int) bool { return present+1 > n },
			change: func(r *pqDeltaRun, _, present int) { r.count = uint64(present + 1) }},
		header("a count one past the page's values", func(r *pqDeltaRun, n, _ int) { r.count = uint64(n + 1) }),
		header("a count of 2^40", func(r *pqDeltaRun, _, _ int) { r.count = 1 << 40 }),
		{name: "the page ending inside a block's bit widths", cut: "widths", refused: yes},
		{name: "the page ending inside a miniblock's values", cut: "values", refused: yes},
		{name: "the page ending inside a miniblock's padding", cut: "padding", refused: yes},
		header("a miniblock of 2^31 values 0 bits wide", func(r *pqDeltaRun, _, _ int) {
			r.block, r.minis, r.blocks = 1<<31, 1, []pqDeltaBlock{{widths: []byte{0}}}
		}),
		{name: "a page claiming 2^26 values, past its chunk's, in a miniblock 0 bits wide", pageValues: 1 << 26,
			refused: yes, change: func(r *pqDeltaRun, _, _ int) {
				r.block, r.minis, r.count, r.blocks = 1<<26, 1, 1<<26, []pqDeltaBlock{{widths: []byte{0}}}
			}},
	}
}

// pqDeltaCase is one forged file of a kind: its name, its bytes, whether the reader
// refuses it, the forged run's count of values and values a miniblock, and whether it
// is of a kind whose INT32 deltas span more than 32 bits.
type pqDeltaCase struct {
	name       string
	b          []byte
	refused    bool
	count, per uint64
	wide       bool
}

// forged are k's files of each forgery, applied to each of its runs: a
// DELTA_BYTE_ARRAY page's prefix lengths and suffix lengths in turn. A kind forged
// honest only has the first.
func (k pqDeltaKind) forged() []pqDeltaCase {
	vals, tail := k.runs()
	var out []pqDeltaCase
	forgeries := pqDeltaForgeries()
	if k.wide {
		forgeries = forgeries[:1]
	}
	for which := range vals {
		for _, f := range forgeries {
			runs := make([]pqDeltaRun, len(vals))
			for i, v := range vals {
				block, minis := 128, 4
				if i == which && f.block != 0 {
					block, minis = f.block, f.minis
				}
				runs[i] = pqDeltaOf(v, block, minis)
			}
			if f.change != nil {
				f.change(&runs[which], pqDeltaRows, k.present())
			}
			body := slices.Clone(k.levels)
			for i, r := range runs {
				b := r.bytes()
				if i == which && f.cut != "" {
					b = b[:r.cut(f.cut)]
				}
				body = append(body, b...)
				if i == which && f.cut != "" {
					break // the page ends there
				}
			}
			if f.cut == "" {
				body = append(body, tail...)
			}
			name := f.name
			if len(vals) > 1 {
				name = []string{"prefix lengths, ", "suffix lengths, "}[which] + name
			}
			values := int32(pqDeltaRows)
			if f.pageValues != 0 {
				values = f.pageValues
			}
			var per uint64
			if r := runs[which]; r.minis > 0 {
				per = r.block / r.minis
			}
			out = append(out, pqDeltaCase{name: name, refused: f.refused(pqDeltaRows, k.present()),
				b:     k.file(pqPage{typ: pqPageData, values: values, encoding: k.enc, body: body}),
				count: runs[which].count, per: per, wide: k.wide})
		}
	}
	return out
}

// pqSlug is s as a file name's part: lower case, each run of other than letters and
// digits a hyphen.
func pqSlug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

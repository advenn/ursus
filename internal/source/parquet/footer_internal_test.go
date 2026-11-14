package parquet

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/apache/arrow-go/v18/parquet/compress"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/metadata"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// The footer walk's tests (step 521): the walk against what writers write and
// arrow-go reads, its counts against the bytes, and its reading of a list against
// arrow-go's.

// tval is a Thrift value as the compact protocol writes it, for footers built here.
type tval struct {
	typ    byte     // its compact type: 1 and 2 a Boolean's, true and false
	i      int64    // an integer's
	f      float64  // a double's
	s      string   // a binary's
	fields []tfield // a struct's
	et     byte     // a list's elements' type, as its header gives it
	elems  []tval   // a list's
	raw    []byte   // where set, the value's bytes as they are
}

type tfield struct {
	id int16
	v  tval
}

func ti32(v int64) tval                 { return tval{typ: thriftI32, i: v} }
func ti64(v int64) tval                 { return tval{typ: thriftI64, i: v} }
func tbin(s string) tval                { return tval{typ: thriftBinary, s: s} }
func tdouble(f float64) tval            { return tval{typ: 7, f: f} }
func tstruct(fs ...tfield) tval         { return tval{typ: thriftStruct, fields: fs} }
func tlist(et byte, elems ...tval) tval { return tval{typ: thriftList, et: et, elems: elems} }
func tf(id int16, v tval) tfield        { return tfield{id, v} }

func tbool(b bool) tval {
	if b {
		return tval{typ: 1}
	}
	return tval{typ: 2}
}

func zigzag(v int64) uint64 { return uint64(v<<1) ^ uint64(v>>63) }

// write appends v; a Boolean field's value is its header's type, an element's a byte.
func (v tval) write(b []byte, field bool) []byte {
	if v.raw != nil {
		return append(b, v.raw...)
	}
	switch v.typ {
	case 1, 2:
		if !field {
			b = append(b, v.typ)
		}
	case thriftI32, thriftI64:
		b = binary.AppendUvarint(b, zigzag(v.i))
	case 7:
		b = binary.LittleEndian.AppendUint64(b, math.Float64bits(v.f))
	case thriftBinary:
		b = append(binary.AppendUvarint(b, uint64(len(v.s))), v.s...)
	case thriftList:
		if len(v.elems) < 15 {
			b = append(b, byte(len(v.elems))<<4|v.et)
		} else {
			b = binary.AppendUvarint(append(b, 0xf0|v.et), uint64(len(v.elems)))
		}
		for _, e := range v.elems {
			b = e.write(b, false)
		}
	case thriftStruct:
		var last int16
		for _, f := range v.fields {
			if d := f.id - last; d > 0 && d <= 15 {
				b = append(b, byte(d)<<4|f.v.typ)
			} else {
				b = binary.AppendUvarint(append(b, f.v.typ), zigzag(int64(f.id)))
			}
			last = f.id
			b = f.v.write(b, true)
		}
		b = append(b, 0)
	default:
		panic(fmt.Sprintf("tval: type %d", v.typ))
	}
	return b
}

func (v tval) bytes() []byte { return v.write(nil, false) }

// with is v with its value at path w: a struct's field by its id, a list's element
// by its index.
func (v tval) with(path []int16, w tval) tval {
	if len(path) == 0 {
		return w
	}
	switch v.typ {
	case thriftStruct:
		v.fields = slices.Clone(v.fields)
		for i, f := range v.fields {
			if f.id == path[0] {
				v.fields[i].v = f.v.with(path[1:], w)
				return v
			}
		}
	case thriftList:
		v.elems = slices.Clone(v.elems)
		v.elems[path[0]] = v.elems[path[0]].with(path[1:], w)
		return v
	}
	panic(fmt.Sprintf("tval: nothing at %v", path))
}

// lists is every list in v and where it is, its field's id last.
func (v tval) lists(path []int16, out map[string][]int16) {
	switch v.typ {
	case thriftList:
		out[fmt.Sprint(path)] = slices.Clone(path)
		for i, e := range v.elems {
			e.lists(append(path, int16(i)), out)
		}
	case thriftStruct:
		for _, f := range v.fields {
			f.v.lists(append(path, f.id), out)
		}
	}
}

// everyList is a footer holding every list footerFields names, each of two elements
// or more, in a file of one row group of two INT64 column chunks: what no writer
// writes all of, a column's own key-value metadata, its geospatial statistics and
// the key that encrypts it among them.
func everyList() tval {
	kv := func(k string) tval { return tstruct(tf(1, tbin(k)), tf(2, tbin("v"))) }
	chunk := func(name string, at int64) tval {
		return tstruct(
			tf(2, ti64(at)), // file_offset
			tf(3, tstruct( // meta_data
				tf(1, ti32(2)), // INT64
				tf(2, tlist(thriftI32, ti32(0), ti32(3), ti32(8))),
				tf(3, tlist(thriftBinary, tbin(name))),
				tf(4, ti32(0)), // UNCOMPRESSED
				tf(5, ti64(2)), tf(6, ti64(16)), tf(7, ti64(16)),
				tf(8, tlist(thriftStruct, kv("a"), kv("b"))),
				tf(9, ti64(at)),
				tf(12, tstruct(tf(5, tbin("\x01\x00\x00\x00\x00\x00\x00\x00")), tf(6, tbin("\x02\x00\x00\x00\x00\x00\x00\x00")))),
				tf(13, tlist(thriftStruct, tstruct(tf(1, ti32(0)), tf(2, ti32(0)), tf(3, ti32(1))),
					tstruct(tf(1, ti32(2)), tf(2, ti32(0)), tf(3, ti32(1))))),
				tf(16, tstruct(tf(1, ti64(0)), tf(2, tlist(thriftI64, ti64(2), ti64(0))),
					tf(3, tlist(thriftI64, ti64(0), ti64(2))))),
				tf(17, tstruct(tf(1, tstruct(tf(1, tdouble(0)), tf(2, tdouble(1)), tf(3, tdouble(0)), tf(4, tdouble(1)))),
					tf(2, tlist(thriftI32, ti32(1), ti32(2))))),
			)),
			tf(8, tstruct(tf(2, tstruct(tf(1, tlist(thriftBinary, tbin(name), tbin(name))), tf(2, tbin("key")))))),
		)
	}
	sorting := func(col int64, desc bool) tval {
		return tstruct(tf(1, ti32(col)), tf(2, tbool(desc)), tf(3, tbool(!desc)))
	}
	order := tstruct(tf(1, tstruct()))
	return tstruct(
		tf(1, ti32(2)), // version
		tf(2, tlist(thriftStruct,
			tstruct(tf(4, tbin("schema")), tf(5, ti32(2))),
			tstruct(tf(1, ti32(2)), tf(3, ti32(0)), tf(4, tbin("a"))),
			tstruct(tf(1, ti32(2)), tf(3, ti32(1)), tf(4, tbin("b"))))),
		tf(3, ti64(2)), // num_rows
		tf(4, tlist(thriftStruct, tstruct(
			tf(1, tlist(thriftStruct, chunk("a", 4), chunk("b", 20))),
			tf(2, ti64(32)), tf(3, ti64(2)),
			tf(4, tlist(thriftStruct, sorting(0, false), sorting(1, true)))))),
		tf(5, tlist(thriftStruct, kv("c"), kv("d"))),
		tf(6, tbin("footer_internal_test.go")),
		tf(7, tlist(thriftStruct, order, order)),
	)
}

// footerOf is a Parquet file's footer.
func footerOf(t *testing.T, b []byte) []byte {
	t.Helper()
	if len(b) < 12 || string(b[len(b)-4:]) != "PAR1" {
		t.Fatalf("not a Parquet file of a plain footer")
	}
	n := int(binary.LittleEndian.Uint32(b[len(b)-8:]))
	return b[len(b)-8-n : len(b)-8]
}

// ursusFile is a random file ursus's writer writes: one to six columns of flat and
// nested types, nulls among them, in row groups of 1 to 16 rows under a random codec,
// with statistics or without.
func ursusFile(t *testing.T, rng *rand.Rand) []byte {
	t.Helper()
	pair := dtype.Struct(dtype.Of("a", dtype.Int64), dtype.Of("b", dtype.String))
	types := []dtype.DataType{dtype.Int64, dtype.Int32, dtype.Float64, dtype.Bool, dtype.String, dtype.Date,
		dtype.Datetime(dtype.Micro, ""), dtype.List(dtype.Int64), pair}
	rows := rng.IntN(40)
	var fields []dtype.Field
	var cols []*data.Column
	for c := range 1 + rng.IntN(6) {
		dt := types[rng.IntN(len(types))]
		name := fmt.Sprint("c", c)
		fields = append(fields, dtype.Of(name, dt))
		cols = append(cols, randomColumn(rng, name, dt, rows))
	}
	s, err := dtype.NewSchema(fields...)
	if err != nil {
		t.Fatal(err)
	}
	b, err := data.NewBatch(s, cols)
	if err != nil {
		t.Fatal(err)
	}
	codecs := []compress.Compression{compress.Codecs.Uncompressed, compress.Codecs.Snappy,
		compress.Codecs.Zstd, compress.Codecs.Gzip}
	var buf bytes.Buffer
	w, err := NewWriter(&buf, s, WriteOptions{Compression: codecs[rng.IntN(len(codecs))],
		RowGroupRows: 1 + rng.IntN(16), Stats: rng.IntN(4) != 0})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteBatch(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// randomColumn is n random values of dt, a value in four null.
func randomColumn(rng *rand.Rand, name string, dt dtype.DataType, n int) *data.Column {
	vb := bitmap.NewBuilder(n)
	for range n {
		vb.Append(rng.IntN(4) != 0)
	}
	valid := vb.Finish()
	switch dt.ID() {
	case dtype.TypeInt64, dtype.TypeDatetime:
		v := make([]int64, n)
		for i := range v {
			v[i] = rng.Int64N(1 << 50)
		}
		return data.NewFixed(name, dt, v, valid)
	case dtype.TypeInt32, dtype.TypeDate:
		v := make([]int32, n)
		for i := range v {
			v[i] = rng.Int32N(40_000)
		}
		return data.NewFixed(name, dt, v, valid)
	case dtype.TypeFloat64:
		v := make([]float64, n)
		for i := range v {
			v[i] = rng.NormFloat64()
		}
		return data.NewFixed(name, dt, v, valid)
	case dtype.TypeBool:
		bits := bitmap.NewBuilder(n)
		for range n {
			bits.Append(rng.IntN(2) == 0)
		}
		return data.NewBool(name, bits.Finish(), valid)
	case dtype.TypeString:
		v := make([]string, n)
		for i := range v {
			v[i] = fmt.Sprint("s", rng.IntN(10))
		}
		return data.NewString(name, v, valid)
	case dtype.TypeList:
		offs := make([]int32, n+1)
		for i := range n {
			offs[i+1] = offs[i] + rng.Int32N(4)
		}
		return data.NewList(name, offs, randomColumn(rng, "item", dtype.Int64, int(offs[n])), valid)
	case dtype.TypeStruct:
		return data.NewStruct(name, []*data.Column{randomColumn(rng, "a", dtype.Int64, n),
			randomColumn(rng, "b", dtype.String, n)}, valid)
	}
	panic(fmt.Sprint("randomColumn: ", dt))
}

// writersFooters are footers to walk: random files of ursus's writer; the files
// pyarrow 25.0.1 wrote at random, testdata/oracle/footers
// (bench/scripts/oracle/write_footers.py); the Parquet fixtures other engines wrote;
// and everyList.
func writersFooters(t *testing.T) map[string][]byte {
	seed := rand.Uint64()
	t.Logf("ursus's files from seed %d", seed)
	rng := rand.New(rand.NewPCG(seed, 521))
	out := map[string][]byte{"everyList": everyList().bytes()}
	for i := range 25 {
		out[fmt.Sprint("ursus ", i)] = footerOf(t, ursusFile(t, rng))
	}
	var paths []string
	for _, glob := range []string{"oracle/footers/*.parquet", "parquet/*.parquet"} {
		p, err := filepath.Glob(filepath.Join("..", "..", "..", "testdata", glob))
		if err != nil || len(p) == 0 {
			t.Fatalf("testdata/%s: %v, %d files", glob, err, len(p))
		}
		paths = append(paths, p...)
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(p)] = footerOf(t, b)
	}
	return out
}

// listsOf is where each list header the walk reads of footer f lies.
func listsOf(t *testing.T, f []byte) [][2]int {
	t.Helper()
	var at [][2]int
	r := thriftReader{b: f, onList: func(start, end int) { at = append(at, [2]int{start, end}) }}
	if err := r.walk(fileMetaData, 0); err != nil {
		t.Fatalf("walking the footer: %v", err)
	}
	return at
}

// withHeader is footer f with the list header at h a list of count elements of type et.
func withHeader(f []byte, h [2]int, et byte, count uint64) []byte {
	var hdr []byte
	if count < 15 {
		hdr = []byte{byte(count)<<4 | et}
	} else {
		hdr = binary.AppendUvarint([]byte{0xf0 | et}, count)
	}
	return slices.Concat(f[:h[0]], hdr, f[h[1]:])
}

// TestFooterWalkDifferential (step 521): the footer walk against arrow-go, over
// footers writers wrote (writersFooters). Each is one arrow-go reads, and the walk
// passes it. Each strict prefix of one the walk refuses, as arrow-go does. Each list
// in one, claiming one element more than the bytes after its header, the walk
// refuses at its header, as arrow-go does once it has read them; claiming 10^8, the
// walk refuses at its header, before anything is sized by the count. And each list
// whose header says its elements are of another type the walk passes, as arrow-go
// reads it, its elements read as parquet.thrift types them; or as arrow-go refuses it
// (step 538), its Thrift holding a list's count to the bytes after its header at the
// least an element of the type its header gives takes.
//
// And its schema's elements (step 533): the footer read back (tparse) and written
// again, the walk passes it and arrow-go reads it. Each element claiming a child
// more than it does, the walk refuses, as arrow-go does, its tree no longer closing
// within the elements; claiming one more than the elements after it, or 2^31−1, the
// walk refuses at that element, before anything is sized by the claim.
func TestFooterWalkDifferential(t *testing.T) {
	stride := 1
	if raceDetector {
		stride = 7
	}
	footers := writersFooters(t)
	var prefixes, lists int
	defer func() { t.Logf("%d footers, %d prefixes, %d lists", len(footers), prefixes, lists) }()
	for name, f := range footers {
		t.Run(name, func(t *testing.T) {
			if _, err := metadata.NewFileMetaData(f, nil); err != nil {
				t.Fatalf("arrow-go does not read the footer: %v", err)
			}
			if _, err := schemaDepth(f); err != nil {
				t.Fatalf("the walk refuses the footer: %v", err)
			}
			for k := 0; k < len(f); k += stride {
				prefixes++
				_, err := schemaDepth(f[:k])
				_, errArrow := metadata.NewFileMetaData(f[:k], nil)
				if err == nil || errArrow == nil {
					t.Errorf("its first %d bytes of %d: the walk answers %v, arrow-go %v", k, len(f), err, errArrow)
				}
			}
			at := listsOf(t, f)
			if len(at) == 0 {
				t.Fatal("the walk read no list")
			}
			for _, h := range at {
				lists++
				et := f[h[0]] & 0x0f
				past := withHeader(f, h, et, uint64(len(f)-h[1]+1))
				_, err := schemaDepth(past)
				_, errArrow := metadata.NewFileMetaData(past, nil)
				if !errors.Is(err, errFooterList) || errArrow == nil {
					t.Errorf("the list at %d claiming %d, a byte past its bytes: the walk answers %v, arrow-go %v",
						h[0], len(f)-h[1]+1, err, errArrow)
				}
				if _, err := schemaDepth(withHeader(f, h, et, 100_000_000)); !errors.Is(err, errFooterList) {
					t.Errorf("the list at %d claiming 10^8: the walk answers %v", h[0], err)
				}
				count := uint64(f[h[0]] >> 4)
				if count == 15 {
					count, _ = binary.Uvarint(f[h[0]+1:])
				}
				for other := byte(1); other <= 13; other++ {
					if other == et {
						continue
					}
					g := withHeader(f, h, other, count)
					_, err := schemaDepth(g)
					_, errArrow := metadata.NewFileMetaData(g, nil)
					if err != nil || errArrow != nil {
						t.Errorf("the list at %d said to hold type %d, not %d: the walk answers %v, arrow-go %v",
							h[0], other, et, err, errArrow)
					}
				}
			}
		})
	}
}

// hidden is a list field id of type et, of one element, which arrow-go reads as
// parquet.thrift types it and a walk by the header's types reads as another, so that
// the bytes after it, which arrow-go reads as the struct's next field, the list again
// claiming count elements, are the element's to that walk:
//   - et a struct: said to hold a binary, its length the first byte of el, a struct
//     arrow-go reads whole, its required fields there, and the list again after it
//     inside the binary;
//   - et an integer: said to hold a binary, to arrow-go its length an integer, its
//     bytes the list again;
//   - et a binary: said to hold a struct, to arrow-go a binary of 8 bytes; to that
//     walk the struct's field 0, a binary holding the list again, then its end.
func hidden(id int16, et byte, el tval, count uint64) []byte {
	// The field's header, its id after it as the compact protocol writes an id not
	// 1 to 15 past the last, and the list's header.
	again := append(binary.AppendUvarint([]byte{thriftList}, zigzag(int64(id))), 0xf0|et)
	again = binary.AppendUvarint(again, count)
	switch et {
	case thriftStruct:
		b := slices.Concat(el.bytes(), again)
		n := int(b[0])
		if n < len(b)-1 {
			panic(fmt.Sprintf("hidden: an element of %d bytes and a first byte of %d", len(b), n))
		}
		return slices.Concat([]byte{0x10 | thriftBinary}, b, make([]byte, 1+n-len(b)))
	case thriftI32, thriftI64:
		return slices.Concat([]byte{0x10 | thriftBinary}, binary.AppendUvarint(nil, uint64(len(again))), again)
	case thriftBinary:
		content := slices.Concat(make([]byte, 6), again)
		return slices.Concat([]byte{0x10 | thriftStruct, thriftBinary, 0}, binary.AppendUvarint(nil, uint64(len(content))),
			content, []byte{0})
	}
	panic(fmt.Sprint("hidden: type ", et))
}

// TestFooterWalkReadsAListAsArrowGoDoes (step 521): each list of everyList replaced
// by one hiding the list again, claiming 10^8 elements, from a walk by the types its
// header gives (hidden): the walk refuses each, reading the element as arrow-go does.
// Claiming 10^5, the walk refuses that too.
func TestFooterWalkReadsAListAsArrowGoDoes(t *testing.T) {
	foot := everyList()
	at := map[string][]int16{}
	foot.lists(nil, at)
	var paths [][]int16
	for _, p := range at {
		if !slices.Equal(p[:min(len(p), 4)], []int16{4, 0, 1, 1}) { // the second column chunk's are the first's
			paths = append(paths, p)
		}
	}
	slices.SortFunc(paths, slices.Compare)
	named := 0
	for _, fields := range footerFields {
		for _, f := range fields {
			if f.list {
				named++
			}
		}
	}
	if len(paths) != named {
		t.Fatalf("everyList holds %d lists apart from its second column chunk's, and footerFields names %d",
			len(paths), named)
	}
	for _, path := range paths {
		list := foot
		for _, k := range path {
			if list.typ == thriftStruct {
				i := slices.IndexFunc(list.fields, func(f tfield) bool { return f.id == k })
				list = list.fields[i].v
			} else {
				list = list.elems[k]
			}
		}
		// A list of structs hides the list again after its first element, or, where
		// that is longer than its first byte says, a row group's and a column chunk's,
		// after the least arrow-go reads as one: its required fields.
		var el tval
		if list.et == thriftStruct {
			el = list.elems[0]
			switch fmt.Sprint(path) {
			case "[4]":
				el = tstruct(tf(2, ti64(0)), tf(3, ti64(0)), tf(1, tlist(thriftStruct)))
			case "[4 0 1]":
				el = tstruct(tf(2, ti64(0)))
			}
		}
		for _, count := range []uint64{100_000_000, 100_000} {
			f := foot.with(path, tval{typ: thriftList, raw: hidden(path[len(path)-1], list.et, el, count)}).bytes()
			if _, err := schemaDepth(f); err == nil {
				t.Errorf("the list at %v hiding itself again, claiming %d: the walk passes it", path, count)
			}
			if count > 100_000 {
				continue
			}
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			metadata.NewFileMetaData(f, nil)
			runtime.ReadMemStats(&after)
			if got := after.TotalAlloc - before.TotalAlloc; got < 4*count {
				t.Errorf("the list at %v hiding itself again, claiming %d: the oracle allocated %d bytes reading it, "+
					"and does not read the hidden list", path, count, got)
			} else {
				t.Logf("the list at %v hiding itself again, claiming %d: the oracle allocated %d bytes", path, count, got)
			}
		}
	}
}

// TestMeasureFooterWalksEveryFooterArrowGoReads (step 521): measureFooter walks every
// footer arrow-go reads, one leaving the leading magic no room too, the footer begun
// 0 to 3 bytes into its file: the walk passes everyList there, and arrow-go reads it;
// its row groups claiming 10^8, the walk refuses it. A footer longer than its file
// less 8 bytes is left to arrow-go, which refuses it.
func TestMeasureFooterWalksEveryFooterArrowGoReads(t *testing.T) {
	fileOf := func(f []byte, j int) []byte {
		return append(binary.LittleEndian.AppendUint32(slices.Concat([]byte("PAR1")[:j], f), uint32(len(f))), "PAR1"...)
	}
	honest := everyList()
	claims := honest.with([]int16{4}, tval{typ: thriftList,
		raw: binary.AppendUvarint([]byte{0xf0 | thriftStruct}, 100_000_000)})
	for j := range 5 {
		ra, err := measureFooter(bytes.NewReader(fileOf(honest.bytes(), j)))
		if _, ok := ra.(*measured); err != nil || !ok {
			t.Errorf("everyList %d bytes into its file: %T, %v; want it walked", j, ra, err)
		} else if _, err := file.NewParquetReader(ra); err != nil {
			t.Errorf("everyList %d bytes into its file: arrow-go refuses it: %v", j, err)
		}
		if _, err := measureFooter(bytes.NewReader(fileOf(claims.bytes(), j))); err == nil {
			t.Errorf("10^8 row groups, the footer %d bytes into its file: the walk passes it", j)
		}
	}
	long := fileOf(honest.bytes(), 4)
	binary.LittleEndian.PutUint32(long[len(long)-8:], uint32(len(long)-7))
	ra, err := measureFooter(bytes.NewReader(long))
	if _, ok := ra.(*bytes.Reader); err != nil || !ok {
		t.Errorf("a footer longer than its file less 8: %T, %v; want it left to arrow-go", ra, err)
	}
	if _, err := file.NewParquetReader(bytes.NewReader(long)); err == nil {
		t.Errorf("a footer longer than its file less 8: arrow-go reads it")
	}
}

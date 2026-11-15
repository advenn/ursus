package parquet

import (
	"bytes"
	"encoding/binary"
	"math/rand/v2"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/compress"
	"github.com/apache/arrow-go/v18/parquet/file"
)

// noisyThrift writes Thrift's compact protocol as a writer may and no writer does:
// each varint padded to more bytes than it needs, an i16's or i32's with bits past its
// 32, a varint of 10 bytes whose last holds bits past 64, a field id in its long form,
// a STOP with its high bits set, and fields arrow-go does not know, a list and a
// binary among them, whose sizes carry bits past 32. Thrift reads each as it reads the
// plain encoding, cut to 32 bits or 64.
type noisyThrift struct {
	b   []byte
	rng *rand.Rand
}

// varint writes v, its bits past 32 replaced by noise where wide, padded to a length
// of up to 10 bytes.
func (w *noisyThrift) varint(v uint64, wide bool) {
	if wide && w.rng.IntN(2) == 0 {
		v = uint64(uint32(v)) | w.rng.Uint64()<<32
	}
	enc := binary.AppendUvarint(nil, v)
	if len(enc) == 10 && w.rng.IntN(2) == 0 {
		enc[9] |= byte(w.rng.IntN(0x80)) // bits past 64, which Thrift drops
	}
	for len(enc) < 10 && w.rng.IntN(3) == 0 {
		enc[len(enc)-1] |= 0x80
		enc = append(enc, 0)
	}
	w.b = append(w.b, enc...)
}

// i32 writes v zigzagged, as a varint whose bits past 32 may be noise.
func (w *noisyThrift) i32(v int32) { w.varint(uint64(uint32(v<<1)^uint32(v>>31)), true) }

// field writes a field header, the id's delta from last or, sometimes or where the
// delta does not fit, the long form.
func (w *noisyThrift) field(last *int16, id int16, typ byte) {
	if d := id - *last; d > 0 && d <= 15 && w.rng.IntN(3) != 0 {
		w.b = append(w.b, byte(d)<<4|typ)
	} else {
		w.b = append(w.b, typ)
		w.i32(int32(id))
	}
	*last = id
}

// stop ends a struct: 0, or a type of 0 under a high nibble Thrift ignores.
func (w *noisyThrift) stop() {
	var h byte
	if w.rng.IntN(2) == 0 {
		h = byte(w.rng.IntN(16)) << 4
	}
	w.b = append(w.b, h)
}

// unknown writes a field arrow-go's PageHeader does not have, which Thrift skips: an
// i32, a binary, or a list of i32s.
func (w *noisyThrift) unknown(last *int16, id int16) {
	switch w.rng.IntN(3) {
	case 0:
		w.field(last, id, 5)
		w.i32(w.rng.Int32())
	case 1:
		w.field(last, id, 8)
		n := w.rng.IntN(5)
		w.varint(uint64(n), true)
		w.b = append(w.b, bytes.Repeat([]byte{'x'}, n)...)
	default:
		w.field(last, id, 9)
		n := w.rng.IntN(20)
		if n < 15 {
			w.b = append(w.b, byte(n)<<4|5)
		} else {
			w.b = append(w.b, 0xf5)
			w.varint(uint64(n), true)
		}
		for range n {
			w.i32(w.rng.Int32())
		}
	}
}

// TestPageHeadersAreReadAsArrowGoReadsThem (step 520, F11 of its record): a
// dictionary page's header, generated in Thrift's compact protocol as a writer may
// write it and none does (noisyThrift), is read by readPageHeader, which
// countDictionaries walks a chunk with (step 510), as arrow-go's page reader reads it:
// wherever arrow-go reads the page, readPageHeader reads its type, its sizes, its
// count of values and its own length alike. A count readPageHeader read otherwise was
// one it did not check, and arrow-go made a slice of it: FuzzOwnPagesMatchArrow found
// a count of 896,532,479 in a varint of 10 bytes, which arrow-go's column reader
// asked 21.5 GB for (testdata/fuzz/FuzzOwnPagesMatchArrow/dictionary-count-in-a-10-byte-varint).
func TestPageHeadersAreReadAsArrowGoReadsThem(t *testing.T) {
	rng := rand.New(rand.NewPCG(520, 11))
	body := []byte("\x02\x00\x00\x00ab\x02\x00\x00\x00cd")
	read, refused := 0, 0
	for range 5_000 {
		w := noisyThrift{rng: rng}
		count := rng.Int32()
		if rng.IntN(4) == 0 {
			count = rng.Int32N(4)
		}
		var last int16
		w.field(&last, 1, 5)
		w.i32(2) // DICTIONARY_PAGE
		w.field(&last, 2, 5)
		w.i32(int32(len(body)))
		w.field(&last, 3, 5)
		w.i32(int32(len(body)))
		if rng.IntN(2) == 0 {
			w.unknown(&last, 4)
		}
		w.field(&last, 7, 12)
		var inner int16
		w.field(&inner, 1, 5)
		w.i32(count)
		w.field(&inner, 2, 5)
		w.i32(0) // PLAIN
		if rng.IntN(3) == 0 {
			w.unknown(&inner, 3+int16(rng.IntN(20)))
		}
		w.stop()
		if rng.IntN(2) == 0 {
			w.unknown(&last, 8+int16(rng.IntN(30)))
		}
		w.stop()
		header := w.b
		b := append(append([]byte(nil), header...), body...)

		stream, err := parquet.NewReaderProperties(nil).GetStream(bytes.NewReader(b), 0, int64(len(b)))
		if err != nil {
			t.Fatal(err)
		}
		pr, err := file.NewPageReader(stream, 1, compress.Codecs.Uncompressed, memory.NewGoAllocator(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if !pr.Next() {
			refused++
			continue
		}
		page, ok := pr.Page().(*file.DictionaryPage)
		if !ok || !bytes.Equal(page.Data(), body) {
			t.Fatalf("header % x: arrow-go read another page, %T", header, pr.Page())
		}
		h, ok := readPageHeader(b)
		if !ok || h.typ != pageDictionary || h.uncompressed != len(body) || h.compressed != len(body) ||
			h.values != int(page.NumValues()) || h.size != len(header) {
			t.Fatalf("header % x: arrow-go reads a dictionary page of %d values, its header %d bytes; "+
				"readPageHeader reads %+v, %v", header, page.NumValues(), len(header), h, ok)
		}
		read++
	}
	t.Logf("%d headers read alike, %d arrow-go refused", read, refused)
	if read < 4_000 {
		t.Errorf("arrow-go read only %d of 5,000 headers", read)
	}
}

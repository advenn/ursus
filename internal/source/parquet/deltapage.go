package parquet

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/file"
)

// A delta-encoded data page's runs, checked against the Parquet format before
// arrow-go decodes them (step 531, F18 of step 521's record).
//
// DELTA_BINARY_PACKED lays a run of integers out as a header, its block size, its
// miniblocks a block, its count of values and its first value, then blocks, each a
// minimum delta, a bit width a miniblock, and the miniblocks holding values, each
// packed at its width and padded to its full count. A DELTA_LENGTH_BYTE_ARRAY page
// packs its values' lengths so, and a DELTA_BYTE_ARRAY page its prefixes' lengths
// and then its suffixes' as a DELTA_LENGTH_BYTE_ARRAY. The format requires a block
// size that is a positive multiple of 128 and miniblocks of a multiple of 32 values
// each; pyarrow 25.0.1 refuses a run that breaks either (testdata/oracle/delta), and
// DuckDB 1.5.5 refused F18's block of 31 values.
//
// arrow-go v18.7.0's deltaBitPackDecoder checks neither, nor the count against its
// page. Its Decode answers at most the header's count of values, and
// columnChunkReader.readBatch loops while the page has values: a REQUIRED column's
// page whose count was short, 0 of its 5 in step 521's input, answered 0 values and
// no error forever, a hang on one goroutine; an OPTIONAL column's page, or a List's,
// read the values it did not answer from whatever its buffer held, with no error. A
// block that is not its miniblocks' whole number of values runs past its bit widths,
// a panic, as does a miniblock of no value. A block of 2^32 values in four
// miniblocks made a slice of a miniblock's 2^30 values before decoding one, 8 GiB, a
// fatal "out of memory" under the fuzz targets' cap. A page that ends inside a
// miniblock reads as 0s past its end, with no error. A DELTA_LENGTH_BYTE_ARRAY page
// makes a slice of the header's count of lengths before it decodes one, so a count
// of 2^40 in a page of 2 KiB was a slice of 4 TiB; and it drops the error of a run it
// cannot decode, reading the lengths it did not get as 0: a miniblock 65 bits wide
// read as other strings, with no error.
//
// So checkedPages reads each delta-packed run of a data page arrow-go decodes before
// arrow-go does, as its decoder reads it, and refuses the page as corrupt where:
//   - the block size is not a positive multiple of 128 that 32 bits hold, as arrow-go
//     keeps a block's count (currentBlockVals) and pyarrow reads the header;
//   - the miniblocks do not divide a block into a multiple of 32 values each;
//   - the count is more than the page's values or its column chunk's, or fewer than
//     its values that are not null, which its definition levels count
//     (presentValues);
//   - a miniblock holds more values than the count and than 65,536 (maxMiniblock);
//   - a miniblock holding a value is wider than 64 bits, which arrow-go refuses as it
//     comes to it and pyarrow at once;
//   - the page ends inside a block's minimum delta or bit widths, or inside a
//     miniblock holding a value, its padding counted.
//
// The last bounds but one are arrow-go's allocations. Its decoder makes a slice of a
// miniblock's values for the first miniblock it decodes (unpackNextMini), and decodes
// every value of each, which a miniblock 0 bits wide holds in no byte: a page of five
// values in one miniblock of 2^31 asked for 16 GiB. A DELTA_LENGTH_BYTE_ARRAY page's
// slice of lengths is the count's size. A page's values are its header's claim, and
// arrow-go takes a page claiming more than its column chunk: the chunk's values, which
// its footer gives and checkRowCounts holds to its row group's rows, bound the count.
// Each allocation is then within what the footer declares, or 512 KiB.
//
// A miniblock that holds no value, at the end of the last block, may give any bit
// width, as the format says readers must accept, and its bytes are not there; it is
// not looked at. An INT32's miniblock up to 64 bits wide is read: arrow-go's writer
// takes an INT32's deltas as an int64's, and writes 33 bits where they span more
// than 32, which arrow-go reads back and pyarrow 25.0.1 fails. A count past the
// page's values pyarrow reads, decoding only the values the page's levels ask for;
// it is refused here, as arrow-go's decoder of lengths sizes a slice by it, and no
// writer counts more values than its page holds.
//
// The runs are read as arrow-go reads them. A DELTA_BINARY_PACKED page's values are
// decoded as its column reader asks for them, its values that are not null, and only
// their blocks are read here; the lengths of the other two are decoded whole, the
// header's count of them, at SetData, and the suffixes' run begins where arrow-go's
// decoder of the prefixes' stopped, at the end of their last miniblock. A header
// arrow-go cannot read, a varint the page ends inside, is left to it: it refuses the
// page.
//
// One pass over a page's definition levels, which presentValues counts, and over its
// runs' headers, a varint and a byte a miniblock, a miniblock skipped whole: no
// value is decoded.

// maxMiniblock is the most values a miniblock holds past its run's count. arrow-go and
// pyarrow put 32 in one, a block of 128 in four miniblocks, as parquet-mr does by
// default; the format sets no bound, and this leaves a writer room.
const maxMiniblock = 1 << 16

// deltaEncoded is whether arrow-go decodes a data page of encoding e, in a column of
// physical type t, with its delta-packed decoder: DELTA_BINARY_PACKED's integers, and
// the lengths a DELTA_LENGTH_BYTE_ARRAY or DELTA_BYTE_ARRAY page packs so. Another
// pairing has no decoder in arrow-go, which panics, and the reader recovers that as
// corrupt (reader.Next).
func deltaEncoded(e parquet.Encoding, t parquet.Type) bool {
	switch e {
	case parquet.Encodings.DeltaBinaryPacked:
		return t == parquet.Types.Int32 || t == parquet.Types.Int64
	case parquet.Encodings.DeltaLengthByteArray, parquet.Encodings.DeltaByteArray:
		return t == parquet.Types.ByteArray
	}
	return false
}

// checkDelta refuses delta-encoded data page p, of encoding e, where one of its runs
// breaks the format (deltaRun). A page arrow-go refuses before it decodes a value,
// levels it cannot read, is left to it.
func (c *checkedPages) checkDelta(p file.Page, e parquet.Encoding) error {
	l, ok := c.levels(p)
	if !ok {
		return nil
	}
	present := c.present(l)
	refuse := func(what, why string) error {
		return fmt.Errorf("corrupt: column %s: a %s page's %s: %s", c.name, e, what, why)
	}
	page := deltaBounds{n: l.n, present: present, chunk: c.chunkValues}
	switch e {
	case parquet.Encodings.DeltaBinaryPacked:
		if _, why := deltaRun(l.data, page, false); why != "" {
			return refuse("values", why)
		}
	case parquet.Encodings.DeltaLengthByteArray:
		if _, why := deltaRun(l.data, page, true); why != "" {
			return refuse("lengths", why)
		}
	case parquet.Encodings.DeltaByteArray:
		end, why := deltaRun(l.data, page, true)
		if why != "" {
			return refuse("prefix lengths", why)
		}
		if end < 0 {
			return nil
		}
		if _, why := deltaRun(l.data[end:], page, true); why != "" {
			return refuse("suffix lengths", why)
		}
	}
	return nil
}

// deltaBounds is what a delta-packed run is checked against: its page's values, those
// of them not null, and its column chunk's values.
type deltaBounds struct {
	n, present int
	chunk      int64
}

// deltaRun reads the delta-packed run at the start of b, of a page bounded by page,
// as arrow-go's deltaBitPackDecoder reads it, and returns where the run ends, or why
// it is refused. It reads the blocks of the header's count of values where whole, as
// arrow-go decodes a run of lengths at SetData, and of the page's values that are not
// null otherwise. end is -1, and why empty, for a header arrow-go cannot read and
// refuses.
func deltaRun(b []byte, page deltaBounds, whole bool) (end int, why string) {
	var h [4]uint64 // the block size, the miniblocks a block, the count, the first value
	at := 0
	for i := range h {
		v, k := binary.Uvarint(b[at:])
		if k <= 0 {
			return -1, ""
		}
		h[i], at = v, at+k
	}
	block, minis, count := h[0], h[1], h[2]
	switch {
	case block == 0 || block%128 != 0 || block > math.MaxUint32:
		return 0, fmt.Sprintf("blocks of %d values, where the format takes a positive multiple of 128", block)
	case minis == 0 || block%minis != 0 || block/minis%32 != 0:
		return 0, fmt.Sprintf("blocks of %d values in %d miniblocks, where the format takes miniblocks of a "+
			"multiple of 32 values", block, minis)
	case count > uint64(page.n):
		return 0, fmt.Sprintf("a count of %d values, more than its page's %d", count, page.n)
	case count > uint64(max(page.chunk, 0)):
		return 0, fmt.Sprintf("a count of %d values, more than its column chunk's %d", count, page.chunk)
	case count < uint64(page.present):
		return 0, fmt.Sprintf("a count of %d values, fewer than the %d its page holds that are not null", count,
			page.present)
	case block/minis > max(count, maxMiniblock):
		return 0, fmt.Sprintf("miniblocks of %d values, more than its count of %d and than %d", block/minis, count,
			maxMiniblock)
	}
	read := uint64(page.present)
	if whole {
		read = count
	}
	per := block / minis
	// The first value is the header's; each delta after it is a miniblock's.
	for left := max(read, 1) - 1; left > 0; {
		_, k := binary.Uvarint(b[at:])
		if k <= 0 {
			return 0, "the page ends inside a block's minimum delta, or it overflows 64 bits"
		}
		at += k
		if uint64(len(b)-at) < minis {
			return 0, "the page ends inside a block's bit widths"
		}
		widths := b[at : at+int(minis)]
		at += int(minis)
		for _, w := range widths {
			if left == 0 {
				break // a miniblock holding no value: any width, and no bytes
			}
			if w > 64 {
				return 0, fmt.Sprintf("a miniblock %d bits wide, past 64", w)
			}
			size := uint64(w) * per / 8 // per is a multiple of 32, so whole bytes
			if uint64(len(b)-at) < size {
				return 0, "the page ends inside a miniblock"
			}
			at += int(size)
			left -= min(left, per)
		}
	}
	return at, ""
}

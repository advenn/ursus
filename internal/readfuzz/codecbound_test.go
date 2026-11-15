package readfuzz_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	pqcompress "github.com/apache/arrow-go/v18/parquet/compress"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/fuzzcheck"
	"github.com/advenn/ursus/internal/source/parquet"
	"github.com/advenn/ursus/ursustest"
)

// Step 520 (F10 of step 517's record): a page whose codec's stream claims or holds
// more than the page declares. arrow-go's page reader decompresses a page into a
// buffer of the size its header declares, and klauspost's Zstandard made a buffer of
// the frame's content size first, 1,211,898,114 bytes for CI's page of 15; its Snappy
// one of the block's length header; a Zstandard frame without a content size grew a
// block at a time; and a page declaring 0 bytes, decompressed into a nil buffer, had
// every codec but LZ4_RAW allocate what its stream held.

// pqCodecRepro is CI's FuzzScanParquet input of run 43639097835 with its last byte
// "1" again (step 517's record, §1 and §7): a Polars file of two columns,
// Zstandard, whose page declares 15 bytes and whose frame claims 1,211,898,114.
const pqCodecRepro = "zstd-frame-claiming-1p2gb"

// corpusFile is the bytes of a fuzz corpus entry of one []byte, as go test writes it.
func corpusFile(t *testing.T, target, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "fuzz", target, name))
	if err != nil {
		t.Fatal(err)
	}
	head, lit, ok := strings.Cut(string(b), "\n")
	lit, ok2 := strings.CutPrefix(strings.TrimSpace(lit), "[]byte(")
	lit, ok3 := strings.CutSuffix(lit, ")")
	if head != "go test fuzz v1" || !ok || !ok2 || !ok3 {
		t.Fatalf("%s is not a corpus entry of one []byte", name)
	}
	s, err := strconv.Unquote(lit)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return []byte(s)
}

// pqCodecKind is where a page of step 520 is read: a flat REQUIRED INT64, which the
// reader's own pages read, and the INT64 element of a List, which arrow-go's column
// reader reads. Both decompress through arrow-go's page reader.
type pqCodecKind struct {
	name string
	k    pqIndexKind
}

// pqCodecKinds are the two kinds, the own pages' first.
func pqCodecKinds() []pqCodecKind {
	list := pqIndexKinds()[2]
	if list.shape.nest != pqInList {
		panic("pqIndexKinds()[2] is not the List's element")
	}
	return []pqCodecKind{
		{"read by the reader's own pages", pqIndexKind{name: "a REQUIRED INT64", shape: pqForged{phys: pqInt64},
			values: list.values, present: pqIndexRows}},
		{"read by arrow-go's column reader", list},
	}
}

// vals is a PLAIN data page's values of kind k: 3 1 4 1 5, one a row.
func (k pqCodecKind) vals() []byte {
	var b []byte
	for i := range k.k.present {
		b = binary.LittleEndian.AppendUint64(b, uint64([]int{3, 1, 4, 1, 5}[i%5]))
	}
	return b
}

// Where pages puts an empty data page, of no values and no bytes, for a column
// without levels: nowhere; first, its buffer nil; or between two data pages, its
// buffer the first's. arrow-go's column reader takes a page of no values for its
// chunk's end, whatever its codec, so it reads none.
const (
	pqNoEmpty = iota
	pqEmptyFirst
	pqEmptyBetween
)

// pages are k's pages: a PLAIN data page of vals, or a dictionary page and a data
// page of its indices, with an empty page where empty says, and a second data page
// after one between.
func (k pqCodecKind) pages(dict bool, empty int) []pqPage {
	var out []pqPage
	if dict {
		out = append(out, k.k.dict())
	}
	data := k.k.data(pqPlain, k.vals())
	if dict {
		data = k.k.data(pqRLEDict, pqIndexRun(k.k.present, 1))
	}
	none := pqPage{typ: pqPageData, encoding: pqPlain, empty: true}
	switch empty {
	case pqEmptyFirst:
		return append(out, none, data)
	case pqEmptyBetween:
		return append(out, data, none, data)
	}
	return append(out, data)
}

// file is k's file of pages under codec, each page's body compressed by enc.
func (k pqCodecKind) file(codec pqcompress.Compression, enc func([]byte) []byte, pages []pqPage) []byte {
	f := k.k.shape
	f.codec = int32(codec)
	for _, p := range pages {
		if codec != pqcompress.Codecs.Uncompressed {
			p.uncompressed, p.body = int32(len(p.body)), enc(p.body)
			p.compressed = int32(len(p.body))
		}
		f.pages = append(f.pages, p)
		if p.typ == pqPageData {
			f.rows += int64(p.values)
		}
	}
	return f.bytes()
}

// pqHonestCodec is one way a writer compresses a page: a codec and its encoding.
type pqHonestCodec struct {
	name  string
	codec pqcompress.Compression
	enc   func([]byte) []byte
}

// pqHonestCodecs are the codecs ursus reads, as arrow-go's codecs encode, and
// Zstandard as a streaming writer frames a page, with no content size, and as two
// frames.
func pqHonestCodecs() []pqHonestCodec {
	var out []pqHonestCodec
	for _, c := range []pqcompress.Compression{pqcompress.Codecs.Snappy, pqcompress.Codecs.Gzip,
		pqcompress.Codecs.Brotli, pqcompress.Codecs.Zstd, pqcompress.Codecs.Lz4Raw} {
		out = append(out, pqHonestCodec{c.String(), c, func(b []byte) []byte { return pqEncode(c, b) }})
	}
	return append(out,
		pqHonestCodec{"ZSTD, a frame without a content size", pqcompress.Codecs.Zstd, zstdStream},
		pqHonestCodec{"ZSTD, two frames", pqcompress.Codecs.Zstd, func(b []byte) []byte {
			return append(zstdStream(b[:len(b)/2]), zstdStream(b[len(b)/2:])...)
		}})
}

// pqEncode is b as arrow-go's codec for c encodes it.
func pqEncode(c pqcompress.Compression, b []byte) []byte {
	codec, err := pqcompress.GetCodec(c)
	if err != nil {
		panic(err)
	}
	return codec.Encode(make([]byte, codec.CompressBound(int64(max(len(b), 1)))), b)
}

// zstdStream is b as klauspost's streaming writer frames it when flushed before its
// end: no content size, which a writer that does not know a page's size leaves out.
func zstdStream(b []byte) []byte {
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf, zstd.WithEncoderConcurrency(1))
	if err == nil {
		_, err = w.Write(b)
	}
	if err == nil {
		err = w.Flush()
	}
	if err == nil {
		err = w.Close()
	}
	if err != nil {
		panic(err)
	}
	out := buf.Bytes()
	if len(out) == 0 {
		return out // no frame: what a page of no bytes compresses to
	}
	if fhd := out[4]; fhd>>6 != 0 || fhd&(1<<5) != 0 {
		panic(fmt.Sprintf("the streaming writer's frame header %#x holds a content size", fhd))
	}
	return out
}

// pqCodecBomb is a stream that claims or holds far more than a page of it declares,
// in its codec.
type pqCodecBomb struct {
	name   string
	codec  pqcompress.Compression
	stream []byte
}

// zstdFrame is a Zstandard frame's header: no content size if fcs is 0, else a 4-byte
// one of fcs; and a window of 128 KiB, a block's most.
func zstdFrame(fcs uint32) []byte {
	b := []byte{0x28, 0xb5, 0x2f, 0xfd, 0x00, (17 - 10) << 3}
	if fcs != 0 {
		b[4] = 2 << 6
		b = binary.LittleEndian.AppendUint32(b, fcs)
	}
	return b
}

// zstdBlock is a block's header: its size, its type (0 raw, 1 RLE) and whether it is
// the frame's last.
func zstdBlock(b []byte, size, typ int, last bool) []byte {
	h := size<<3 | typ<<1
	if last {
		h |= 1
	}
	return append(b, byte(h), byte(h>>8), byte(h>>16))
}

// pqZeros is 1 MiB of zeros, which the gzip and LZ4_RAW bombs compress.
var pqZeros = sync.OnceValue(func() []byte { return make([]byte, 1<<20) })

// pqGzipMember is a gzip member of 1 MiB of zeros at the best compression: 1,052 bytes.
var pqGzipMember = sync.OnceValue(func() []byte {
	var b bytes.Buffer
	w, err := gzip.NewWriterLevel(&b, gzip.BestCompression)
	if err == nil {
		_, err = w.Write(pqZeros())
	}
	if err == nil {
		err = w.Close()
	}
	if err != nil {
		panic(err)
	}
	return b.Bytes()
})

// pqBrotli64 is 64 MiB of zeros as arrow-go's Brotli codec encodes it at quality 5:
// 102 bytes.
var pqBrotli64 = sync.OnceValue(func() []byte {
	codec, err := pqcompress.GetCodec(pqcompress.Codecs.Brotli)
	if err != nil {
		panic(err)
	}
	return codec.EncodeLevel(nil, make([]byte, 64<<20), 5)
})

// pqBrotliWindow is a Brotli stream of 16 MiB of zeros in one meta-block, its window
// 16 MiB, as andybalholm/brotli v1.2.2 writes it at quality 5 and a window of 2^24
// (WriterOptions{Quality: 5, LGWin: 24}): 14 bytes. Its decoder makes a ring buffer
// of 16 MiB for the meta-block before it decodes a byte.
var pqBrotliWindow = []byte{0xcf, 0xff, 0xff, 0x7f, 0x00, 0x24, 0x00, 0xe2, 0xb1, 0x40, 0x72, 0xef, 0xff, 0x07}

// pqCodecBombs are the streams, each of a page holding body:
//   - a Zstandard frame claiming 2^30 bytes, and holding body in a raw block;
//   - a Zstandard frame without a content size of 4,096 RLE blocks of 128 KiB, 512 MiB;
//   - a Snappy block whose length header claims 2^30, and holding body;
//   - 128 gzip members of 1 MiB of zeros each, 128 MiB;
//   - a Brotli stream of 64 MiB of zeros, 102 bytes;
//   - a Brotli stream of 16 MiB in a window of 16 MiB (pqBrotliWindow);
//   - an LZ4_RAW block of 1 MiB of zeros, which LZ4_RAW decoded into its buffer and
//     no further before step 520 as after it.
//
// small makes them seeds' size: 600 RLE blocks, 75 MiB; 4 gzip members; no Brotli of
// 64 MiB.
func pqCodecBombs(body []byte, small bool) []pqCodecBomb {
	blocks, members := 4096, 128
	if small {
		blocks, members = 600, 4
	}
	claim := zstdBlock(zstdFrame(1<<30), len(body), 0, true)
	runs := zstdFrame(0)
	for i := range blocks {
		runs = append(zstdBlock(runs, 128<<10, 1, i == blocks-1), 0)
	}
	honest := pqEncode(pqcompress.Codecs.Snappy, body)
	_, n := binary.Uvarint(honest)
	bombs := []pqCodecBomb{
		{"a Zstandard frame claiming 2^30 bytes", pqcompress.Codecs.Zstd, append(claim, body...)},
		{fmt.Sprintf("a Zstandard frame without a content size, of %d MiB", blocks/8), pqcompress.Codecs.Zstd, runs},
		{"a Snappy block claiming 2^30 bytes", pqcompress.Codecs.Snappy,
			append(binary.AppendUvarint(nil, 1<<30), honest[n:]...)},
		{fmt.Sprintf("gzip of %d MiB", members), pqcompress.Codecs.Gzip, bytes.Repeat(pqGzipMember(), members)},
		{"Brotli in a window of 16 MiB", pqcompress.Codecs.Brotli, pqBrotliWindow},
		{"LZ4_RAW of 1 MiB", pqcompress.Codecs.Lz4Raw, pqEncode(pqcompress.Codecs.Lz4Raw, pqZeros())},
	}
	if !small {
		bombs = append(bombs, pqCodecBomb{"Brotli of 64 MiB", pqcompress.Codecs.Brotli, pqBrotli64()})
	}
	return bombs
}

// pqCodecSeeds are step 520's forged files among FuzzScanParquet's seeds: each small
// bomb in a data page of each kind, declaring its bytes and none, and an honest page
// under each codec, read by the reader's own pages.
func pqCodecSeeds() [][]byte {
	var seeds [][]byte
	kinds := pqCodecKinds()
	for _, k := range kinds {
		for _, bomb := range pqCodecBombs(k.pages(false, pqNoEmpty)[0].body, true) {
			seeds = append(seeds, k.bombFile(bomb, false, false), k.bombFile(bomb, false, true))
		}
	}
	for _, c := range pqHonestCodecs() {
		seeds = append(seeds, kinds[0].file(c.codec, c.enc, kinds[0].pages(false, pqEmptyBetween)))
	}
	return seeds
}

// pqBombFile is k's file whose data page, or dictionary page, is bomb's stream, its
// header declaring the page's PLAIN bytes, or none; the chunk's other pages honest.
func (k pqCodecKind) bombFile(bomb pqCodecBomb, dict, none bool) []byte {
	pages := k.pages(dict, pqNoEmpty)
	f := k.k.shape
	f.codec = int32(bomb.codec)
	for i, p := range pages {
		p.uncompressed, p.body = int32(len(p.body)), pqEncode(bomb.codec, p.body)
		if i == 0 {
			p.body, p.empty = bomb.stream, none
			if none && p.typ == pqPageDict {
				// A dictionary page of 0 bytes says no values, or step 510's count
				// refuses it before its stream is read.
				p.values = 0
			}
		}
		p.compressed = int32(len(p.body))
		f.pages = append(f.pages, p)
		if p.typ == pqPageData {
			f.rows += int64(p.values)
		}
	}
	return f.bytes()
}

// TestAPageDecompressingPastItsSizeAllocatesNothingOfIt (step 520, F10 of step 517's
// record): a page whose codec's stream claims or holds far more than the page's
// header declares, for each codec ursus reads (pqCodecBombs), its header declaring
// the page's bytes or none, as a data page and as a dictionary page, read by the
// reader's own pages and by arrow-go's column reader; and CI's file. Each read
// allocated what the stream claimed or held, up to a gigabyte, before arrow-go
// compared the output with the page; a page declaring none, every codec's stream but
// LZ4_RAW's whole; a Brotli stream, a ring buffer of its window. Each is refused now
// as corrupt, an I/O error, read every way FuzzScanParquet reads an input, within the
// harness's bounds and with no request past the bounded allocator's, the error naming
// the page's declared bytes; LZ4_RAW's is refused as before.
//
// What no reader refuses, each read as the same pages uncompressed: a page under each
// codec as arrow-go's codecs encode it, and Zstandard without a content size and as
// two frames; a dictionary page and its indices; and, for the reader's own pages, an
// empty page, declaring none, before them, its buffer nil, and between two of them,
// its buffer the first's.
func TestAPageDecompressingPastItsSizeAllocatesNothingOfIt(t *testing.T) {
	read := func(t *testing.T, b []byte) (*ursus.DataFrame, error) {
		mem := fuzzcheck.NewAllocator(len(b))
		defer parquet.ReadPagesWith(mem)()
		lim := limits(len(b))
		lim.Mem = mem
		var df *ursus.DataFrame
		var err error
		fuzzcheck.Run(t, lim, func() error {
			df, err = ursus.ScanParquetBytes(b, "forged.parquet").Collect(t.Context())
			return err
		})
		return df, err
	}
	// refused reads b as FuzzScanParquet reads an input, its every way, which reads
	// each page several times.
	refused := func(t *testing.T, b []byte, codec pqcompress.Compression) {
		t.Helper()
		mem := fuzzcheck.NewAllocator(len(b))
		mem.Poison = true
		defer parquet.ReadPagesWith(mem)()
		lim := limits(len(b))
		lim.Mem = mem
		var err error
		fuzzcheck.Run(t, lim, func() error {
			err = readParquet(t, b, mem.Limit)
			return err
		})
		switch {
		case err == nil:
			t.Error("read, and no error")
		case !errors.Is(err, ursus.ErrIO):
			t.Errorf("%v, not an I/O error", err)
		case codec != pqcompress.Codecs.Lz4Raw && !strings.Contains(err.Error(), "decompresses past the"):
			t.Errorf("%v, not an error naming the page's declared bytes", err)
		}
	}

	t.Run("CI's file", func(t *testing.T) {
		refused(t, corpusFile(t, "FuzzScanParquet", pqCodecRepro), pqcompress.Codecs.Zstd)
	})
	for _, k := range pqCodecKinds() {
		t.Run(k.name, func(t *testing.T) {
			for _, dict := range []bool{false, true} {
				page := map[bool]string{false: "a data page", true: "a dictionary page"}[dict]
				for _, bomb := range pqCodecBombs(k.pages(dict, pqNoEmpty)[0].body, false) {
					for _, none := range []bool{false, true} {
						declares := map[bool]string{false: "declaring its bytes", true: "declaring none"}[none]
						t.Run(fmt.Sprintf("%s, %s %s", bomb.name, page, declares), func(t *testing.T) {
							refused(t, k.bombFile(bomb, dict, none), bomb.codec)
						})
					}
				}
			}

			empties := []int{pqNoEmpty}
			if k.k.shape.nest == pqFlat {
				empties = append(empties, pqEmptyFirst, pqEmptyBetween)
			}
			for _, dict := range []bool{false, true} {
				for _, empty := range empties {
					where := []string{"", ", an empty page first", ", an empty page between"}[empty]
					want, err := read(t, k.file(pqcompress.Codecs.Uncompressed, nil, k.pages(dict, empty)))
					if err != nil {
						t.Fatalf("uncompressed, dictionary %v%s: %v", dict, where, err)
					}
					for _, c := range pqHonestCodecs() {
						got, err := read(t, k.file(c.codec, c.enc, k.pages(dict, empty)))
						if err != nil {
							t.Errorf("%s, dictionary %v%s: %v", c.name, dict, where, err)
							continue
						}
						ursustest.AssertFrameEqual(t, got, want)
					}
				}
			}
		})
	}
}

// pqLongCounts are a dictionary page's count written as varints of 10 bytes, each as
// Thrift reads it, cut to 32 bits and zigzagged, and as binary.Uvarint does not:
// FuzzOwnPagesMatchArrow's input of step 520 (F11), which Thrift reads as 896,532,479
// and binary.Uvarint calls an overflow; and 2^31−1 and 2, each padded to 10 bytes
// with bits past 32 that Thrift drops.
func pqLongCounts() (past [][]byte, two []byte) {
	pad := func(v uint64) []byte {
		b := binary.AppendUvarint(nil, v)
		for len(b) < 10 {
			b[len(b)-1] |= 0x80
			b = append(b, 0)
		}
		return b
	}
	zz := func(n int32) uint64 { return uint64(uint32(n<<1) ^ uint32(n>>31)) }
	return [][]byte{
		{0xfe, 0xff, 0xff, 0xd6, 0xd6, 0xd6, 0xd6, 0xd6, 0xd6, 0x0f},
		pad(zz(1<<31-1) | 0x5a5a5a5a<<32),
	}, pad(zz(2) | 0x5a5a5a5a<<32)
}

// TestADictionaryCountInALongVarintIsCountedAsArrowGoReadsIt (step 520, F11 of its
// record): a dictionary page of 12 bytes, two strings, whose header's count is written
// in a varint of 10 bytes (pqLongCounts), read by the reader's own pages and, the
// footer listing an encoding they do not read, by arrow-go's column reader. Step 510's
// walk of a chunk's page headers (countDictionaries) read such a count otherwise than
// Thrift, which arrow-go reads it with, or not at all, and did not count it: arrow-go
// made a slice of 896,532,479 slice headers, 21.5 GB, a fatal "out of memory". Each is
// refused now within the harness's bounds; the count of 2 so written reads as the
// count of 2 written plainly.
func TestADictionaryCountInALongVarintIsCountedAsArrowGoReadsIt(t *testing.T) {
	past, two := pqLongCounts()
	honest := pqDictPages([]string{"ab", "cd"}, 5)
	for _, encs := range [][]int32{nil, {pqDeltaLength}} {
		for _, count := range past {
			pages := append([]pqPage(nil), honest...)
			pages[0].count = count
			b := pqForged{phys: pqByteArray, rows: 5, pages: pages, encs: encs}.bytes()
			fuzzcheck.Run(t, limits(len(b)), func() error {
				df, err := ursus.ScanParquetBytes(b, "forged.parquet").Collect(t.Context())
				if err == nil {
					t.Errorf("listing %v, a count of % x: read as %d rows, and no error", encs, count, df.Height())
				}
				return err
			})
		}
		pages := append([]pqPage(nil), honest...)
		pages[0].count = two
		got, err := ursus.ScanParquetBytes(pqForged{phys: pqByteArray, rows: 5, pages: pages, encs: encs}.bytes(),
			"long.parquet").Collect(t.Context())
		if err != nil {
			t.Fatalf("listing %v, a count of 2 in 10 bytes: %v", encs, err)
		}
		want, err := ursus.ScanParquetBytes(pqForged{phys: pqByteArray, rows: 5, pages: honest, encs: encs}.bytes(),
			"honest.parquet").Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, want)
	}
}

package parquet

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"

	"github.com/apache/arrow-go/v18/parquet/compress"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/snappy"
	"github.com/klauspost/compress/zstd"
)

// A page decompresses to no more than it declares (step 520).
//
// The page reader decompresses each page into a buffer of the size its header
// declares, which boundPages bounds by the file's largest column chunk (D14), and
// refuses a page whose output is another size. A compressed stream makes claims of
// its own, and none of them is to size anything:
//
//   - a Zstandard frame may declare its content size, and klauspost's DecodeAll makes
//     a buffer of it, up to the decoder's limit of 64 GiB, where the buffer it is
//     given is short of it; a frame without a content size grows its output a block
//     at a time, up to that limit;
//   - a Snappy block declares its length in a header, and klauspost's s2.Decode makes
//     a buffer of it, up to 2^32−1, where the buffer it is given is short of it;
//   - a nil buffer is a codec's "allocate what the stream holds", and a page declaring
//     0 bytes can come with one, the buffer of a page reader that has not grown one.
//
// LZ4_RAW decodes into its buffer and no further, nil or not. arrow-go reads neither
// Codecs.Lz4 nor LZO.
//
// # arrow-go's registry
//
// A page reader takes its codec from arrow-go's compress registry, a map of the
// process's, and arrow-go offers no other way in: GetColumnPageReader looks the codec
// up itself, and a page reader made by hand loses the file's page limits and its
// dictionary's offset. So the package's init replaces four of the registry's codecs,
// Snappy, gzip, Brotli and Zstandard, with boundedCodec: the codec that was
// registered, whose decoding into a buffer stops at the buffer and refuses the page as
// corrupt. That is the registry of every program that imports ursus, so each is a
// strict tightening of its codec, by the codec's own reading of a buffer:
//
//   - Zstandard and Snappy decode into the buffer's capacity, as klauspost's do where
//     the output fits it, and refuse a stream claiming more: a frame's content size or
//     a block's length header past the capacity, before anything is allocated, and a
//     frame without one once a block's output passes it;
//   - gzip and Brotli read into the buffer's length and refuse a byte past it, short
//     streams answered as the codec that was registered answers them;
//   - a nil buffer from the page reader is a page of 0 bytes, so it holds nothing;
//     from any other caller it is still "allocate", and the codec that was registered
//     decodes it (calledByPageReader).
//
// One answer differs: a Brotli reader may answer a read of nothing at its stream's
// end, before io.EOF, and past reads again, so an empty page after a page of bytes,
// its buffer not nil, reads as empty.
//
// Encoding, its bound, and a streaming reader or writer are the codec that was
// registered, so every file written is as before, ursus's own writer's among them.
// The page reader requires a page's output to fill its buffer, so a page it read
// before is read now: TestEveryParquetFileReadsAsWithArrowGosCodecs holds the
// fixtures and the files pyarrow, Polars and ursus wrote to that, and
// TestBoundedCodecsDecodeAsTheCodecsTheyReplace each codec on generated streams.
//
// A codec registered for one of the four before this package's init is wrapped, its
// decoding into a buffer replaced by klauspost's, which arrow-go's are; one registered
// after it replaces boundedCodec, and the bound with it.
//
// # What is left
//
// klauspost checks a Zstandard frame without a content size against the buffer once a
// raw or RLE block is appended, so a corrupt one can append one block, at most 128
// KiB, past the buffer's capacity before it is refused, the slice grown once to hold
// it: at most about a quarter more than the page's buffer, and that block. A
// compressed block whose output passes the capacity is refused by klauspost's own
// check, "output bigger than max block size".
// A codec's working memory is its own, bounded by its format: gzip's window of 32 KiB,
// Brotli's Huffman tables, some megabytes at most, and its ring buffer, the window
// brotliWindow lowers.

func init() {
	registerBounded(compress.Codecs.Snappy, "Snappy", decodeSnappy)
	registerBounded(compress.Codecs.Gzip, "gzip", decodeGzip)
	registerBounded(compress.Codecs.Zstd, "Zstandard", decodeZstd)
	if was, err := compress.GetCodec(compress.Codecs.Brotli); err == nil {
		if s, ok := was.(compress.StreamingCodec); ok {
			registerBounded(compress.Codecs.Brotli, "Brotli", func(dst, src []byte) ([]byte, error) {
				return decodeBrotli(s, dst, src)
			})
		}
	}
}

// registerBounded registers boundedCodec for c in place of the codec registered for
// it. One that cannot stream is wrapped as one that cannot: arrow-go's page reader
// asks.
func registerBounded(c compress.Compression, name string, decode func(dst, src []byte) ([]byte, error)) {
	was, err := compress.GetCodec(c)
	if err != nil {
		return
	}
	b := boundedCodec{Codec: was, name: name, decode: decode}
	if s, ok := was.(compress.StreamingCodec); ok {
		compress.RegisterCodec(c, boundedStreaming{b, s})
		return
	}
	compress.RegisterCodec(c, b)
}

// boundedCodec is a codec of arrow-go's registry whose decoding into a buffer stops at
// the buffer (the package's "arrow-go's registry"). Everything else is the codec it
// replaced, Codec.
type boundedCodec struct {
	compress.Codec
	name string
	// decode decodes src into dst, a buffer that is not nil, at most to its length
	// or its capacity as the codec reads a buffer, and refuses more with errPast.
	decode func(dst, src []byte) ([]byte, error)
}

// boundedStreaming is a boundedCodec of a codec that streams.
type boundedStreaming struct {
	boundedCodec
	compress.StreamingCodec
}

var (
	_ compress.ErrorCodec     = boundedCodec{}
	_ compress.StreamingCodec = boundedStreaming{}
)

func (c boundedCodec) Decode(dst, src []byte) []byte {
	out, err := c.DecodeWithError(dst, src)
	if err != nil {
		panic(err)
	}
	return out
}

func (c boundedCodec) DecodeWithError(dst, src []byte) ([]byte, error) {
	if dst == nil {
		if !calledByPageReader() {
			return compress.Decode(c.Codec, nil, src)
		}
		dst = []byte{}
	}
	out, err := c.decode(dst, src)
	if errors.Is(err, errPast) {
		return nil, fmt.Errorf("corrupt: a %s page decompresses past the %d bytes its header declares: %w",
			c.name, len(dst), err)
	}
	return out, err
}

// errPast is a stream that holds more than its buffer.
var errPast = errors.New("more than its buffer holds")

// pageReader begins the name of each method of arrow-go's page reader,
// file.(*serializedPageReader): decompress and readV2Encrypted are arrow-go's only
// callers of a codec's Decode.
const pageReader = "github.com/apache/arrow-go/v18/parquet/file.(*serializedPageReader)."

// calledByPageReader reports whether a codec's DecodeWithError, which calls it, was
// called by arrow-go's page reader. A nil buffer means two things to a codec: to its
// page reader, a page of 0 bytes, whose buffer was never grown (memory.Buffer.Bytes
// of no allocation); to any other caller, as Codec's documentation says, a buffer to
// allocate. Only the caller tells them apart, and this is asked only for a nil buffer.
// arrow-go is pinned (go.mod); TestAPageDecompressingPastItsSizeAllocatesNothingOfIt's
// pages of 0 bytes fail if its page reader is renamed.
func calledByPageReader() bool {
	var pcs [12]uintptr
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs[:])])
	for {
		f, more := frames.Next()
		if strings.HasPrefix(f.Function, pageReader) {
			return true
		}
		if !more {
			return false
		}
	}
}

// decodeSnappy decodes a Snappy block into dst's capacity: klauspost's Decode makes a
// buffer of the block's length header where dst's capacity is short of it.
func decodeSnappy(dst, src []byte) ([]byte, error) {
	n, err := snappy.DecodedLen(src)
	if err != nil {
		return nil, err
	}
	if n > cap(dst) {
		return nil, errPast
	}
	return snappy.Decode(dst, src)
}

// zstdDecoder is klauspost's DecodeAll held to its buffer's capacity: a frame whose
// content size is past it is refused before anything is allocated, and a frame
// without one once a block takes its output past it. Made once, as arrow-go makes its
// own, with the same concurrency.
var zstdDecoder = sync.OnceValues(func() (*zstd.Decoder, error) {
	return zstd.NewReader(nil, zstd.WithDecodeAllCapLimit(true))
})

func decodeZstd(dst, src []byte) ([]byte, error) {
	d, err := zstdDecoder()
	if err != nil {
		return nil, err
	}
	out, err := d.DecodeAll(src, dst[:0])
	if errors.Is(err, zstd.ErrDecoderSizeExceeded) {
		return nil, errPast
	}
	return out, err
}

// decodeGzip reads a gzip stream into dst, as arrow-go's codec does: a short stream is
// an error, and a byte past dst refuses it.
func decodeGzip(dst, src []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(src))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	n, err := io.ReadFull(r, dst)
	if err != nil {
		return nil, err
	}
	return dst[:n], past(r)
}

// decodeBrotli reads a Brotli stream, through s, the Brotli codec that was registered,
// into dst, as arrow-go's codec does: until the stream ends, a read gives nothing or
// dst is full; a short stream is what it holds, and a byte past dst refuses it. The
// stream's window is lowered to dst's first (brotliWindow).
func decodeBrotli(s compress.StreamingCodec, dst, src []byte) ([]byte, error) {
	in := io.Reader(bytes.NewReader(src))
	if len(src) > 0 {
		in = io.MultiReader(bytes.NewReader([]byte{brotliWindow(src[0], len(dst))}), bytes.NewReader(src[1:]))
	}
	r := s.NewReader(in)
	defer r.Close()
	for n := 0; n < len(dst); {
		k, err := r.Read(dst[n:])
		n += k
		switch {
		case err == io.EOF, err == nil && k == 0:
			return dst[:n], nil
		case err != nil:
			return nil, err
		}
	}
	return dst, past(r)
}

// brotliWindow is b, a Brotli stream's first byte, its window lowered to the least of
// its code's length that holds n bytes: a window of 2^w bytes holds 2^w−16. The
// decoder makes a ring buffer of the window, or less where the stream's first
// meta-block says it holds less, and the window is the stream's claim: a few bytes
// can claim the format's largest, 16 MiB, for a page of none. Lowered, it is at most
// 256 KiB, or the page's size where that is more.
//
// The window's code is the stream's first 1, 4 or 7 bits, the least significant first
// (RFC 7932, 9.1): 0 for 16; 1 and 3 bits n, not 0, for 17+n, 18 to 24; 1, 000 and 3
// bits m for 17 where m is 0 and 8+m, 10 to 15, where it is 2 or more. A code is
// changed for one of its own length only, so no bit after it moves. What is decoded
// is the same where the stream's output fits n bytes: a distance is a back-reference
// up to min(window−16, the bytes decoded so far) and a dictionary word past it, and the
// bytes decoded so far are at most n, within either window. Past n the stream is
// refused whatever the byte past it is.
func brotliWindow(b byte, n int) byte {
	fits := func(w int) bool { return 1<<w-16 >= n }
	switch {
	case b&1 == 0:
		return b // 16, a code of one bit
	case b>>1&7 != 0:
		for w := 18; w < 17+int(b>>1&7); w++ {
			if fits(w) {
				return b&^0x0e | byte(w-17)<<1
			}
		}
	case b>>4&7 != 1: // 1 is Large Window Brotli's, which the decoder refuses
		was := 17
		if m := int(b >> 4 & 7); m != 0 {
			was = 8 + m
		}
		for _, w := range []int{10, 11, 12, 13, 14, 15, 17} {
			if w >= was {
				break
			}
			if fits(w) {
				m := 0
				if w != 17 {
					m = w - 8
				}
				return b&^0x70 | byte(m)<<4
			}
		}
	}
	return b
}

// past is errPast if r, read to its buffer's end, holds another byte, or the error
// reading one. A read of no byte and no error is read again: Brotli's reader answers
// one at its stream's end, which is not a byte more. A reader that answers nothing
// else is taken, after a few, for one that holds more.
func past(r io.Reader) error {
	var one [1]byte
	for range 4 {
		n, err := r.Read(one[:])
		switch {
		case n > 0:
			return errPast
		case err == io.EOF:
			return nil
		case err != nil:
			return err
		}
	}
	return errPast
}

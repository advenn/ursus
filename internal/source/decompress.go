package source

import (
	"bufio"
	"bytes"
	"errors"
	"io"

	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zlib"
	"github.com/klauspost/compress/zstd"
)

// Compressed input (step 346).
//
// Polars reads a CSV, an NDJSON or a JSON file compressed by gzip, zlib or Zstandard as
// itself, whatever its name, knowing each by the bytes it begins with. Decompressed
// does so for a stream, as it is read: the file is never held whole. Decompression is
// the read's serial part, so it is klauspost's, which arrow-go's Parquet uses too:
// its gzip inflates 1.3 times as fast as the standard library's, and its Zstandard
// decoder decodes blocks ahead on goroutines of its own.

// Decompressed is rc's bytes, decompressed where they begin as a gzip, a zlib or a
// Zstandard stream does, and rc's own otherwise. Closing it closes rc.
//
// gzip's two bytes, 1f 8b, and Zstandard's four, 28 b5 2f fd, begin no text. zlib's,
// 78 and one of 01, 5e, 9c and da, can: "x^" may start a CSV header. So a zlib header
// is trusted only where the stream is zlib's beyond doubt (isZlib); a text it begins
// is read as text, where Polars reads a CSV of "x^2,b" as no rows. Bytes after a zlib
// stream's end are refused, as a gzip stream's are.
func Decompressed(rc io.ReadCloser) (io.ReadCloser, error) {
	head := make([]byte, 4)
	n, err := io.ReadFull(rc, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		rc.Close()
		return nil, err
	}
	head = head[:n]
	switch {
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}):
		zr, err := gzip.NewReader(io.MultiReader(bytes.NewReader(head), rc))
		if err != nil {
			rc.Close()
			return nil, err
		}
		return &decompressing{r: zr, close: func() error { zr.Close(); return rc.Close() }}, nil
	case bytes.HasPrefix(head, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		zr, err := zstd.NewReader(io.MultiReader(bytes.NewReader(head), rc))
		if err != nil {
			rc.Close()
			return nil, err
		}
		return &decompressing{r: zr, close: func() error { zr.Close(); return rc.Close() }}, nil
	case len(head) >= 2 && head[0] == 0x78 && bytes.IndexByte([]byte{0x01, 0x5e, 0x9c, 0xda}, head[1]) >= 0:
		prefix := make([]byte, 64<<10)
		copy(prefix, head)
		m, err := io.ReadFull(rc, prefix[len(head):])
		whole := err != nil
		if whole && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			rc.Close()
			return nil, err
		}
		prefix = prefix[:len(head)+m]
		r := io.MultiReader(bytes.NewReader(prefix), rc)
		if !isZlib(prefix, whole) {
			return &decompressing{r: r, close: rc.Close}, nil
		}
		br := bufio.NewReaderSize(r, 64<<10)
		zr, err := zlib.NewReader(br)
		if err != nil {
			rc.Close()
			return nil, err
		}
		return &decompressing{r: &zlibStream{zr: zr, after: br}, close: func() error { zr.Close(); return rc.Close() }}, nil
	}
	return &decompressing{r: io.MultiReader(bytes.NewReader(head), rc), close: rc.Close}, nil
}

// isZlib is whether prefix, a stream's first bytes or the whole of it, is zlib's: the
// whole stream decompressed to its end, its checksum met; or its first 64 KiB
// decompressed without an error to a quarter more than they are. Text a zlib header
// begins decompresses, deflate's fixed codes taking most of its bytes for a byte each,
// to about as much as it is, until an error; a compressed text, to more.
func isZlib(prefix []byte, whole bool) bool {
	zr, err := zlib.NewReader(bytes.NewReader(prefix))
	if err != nil {
		return false
	}
	n, err := io.Copy(io.Discard, zr)
	if whole {
		return err == nil
	}
	return errors.Is(err, io.ErrUnexpectedEOF) && n >= int64(len(prefix))*5/4
}

// zlibStream is a zlib stream's bytes, an error where bytes follow its end.
type zlibStream struct {
	zr    io.Reader
	after *bufio.Reader
}

func (z *zlibStream) Read(p []byte) (int, error) {
	n, err := z.zr.Read(p)
	if errors.Is(err, io.EOF) {
		if _, more := z.after.Peek(1); more == nil {
			return n, errors.New("zlib: bytes after the stream's end")
		}
	}
	return n, err
}

// decompressing is a stream's bytes, decompressed or not, closed with the stream.
type decompressing struct {
	r     io.Reader
	close func() error
}

func (d *decompressing) Read(p []byte) (int, error) { return d.r.Read(p) }
func (d *decompressing) Close() error               { return d.close() }

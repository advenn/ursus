package readfuzz_test

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zlib"
	"github.com/klauspost/compress/zstd"

	"github.com/advenn/ursus/internal/fuzzcheck"
	"github.com/advenn/ursus/internal/fuzzcheck/invariants"
)

// limits bounds one read of an input that is n bytes once decompressed: ten seconds,
// and 64 MiB plus 1 KiB a byte of allocation. The worst seen while choosing it was
// about 250 bytes a byte, a header of commas whose every column costs a name and a
// field; a small CSV costs 1.2 MiB, the reader's buffers.
func limits(n int) fuzzcheck.Limits {
	return fuzzcheck.Limits{Wall: 10 * time.Second, Alloc: 64<<20 + 1024*int64(n), API: true}
}

// maxInflated is the most an input may decompress to and still be read: a fuzz run
// is for bugs, and reading a gigabyte a ratio of 1,000 makes from a megabyte is a
// question of capacity.
const maxInflated = 64 << 20

// inflated is b's size as a reader sees it: decompressed, where it begins as one of
// the streams every text reader decompresses (internal/source.Decompressed), with the
// same decoders, and b's own length otherwise or when that is more. A stream that
// fails part way counts what it gave. It reads at most maxInflated+1 bytes, so ok is
// false for an input past maxInflated.
func inflated(b []byte) (n int, ok bool) {
	var r io.Reader
	switch {
	case bytes.HasPrefix(b, []byte{0x1f, 0x8b}):
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return len(b), true
		}
		defer zr.Close()
		r = zr
	case bytes.HasPrefix(b, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		zr, err := zstd.NewReader(bytes.NewReader(b), zstd.WithDecoderConcurrency(1))
		if err != nil {
			return len(b), true
		}
		defer zr.Close()
		r = zr
	case len(b) >= 2 && b[0] == 0x78:
		zr, err := zlib.NewReader(bytes.NewReader(b))
		if err != nil {
			return len(b), true
		}
		defer zr.Close()
		r = zr
	default:
		return len(b), true
	}
	m, _ := io.Copy(io.Discard, io.LimitReader(r, maxInflated+1))
	return max(len(b), int(m)), m <= maxInflated
}

// TestTheInvariantChecksAreOn: the targets here build batches, and a batch that breaks
// internal/data's boundary checks is ErrInternal only with them on.
func TestTheInvariantChecksAreOn(t *testing.T) {
	if !invariants.On() {
		t.Fatal("internal/data's CheckNonNullable or CheckTimeRange is off in the fuzz targets' binary")
	}
}

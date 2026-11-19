package arrowmeta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"

	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/internal/zstdframe"
)

// zstdBuffer is a Zstandard buffer of an IPC input: where its bytes begin in the
// input, and how many there are.
type zstdBuffer struct{ at, n int64 }

// zstdBuffersOf is every Zstandard buffer of b's batches, a file or a stream arrow-go
// wrote, found by the walk, and how many batches hold one.
func zstdBuffersOf(t *testing.T, b []byte, file bool) (bufs []zstdBuffer, batches int64) {
	t.Helper()
	add := func(body int64, z zstdBuffers) {
		if len(z.spans) > 0 {
			batches++
		}
		for _, s := range z.spans {
			bufs = append(bufs, zstdBuffer{body + s.off, s.n})
		}
	}
	if file {
		n := int(binary.LittleEndian.Uint32(b[len(b)-10:]))
		s, blocks, err := walkFooter(b[len(b)-10-n : len(b)-10])
		if err != nil {
			t.Fatal(err)
		}
		for _, blk := range blocks {
			w, err := newWalker(b[blk.off+8 : blk.off+int64(blk.meta)])
			if err == nil {
				err = walkBlockMessage(w, s, blk.block)
			}
			if err != nil {
				t.Fatal(err)
			}
			add(blk.off+int64(blk.meta), w.zstd)
		}
		return bufs, batches
	}
	m := &messageReader{op: "test", name: "a stream"}
	for pos := int64(0); pos+8 <= int64(len(b)); {
		n := int64(binary.LittleEndian.Uint32(b[pos+4:]))
		if n == 0 {
			break
		}
		body, z, err := m.walk(b[pos+8 : pos+8+n])
		if err != nil {
			t.Fatal(err)
		}
		add(pos+8+n, z)
		pos += 8 + n + body
	}
	return bufs, batches
}

// TestEveryZstdBodyIsChecked: File checks the Zstandard buffers of every batch that has
// one, dictionaries' and records', in the bytes it hands arrow-go: arrow-go reads each
// block's body in one ReadAt, at the offset past its metadata and of its length, as
// File expects (file.go). Each record gen makes from 40 seeds, as arrow-go writes it,
// big- and little-endian.
func TestEveryZstdBodyIsChecked(t *testing.T) {
	files := 0
	generated(t, 40, func(name string, b []byte, file bool, _ arrow.RecordBatch) {
		if !file || !strings.Contains(name, "Zstandard") {
			return
		}
		_, want := zstdBuffersOf(t, b, true)
		f, err := OpenFile("test", "a file", bytes.NewReader(b), int64(len(b)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := readFile(f); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := f.checked.Load(); got != want {
			t.Fatalf("%s: %d Zstandard bodies checked, and it has %d", name, got, want)
		}
		if want > 0 {
			files++
		}
	})
	if files == 0 {
		t.Fatal("no file with a Zstandard buffer read")
	}
}

// declarePast is frame, a Zstandard frame arrow-go's writer wrote (klauspost's
// EncodeAll), its header changed in place to declare a window of 256 MiB: a
// Window_Descriptor where its header has room for one, a single segment's content
// size otherwise. The frame is no longer one a decoder reads to its end; its header
// is refused first.
func declarePast(frame []byte) {
	fhd := frame[4]
	switch {
	case fhd&(1<<5) == 0: // a Window_Descriptor
		frame[5] = 18 << 3
	case fhd>>6 == 0: // a content size of 1 byte: a Window_Descriptor in its place
		frame[4], frame[5] = fhd&(1<<2), 18<<3
	case fhd>>6 == 1: // of 2 bytes: a Window_Descriptor and a dictionary ID of 0
		frame[4], frame[5], frame[6] = fhd&(1<<2)|1, 18<<3, 0
	case fhd>>6 == 2:
		binary.LittleEndian.PutUint32(frame[5:], 256<<20)
	default:
		binary.LittleEndian.PutUint64(frame[5:], 256<<20)
	}
}

// TestAZstdWindowPastTheLimitIsRefusedBeforeArrowGo: a Zstandard buffer whose frame
// declares a window past 128 MiB, in a dictionary or a record batch, of a file or a
// stream, is refused before arrow-go's decoder, made with klauspost's defaults, makes
// the window: KindIO, a *zstdframe.WindowError its cause, within a few megabytes of
// allocation. The first and the last buffer of each record gen makes from 20 seeds.
func TestAZstdWindowPastTheLimitIsRefusedBeforeArrowGo(t *testing.T) {
	refusedFiles, refusedStreams := 0, 0
	generated(t, 20, func(name string, b []byte, file bool, _ arrow.RecordBatch) {
		if !strings.Contains(name, "Zstandard") {
			return
		}
		bufs, _ := zstdBuffersOf(t, b, file)
		var picked []zstdBuffer
		for _, buf := range bufs {
			if buf.n > 8+4 && int64(binary.LittleEndian.Uint64(b[buf.at:])) > 0 {
				picked = append(picked, buf)
			}
		}
		if len(picked) > 1 {
			picked = []zstdBuffer{picked[0], picked[len(picked)-1]}
		}
		for _, buf := range picked {
			c := bytes.Clone(b)
			declarePast(c[buf.at+8 : buf.at+buf.n])
			before := heapAllocs()
			_, _, err := readWalked(c, file)
			if grew := heapAllocs() - before; grew > 16<<20 {
				t.Fatalf("%s, the buffer at byte %d: %d bytes allocated", name, buf.at, grew)
			}
			var we *zstdframe.WindowError
			var ue *uerr.Error
			if !errors.As(err, &we) || we.Window != 256<<20 || !errors.As(err, &ue) || ue.Kind != uerr.KindIO {
				t.Fatalf("%s, the buffer at byte %d declaring 256 MiB: %v", name, buf.at, err)
			}
			if file {
				refusedFiles++
			} else {
				refusedStreams++
			}
		}
	})
	if refusedFiles == 0 || refusedStreams == 0 {
		t.Fatalf("%d files and %d streams refused", refusedFiles, refusedStreams)
	}
}

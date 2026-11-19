// Package zstdframe holds the window a Zstandard frame declares to 128 MiB, the
// reference decoder's limit (step 528).
//
// A Zstandard frame declares the window its decoder keeps, the bytes its matches reach
// back into (RFC 8878 §3.1.1.1.2): up to 3.75 TB by the format, a single segment's its
// content size. klauspost's streaming decoder makes the whole window a frame declares,
// up to its MaxWindowSize of 512 MiB, at the frame's first block, whatever the frame
// holds: (*frameDec).reset sizes the frame's history from the declared window alone,
// and (*history).ensureBlock, or the stream's executing goroutine, makes it. So 17
// bytes made the CSV, NDJSON and JSON readers allocate 514 MiB (step 511).
//
// The reference decoder, libzstd, refuses a window past 128 MiB in its streaming API
// unless told otherwise (ZSTD_WINDOWLOG_LIMIT_DEFAULT, 27; `zstd -d` answers "Window
// size larger than maximum"), and Polars, which decompresses CSV, NDJSON, JSON and an
// Arrow IPC file's buffers through it, refuses such a frame too. zstd writes one only
// in its --long modes past 27. So ursus refuses it before a decoder reads the frame's
// header:
//
//   - NewReader, the text readers' decoder (internal/source.Decompressed), walks the
//     stream's frames as the decoder reads them (checking), and the decoder is told the
//     limit too;
//   - Check walks an Arrow IPC buffer's frames once the body holding them is read,
//     before arrow-go has it (internal/arrowmeta).
//
// Not a Parquet page's: klauspost's DecodeAll decodes a page into the buffer its header
// declares and keeps no window apart from it (step 520 holds it to that buffer), and
// Polars 2.1 and pyarrow both read a page whose frame declares a window of 512 MiB.
//
// Below the limit a frame's window is still made whole at its first block, from a few
// bytes: klauspost has no way to grow it as output arrives. WithDecoderLowmem, its
// default, already makes the least, the window and 1 MiB (2 MiB less one being a
// block's most); a single segment's window is the content size it declares, a claim
// as cheap to make as a window; and WithDecoderMaxMemory, in a stream, lowers the
// window allowed and nothing else. libzstd, too, allocates a stream's window when it
// reads the frame's header.
package zstdframe

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
)

// MaxWindow is the largest window a Zstandard frame may declare and be read: 128 MiB,
// libzstd's default limit for a stream (ZSTD_WINDOWLOG_LIMIT_DEFAULT, 27), which
// Polars keeps through it.
const MaxWindow = 128 << 20

// WindowError is the refusal of a frame declaring a window past MaxWindow.
type WindowError struct {
	Window uint64 // the window the frame declares, a single segment's its content size
}

func (e *WindowError) Error() string {
	size := fmt.Sprintf("%d bytes", e.Window)
	if e.Window%(1<<20) == 0 {
		size = fmt.Sprintf("%d MiB", e.Window>>20)
	}
	return "a Zstandard frame declares a window of " + size +
		", past 128 MiB, the most the reference decoder (libzstd) and Polars read by default"
}

// NewReader is r's Zstandard stream decompressed by klauspost's decoder under opts, a
// frame declaring a window past MaxWindow refused with a *WindowError before the
// decoder reads its header. Closing it closes the decoder, not r.
func NewReader(r io.Reader, opts ...zstd.DOption) (io.ReadCloser, error) {
	opts = append(opts[:len(opts):len(opts)], zstd.WithDecoderMaxWindow(MaxWindow))
	d, err := zstd.NewReader(&checking{r: r}, opts...)
	if err != nil {
		return nil, err
	}
	return d.IOReadCloser(), nil
}

// checking is r's bytes, a Zstandard stream's, each frame's header walked as it
// passes: a read that completes a header declaring a window past MaxWindow fails with
// a *WindowError, and every read after it. Where the bytes stop being frames the walk
// stops, and the decoder refuses them itself.
type checking struct {
	r   io.Reader
	w   walker
	err error
}

func (c *checking) Read(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	n, err := c.r.Read(p)
	if werr := c.w.feed(p[:n], MaxWindow); werr != nil {
		c.err = werr
		return 0, werr
	}
	return n, err
}

// Check walks b's Zstandard frames, as many as follow each other from its start, and
// refuses with a *WindowError the first declaring a window past MaxWindow. Bytes that
// are not a frame end the walk, without an error.
func Check(b []byte) error {
	var w walker
	return w.feed(b, MaxWindow)
}

// Windows is the window each of b's frames declares, in order, walked as Check walks
// them, without a limit.
func Windows(b []byte) []uint64 {
	var w walker
	w.feed(b, 0)
	return w.windows
}

// The magic numbers a frame begins with (RFC 8878 §3.1.1, §3.1.2): a Zstandard
// frame's, and a skippable frame's, 0x184D2A50 to 0x184D2A5F.
const (
	frameMagic     = 0xFD2FB528
	skippableMagic = 0x184D2A50
)

// The walk's steps, each taken once the bytes it reads are gathered.
const (
	stepMagic    = iota // a frame's magic number, 4 bytes
	stepSkipSize        // a skippable frame's size, 4 bytes
	stepFHD             // a Zstandard frame's Frame_Header_Descriptor, 1 byte
	stepHeader          // the rest of its header: window, dictionary ID, content size
	stepBlock           // a block's header, 3 bytes
	stepDone            // not a frame: the walk ends
)

// walker walks a stream of Zstandard frames as its bytes arrive, a piece at a time:
// each frame's header, each block's header, its content skipped, then its checksum.
type walker struct {
	step    int
	buf     [14]byte // the bytes of the step's field gathered so far
	have    int
	need    int      // how many the step reads, 0 in a walker not yet fed
	skip    uint64   // bytes to pass over before the step
	fhd     byte     // the frame's Frame_Header_Descriptor
	windows []uint64 // each frame's window, without a limit
}

// feed walks p, the stream's next bytes, refusing a frame whose window is past limit,
// unless limit is 0.
func (w *walker) feed(p []byte, limit uint64) error {
	if w.need == 0 && w.step == stepMagic {
		w.need = 4
	}
	for len(p) > 0 && w.step != stepDone {
		if w.skip > 0 {
			n := min(uint64(len(p)), w.skip)
			w.skip -= n
			p = p[n:]
			continue
		}
		n := copy(w.buf[w.have:w.need], p)
		w.have += n
		p = p[n:]
		if w.have < w.need {
			return nil
		}
		if err := w.take(limit); err != nil {
			w.step = stepDone
			return err
		}
	}
	return nil
}

// next makes the walk's next step one reading need bytes.
func (w *walker) next(step, need int) {
	w.step, w.need, w.have = step, need, 0
}

// take takes the step whose bytes are gathered.
func (w *walker) take(limit uint64) error {
	b := w.buf[:w.need]
	switch w.step {
	case stepMagic:
		switch m := binary.LittleEndian.Uint32(b); {
		case m == frameMagic:
			w.next(stepFHD, 1)
		case m&^0xF == skippableMagic:
			w.next(stepSkipSize, 4)
		default:
			w.next(stepDone, 0)
		}
	case stepSkipSize:
		w.skip = uint64(binary.LittleEndian.Uint32(b))
		w.next(stepMagic, 4)
	case stepFHD:
		w.fhd = b[0]
		if w.fhd&(1<<3) != 0 { // the reserved bit, which a decoder refuses
			w.next(stepDone, 0)
			return nil
		}
		w.next(stepHeader, headerSize(w.fhd))
	case stepHeader:
		win := window(w.fhd, b)
		if limit == 0 {
			w.windows = append(w.windows, win)
		} else if win > limit {
			return &WindowError{Window: win}
		}
		w.next(stepBlock, 3)
	case stepBlock:
		h := uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16
		last, typ, size := h&1 == 1, h>>1&3, uint64(h>>3)
		switch typ {
		case 0, 2: // raw, compressed: the block's bytes
			w.skip = size
		case 1: // RLE: its one byte
			w.skip = 1
		default: // reserved, which a decoder refuses
			w.next(stepDone, 0)
			return nil
		}
		if !last {
			w.next(stepBlock, 3)
			return nil
		}
		if w.fhd&(1<<2) != 0 { // the content checksum
			w.skip += 4
		}
		w.next(stepMagic, 4)
	}
	return nil
}

// headerSize is how many bytes follow a frame's Frame_Header_Descriptor fhd in its
// header: its Window_Descriptor, unless it is a single segment; its Dictionary_ID; and
// its Frame_Content_Size, of 1 byte under flag 0 for a single segment alone.
func headerSize(fhd byte) int {
	single := fhd&(1<<5) != 0
	n := [4]int{0, 1, 2, 4}[fhd&3] + [4]int{0, 2, 4, 8}[fhd>>6]
	switch {
	case !single:
		n++
	case fhd>>6 == 0:
		n++
	}
	return n
}

// window is the window a frame declares, its header past fhd being b: its
// Window_Descriptor's, or a single segment's Frame_Content_Size.
func window(fhd byte, b []byte) uint64 {
	if fhd&(1<<5) == 0 {
		exp, mant := uint64(b[0]>>3), uint64(b[0]&7)
		base := uint64(1) << (10 + exp)
		return base + base/8*mant
	}
	fcs := b[[4]int{0, 1, 2, 4}[fhd&3]:]
	switch fhd >> 6 {
	case 0:
		return uint64(fcs[0])
	case 1:
		return uint64(binary.LittleEndian.Uint16(fcs)) + 256
	case 2:
		return uint64(binary.LittleEndian.Uint32(fcs))
	}
	return binary.LittleEndian.Uint64(fcs)
}

package zstdframe_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/klauspost/compress/zstd"

	"github.com/advenn/ursus/internal/fuzzcheck"
	"github.com/advenn/ursus/internal/zstdframe"
)

var magic = []byte{0x28, 0xb5, 0x2f, 0xfd}

// headerWindow is the window klauspost's own header parser reads from a frame's
// header, the Go oracle of the walk's: its Window_Descriptor's, or a single segment's
// Frame_Content_Size. ok is false where klauspost refuses the header.
func headerWindow(b []byte) (window uint64, size int, ok bool) {
	var h zstd.Header
	if err := h.Decode(b); err != nil {
		return 0, 0, false
	}
	if h.SingleSegment {
		return h.FrameContentSize, h.HeaderSize, true
	}
	return h.WindowSize, h.HeaderSize, true
}

// encodeAll is b as klauspost's EncodeAll frames it: a single segment when b is small.
func encodeAll(t *testing.T, b []byte) []byte {
	t.Helper()
	e, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	return e.EncodeAll(b, nil)
}

// stream is b as klauspost's streaming writer frames it under a window of window
// bytes, flushed before its end: no content size, the window declared as it is.
func stream(t *testing.T, b []byte, window int) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf, zstd.WithEncoderConcurrency(1), zstd.WithWindowSize(window), zstd.WithLowerEncoderMem(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// declare is a frame klauspost's streaming writer wrote, its Window_Descriptor
// changed to wd: a larger window leaves a frame what it was.
func declare(frame []byte, wd byte) []byte {
	out := slices.Clone(frame)
	if out[4]&(1<<5) != 0 {
		panic("a single segment has no Window_Descriptor")
	}
	out[5] = wd
	return out
}

// singleSegment is a single segment's header declaring fcs bytes, in a field of 4
// bytes, or of 8 where fcs needs them, then a last raw block of no bytes.
func singleSegment(fcs uint64) []byte {
	b := slices.Clone(magic)
	if fcs < 1<<32 {
		b = binary.LittleEndian.AppendUint32(append(b, 2<<6|1<<5), uint32(fcs))
	} else {
		b = binary.LittleEndian.AppendUint64(append(b, 3<<6|1<<5), fcs)
	}
	return append(b, 1, 0, 0)
}

// skippable is a skippable frame of payload.
func skippable(id byte, payload []byte) []byte {
	b := binary.LittleEndian.AppendUint32([]byte{0x50 | id, 0x2a, 0x4d, 0x18}, uint32(len(payload)))
	return append(b, payload...)
}

// text is n bytes of compressible text.
func text(n int) []byte {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		fmt.Fprintf(&b, "%d,row %d,%d.25\n", i, i%97, i%13)
	}
	return []byte(b.String()[:n])
}

// TestWindowsAreKlauspostsHeaders: the window the walk reads from every frame header
// there is, each Frame_Header_Descriptor with each Window_Descriptor, its dictionary
// ID and content size random, is the one klauspost's header parser reads, and the
// walk goes on past the frame, its block and its checksum, to the next frame. A header
// klauspost refuses, its reserved bit set, ends the walk.
func TestWindowsAreKlauspostsHeaders(t *testing.T) {
	rng := rand.New(rand.NewPCG(528, 1))
	next := encodeAll(t, []byte("after"))
	nextWindow, _, _ := headerWindow(next)
	headers := 0
	for fhd := range 256 {
		for wd := range 256 {
			single := fhd&(1<<5) != 0
			if single && wd > 0 {
				break
			}
			b := append(slices.Clone(magic), byte(fhd))
			if !single {
				b = append(b, byte(wd))
			}
			for range 12 {
				b = append(b, byte(rng.Uint32()))
			}
			want, size, ok := headerWindow(b)
			if !ok {
				if got := zstdframe.Windows(b); len(got) != 0 {
					t.Fatalf("% x, a header klauspost refuses: windows %v", b, got)
				}
				continue
			}
			frame := append(b[:size:size], 1, 0, 0) // a last raw block of no bytes
			if fhd&(1<<2) != 0 {
				frame = append(frame, 0, 0, 0, 0)
			}
			got := zstdframe.Windows(append(frame, next...))
			if !slices.Equal(got, []uint64{want, nextWindow}) {
				t.Fatalf("% x: windows %v, and klauspost reads %d, then %d", frame, got, want, nextWindow)
			}
			headers++
		}
	}
	if headers < 32*256 {
		t.Fatalf("%d headers walked", headers)
	}
}

// TestTheWalkGoesPastSkippableFramesAndBlocks: skippable frames, whose payloads hold a
// frame's magic, and frames of many blocks, raw, RLE and compressed, are walked
// through to each frame's header, each window klauspost's.
func TestTheWalkGoesPastSkippableFramesAndBlocks(t *testing.T) {
	frames := [][]byte{
		encodeAll(t, nil),
		encodeAll(t, text(300)),
		encodeAll(t, text(1<<20)),
		stream(t, text(400<<10), 1<<10),
		stream(t, text(400<<10), 1<<20),
		stream(t, bytes.Repeat([]byte{'x'}, 1<<20), 1<<17),
		skippable(0, nil),
		skippable(15, append(slices.Clone(magic), 0x00, 0x80)),
	}
	var all []byte
	var want []uint64
	for i := range 40 {
		f := frames[(i*7)%len(frames)]
		all = append(all, f...)
		if w, _, ok := headerWindow(f); ok && f[0] == 0x28 {
			want = append(want, w)
		}
	}
	if got := zstdframe.Windows(all); !slices.Equal(got, want) {
		t.Fatalf("windows %v, and klauspost reads %v", got, want)
	}
	if err := zstdframe.Check(all); err != nil {
		t.Fatal(err)
	}
}

// TestCheckIsTheLimit: a frame declaring a window past 128 MiB is refused, its window
// klauspost's reading of the header, and one declaring 128 MiB or less is not: every
// Window_Descriptor, and single segments about the limit and at the format's most.
// klauspost's decoder held to the same limit refuses each refused frame too, at its
// header; of those passed, it decodes the ones of a window it can afford to make, a
// megabyte or less, without a word about the window.
func TestCheckIsTheLimit(t *testing.T) {
	var frames [][]byte
	base := stream(t, []byte("a,b\n1,2\n"), 1<<10)
	for wd := range 256 {
		frames = append(frames, declare(base, byte(wd)))
	}
	for _, fcs := range []uint64{0, 1 << 20, zstdframe.MaxWindow - 1, zstdframe.MaxWindow, zstdframe.MaxWindow + 1,
		1<<32 - 1, 1 << 32, 1<<64 - 1} {
		frames = append(frames, singleSegment(fcs))
	}
	refused, passed := 0, 0
	for _, f := range frames {
		window, _, ok := headerWindow(f)
		if !ok {
			t.Fatalf("% x: klauspost refuses the header", f[:6])
		}
		err := zstdframe.Check(f)
		var we *zstdframe.WindowError
		switch {
		case window > zstdframe.MaxWindow && !errors.As(err, &we):
			t.Fatalf("a window of %d bytes: %v", window, err)
		case window > zstdframe.MaxWindow && we.Window != window:
			t.Fatalf("a window of %d bytes, refused as one of %d", window, we.Window)
		case window > zstdframe.MaxWindow && !strings.Contains(err.Error(), "past 128 MiB"):
			t.Fatalf("the refusal does not name the limit: %v", err)
		case window <= zstdframe.MaxWindow && err != nil:
			t.Fatalf("a window of %d bytes is refused: %v", window, err)
		}
		if window > zstdframe.MaxWindow {
			refused++
		} else {
			passed++
		}
		if window > 1<<20 && window <= zstdframe.MaxWindow {
			continue
		}
		d, derr := zstd.NewReader(bytes.NewReader(f), zstd.WithDecoderMaxWindow(zstdframe.MaxWindow), zstd.WithDecoderConcurrency(1))
		if derr != nil {
			t.Fatal(derr)
		}
		_, derr = io.ReadAll(d)
		d.Close()
		windowErr := errors.Is(derr, zstd.ErrWindowSizeExceeded) || errors.Is(derr, zstd.ErrDecoderSizeExceeded)
		if windowErr != (window > zstdframe.MaxWindow) {
			t.Fatalf("a window of %d bytes: klauspost's decoder held to the limit answers %v", window, derr)
		}
	}
	if refused == 0 || passed == 0 {
		t.Fatalf("%d refused, %d passed", refused, passed)
	}
}

// TestNewReaderReadsAsKlauspost: streams klauspost's encoder writes, single segments
// and windows of 1 KiB to 8 MiB, several frames and skippable ones among them, read
// whole, a byte at a time and in halves, decompress through NewReader as through
// klauspost's decoder made with its defaults; and each cut short or with a byte
// changed, on one goroutine, to the same bytes and an error where it gives one,
// unless a window the change declares is past the limit.
func TestNewReaderReadsAsKlauspost(t *testing.T) {
	var streams [][]byte
	for _, n := range []int{0, 1, 255, 256, 70_000, 300_000} {
		streams = append(streams, encodeAll(t, text(n)))
	}
	for k := 10; k <= 23; k += 3 {
		streams = append(streams, stream(t, text(300_000), 1<<k))
	}
	streams = append(streams,
		slices.Concat(streams[1], skippable(3, text(40)), streams[7], streams[4]),
		slices.Concat(skippable(0, nil), streams[len(streams)-1], encodeAll(t, nil)))
	plain := func(r io.Reader, opts ...zstd.DOption) ([]byte, error) {
		d, err := zstd.NewReader(r, opts...)
		if err != nil {
			return nil, err
		}
		defer d.Close()
		return io.ReadAll(d)
	}
	ours := func(r io.Reader, opts ...zstd.DOption) ([]byte, error) {
		d, err := zstdframe.NewReader(r, opts...)
		if err != nil {
			return nil, err
		}
		defer d.Close()
		return io.ReadAll(d)
	}
	for i, s := range streams {
		want, err := plain(bytes.NewReader(s))
		if err != nil {
			t.Fatalf("stream %d: %v", i, err)
		}
		for name, r := range map[string]io.Reader{
			"whole":                   bytes.NewReader(s),
			"a byte each":             iotest.OneByteReader(bytes.NewReader(s)),
			"in halves":               iotest.HalfReader(bytes.NewReader(s)),
			"EOF with the last bytes": iotest.DataErrReader(bytes.NewReader(s)),
		} {
			got, err := ours(r)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("stream %d read %s: %d bytes, %v; klauspost's decoder gives %d bytes", i, name, len(got), err, len(want))
			}
		}
	}
	rng := rand.New(rand.NewPCG(528, 2))
	one := zstd.WithDecoderConcurrency(1)
	changed := 0
	for i, s := range streams {
		for range 60 {
			c := slices.Clone(s)
			if rng.IntN(2) == 0 && len(c) > 0 {
				c[rng.IntN(len(c))] ^= byte(1 + rng.IntN(255))
			} else {
				c = c[:rng.IntN(len(c)+1)]
			}
			if slices.ContainsFunc(zstdframe.Windows(c), func(w uint64) bool { return w > 8<<20 }) {
				continue
			}
			want, werr := plain(bytes.NewReader(c), one)
			got, gerr := ours(bytes.NewReader(c), one)
			if !bytes.Equal(got, want) || (werr == nil) != (gerr == nil) {
				t.Fatalf("stream %d changed: %d bytes, %v; klauspost's decoder gives %d bytes, %v", i, len(got), gerr, len(want), werr)
			}
			changed++
		}
	}
	if changed < 500 {
		t.Fatalf("%d changed streams compared", changed)
	}
}

// TestNewReaderRefusesBeforeTheWindow: a frame declaring a window past the limit,
// after frames that do not, is refused with a *WindowError naming its window, once
// the frames before it are read, and within 16 MiB of allocation: its window is never
// made. Read whole and a byte at a time.
func TestNewReaderRefusesBeforeTheWindow(t *testing.T) {
	first := stream(t, text(200_000), 1<<16)
	firstText := text(200_000)
	base := stream(t, text(1000), 1<<10)
	cases := map[string][]byte{
		"144 MiB":                               declare(base, 17<<3|1),
		"256 MiB":                               declare(base, 18<<3),
		"512 MiB":                               declare(base, 19<<3),
		"the format's most":                     declare(base, 0xff),
		"a single segment's 128 MiB and a byte": singleSegment(zstdframe.MaxWindow + 1),
		"a single segment's most":               singleSegment(1<<64 - 1),
	}
	for name, past := range cases {
		window, _, _ := headerWindow(past)
		for how, wrap := range map[string]func(io.Reader) io.Reader{
			"whole":       func(r io.Reader) io.Reader { return r },
			"a byte each": iotest.OneByteReader,
		} {
			t.Run(name+", "+how, func(t *testing.T) {
				var got []byte
				var err error
				fuzzcheck.Run(t, fuzzcheck.Limits{Alloc: 16 << 20}, func() error {
					d, nerr := zstdframe.NewReader(wrap(bytes.NewReader(slices.Concat(first, past))))
					if nerr != nil {
						return nerr
					}
					defer d.Close()
					got, err = io.ReadAll(d)
					return nil
				})
				var we *zstdframe.WindowError
				if !errors.As(err, &we) || we.Window != window {
					t.Fatalf("a frame declaring %d bytes: %v", window, err)
				}
				if !bytes.HasPrefix(firstText, got) {
					t.Fatalf("%d bytes before the refusal, not the first frame's", len(got))
				}
			})
		}
	}
}

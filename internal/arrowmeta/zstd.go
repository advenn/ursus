package arrowmeta

import (
	"encoding/binary"

	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/internal/zstdframe"
)

// A Zstandard buffer's window (step 528).
//
// A Zstandard frame's header declares the window its decoder keeps, a claim as cheap
// to make as any length. Polars refuses a frame declaring a window past 128 MiB, as
// the reference decoder does by default (internal/zstdframe), and so does ursus: each
// Zstandard buffer's frames are walked once the body holding them is read, before
// arrow-go has it.
//
// A compressed buffer is the length of what it decompresses to, 8 bytes, then its
// frames; a buffer of length -1 is not compressed, and one of 0 holds nothing. Every
// frame of the buffer is walked, though decoding stops once it has the length: a
// frame after the length's end, which no writer writes, is refused too.

// checkZstd walks the frames of each Zstandard buffer of body, as z places them, and
// refuses the first declaring a window past zstdframe.MaxWindow, KindIO, as name's, the
// batch's body being where.
func checkZstd(body []byte, z zstdBuffers, op, name, where string) error {
	for _, s := range z.spans {
		b := body[s.off : s.off+s.n]
		if len(b) < 8 || int64(binary.LittleEndian.Uint64(b)) <= 0 {
			continue
		}
		if err := zstdframe.Check(b[8:]); err != nil {
			return uerr.Wrap(err, uerr.KindIO, op, "reading %s: buffer %d of %s %s", name, s.buf, z.what, where).
				Hint("write it again with a Zstandard window of 128 MiB or less")
		}
	}
	return nil
}

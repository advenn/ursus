package arrowmeta

import (
	"encoding/binary"

	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/internal/zstdframe"
)

// A Zstandard buffer's window (step 528).
//
// arrow-go decompresses each Zstandard buffer of a record batch through klauspost's
// streaming decoder, made with its defaults (getDecompressor, arrow/ipc/compression.go),
// which ursus cannot reach: that decoder makes the whole window a frame declares, up to
// 512 MiB, before it decodes a block, so a 4.7 KB file allocated 513 MiB, and a window
// past 512 MiB panicked arrow-go's reader. Polars refuses a frame declaring a window
// past 128 MiB, as the reference decoder does by default (internal/zstdframe), and so
// does ursus now: each Zstandard buffer's frames are walked once the body holding them
// is read, before arrow-go has it.
//
// A buffer is arrow-go's length of what it decompresses to, 8 bytes, then its frames;
// arrow-go decodes nothing of a buffer of length -1, which is not compressed, or of 0.
// Every frame of the buffer is walked, though arrow-go stops decoding once it has the
// length: a frame after the length's end, which no writer writes, is refused too.

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

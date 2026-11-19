package readfuzz_test

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"
	"math/rand/v2"
	"strconv"
	"testing"

	kzstd "github.com/klauspost/compress/zstd"

	"github.com/advenn/ursus"
)

// A compressed CSV, NDJSON or JSON text cut short (step 534): step 534's IPC finding
// was a read error wrapping io.EOF, the engine's end of its input, and the text
// readers read compressed streams through internal/source.Decompressed, whose
// decoders answer an error for a stream cut short. This holds them to it.

// textFormat is a text format and the public API's read of its bytes.
type textFormat struct {
	name string
	scan func(b []byte) *ursus.LazyFrame
}

var textFormats = []textFormat{
	{"CSV", func(b []byte) *ursus.LazyFrame { return ursus.ScanCSVReader(b, "cut.csv") }},
	{"NDJSON", func(b []byte) *ursus.LazyFrame { return ursus.ScanNDJSONBytes(b, "cut.ndjson") }},
	{"JSON", func(b []byte) *ursus.LazyFrame { return ursus.ScanJSONBytes(b, "cut.json") }},
}

// TestACompressedTextCutShortIsRefusedOrReadAsItsDecoderReadsIt: a CSV, an NDJSON and
// a JSON text of rows a seeded generator makes (cutText), compressed by gzip, by gzip
// in two members, by Zstandard, by Zstandard in two frames and by zlib, each cut
// after each byte of its compressed form, is refused, by Collect and by Count, with
// an error that does not wrap io.EOF; or, where the decoder of the format reads the
// cut bytes, the standard library's gzip and zlib and klauspost's Zstandard
// DecodeAll, which ursus does not read through, it is read as the text the decoder
// reads, read itself: the same frame, or both refused. A two-member or two-frame
// stream cut between them reads as its first, as the decoders read it.
//
// Cuts begin at the bytes Decompressed knows a stream by, gzip's two and Zstandard's
// four: fewer are not that stream, and are read as text. A zlib stream is trusted, as
// step 346 has it, only where its first 64 KiB decompress to a quarter more than they
// are, or the whole stream to its checksum, which no cut does: a cut of fewer than
// 64 KiB is read as text. So the zlib stream is one just past 64 KiB, of a text
// that compresses little (zlibPast), cut after each byte past its first 64 KiB.
func TestACompressedTextCutShortIsRefusedOrReadAsItsDecoderReadsIt(t *testing.T) {
	for _, format := range textFormats {
		t.Run(format.name, func(t *testing.T) {
			text := cutText(format.name, 20, 1)
			half := bytes.IndexByte(text[len(text)/2:], '\n') + len(text)/2 + 1
			zstd := func(b []byte) []byte { return compress(t, b, 3) }
			gz := func(b []byte) []byte { return compress(t, b, 1) }
			streams := []struct {
				name  string
				codec string
				z     []byte
				from  int
			}{
				{"gzip", "gzip", gz(text), 2},
				{"gzip of two members", "gzip", append(gz(text[:half]), gz(text[half:])...), 2},
				{"Zstandard", "zstd", zstd(text), 4},
				{"Zstandard of two frames", "zstd", append(zstd(text[:half]), zstd(text[half:])...), 4},
				{"zlib past 64 KiB", "zlib", zlibPast(t, format.name), 64 << 10},
			}
			for _, s := range streams {
				// The whole stream reads as its text: the decoder is the one
				// Decompressed knows it by, and the oracle reads it.
				if !textCutRead(t, format, s.name+", whole", s.codec, s.z) {
					t.Fatalf("%s: the whole stream is refused", s.name)
				}
				reads, cuts := 0, 0
				for k := s.from; k < len(s.z); k += flipStride {
					cuts++
					if textCutRead(t, format, fmt.Sprintf("%s cut after %d of its %d bytes", s.name, k, len(s.z)),
						s.codec, s.z[:k]) {
						reads++
					}
				}
				t.Logf("%s: %d cuts, %d of them read as the decoder reads them, the others refused", s.name, cuts, reads)
			}
		})
	}
}

// textCutRead holds format's read of z, a stream of codec cut short or whole,
// described as what, to the decoder's reading of it (inflateOracle), and says whether
// it read z. Where the decoder refuses z, Collect and Count refuse it. Where it reads
// z as a text, Collect and Count read z as they read that text, or refuse both. Each
// refusal is one (refusal): no error wrapping io.EOF, the engine's end.
func textCutRead(t *testing.T, format textFormat, what, codec string, z []byte) bool {
	t.Helper()
	ctx := t.Context()
	lf := format.scan(z)
	df, err := lf.Collect(ctx)
	n, cerr := lf.Count(ctx)
	refusal(t, what+": Collect", err)
	refusal(t, what+": Count", cerr)
	if (err == nil) != (cerr == nil) || err == nil && n != int64(df.Height()) {
		t.Errorf("%s: Collect reads %v (%v), and Count %d (%v)", what, df, err, n, cerr)
	}
	text, ierr := inflateOracle(codec, z)
	if ierr != nil {
		if err == nil {
			t.Errorf("%s: read as %d rows, and no error, where the decoder refuses the bytes: %v", what, df.Height(), ierr)
		}
		return err == nil
	}
	want, werr := format.scan(text).Collect(ctx)
	switch {
	case (err == nil) != (werr == nil):
		t.Errorf("%s: read as %v (%v), and the %d bytes the decoder reads of it as %v (%v)",
			what, df, err, len(text), want, werr)
	case err == nil && !df.Schema().Equal(want.Schema()):
		t.Errorf("%s: read as %s, and the text the decoder reads of it as %s", what, df.Schema(), want.Schema())
	case err == nil:
		same, eerr := df.Equals(ctx, want, true)
		if eerr != nil || !same {
			t.Errorf("%s: read as\n%v\nand the text the decoder reads of it as\n%v\n(%v)", what, df, want, eerr)
		}
	}
	return err == nil
}

// inflateOracle is z, a stream of codec, as a decoder ursus does not read through
// reads it: the standard library's gzip, every member, and zlib; and klauspost's
// Zstandard DecodeAll, every frame, where ursus decodes a stream.
func inflateOracle(codec string, z []byte) ([]byte, error) {
	switch codec {
	case "gzip":
		r, err := gzip.NewReader(bytes.NewReader(z))
		if err != nil {
			return nil, err
		}
		return io.ReadAll(r)
	case "zlib":
		r, err := zlib.NewReader(bytes.NewReader(z))
		if err != nil {
			return nil, err
		}
		return io.ReadAll(r)
	case "zstd":
		d, err := kzstd.NewReader(nil, kzstd.WithDecoderConcurrency(1))
		if err != nil {
			return nil, err
		}
		defer d.Close()
		return d.DecodeAll(z, nil)
	}
	return nil, fmt.Errorf("no codec %q", codec)
}

// cutText is n rows of format, CSV, NDJSON or JSON, of a seeded generator's: an
// integer, a float, text of letters, digits and punctuation, a quote and a comma among
// them, a boolean, and a date.
func cutText(format string, n int, seed uint64) []byte {
	r := rand.New(rand.NewPCG(534, seed))
	const alphabet = `abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 ,"'-_/+`
	var b bytes.Buffer
	switch format {
	case "CSV":
		b.WriteString("id,score,name,flag,day\n")
	case "JSON":
		b.WriteString("[")
	}
	for i := range n {
		name := make([]byte, 4+r.IntN(20))
		for k := range name {
			name[k] = alphabet[r.IntN(len(alphabet))]
		}
		score := strconv.FormatFloat(r.NormFloat64()*1000, 'g', -1, 64)
		flag := strconv.FormatBool(r.IntN(2) == 0)
		day := fmt.Sprintf("2024-%02d-%02d", 1+r.IntN(12), 1+r.IntN(28))
		switch format {
		case "CSV":
			fmt.Fprintf(&b, "%d,%s,%s,%s,%s\n", r.Int64N(1<<40)-1<<39, score, csvField(name), flag, day)
		default:
			if format == "JSON" && i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"id":%d,"score":%s,"name":%s,"flag":%s,"day":"%s"}`,
				r.Int64N(1<<40)-1<<39, score, strconv.Quote(string(name)), flag, day)
			if format == "NDJSON" {
				b.WriteString("\n")
			}
		}
	}
	if format == "JSON" {
		b.WriteString("]")
	}
	return b.Bytes()
}

// csvField is s quoted as a CSV field, its quotes doubled.
func csvField(s []byte) string {
	return `"` + string(bytes.ReplaceAll(s, []byte(`"`), []byte(`""`))) + `"`
}

// zlibPast is a zlib stream of cutText's rows of format, as many as make it a few
// hundred bytes longer than 64 KiB.
func zlibPast(t *testing.T, format string) []byte {
	t.Helper()
	const want = 64<<10 + 256
	lo, hi := 1, 64
	for len(compress(t, cutText(format, hi, 2), 2)) < want {
		lo, hi = hi, 2*hi
	}
	for lo < hi {
		mid := (lo + hi) / 2
		if len(compress(t, cutText(format, mid, 2), 2)) < want {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return compress(t, cutText(format, lo, 2), 2)
}

package readfuzz_test

import (
	"encoding/binary"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/fuzzcheck"
	"github.com/advenn/ursus/internal/source/parquet"
	"github.com/advenn/ursus/ursustest"
)

// TestAPageWhoseBytesAreMissingIsRefused (step 521): a page whose header is whole and
// whose bytes stop short at its column chunk's end. The engine takes an error that is
// io.EOF for the end of the scan, so the chunk's error for bytes that are not there
// is one that is not, or the read would answer the rows before it, and no error.
// pyarrow 25.0.1 refuses a dictionary page none of whose 24 bytes is in its chunk
// ("Unexpected end of stream: Page was smaller (0) than expected (24)"), and DuckDB
// 1.5.5 ("TProtocolException: Invalid data"); both count its rows from the footer, as
// ursus's Count does, which reads no chunk (step 370).
//
// Every such cut is refused, on one thread and on four: each page of a file of
// three, a dictionary, its indices and a PLAIN page, cut after each of its bytes,
// none of them first, the pages after it gone; a cut at a page's end leaves a chunk
// of fewer rows than its row group's, which step 416's count refuses. In each kind
// of chunk arrow-go's column reader decodes (pqIndexKinds) and, where a kind's
// footer lists an encoding only to keep it from the reader's own pages, that kind
// read by them. The whole file reads, and Count is its height; a cut file's Count,
// from the same footer, is the whole file's or an error.
func TestAPageWhoseBytesAreMissingIsRefused(t *testing.T) {
	ctx := t.Context()
	read := func(b []byte, threads int) (*ursus.DataFrame, error) {
		return ursus.ScanParquetBytes(b, "cut.parquet").Collect(ctx, ursus.WithThreads(threads))
	}
	count := func(b []byte) (int64, error) { return ursus.ScanParquetBytes(b, "cut.parquet").Count(ctx) }

	var kinds []pqIndexKind
	for _, k := range pqIndexKinds() {
		kinds = append(kinds, k)
		if k.shape.encs != nil {
			own := k
			own.name += ", read by the reader's own pages"
			own.shape.encs = nil
			kinds = append(kinds, own)
		}
	}
	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			pages := []pqPage{k.dict(), k.data(pqRLEDict, pqIndexRun(k.present, 1)), k.plain(k.values[0])}
			whole := k.file(pages...)
			want, err := read(whole, 1)
			if err != nil {
				t.Fatalf("the whole file: %v", err)
			}
			rows, err := count(whole)
			if err != nil || rows != int64(want.Height()) {
				t.Fatalf("the whole file reads as %d rows, and Count says %d, %v", want.Height(), rows, err)
			}
			cut := func(i, n int) []byte {
				ps := slices.Clone(pages[:i+1])
				ps[i].uncompressed, ps[i].compressed = int32(len(ps[i].body)), int32(len(ps[i].body))
				ps[i].body = ps[i].body[:n]
				f := k.shape
				f.pages, f.rows = ps, rows
				return f.bytes()
			}
			for i, p := range pages {
				for n := 0; n <= len(p.body); n += flipStride {
					if i == len(pages)-1 && n == len(p.body) {
						break // the whole file
					}
					b := cut(i, n)
					for _, threads := range []int{1, 4} {
						if df, err := read(b, threads); err == nil {
							t.Errorf("page %d of %d cut after %d of its %d bytes, %d threads: read as %d rows "+
								"of %d, and no error", i+1, len(pages), n, len(p.body), threads, df.Height(), rows)
						}
					}
					if got, err := count(b); err == nil && got != rows {
						t.Errorf("page %d cut after %d bytes: Count says %d, and the footer %d", i+1, n, got, rows)
					}
				}
			}
		})
	}
}

// TestAFooterListClaimingMoreThanItsBytesIsRefused (step 521): a Thrift list's header
// carries a count, a claim, and step 423's walk of the footer refuses a count past
// its bytes before anything is sized by it. The walk takes in any footer within the
// file less 8 bytes, one leaving the file's leading magic no room among them, and
// reads a list's elements as the types parquet.thrift gives them, whatever its header
// says, so that a list of row groups said to hold a binary hides no list of 10^8
// column chunks from it (pqHiddenGroups). Each is refused as corrupt, within
// fuzzcheck's bounds: the forged footer of 10^8 row groups leaving the magic no room,
// at each offset, and the hidden list. Its schema, its rows read and its Count are
// each refused.
func TestAFooterListClaimingMoreThanItsBytesIsRefused(t *testing.T) {
	ctx := t.Context()
	groups := pqFooterOf(pqForged{phys: pqInt64, rows: 8, pages: []pqPage{pqInt64Page(make([]int64, 8))},
		groups: 100_000_000}.bytes())
	inputs := []struct {
		name string
		b    []byte
	}{
		{"a list of row groups said to hold a binary", pqHiddenGroups()},
	}
	for j := range 4 {
		inputs = append(inputs, struct {
			name string
			b    []byte
		}{"10^8 row groups, the footer " + strconv.Itoa(j) + " bytes into the file", pqMagicless(groups, j)})
	}
	for _, in := range inputs {
		fuzzcheck.Run(t, limits(len(in.b)), func() error {
			lf := ursus.ScanParquetBytes(in.b, "footer.parquet")
			_, errSchema := lf.CollectSchema(ctx)
			_, errRead := lf.Collect(ctx)
			_, errCount := lf.Count(ctx)
			for what, err := range map[string]error{"its schema": errSchema, "its rows": errRead, "Count": errCount} {
				if err == nil || !errors.Is(err, ursus.ErrIO) {
					t.Errorf("%s: %s answered %v, not an I/O error", in.name, what, err)
				}
			}
			return errors.Join(errSchema, errRead, errCount)
		})
	}
}

// TestAnUncompressedPageWhoseSizesDifferIsRefused (step 521): an UNCOMPRESSED page
// holds its bytes as they lie, so its header's two sizes are one, and a page whose
// sizes differ is refused as corrupt. DuckDB 1.5.5 refuses every such
// page ("Page size mismatch"), and pyarrow 25.0.1 one holding none ("Unexpected end
// of stream"). In each kind's file of three pages, each page claiming 1 and 8 bytes
// more than it holds, and 1 fewer, and holding none of its bytes, is refused, read
// into poisoned buffers on one thread and on four; the whole file reads as it did.
func TestAnUncompressedPageWhoseSizesDifferIsRefused(t *testing.T) {
	ctx := t.Context()
	read := func(b []byte, threads int) (*ursus.DataFrame, error) {
		mem := fuzzcheck.NewAllocator(len(b))
		mem.Poison = true
		defer parquet.ReadPagesWith(mem)()
		return ursus.ScanParquetBytes(b, "uncompressed.parquet").Collect(ctx, ursus.WithThreads(threads))
	}
	var kinds []pqIndexKind
	for _, k := range pqIndexKinds() {
		kinds = append(kinds, k)
		if k.shape.encs != nil {
			own := k
			own.name += ", read by the reader's own pages"
			own.shape.encs = nil
			kinds = append(kinds, own)
		}
	}
	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			pages := []pqPage{k.dict(), k.data(pqRLEDict, pqIndexRun(k.present, 1)), k.plain(k.values[0])}
			for _, threads := range []int{1, 4} {
				if _, err := read(k.file(pages...), threads); err != nil {
					t.Fatalf("the whole file, %d threads: %v", threads, err)
				}
			}
			for i, p := range pages {
				n := int32(len(p.body))
				for what, mut := range map[string]func(*pqPage){
					"1 byte more than it holds":  func(q *pqPage) { q.uncompressed = n + 1 },
					"8 bytes more than it holds": func(q *pqPage) { q.uncompressed = n + 8 },
					"1 byte fewer than it holds": func(q *pqPage) { q.uncompressed = n - 1 },
					"none of its bytes":          func(q *pqPage) { q.uncompressed, q.body = n, nil },
				} {
					ps := slices.Clone(pages)
					mut(&ps[i])
					for _, threads := range []int{1, 4} {
						if df, err := read(k.file(ps...), threads); err == nil {
							t.Errorf("page %d of %d claiming %s, %d threads: read as %d rows, and no error",
								i+1, len(pages), what, threads, df.Height())
						}
					}
				}
			}
		})
	}
}

// TestADictionaryIndexOfZeroBitsReadsAsIndexZero (step 521): a data page's dictionary
// indices 0 bits wide are each 0, and an RLE run of them stores its value in no byte,
// so a byte after the run's header, a stray one or the header of a run after it, is
// no index of it. pyarrow 25.0.1 and DuckDB 1.5.5 read index 0 there, as the reader's
// own pages do. In each kind's file of a dictionary and such a page, the indices one
// run of 0 bits, then a stray byte of 0, 1 or 255, or two runs, each reads as the page
// without them reads, the PLAIN page of the dictionary's first value: through the own
// pages always, through arrow-go's column reader as that or refused.
func TestADictionaryIndexOfZeroBitsReadsAsIndexZero(t *testing.T) {
	ctx := t.Context()
	read := func(b []byte) (*ursus.DataFrame, error) {
		return ursus.ScanParquetBytes(b, "zero.parquet").Collect(ctx)
	}
	var kinds []pqIndexKind
	own := map[string]bool{}
	for _, k := range pqIndexKinds() {
		kinds = append(kinds, k)
		if k.shape.encs != nil {
			o := k
			o.name += ", read by the reader's own pages"
			o.shape.encs = nil
			kinds = append(kinds, o)
			own[o.name] = true
		}
	}
	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			want, err := read(k.file(k.dict(), k.plain(k.values[0])))
			if err != nil {
				t.Fatalf("the PLAIN page: %v", err)
			}
			run := func(n int) []byte { return binary.AppendUvarint(nil, uint64(n)<<1) }
			for what, idx := range map[string][]byte{
				"one run":                      slices.Concat([]byte{0}, run(k.present)),
				"one run and a stray 0":        slices.Concat([]byte{0}, run(k.present), []byte{0}),
				"one run and a stray 1":        slices.Concat([]byte{0}, run(k.present), []byte{1}),
				"one run and a stray 255":      slices.Concat([]byte{0}, run(k.present), []byte{255}),
				"two runs":                     slices.Concat([]byte{0}, run(2), run(k.present-2)),
				"two runs and a stray 1 after": slices.Concat([]byte{0}, run(2), run(k.present-2), []byte{1}),
			} {
				got, err := read(k.file(k.dict(), k.data(pqRLEDict, idx)))
				switch {
				case err != nil && own[k.name]:
					t.Errorf("%s, by the reader's own pages: %v", what, err)
				case err == nil:
					ursustest.AssertFrameEqual(t, got, want)
				}
			}
		})
	}
}

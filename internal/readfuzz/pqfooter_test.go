package readfuzz_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/fuzzcheck"
	"github.com/advenn/ursus/internal/source/parquet"
)

// corpusBytes is the first value of a fuzz corpus file, a []byte.
func corpusBytes(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	if len(lines) < 2 || lines[0] != "go test fuzz v1" {
		t.Fatalf("%s is not a fuzz corpus file", path)
	}
	v, ok := strings.CutPrefix(lines[1], "[]byte(")
	if v, ok = strings.CutSuffix(v, ")"); !ok {
		t.Fatalf("%s: its first value is not a []byte", path)
	}
	s, err := strconv.Unquote(v)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return []byte(s)
}

// TestAPageWhoseBytesAreMissingIsRefused (F12 of step 521): a page whose header is
// whole and whose bytes stop short at its column chunk's end. arrow-go's page reader
// answers io.EOF where none of a page's bytes is there, which the reader handed on
// as the chunk's error (batchErr, step 517; the reader's own pages since step 192),
// and the engine takes an error that is io.EOF for the end of the scan: a read of
// FuzzScanParquet's input, a dictionary page none of whose 24 bytes is in its chunk,
// answered 0 rows and no error, where Count, from the footer, said 5. pyarrow
// 25.0.1 refuses that file ("Unexpected end of stream: Page was smaller (0) than
// expected (24)"), and DuckDB 1.5.5 ("TProtocolException: Invalid data"); both
// count its 5 rows from the footer, as ursus's Count does, which reads no chunk
// (step 370).
//
// Every such cut is refused now, on one thread and on four: each page of a file of
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

	f11 := corpusBytes(t, filepath.Join("testdata", "fuzz", "FuzzScanParquet", "page-bytes-missing-read-as-no-rows"))
	n, err := count(f11)
	if err != nil {
		t.Fatalf("Count of FuzzScanParquet's input: %v", err)
	}
	for _, threads := range []int{1, 4} {
		if df, err := read(f11, threads); err == nil {
			t.Errorf("FuzzScanParquet's input, %d threads: read as %d rows, and no error; Count says %d",
				threads, df.Height(), n)
		} else if errors.Is(err, io.EOF) {
			t.Errorf("FuzzScanParquet's input, %d threads: %v, an error the engine takes for the end", threads, err)
		}
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

// TestAFooterListClaimingMoreThanItsBytesIsRefused (F13 of step 521): arrow-go makes
// each list of a Parquet footer a slice of the count its header claims before it
// reads an element, a count Thrift bounds only by its 100 MB message limit, and
// step 423's walk of the footer is to refuse a count past its bytes first. It walked
// no footer that left the file's leading magic no room, one longer than the file
// less 12 bytes, which arrow-go reads up to the file less 8: FuzzOwnPagesMatchArrow's
// input, 78 bytes claiming 10^8 row groups, had arrow-go ask for 1.5 GB before it
// refused the footer. Nor did it read a list as arrow-go reads one: arrow-go takes a
// list's elements for the types parquet.thrift gives them, whatever its header
// says, so a list of row groups said to hold a binary hid a list of 10^8 column
// chunks in it from a walk by the header's types (pqHiddenGroups). Each is refused
// now, as corrupt, within fuzzcheck's bounds: the fuzzer's input; the forged
// footer of 10^8 row groups leaving the magic no room, at each offset; and the
// hidden list. Its schema, its rows read and its Count are each refused.
func TestAFooterListClaimingMoreThanItsBytesIsRefused(t *testing.T) {
	ctx := t.Context()
	groups := pqFooterOf(pqForged{phys: pqInt64, rows: 8, pages: []pqPage{pqInt64Page(make([]int64, 8))},
		groups: 100_000_000}.bytes())
	inputs := []struct {
		name string
		b    []byte
	}{
		{"FuzzOwnPagesMatchArrow's input", corpusBytes(t, filepath.Join("..", "source", "parquet", "testdata", "fuzz",
			"FuzzOwnPagesMatchArrow", "footer-1e8-row-groups-over-the-leading-magic"))},
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

// TestAnUncompressedPageWhoseSizesDifferIsRefused (F14 of step 521, found by its
// fuzz runs and by step 519's): an UNCOMPRESSED page holds its bytes as they lie, so its header's two sizes
// are one. arrow-go read a page's compressed size of bytes into a buffer of its
// uncompressed size, and its codec for UNCOMPRESSED answered the whole buffer, so a
// page holding fewer bytes than it claims read the rest from whatever the buffer held:
// FuzzScanParquet's input read 0xA5, the poisoned allocator's byte, as its values,
// and no error. DuckDB 1.5.5 refuses every such page ("Page size mismatch"), and
// pyarrow 25.0.1 one holding none ("Unexpected end of stream"). In each kind's file
// of three pages, each page claiming 1 and 8 bytes more than it holds, and 1 fewer,
// and holding none of its bytes, is refused now, read into poisoned buffers on one
// thread and on four; the whole file reads as it did.
func TestAnUncompressedPageWhoseSizesDifferIsRefused(t *testing.T) {
	ctx := t.Context()
	read := func(b []byte, threads int) (*ursus.DataFrame, error) {
		mem := fuzzcheck.NewAllocator(len(b))
		mem.Poison = true
		defer parquet.ReadPagesWith(mem)()
		return ursus.ScanParquetBytes(b, "uncompressed.parquet").Collect(ctx, ursus.WithThreads(threads))
	}
	b := corpusBytes(t, filepath.Join("testdata", "fuzz", "FuzzScanParquet", "uncompressed-page-of-no-bytes-claiming-3072"))
	for _, threads := range []int{1, 4} {
		if df, err := read(b, threads); err == nil {
			t.Errorf("FuzzScanParquet's input, %d threads: read as %d rows, and no error", threads, df.Height())
		}
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

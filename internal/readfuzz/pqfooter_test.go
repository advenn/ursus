package readfuzz_test

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/fuzzcheck"
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

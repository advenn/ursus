package parquet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/compress"

	"github.com/advenn/ursus/internal/arrowout"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/fuzzcheck"
	_ "github.com/advenn/ursus/internal/fuzzcheck/invariants"
	"github.com/advenn/ursus/internal/source"
)

// twinSeeds are FuzzOwnPagesMatchArrow's seed corpus: writePagesFixture's every type
// the reader decodes from its pages, 120 rows, in each layout
// TestOwnPagesReadAsArrowGoDoes reads (V1 and V2 pages, dictionary and PLAIN, small
// pages, a dictionary that falls back to PLAIN, Zstandard), with nulls scattered or
// in runs; and the Parquet fixtures other engines wrote.
func twinSeeds(f *testing.F) [][]byte {
	v1 := parquet.WithDataPageVersion(parquet.DataPageV1)
	v2 := parquet.WithDataPageVersion(parquet.DataPageV2)
	dict, plain := parquet.WithDictionaryDefault(true), parquet.WithDictionaryDefault(false)
	small := parquet.WithDataPageSize(512)
	overflow := parquet.WithDictionaryPageSizeLimit(300)
	layouts := [][]parquet.WriterProperty{
		{v1, dict}, {v2, dict}, {v1, plain}, {v2, plain}, {v1, dict, small}, {v2, plain, small},
		{v1, dict, small, overflow}, {v2, dict, small, overflow},
		{v1, dict, parquet.WithCompression(compress.Codecs.Zstd)},
	}
	var seeds [][]byte
	dir := f.TempDir()
	for i, props := range layouts {
		path := filepath.Join(dir, fmt.Sprint(i, ".parquet"))
		writePagesFixture(f, path, 120, []string{"scattered", "runs"}[i%2], props...)
		b, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		seeds = append(seeds, b)
	}
	paths, err := filepath.Glob(filepath.Join("..", "..", "..", "testdata", "parquet", "*.parquet"))
	if err != nil || len(paths) == 0 {
		f.Fatalf("the Parquet fixtures: %v, %d files", err, len(paths))
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			f.Fatal(err)
		}
		seeds = append(seeds, b)
	}
	return seeds
}

// FuzzOwnPagesMatchArrow (step 510, step 449 of v0.7.2-plan.md): any bytes, read as
// a Parquet file with the reader's own page decoding (pages.go) and through
// arrow-go's column reader, batch for batch, answer the same wherever both answer.
// Both read on one thread, in batches of the fuzzer's size, every page decompressed
// into a bounded, poisoned allocator's buffers, inside fuzzcheck's limits. An input
// only one of them reads is not judged: either may refuse a corruption the other
// does not look for.
func FuzzOwnPagesMatchArrow(f *testing.F) {
	for i, b := range twinSeeds(f) {
		f.Add(b, uint16([]int{1023, 332}[i%2]))
	}
	f.Fuzz(func(t *testing.T, b []byte, batch uint16) {
		if why, ok := twinFindings[fmt.Sprintf("%x", sha256.Sum256(b))]; ok {
			t.Skip("finding: " + why)
		}
		size := 1 + int(batch)%4096
		mem := fuzzcheck.NewAllocator(len(b))
		mem.Poison = true
		defer func(was memory.Allocator) { pages = was }(pages)
		pages = mem
		lim := fuzzcheck.Limits{Wall: 10 * time.Second, Alloc: 64<<20 + 1024*int64(len(b)), Mem: mem}
		fuzzcheck.Run(t, lim, func() error {
			own, errOwn := readTwin(t.Context(), b, size, true)
			theirs, errTheirs := readTwin(t.Context(), b, size, false)
			if errOwn != nil || errTheirs != nil {
				return errors.Join(errOwn, errTheirs)
			}
			if len(own) != len(theirs) {
				t.Fatalf("batches of %d: %d batches read from the pages, %d through arrow-go", size, len(own), len(theirs))
			}
			for i := range own {
				if own[i] != theirs[i] {
					t.Fatalf("batches of %d: batch %d differs\npages:\n%.3000s\narrow-go:\n%.3000s", size, i, own[i], theirs[i])
				}
			}
			return nil
		})
	})
}

// twinFindings are inputs FuzzOwnPagesMatchArrow failed on that are left for the
// maintainer, by the SHA-256 of their bytes, each kept in its corpus under the name
// the reason gives (step 510's record, §4). None is left: step 517 fixed F9, whose
// input, dictionary-index-past-its-dictionary, was the one.
var twinFindings = map[string]string{}

// readTwin reads b in batches of batch rows on one thread, with the reader's own
// pages on or off, each batch rendered value by value.
func readTwin(ctx context.Context, b []byte, batch int, own bool) (_ []string, err error) {
	defer func(was bool) { ownPages = was }(ownPages)
	ownPages = own
	s := New([]Opener{func(context.Context) (parquet.ReaderAtSeeker, io.Closer, error) {
		return bytes.NewReader(b), nil, nil
	}}, "fuzz.parquet", Options{})
	bs, err := s.Open(ctx, source.ScanSpec{Threads: 1, BatchSize: batch})
	if err != nil {
		return nil, err
	}
	defer bs.Close()
	var out []string
	for {
		bt, err := bs.Next(ctx)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		r, err := renderBatch(bt)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
}

// renderBatch is b's values as text, a line a row: each through arrow-go's ValueStr
// of the column exported, so a Categorical is compared by its values and not by its
// codes, which the two readers may number apart.
func renderBatch(b *data.Batch) (string, error) {
	rec, err := arrowout.Record(b)
	if err != nil {
		return "", err
	}
	defer rec.Release()
	var sb strings.Builder
	for r := range int(rec.NumRows()) {
		for c := range int(rec.NumCols()) {
			col := rec.Column(c)
			if c > 0 {
				sb.WriteString(" | ")
			}
			if col.IsNull(r) {
				sb.WriteString("null")
			} else {
				sb.WriteString(col.ValueStr(r))
			}
		}
		sb.WriteByte('\n')
	}
	return sb.String(), nil
}

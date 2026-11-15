package readfuzz_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/file"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/fuzzcheck"
	"github.com/advenn/ursus/ursustest"
)

// TestAMalformedDeltaPageIsRefusedNotHungOn (step 531, F18 of step 521's record): a
// delta-encoded data page whose run the format does not allow, in each kind of
// column chunk arrow-go's column reader decodes one (pqDeltaKinds), each field of its
// header wrong, a miniblock wider than 64 bits, the page ending inside the run
// (pqDeltaForgeries). arrow-go's decoder checked none of them: a count of values
// short of a REQUIRED column's page hung its column reader, F18's input, whose count
// was 0 of 5. Each is refused now as corrupt, an I/O error naming the page's
// encoding, before arrow-go decodes the page, within fuzzcheck.Run's bounds of ten
// seconds and 64 MiB.
//
// What the format allows, each read as the PLAIN page of the same values: the run in
// one miniblock of 128 values a block, and in blocks of 256; a miniblock past the last
// value giving a width of 255; and, in an OPTIONAL column, a count one past the
// values that are not null and within the page's, its extra value in the padding.
func TestAMalformedDeltaPageIsRefusedNotHungOn(t *testing.T) {
	read := func(b []byte) (*ursus.DataFrame, error) {
		return ursus.ScanParquetBytes(b, "forged.parquet").Collect(t.Context())
	}
	for _, k := range pqDeltaKinds() {
		t.Run(k.name, func(t *testing.T) {
			want, err := read(k.file(k.plain()))
			if err != nil {
				t.Fatalf("the PLAIN page: %v", err)
			}
			if want.Height() != pqDeltaRows {
				t.Fatalf("the PLAIN page reads as %d rows, not %d", want.Height(), pqDeltaRows)
			}
			encoding := map[int32]string{pqDeltaBinary: "DELTA_BINARY_PACKED", pqDeltaLength: "DELTA_LENGTH_BYTE_ARRAY",
				pqDeltaBytes: "DELTA_BYTE_ARRAY"}[k.enc]
			for _, c := range k.forged() {
				fuzzcheck.Run(t, limits(len(c.b)), func() error {
					df, err := read(c.b)
					switch {
					case c.refused && err == nil:
						t.Errorf("%s: read as %d rows, and no error", c.name, df.Height())
					case c.refused && (!errors.Is(err, ursus.ErrIO) || !strings.Contains(err.Error(), "corrupt: column") ||
						!strings.Contains(err.Error(), encoding)):
						t.Errorf("%s: %v, not an I/O error naming the page as a corrupt %s", c.name, err, encoding)
					case !c.refused && err != nil:
						t.Errorf("%s: %v", c.name, err)
					case !c.refused:
						ursustest.AssertFrameEqual(t, df, want)
					}
					return err
				})
			}
		})
	}
}

// TestTheDeltaForgeriesAreWhatTheySay: the forged runs' own guards. Each kind's runs
// end in a block whose last miniblock holding values is not full and whose bits do
// not end on a byte, so a cut inside its values, a cut inside its padding and a count
// one past the values each mean what they say; and a miniblock after it holds none.
func TestTheDeltaForgeriesAreWhatTheySay(t *testing.T) {
	for _, k := range pqDeltaKinds() {
		vals, _ := k.runs()
		for i, v := range vals {
			r := pqDeltaOf(v, 128, 4)
			last := r.blocks[len(r.blocks)-1]
			m := len(last.bodies) - 1
			need := (last.held[m]*int(last.widths[m]) + 7) / 8
			if last.held[m] >= 32 || need < 2 || need >= len(last.bodies[m]) || len(last.bodies) == len(last.widths) {
				t.Errorf("%s, run %d: its last miniblock holds %d values of %d bits, %d bytes of %d, and is the "+
					"block's %d of %d", k.name, i, last.held[m], last.widths[m], need, len(last.bodies[m]), m+1,
					len(last.widths))
			}
		}
	}
}

// deltaVerdicts is testdata/oracle/delta/verdicts.json: what pyarrow read of each
// forged file of TestDeltaPagesAgainstPyarrow, recorded by
// bench/scripts/oracle/record_delta.py.
type deltaVerdicts struct {
	Pyarrow   string `json:"pyarrow"`
	Generator string `json:"generator"`
	Files     map[string]struct {
		SHA256  string `json:"sha256"`
		Twin    string `json:"twin"`    // the file of the same values PLAIN
		Pyarrow string `json:"pyarrow"` // S read as its twin, D read as other values, E refused
		Error   string `json:"error"`
	} `json:"files"`
}

// deltaFiles are TestDeltaPagesAgainstPyarrow's files by name: each kind's forged
// files, and its twin, the PLAIN page of its values.
func deltaFiles() (forged map[string]pqDeltaCase, twins map[string][]byte) {
	forged, twins = map[string]pqDeltaCase{}, map[string][]byte{}
	for _, k := range pqDeltaKinds() {
		twins[pqSlug(k.name)+"--plain.parquet"] = k.file(k.plain())
		for _, c := range k.forged() {
			forged[pqSlug(k.name)+"--"+pqSlug(c.name)+".parquet"] = c
		}
	}
	return forged, twins
}

// TestDeltaPagesAgainstPyarrow (step 531): the delta-encoded pages'
// check against pyarrow 25.0.1's verdict on the same forged files, recorded once.
// Where pyarrow refuses a file, the reader must refuse it; where pyarrow reads it as
// its PLAIN twin, the reader must read it so; and the reader never reads one as other
// values. The two known disagreements are deltaDisagreement's.
//
// To record again: URSUS_DELTA_FORGERIES=<dir> runs this test to write the files to
// <dir> and stop, and record_delta.py reads them (its docstring).
func TestDeltaPagesAgainstPyarrow(t *testing.T) {
	forged, twins := deltaFiles()
	if dir := os.Getenv("URSUS_DELTA_FORGERIES"); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, c := range forged {
			twins[name] = c.b
		}
		for name, b := range twins {
			if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("wrote %d files to %s", len(twins), dir)
		return
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "oracle", "delta", "verdicts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rec deltaVerdicts
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	if len(rec.Files) != len(forged) {
		t.Fatalf("%d files recorded, %d forged: run %s", len(rec.Files), len(forged), rec.Generator)
	}
	tally := map[string]int{}
	for _, name := range slices.Sorted(maps.Keys(forged)) {
		c, v := forged[name], rec.Files[name]
		sum := sha256.Sum256(c.b)
		if hex.EncodeToString(sum[:]) != v.SHA256 {
			t.Fatalf("%s is not the file pyarrow's verdict was recorded of: run %s", name, rec.Generator)
		}
		twin, ok := twins[v.Twin]
		if !ok {
			t.Fatalf("%s: its twin %s is not forged", name, v.Twin)
		}
		want, err := ursus.ScanParquetBytes(twin, v.Twin).Collect(t.Context())
		if err != nil {
			t.Fatalf("%s: %v", v.Twin, err)
		}
		ours := "E"
		fuzzcheck.Run(t, limits(len(c.b)), func() error {
			df, err := ursus.ScanParquetBytes(c.b, name).Collect(t.Context())
			if err != nil {
				return err
			}
			same, err := df.Equals(t.Context(), want, true)
			if err != nil {
				return err
			}
			ours = map[bool]string{true: "S", false: "D"}[same]
			return nil
		})
		tally[ours+v.Pyarrow]++
		why := deltaDisagreement(c, ours, v.Pyarrow)
		switch {
		case ours == "D":
			t.Errorf("%s: the reader answers other values than its PLAIN twin", name)
		case ours != v.Pyarrow && why == "":
			t.Errorf("%s: the reader answers %s, pyarrow %s (%s)", name, ours, v.Pyarrow, v.Error)
		case ours != v.Pyarrow:
			t.Logf("%s: the reader answers %s, pyarrow %s (%s): %s", name, ours, v.Pyarrow, v.Error, why)
		}
	}
	var lines string
	for _, k := range slices.Sorted(maps.Keys(tally)) {
		lines += fmt.Sprintf(" %s %d", k, tally[k])
	}
	t.Logf("%d forged files: (reader, pyarrow) outcomes:%s", len(forged), lines)
}

// deltaDisagreement is why the reader answers ours of forged file c and pyarrow
// 25.0.1 theirs, where the two are known to answer apart; empty elsewhere.
//
//   - An INT32's deltas spanning more than 32 bits: arrow-go's writer takes an INT32's
//     deltas as an int64's and packs them 33 or 34 bits wide, and arrow-go reads them
//     back; pyarrow fails the page ("Unexpected end of stream").
//   - A miniblock of more values than its run's count and than 65,536: the reader
//     refuses it, as arrow-go makes a slice of a miniblock's values before it decodes
//     one (deltapage.go); pyarrow decodes into its output and reads the page, here as
//     other values than the twin's, the forged run's.
//   - A count of values past the page's or its column chunk's: the reader refuses it,
//     as the format's count is of the page's values and arrow-go's decoder of lengths
//     makes a slice of it before it decodes one; pyarrow decodes as many values as the
//     page's levels ask for, and reads the file. A count of 2^40 pyarrow refuses too,
//     reading the header's fields as 32 bits.
func deltaDisagreement(c pqDeltaCase, ours, theirs string) string {
	switch {
	case c.wide && ours == "S" && theirs == "E":
		return "an INT32's deltas packed wider than 32 bits, as arrow-go's writer packs them"
	case ours != "E" || theirs == "E":
	case c.per > max(c.count, 1<<16):
		return "a miniblock of more values than its count and than 65,536"
	case c.count > pqDeltaRows:
		return "a count of values past the page's or its column chunk's"
	}
	return ""
}

// TestPyarrowDeltaFilesReadAsTheirPlainTwin (step 531): the delta-encoded pages
// pyarrow 25.0.1 writes, from bench/scripts/oracle/write_delta.py, read as their PLAIN
// twin, the same table with no encoding but PLAIN, on one thread and on four: every
// integer column under DELTA_BINARY_PACKED, INT64 and INT32 spanning their types'
// ranges, strings and binaries under DELTA_LENGTH_BYTE_ARRAY and DELTA_BYTE_ARRAY, a
// List's element and a Struct's fields, V1 pages uncompressed and V2 under
// Zstandard, in pages of 256 bytes. No page a writer made is refused by the check.
// Polars 2.1.0 writes no delta-encoded page.
func TestPyarrowDeltaFilesReadAsTheirPlainTwin(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "parquet", "delta")
	read := func(name string, threads int) *ursus.DataFrame {
		t.Helper()
		df, err := ursus.ScanParquet(filepath.Join(dir, name)).Collect(t.Context(), ursus.WithThreads(threads))
		if err != nil {
			t.Fatalf("%s, %d threads: %v", name, threads, err)
		}
		return df
	}
	want := read("pyarrow_plain.parquet", 1)
	if want.Height() != 500 || want.Width() != 11 {
		t.Fatalf("the PLAIN twin is %d rows of %d columns", want.Height(), want.Width())
	}
	for _, name := range []string{"pyarrow_delta_v1.parquet", "pyarrow_delta_v2_zstd.parquet"} {
		pf, err := file.OpenParquetFile(filepath.Join(dir, name), false)
		if err != nil {
			t.Fatal(err)
		}
		md := pf.MetaData()
		for g := range md.NumRowGroups() {
			for c := range md.Schema.NumColumns() {
				cc, err := md.RowGroup(g).ColumnChunk(c)
				if err != nil {
					t.Fatal(err)
				}
				if !slices.ContainsFunc(cc.Encodings(), func(e parquet.Encoding) bool {
					return e == parquet.Encodings.DeltaBinaryPacked || e == parquet.Encodings.DeltaLengthByteArray ||
						e == parquet.Encodings.DeltaByteArray
				}) {
					t.Errorf("%s: row group %d's column %s lists %v, no delta encoding", name, g,
						md.Schema.Column(c).Path(), cc.Encodings())
				}
			}
		}
		pf.Close()
		for _, threads := range []int{1, 4} {
			ursustest.AssertFrameEqual(t, read(name, threads), want)
		}
	}
}

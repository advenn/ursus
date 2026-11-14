package readfuzz_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/fuzzcheck"
	"github.com/advenn/ursus/ursustest"
)

// flipRecord is testdata/oracle/flips/flips.json: what DuckDB and pyarrow read of
// each byte of each file flipped, recorded by bench/scripts/oracle/record_flips.py.
type flipRecord struct {
	DuckDB    string `json:"duckdb"`
	Pyarrow   string `json:"pyarrow"`
	Flip      string `json:"flip"`
	Generator string `json:"generator"`
	Files     map[string]struct {
		SHA256   string `json:"sha256"`
		Original struct {
			I []int64    `json:"i"`
			F []*float64 `json:"f"`
			S []string   `json:"s"`
		} `json:"original"`
		DuckDB  string `json:"duckdb"`  // an offset's outcome a letter: S the original, D another answer, E an error
		Pyarrow string `json:"pyarrow"` // the same
	} `json:"files"`
}

// TestParquetByteFlipsAgainstDuckDBAndPyarrow (step 510, step 444 of
// v0.7.2-plan.md): every byte of two files ursus wrote, H2's 300 rows of an Int64,
// a nullable Float64 and a String, uncompressed and with Snappy, flipped in turn
// (XOR 0x5A) and read by ursus on one thread inside fuzzcheck's limits. Where
// pyarrow 25.0.1 reads the flipped file as the original, ursus must not answer
// another frame: a byte the format's reference reader passes over, or reads past,
// is one ursus must not misread. An error there is a refusal, allowed; a panic, a
// hang or an allocation past the bound fails as in every target. The original reads
// as both engines read it, and the tally of outcomes is logged.
//
// The plan asked for both engines to read the original. DuckDB 1.5.5 is recorded
// too, and is not required: it reads a row group whose num_rows says 257 of its 300
// rows as 257 rows, no error, as ursus did before step 416. Required, it would let
// that bug back (H2's recording, which had DuckDB read all 300, read the unflipped
// file from DuckDB's file cache: record_flips.py).
//
// Under the race detector every eighth offset is read.
func TestParquetByteFlipsAgainstDuckDBAndPyarrow(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "oracle", "flips")
	raw, err := os.ReadFile(filepath.Join(dir, "flips.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rec flipRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	if len(rec.Files) < 2 {
		t.Fatalf("%d files recorded", len(rec.Files))
	}
	for _, name := range slices.Sorted(maps.Keys(rec.Files)) {
		f := rec.Files[name]
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != f.SHA256 || len(f.DuckDB) != len(b) ||
			len(f.Pyarrow) != len(b) {
			t.Fatalf("%s is not the file the flips were recorded of: run %s", name, rec.Generator)
		}
		vals := make([]float64, len(f.Original.F))
		valid := make([]bool, len(f.Original.F))
		for i, v := range f.Original.F {
			if v != nil {
				vals[i], valid[i] = *v, true
			}
		}
		want, err := ursus.Frame(ursus.Values("i", f.Original.I), ursus.ValuesNullable("f", vals, valid),
			ursus.Values("s", f.Original.S)).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		orig, err := ursus.ScanParquetBytes(b, name).Collect(t.Context(), ursus.WithThreads(1))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		ursustest.AssertFrameEqual(t, orig, want)

		tally := map[string]int{}
		flipped := slices.Clone(b)
		for i := 0; i < len(b); i += flipStride {
			flipped[i] ^= 0x5A
			outcome := "E"
			fuzzcheck.Run(t, limits(len(b)), func() error {
				got, err := ursus.ScanParquetBytes(flipped, name).Collect(t.Context(), ursus.WithThreads(1))
				if err != nil {
					return err
				}
				same, err := got.Equals(t.Context(), want, true)
				if err != nil {
					return err
				}
				outcome = map[bool]string{true: "S", false: "D"}[same]
				return nil
			})
			flipped[i] ^= 0x5A
			duck, arrow := f.DuckDB[i:i+1], f.Pyarrow[i:i+1]
			tally[outcome+duck+arrow]++
			if outcome == "D" && arrow == "S" {
				t.Errorf("%s, byte %d flipped: ursus answers another frame, where pyarrow reads the original "+
					"(DuckDB: %s)", name, i, duck)
			}
		}
		var lines string
		for _, k := range slices.Sorted(maps.Keys(tally)) {
			lines += fmt.Sprintf(" %s %d", k, tally[k])
		}
		t.Logf("%s, %d bytes, each flipped: (ursus, DuckDB, pyarrow) outcomes:%s", name, len(b), lines)
	}
}

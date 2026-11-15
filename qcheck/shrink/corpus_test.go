package shrink

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/advenn/ursus/internal/fuzzcheck"
)

// corpusDir is qcheck's corpus of shrunk cases.
const corpusDir = "../testdata/corpus"

// TestCorpus replays every shrunk case of the corpus, the ratchet: a case that
// fails and is not marked open fails the test, so a finding is fixed or marked open
// with the step that fixes it; a case marked open that no longer fails fails it as
// stale, so its mark is removed once it is fixed and it stays a regression; a case
// that fails another way fails it too. A DuckDB case is replayed by package duck's
// TestCorpusAgainstDuckDB, in a build that links DuckDB.
func TestCorpus(t *testing.T) {
	cases, paths, err := LoadCorpus(corpusDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatalf("no case in %s", corpusDir)
	}
	open, duck := 0, 0
	for i, c := range cases {
		name := strings.TrimSuffix(filepath.Base(paths[i]), ".json")
		if name != c.Name() {
			t.Errorf("%s holds the case %s", paths[i], c.Name())
		}
		if c.Signature.Zero() {
			t.Errorf("%s names no signature", name)
		}
		if c.Mode == ModeDuck {
			duck++
			continue
		}
		r, err := Replay(c, nil, fuzzcheck.Limits{})
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if err := Judge(c, r); err != nil {
			t.Error(err)
		}
		if c.Open != "" {
			open++
		}
		t.Logf("%s: %v, open %q, failed %v in %d run(s)", name, c.Signature, c.Open, r.Reproduced, r.Runs)
	}
	t.Logf("%d cases, %d of them open, %d left to package duck", len(cases), open, duck)
}

// TestTheRatchetBites: a case that fails and is not marked open fails the
// ratchet, as one marked open that passes does, and one marked open that fails
// and one not marked that passes hold it. The planted bug makes a shrunk case
// fail; without it the case passes.
func TestTheRatchetBites(t *testing.T) {
	plantBug(t)
	res, err := Shrink(plantedCases(t, 514_000, 1, ModeDifferential)[0], Options{})
	if err != nil {
		t.Fatal(err)
	}
	c := roundTrip(t, res.Case)
	verdict := func(open string) error {
		c.Open = open
		r, err := Replay(c, nil, fuzzcheck.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		return Judge(c, r)
	}
	if err := verdict(""); err == nil || !strings.Contains(err.Error(), "not marked open") {
		t.Fatalf("a failing case not marked open: %v", err)
	}
	if err := verdict("step 0: the planted bug"); err != nil {
		t.Fatalf("a failing case marked open: %v", err)
	}
	corrupt = nil
	if err := verdict("step 0: the planted bug"); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("a passing case marked open: %v", err)
	}
	if err := verdict(""); err != nil {
		t.Fatalf("a passing case not marked: %v", err)
	}
}

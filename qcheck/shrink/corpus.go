package shrink

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/advenn/ursus/internal/fuzzcheck"
	"github.com/advenn/ursus/qcheck/compare"
	"github.com/advenn/ursus/qcheck/model"
)

// LoadCorpus reads every case in dir, by file name.
func LoadCorpus(dir string) ([]*Case, []string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, nil, err
	}
	slices.Sort(paths)
	var cases []*Case
	for _, p := range paths {
		c, err := Load(p)
		if err != nil {
			return nil, nil, err
		}
		cases = append(cases, c)
	}
	return cases, paths, nil
}

// Replayed is a replay of a case.
type Replayed struct {
	// Reproduced is whether a run failed with the case's signature.
	Reproduced bool
	// Other is a failure of another signature, which is a finding of its own.
	Other  Signature
	Detail string
	Runs   int
}

// replayTries is how many times a case runs before it is taken not to fail: a
// finding over several threads may fail only most of the time.
const replayTries = 3

// Replay runs c as it is, up to three times, until it fails with its signature;
// engine is DuckDB for a ModeDuck case.
func Replay(c *Case, engine compare.Engine, lim fuzzcheck.Limits) (Replayed, error) {
	k := newChecker(c.Mode, engine, lim)
	ref := c.Reference
	if ref == (model.Config{}) {
		ref = model.Reference()
	}
	st := &state{tables: c.Tables, q: c.Query, ref: ref, cfg: c.Config}
	tables, err := st.tableMap()
	if err != nil {
		return Replayed{}, err
	}
	k.joinBound = max(maxJoinRows, joinMatches(st.q, tables))
	var r Replayed
	for range replayTries {
		o := k.check(st)
		r.Runs++
		switch {
		case o.invalid != "":
			return r, fmt.Errorf("not a case: %s", o.invalid)
		case o.sig == c.Signature:
			r.Reproduced, r.Detail = true, o.detail
			return r, nil
		case !o.sig.Zero():
			r.Other, r.Detail = o.sig, o.detail
			return r, nil
		}
	}
	return r, nil
}

// Judge is the corpus ratchet's verdict on a replay of c: nil where it holds. A
// case that fails and is not marked open fails it, as one marked open that no
// longer fails does, which is then a regression with its mark removed; a case that
// fails another way fails it too.
func Judge(c *Case, r Replayed) error {
	switch {
	case !r.Other.Zero():
		return fmt.Errorf("%s fails as %v, not as its signature %v:\n%s", c.Name(), r.Other, c.Signature,
			indentLines(r.Detail, "  "))
	case r.Reproduced && c.Open == "":
		return fmt.Errorf("%s fails as %v and is not marked open: fix it, or mark it open with the step "+
			"that fixes it:\n%s", c.Name(), c.Signature, indentLines(r.Detail, "  "))
	case !r.Reproduced && c.Open != "":
		return fmt.Errorf("%s is marked open (%s) but no longer fails as %v in %d runs: it is stale; remove "+
			"the mark, and it stays a regression", c.Name(), c.Open, c.Signature, r.Runs)
	}
	return nil
}

// WriteArtifacts writes a shrunk case's file and what is emitted from it into dir:
// NAME.json, NAME_test.go, and NAME.sql for a DuckDB case and NAME.py where the
// Polars snippet could be written. It returns the paths written and why any
// emitter declined.
func WriteArtifacts(dir string, c *Case, detail string) (paths, notes []string, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, err
	}
	name := filepath.Join(dir, c.Name())
	if err := Save(c, name+".json"); err != nil {
		return nil, nil, err
	}
	paths = append(paths, name+".json")
	write := func(path, src string) error {
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			return err
		}
		paths = append(paths, path)
		return nil
	}
	if src, err := EmitGo(c, detail); err != nil {
		notes = append(notes, "no Go test: "+err.Error())
	} else if err := write(name+"_test.go", src); err != nil {
		return paths, notes, err
	}
	if c.Source == SourceDuck {
		if src, err := EmitSQL(c); err != nil {
			notes = append(notes, "no SQL: "+err.Error())
		} else if err := write(name+".sql", src); err != nil {
			return paths, notes, err
		}
	}
	src, unmapped, err := EmitPolars(c)
	switch {
	case err != nil:
		notes = append(notes, "no Polars snippet: "+err.Error())
	case len(unmapped) > 0:
		notes = append(notes, "no Polars snippet: "+strings.Join(unmapped, "; "))
	default:
		if err := write(name+".py", src); err != nil {
			return paths, notes, err
		}
	}
	return paths, notes, nil
}

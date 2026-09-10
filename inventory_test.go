package ursus_test

// Instruments that FAIL rather than going quiet.
//
// Every guard in this repository is a list — a list of pushdown arms, a list of
// plans, a list of golden files — and every one was written against the node set
// that existed the day it was written. A list does not fail when the code grows past
// it; it goes SILENT, and silence reads exactly like green. That is how Explode and
// Unnest went from steps 30 and 35 to step 48 with no projection-pushdown arm: every
// list simply did not mention them.
//
// The two fixes that have broken that pattern are the template for this file:
// Resolve's default arm, which enforces the function's own documented postcondition
// and so covers "every node added later"; and TestPushdownSoundness's anti-vacuity
// counter, which fails when no assertion ran. Total by construction, or loud when it
// stops checking.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// repoFiles walks the module, calling fn for every .go file outside .git and bench.
func repoFiles(t *testing.T, fn func(path string, src []byte)) {
	t.Helper()
	root := ".."
	if _, err := os.Stat("go.mod"); err == nil {
		root = "."
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "bench", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fn(path, src)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

var citedTest = regexp.MustCompile(`\bTest[A-Z][A-Za-z0-9_]*`)

// TestEveryCitedTestExists closes the phantom-instrument class.
//
// eval.go's doc claimed "TestEvaluatorContract asserts it across the whole op ×
// dtype matrix" and no such test existed anywhere in the repository — the contract
// most likely to be violated, guarded by a sentence. It is the third instance of the
// shape: step 13 found a join test cited only by the comment naming it, and step 2
// found rejectAggregate documented as guarding Select and never called from it.
//
// This is total by construction: it reads every comment in the module rather than a
// list of the ones someone remembered.
func TestEveryCitedTestExists(t *testing.T) {
	defined := map[string]string{}
	cited := map[string]string{}
	wrapped := map[string]string{}

	repoFiles(t, func(path string, src []byte) {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
		if err != nil {
			return // not our business here; the build catches it
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
				defined[fn.Name.Name] = path
			}
		}
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				for _, loc := range citedTest.FindAllStringIndex(c.Text, -1) {
					name := c.Text[loc[0]:loc[1]]
					// A name touching the end of the line may have been WRAPPED:
					// gofmt splits a long identifier across two comment lines, and
					// reporting the first half of one is a false
					// alarm. An instrument that cries wolf gets switched off, so
					// these are recorded separately and satisfied by any defined
					// name they prefix.
					if loc[1] == len(strings.TrimRight(c.Text, " \t")) {
						if _, seen := wrapped[name]; !seen {
							wrapped[name] = path
						}
						continue
					}
					if _, seen := cited[name]; !seen {
						cited[name] = path
					}
				}
			}
		}
	})

	if len(defined) < 100 {
		t.Fatalf("found only %d test functions — the walk is not reaching the "+
			"repository, so this check has gone vacuous", len(defined))
	}

	var missing []string
	for name, where := range cited {
		// TestMain is a language convention, not a citation: a comment saying "set
		// from a TestMain" names the mechanism rather than a specific function.
		if name == "TestMain" {
			continue
		}
		if _, ok := defined[name]; !ok {
			missing = append(missing, name+" (cited in "+where+")")
		}
	}
	for name, where := range wrapped {
		if _, ok := defined[name]; ok {
			continue
		}
		var found bool
		for d := range defined {
			if strings.HasPrefix(d, name) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, name+"... (line-wrapped, cited in "+where+")")
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("a comment cites a test that does not exist: %s", m)
	}
}

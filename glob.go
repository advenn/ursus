package ursus

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/advenn/ursus/internal/uerr"
)

// Globs (step 349): Polars' glob patterns, whose ** matches any number of
// directories, none among them, where Go's filepath.Glob matches one name an element.

// globFiles is the files pattern matches, sorted: filepath.Match's syntax in each
// element of the pattern, and an element of ** any number of directories. A pattern
// with no ** is filepath.Glob's, directories and all, as before.
func globFiles(pattern string) ([]string, error) {
	if !strings.Contains(pattern, "**") {
		return filepath.Glob(pattern)
	}
	elems := strings.Split(filepath.ToSlash(pattern), "/")
	// The walk starts at the elements before the first with a wildcard.
	k := 0
	for k < len(elems) && !strings.ContainsAny(elems[k], "*?[\\") {
		k++
	}
	root := strings.Join(elems[:k], "/")
	switch {
	case root == "" && strings.HasPrefix(filepath.ToSlash(pattern), "/"):
		root = "/"
	case root == "":
		root = "."
	}
	rest := elems[k:]
	for _, e := range rest {
		if e != "**" {
			if _, err := filepath.Match(e, ""); err != nil {
				return nil, err
			}
		}
	}
	var out []string
	err := filepath.WalkDir(filepath.FromSlash(root), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == filepath.FromSlash(root) {
				return fs.SkipAll // no such root: no match, as filepath.Glob
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(filepath.FromSlash(root), p)
		if err != nil {
			return nil
		}
		if matchElems(filepath.Match, rest, strings.Split(filepath.ToSlash(rel), "/")) {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// matchElems reports whether the path's elements match the pattern's by match, **
// any run of them, none among them.
func matchElems(match func(pattern, name string) (bool, error), pat, path []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(path); i++ {
				if matchElems(match, pat[1:], path[i:]) {
					return true
				}
			}
			return false
		}
		if len(path) == 0 {
			return false
		}
		if ok, _ := match(pat[0], path[0]); !ok {
			return false
		}
		pat, path = pat[1:], path[1:]
	}
	return len(path) == 0
}

// datasetFiles is every file below dir, as Polars reads a directory, sorted by path:
// a name beginning '.' or '_', a file's or a directory's, is passed over, as Spark and
// Hive name what they keep beside the data: _SUCCESS, _temporary, .crc files. Files of
// two extensions are refused, as Polars refuses them: a glob names which to read.
func datasetFiles(op, dir string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case p != dir && (strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_")):
			if d.IsDir() {
				return filepath.SkipDir
			}
		case !d.IsDir():
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return nil, uerr.Wrap(err, uerr.KindIO, op, "listing %s", dir)
	}
	if len(paths) == 0 {
		return nil, uerr.New(uerr.KindIO, op, "no files in %s", dir)
	}
	sort.Strings(paths)
	for _, p := range paths[1:] {
		if filepath.Ext(p) != filepath.Ext(paths[0]) {
			// Polars: "directory contained paths with different file extensions".
			return nil, uerr.New(uerr.KindValue, op, "%s holds files of two extensions: %s and %s", dir, paths[0], p).
				Hint("a glob, %s, names the files to read", filepath.Join(dir, "**", "*"+filepath.Ext(paths[0])))
		}
	}
	return paths, nil
}

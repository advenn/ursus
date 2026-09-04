// Command levels enforces ursus's import-level invariant:
//
//	a package may import strictly lower levels only.
//
// This is what keeps the layout acyclic and, more importantly, keeps it
// *comprehensible*: the root package must never import selector, sql, or
// ursustest, and the engine must never import the root. Both are easy to violate
// with a single convenience method, and neither produces a compile error until
// the cycle actually closes — by which point the fix is a refactor.
//
// Run via `make levels`; wired into CI.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

const modulePath = "ursus"

// levels maps a package path (module-relative) to its level. Longest-prefix wins,
// so "internal/source/parquet" inherits from "internal/source" unless listed.
//
// Numbers are spaced by 10 so a package can be inserted between two existing
// levels without renumbering the world.
var levels = map[string]int{
	// L0 — leaves. Import stdlib and third-party only, never another ursus package.
	"internal/uerr":   0, // stdlib only
	"internal/arrowx": 0, // arrow-go only: the allocator and the ownership model
	"objstore":        0, // stdlib only
	"i128":            0, // stdlib only (math/big is a TEST-only import)
	"internal/gen":    0, // build tooling

	"internal/bitmap": 5, // + arrowx

	"dtype": 10, // + uerr. The public type system.

	// L20 — the two independent IRs over the type system.
	"internal/data": 20, // + dtype, bitmap, arrowx, uerr — Column/Batch/Series
	"internal/expr": 20, // + dtype, uerr — the expression IR

	"internal/spill": 25, // + data, dtype, bitmap, arrowx — the run format

	"internal/kernel":  30, // + data, bitmap
	"internal/source":  30, // + dtype, data, expr, uerr
	"internal/scanopt": 30,
	"internal/execopt": 30,

	"internal/plan":           40, // + expr, source, dtype
	"internal/source/memsrc":  45, // concrete sources sit above the contract
	"internal/source/parquet": 45,
	"internal/source/csv":     45,
	"internal/source/testsrc": 45, // the deliberately-misbehaving source

	"internal/physical": 50, // + plan, kernel, data, source
	"internal/exec":     60, // + physical, plan

	"": 70, // the root package

	// L80 — public packages that speak the IR directly. They may reach into
	// internal/ (which is module-scoped, not package-scoped); that is exactly what
	// lets them avoid round-tripping through the root and creating a cycle.
	"selector":  80,
	"sql":       80,
	"ursustest": 80,
}

func levelOf(rel string) (int, bool) {
	best, bestLen, found := 0, -1, false
	for prefix, lvl := range levels {
		if prefix == "" {
			if rel == "" {
				return lvl, true
			}
			continue
		}
		if rel == prefix || strings.HasPrefix(rel, prefix+"/") {
			if len(prefix) > bestLen {
				best, bestLen, found = lvl, len(prefix), true
			}
		}
	}
	return best, found
}

func rel(pkg string) (string, bool) {
	if pkg == modulePath {
		return "", true
	}
	if s, ok := strings.CutPrefix(pkg, modulePath+"/"); ok {
		return s, true
	}
	return "", false // not ours (stdlib or third-party)
}

func main() {
	out, err := exec.Command("go", "list", "-deps=false",
		"-f", "{{.ImportPath}}\t{{join .Imports \",\"}}", "./...").Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "levels: go list failed: %v\n", err)
		os.Exit(1)
	}

	var violations []string
	var unknown []string

	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		path, importList, _ := strings.Cut(line, "\t")
		src, ok := rel(path)
		if !ok {
			continue
		}
		srcLvl, known := levelOf(src)
		if !known {
			unknown = append(unknown, path)
			continue
		}
		for imp := range strings.SplitSeq(importList, ",") {
			dst, ok := rel(imp)
			if !ok || imp == "" {
				continue
			}
			dstLvl, known := levelOf(dst)
			if !known {
				unknown = append(unknown, imp)
				continue
			}
			if dstLvl >= srcLvl {
				violations = append(violations, fmt.Sprintf(
					"  %s (L%d)\n      imports %s (L%d)", path, srcLvl, imp, dstLvl))
			}
		}
	}

	sort.Strings(violations)
	sort.Strings(unknown)

	if len(unknown) > 0 {
		fmt.Fprintf(os.Stderr, "levels: packages with no declared level "+
			"(add them to internal/gen/levels/main.go):\n")
		for _, u := range dedup(unknown) {
			fmt.Fprintf(os.Stderr, "  %s\n", u)
		}
		os.Exit(1)
	}

	if len(violations) > 0 {
		fmt.Fprintf(os.Stderr, "levels: %d import-level violation(s) — "+
			"a package may import strictly LOWER levels only:\n", len(violations))
		for _, v := range violations {
			fmt.Fprintln(os.Stderr, v)
		}
		os.Exit(1)
	}

	fmt.Println("levels: ok")
}

func dedup(xs []string) []string {
	out := xs[:0]
	var prev string
	for i, x := range xs {
		if i == 0 || x != prev {
			out = append(out, x)
		}
		prev = x
	}
	return out
}

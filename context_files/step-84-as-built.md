# Step 84 — as built

**Release readiness for v0.3.0.** The road to 0.3 ends here. This step changes no
Go code: it makes the README, the release notes and the scope document match the
code, and records what checked them. Tagging is left to the user, for the reason
in §4.

Two commits before this document: the README, then `CHANGELOG.md`.

Authoritative where it disagrees with the vision docs and [`design/`](./design/).

---

## 1. What was measured

- **v0.2.0 was tagged at "Rename the module", on 2026-09-04.** The first as-built
  after it is step 22's, so v0.3.0 is steps 22–83, about 250 commits. That is far
  more than "the road to 0.3", which began at step 74: nested data, Arrow both
  ways, UDFs, `JoinWhere`, `Unpivot`, and the whole audit all land in this release.
- **The README:**
  - it said "v0.2 is complete", and "This is v0.2";
  - its test count was 1610, where the suite has 2890;
  - its types row did not say Decimal computes or that an Enum can be built;
  - its sources row did not say Parquet writes nested columns, or that a caller can
    open the files;
  - step 82's longer first sentence chained two em-dashes.
- **Release notes:** none existed — no CHANGELOG, and an annotated tag reading only
  "v0.2.0".
- **The exported API since v0.2.0**, diffed identifier by identifier through
  `git show v0.2.0:…`:
  - **added:** 56 identifiers in the root package, 9 in `dtype` and 4 in `i128`;
  - **removed:** `dtype.DaysInterval` and `dtype.MonthsInterval`, deleted at step 66
    as unvalidated constructors nothing called;
  - **changed parameters:** none. Nine methods gained named results, which callers
    cannot see.
- **`go mod tidy`** gives no diff.
- **Cross-compiling** for windows/amd64, darwin/arm64 and linux/arm64 builds, which
  backs the README's "pure Go … cross-compiles" claim.

## 2. What changed

- **The README** now:
  - says v0.3, pointing at `CHANGELOG.md`;
  - has the current test count;
  - describes Decimal arithmetic and Enum construction in its types row;
  - names nested write and `ScanParquetFrom` / `ScanCSVFrom` in its sources row;
  - lists built-in object stores as not done, for 0.4;
  - splits its first sentence in two.

  The benchmark table stays as the checked-in report has it: no benchmark was
  re-run, per the standing rule.
- **`CHANGELOG.md`**, written for a v0.2 user:
  - the highlights;
  - the behaviour changes, by area, that could make an existing program answer
    differently or refuse;
  - the API added and removed;
  - the known limitations.

  Each entry carries its step number.
- **`v0.3-scope.md` §5:** each of the three sentences the release claim was to
  change is marked with the steps that changed it.

## 3. The gate

No Go code, `go.mod` or Makefile changed after `7f45057`, the last commit of step
83. Its gate stands for the release. Each check was asserted on its own exit code:

- `make test-all` exit 0, with **105** package-ok lines: 21 packages × 5 SIMD
  configurations. That includes the runnable examples, which `go test` checks.
- `make race` exit 0 (21).
- `make levels` and `go vet` clean in all three modules.
- PDS-H SF=0.1 exit 0, with **22/22** matching the duckdb reference.

**The suite is 2890 passing tests and subtests.**

## 4. The tag is the user's to make

A module version, once anything fetches it through the Go module proxy, is cached
there for good. A wrong `v0.3.0` cannot be withdrawn, only retracted. So the tag is
not pushed from here. To release:

1. Change `CHANGELOG.md`'s heading `v0.3.0 — unreleased` to the release date, and
   commit.
2. Then run:

```sh
git tag -a v0.3.0 -m "v0.3.0"
git push origin v0.3.0
```

## 5. Still open, for 0.4

- **Built-in object stores** (`v0.3-scope.md` §4): URI schemes, globbing through a
  `List`, retries, a sink that cannot rename, and a cached footer.
- **Operators that do not spill:** reverse, hstack, tail, `JoinAsOf`,
  `MergeSorted`, `Rolling`, `GroupByDynamic`, and a window with no partition key.
- **Nested types:** a List of Lists or of Structs, Array, and nested CSV.
- **The rest of `audit.md`'s open rows**, O14 among them.

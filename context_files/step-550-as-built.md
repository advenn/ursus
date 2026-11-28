# Step 550 — the module zip holds every file again

**A rename and a CI step.** v0.8.0 and v0.8.1 could not be fetched as modules: the
Go module proxy answered both with "create zip: ... malformed file path
\"internal/source/ndjson/testdata/fuzz/FuzzObjectLines/nul\": \"nul\" disallowed as
path element component on Windows". `go get` with `GOPROXY=direct` applies the same
check to the zip it builds, so the two tags failed every way. `sum.golang.org` holds
neither, and `go get @latest` resolved to v0.7.3.

The file is a fuzz seed of step 511, a line holding one NUL byte. Go's module zip
refuses a path element whose name before its first dot is one Windows reserves
(CON, PRN, AUX, NUL, COM1–9, LPT1–9), whatever the case.

## What changed

- **The seed** is `FuzzObjectLines/nul-byte`. Its content is unchanged, and no code
  names it.
- **CI's build job** runs `go mod download -json github.com/advenn/ursus@<commit>`
  with `GOPRIVATE` and `GOPROXY=direct`, outside the module. That builds the commit's
  zip from GitHub as the proxy does, with Go's own checks, so a path the zip refuses
  fails the pull request. On master at `a8d028f` it fails with the proxy's error; on
  this branch it passes.
- **Every tracked path** of the root module (2,641 files, the `qcheck`, `bench/micro`
  and `bench/engines/go` modules left out) was checked against the zip's rules: the
  characters a file name may hold, no trailing dot, no Windows reserved name, no tilde
  short name, and no two paths differing only in case. `nul` was the only one.

v0.8.2 is the first 0.8 release `go get` can fetch. Its tag is the maintainer's.

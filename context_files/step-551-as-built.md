# Step 551 — v0.8.2 prepared, not tagged

**Docs and the API list.** The CHANGELOG's "Unreleased — toward v0.8.2" is now
"v0.8.2 — 2026-11-28": step 545, a far Datetime's text, and step 550, the module zip.

- **The first 0.8 release `go get` can fetch.** v0.8.0's and v0.8.1's zips are
  invalid (step 550), so the section says so, and that a user moving from v0.7.x reads
  the trip lists of v0.8.0 and v0.8.1 too.
- **What a v0.8.1 user can trip on:** a Datetime(s) more than 10^12 days from 1970 is
  written as its own instant (step 545).
- **Changed:** five messages of corrupt-input errors are worded differently (step
  549), their kinds unchanged.
- **`api/v0.8.2.txt`**, by `make api-tag TAG=v0.8.2` (16 s, `GOEXPERIMENT=simd`), is
  v0.8.1's 2,481 features, identical but for its header. `make api` measures against
  it once the tag exists.
- **The README** names v0.8.2 as the last release.

The tag is the maintainer's, on this branch's merge commit into master, as v0.8.1's
was. Once it is pushed, `go list -m github.com/advenn/ursus@v0.8.2` through the proxy
is the check that it can be fetched.

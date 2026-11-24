# Step 541 — v0.8.1 prepared

**v0.8.1: v0.8.0 with arrow-go v18.9.0 and a Parquet fix.** Release preparation
only. The maintainer approved it on 2026-11-23. The tag is theirs, on this pull
request's merge commit on master, as v0.8.0's is.

## Why now

v0.8.0 refuses a Parquet file pyarrow writes routinely: a dictionary-encoded column
of only nulls whose pages are compressed. Its dictionary page holds no value, and
the reader's own pages took that for no dictionary at all.
`testdata/oracle/footers/pyarrow-03.parquet` is such a file. Step 538 fixed it on
master with the arrow-go upgrade, and this releases the fix.

## What is in it

| step | what |
| --- | --- |
| 538 | arrow-go v18.7.0 → v18.9.0, with Thrift v0.25.0, klauspost/compress v1.20.3 and golang.org/x/text v0.43.0; a column chunk whose dictionary holds no value reads; two malformed Parquet pages arrow-go now refuses, ursus with it |
| 540 | CI only: no full-size benchmark on GitHub's runners, and a superseded pull-request run cancelled. Not in the CHANGELOG. |

No API is added, changed or removed. `api/v0.8.1.txt`, written by `make api-tag
TAG=v0.8.1`, lists the same 2,481 features as `api/v0.8.0.txt`, and `make api`
measures against it from here on.

## The release

- **CHANGELOG.** "Unreleased — toward v0.8.1" is now "v0.8.1 — 2026-11-23". It has a
  paragraph on what the release is, and a "What a v0.8.0 user can trip on" for the two
  pages now refused, with what to do. `WriteParquet` sets no encodings: arrow-go's
  default is a dictionary falling back to PLAIN, so it writes neither delta page, and
  such a file read by v0.8.0 can be written again. The Fixed entry says pyarrow's
  files are refused by v0.8.0 too.
- **README.** Both "last release" lines name v0.8.1.
- **Tagging,** after the merge (since step 544, on 544's merge commit, its branch
  `fix/dt-far-arithmetic` in the commands below):

      git fetch origin
      M=$(gh pr view release/v0.8.1-prep --json mergeCommit --jq .mergeCommit.oid)
      GOEXPERIMENT=simd go run ./internal/gen/api -check   # at $M: 0 added, 0 gone since v0.8.1
      git tag -a v0.8.1 -m v0.8.1 "$M" && git push origin v0.8.1

  The tag's push publishes 0.8.1's documentation and moves `latest` to it
  (`docs.yml`).

## Checks

Listed in the pull request: build, vet, levels, `api -check`, testinventory, and
`go test ./internal/...` and the root package in full.

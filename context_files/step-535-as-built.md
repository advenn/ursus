# Step 535 — as built

**v0.7.3: three fixes backported from master, on the 0.7 line.** Release
preparation only. The maintainer approved it on 2026-11-20: tag v0.7.3 with the three
fixes, and port nothing more.

## What is in it

Step 532 backported these three fixes (`step-532-as-built.md`):

| master step | fix |
| --- | --- |
| 518 | a JSON or NDJSON field holding `{}` reads as a `Struct()` column; it used to fail the whole read with an internal error |
| 522 | an Arrow IPC file or stream with a Null column reads, Polars' and ursus's own included; every such file was refused |
| 525 | an Arrow time past its day is refused, not wrapped into the day; a Parquet TIME past its day is refused too |

No API is added, changed or removed. `api/v0.7.3.txt`, written by `make api-tag
TAG=v0.7.3`, lists the same 2,389 features as `api/v0.7.2.txt`.

## What is not in it

master's crash guards for untrusted input (steps 510, 511, 517 and 519–521), its
silent-wrong-answer fixes (509, 521, 534) and its fixes for memory and time blow-ups
(512, 515) are not ported. Step 532's record lists them. A service reading untrusted
Parquet, IPC or compressed text should use v0.8 when it is released, not 0.7.

## The release

- CHANGELOG: "Unreleased — toward v0.7.3" became "v0.7.3 — 2026-11-20".
- This branch is merged into `release-0.7` by a PR with green CI, and v0.7.3 is
  tagged on that merge commit.

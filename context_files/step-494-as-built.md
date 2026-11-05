# Step 494 — as built

**The public API is stable.** On 2026-11-05 the maintainer, after the project's
analysis named the 2,392-feature API under ZeroVer's "any minor may break" as a main
risk, said: "breaking apis ends, the apis will be stable now." From this step a
release adds, and never renames, removes or reshapes what an earlier release had. A
better design goes beside the old one.

## 1. What held the rule before

`make api` (step 401) listed every feature of the newest `api/<tag>.txt` that the
tree no longer had, and failed unless a Breaks section of an unreleased version in the
CHANGELOG named it, in backquotes. So a break passed once it was written down.

## 2. The change

- `internal/gen/api -check` fails on any gone feature. The CHANGELOG is no longer
  read: `breaksNamed`, `names` and their five regular expressions are gone, and the
  message says what to do instead: "put the better design beside the old one, never in
  its place". `TestAGoneFeatureFails` holds a removal and a changed signature to a
  failure, named in Breaks or not, and an addition to a pass.
- The README, the CHANGELOG's head, the Makefile and CI's step ("the public API
  unchanged") say so. ZeroVer's wording stays: 0.x, a stable 1.0 not expected yet.
- master is protected since the same day: a pull request, the nine CI jobs green and
  up to date, administrators included, no force push and no deletion.

**The baseline** is v0.7.2's listing, 2,392 features, plus master's 70 additions since
(0.8's steps 392–407 and this week's): `make api` reports 0 gone. 0.8's two changes of
defaults already built, the shared memory budget (395) and the shared threads (397),
stay under its Breaks as the last.

**Behaviour counts too.** The six places where ursus answers as Polars 1.44.1 and
Polars 2.0 does not (step 487 §3) stay as documented differences.

## 3. Checks

- `go test ./internal/gen/api`: ok.
- `go run ./internal/gen/api -check` on master's tree: 2459 features, 70 added and
  0 gone since v0.7.2.
- CI's required job `build` runs the step; its name changed, the job's did not.

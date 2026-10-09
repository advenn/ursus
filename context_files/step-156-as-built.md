# Step 156 — as built

**The scope of 0.6, in [`v0.6-scope.md`](./v0.6-scope.md),** with the record corrected
and the version stance written down. Only a doc comment changed in Go.

## 1. How it was made

**The maintainer's decisions,** after v0.5.0 was tagged:

- **ursus will never reach 1.0.** So nothing is planned toward stability.
- **APIs break when that helps,** with no deprecation period.
- **The focus is speed, the features 0.5 left, and robustness.** I/O breadth is not
  this release's.
- **Object stores and SQL stay out.**

**Three surveys,** read from the code and the records, not measured:

1. Every open, deferred or "later" item.
2. The Parquet reader's costs.
3. The join probe, the filters, and the plans of the widest PDS-H gaps.

## 2. What the surveys found that the scope rests on

- **Half of 0.5's PDS-H move was Polars slowing between sessions:** 70 to 78 ms. So
  0.6's target also measures ursus against v0.5.0 directly.
- **The Parquet reader copies every batch's strings twice.** It is the mistake steps
  136–138 fixed for fixed-width columns, and no test reads a String.
- **q12 builds 1.5M orders keys to probe about 31k rows,** because `EstimateRows`
  ignores filters.
- **q4 and q22 build their semi joins serially.** `deferBuild` refuses joins that keep
  no rows.
- **Every join and group-by key goes through 9-byte encoding.** There is no integer
  path.
- **q19 joins all 200k parts** before an OR whose arms each constrain the part side.
- **No runtime filters exist.** q2's partsupp could fall from 800k rows to about 3k.
- **PDS-H's files have no page index,** so page-index pruning would do nothing there.

## 3. The record, corrected (H1)

- **`CHANGELOG.md`:**
  - the v0.5.0 highlight gains the Polars-drift reading;
  - its limitations gain S21, and that the default budget is Linux's only;
  - each addition is marked as made on master after the tag.
- **`step-154-as-built.md`:** the same reading, marked.
- **`README.md`:**
  - the "Not done" list no longer names what 0.5 shipped;
  - the object-store note no longer says "not planned for 0.5".
- **`ursus.go`'s package doc:** `GOEXPERIMENT=simd` is optional, as the README and
  CI say. It had said the package does not compile without it.
- **`audit.md`:**
  - J8 records its remainder, which the changelog listed as open;
  - S26's Int128 half is struck, since step 100 built Int128 `//` and `%`.

## 4. The version stance (H2)

- **`CHANGELOG.md` and `README.md`** say ursus stays 0.x, that a minor version
  renames, removes or reshapes an API whenever the result is better, and that each
  release lists its breaks with their replacements.
- **`dataframe-features.md`** said to build SQL "after the expression API is stable";
  it now says SQL is out by decision.
- **The historical scopes** keep their own words: they record what was decided then.

**Gate:** `go build ./...` and `go vet .` clean; `go doc .` renders the package doc.
No code changed.

# Step 233 — as built

**v0.6.3 tagged,** on the maintainer's request. No code changed.

- **v0.6.3** is tagged at this step's commit. Its code is step 231's, `d661e79`, the
  last Go change, whose gate was clean and on which step 232's report ran, every
  suite of it.
- **`CHANGELOG.md`** dates the section: v0.6.3 on 2026-10-12.
- **The scope** records the tag, and what is left for a later release:
  - Speed's S3 and S4, which waited for evidence that never came;
  - Tier 2: rank, ordered windows and reversed cumulatives over a huge partition;
    keyed `Rolling` and `GroupByDynamic` handing their answer out; `Expr.Sort`,
    `SortBy` and `ArgSort`; business days and selectors; chained runtime filters;
    and CSE by structure.

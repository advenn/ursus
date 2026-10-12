# Step 212 — as built

**v0.6.1 and v0.6.2 tagged,** on the maintainer's request. No code changed.

- **v0.6.1** is tagged at `f87c31d`, "docs: the 0.6.1 report", step 200: the code
  steps 191 to 199 built, and the documents that measured it. It was a candidate
  while 0.6.2 was built on top, so its tag goes back to it rather than onto
  0.6.2's code.
- **v0.6.2** is tagged at this step's commit. Its code is step 211's, `2cc9012`, the
  last Go change, whose gate was clean and on which step 208's h2o half ran; step
  208's PDS-H half ran on `3aaf333`, and nothing since touches what PDS-H runs.
- **`CHANGELOG.md`** dates both sections: v0.6.1 on 2026-10-11, v0.6.2 on 2026-10-12.
  At `f87c31d` the v0.6.1 section still reads "candidate, not tagged", as it was
  then.
- **The scopes** record the tags. Tier 2's quantile over a huge group and chained
  runtime filters are left for a later release.

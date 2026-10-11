# Step 190 — as built

**v0.6.0 is tagged,** at the maintainer's request. The tag covers steps 156 to 189,
all on master since `v0.5.0`.

## 1. What was checked before tagging

- **The gate** ran on `c6aa77d`, step 188's code and the last Go change: test-all
  115 ok, race 23 ok, levels, vet ×3 and the bench engine tests clean. Since then
  only Markdown and `bench/results/REPORT.md` have changed: no Go file, and not
  `go.mod`.
- **Every answer validated:** step 189's report ran every query of all four suites
  on `c6aa77d`, and each matches DuckDB's reference.
- **`gb10` and `j5` under a 3 GB container,** on the same code, in step 189: all four
  pass, peaking at 2.77 to 2.92 GB.

## 2. What changed for the release

- **`CHANGELOG.md`:** the heading is `v0.6.0 — 2026-10-11`, and the section covers
  steps 156 to 190.
- **`README.md`:**
  - the status and "not for" lists say v0.6;
  - DuckDB-Go is about 2× ursus on TPC-H at SF=1, by the current report, where the
    README still said 3.5×.
- **`v0.6-scope.md`:** where it stands says v0.6.0 is tagged.

## 3. The tag

- **Annotated,** with the message `v0.6.0`, as the earlier tags were.
- **No GitHub release** was made, as for the earlier tags.

## 4. What comes next

The maintainer named the next release **0.6.1, not 0.7**, with two themes: speed
and out-of-core. `v0.6.1-scope.md` comes next, written from profiles.

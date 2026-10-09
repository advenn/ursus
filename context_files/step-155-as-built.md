# Step 155 — as built

**v0.5.0 is tagged,** at the maintainer's request. The tag covers steps 140 to 154,
all on master since `v0.4.0`.

## 1. What was checked before tagging

- **The gate** was run on `fb7788b`, step 153's code: test-all 115 ok, race 23 ok,
  levels, vet ×3 and the bench engine tests clean. Since then only Markdown and
  `bench/results/REPORT.md` have changed: no Go file, and not `go.mod`.
- **Every answer validated:** step 154's report ran every query of all four suites on
  `fb7788b`, and each matches DuckDB's reference, PDS-H at SF=0.1 among them.
- **`gb10` and `j5` under a 3 GB container,** at ten million rows, ursus alone, one
  timed run each, on the same code, again at the maintainer's request:

  | query | Parquet | CSV |
  | --- | --- | --- |
  | `gb10` | 3,571 ms, 2.70 GB | 5,864 ms, 2.78 GB |
  | `j5` | 3,941 ms, 2.88 GB | 6,994 ms, 2.84 GB |

  All four pass, within 0.1 GB of step 154's run.

## 2. What changed for the release

- **`CHANGELOG.md`:** the heading is `v0.5.0 — 2026-10-09`, and the section covers
  steps 140 to 155.
- **`README.md`:** the status and the report's provenance say v0.5, not a candidate.
- **`v0.5-scope.md`:** where it stands says v0.5.0 is tagged.

## 3. The tag

- **Annotated,** with the message `v0.5.0`, as the earlier tags were.
- **No GitHub release** was made, as for the earlier tags.

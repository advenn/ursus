# Step 139 — as built

**v0.4.0 is tagged,** at the maintainer's request. The tag covers steps 92 to 138,
all on master since `v0.3.1`.

## 1. What was checked before tagging

- **The gate** was run on `97ab4fc`, step 138's code. Since then only Markdown has
  changed: no Go file, and not `go.mod`. Results: test-all 115 ok, race 23 ok,
  levels, vet ×3 and the bench engine tests clean.
- **PDS-H at SF=0.1:** all 22 queries ran, and every answer matches DuckDB's
  reference. Steps 128 to 138 had not run it, by request. They changed the join,
  the group-by, the key table, the accumulators and both readers, so it was run
  once here, for the release.
- **The memory work was measured** on h2o `gb10` and `j5` at ten million rows under
  3 GB, after each of steps 130 to 138.

**The full benchmark report was not re-run.** The README's tables are step 127's,
from before steps 128 to 138, and say so.

## 2. What changed for the release

- **`CHANGELOG.md`:**
  - the heading is `v0.4.0 — 2026-10-09`;
  - the highlights gain the memory work of steps 128 to 138;
  - the speed highlight says its report predates that work.
- **`README.md`:** the report's caveats say the same, and point to the changelog for
  the later figures.
- **`v0.4-scope.md`:** where it stands, and §7's opening, say v0.4.0 is tagged.

## 3. The tag

- **Annotated,** with the message `v0.4.0`, as `v0.3.0` and `v0.3.1` were.
- **No GitHub release** was made: none was for the earlier tags.

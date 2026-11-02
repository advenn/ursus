# Step 436 — as built

**v0.7.1's API pinned on the 0.7 line, and the one addition it found taken out.**
The plan's 414, M1 of `v0.7.2-plan.md` (D3), the first step of cutting v0.7.2.

## 1. What it found

0.7.2's scope adds no API (`v0.7.2-scope.md`). The critic warned that fixes would
merge before any check proved it. Ported and run, the lister showed the 0.7 line's API
was v0.7.1's but for one line:

```
+pkg github.com/advenn/ursus, method (*Column) Spans() iter.Seq2[data.BufferID, data.Span]
```

Step 424 gave `internal/data`'s Column a `Spans` method, for the ledger in
`internal/execopt`. `ursus.Column` is an alias of that type, so every exported method
of it is public API, its signature naming two types a caller cannot name.

## 2. The change

- **`data.Spans(c)` is a function** of `internal/data`, which no caller can import, in
  place of the method. `Buffers()` stays a method, as it was in v0.7.1.
- **The lister, ported from master byte for byte.** `internal/gen/api/main.go` and
  `main_test.go` (step 401, unchanged since `90bcf28`), and `api/v0.7.1.txt`, whose
  2,389 features are v0.7.1's.
- **`make api` diffs strictly:** the lister's output, headed as the newest
  `api/<tag>.txt`, against that file, with `diff -u`.
  - An addition fails as a removal does.
  - Master's `make api` is `-check`, which passes an addition, and a removal the
    CHANGELOG names under Breaks: right for a minor that may break, too loose for a
    release of fixes.
  - `make api-tag TAG=vX.Y.Z` writes a tag's list, as master's does.
- **CI runs `make api`** in the build job, after the import levels.
- **Not forward-ported:** master has the lister and its own `make api`. The
  `data.Spans` function goes to master with step 424's forward-port.

## 3. Teeth: 3 of 3 bite

| mutation | `make api` |
| --- | --- |
| `func APITooth()` added to the root package | **fails:** `+pkg … func APITooth()` |
| `MinSamples(int)` made `MinSamples(int64)` | **fails:** the old line removed, the new added |
| `Spans` a method of Column again | **fails:** `+pkg … method (*Column) Spans()` |

**Gate:**
- `make api` passes;
- `./internal/...` and the root package in full, vet and levels;
- on the pull request, CI's every width, the scalar build, the race detector, and its
  new API step.

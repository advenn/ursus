# Step 435 — as built

**`release-0.7` builds its tests again after the merges.** Two lines.

## 1. What was wrong

Pull requests #1 to #25 were merged into `release-0.7` in order on 2026-11-02. Each
had been tested against the branch as it stood when it was opened, and two of them,
together, broke the branch's tests:

- **#12 (step 421)** gave `kernel.winRolling` a context, its first argument.
- **#20 (step 429)** added `TestARollingWindowStreamedInPiecesIsTheWindow`, which calls
  `winRolling` without one.

Neither touched the other's lines, so the merge was clean. `go build` passed, but the
`internal/kernel` tests did not compile, nor did `go vet`.

Separately, step 421's commit added the `context` import to `internal/kernel/window.go`
out of order. The `gofmt` on PATH, Go 1.26's, rejects every generic method in the
repository, so its check could not show that. The toolchain's `gofmt`, Go 1.27's,
does.

## 2. The change

- The test passes `t.Context()` to `winRolling`.
- `window.go`'s imports are sorted by the toolchain's `gofmt`. It now reports no
  file in the repository.

## 3. Also, from the merges

- **#16 (step 425)** was stacked on #15. Its base change to `release-0.7` failed on a
  GitHub GraphQL error about classic Projects, and the chain did not stop, so #16
  merged into #15's branch, `hunt/ledger-slice-identity`, not `release-0.7`. Its
  commit, 922a199, is re-opened as #26 into `release-0.7`.
- **#21 (step 430),** stacked on #20, was moved to `release-0.7` through the REST API,
  then closed and reopened so its CI runs against that base.

## 4. Gate

- `go vet ./...` clean, and the toolchain's `gofmt -l .` empty;
- `./internal/...` and the root package in full, on the merged branch with these two
  lines;
- CI on the pull request.

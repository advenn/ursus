# Step 402 — as built

**Errors a caller can read.** C2 of `v0.8-scope.md`.

## 1. What was wrong

Every failure of ursus's own is a `uerr.Error`: its kind, the operation as the caller
named it, the message, the plan node it happened at, the hints under it, a near
column's name or a knob to turn, and its cause. It is internal, so a caller outside
the module could not `errors.As` it: it had seven sentinels for `errors.Is`, one a
kind (`ursus.go:96`), and the message as text. A service that logs a failure's
operation, or tells its user which column was meant, matched strings.

## 2. How

- **`ursus.Error`** is `uerr.Public`, a struct of the six: `Kind`, `Op`, `Msg`,
  `Node`, `Hints` and `Err`, with `Error`, the message the internal error gives,
  `Unwrap`, its cause, and `Is`, its kind's sentinel. **`ErrorKind`** is the kind,
  and `KindInternal` to `KindResource` its seven values, which the sentinels name.
- **`errors.As` gives a copy** (`uerr.Error.As`), made as it is asked for, its hints
  cloned. Not the internal error itself: that one is annotated in place as it
  propagates (`Annotate`), which `errsharing_test.go` records, and its builders,
  `Hint`, `WithOp` and `WithNode`, are ursus's, which an alias would have published
  and step 401's check would have held as API.
- **The sentinels are as they were**, and match an `Error` of their kind too.
- **A query stopped by its context** fails with the context's error,
  `context.Canceled` or `context.DeadlineExceeded` (step 394), not an `Error`, as
  `Error`'s doc says.
- **`ExampleError`** shows it on pkg.go.dev: an unknown column's kind, operation, plan
  node, message and first hint.

## 3. Measured

Nothing to time: `errors.As` copies an error of six fields as it is asked for. The
API gains 27 features, none gone (`make api`: 2,434, 47 added since v0.6.8).

## 4. Tests

- **`TestAnErrorIsReadByErrorsAs`** (root): an unknown column, wrapped by the caller's
  own `fmt.Errorf`, is an `*ursus.Error` of `KindSchema`, operation `select`, node
  `Project`, its message naming the column, a hint naming the near one, and the
  message the error gives; `errors.Is` matches `ErrSchema` on the error and on the
  `Error`, and not `ErrType`. Writing to the copy's message and hints changes neither
  a second `errors.As` nor the error's message.
- **`TestAnErrorsCauseUnwraps`**: a Parquet file that is not there is `KindIO`, and
  `errors.Is` finds `fs.ErrNotExist` through the `Error`.
- **`TestACancelledQueryIsItsContextsError`**: a cancelled query's error is
  `context.Canceled` and no `*ursus.Error`; nor is an error ursus did not make.
- **`ExampleError`**.

**Teeth: 6 of 6 bite:** no `As`; the hints shared, not cloned; the copy matching no
sentinel; the copy with no cause; no node copied; no operation copied.

**Gate:** the light gate, 29 packages ok, levels, vet ×3 and the bench engine tests;
the 22 PDS-H answers match DuckDB's at SF=0.1; `make api`, none gone.

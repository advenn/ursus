# Step 428 — as built

**A panic around the Arrow source's caller code is attributed to whoever's code
raised it.** One of the confirmed items in `v0.7.2-plan.md`, which the critic gave
two options: fix it in 0.7.2, or record it in 442. It is fixed, as the critic
suggested: by the innermost frame, as Parquet's `corrupt` already does.

## 1. What was wrong

`arrowsrc` runs the caller's factory and reader inside a recover. Each panic it
caught became an I/O failure of the stream, `ErrIO` (audit I25, step 115). That was
right for the caller's code.

The same recovers also cover ursus's own code:
- the conversion of the stream's schema (`arrowin.Schema`);
- the checks of a reader's schema and records;
- the readers `ScanIPC` and `ScanIPCStream` open, which are written in ursus.

A bug in any of them reached the caller as "reading the Arrow stream panicked",
`ErrIO`. It should be ursus's own, `ErrInternal`, which every other recover in the
engine returns for ursus's code. Run at step 428: a nil-map write in ursus's code,
and `strings.Repeat` of a negative count called from it, were both `ErrIO`.

## 2. The change

- **`attributed`** (`internal/source/arrowsrc/arrowsrc.go`) is used by both
  `callerCode` and `Schema`'s recover. The innermost frame outside Go's standard
  library decides:
  - ursus's own is `uerr.FromPanic`, `ErrInternal`;
  - the caller's, or another module's, is `ErrIO` as before. arrow-go reading the
    stream counts as another module's.
- **`ownerOf`** reads the package path from a frame's function name:
  - `github.com/advenn/ursus`, and paths under it, are ursus's own;
  - `main`, and any path whose first element has a dot, are the caller's or another
    module's. `ursus_test` and `ursusx` count here, not as ursus's;
  - anything else is the standard library, and the next frame out decides.
- **Not changed:** a caller's panic, its factory's or its reader's, is `ErrIO`, as
  `panic_test.go` holds.

## 3. Tests

- **`TestAPanicIsWhoseCodeRaisedIt`** (`internal/source/arrowsrc`):
  - Each case runs through `callerCode`, and through `Schema`'s probe in its own
    `Once`:
    - a panic in ursus's code is internal, where it was `ErrIO`;
    - one in the standard library called from ursus's code is internal;
    - one in arrow-go is the stream's I/O failure.
  - Twelve function names are classified. They include `ursus_test` and `ursusx` as
    others, `main` as the caller's, and `encoding/json` and `internal/bytealg` as the
    standard library.
- **`panic_test.go`'s cases are unchanged:** a factory or a reader that panics is
  still `ErrIO`, its frames being `ursus_test`'s.

**Teeth: 6 of 6 bite.**
- ursus's frames counted as the caller's;
- the standard library deciding;
- `main` unrecognised;
- a module whose path only begins with ursus's taken for ursus;
- `Schema`'s recover left all `ErrIO`;
- `callerCode`'s left all `ErrIO`.

**Gate:**
- `./internal/...` and the root package in full;
- vet and levels;
- on the pull request, CI's every width, the scalar build and the race detector.

The public API is unchanged. Only the kind of an error ursus's own bug returns differs.

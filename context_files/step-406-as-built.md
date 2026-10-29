# Step 406 — as built

**A guide for a service.** D1 of `v0.8-scope.md`.

## 1. What was wrong

No page covered a service: its threads, its budget, its cancellation. A `LazyFrame`'s
doc said it was safe to share between goroutines; a `DataFrame`'s said nothing, and an
`http.ResponseWriter`'s being an `io.Writer` no guide said.

## 2. How

- **`docs/guide/services.md`,** "ursus in a service", in the user guide's navigation
  and its index of chapters, every block an example `go test` runs
  (`docs/examples/guide_service_test.go`):
  - a query built once and shared, each request's filter added and its answer
    written to an `http.ResponseWriter` by `WriteCSV`, run through `httptest`; the
    status a writer sends with its first byte, and `Collect` first where it must say
    whether the query worked; `CollectBatches` a batch at a time;
  - the request's context, a deadline passed: `context.DeadlineExceeded`;
  - errors as HTTP statuses: `statusOf`, from `ursus.Error`'s kind and the context's
    errors, a misspelt column a 400 and its hints; panics as errors;
  - the shared budget and threads, an engine of the service's own with a hook that
    reads a route from the query's context, and `Stats`;
  - data the service carries: `ScanCSVFS` over an `embed.FS`, and object storage
    through the `From` readers;
  - what a large query leaves in RSS, 1.85 GB after a five-million-key group-by whose
    live heap is 153 MB, 181 MB after `debug.FreeOSMemory` (step 392's measure), and
    the process's soft limit, `SetProcessMemoryLimit`, `LeaveProcessMemoryLimit` and
    `GOMEMLIMIT`.
- **`DataFrame`'s doc:** never changed once made, so shared between goroutines as a
  `LazyFrame` is; a `Column`'s raw accessors give its memory, not to be written.

## 3. Measured

Nothing to time. The seven examples run in `go test ./docs/examples`, and `make docs`
writes their fragments, each one the page includes found.

## 4. Tests

The examples are the tests: `Example_serviceHandler`, `Example_serviceBatches`,
`Example_serviceDeadline`, `Example_serviceBadColumn`, `Example_serviceEngine` and
`Example_serviceEmbed`, each held to its output, and `statusOf`, the fragment
`func.statusOf.md`. CI's docs job builds the site, its links and fragments checked;
mkdocs is not installed here.

**Gate:** the light gate, 29 packages ok, levels, vet ×3 and the bench engine tests,
step 407's edits with it; the 22 PDS-H answers match DuckDB's at SF=0.1. Its first
run failed `TestTheExamplesFitThePage`: three lines of `statusOf` were wider than the
page, which a switch on the kind fixed.

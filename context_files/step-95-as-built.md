# Step 95 — as built

**A query that fails while it is being planned closes what it already opened.**
This is `v0.4-scope.md` item 5, recorded open since step 60 ("`planJoin` leaks the
left operator when the right side fails to plan") and counted at seventeen sites in
step 64.

## 1. What was wrong

A CSV source opens its stream in `Open`, which runs while the operator tree is
being built, before the first batch. For `ScanCSVFrom` that stream is the caller's:
a file handle, or the body of an HTTP GET.

Planning goes child by child, and no planning function closed what it had built
when a later step failed. So:

- the left side of a join stayed open when the right side could not open;
- the inputs of a `Concat` before a failing one stayed open;
- **`planScan` itself** dropped a source it had just opened, when the scan's schema
  then failed — an eighteenth site.

**Measured** (`TestAFailedPlanClosesWhatItOpened`): a healthy CSV source beside one
that resolves its schema and then cannot be opened again (an object deleted between
planning and reading). In every shape, the healthy stream was left open:

- the two sides of a join, including a left side under a filter and a sort;
- `Concat`;
- `HStack`;
- an as-of join;
- `MergeSorted`;
- a join nested inside a join.

In the shapes with two healthy streams, both stayed open. The same join with
nothing failing closed everything.

## 2. The fix

- **One record, at the one place sources are opened.** `PlanRoot` gives the plan an
  `openedSources` list through `Options`, and `planScan` adds each source to it as
  soon as `Open` succeeds.
- **If planning fails, `PlanRoot` closes them all.** The planning error is returned
  unchanged; errors from closing are dropped, since the query has already failed
  and the planning error is the one the caller needs.
- **This covers all eighteen sites, and any added later,** without touching them.
- **Nothing is closed twice:** no planning function closes a child on its own error
  path. That was checked by grepping every `plan*` function.
- **No goroutine leaks:** a parallel operator starts its workers on its first `Next`
  (`parallelOp.start`), so a plan that fails starts none.
- `Plan`, which tests call directly, does not record by itself. Only `PlanRoot`,
  which every query goes through (`LazyFrame.compile`), does.

## 3. Tests and teeth

**`TestAFailedPlanClosesWhatItOpened`:**

- **Seven shapes, each at 1 and at 4 threads.** Every case also checks two things,
  so a passing case cannot be vacuous:
  - the query failed for the planted reason;
  - the healthy source was opened past its schema.
- **A control:** the same join with nothing failing closes every stream.

| tooth | result |
| --- | --- |
| a failed plan closes nothing | **bites:** all 14 failure cases |
| `planScan` records nothing | **bites:** all 14 |
| only the first recorded source is closed | **bites:** the two shapes with two healthy streams |
| everything closed on success too | **bites:** `TestScanCSVFrom` and the CSV round trips, which read from a closed stream |

**Gate:** test-all 105 ok, race 21 ok, levels, vet ×3 and the bench engine tests clean; PDS-H SF=0.1 22/22 against DuckDB. Run once over steps 94 and 95 together.

# Step 120 — as built

**What the audit of 0.4's service-safety work found, fixed.** The read-only review of
steps 94–99 and 92 traced four defects. Each was reproduced by a test first.

## 1. The defects

| | what happened | why |
| --- | --- | --- |
| a non-nullable CSV column | `ScanCSV` with a `WithSchema` field declared non-nullable, over a file with an empty cell in it, failed as ursus's bug. Before step 98 it succeeded, silently holding a null the schema said could not be there. | The CSV source takes the caller's schema as given, and `data.NewBatch`'s check, on in production since step 98, reports an internal error. |
| state kept after the answer | A group-by's accumulators and key table, and a rolling or dynamic group-by's accumulators, stayed referenced until the query ended, after the budget had stopped counting them. A median holds every value of every group. | `Finish` released the account's claim and kept the objects. |
| a CSV stream leaked | A scan whose stream failed on its header leaked the stream: a file handle, or an HTTP body, per attempt. | `Open` opened the first stream and returned the error without closing it. The reader it would have closed through was never returned. |
| a panic while planning | A panicking `Open`, a caller's, left the sources opened before it open. | Step 95 closed them when planning returned an error, not when it panicked, and `Collect` recovers the panic, so the process carried on. |

## 2. The fixes

- **The CSV reader checks each non-nullable column before building a batch.** An empty
  cell there is a value error naming the data row and the column, with the remedy.
  The control test reads the same file under a nullable declaration.
- **Each sink drops its state once its answer is built.** `hashAggSink.releaseState`
  drops the accumulators and the key table, and keeps `firstSeen`, which
  `finishOrdered` reads. `temporalSink.Finish` drops its accumulators.
- **The CSV source closes its reader when `Open` fails.**
- **`PlanRoot` closes what was opened in a deferred function,** unless planning
  succeeded.

**One test changed:** `TestAFoldedWorkerHoldsNoTable` checked that the surviving
worker held six keys after `Finish`, which is the state now dropped. It checks the six
groups in the answer instead, and that no table remains afterwards.

## 3. Tests and teeth

- **`serviceaudit_test.go`:**
  - `TestANonNullableCSVColumnWithAnEmptyCell`;
  - `TestAFailedCSVOpenClosesItsStream`;
  - `TestAPanicWhilePlanningClosesWhatWasOpened`: a `Concat` of a healthy CSV and one
    whose `Open` panics. It fails if the healthy one was never opened, which would
    make it test nothing.
- **`internal/physical/statedrop_internal_test.go`:**
  `TestFinishedGroupingsDropTheirState` plans a group-by and a rolling group-by over
  `memsrc`, drains them, and inspects their sinks.

| tooth | result |
| --- | --- |
| no CSV non-nullable check | **bites** |
| a failed `Open` leaves its stream | **bites** |
| a planning panic skips closing | **bites** |
| the group-by keeps its accumulators | **bites** |
| the rolling sink keeps its accumulators | **bites** |

**Gate:** with steps 118–122; see step 122.

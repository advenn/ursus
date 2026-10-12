# Step 221 — as built

**O5 of `v0.6.3-scope.md`: an ewm function and a reversed cum_count stream over one
huge partition.**

## 1. Why

A window with no partition_by is one partition. Since step 197 it streams past the
budget when every function needs only a bounded context around a batch or carries
its last answer. Since step 209 a keyed window's one huge partition streams the same
way. The ewm functions and a reversed cum_count were left refusing, but neither
needs the whole partition:

- an ewm function's answer for a row is its running state after the row: seven
  scalars;
- a reversed cum_count is the partition's rows less the row's position, and the
  file counts its rows.

## 2. What changed

**`internal/kernel/ewm.go`:**

- **`EwmState`** is what `winEwm` kept between rows: whether a value has been seen,
  how many, the mean, the covariance and the three weights. Its zero value is a
  partition's start.
- **`ewmRun.step`** is the recurrence, unchanged, taking a row into a state.
  `winEwm` runs it with a fresh state a segment.
- **`EwmNext`** runs it over a partition's next rows with a state carried in.

**`internal/physical/winstream.go`:**

- **`streamNeed`:** cum_count streams either way, and an ewm function streams with
  `winNeed.ewm`.
- **`finishOneSpill`** keeps the file's row count.
- **A reversed cum_count** answers that count less the rows before.
- **An ewm function** runs `EwmNext` over each batch with its spec's state.

**The refusal hints** (`extwindow.go`, `execopt`) list what still refuses: rank,
interpolation, the other reversed cumulative functions and ordered windows.

## 3. Tests and teeth

- **`TestAnEwmCarriedAcrossSplitsIsOnePass`** (kernel):
  - each ewm function, under all sixteen combinations of adjust, bias, ignore_nulls
    and a minimum of four values;
  - run in pieces of one to sixty rows, over runs of ninety nulls, with a late NaN;
  - bit for bit as one pass.
- **`TestUnpartitionedWindowsStreamPastTheirLimit`** adds four functions:
  - a reversed cum_count;
  - an ewm mean;
  - an ewm variance ignoring nulls, over runs of nulls longer than two batches;
  - an unadjusted, biased ewm std wanting five values.
- **`TestAKeyedWindowStreamsItsOneHugePartition`** adds a reversed cum_count and an
  ewm mean over the hot key, on one thread and four.
- **`TestUnpartitionedWindowsThatNeedTheWholeFrameStillRefuse`** drops its ewm case.

| tooth | result |
| --- | --- |
| ewm never streams | **bites:** both root tests |
| a reversed cum_count never streams | **bites:** both |
| a reversed cum_count counts up | **bites:** both |
| a reversed cum_count off by one | **bites:** both |
| the file's rows not counted | **bites:** both |
| ewm state fresh each batch | **bites:** both |
| ewm's count of values not carried | **bites:** the kernel test, the ewm tests, the hot key |
| winEwm's state shared across segments | **bites:** the per-group ewm test, the hot key |
| EwmNext reads the rows backwards | **bites:** the kernel test and both root tests |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.

# Step 240 — as built

**W6 of `v0.6.4-scope.md`: a window with no partition_by beside a keyed one spills,**
where it refused.

## 1. Why

`Select(x.Rank().Over(), y.Sum().Over(k))` partitions two ways, one of them none.
Past the limit the keyed spill routes the rows by each partitioning in turn and
answers each pass's files in sub-sinks a level down (step 82). The one-file stream
(step 197) needs every partitioning keyless. So the mix refused, whatever its
functions.

## 2. What changed

**`internal/physical/extwindow.go`:**

- **`overBudget`** lets a keyless partitioning through when every one of its
  functions streams alone (`keylessStreams`: `streamsAlone`, steps 235–238). The
  keyed spill then takes the window as it is, with nothing else changed:
  - routing by no key encodes every row alike, so the keyless pass is one file;
  - that file's sink, a level down, holds only the keyless pass's expressions, so it
    is a keyless window, which streams in one file, in sorted passes;
  - its rows come back in ordinal order, as every pass's do.
- **It refuses** only when a keyless function needs all of its partition: a rolling
  window by time.
- **Both refusal hints** now name only the rolling window by time as needing all of
  a partition. The hints of steps 237 and 238 had still listed rank and
  interpolation.

## 3. Tests and teeth

**`TestAWindowWithNoPartitionBesideAKeyedOneStreams`:**

- **keyless:** a cumulative sum in the file's order, a rank by average, and an
  interpolation in an order of its own;
- **keyed:** a sum over k, whose key 0 holds most of the rows, so its pass streams a
  hot key, and a max over k mod 7;
- under a tenth of the input, on one thread and four, frame-equal to the in-memory
  answer, spilling, and the spill directory empty after;
- a rolling sum by time beside a keyed sum still refuses, saying the window has no
  partition.

**Under the race detector:**

- **The first gate's race run timed out.** The root package passed its ten minutes,
  while `TestUnpartitionedWindowsStreamPastTheirLimit` happened to be running.
- **The cause:** `TestAWindowInAnOrderOfItsOwnStreamsPastItsLimit` had grown, step
  by step, to over thirty window columns, two shapes and two thread counts. It took
  94 seconds under the race detector.
- **The fix:** under it, the test now runs only the hot key on four threads, which
  drives the sorted pass, the hot key's split and the parallel scan. That takes 26
  seconds. The rest runs without the race detector, as `measureUnkeyed` has since
  step 210.

| tooth | result |
| --- | --- |
| a keyless window beside a keyed one refuses | **bites** |
| a keyless window's functions not looked at | **bites:** the refusals of rolling by time, here and in two other tests |

**Gate:** test-all 115 ok, race 23 ok once the heavy test was trimmed under it,
levels, vet ×3 and the bench engine tests clean. **PDS-H at SF=0.1:** all 22
answers match DuckDB's.

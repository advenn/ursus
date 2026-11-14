# Step 516 — as built

**The fuzzer minimises a new input for 5 s, not 60.** A CI change, asked for by the
three fuzz-target steps (509, 510 and 511), each of which met it locally.

## What it was

`go test -fuzz` minimises every input that widens coverage, and every failing one,
for `-fuzzminimizetime`, by default 60 s. CI's fuzz workflow (step 508) gives each
target 30 s. A target that takes a millisecond or more an input, as the Parquet,
NDJSON and IPC ones do, spent nearly all of it minimising:

| target | minimise time | inputs in 20 s |
| --- | --- | --- |
| `FuzzScanParquet` (510) | 60 s, the default | 40 |
| `FuzzScanParquet` (510) | 1 s | 11,366 |

Step 511 saw its slower targets at 0 inputs a second under the default, and fuzzed
with `-fuzzminimizetime=0` or `3s` locally.

## What it is

- `fuzz.yml` runs each target with `-fuzzminimizetime=5s`.
- `make fuzz` passes `-fuzzminimizetime $(FUZZMIN)`, 5 s unless set.

A failing input is still written to `testdata/fuzz/<target>/`, uploaded and kept as
a seed; with less time to minimise it may be larger than it would have been, and the
step that keeps it can minimise it further by hand.

## Checks

`make -n fuzz` prints the flag. The workflow's own run on this pull request is the
check of the YAML.

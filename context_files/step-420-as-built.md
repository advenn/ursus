# Step 420 — as built

**A partition path with a parent directory in it is refused.** H2 of
`v0.7.2-scope.md`, confirmed by its prober, the plan's 429, decided as D11
recommends: refused as Polars refuses it; `\` is not percent-encoded.

## 1. What was wrong

A partitioned write names each directory `name=value`, through `hive.Escape`, which
percent-encodes `/` but leaves `\` as it is. Windows splits a path at `\`, so a value
of `..\..\..\x`, or a key so named, made the segments `k=..`, `..`, `..`, `x`, and
`filepath.Join` wrote the files outside the dataset's directory: a path traversal
from the data. On Linux `\` is a name's byte and nothing escaped.

## 2. The change

**`noParent`,** on each segment as it is made: split at `\` and `/`, a component that
is `..` refuses the write, an `ErrValue`, "the partition path S holds a parent
directory, '..'", on every system, as Polars 1.44.1 refuses it on Linux too. `a\b`
and `..` alone, one segment, `k=..`, are written, as Polars writes them.

## 3. Tests

- **`TestAPartitionPathHoldsNoParentDirectory`** (root): a value of `..\..\..\x`, a key
  named `..\..\x` and a value of `y\..\..` are refused; `a\b` and `..` are written at
  `k=a\b` and `k=..`. Before the change the three were written.

**Teeth: 3 of 3 bite:** the check gone writes them again; `\` not a separator writes
the value; any `..` within a component refused refuses `..` alone, which Polars writes.

**Gate:** the root package's partition, hive and sink tests; vet. CI's every width,
the scalar build and the race detector on the pull request.

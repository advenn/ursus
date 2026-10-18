# Step 277 — as built

**M1 of `v0.6.6-scope.md`, second half: entropy, mode and Spearman's correlation.**
This finishes M1.

## 1. What changed

**`Entropy(base, normalize)`** (`AggEntropy`, with `AggParams.Base` and
`Normalize`):

- **The accumulator,** `entropyAcc` in `internal/kernel/aggrank.go`, keeps each
  group's sum S and Σ x·ln x, so it merges.
- **The answer:** normalized, ln S − (Σ x·ln x)/S, which is −Σ p·ln p with p = x/S;
  raw, −Σ x·ln x; each over ln(base). Polars divides each value by S first, so the
  two agree to the last few bits.
- **As Polars answers:**
  - a zero or a negative value makes it NaN, since 0·ln 0 is;
  - an infinity or a NaN is NaN;
  - base 1 is an infinity;
  - a Float32's entropy is a Float32, any other number's a Float64, and a Boolean or
    text is refused.
- **Two differences:**
  - A group of no value is **null**, by ursus's rule that an aggregate reading no
    value answers null, as `Sum` does. Polars' is −0.0, its empty sum negated. The
    one-row aggregate sweep holds the rule, and caught the first draft's −0.0.
  - Polars gives a **one-value group's raw entropy** as 0.0, where −x·ln x is, for 5,
    −8.05. Over a whole column it is −Σ x·ln x, so this looks like a Polars bug, and
    ursus gives −x·ln x in both.

**`CorrSpearman(a, b)`** (`AggSpearman`, paired as `corr`):

- **The accumulator,** `spearmanAcc`, keeps each group's pairs where both are
  non-null, as a median keeps its values, so its state grows with its input. Its
  merge appends.
- **Finish:** ranks each side, a tie the mean of its ranks and NaN above every
  number, as Polars ranks them, then takes Pearson's of the ranks.
- **Below two pairs, or over values all alike,** it is NaN, as Polars answers and as
  ursus's `corr` already does.
- **A difference:** it takes numbers, as `corr` does. Polars ranks text too.

**`Mode(maintainOrder)`** (`ReshapeMode`):

- **Its shape:** a column of its own length in a `Select` and a list a group in an
  aggregation, as Polars' `mode`.
- **In a `Select`,** the zipper's frame form: a group-by of the value counting its
  rows, the first row's index beside it, kept where the count is the largest. Then
  it is sorted by value, or by first appearance with `maintainOrder`.
- **In an aggregation,** `list.mode` (`internal/kernel/listmode.go`):
  - each list's elements are counted by `GroupKeyEncoder`'s keys, so NaN is one
    value, −0.0 is 0.0, and a null is a value;
  - each key at the largest count is kept once, at its first index, sorted by the
    element comparator unless `maintainOrder`;
  - `FnListMode` joins the list block at 700.
- **Two differences:**
  - Without `maintainOrder`, Polars' order is its hash table's: `[1.0, 4.0, None,
    2.0]` for a group of four once each. ursus sorts, nulls first.
  - A Struct or a List cannot be a key in ursus, so its mode is refused, at
    planning, where Polars counts them. The typing first allowed them, and a probe
    found the kernel refusing what the typing promised.

**The sweeps:**

- the Decimal types (entropy a scaled Float64, Spearman a pair);
- the behaviour classes (both approximate);
- the merge equivalence (entropy normalized in e and raw in bits);
- the names test, at 198.

## 2. Tests and teeth

Every answer is Polars 1.44.1's, the floats to a relative 1e-12, but where the
differences above are named.

- **`TestEntropyModeSpearman`:**
  - entropy in e, in bits and raw, and Spearman's, over the frame and by group;
  - entropy of integers, a zero, a negative and base 1;
  - Spearman's of ties, integers, a constant, a NaN and past nulls;
  - a Float32's entropy, and entropy of none;
  - mode sorted and in order, with a null, NaN, both zeros, Booleans, no rows and its
    length;
  - a group's modes both ways;
  - three refusals.

| tooth | result |
| --- | --- |
| entropy adds where it subtracts | **bites** |
| raw entropy's sign | **bites** |
| a Float32's entropy is a Float64 | **bites** |
| entropy of none is a value | **bites** |
| a tie takes its least rank | **bites** |
| NaN ranks in IEEE order | **bites** |
| a pair with a null y | **bites** |
| a list's modes unsorted | **bites** |
| a list's every value a mode | **bites** |
| a frame's every value a mode | **bites** |
| a frame's modes in the other order | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.

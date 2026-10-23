# Step 350 — as built

**`Cut` answers an Enum** of its bins' labels, in their order, as Polars 1.44.1's `cut`
does, where it answered a String; from `polars-parity.md` §3's Categorical row, whose
`cut` half Polars now answers with an Enum, which ursus has. `QCut`'s labels come from
the data, and Polars' are a Categorical, which ursus has not yet: text still.

## 1. How

`Cut` knows its breaks when the query is planned, so its labels, given or the
intervals `kernel.CutIntervals` names, are the Enum's categories, and the kernel's text
is cast to it. The labels must differ, as an Enum's categories do; Polars refuses them
so ("FrozenCategories must contain unique strings"). A break is written in decimal,
`0.0000001` and `100000000000000000000`, as Polars writes it, where Go's shortest form
wrote `1e-07` and `1e+20`, in `QCut`'s labels too.

| Polars 1.44.1 | ursus |
| --- | --- |
| `cut([3, 6])` | `Enum("(-inf, 3]", "(3, 6]", "(6, inf]")` |
| `labels=["lo", "mid", "hi"]` | `Enum(lo, mid, hi)` |
| `left_closed=True` | `Enum("[-inf, 3)", ...)` |
| `cut([1e-7, 1e20])` | `(-inf, 0.0000001]`, `(0.0000001, 100000000000000000000]` |
| `labels=["a", "a", "b"]` | refused |
| `cut(...) == "(3, 6]"` | compared by its text |
| `qcut` | a Categorical in Polars; a String here (feature-gaps §9) |

## 2. Tests

**`TestCutIsAnEnum`,** Polars 1.44.1's answers: by intervals, labelled, closed on the
left, small and large breaks, no break; a comparison with a label; a group-by and sort
in the Enum's order; a label twice refused; `QCut` text. `TestCut`'s and `TestQCut`'s
answers, read as text, are unchanged.

**Teeth (4 of 4 bite):** `Cut` a String; a break in exponent form; a label twice let
through; the intervals the categories beside labels.

## 3. Performance

A million values in six bins, in memory, the median of seven, the machine shared:
`Cut` 25.4 ms, Polars' `cut` 24.6. Its answer cast back to text takes 40.2: an Enum's
codes are cheaper to hold than the labels' text.

**Gate:** test-all 140 ok, race 28 ok (the root package 591 s here), levels, vet ×3 and
the bench engine tests clean; the 22 PDS-H answers match DuckDB.

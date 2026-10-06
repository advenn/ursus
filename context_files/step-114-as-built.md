# Step 114 — as built

**The trigonometric and hyperbolic functions.** This is `v0.4-scope.md` item 21's
"trigonometric block, which is cheap".

## 1. What was added

**Fourteen unary operations,** with Polars' names:

- **Trigonometric:** `Sin`, `Cos`, `Tan`, `ArcSin`, `ArcCos`, `ArcTan`.
- **Hyperbolic:** `Sinh`, `Cosh`, `Tanh`, `ArcSinh`, `ArcCosh`, `ArcTanh`.
- **Angle conversion:** `Degrees` and `Radians`.

**They behave as `Sqrt` does,** sharing its kernel, `unaryMath`:

- they widen an integer to Float64 and keep a Float32;
- a value outside the domain, such as `ArcSin(2)` or `ArcCosh(0)`, is NaN, not an
  error and not a null;
- a null stays null.

## 2. The one decision

**The operation enum is append-only:** its classifiers are range tests over
declaration order, so a constant inserted in the middle moves its neighbours'
families.

- **The new operations are appended after `OpCeil`,** rather than inserted after
  `OpLog1p` where they would have joined the widening block.
- **`IsMath` therefore tests two ranges.**
- **`TestMathOpsAreClassified`'s explicit list of widening operations** gains the
  fourteen. That is the declaration the test exists to force, not a loosening.
- **The kernel lists every widening operation in its arm,** as it did, so an
  operation added to the enum without an arm still fails loudly.

## 3. Tests and teeth

**`TestTrigonometry`:**

- **Values:** each function over nine inputs, against Go's `math`, within 1e-12
  relative, and NaN exactly where `math` gives NaN.
- **Nulls:** a null stays null.
- **Types:** `sin` of an Int64 is Float64; `cos` of a Float32 is Float32.
- **Degrees and radians are inverses.** This case's first version computed a `Max`
  inside a `Select`, which is refused, and skipped its check when the error came
  back; it now aggregates and fails on any error.

| tooth | result |
| --- | --- |
| `sin` computing `cos` | **bites** |
| `IsMath` losing its second range | **bites:** the types, the classification, and the inverse functions |
| `degrees` and `radians` the same factor | **bites** |

**Gate:** test-all 110 ok, race 22 ok, levels, vet ×3 and the bench engine tests clean; PDS-H SF=0.1 22/22 against DuckDB.

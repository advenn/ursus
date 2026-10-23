# Step 322 — as built

**Selectors:** `polars.selectors`, as a package of its own, `ursus/selector`, as
`ursus-api.md` §5 designed it (`polars-parity.md` §2).

## 1. The API

```go
import "github.com/advenn/ursus/selector"

lf.Select(selector.Numeric().Difference(selector.ByName("id")).Expr())
lf.WithColumns(selector.Float().Exclude("price").Expr().Round(2))
```

A `Selector` is a choice of columns, resolved against the schema when the query is
planned, so one written selector serves any schema; its `Expr` is the expression of
its columns, which goes wherever an expression does, `cs.Float().Expr().Round(2)`.

| Kind | Selectors |
| --- | --- |
| Types | `Numeric` (integers, floats, Decimals), `Integer`, `SignedInteger`, `UnsignedInteger`, `Float`, `Decimal`, `Temporal`, `Date`, `Datetime` (any unit and zone), `Duration`, `Time`, `String`, `Binary`, `Boolean`, `Enum`, `List`, `Struct`, `Nested`, and `ByDType` of exact types |
| Names | `StartsWith`, `EndsWith`, `Contains` (any of several), `Matches` (a regular expression anywhere in the name), `Alpha`, `Digit`, `Alphanumeric` (every script's), `ByName` |
| Places | `ByIndex` (negative from the end), `First`, `Last` |
| Sets | `All`, `Empty`; `Union`, `Intersect`, `Difference`, `SymmetricDifference`, `Complement`: Polars' `|`, `&`, `-`, `^`, `~`, named so that a selector's complement is never taken for an expression's negation; and `Exclude(names...)` |

**Order:** the schema's, but `ByName` and `ByIndex` alone, which answer in the order
written, as Polars' do; a set of selectors answers in the schema's order.

**Refused:** a name or a place the frame lacks, as Polars refuses it; a pattern that
does not compile, when the query is planned. A selector that matches nothing selects
nothing.

**`Expand(schema)`** is the names a selector selects in a schema, Polars'
`expand_selector`.

**Not built:** `categorical` and `array`, whose types ursus lacks, and `object`.

## 2. How

- **`internal/expr`** gains `SetMatcher` and `NotMatcher` beside the matchers `Col`,
  `ColRegex` and `Exclude` already used; each walks the schema's names, so a set
  answers in the schema's order.
- **The package builds the root's expressions** through `internal/exprbridge`, a
  `Wrap` the root sets when it initialises: the root's API names no internal type, and
  the package needs no root function taking one.
- A selector is resolved where `Col("^…$")` is, at expansion, so it costs the query
  nothing once planned.

**Found by the first gate:** `make levels` refused the draft, packages
`internal/exprbridge` and `selectors` having no declared level. The level table
already held `selector`, L80, and `ursus-api.md` §5 its API: the singular name, and
the set algebra named, `Union` to `Complement`, `Exclude` a method, and `Expand`. The
draft had followed Polars' spelling instead, `selectors` with `Or`, `And`, `Minus`,
`Xor` and `Not`. It follows the design now; `internal/exprbridge` is level 25, above
the expression IR it wraps.

## 3. Tests

**`TestSelectorsAsPolars`,** over one schema of eight types, against Polars 1.44.1's
`expand_selector`: each selector, the set algebra, `Exclude` of a name not selected,
`ByName` in its own order, a selector as an expression whose names are kept, the
refusals, `String`'s rendering, and `Expand` of a union, of names alone and of a
name not there.

## 4. Docs

- **Selecting:** a paragraph and an example.
- **Coming from Polars:** a row.
- **The CHANGELOG, `feature-gaps.md`, `polars-parity.md`:** the package.

## 5. Teeth, all 10 biting

- a union as an intersection, and an intersection as a union;
- a difference keeping both sides;
- a symmetric difference as equality;
- a complement dropping nothing;
- `StartsWith` as `EndsWith`;
- `Alpha` as `Digit`;
- `Temporal` as `Numeric`;
- a bad pattern taken as every column;
- the bridge left unset.

One first failed the build, the complement's, which left a variable unused, and was
rewritten.

**Gate, the second:** test-all 130 ok, race 26 ok (the root package 513 s here),
levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers match DuckDB.
The first had passed test-all and race and failed levels, above.

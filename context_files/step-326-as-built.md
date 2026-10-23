# Step 326 — as built

**The meta namespace:** Polars' `Expr.meta`, as `ursus-api.md` §4 designs
`MetaExpr` (`polars-parity.md` §2).

## 1. The API

| Method | Polars | What it answers |
| --- | --- | --- |
| `RootNames()` | `root_names` | the columns read by name, a name each time it is read: an operation's inputs left to right, a conditional's branches before its condition, a selector none |
| `OutputName()` | `output_name` | the column's name, or an error for an expression that may make several, "unable to find root column name …" |
| `HasMultipleOutputs()` | `has_multiple_outputs` | a selector anywhere in it: several names, a pattern, a type, `All`, `Exclude` |
| `IsColumn()` | `is_column` | one column by name, unnamed |
| `IsColumnSelection(allowAliasing)` | `is_column_selection` | a column or a selector, under a name with `allowAliasing` |
| `IsLiteral(allowAliasing)` | `is_literal` | a literal, under a name with `allowAliasing` |
| `IsRegexProjection()` | `is_regex_projection` | `ColRegex` |
| `UndoAliases()` | `undo_aliases` | the expression without every alias and name function in it |
| `Eq(other)` | `eq` | one computation, by `expr.Identity`: `Lit(1)` is not `Lit(1.0)` |
| `Pop()` | `pop` | the outermost operation's inputs, the last first, as Polars gives them |
| `TreeFormat()` | `tree_format` | a drawing of the expression, an operation a line, ursus's own |

**Where ursus differs:**

- **`Col` never reads a name as a pattern,** an anchored one too, by an earlier
  decision, so `Col("^a$")` is a column and `ColRegex("^a$")` the pattern; Polars'
  `col("^a$")` is the pattern.
- **`TreeFormat` draws ursus's own tree,** where Polars draws a grid of boxes.
- **`serialize`, `as_selector` and `show_graph` are not built.**

## 2. How

`expr_meta.go`, over the expression nodes: a walk for the names, `Match` for a
selector, `expr.Identity` for equality, `expr.Rebuild` to strip the names below the
top, and a label a node for the drawing.

## 3. Tests

**`TestMetaNamespace`,** every answer Polars':

- root names of eight expressions, over a window and a conditional among them;
- output names of six, and two refused;
- 28 answers of the predicates, `Eq` and `UndoAliases`;
- `Pop` of four;
- the tree of `(a + b*2).alias("z")`.

## 4. Docs

- **Expressions:** a paragraph under "Names".
- **Coming from Polars:** a row.
- **The CHANGELOG, `feature-gaps.md`, `polars-parity.md`:** the namespace.

## 5. Teeth, all 7 biting

- a conditional's condition first;
- a selector named among the roots;
- `Pop` in natural order;
- aliasing never allowed;
- names undone at the top only;
- `Eq` by rendering, which lost the literals' types;
- several outputs seen nowhere.

**Gate:** test-all 130 ok, race 26 ok (the root package 527 s here), levels, vet ×3
and the bench engine tests clean; the 22 PDS-H answers match DuckDB.

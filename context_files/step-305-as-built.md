# Step 305 — as built

**P3: `PartitionBy`** (`v0.6.7-scope.md` §3), as Polars' `DataFrame.partition_by`.

## 1. What Polars 1.44.1 answers, probed

`partition_by(by, *more_by, maintain_order=True, include_key=True, as_dict=False)`:

- **a part for each distinct key,** in the order each first appears, each part's rows
  in the frame's order;
- **keys group as a group-by's do:** a null key is a part of its own, -0 is 0, and NaN
  is NaN;
- **`include_key=False`** drops the key columns; with every column a key, each part
  is a frame of no columns, its rows' height;
- **`as_dict=True`** is a dict by key tuples, `('b', 1)`;
- **no rows** give no parts; **no key**, "at least one key is required"; an unknown
  column, "not found";
- **a List or a Struct key** partitions.

## 2. The API

```go
type Partition struct {
	Key   *DataFrame // one row, a column for each of By, typed as the frame's
	Frame *DataFrame // the rows holding the key
}
type PartitionOptions struct {
	By      []string
	DropKey bool // Polars' include_key=False
}
func (df *DataFrame) PartitionBy(ctx context.Context, o PartitionOptions) ([]Partition, error)
```

**The key is a one-row frame.** ursus reads values through typed accessors, `At[T]`,
`Column[T]` and `Rows[T]`, with no `[]any`. So a part's key is its values as columns
of their own types, read as `p.Key.At[string](0, "region")`, a null key's `ok` false.
This is the scope's "a slice of each key's values beside its frame": the slice is
the parts, the Key beside the Frame, where Polars' `as_dict` keys a dict by tuples
Go has no map for.

**An options struct,** as `PivotOptions` and `UnpivotOptions` are. `DropKey` is the
negative of Polars' `include_key`, so that the zero value is Polars' default.

**No `maintain_order`:** the parts always keep first appearance, which is Polars'
default; its other order is no order. **No lazy form:** Polars has none, and the
scope's claim is that it holds its frame, as `Pivot` held its until step 303.

## 3. How it works

`framepartition.go`:

1. the group-key encoder numbers each row's part by its key, in the order each first
   appears. A List or a Struct key is refused there, "cannot use a List(String)
   column as a key", as a group-by refuses one; §3's row now names `PartitionBy`;
2. a counting pass gives each part its rows, in the frame's order;
3. each part is a `Take` of the kept columns by its rows, and its Key a `Take` of By
   at its first row. A key is the value first seen: of 0 and then -0, 0.

The schemas are the frame's own fields, nullability kept, rather than
`frameOfColumns`, which makes every column nullable. It checks the context every 256
parts.

## 4. The site

`docs/guide/reshaping.md` on `dev`:

- a section, "Split by key: partition", new in 0.6.7;
- `Example_reshapePartitionBy`: the sales by region without the key, each part's
  height, and the first part. It is Polars' answer exactly.

**Found on the way:** the Transpose section said "Like `Pivot`, it is a
`DataFrame`'s", untrue since step 303's lazy pivot. It now says Transpose is a
`DataFrame`'s only, its schema waiting for the data. The Polars page maps
`partition_by`.

## 5. Tests and teeth

**`TestPartitionBy`,** every answer Polars 1.44.1's:

- by one column and by two, the null key among them;
- `DropKey`, and every column a dropped key, parts of no columns;
- the floats: -0 with 0 and NaN with NaN, the key the 0 first seen;
- no rows;
- the typed key read back, and a null key's;
- refused: no `By`, an unknown column, a column twice, a List key.

**Teeth, all biting:**

- rows backwards;
- `DropKey` ignored;
- a key from the last row, which the floats' 0 then -0 catches;
- no `By` let through;
- a column twice let through;
- parts numbered by row.

**Gate:** test-all 125 ok, race 25 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.

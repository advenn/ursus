# Step 533 — as built

**A Parquet schema element's `num_children` is bounded before anything is sized by
it.** [`step-531-as-built.md`](./step-531-as-built.md) §7 left a fuzz input open: a
file of 125 bytes whose root claims 16,909,320 children. Step 521's footer walk now
reads the schema's elements as a tree, as `FromParquet` does, and refuses as corrupt,
an I/O error, before arrow-go reads the footer, an element claiming more children than
the elements after it and a tree that does not close by the list's end. The second rule
is the one that matters: groups nested a thousand deep, each claiming every element
after it, pass the first, and their claims add up by the square of the elements
(§2.2). Every footer ursus, pyarrow and the fixtures' writers wrote passes as before.

## 1. The input

The fuzzer's file of 125 bytes, found at step 531. The footer is 64 bytes. Its schema
list's header says one element of compact type 13, which the walk reads as a
`SchemaElement` whatever the header says, as arrow-go does (step 521). The element is
the root: its name "schema", and `num_children` 16,909,320. No element follows it.

pyarrow 25.0.1 refuses the file: "Malformed schema: not enough elements".

Steps 423 and 521's walk kept each element's `num_children`, to measure the schema's
depth (`schemaDepth`), and held nothing else to them.

## 2. The design

### 2.1 Where the check goes

In `schemaDepth`, in `internal/source/parquet/footer.go`, which `measureFooter` calls on
every footer before `file.NewParquetReader` is given the file. It already walks the
whole footer as arrow-go reads it (step 521's `footerFields`, its lists sized against
their bytes, `errFooterList`) and keeps every schema element's `num_children` in
order. The loop that measured the depth now also reads the list as a tree: a stack of
each open group's children yet to come, as before.

### 2.2 The rules

A schema is refused as corrupt where its tree, read from the root as `FromParquet`
reads it:

1. has an element claiming more children than the elements after it,
   `errSchemaChildren`, at the root or nested;
2. does not close by the list's end, a group still owed children when the elements
   run out, `errSchemaOpen`: pyarrow's "not enough elements", and arrow-go's.

A tree that closes holds an element for each child its groups claim, so what its
groups claim adds up to its elements: linear in the footer, which step 521 already
bounds against its bytes.

**Rule 1 alone is not enough.** The root and 999 groups, each the next's parent, each
claiming every element after it, then leaves, pass it: each claim is the elements after
it exactly. The last group takes the leaves and closes, and the group above it is owed
one child more, so the tree does not close. Their claims add up by the square of the
elements, and the depth, 1,000, is within `maxSchemaDepth`. Only the list's end shows
the tree open, so the walk reads to it before the schema is built.

**Rule 1 is implied by rule 2.** An element of the tree claiming more than the elements
after it leaves the tree open at the end. It is kept because the brief asks for it and
because it names the element: "element 0 of 1 claims 16909320". Only the tests that
ask for `errSchemaChildren` by name catch its mutant (§5, M1), and the file is refused
either way.

**Elements after the root's tree are left alone.** pyarrow and arrow-go take the root's
tree for the schema and read no element after it. Probed with footers of nothing but a
schema:

| schema, each element's `num_children` | pyarrow 25.0.1 |
| --- | --- |
| root 2, one leaf | not enough elements |
| root 1, two leaves | reads `a` |
| root 1, group 1, two leaves | reads `g.a` |
| root 2, group 1, one leaf | not enough elements |
| root 1, group 5, one leaf | not enough elements |
| root 2^31−1, one leaf | not enough elements |
| root 0, one leaf | root had no children |
| root 2, group 2 of two leaves, a leaf | reads `g.a`, `g.b`, `c` |

So the walk reads the tree only: once the root's tree has closed, an element after it
is held to neither rule. That makes it refuse exactly where pyarrow and arrow-go find
the tree malformed, which the generated differential checks (§3). Refusing the trailing
elements too would refuse what both read; its mutant (M4) fails that differential. A
root claiming none with elements after it is refused by arrow-go's own check, and is
left to it as before.

**A schema nested past `maxSchemaDepth` is refused for its depth first.** `schemaDepth`
answers the depth with a tree error, and `measureFooter` judges the depth before the
error, so no file refused before this step changes kind: a corrupt tree nested past
1,000 levels is still the `ErrUnsupported` it was. Every footer the walk cannot read is
still answered with depth 0 and its error.

### 2.3 The error

Both wrap into `measureFooter`'s existing `uerr.KindIO` "corrupt: walking the footer",
so a refused file is an `ErrIO` naming it:

```
scan_parquet: corrupt: walking the footer: a schema element claims more children than the elements after it: element 0 of 1 claims 16909320
scan_parquet: corrupt: walking the footer: the schema's elements end before its groups' children do: 1 of its groups still owed children after its 3000 elements
```

## 3. The tests and their oracles

- **The seed**: §1's input, in `FuzzScanParquet`'s corpus, which runs it in every
  `go test`, under fuzzcheck's bounds.
- **`TestFooterWalkDifferential`, extended** (`internal/source/parquet`). The oracle is
  arrow-go itself, over every footer step 521 walks: 25 random files of ursus's writer,
  pyarrow 25.0.1's in `testdata/oracle/footers`, the fixtures in `testdata/parquet`,
  and `everyList`; and, new here, the fixtures in `testdata/parquet`'s folders, Polars'
  and pyarrow's under each codec and pyarrow's delta-encoded files of step 531. 66
  footers and 478 schema elements in the run recorded. Each footer
  is read back with `tparse`, a reader of the compact protocol into the test's `tval`,
  and written again; the walk passes it and arrow-go reads it. Then, for each element:
  claiming a child more than it does, the walk refuses it, as `errSchemaOpen` or
  `errSchemaChildren`, and arrow-go refuses it; claiming one more than the elements
  after it, the walk answers `errSchemaChildren` and arrow-go refuses it; claiming
  2^31−1, the walk answers `errSchemaChildren`. A writer's footer has no element
  after its tree, so one child more always leaves the tree open: that is a count, not
  a typed answer.
- **`TestASchemaTreeIsReadAsArrowGoReadsIt`**, a differential over generated inputs:
  4,000 random schemas of 1 to 12 elements, the root claiming 1 to 4 children and each
  other element 0 to 4, in footers whose groups have no type and whose leaves are
  REQUIRED INT64s, so that the tree is all arrow-go can refuse. The walk refuses one,
  as one of the two errors, exactly where arrow-go refuses it. A run: 2,315 refused,
  1,685 read, 1,354 of those with elements after the root's tree, counted from
  arrow-go's own tree. The probe table in §2.2 is pyarrow's agreement on the shapes; it
  is not a test, since pyarrow is not in the recorded oracle.
- **`TestASchemaTreeNotClosingIsRefusedAsCorrupt`**: the chain of §2.2, 1,000 groups
  and 2,000 leaves. The walk answers its depth, 1,000, and `errSchemaOpen`;
  `measureFooter` refuses it as `ErrIO`; with a group more, 1,001 deep, as
  `ErrUnsupported`, for its depth, as before.
- **`TestASchemaClaimingMoreChildrenThanItHoldsIsRefused`** (`internal/readfuzz`), through
  the public API: 46 files, each refused within `fuzzcheck.Run`'s bounds, its schema,
  its rows and its `Count` each an `ErrIO` saying corrupt. The fuzzer's input; the
  chain at 1,000 × 20,000 (`pqUnclosedSchema`); and, for each kind of chunk's file of
  step 517 (`pqIndexKinds`), whose honest file must read first, each schema element
  claiming 2^31−1 children, one more than the elements after it, and, a group, a child
  more than it holds, at the root and nested (in a Struct, a List's two groups), the
  last a tree that does not close. `pqForged` takes the claims as `children`, by an
  element's place in the list; a file without them is written byte for byte as before.
- **Seeds**: `pqForgedSeeds` adds a root claiming 2^31−1, a Struct claiming as many,
  and a List whose root claims a child more than its tree holds.

`go run ./internal/gen/testinventory -check`: 1,223 hand-typed lines against a baseline
of 1,223.

## 4. The fuzz runs

Each target ran alone, under the shared lock, from binaries built with
`go test -c -fuzz=…` and `GOFLAGS=-p=1 GOEXPERIMENT=simd GOAMD64=v3`, under
`prlimit --as=4294967296`, `GOMAXPROCS=4`, `-test.fuzztime=20s
-test.fuzzminimizetime=3s`, on the final code:

| target | execs | new interesting | outcome |
| --- | --- | --- | --- |
| `FuzzScanParquet` | 15,499 | 20 (117 in all) | PASS |
| `FuzzOwnPagesMatchArrow` | 10,324 | 25 (70 in all) | PASS |

Both ran at a few to a few dozen execs a second for their first 15 s, then at 1,500 to
3,300 a second; the binaries carry the fuzzer's coverage instrumentation.

## 5. The teeth

Each mutant was written from a saved copy of `footer.go`, both test binaries were
built, and the file was restored from the copy and compared with `cmp`, never by git.
The tests: the extended differential (D), the generated differential (G), the chain
(C), step 521's `TestMeasureFooterWalksEveryFooterArrowGoReads` (W), the public refusal
test (R) and `FuzzScanParquet`'s seeds (S), each under `prlimit --as=8589934592`.

| mutation | fails | how |
| --- | --- | --- |
| M3, both rules off (the base's behaviour) | D, G, C, R, S | D: "schema element 0 of 11 claiming 2^31-1 children: the walk answers <nil>"; R: the fuzzer's input not refused; S: the new seeds fail their target |
| M2, rule 2 off | D, G, C, R | D: "claiming 7 children, not 6: the walk answers <nil>"; G: "children [4 3 1 4 3 0 0 0]: the walk answers <nil>"; R: the chain not refused |
| M1, rule 1 off | D | "claiming 9 children, one past the elements after it: the walk answers the schema's elements end before its groups' children do", the right refusal under the other name (§2.2) |
| M4, elements after the tree held to both rules | G | "children [4 2 0 2 0 0 0 0 0 1 3 0]: the walk answers … element 10 of 12 claims 3, arrow-go <nil>" |
| M6, rule 1 by one too strict (`>=`) | D, G, C, W, R | D: "the walk refuses the footer: … element 6 of 8 claims 1", an honest footer; W: everyList refused |
| M7, rule 1 by one too loose (`> after+1`) | D | "claiming 7 children, one past the elements after it: the walk answers the schema's elements end before …" |
| M5, the depth judged after the tree | C | "a group more, 1001 deep: measureFooter answers ursus: scan_parquet: corrupt: walking the footer" |

## 6. Observations and what is left

- **A schema with elements after its root's tree reads**, as pyarrow and arrow-go read
  it, the elements unread. No writer writes one; it costs nothing.
- **A root claiming no children with elements after it** is left to arrow-go's own
  check, which refuses it.
- **The depth is still measured over the whole list**, the elements after the root's
  tree among them, as steps 423 and 521 measured it; such elements nested past 1,000
  levels are refused for their depth, as before.

## 7. Checks

After the last edit, with `GOFLAGS=-p=1 GOMAXPROCS=4 GOEXPERIMENT=simd GOAMD64=v3`:

All pass, run after merging origin/master 3627942 (steps 528 and 531):

| check | outcome |
| --- | --- |
| `go test ./internal/...` | ok, 82 s |
| `go test -timeout 40m .` in full, under the lock | ok, 282 s |
| `cd qcheck && go vet ./... && go test ./...` | ok, 89 s |
| `GOEXPERIMENT=simd go vet ./...` | clean |
| `go run ./internal/gen/levels` | ok |
| `GOEXPERIMENT=simd go run ./internal/gen/api -check` | 2,481 features, 92 added and 0 gone since v0.7.2 |
| `go run ./internal/gen/testinventory -check` | 1,223 lines, the baseline |
| `$(go env GOROOT)/bin/gofmt -l .` | prints nothing |

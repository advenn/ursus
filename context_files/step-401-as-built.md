# Step 401 — as built

**A minor version's breaks, checked.** C1 of `v0.8-scope.md`.

## 1. What was wrong

ursus is on ZeroVer, a stable 1.0 not expected yet: a minor version may rename,
remove or reshape an API with no deprecation, and the CHANGELOG lists each release's
breaks. Nothing checked the list. 0.6.7 listed 19 breaks and 0.6.8 three, written by
hand from the steps' records, and neither `apidiff` nor `gorelease` was in the
Makefile or CI.

**apidiff and gorelease do not finish ursus.** `apidiff -m` of v0.6.8 against this
tree, and `gorelease -base=v0.6.8`, which runs apidiff, were killed by the machine's
earlyoom: apidiff's correspondence check of the root package grows by 230 MB a
second, past 4.5 GB in twenty seconds, computing method sets in `checkMethodSet`
without end (x/exp of 2026-10-14). `dtype`, `i128`, `selector` and `ursustest` it
compares in a second each.

## 2. How

- **`internal/gen/api`**, a command, in the manner of Go's own `cmd/api`: it lists the
  public packages' API a feature a line, from their export data (`go list -export`,
  `go/importer`), so it adds nothing to `go.mod`. A feature is a function's
  signature; a type's kind, each exported field and embedded type; an interface's
  methods, each and as one line of their names, so a method added to it is a break;
  a constraint's terms; a constant's type and value; a variable's type; and every
  method a `*T` calls under `(*T)`, those a `T` calls under `(T)` again, so a
  method moved to a value's receiver breaks nothing and one moved to a pointer's
  does. Parameters are written without their names, and type parameters by their
  place, a type's `P0`, a function's or method's own `T0`, so renaming either changes
  no feature. An alias of an internal type is listed as the type it is, under the
  alias's name: `ursus.Series` was `data.Series` until v0.6.7, its methods data's.
- **A break is a feature the last tag had that the tree does not.** `api/v0.6.8.txt`
  holds v0.6.8's, 2,387; `make api` (`-check`) lists what the newest `api/v*.txt`
  has and the tree does not, and fails on one no Breaks section of the CHANGELOG's
  unreleased versions names, in backquotes: by its name, a member's type, its own
  name or `Type.Member`, a constant's type. CI's build job runs it.
- **A tag's list is written as it is cut:** `make api-tag TAG=v0.8.0`, at the commit
  tagged, which `-tag` heads with what it is.
- **Left:** a struct field added to a struct a caller compares or builds unkeyed, and
  a type that stops being comparable, which `cmd/api` does not see either. Ten
  features name an internal package's type, an option type's function over it or
  `Series`' embedded field: renaming that type shows as a break.

## 3. Measured

Over the three tags before, each from a checkout of the tag, its CHANGELOG's release
taken as unreleased:

| | features | added | gone | named in its Breaks |
| --- | --- | --- | --- | --- |
| v0.6.6 to v0.6.7 | 2,352 | 622 | 4 | 4 |
| v0.6.7 to v0.6.8 | 2,387 | 36 | 1 | 1 |
| v0.6.8 to this tree | 2,407 | 20 | 0 | |

v0.6.7's four: `AsOfTolerance` and `JoinMaintainOrder` taking another type,
`FillStrategy` a `uint64` where a `uint8`, `PivotOptions.On` a list of names where
one. v0.6.8's one: `SinkNDJSON` taking its own options. 0.7's breaks and 0.8's so
far are what code does, not its API. Before the listing kept every pointer's method,
it took `Series`' methods moving from `data.Series` to the embedded field for eleven
breaks, and before it expanded an internal alias it missed them. `make api` takes
about a second once the packages are built.

## 4. Tests

- **`TestABreakIsWhatACallerSees`** (`internal/gen/api`): two versions of a module of
  a public and an internal package. Gone, and so breaks: a method moved to a
  pointer's receiver, a method added to an interface, a term added to a constraint,
  a constant's value changed, a method of an internal type its public alias had, an
  embedded field taken out. Not: parameters renamed, a field added, a method moved to
  a value's receiver, type parameters renamed, a generic method's included.
- **`TestABreakIsNamedInAnUnreleasedBreaksSection`**: by its name, its type's or
  `Type.Member`, in backquotes, in a Breaks section of an unreleased version; not in
  its Features, nor a released version's Breaks; `Engine.Stats` names no other
  type's `Stats`.
- **`TestTheNewestTagIsByVersion`**: v0.10.0 after v0.9.1.
- **`TestAnUnnamedBreakFails`**: a signature changed fails the check named in
  Features, and passes named in Breaks.

**Teeth: 12 of 12 bite:** parameter names printed; a pointer's methods not listed; a
value's; no interface summary; an internal alias not expanded; type parameters by
name; a constant's value not listed; a Features section naming a break; a released
version naming one; the newest tag by its text; an unnamed break passing; an
embedded field not listed, which bit once the fixture embedded one.

**Gate:** the light gate, 29 packages ok, the new command's among them, levels, vet
×3 and the bench engine tests; the 22 PDS-H answers match DuckDB's at SF=0.1;
`make api` passes, 20 features added and none gone since v0.6.8.

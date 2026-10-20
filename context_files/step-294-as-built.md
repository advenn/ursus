# Step 294 — as built

**The documentation site, and being found.** The maintainer asked for both on
2026-10-20, beside the code:

- a search for "golang dataframe library" did not lead to ursus;
- ursus needed a documentation site like a professional library's, versioned by
  release.

[`docs-site-scope.md`](./docs-site-scope.md) scoped it. This step built the site's
frame, its first eleven pages and its publication, and the repository's words.

## 1. The site

**The tools** are pinned in `docs/requirements.txt`: MkDocs 1.6.1, Material for
MkDocs 9.7.7 and mike 2.2.0.

- Material is in maintenance mode, with critical fixes until 5 May 2027.
- Zensical, its successor, reads `mkdocs.yml` and keeps mike, so the pages move over
  as they are.

**`mkdocs.yml`:**

- the theme in a slate and Go's blue, light, dark and the system's;
- tabs, instant navigation, search suggestions, copy buttons, edit links;
- the snippets extension, reading the generated fragments (§2);
- mike's plugin with `canonical_version: latest/`;
- `exclude_docs` keeping `docs/examples`, the templates and the pins out of the site;
- link and anchor validation at warning, so `--strict` refuses a broken one.

**`docs/overrides/main.html`:**

- the words people search, in every title: "Joins - ursus: DataFrames for Go", and
  the home page "ursus: a DataFrame library for Go (Golang), modelled on Polars";
- Open Graph and Twitter tags;
- JSON-LD for a software project on the home page;
- `noindex` where the build asks for it, which `dev`'s does;
- Google's verification token when `DOCS_GOOGLE_VERIFICATION` gives one;
- the banner of a version that is not the latest: an older release's, or the
  development version's, ahead of it.

**The logo and favicon** are Material Design Icons' paw, under the Apache License 2.0.

**The pages:**

- home;
- installation, a quick start, coming from Polars;
- lazy and eager, expressions, types and nulls, memory and spilling;
- benchmarks;
- the API reference and the changelog.

Each page has a description of its own for search engines.

**Checked as a reader sees it,** through headless Chrome's screenshots of the built
site:

- A `line-height` of 1.25 on text blocks hid the horizontal strokes of every printed
  frame's box-drawing. A test page of the same glyphs in the same fonts drew them, and
  removing the rule brought them back. It was removed.
- The version selector did not appear. Material draws it only while mike's plugin has
  `version_selector` on, and mike has no selector of its own for Material, so turning
  it off had emptied the page's version config. It is on.

## 2. Every code block is a test

**`docs/examples`** is a package of tests only, at level 80 of `internal/gen/levels`
beside `internal/memcheck`.

- Its 25 `Example` functions are every code block on the pages, `go test` holding
  each to its `// Output:`.
- One, the sink of a larger-than-memory sort, is compiled and not run, since it reads
  files the tests do not have.
- They read `testdata/sales.csv`, eight orders.
- Their outputs held at `-cpu` 1, 2, 4 and 16, under `GODEBUG=simd=128`, with the
  experiment off, and under the race detector. That includes the spill's and the
  refusal's: the refusal names bytes held, which a thread count could have moved, and
  did not.

**`internal/gen/docsite`** writes the fragments the pages include, into the ignored
`docs/.snippets`:

- `<Name>.md`, an example's body less a tab and its output, as a Go block and a text
  block titled Output;
- `<Name>.main.md`, the same body as a whole program, `package main`, its imports
  grouped and gofmt'd, which the installation page shows;
- `CHANGELOG.md`, its links into the repository pointed at GitHub at `-ref`, and for a
  tag without the Unreleased section;
- `api-link.md`, pkg.go.dev at `-ref`, which is a tag or master.

`make docs`, `make docs-serve` and `make docs-build` run it.

**The 0.6.6 version** is built from these pages. They describe only what v0.6.6 has,
and their examples, copied into `git archive v0.6.6`, pass against its code.

## 3. Publication

`.github/workflows/docs.yml` runs one publication at a time:

- the examples, the fragments and a strict build come first;
- then mike to `gh-pages`: a push to master publishes `dev`, built noindex;
- a `v*` tag publishes its version and copies it to `latest`;
- a manual run publishes a version from a commit, for v0.6.6;
- inputs reach the shell through the environment, not the script.

**Rehearsed** in a scratch repository, before it ran here:

- 0.6.6 as `latest`, and `dev`, both deployed;
- every version's canonical URL is latest's page;
- only `dev` carries `noindex`;
- 0.6.6's API link is `@v0.6.6`, and its changelog has no Unreleased section;
- the root redirects to `latest`;
- mike writes `.nojekyll`.

## 4. Being found

- **The README's first line:** "ursus is a dataframe library for Go (Golang), modelled
  on Polars". It also gained:
  - badges for pkg.go.dev, CI, the docs and the licence;
  - the site's link;
  - a Docs section naming the site first.
- **The repository's description, topics and homepage,** and Pages switched on, after
  the push: §7.

## 5. Found on the way

- **A Go literal keeps its Go type.** `Col("a").Add(3)` of an Int32 column is Int64,
  and `Mul(2.5)` of a Float32 one Float64, where Polars' literal takes the column's
  type; only the fills meet the column (`fill.go`'s weak literals). A probe showed it
  while the expressions page was written. It is by design, but §9 of `feature-gaps.md`
  did not list it, and now does; the expressions and Polars pages say so.
- **`WithMemoryLimit`'s doc** said the default budget was Linux's alone. Step 181 gave
  it macOS and Windows, which `DefaultLimit`'s doc and the README say. Corrected.

## 6. Tests and teeth

**The tests:**

- `TestSplit`: a body's code and output, none, and code after the output refused;
- `TestChangelogPage`: links as blob and tree at the ref, and a release without
  Unreleased;
- `TestMainProgram`: a whole program's imports grouped;
- `TestThePagesIncludeExamplesThatExist`: every page's include names an example, so a
  renamed one fails in the gate, before a site build would find it.

**Teeth, all biting:**

- a release keeping Unreleased;
- no dedent;
- a directory link as a blob;
- the imports ungrouped;
- an example renamed;
- an output changed.

**Gate:** test-all 125 ok, race 25 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB. The new packages add ten to test-all and
two to the race run.

## 7. After the push

- **The docs workflow ran twice, both green:**
  - the push published `dev`;
  - a manual run from the step's commit, `1070c25`, with the tag v0.6.6, published
    0.6.6 as `latest`.
- **Pages** was switched on from `gh-pages` through the API. The site answers at
  https://advenn.github.io/ursus/:
  - the root goes to `latest`;
  - the selector shows "0.6.6 latest" and `dev`;
  - `latest`'s canonical URL and title are the ones built.
- **The repository:**
  - its description: "A dataframe library for Go (Golang), modelled on Polars: lazy
    queries with a query optimizer, Apache Arrow memory, SIMD kernels and out-of-core
    execution that spills to disk. Pure Go, no cgo.";
  - its homepage: the site;
  - 20 topics: the six it had, and polars, apache-arrow, parquet, csv, data-analysis,
    data-science, data-engineering, query-engine, query-optimizer, simd,
    lazy-evaluation, analytics, etl, golang-library.
- **pkg.go.dev:** the module proxy serves v0.6.6 now. pkg.go.dev's own fetch timed
  out, and it indexes from the proxy.

What only the maintainer can do is in `docs-site-scope.md` §4: Search Console, the
social preview image, awesome-go, and the announcements.

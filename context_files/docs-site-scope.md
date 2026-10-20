# The documentation site, and being found — scope

Asked on 2026-10-20, beside the code: a search for "golang dataframe library" does
not lead to ursus, and ursus needs a documentation site like a professional
library's, versioned by release.

## 1. What a search finds today

- **The repository's description** read "A more like a go native dataframe library
  experiment using generic methods and simd(experimental)". It never says "dataframe
  library for Go", the phrase people search, and it had no homepage.
- **Its topics** were six: arrow, dataframe, dataframe-library, dataframes, go,
  golang.
- **pkg.go.dev** has v0.6.0 to v0.6.5; v0.6.6 had not been fetched.
- **No site.** A search for the phrase gives pkg.go.dev pages and READMEs: gorilla,
  gandalff, df, fileframe, otters, golumn. Each says "DataFrame library for Go" in
  its first line, which is what a search engine matches; what orders them is links
  to them.

So the page text is ursus's to fix, and the links come from people finding it:
announcements, awesome-go, other projects. §4 lists what only the maintainer can
do.

## 2. The site

**Tool: MkDocs with Material for MkDocs, and mike for the versions.**

- Polars' own user guide is MkDocs Material, so ursus's readers, most of them
  Polars' too, find the same layout.
- Material entered maintenance mode at 9.7, with critical fixes until 5 May 2027.
  Its successor from the same team, Zensical, reads `mkdocs.yml` and keeps mike for
  versions, so the pages move over without rewriting.
- The tools are pinned in `docs/requirements.txt`.

**Hosting:** GitHub Pages from the `gh-pages` branch, at
`https://advenn.github.io/ursus/`. A custom domain is the maintainer's choice, and
a CNAME later.

**Versions:**

- one per release from v0.6.6 on, at `/0.6.6/`, `/0.6.7/` and so on;
- `latest`, a copy of the newest release, where the site's root goes;
- `dev`, master's, kept out of search engines;
- every version's canonical URL is its page in `latest`, as Read the Docs does, so
  a search engine ranks one copy;
- an older version's pages say so in a banner;
- the API reference link of each version is pkg.go.dev at that tag.

v0.6.6 has no docs of its own in its tree. Its version is built from the first
site's pages, whose examples run against v0.6.6's code (`git archive`) before it is
published.

**Every code block is a Go example that `go test` runs.**

- The pages' examples live in `docs/examples/`, `Example` functions with an
  `// Output:` comment, in the module, so the gate and CI run them.
- `internal/gen/docsite` writes each one's body and output as a fragment the pages
  include. An example that stops being true fails the build, as `example_test.go`
  does on pkg.go.dev.
- It also writes the changelog's page, its links into `context_files/` pointed at
  GitHub.

**Pages, first:**

- home;
- installation, a quick start, coming from Polars;
- the concepts: lazy and eager, expressions, types and nulls, memory and spilling;
- benchmarks;
- the changelog and the API reference.

**Then the user guide,** a chapter at a time:

- selecting and filtering;
- aggregation and group-by;
- windows;
- joins;
- reshaping;
- strings;
- lists and structs;
- dates and times;
- missing data;
- Go values in and out;
- Go functions;
- CSV, Parquet and Arrow.

**Built and published by `.github/workflows/docs.yml`:**

- master's push publishes `dev`;
- a `v*` tag publishes its version as `latest`;
- a manual run publishes any version from any commit, as v0.6.6's is.

The workflow runs the examples first, so a version whose examples fail is not
published.

## 3. Being found

- **The repository:**
  - a description that says what it is in the searched words;
  - twenty topics;
  - the site as its homepage.
- **The README's first line:** "a dataframe library for Go (Golang)", with badges
  for pkg.go.dev, CI, the docs and the licence, and a link to the site.
- **The site:**
  - a description on every page;
  - the words people search in the titles;
  - JSON-LD for a software project;
  - Open Graph tags;
  - a sitemap;
  - canonical URLs.
- **pkg.go.dev:** v0.6.6 fetched through the module proxy, and every tag after it.
  pkg.go.dev's first sentence is the package doc's, which already says "a
  dataframe library for Go".

## 4. What only the maintainer can do

- **Google Search Console:**
  - verify the site; `extra.google_site_verification` in `mkdocs.yml` takes the
    token;
  - submit `https://advenn.github.io/ursus/latest/sitemap.xml`.
- **The social preview image:** the repository's Settings page, which the API does
  not reach.
- **awesome-go:** a pull request adding ursus under "Science and Data Analysis".
  - Its rules want a Go Report Card grade and a coverage report linked.
  - The README can carry both badges once the maintainer wants them.
- **Announcements:** r/golang, Hacker News' Show HN, Golang Weekly, Gophers Slack.
  These are where the links come from that rank a page.

## 5. Order

1. **step 294:**
   - this scope;
   - the site's frame, the generator and the workflow;
   - the first pages;
   - the README's top;
   - the repository's description, topics and homepage;
   - Pages switched on;
   - v0.6.6 and `dev` published.
2. **the user guide,** a few chapters a step.
3. **0.6.7's frame operations** go on between them, as the maintainer directs.

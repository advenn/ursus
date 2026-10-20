// Command docsite writes the fragments the documentation site includes
// (context_files/docs-site-scope.md §2).
//
// Every code block on the site is an Example function in docs/examples, which `go
// test` runs and holds to its // Output: comment. For each one docsite writes
// <out>/<Name>.md: the function's body as a Go block and its output as a text
// block, so a page includes both with one line,
//
//	--8<-- "Example_quickstartFilter.md"
//
// and an example that stops being true fails the build before it reaches the site.
// <out>/<Name>.main.md is the same example as a whole program, package main and its
// imports, for a page that shows one. Each other function in docs/examples, the
// helpers the examples share, is <out>/func.<name>.md, its whole declaration, so a
// page shows the data its examples read as the code that builds it.
//
// It also writes, for the version of the site being built, which -ref names (a tag,
// or master for dev):
//
//   - <out>/CHANGELOG.md, the changelog with its links into the repository pointed at
//     GitHub at -ref, and for a tag without its Unreleased section;
//   - <out>/api-link.md, the version's API reference on pkg.go.dev.
//
// Run via `make docs`, and by .github/workflows/docs.yml before mkdocs builds.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const repoURL = "https://github.com/advenn/ursus"

func main() {
	examples := flag.String("examples", "docs/examples", "the directory of the site's Example functions")
	out := flag.String("out", "docs/.snippets", "the directory the fragments are written to")
	changelog := flag.String("changelog", "CHANGELOG.md", "the changelog")
	ref := flag.String("ref", "master", "the version's git ref: a tag such as v0.6.6, or master")
	flag.Parse()
	if err := run(*examples, *out, *changelog, *ref); err != nil {
		fmt.Fprintln(os.Stderr, "docsite:", err)
		os.Exit(1)
	}
}

func run(examples, out, changelog, ref string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	frags, err := fragments(examples)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(frags))
	for name := range frags {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		f := frags[name]
		if err := os.WriteFile(filepath.Join(out, name+".md"), []byte(fragment("go", f.code, f.output)), 0o644); err != nil {
			return err
		}
		if f.helper {
			continue
		}
		if err := os.WriteFile(filepath.Join(out, name+".main.md"),
			[]byte(fragment(`go title="main.go"`, f.program, f.output)), 0o644); err != nil {
			return err
		}
	}
	log, err := os.ReadFile(changelog)
	if err != nil {
		return err
	}
	release := strings.HasPrefix(ref, "v")
	if err := os.WriteFile(filepath.Join(out, "CHANGELOG.md"), changelogPage(log, ref, release), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, "api-link.md"), []byte(apiLink(ref)), 0o644)
}

// apiLink is the version's API reference: pkg.go.dev at the tag, or at master for
// dev, which pkg.go.dev resolves to master's latest commit.
func apiLink(ref string) string {
	url := "https://pkg.go.dev/github.com/advenn/ursus@" + ref
	return fmt.Sprintf("[github.com/advenn/ursus@%s on pkg.go.dev :octicons-link-external-16:](%s){ .md-button .md-button--primary }\n", ref, url)
}

// example is an Example function as the site shows it.
type example struct {
	code    string // the body, one tab of indentation removed; a helper's declaration
	program string // the body as a whole program
	output  string
	helper  bool // a function the examples share, not an Example
}

// fragments is each Example function by its name.
func fragments(dir string) (map[string]example, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no examples in %s", dir)
	}
	frags := map[string]example{}
	fset := token.NewFileSet()
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		imports := map[string]string{} // a package's name in the file, to its path
		for _, im := range f.Imports {
			p := strings.Trim(im.Path.Value, `"`)
			name := p[strings.LastIndex(p, "/")+1:]
			if im.Name != nil {
				name = im.Name.Name
			}
			imports[name] = p
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Body == nil {
				continue
			}
			if !strings.HasPrefix(fn.Name.Name, "Example") {
				name := "func." + fn.Name.Name
				if _, dup := frags[name]; dup {
					return nil, fmt.Errorf("%s: %s is defined twice", path, fn.Name.Name)
				}
				decl := src[fset.Position(fn.Pos()).Offset:fset.Position(fn.End()).Offset]
				frags[name] = example{code: string(decl), helper: true}
				continue
			}
			name := fn.Name.Name
			if _, dup := frags[name]; dup {
				return nil, fmt.Errorf("%s: %s is defined twice", path, name)
			}
			lb, rb := fset.Position(fn.Body.Lbrace).Offset, fset.Position(fn.Body.Rbrace).Offset
			code, output, err := split(string(src[lb+1 : rb]))
			if err != nil {
				return nil, fmt.Errorf("%s: %s: %w", path, name, err)
			}
			program, err := mainProgram(code, used(fn.Body, imports))
			if err != nil {
				return nil, fmt.Errorf("%s: %s: %w", path, name, err)
			}
			frags[name] = example{code: code, program: program, output: output}
		}
	}
	return frags, nil
}

// split separates an Example's body into its code, one tab of indentation removed,
// and its output, the // Output: comment's lines without their slashes.
func split(body string) (code, output string, err error) {
	lines := strings.Split(strings.Trim(body, "\n"), "\n")
	at := len(lines)
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "// Output:" || t == "// Unordered output:" {
			at = i
			break
		}
	}
	codeLines := lines[:at]
	for i, l := range codeLines {
		codeLines[i] = strings.TrimPrefix(l, "\t")
	}
	code = strings.Trim(strings.Join(codeLines, "\n"), "\n")
	if code == "" {
		return "", "", fmt.Errorf("an empty example")
	}
	if at == len(lines) {
		return code, "", nil
	}
	var outLines []string
	for _, l := range lines[at+1:] {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "//") {
			return "", "", fmt.Errorf("code after the output comment: %q", t)
		}
		t = strings.TrimPrefix(t, "//")
		outLines = append(outLines, strings.TrimPrefix(t, " "))
	}
	return code, strings.Trim(strings.Join(outLines, "\n"), "\n"), nil
}

// used is the paths of the imports a function's body names.
func used(body *ast.BlockStmt, imports map[string]string) []string {
	seen := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && imports[id.Name] != "" {
				seen[imports[id.Name]] = true
			}
		}
		return true
	})
	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// mainProgram is code as the body of a program's main, with the imports it uses: the
// standard library's first, then the rest, as goimports groups them. Code is
// indented back by a tab a line, which a raw string over several lines would not
// survive; no example has one.
func mainProgram(code string, paths []string) (string, error) {
	var std, other []string
	for _, p := range paths {
		if strings.Contains(strings.SplitN(p, "/", 2)[0], ".") {
			other = append(other, p)
		} else {
			std = append(std, p)
		}
	}
	var b strings.Builder
	b.WriteString("package main\n\nimport (\n")
	for _, p := range std {
		fmt.Fprintf(&b, "\t%q\n", p)
	}
	if len(std) > 0 && len(other) > 0 {
		b.WriteString("\n")
	}
	for _, p := range other {
		fmt.Fprintf(&b, "\t%q\n", p)
	}
	b.WriteString(")\n\nfunc main() {\n")
	for _, l := range strings.Split(code, "\n") {
		if l != "" {
			b.WriteString("\t" + l)
		}
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	src, err := format.Source([]byte(b.String()))
	return strings.TrimRight(string(src), "\n"), err
}

func fragment(lang, code, output string) string {
	var b strings.Builder
	b.WriteString("```" + lang + "\n" + code + "\n```\n")
	switch {
	case output == "":
	case wraps(output):
		b.WriteString("\n```{.text .wrap title=\"Output\"}\n" + output + "\n```\n")
	default:
		b.WriteString("\n```text title=\"Output\"\n" + output + "\n```\n")
	}
	return b.String()
}

// pageWidth is how many columns a code block shows at the site's width.
const pageWidth = 84

// wraps reports whether an output is prose wider than the page, an error message or
// a schema, which the site wraps rather than scrolls. A printed frame never wraps: a
// table broken across lines is no table, so it scrolls instead.
func wraps(output string) bool {
	if strings.Contains(output, "│") {
		return false
	}
	for _, l := range strings.Split(output, "\n") {
		if displayWidth(l) > pageWidth {
			return true
		}
	}
	return false
}

// displayWidth is a line's width in runes, as the site's monospace font draws the
// examples' output.
func displayWidth(s string) int { return len([]rune(s)) }

// repoLink is a Markdown link into the repository, relative to its root.
var repoLink = regexp.MustCompile(`\]\(\./([^)\s]*)\)`)

// changelogPage is the changelog as the site shows it: its repository links on
// GitHub at ref, and for a release without the Unreleased section.
func changelogPage(log []byte, ref string, release bool) []byte {
	if release {
		log = dropUnreleased(log)
	}
	return repoLink.ReplaceAllFunc(log, func(m []byte) []byte {
		path := string(repoLink.FindSubmatch(m)[1])
		kind := "blob"
		if path == "" || strings.HasSuffix(path, "/") {
			kind = "tree"
		}
		return fmt.Appendf(nil, "](%s/%s/%s/%s)", repoURL, kind, ref, path)
	})
}

// dropUnreleased removes the "## Unreleased" section, up to the next "## " heading.
func dropUnreleased(log []byte) []byte {
	lines := bytes.SplitAfter(log, []byte("\n"))
	var kept [][]byte
	skipping := false
	for _, l := range lines {
		if bytes.HasPrefix(l, []byte("## ")) {
			skipping = bytes.HasPrefix(l, []byte("## Unreleased"))
		}
		if !skipping {
			kept = append(kept, l)
		}
	}
	return bytes.Join(kept, nil)
}

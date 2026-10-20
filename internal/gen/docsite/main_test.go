package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSplit(t *testing.T) {
	code, out, err := split("\n\tdf := f()\n\tif ok {\n\t\tg()\n\t}\n\tfmt.Println(df)\n\t// Output:\n\t// a\n\t//\n\t// b\n")
	if err != nil {
		t.Fatal(err)
	}
	if want := "df := f()\nif ok {\n\tg()\n}\nfmt.Println(df)"; code != want {
		t.Errorf("code:\n%s\nwant:\n%s", code, want)
	}
	if want := "a\n\nb"; out != want {
		t.Errorf("output %q, want %q", out, want)
	}

	code, out, err = split("\n\tf()\n")
	if err != nil || code != "f()" || out != "" {
		t.Errorf("no output: %q %q %v", code, out, err)
	}
	if _, _, err := split("\n\tf()\n\t// Output:\n\t// a\n\tg()\n"); err == nil {
		t.Error("code after the output comment was taken")
	}
}

func TestChangelogPage(t *testing.T) {
	log := "# Changelog\n\nSee [`context_files/`](./context_files/).\n\n---\n\n" +
		"## Unreleased — toward v0.6.7\n\n[scope](./context_files/v0.6.7-scope.md)\n\n---\n\n" +
		"## v0.6.6 — 2026-10-19\n\n[scope](./context_files/v0.6.6-scope.md#2-x)\n"

	dev := string(changelogPage([]byte(log), "master", false))
	for _, want := range []string{
		"(https://github.com/advenn/ursus/tree/master/context_files/)",
		"(https://github.com/advenn/ursus/blob/master/context_files/v0.6.7-scope.md)",
		"(https://github.com/advenn/ursus/blob/master/context_files/v0.6.6-scope.md#2-x)",
	} {
		if !strings.Contains(dev, want) {
			t.Errorf("dev's changelog lacks %s:\n%s", want, dev)
		}
	}

	rel := string(changelogPage([]byte(log), "v0.6.6", true))
	if strings.Contains(rel, "Unreleased") || strings.Contains(rel, "v0.6.7-scope") {
		t.Errorf("a release's changelog kept the Unreleased section:\n%s", rel)
	}
	if !strings.Contains(rel, "## v0.6.6") || !strings.Contains(rel, "/blob/v0.6.6/context_files/v0.6.6-scope.md#2-x)") {
		t.Errorf("a release's changelog lost its own section or its links:\n%s", rel)
	}
}

func TestMainProgram(t *testing.T) {
	got, err := mainProgram("df := ursus.Frame()\nfmt.Println(df)",
		[]string{"fmt", "github.com/advenn/ursus", "log"})
	if err != nil {
		t.Fatal(err)
	}
	want := "package main\n\nimport (\n\t\"fmt\"\n\t\"log\"\n\n\t\"github.com/advenn/ursus\"\n)\n\n" +
		"func main() {\n\tdf := ursus.Frame()\n\tfmt.Println(df)\n}"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestThePagesIncludeExamplesThatExist holds every page's include to an Example in
// docs/examples, so a renamed example fails here, in the gate, and not only when the
// site is built.
func TestThePagesIncludeExamplesThatExist(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	frags, err := fragments(filepath.Join(root, "docs", "examples"))
	if err != nil {
		t.Fatal(err)
	}
	include := regexp.MustCompile(`(?m)^--8<-- "([^"]+)"$`)
	generated := map[string]bool{"CHANGELOG.md": true, "api-link.md": true}
	pages := 0
	err = filepath.WalkDir(filepath.Join(root, "docs"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "examples" || strings.HasPrefix(d.Name(), ".")) {
			return filepath.SkipDir
		}
		if d.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		pages++
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range include.FindAllStringSubmatch(string(src), -1) {
			name := m[1]
			if generated[name] {
				continue
			}
			name = strings.TrimSuffix(strings.TrimSuffix(name, ".md"), ".main")
			if _, ok := frags[name]; !ok {
				t.Errorf("%s includes %s, which no Example in docs/examples writes", path, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if pages == 0 {
		t.Fatal("no pages found")
	}
}

// TestHelpersAreTheirDeclarations holds a shared function's fragment to its whole
// declaration, so a page that shows the data its examples read shows the code.
func TestHelpersAreTheirDeclarations(t *testing.T) {
	frags, err := fragments(filepath.Join("..", "..", "..", "docs", "examples"))
	if err != nil {
		t.Fatal(err)
	}
	f, ok := frags["func.sales"]
	if !ok || !f.helper {
		t.Fatalf("no helper fragment for sales: %+v", f)
	}
	if !strings.HasPrefix(f.code, "func sales() *ursus.LazyFrame {") || !strings.HasSuffix(f.code, "\n}") {
		t.Errorf("sales's fragment is not its declaration:\n%s", f.code)
	}
	if _, ok := frags["func.Example_home"]; ok {
		t.Error("an Example was written as a helper")
	}
}

// TestFragmentWrapsProseNotTables wraps an output wider than the page when it is
// prose, an error or a schema, and never a printed frame, which a wrap would break.
func TestFragmentWrapsProseNotTables(t *testing.T) {
	long := strings.Repeat("x", pageWidth+1)
	if got := fragment("go", "f()", long); !strings.Contains(got, "```{.text .wrap") {
		t.Errorf("long prose not wrapped:\n%s", got)
	}
	if got := fragment("go", "f()", "│ "+long+" │"); strings.Contains(got, ".wrap") {
		t.Errorf("a wide table wrapped:\n%s", got)
	}
	if got := fragment("go", "f()", "short"); strings.Contains(got, ".wrap") {
		t.Errorf("short prose wrapped:\n%s", got)
	}
}

// TestTheExamplesFitThePage holds every fragment's code, and every printed frame in
// its output, to the page's width: a wider line scrolls sideways, and 31 did before
// step 295 noticed. Prose wider than the page wraps, and is not held.
func TestTheExamplesFitThePage(t *testing.T) {
	frags, err := fragments(filepath.Join("..", "..", "..", "docs", "examples"))
	if err != nil {
		t.Fatal(err)
	}
	for name, f := range frags {
		for _, l := range strings.Split(f.code, "\n") {
			if w := displayWidth(strings.ReplaceAll(l, "\t", "    ")); w > pageWidth {
				t.Errorf("%s: a line of code %d wide: %s", name, w, l)
			}
		}
		if wraps(f.output) {
			continue
		}
		for _, l := range strings.Split(f.output, "\n") {
			if w := displayWidth(l); w > pageWidth {
				t.Errorf("%s: an output line %d wide: %s", name, w, l)
			}
		}
	}
}

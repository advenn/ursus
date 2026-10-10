package kernel

// Step 169: a regexp of literals joined by .*, LIKE '%a%b%' written for Contains, is
// matched by substring searches. It must answer as the regexp does on every value,
// newlines, multi-byte runes and invalid UTF-8 included, and nothing else may be
// read as a chain.

import (
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
)

func TestALiteralChainIsOnlyLiteralsAndDotStars(t *testing.T) {
	for pat, want := range map[string][]string{
		"special.*requests": {"special", "requests"},
		"a.*b.*c":           {"a", "b", "c"},
		".*a.*":             {"a"},
		"plain":             {"plain"},
		"":                  {},
		".*":                {},
		"a.*.*b":            {"a", "b"},
		"a.b":               nil, // a single dot is one character, not any run
		"a.*?b":             nil, // lazy
		"a.**b":             nil,
		"^a.*b":             nil,
		"a.*b$":             nil,
		"(?i)a.*b":          nil,
		`a\.*b`:             nil,
		"a|b":               nil,
		"a\n.*b":            nil, // a literal newline
	} {
		got := literalChain(pat)
		if (got == nil) != (want == nil) || !slices.Equal(got, want) {
			t.Errorf("literalChain(%q) = %q, want %q", pat, got, want)
		}
	}
}

func TestContainsAChainAnswersAsTheRegexp(t *testing.T) {
	rng := rand.New(rand.NewPCG(169, 1))
	alphabet := []string{"a", "b", "ab", "ba", "é", "\n", "\xff", "x", "special", "requests", " "}
	value := func() string {
		var b strings.Builder
		for range rng.IntN(12) {
			b.WriteString(alphabet[rng.IntN(len(alphabet))])
		}
		return b.String()
	}
	pats := []string{"special.*requests", "a.*b", "b.*a.*b", "é.*x", ".*ab.*", "", "x", "ab.*ab"}
	const n = 4_000
	vals := make([]string, n)
	for i := range vals {
		vals[i] = value()
	}
	col := data.NewString("s", vals, bitmap.AllSet(n))
	for _, pat := range pats {
		re := regexp.MustCompile(pat)
		if literalChain(pat) == nil {
			t.Fatalf("%q is not read as a chain", pat)
		}
		got, err := StrCall(expr.FnStrContains, "c", dtype.Bool, col, []any{pat, false}, re)
		if err != nil {
			t.Fatal(err)
		}
		bits := got.Bools()
		for i, v := range vals {
			if want := re.MatchString(v); bits.Get(i) != want {
				t.Fatalf("Contains(%q) on %q is %v, the regexp says %v", pat, v, bits.Get(i), want)
			}
		}
	}
}

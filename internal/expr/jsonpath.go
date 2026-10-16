package expr

import (
	"strconv"
	"strings"

	"github.com/advenn/ursus/internal/uerr"
)

// The JSONPath of str.json_path_match (step 271), read when the plan is typed so a
// path it cannot follow is refused before any row is read.

// JSONPathStep is one step of a JSONPath: a key, an index, or a key looked for at any
// depth.
type JSONPathStep struct {
	Key     string
	Index   int
	IsIndex bool
	Deep    bool
}

// ParseJSONPath reads the subset of JSONPath Polars' json_path_match is used with:
// $, then .key, ['key'], [n] (from the end where negative) and ..key, which finds the
// first key of that name at any depth.
func ParseJSONPath(p string) ([]JSONPathStep, error) {
	bad := func(why string) error {
		return uerr.New(uerr.KindValue, "str.json_path_match", "JSONPath %q %s", p, why).
			Hint("$ then .key, ['key'], [n] or ..key")
	}
	if !strings.HasPrefix(p, "$") {
		return nil, bad("does not start at $")
	}
	var steps []JSONPathStep
	rest := p[1:]
	for rest != "" {
		switch {
		case strings.HasPrefix(rest, ".."):
			k, r := pathKey(rest[2:])
			if k == "" {
				return nil, bad("has no key after ..")
			}
			steps, rest = append(steps, JSONPathStep{Key: k, Deep: true}), r
		case rest[0] == '.':
			k, r := pathKey(rest[1:])
			if k == "" || k == "*" {
				return nil, bad("has a step that is not a key")
			}
			steps, rest = append(steps, JSONPathStep{Key: k}), r
		case rest[0] == '[':
			end := strings.IndexByte(rest, ']')
			if end < 0 {
				return nil, bad("has an unclosed [")
			}
			in := strings.TrimSpace(rest[1:end])
			rest = rest[end+1:]
			if len(in) >= 2 && (in[0] == '\'' || in[0] == '"') && in[len(in)-1] == in[0] {
				steps = append(steps, JSONPathStep{Key: in[1 : len(in)-1]})
				continue
			}
			i, err := strconv.Atoi(in)
			if err != nil {
				return nil, bad("has a bracket that is neither a key nor an index")
			}
			steps = append(steps, JSONPathStep{Index: i, IsIndex: true})
		default:
			return nil, bad("has a step that is not ., [ or ..")
		}
	}
	return steps, nil
}

// pathKey is a dotted key, up to the next . or [.
func pathKey(s string) (string, string) {
	i := strings.IndexAny(s, ".[")
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i:]
}

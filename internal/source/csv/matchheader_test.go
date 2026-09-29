package csv

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus/internal/uerr"
)

// TestMatchHeader: a later part's header maps onto the reference's columns by
// name, or is refused naming both parts.
func TestMatchHeader(t *testing.T) {
	wanted0 := []int{0, -1, 1} // a -> out 0, b skipped, c -> out 1
	ref := []string{"a", "b", "c"}
	for _, tc := range []struct {
		name   string
		ref    []string
		got    []string
		wanted []int
		want   []int  // nil means refused
		says   string // what the refusal must contain
	}{
		{"identical", ref, []string{"a", "b", "c"}, wanted0, wanted0, ""},
		{"reordered", ref, []string{"c", "a", "b"}, wanted0, []int{1, 0, -1}, ""},
		{"missing", ref, []string{"a", "b"}, wanted0, nil, "does not have the same columns"},
		{"extra", ref, []string{"a", "b", "c", "d"}, wanted0, nil, "does not have the same columns"},
		{"renamed", ref, []string{"a", "b", "x"}, wanted0, nil, "does not have the same columns"},
		{"repeated in the part", ref, []string{"a", "a", "c"}, wanted0, nil, "repeats the column"},
		{"repeated in the reference, reordered", []string{"a", "a"}, []string{"a", "a", "a"},
			[]int{0, 1}, nil, "repeats"},
		// WithSchema's width need not match the header's; a reordering cannot then
		// be mapped consistently.
		{"reordered under a narrower schema", []string{"a", "b"}, []string{"b", "a"},
			[]int{0}, nil, "another order"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := matchHeader(tc.ref, tc.got, tc.wanted, "one.csv", "two.csv")
			if tc.want == nil {
				if err == nil {
					t.Fatalf("mapped to %v, want a refusal", got)
				}
				if !errors.Is(err, uerr.ErrSchema) || !strings.Contains(err.Error(), tc.says) ||
					!strings.Contains(err.Error(), "one.csv") || !strings.Contains(err.Error(), "two.csv") {
					t.Errorf("refusal = %v, want ErrSchema naming both parts and saying %q", err, tc.says)
				}
				return
			}
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("mapping = %v, want %v", got, tc.want)
			}
		})
	}
}

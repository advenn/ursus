package ursus_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/ursustest"
)

// splitFrame is the fixture the whole file shares. It carries the three cases that
// a naive split collapses — an ordinary value, an EMPTY string, and a NULL — plus a
// multi-byte value, a value with no separator in it, and one with adjacent
// separators producing empty parts in the middle. What Split makes of each is the
// oracle's strsplit/split: an empty string splits to [""], not to the empty list, and
// a null to a null list, which differ only in a validity bit. The namespace fed, the
// list exploded, filtered and grouped by, extract_all, pad_start, pad_end and zfill
// are the group's other cases, spelled in oracle_strsplit_test.go since step 499.
func splitFrame() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.Values("id", []int64{0, 1, 2, 3, 4, 5}),
		ursus.ValuesNullable("s",
			[]string{"a,b", "", "x", "p,,q", "é,ü", "ignored"},
			[]bool{true, true, true, true, true, false}),
	)
}

// lists reads a List(String) column back as Go values, so a test can assert on the
// rows rather than on the rendered frame.
//
// The second return is validity, and it exists because it is the ONLY thing that
// distinguishes an empty list from a null one — both leave the offset unmoved.
func lists(t *testing.T, df *ursus.DataFrame, name string) ([][]string, []bool) {
	t.Helper()
	c, found := df.Batch().ByName(name)
	if !found {
		t.Fatalf("no column %q in\n%s", name, df)
	}
	if c.DType().ID() != dtype.TypeList {
		t.Fatalf("%s came back as %s, not a List", name, c.DType())
	}
	child := c.Lists().Child().Strings()

	out := make([][]string, 0, df.Height())
	valid := make([]bool, 0, df.Height())
	for row := range df.Height() {
		start, end, ok := c.Lists().Get(row)
		valid = append(valid, ok)
		vals := []string{}
		for e := start; e < end; e++ {
			vals = append(vals, child.Get(int(e)))
		}
		out = append(out, vals)
	}
	return out, valid
}

// TestSplitIsBatchSizeInvariant is why listns_test.go runs its sweep: the child
// offsets are relative to a batch's own child column, and concatenating two batches
// has to shift the second's. Correct at 8192 and wrong at 2 is the signature.
func TestSplitIsBatchSizeInvariant(t *testing.T) {
	query := func() *ursus.LazyFrame {
		return splitFrame().WithColumns(ursus.Col("s").Str().Split(",").Alias("parts"))
	}
	want, err := query().Collect(t.Context(), ursus.WithBatchSize(8192))
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{1, 2, 3, 4, 8192} {
		got, err := query().Collect(t.Context(), ursus.WithBatchSize(size), ursus.WithVerify())
		if err != nil {
			t.Fatalf("batch %d: %v", size, err)
		}
		ursustest.AssertFrameEqual(t, got, want, ursustest.CheckNullability())
	}
}

// TestSplitSurvivesTakeAndJoin covers the two operators that see a list they did
// not read from a file.
//
// The join half is the reason kernel.NullColumn gained a List arm in this step: an
// unmatched left row null-pads every right column, and before Split there was no
// way for one of those to be a List.
func TestSplitSurvivesTakeAndJoin(t *testing.T) {
	// Take: Filter after Split gathers through takeList; the rows it keeps are the
	// oracle's strsplit/split_then_filter.
	df, err := splitFrame().
		WithColumns(ursus.Col("s").Str().Split(",").Alias("parts")).
		Filter(ursus.Col("id").Gt(int64(2))).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 3 {
		t.Fatalf("got %d rows, want 3:\n%s", df.Height(), df)
	}

	// Join: a FULL join, and the kind matters. A left join gathers its right
	// columns through takeList with a NullIndex, which handles a missing row on its
	// own; only a right or full join builds a PAD for the other side, and the pad is
	// what kernel.NullColumn makes. So this is the shape that reaches the arm.
	left := splitFrame().WithColumns(ursus.Col("s").Str().Split(",").Alias("parts"))
	right := ursus.Frame(ursus.Values("id", []int64{0, 99}))

	joined, err := left.
		Join(right, ursus.JoinOn(ursus.Col("id")), ursus.JoinHow(ursus.JoinFull)).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("a full join must be able to pad a List column: %v", err)
	}
	// Six left rows, plus the unmatched right key 99.
	if joined.Height() != 7 {
		t.Fatalf("got %d rows, want 7:\n%s", joined.Height(), joined)
	}

	_, valid := lists(t, joined, "parts")
	nulls := 0
	for _, ok := range valid {
		if !ok {
			nulls++
		}
	}
	// The null input's own row, and the padded row for right key 99.
	if nulls != 2 {
		t.Errorf("got %d null lists, want 2 (one from the null input, one padded)\n%s",
			nulls, joined)
	}
}

// TestStrScalarAdditionsMatchStdlib is the differential the namespace is tested by,
// extended. Three of these have exact stdlib oracles, which is why they are the
// ones that shipped.
func TestStrScalarAdditionsMatchStdlib(t *testing.T) {
	corpus := []string{"", "abc", "  padded  ", "xxhixx", "héllo", "a.b*c", "\t\nws\r "}
	lf := ursus.Frame(ursus.Values("s", corpus))

	df, err := lf.WithColumns(
		ursus.Col("s").Str().StripCharsStart("x").Alias("ls"),
		ursus.Col("s").Str().StripCharsEnd("x").Alias("rs"),
		ursus.Col("s").Str().EscapeRegex().Alias("esc"),
		ursus.Col("s").Str().StripCharsStart("").Alias("lws"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	for i, s := range corpus {
		for _, c := range []struct {
			col  string
			want string
		}{
			{"ls", strings.TrimLeft(s, "x")},
			{"rs", strings.TrimRight(s, "x")},
			{"esc", regexp.QuoteMeta(s)},
			{"lws", strings.TrimLeft(s, " \t\n\r")},
		} {
			got, _, err := df.At[string](i, c.col)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("%s(%q) = %q, want %q", c.col, s, got, c.want)
			}
		}
	}
}

// TestStrNullsPropagateThroughTheNewFunctions — a null in is a null out, for every
// one of them, which is the rule the namespace already holds to.
func TestStrNullsPropagateThroughTheNewFunctions(t *testing.T) {
	lf := ursus.Frame(ursus.ValuesNullable("s", []string{"x"}, []bool{false}))

	scalars := map[string]ursus.Expr{
		"ps":  ursus.Col("s").Str().PadStart(3, "0"),
		"pe":  ursus.Col("s").Str().PadEnd(3, "0"),
		"z":   ursus.Col("s").Str().ZFill(3),
		"ls":  ursus.Col("s").Str().StripCharsStart("x"),
		"rs":  ursus.Col("s").Str().StripCharsEnd("x"),
		"esc": ursus.Col("s").Str().EscapeRegex(),
	}
	for name, e := range scalars {
		df, err := lf.Select(e.Alias(name)).Collect(t.Context(), ursus.WithVerify())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, ok, err := df.At[string](0, name); err != nil {
			t.Fatal(err)
		} else if ok {
			t.Errorf("%s(null) should be null", name)
		}
	}
}

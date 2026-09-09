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
// separators producing empty parts in the middle.
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
// distinguishes an empty list from a null one — both leave the offset unmoved. A
// helper that dropped it would make the central test of this file unwritable.
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

// TestSplitDistinguishesEmptyFromNull is the load-bearing test of step 45.
//
// An empty list and a null list have IDENTICAL offsets; only the validity bit tells
// them apart. So a test that checks lengths, or that reads the frame after an
// Explode, passes whichever way round the kernel has it — Explode erases the
// distinction by design. This one looks at the row.
func TestSplitDistinguishesEmptyFromNull(t *testing.T) {
	df, err := splitFrame().
		WithColumns(ursus.Col("s").Str().Split(",").Alias("parts")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	want := []struct {
		vals  []string
		valid bool
	}{
		{[]string{"a", "b"}, true},
		{[]string{""}, true}, // NOT the empty list: strings.Split("", ",") is [""]
		{[]string{"x"}, true},
		{[]string{"p", "", "q"}, true},
		{[]string{"é", "ü"}, true},
		{nil, false}, // a null input is a NULL list
	}

	got, valid := lists(t, df, "parts")
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i, w := range want {
		if valid[i] != w.valid {
			t.Errorf("row %d: valid = %v, want %v — an empty list and a null list "+
				"have the same offsets, so this bit is the whole difference",
				i, valid[i], w.valid)
			continue
		}
		if !w.valid {
			continue
		}
		if strings.Join(got[i], "\x00") != strings.Join(w.vals, "\x00") {
			t.Errorf("row %d: got %q, want %q", i, got[i], w.vals)
		}
	}
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

// TestSplitFeedsTheListNamespace proves the output is a real List that the
// consuming half already handles — not something that merely renders.
//
// Until this step every List column came out of a Parquet file, so the .list
// namespace had never been handed one built in memory.
func TestSplitFeedsTheListNamespace(t *testing.T) {
	df, err := splitFrame().
		WithColumns(
			ursus.Col("s").Str().Split(",").List().Len().Alias("n"),
			ursus.Col("s").Str().Split(",").List().Get(0).Alias("first"),
			ursus.Col("s").Str().Split(",").List().Sort().List().Get(0).Alias("smallest"),
		).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	for i, want := range []struct {
		n     uint32
		first string
		ok    bool
	}{
		{2, "a", true},
		{1, "", true},
		{1, "x", true},
		{3, "p", true},
		{2, "é", true},
		{0, "", false},
	} {
		n, ok, err := df.At[uint32](i, "n")
		if err != nil {
			t.Fatal(err)
		}
		if ok != want.ok || (want.ok && n != want.n) {
			t.Errorf("row %d: len = %v (ok=%v), want %v (ok=%v)", i, n, ok, want.n, want.ok)
		}
		f, ok, err := df.At[string](i, "first")
		if err != nil {
			t.Fatal(err)
		}
		if ok != want.ok || (want.ok && f != want.first) {
			t.Errorf("row %d: first = %q (ok=%v), want %q", i, f, ok, want.first)
		}
	}

	// The sorted-first of "p,,q" is the empty part, which is a value and not a null.
	if v, ok, err := df.At[string](3, "smallest"); err != nil {
		t.Fatal(err)
	} else if !ok || v != "" {
		t.Errorf("sorted first of [p,,q] = %q (ok=%v), want an empty string", v, ok)
	}
}

// TestSplitThenExplode is the canonical composition, and it needs no change to
// Explode: it reads the schema ResolveCall computed and finds List(String) there
// exactly as it would from a Parquet read.
//
// Explode ERASES the empty-versus-null distinction — both become one null row — so
// this test deliberately does not try to observe it here. That is what the first
// test in this file is for.
func TestSplitThenExplode(t *testing.T) {
	df, err := splitFrame().
		WithColumns(ursus.Col("s").Str().Split(",").Alias("part")).
		Explode("part").
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	// 2 + 1 + 1 + 3 + 2 parts, plus one null row for the null input.
	if df.Height() != 10 {
		t.Fatalf("got %d rows, want 10:\n%s", df.Height(), df)
	}
	if v, ok, err := df.At[string](0, "part"); err != nil {
		t.Fatal(err)
	} else if !ok || v != "a" {
		t.Errorf("first part = %q (ok=%v), want \"a\"", v, ok)
	}
	if _, ok, err := df.At[string](9, "part"); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Error("the null input should explode to a null row")
	}
}

// TestSplitSurvivesTakeAndJoin covers the two operators that see a list they did
// not read from a file.
//
// The join half is the reason kernel.NullColumn gained a List arm in this step: an
// unmatched left row null-pads every right column, and before Split there was no
// way for one of those to be a List.
func TestSplitSurvivesTakeAndJoin(t *testing.T) {
	// Take: Filter after Split gathers through takeList.
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
	got, _ := lists(t, df, "parts")
	if strings.Join(got[0], "|") != "p||q" {
		t.Errorf("after Filter the first row is %q, want [p,,q]", got[0])
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

// TestSplitNAndExtractAll cover the two functions that share Split's builder.
func TestSplitNAndExtractAll(t *testing.T) {
	df, err := ursus.Frame(
		ursus.Values("s", []string{"a,b,c", "one1two22three", "no-digits"}),
	).WithColumns(
		ursus.Col("s").Str().SplitN(",", 2).Alias("two"),
		ursus.Col("s").Str().ExtractAll(`\d+`).Alias("nums"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	two, _ := lists(t, df, "two")
	if strings.Join(two[0], "|") != "a|b,c" {
		t.Errorf("SplitN(2) = %q, want [a, \"b,c\"] — the last part keeps the rest", two[0])
	}

	nums, valid := lists(t, df, "nums")
	if strings.Join(nums[1], "|") != "1|22" {
		t.Errorf("ExtractAll = %q, want [1, 22]", nums[1])
	}
	// No match is an EMPTY list, not a null one: the input was present and simply
	// contained nothing matching.
	if !valid[2] || len(nums[2]) != 0 {
		t.Errorf("no match should be an empty list, got %q (valid=%v)", nums[2], valid[2])
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

// TestPadAndZFill has no stdlib oracle, so the expectations are written out. ZFill
// is the one worth reading: the sign stays in front of the zeros.
func TestPadAndZFill(t *testing.T) {
	in := []string{"5", "-5", "+5", "12345", "", "é"}
	df, err := ursus.Frame(ursus.Values("s", in)).WithColumns(
		ursus.Col("s").Str().PadStart(4, "0").Alias("ps"),
		ursus.Col("s").Str().PadEnd(4, ".").Alias("pe"),
		ursus.Col("s").Str().ZFill(4).Alias("z"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	for i, want := range []struct{ ps, pe, z string }{
		{"0005", "5...", "0005"},
		{"00-5", "-5..", "-005"}, // PadStart pads before the sign; ZFill after it
		{"00+5", "+5..", "+005"},
		{"12345", "12345", "12345"}, // already wider: never truncate
		{"0000", "....", "0000"},
		{"000é", "é...", "000é"}, // runes, not bytes: é is two bytes and one char
	} {
		for _, c := range []struct{ col, want string }{
			{"ps", want.ps}, {"pe", want.pe}, {"z", want.z},
		} {
			got, _, err := df.At[string](i, c.col)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("%s(%q) = %q, want %q", c.col, in[i], got, c.want)
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

// TestGroupByAListRefusesReadably. A List column cannot be a group key —
// GroupKeyEncoder has no arm for one — and Split is the first way a user could
// arrive at that. It must be an error naming the problem, not a panic.
func TestGroupByAListRefusesReadably(t *testing.T) {
	_, err := splitFrame().
		WithColumns(ursus.Col("s").Str().Split(",").Alias("parts")).
		GroupBy(ursus.Col("parts")).
		Agg(ursus.Len().Alias("n")).
		Collect(t.Context())
	if err == nil {
		t.Fatal("grouping by a List column should be refused")
	}
	if !strings.Contains(err.Error(), "List") {
		t.Errorf("the error should name the type: %v", err)
	}
}

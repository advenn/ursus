package ursus_test

import (
	"errors"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"ursus"
	"ursus/dtype"
	"ursus/internal/uerr"
	"ursus/ursustest"
)

// --- the temporal defects, one regression test each ---------------------------------
//
// Six defects, none of which any test touched — before step 6 zero test files
// referenced time.Time at all, which is exactly why the temporal type family could
// be advertised and half-broken at the same time.

// TestDatetimeRendersAsATimestamp is the one a user hits first. A Datetime is
// physically Int64, so without a guard it falls through renderCell's integer branch
// and 2024-01-01 prints as 1704067200000000000.
func TestDatetimeRendersAsATimestamp(t *testing.T) {
	ts := time.Date(2024, 1, 1, 12, 30, 45, 0, time.UTC)
	df, err := ursus.Frame(ursus.Values("ts", []time.Time{ts})).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got := df.String()
	if !strings.Contains(got, "2024-01-01T12:30:45") {
		t.Errorf("a Datetime must render as an instant:\n%s", got)
	}
	if strings.Contains(got, "1704110445000000000") {
		t.Errorf("a Datetime rendered as its raw tick count:\n%s", got)
	}
}

// TestCastTimeUnitRescales pins the worst of the six: the cast used to be a pure
// relabel, because Datetime(ns) and Datetime(s) are both physically Int64. A
// nanosecond timestamp reinterpreted as seconds silently became the year 55,974,289.
func TestCastTimeUnitRescales(t *testing.T) {
	ts := time.Date(2024, 1, 1, 12, 30, 45, 0, time.UTC)
	lf := ursus.Frame(ursus.Values("ts", []time.Time{ts}))

	for _, unit := range []dtype.TimeUnit{dtype.Second, dtype.Milli, dtype.Micro} {
		df, err := lf.Select(ursus.Col("ts").Cast(dtype.Datetime(unit, "UTC")).Alias("t")).
			Collect(t.Context(), ursus.WithVerify())
		if err != nil {
			t.Fatalf("%s: %v", unit, err)
		}
		if !strings.Contains(df.String(), "2024-01-01T12:30:45") {
			t.Errorf("cast to %s did not rescale:\n%s", unit, df)
		}
	}

	// And round-tripping back preserves the instant, since seconds divide nanos.
	df, err := lf.Select(
		ursus.Col("ts").Cast(dtype.Datetime(dtype.Second, "UTC")).
			Cast(dtype.Datetime(dtype.Nano, "UTC")).Alias("t"),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(df.String(), "2024-01-01T12:30:45") {
		t.Errorf("ns -> s -> ns lost the instant:\n%s", df)
	}
}

// TestInstantPlusDurationReconcilesUnits: Datetime(s) + Duration(ns) used to add a
// nanosecond count to a second count. Both are Int64, so the kernel ran happily and
// the answer was off by 10^9.
func TestInstantPlusDurationReconcilesUnits(t *testing.T) {
	ts := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	lf := ursus.Frame(ursus.Values("ts", []time.Time{ts}))

	for _, unit := range []dtype.TimeUnit{dtype.Second, dtype.Milli, dtype.Micro, dtype.Nano} {
		df, err := lf.Select(
			ursus.Col("ts").Cast(dtype.Datetime(unit, "UTC")).
				Add(ursus.Lit(90*time.Minute)).Alias("t"),
		).Collect(t.Context(), ursus.WithVerify())
		if err != nil {
			t.Fatalf("%s: %v", unit, err)
		}
		if !strings.Contains(df.String(), "2024-01-01T01:30:00") {
			t.Errorf("adding 90m at %s resolution gave the wrong instant:\n%s", unit, df)
		}
	}
}

// TestDurationScaledByNumber: `Duration * 2.5` bound a Float64 operand to an Int64
// kernel and failed at execution, for something the type checker had accepted.
func TestDurationScaledByNumber(t *testing.T) {
	lf := ursus.Frame(ursus.Values("d", []time.Duration{time.Hour}))

	df, err := lf.Select(ursus.Col("d").Mul(2).Alias("x")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("Duration * 2: %v", err)
	}
	if !strings.Contains(df.String(), "2h0m0s") {
		t.Errorf("got %s, want 2h0m0s", df)
	}
	// A fractional multiplier is refused at PLAN time. The original defect was that
	// it type-checked and then failed inside the kernel; the fix is an error that
	// arrives before execution and names a conversion that works.
	_, err = lf.Select(ursus.Col("d").Mul(2.5).Alias("x")).Collect(t.Context())
	if err == nil {
		t.Fatal("Duration * 2.5 has no exact answer at nanosecond resolution")
	}
	if !errors.Is(err, uerr.ErrType) {
		t.Errorf("must be a plan-time type error, not a runtime failure: %v", err)
	}
	for _, want := range []string{"fractional", "Cast"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}

	// And the named workaround actually works.
	scaled, err := lf.Select(
		ursus.Col("d").Cast(ursus.Float64).Mul(2.5).
			Cast(dtype.Duration(dtype.Nano)).Alias("x"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("the suggested conversion must work: %v", err)
	}
	if !strings.Contains(scaled.String(), "2h30m0s") {
		t.Errorf("1h * 2.5 should be 2h30m:\n%s", scaled)
	}
}

// TestNullCastsAgreeWithCanCast is the same divergence one type further along.
//
// CanCast's very first line is `if from == to || from.IsNull()`, so it promises a
// cast from Null to EVERY type; kernel.Cast implemented none of them and answered
// "cast from Null to Int64 is not implemented yet" at execution time. Found while
// building the conditional, because `Otherwise(Null(NullT))` promotes to the other
// branch's type and then has to get there.
//
// The result must also be usable, not merely produced: data.NewNull carries no
// payload buffer, so a relabelled null column fails in the first kernel that reads
// one. Sorting the result exercises exactly that.
func TestNullCastsAgreeWithCanCast(t *testing.T) {
	targets := []dtype.DataType{
		dtype.Int8, dtype.Int64, dtype.Uint32, dtype.Float32, dtype.Float64,
		dtype.Bool, dtype.String, dtype.Date, dtype.Datetime(dtype.Micro, "UTC"),
	}
	for _, to := range targets {
		t.Run(to.String(), func(t *testing.T) {
			if !dtype.CanCast(dtype.Null, to) {
				t.Fatalf("CanCast(Null, %s) is false; this test assumes it is true", to)
			}
			df, err := ursus.Frame(ursus.Values("k", []int64{1, 2})).
				Select(
					ursus.Col("k"),
					ursus.Null(ursus.NullT).Cast(to).Alias("v"),
				).
				Sort(ursus.Desc(ursus.Col("v"))).
				Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatalf("CanCast permits Null -> %s, so the kernel must implement it: %v", to, err)
			}
			if df.Height() != 2 {
				t.Fatalf("height = %d, want 2", df.Height())
			}
			if got := df.Schema().Field(1).Type; got != to {
				t.Errorf("type = %s, want %s", got, to)
			}
		})
	}
}

// TestStringCastsAgreeWithCanCast closes a plan-accepts / kernel-rejects
// divergence: dtype.CanCast permitted string<->numeric and string<->temporal, and
// kernel.Cast refused them, so the cast type-checked, planned, and failed at
// execution.
func TestStringCastsAgreeWithCanCast(t *testing.T) {
	f := ursus.Frame(
		ursus.Values("d", []string{"2024-03-15", "bad"}),
		ursus.Values("n", []string{"42", "bad"}),
		ursus.Values("b", []string{"true", "bad"}),
	)
	df, err := f.Select(
		ursus.Col("d").CastLossy(dtype.Date).Alias("date"),
		ursus.Col("n").CastLossy(dtype.Int64).Alias("num"),
		ursus.Col("b").CastLossy(dtype.Bool).Alias("flag"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("CanCast permits these, so the kernel must implement them: %v", err)
	}
	if df.Height() != 2 {
		t.Fatalf("got %d rows\n%s", df.Height(), df)
	}
	// A non-strict cast turns an unparseable value into a null, which is why the
	// type checker marks every non-strict cast nullable.
	for _, col := range []string{"date", "num", "flag"} {
		if _, ok, err := df.At[int64](1, col); err == nil && ok {
			t.Errorf("row 1 of %q should be null\n%s", col, df)
		}
	}

	// Strict names the row and the text.
	_, err = f.Select(ursus.Col("n").Cast(dtype.Int64)).Collect(t.Context())
	if err == nil {
		t.Fatal("a strict cast over unparseable text must fail")
	}
	for _, want := range []string{`"bad"`, "row 1", "Int64"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %s: %v", want, err)
		}
	}
}

// TestDurationColumnKeepsItsType: []time.Duration fell through to
// convertNamedSlice and landed as a bare Int64, losing the type.
func TestDurationColumnKeepsItsType(t *testing.T) {
	df, err := ursus.Frame(ursus.Values("d", []time.Duration{90 * time.Minute})).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := df.Schema().Field(0).Type; got.ID() != dtype.TypeDuration {
		t.Errorf("type = %s, want a Duration", got)
	}
	if !strings.Contains(df.String(), "1h30m0s") {
		t.Errorf("a Duration must render as a duration:\n%s", df)
	}
}

// --- the differential tests -----------------------------------------------------

// TestDtComponentsMatchGoTime is what licenses hand-written calendar extraction.
//
// Month lengths, leap years and ISO week numbering are rules, not formulas.
// Re-deriving them is how a library acquires an off-by-one-day bug that survives
// for a decade, so every component is checked against Go's own time package over a
// corpus built to hit the cases that break naive implementations.
func TestDtComponentsMatchGoTime(t *testing.T) {
	var times []time.Time
	add := func(y int, m time.Month, d, h, mi, s int) {
		times = append(times, time.Date(y, m, d, h, mi, s, 0, time.UTC))
	}
	// Leap days, century non-leap, year boundaries where the ISO week belongs to
	// the neighbouring year, and instants before the epoch.
	add(2024, time.February, 29, 12, 0, 0)   // leap day
	add(1900, time.February, 28, 23, 59, 59) // 1900 is NOT a leap year
	add(2000, time.February, 29, 0, 0, 0)    // 2000 IS
	add(2023, time.January, 1, 0, 0, 0)      // a Sunday: ISO week 52 of 2022
	add(2021, time.January, 3, 0, 0, 0)      // Sunday: ISO week 53 of 2020
	add(2019, time.December, 30, 0, 0, 0)    // Monday: ISO week 1 of 2020
	add(1969, time.July, 20, 20, 17, 40)     // before the epoch
	add(1900, time.January, 1, 0, 0, 0)
	add(2100, time.December, 31, 23, 59, 59)

	rng := rand.New(rand.NewPCG(7, 11))
	for range 200 {
		add(1900+rng.IntN(250), time.Month(1+rng.IntN(12)), 1+rng.IntN(28),
			rng.IntN(24), rng.IntN(60), rng.IntN(60))
	}

	lf := ursus.Frame(ursus.Values("ts", times))
	df, err := lf.Select(
		ursus.Col("ts").Dt().Year().Alias("y"),
		ursus.Col("ts").Dt().Month().Alias("mo"),
		ursus.Col("ts").Dt().Day().Alias("d"),
		ursus.Col("ts").Dt().Hour().Alias("h"),
		ursus.Col("ts").Dt().Minute().Alias("mi"),
		ursus.Col("ts").Dt().Second().Alias("s"),
		ursus.Col("ts").Dt().Weekday().Alias("wd"),
		ursus.Col("ts").Dt().OrdinalDay().Alias("yd"),
		ursus.Col("ts").Dt().Quarter().Alias("q"),
		ursus.Col("ts").Dt().Week().Alias("wk"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	get := func(row int, col string) int32 {
		v, _, err := df.At[int32](row, col)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	for i, tm := range times {
		_, isoWeek := tm.ISOWeek()
		wantWd := int32(tm.Weekday())
		if wantWd == 0 {
			wantWd = 7 // ISO: Monday is 1, Sunday is 7
		}
		checks := []struct {
			name string
			got  int32
			want int32
		}{
			{"year", get(i, "y"), int32(tm.Year())},
			{"month", get(i, "mo"), int32(tm.Month())},
			{"day", get(i, "d"), int32(tm.Day())},
			{"hour", get(i, "h"), int32(tm.Hour())},
			{"minute", get(i, "mi"), int32(tm.Minute())},
			{"second", get(i, "s"), int32(tm.Second())},
			{"weekday", get(i, "wd"), wantWd},
			{"ordinal_day", get(i, "yd"), int32(tm.YearDay())},
			{"quarter", get(i, "q"), int32((int(tm.Month())-1)/3 + 1)},
			{"week", get(i, "wk"), int32(isoWeek)},
		}
		for _, c := range checks {
			if c.got != c.want {
				t.Fatalf("%s of %s: got %d, want %d", c.name, tm.Format(time.RFC3339), c.got, c.want)
			}
		}
	}
}

// TestStrMatchesStdlib is the same argument for the string family: a hand-written
// implementation is allowed only because something independent checks it.
//
// The corpus carries empty strings, multi-byte UTF-8 and invalid UTF-8, because
// those are where a byte-oriented implementation and a rune-oriented one diverge.
func TestStrMatchesStdlib(t *testing.T) {
	inputs := []string{
		"", "a", "Hello World", "HELLO", "hello",
		"héllo", "日本語テキスト", "emoji 🎉 here",
		"  padded  ", "\ttabbed\t", "aaa", "abcabcabc",
		string([]byte{0xff, 0xfe, 'a'}), // invalid UTF-8
	}
	lf := ursus.Frame(ursus.Values("s", inputs))

	df, err := lf.Select(
		ursus.Col("s").Str().ToLower().Alias("lower"),
		ursus.Col("s").Str().ToUpper().Alias("upper"),
		ursus.Col("s").Str().LenBytes().Alias("nbytes"),
		ursus.Col("s").Str().LenChars().Alias("nchars"),
		ursus.Col("s").Str().Contains("l", true).Alias("has_l"),
		ursus.Col("s").Str().StartsWith("h").Alias("starts_h"),
		ursus.Col("s").Str().EndsWith("o").Alias("ends_o"),
		ursus.Col("s").Str().ReplaceAll("a", "X", true).Alias("repl"),
		ursus.Col("s").Str().StripChars("").Alias("trimmed"),
		ursus.Col("s").Str().CountMatches("a", true).Alias("n_a"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	str := func(row int, col string) string {
		v, _, err := df.At[string](row, col)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	u32 := func(row int, col string) uint32 {
		v, _, err := df.At[uint32](row, col)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	b := func(row int, col string) bool {
		v, _, err := df.At[bool](row, col)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	differed := false
	for i, s := range inputs {
		if got, want := str(i, "lower"), strings.ToLower(s); got != want {
			t.Errorf("ToLower(%q) = %q, want %q", s, got, want)
		}
		if got, want := str(i, "upper"), strings.ToUpper(s); got != want {
			t.Errorf("ToUpper(%q) = %q, want %q", s, got, want)
		}
		if got, want := u32(i, "nbytes"), uint32(len(s)); got != want {
			t.Errorf("LenBytes(%q) = %d, want %d", s, got, want)
		}
		if got, want := u32(i, "nchars"), uint32(len([]rune(s))); got != want {
			t.Errorf("LenChars(%q) = %d, want %d", s, got, want)
		}
		if u32(i, "nbytes") != u32(i, "nchars") {
			differed = true
		}
		if got, want := b(i, "has_l"), strings.Contains(s, "l"); got != want {
			t.Errorf("Contains(%q, \"l\") = %v, want %v", s, got, want)
		}
		if got, want := b(i, "starts_h"), strings.HasPrefix(s, "h"); got != want {
			t.Errorf("StartsWith(%q, \"h\") = %v, want %v", s, got, want)
		}
		if got, want := b(i, "ends_o"), strings.HasSuffix(s, "o"); got != want {
			t.Errorf("EndsWith(%q, \"o\") = %v, want %v", s, got, want)
		}
		if got, want := str(i, "repl"), strings.ReplaceAll(s, "a", "X"); got != want {
			t.Errorf("ReplaceAll(%q) = %q, want %q", s, got, want)
		}
		if got, want := str(i, "trimmed"), strings.TrimSpace(s); got != want {
			t.Errorf("StripChars(%q) = %q, want %q", s, got, want)
		}
		if got, want := u32(i, "n_a"), uint32(strings.Count(s, "a")); got != want {
			t.Errorf("CountMatches(%q, \"a\") = %d, want %d", s, got, want)
		}
	}
	if !differed {
		t.Error("no corpus entry made LenBytes and LenChars differ, so the multi-byte " +
			"cases proved nothing")
	}
}

// TestStrRegexAndLiteralDiffer: the literal flag has to actually do something, or
// every pattern is silently a regex and "a.b" matches "axb".
func TestStrRegexAndLiteralDiffer(t *testing.T) {
	lf := ursus.Frame(ursus.Values("s", []string{"a.b", "axb"}))

	df, err := lf.Select(
		ursus.Col("s").Str().Contains("a.b", true).Alias("lit"),
		ursus.Col("s").Str().Contains("a.b", false).Alias("re"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	lit0, _, _ := df.At[bool](0, "lit")
	lit1, _, _ := df.At[bool](1, "lit")
	re0, _, _ := df.At[bool](0, "re")
	re1, _, _ := df.At[bool](1, "re")

	if !lit0 || lit1 {
		t.Errorf("literal \"a.b\" must match only \"a.b\", got %v and %v", lit0, lit1)
	}
	if !re0 || !re1 {
		t.Errorf("regex \"a.b\" must match both, got %v and %v", re0, re1)
	}

	// An invalid pattern is a user error naming the pattern, not a panic.
	_, err = lf.Select(ursus.Col("s").Str().Contains("a(", false)).Collect(t.Context())
	if err == nil {
		t.Fatal("an invalid regex must be an error")
	}
	if !strings.Contains(err.Error(), "a(") {
		t.Errorf("the error should name the pattern: %v", err)
	}
}

// TestNamespaceNullsPropagate: null in, null out, for every function. A kernel that
// reads the payload at a null position is reading a zero it should never see.
func TestNamespaceNullsPropagate(t *testing.T) {
	strs := ursus.Frame(ursus.ValuesNullable("s",
		[]string{"abc", "", "def"}, []bool{true, false, true}))
	df, err := strs.Select(
		ursus.Col("s").Str().ToLower().Alias("a"),
		ursus.Col("s").Str().LenBytes().Alias("b"),
		ursus.Col("s").Str().Contains("a", true).Alias("c"),
		ursus.Col("s").Str().Slice(0, 2).Alias("d"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"a", "b", "c", "d"} {
		if _, ok, err := df.At[string](1, col); err == nil && ok {
			t.Errorf("%q row 1 must be null\n%s", col, df)
		}
	}

	ts := time.Date(2024, 5, 5, 0, 0, 0, 0, time.UTC)
	times := ursus.Frame(ursus.ValuesNullable("t",
		[]time.Time{ts, ts, ts}, []bool{true, false, true}))
	dt, err := times.Select(
		ursus.Col("t").Dt().Year().Alias("y"),
		ursus.Col("t").Dt().Truncate(24*time.Hour).Alias("day"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := dt.At[int32](1, "y"); ok {
		t.Errorf("a null instant has no year\n%s", dt)
	}
}

// TestNamespaceRefusals: the wrong receiver type is a plan-time error naming the
// operand, not a runtime surprise.
func TestNamespaceRefusals(t *testing.T) {
	nums := ursus.Frame(ursus.Values("n", []int64{1}))
	durs := ursus.Frame(ursus.Values("d", []time.Duration{time.Hour}))

	cases := []struct {
		name string
		lf   func() *ursus.LazyFrame
		want string
	}{
		{"str on a number", func() *ursus.LazyFrame {
			return nums.Select(ursus.Col("n").Str().ToLower())
		}, "requires a String"},
		{"dt on a number", func() *ursus.LazyFrame {
			return nums.Select(ursus.Col("n").Dt().Year())
		}, "requires a Date, Time or Datetime"},
		{"calendar component of a Duration", func() *ursus.LazyFrame {
			return durs.Select(ursus.Col("d").Dt().Year())
		}, "a length, not a point in time"},
		{"total_hours of an instant", func() *ursus.LazyFrame {
			return ursus.Frame(ursus.Values("t", []time.Time{time.Now()})).
				Select(ursus.Col("t").Dt().TotalHours())
		}, "requires a Duration"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.lf().Collect(t.Context())
			if err == nil {
				t.Fatal("expected a type error")
			}
			if !errors.Is(err, uerr.ErrType) {
				t.Errorf("kind should be Type: %v", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error should explain %q:\n%v", c.want, err)
			}
		})
	}
}

// TestNamespacesAreParallelSafe: the kernels run inside BatchOps, which parallelise
// hands to N workers, so a compiled regex is shared across goroutines. regexp is
// documented safe for concurrent use; this asserts it rather than trusting it.
func TestNamespacesAreParallelSafe(t *testing.T) {
	n := 5000
	strs := make([]string, n)
	times := make([]time.Time, n)
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range n {
		strs[i] = "row-" + string(rune('a'+i%26))
		times[i] = base.AddDate(0, 0, i)
	}
	lf := ursus.Frame(ursus.Values("s", strs), ursus.Values("t", times))

	build := func() *ursus.LazyFrame {
		return lf.Select(
			ursus.Col("s").Str().Contains("row-[aeiou]", false).Alias("vowel"),
			ursus.Col("s").Str().ToUpper().Alias("up"),
			ursus.Col("t").Dt().Year().Alias("y"),
			ursus.Col("t").Dt().Week().Alias("wk"),
		)
	}
	// AssertFrameEqual rather than df.String(), which stops at ten rows and would
	// have compared a tenth of this fixture.
	var want *ursus.DataFrame
	for _, threads := range []int{1, 2, 4, 8} {
		df, err := build().Collect(t.Context(),
			ursus.WithThreads(threads), ursus.WithBatchSize(128), ursus.WithVerify())
		if err != nil {
			t.Fatalf("threads=%d: %v", threads, err)
		}
		if want == nil {
			want = df
			continue
		}
		ursustest.AssertFrameEqual(t, df, want)
	}
}

// TestTemporalCSVRoundTrip: temporal types round-tripped through neither format
// before step 6. Writing them as their storage integers was refused precisely
// because the writer's output would not be readable by its own reader — so the
// test that matters is that it now is.
func TestTemporalCSVRoundTrip(t *testing.T) {
	ts := time.Date(2024, 3, 15, 14, 30, 45, 0, time.UTC)
	src := ursus.Frame(
		ursus.Values("ts", []time.Time{ts, ts.Add(48 * time.Hour)}),
		ursus.Values("d", []time.Duration{90 * time.Minute, 2 * time.Second}),
	)
	want, err := src.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	path := writeFile(t, "temporal.csv", "")
	if err := src.SinkCSV(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	got, err := ursus.ScanCSV(path, ursus.WithSchema(want.Schema())).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("the writer's output must be readable by its own reader: %v", err)
	}
	if got.String() != want.String() {
		t.Errorf("round trip changed the data\ngot:\n%s\nwant:\n%s", got, want)
	}
}

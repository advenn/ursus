package ursus_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestTemporalGroupsAnswerAsTheBufferedSinkDid: step 196, v0.6.1 item O1.
//
// GroupByDynamic and Rolling buffered their whole input until step 196, which
// streams it. testdata/temporal/golden.json holds what the buffered sink answered,
// as a hash of each result in full, for a hundred and twenty configurations drawn
// with a fixed seed:
//
//   - every, period and offset, in minutes and hours, windows overlapping and gapped;
//   - the four closed conventions, the three labels, both starts, and boundaries;
//   - with and without a categorical key whose rows interleave;
//   - an index with ties and a value column with nulls;
//   - order-dependent aggregates, First, Last and Implode, beside Sum, Len, Min
//     and Median.
//
// Recorded once, on the buffered sink, with URSUS_TEMPORAL_GOLDEN=write.
func TestTemporalGroupsAnswerAsTheBufferedSinkDid(t *testing.T) {
	rng := rand.New(rand.NewPCG(196, 7))
	const n = 600
	ts, v, vok, g := make([]int64, n), make([]int64, n), make([]bool, n), make([]string, n)
	at := int64(1_700_000_000_000_000) // microseconds
	for i := range n {
		at += int64(rng.IntN(5)) * 7 * 60_000_000 // 0 to 28 minutes, ties included
		ts[i], v[i], vok[i] = at, int64(rng.IntN(100)), rng.IntN(10) != 0
		g[i] = string(rune('a' + rng.IntN(3)))
	}
	frame := ursus.Frame(ursus.Values("t", ts), ursus.ValuesNullable("v", v, vok), ursus.Values("g", g)).
		WithColumns(ursus.Col("t").Cast(ursus.Datetime(ursus.Micro, "")).Alias("t"))
	aggs := []ursus.Expr{
		ursus.Len().Alias("n"), ursus.Col("v").Sum().Alias("sum"), ursus.Col("v").Min().Alias("min"),
		ursus.Col("v").First().Alias("first"), ursus.Col("v").Last().Alias("last"),
		ursus.Col("v").Median().Alias("median"), ursus.Col("v").Implode().Alias("all"),
	}
	pick := func(xs ...string) string { return xs[rng.IntN(len(xs))] }
	closed := []ursus.Closed{ursus.ClosedLeft, ursus.ClosedRight, ursus.ClosedBoth, ursus.ClosedNone}

	got := map[string]string{}
	var names []string
	run := func(name string, lf *ursus.LazyFrame) {
		df, err := lf.Collect(t.Context(), ursus.WithBatchSize(64))
		if err != nil {
			got[name] = "error: " + err.Error()
		} else {
			rec, err := df.Record()
			if err != nil {
				t.Fatal(err)
			}
			var sb strings.Builder
			for _, c := range rec.Columns() {
				sb.WriteString(c.String())
				sb.WriteByte('\n')
			}
			rec.Release()
			sum := sha256.Sum256([]byte(sb.String()))
			got[name] = fmt.Sprintf("%d rows %s", df.Height(), hex.EncodeToString(sum[:8]))
		}
		names = append(names, name)
	}
	for i := range 80 {
		o := ursus.DynamicOptions{
			Every:  ursus.Every(pick("15m", "1h", "90m")),
			Closed: closed[rng.IntN(4)],
			Label:  pick("left", "right", "datapoint"),
		}
		desc := []string{"every " + o.Every.String()}
		if p := pick("", "30m", "2h", "10m"); p != "" {
			o.Period = ursus.Every(p)
			desc = append(desc, "period "+p)
		}
		if off := pick("", "", "-20m", "10m"); off != "" {
			o.Offset = ursus.Every(off)
			desc = append(desc, "offset "+off)
		}
		if rng.IntN(3) == 0 {
			o.StartBy = "datapoint"
			desc = append(desc, "from the first row")
		}
		if rng.IntN(2) == 0 {
			o.IncludeBoundaries = true
			desc = append(desc, "boundaries")
		}
		if rng.IntN(2) == 0 {
			o.GroupBy = []ursus.Expr{ursus.Col("g")}
			desc = append(desc, "by g")
		}
		desc = append(desc, fmt.Sprintf("closed %v, label %s", o.Closed, o.Label))
		run(fmt.Sprintf("dynamic %02d: %s", i, strings.Join(desc, ", ")),
			frame.GroupByDynamic(ursus.Col("t"), o).Agg(aggs...))
	}
	for i := range 40 {
		o := ursus.RollingOptions{Period: ursus.Every(pick("10m", "30m", "2h")), Closed: closed[rng.IntN(4)]}
		desc := []string{"period " + o.Period.String()}
		if off := pick("", "", "-10m", "15m", "-3h"); off != "" {
			o.Offset = ursus.Every(off)
			desc = append(desc, "offset "+off)
		}
		if rng.IntN(2) == 0 {
			o.GroupBy = []ursus.Expr{ursus.Col("g")}
			desc = append(desc, "by g")
		}
		desc = append(desc, fmt.Sprintf("closed %v", o.Closed))
		run(fmt.Sprintf("rolling %02d: %s", i, strings.Join(desc, ", ")),
			frame.Rolling(ursus.Col("t"), o).Agg(aggs...))
	}

	path := filepath.Join("testdata", "temporal", "golden.json")
	if os.Getenv("URSUS_TEMPORAL_GOLDEN") == "write" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		b, err := json.MarshalIndent(got, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %d cases to %s", len(got), path)
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]string
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	if len(want) != len(got) {
		t.Fatalf("%d cases recorded, %d run", len(want), len(got))
	}
	for _, name := range names {
		if got[name] != want[name] {
			t.Errorf("%s:\n got  %s\n want %s", name, got[name], want[name])
		}
	}
}

// TestTemporalGroupsStreamTheirInput: the out-of-core half of step 196. Over 300,000
// rows a minute apart, 6.4 MB of input:
//
//   - a dynamic group, with a key and without, answers under a limit of a fifth of
//     its input, as it does without one; buffered, it peaked at 7.5 and 9.3 MB.
//     What it holds is its windows' state, the answer, and a batch: a tenth of the
//     input refuses the keyed three-hour case, whose 10,000 windows of three
//     aggregates are that much already;
//   - a rolling group's answer is a row for every input row, 10 to 11 MB here with
//     three aggregates, and its windows' state is that size: it holds its output
//     rather than its input, peaking at about twice the answer, the state and the
//     answer built from it. It answers under 24 MB, where the buffered sink peaked
//     at 29 and 30 MB.
func TestTemporalGroupsStreamTheirInput(t *testing.T) {
	const n = 300_000
	lf := minuteSeries(n)
	in, err := lf.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	fifth := in.Batch().NBytes() / 5
	k := []ursus.Expr{ursus.Col("k")}
	aggs := []ursus.Expr{ursus.Col("v").Sum().Alias("s"), ursus.Len().Alias("n"), ursus.Col("v").Last().Alias("l")}
	for _, c := range []struct {
		name  string
		lf    *ursus.LazyFrame
		limit int64
	}{
		{"dynamic, every 1h", lf.GroupByDynamic(ursus.Col("ts"),
			ursus.DynamicOptions{Every: ursus.Every("1h")}).Agg(aggs...), fifth},
		{"dynamic, every 1h, period 3h, by k", lf.GroupByDynamic(ursus.Col("ts"),
			ursus.DynamicOptions{Every: ursus.Every("1h"), Period: ursus.Every("3h"), GroupBy: k}).Agg(aggs...), fifth},
		{"rolling, 30m", lf.Rolling(ursus.Col("ts"),
			ursus.RollingOptions{Period: ursus.Every("30m")}).Agg(aggs...), 24 << 20},
		{"rolling, 30m, by k", lf.Rolling(ursus.Col("ts"),
			ursus.RollingOptions{Period: ursus.Every("30m"), GroupBy: k}).Agg(aggs...), 24 << 20},
	} {
		want, err := c.lf.Collect(t.Context(), ursus.WithBatchSize(8192))
		if err != nil {
			t.Fatal(err)
		}
		var st ursus.MemoryStats
		got, err := c.lf.Collect(t.Context(), ursus.WithBatchSize(8192), ursus.WithMemoryLimit(c.limit),
			ursus.WithMemoryStats(&st))
		if err != nil {
			t.Fatalf("%s, under %d bytes: %v", c.name, c.limit, err)
		}
		ursustest.AssertFrameEqual(t, got, want)
		t.Logf("%s: a peak of %d bytes under %d", c.name, st.Peak, c.limit)
	}
}

// TestUnkeyedTemporalGroupsHandOutTheirWindows: step 207, v0.6.2 item O2. A
// temporal group with no key hands its windows out as they close, a segment of a
// batch's worth at a time, where it held its answer to the end. Each case answers
// as the same group under a key that is the same in every row, which still holds
// its answer:
//
//   - over 200,000 rows a minute apart, under a tenth of their bytes, which a
//     rolling group's answer is many times. What it holds is a batch's worth
//     whatever the input: the batch, a segment of as many windows, and their pairs,
//     about 700 KB at 8,192 rows a batch, more than a tenth of this input; so
//     batches of 2,048;
//   - over 3,000, at batches of 1, 7 and 64, so that segments end everywhere, among
//     windows that overlap, reach forward, and close both ends.
func TestUnkeyedTemporalGroupsHandOutTheirWindows(t *testing.T) {
	c := ursus.Col
	// Implode keeps a window's every value, an answer many times its input, so only
	// the small cases take it.
	bounded := []ursus.Expr{c("v").Sum().Alias("s"), ursus.Len().Alias("n"), c("v").Last().Alias("l")}
	aggs := bounded
	one := []ursus.Expr{c("one")}
	type group func(lf *ursus.LazyFrame, by []ursus.Expr) *ursus.LazyFrame
	cases := []struct {
		name string
		q    group
	}{
		{"rolling, 30m", func(lf *ursus.LazyFrame, by []ursus.Expr) *ursus.LazyFrame {
			return lf.Rolling(c("ts"), ursus.RollingOptions{Period: ursus.Every("30m"), GroupBy: by}).Agg(aggs...)
		}},
		{"rolling, 2h, 15m forward, closed both", func(lf *ursus.LazyFrame, by []ursus.Expr) *ursus.LazyFrame {
			return lf.Rolling(c("ts"), ursus.RollingOptions{Period: ursus.Every("2h"),
				Offset: ursus.Every("15m"), Closed: ursus.ClosedBoth, GroupBy: by}).Agg(aggs...)
		}},
		{"dynamic, every 1h, period 3h, boundaries, labelled by a row", func(lf *ursus.LazyFrame, by []ursus.Expr) *ursus.LazyFrame {
			return lf.GroupByDynamic(c("ts"), ursus.DynamicOptions{Every: ursus.Every("1h"),
				Period: ursus.Every("3h"), IncludeBoundaries: true, Label: "datapoint", GroupBy: by}).Agg(aggs...)
		}},
		{"dynamic, every 10m, offset -20m, closed none", func(lf *ursus.LazyFrame, by []ursus.Expr) *ursus.LazyFrame {
			return lf.GroupByDynamic(c("ts"), ursus.DynamicOptions{Every: ursus.Every("10m"),
				Offset: ursus.Every("-20m"), Closed: ursus.ClosedNone, GroupBy: by}).Agg(aggs...)
		}},
	}
	keyed := func(q group, lf *ursus.LazyFrame) *ursus.LazyFrame {
		return q(lf.WithColumns(ursus.Lit("x").Alias("one")), one).Drop("one")
	}

	big := minuteSeries(200_000)
	small := minuteSeries(3_000)
	for _, tc := range cases {
		// The large half measures the budget, not a race, and under the race
		// detector it made the root package's run pass its ten minutes (step 210).
		if !raceDetector {
			measureUnkeyed(t, tc.name, tc.q(big, nil), keyed(tc.q, big), big)
		}
		aggs = append(bounded, c("v").Implode().Alias("all"))
		want, err := keyed(tc.q, small).Collect(t.Context(), ursus.WithBatchSize(64))
		if err != nil {
			t.Fatal(err)
		}
		for _, bs := range []int{1, 7, 64} {
			got, err := tc.q(small, nil).Collect(t.Context(), ursus.WithBatchSize(bs))
			if err != nil {
				t.Fatalf("%s, batch %d: %v", tc.name, bs, err)
			}
			ursustest.AssertFrameEqual(t, got, want)
		}
		aggs = bounded
	}
}

// measureUnkeyed is TestUnkeyedTemporalGroupsHandOutTheirWindows's large half: lf,
// under a tenth of input's bytes at batches of 2,048, answers as keyed does, and
// peaks under that limit.
func measureUnkeyed(t *testing.T, name string, lf, keyed, input *ursus.LazyFrame) {
	t.Helper()
	in, err := input.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	limit := in.Batch().NBytes() / 10
	want, err := keyed.Collect(t.Context(), ursus.WithBatchSize(8192))
	if err != nil {
		t.Fatal(err)
	}
	var st ursus.MemoryStats
	got, err := lf.Collect(t.Context(), ursus.WithBatchSize(2048),
		ursus.WithMemoryLimit(limit), ursus.WithMemoryStats(&st))
	if err != nil {
		t.Fatalf("%s, under %d bytes: %v", name, limit, err)
	}
	ursustest.AssertFrameEqual(t, got, want)
	if st.Peak > limit {
		t.Errorf("%s: a peak of %d bytes under a limit of %d", name, st.Peak, limit)
	}
}

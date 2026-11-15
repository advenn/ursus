package ursus_test

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
)

// Step 530: a float aggregate whose answer is NaN by arithmetic answers one NaN,
// math.NaN()'s bits, or float32(math.NaN())'s in a Float32, whatever the payloads of
// the NaNs it combined and the order it combined them in. Step 523 listed the finding:
// over NaNs of several payloads Sum, Mean, Var, Std and Product answered NaN at every
// thread count, but which payload came out followed the order of the additions.
//
// Nothing expected is typed. A NaN answer is held to Go's math.NaN(), a Go oracle;
// every answer to the run recorded from the code before this step, which held the
// same bits wherever it did not answer a NaN (testdata/canonicalnan/before.json,
// written by URSUS_CANONICAL_NAN=write, a NaN of any payload recorded as N, at
// GOAMD64=v3: recordedBits); and the windows, in row order, to the same query on one
// thread in batches of 8192.

// The NaNs the generated columns hold: both signs, quiet and signalling.
var (
	canonNaNs64 = []uint64{
		0x7FF8000000000001, // math.NaN()
		0xFFF8000000000000, // what x86 arithmetic makes
		0x7FF8DEADBEEF0001,
		0xFFFC000000000005,
		0x7FF4000000000003, // signalling
		0xFFF0000000000007, // signalling, its sign set
	}
	canonNaNs32 = []uint32{0x7FC00000, 0xFFC00000, 0x7FC0BEEF, 0xFFE00003, 0x7FA00001, 0xFF800009}
	// canonFinite are halves, so a sum of a few hundred is exact in any order.
	canonFinite = []float64{-1.5, 2.5, 0.5, -2, 1, 0, math.Copysign(0, -1), 4}
	// canonHuge are numbers two of which overflow a sum, to an infinity of either
	// sign, so that arithmetic over numbers alone makes a NaN.
	canonHuge = []float64{1.5e308, -1.5e308, 1e308, -1, 2}
)

// nanData is a generated frame: a key g; a row index i; a, b and f, Float64, Float64
// and Float32 with nulls, whose rows each draw by their group's class (draw); c,
// numbers only; h, numbers whose sums overflow; and z, zeros, which do not vary.
type nanData struct {
	g, i       []int64
	a, b, c, h []float64
	f          []float32
	aok, bok   []bool
	fok        []bool
	rows, keys int
}

// genNaNs draws rows in groups of four classes, a group's class its key mod 4, so
// that every class has groups: numbers only; numbers and a NaN of the group's own
// payload; numbers and NaNs of every payload; numbers and both infinities, whose sum
// is a NaN of the arithmetic's. b's class is a's next, so a pair mixes them. The rows
// of the groups are interleaved in a random order.
func genNaNs(seed uint64, rows, groups int) *nanData {
	r := rand.New(rand.NewPCG(seed, seed^0x530))
	d := &nanData{rows: rows, keys: groups}
	draw := func(class, g int) float64 {
		x := canonFinite[r.IntN(len(canonFinite))]
		switch class {
		case 1:
			if r.IntN(4) == 0 {
				x = math.Float64frombits(canonNaNs64[g%len(canonNaNs64)])
			}
		case 2:
			if r.IntN(3) == 0 {
				x = math.Float64frombits(canonNaNs64[r.IntN(len(canonNaNs64))])
			}
		case 3:
			switch r.IntN(6) {
			case 0:
				x = math.Inf(1)
			case 1:
				x = math.Inf(-1)
			}
		}
		return x
	}
	draw32 := func(class, g int) float32 {
		x := float32(canonFinite[r.IntN(len(canonFinite))])
		switch class {
		case 1:
			if r.IntN(4) == 0 {
				x = math.Float32frombits(canonNaNs32[g%len(canonNaNs32)])
			}
		case 2:
			if r.IntN(3) == 0 {
				x = math.Float32frombits(canonNaNs32[r.IntN(len(canonNaNs32))])
			}
		case 3:
			switch r.IntN(6) {
			case 0:
				x = float32(math.Inf(1))
			case 1:
				x = float32(math.Inf(-1))
			}
		}
		return x
	}
	for row := range rows {
		g := r.IntN(groups)
		d.g = append(d.g, int64(g))
		d.i = append(d.i, int64(row))
		d.a = append(d.a, draw(g%4, g))
		d.b = append(d.b, draw((g+1)%4, g))
		d.f = append(d.f, draw32(g%4, g))
		d.c = append(d.c, canonFinite[r.IntN(len(canonFinite))])
		d.h = append(d.h, canonHuge[r.IntN(len(canonHuge))])
		d.aok = append(d.aok, r.IntN(8) != 0)
		d.bok = append(d.bok, r.IntN(8) != 0)
		d.fok = append(d.fok, r.IntN(8) != 0)
	}
	return d
}

func (d *nanData) frame() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.Values("g", d.g),
		ursus.Values("i", d.i),
		ursus.ValuesNullable("a", d.a, d.aok),
		ursus.ValuesNullable("b", d.b, d.bok),
		ursus.ValuesNullable("f", d.f, d.fok),
		ursus.Values("c", d.c),
		ursus.Values("h", d.h),
		ursus.Values("z", make([]float64, d.rows)),
	)
}

// nanAggs are the aggregates by arithmetic, over a, f, c and the pairs: each answer
// that is NaN must be the one NaN.
func nanAggs() []ursus.Expr {
	c := ursus.Col
	var out []ursus.Expr
	for _, v := range []string{"a", "f", "c"} {
		out = append(out,
			c(v).Sum().Alias("sum "+v),
			c(v).Mean().Alias("mean "+v),
			c(v).Var(1).Alias("var "+v),
			c(v).Std(0).Alias("std "+v),
			c(v).Skew(false).Alias("skew "+v),
			c(v).Kurtosis(true, true).Alias("kurtosis "+v),
			c(v).Product().Alias("product "+v),
			c(v).Entropy(2, true).Alias("entropy "+v),
			c(v).Implode().List().Sum().Alias("list sum "+v),
			c(v).Implode().List().Mean().Alias("list mean "+v),
			c(v).Implode().List().Std(1).Alias("list std "+v),
		)
	}
	out = append(out,
		c("h").Sum().Alias("sum h"),
		c("h").Mean().Alias("mean h"),
		c("h").Var(1).Alias("var h"),
		ursus.Corr(c("a"), c("b")).Alias("corr a b"),
		ursus.Cov(c("a"), c("b"), 1).Alias("cov a b"),
		ursus.CorrSpearman(c("a"), c("b")).Alias("spearman a b"),
		ursus.Corr(c("a"), c("c")).Alias("corr a c"),
		ursus.Corr(c("c"), c("z")).Alias("corr c z"),
		ursus.CorrSpearman(c("c"), c("z")).Alias("spearman c z"),
		c("a").Dot(c("b")).Alias("dot a b"),
	)
	return out
}

// nanWindows are the functions by arithmetic over a window of rows: the cumulative,
// rolling and exponentially weighted ones, in a partition and over the frame, and
// the horizontal reductions.
func nanWindows() []ursus.Expr {
	c, g := ursus.Col, ursus.Col("g")
	var out []ursus.Expr
	for _, v := range []string{"a", "f"} {
		x := c(v)
		out = append(out,
			x.CumSum(false).Alias("cum_sum "+v),
			x.CumSum(true).Alias("cum_sum rev "+v),
			x.CumProd(false).Alias("cum_prod "+v),
			x.CumSum(false).Over(g).Alias("cum_sum over "+v),
			x.RollingSum(3).Alias("rolling_sum "+v),
			x.RollingMean(3, ursus.MinSamples(1)).Alias("rolling_mean "+v),
			x.RollingVar(3).Alias("rolling_var "+v),
			x.RollingStd(4, ursus.RollingCenter()).Alias("rolling_std "+v),
			x.RollingSkew(4, false).Alias("rolling_skew "+v),
			x.RollingKurtosis(4, true, false).Alias("rolling_kurtosis "+v),
			x.RollingSum(3).Over(g).Alias("rolling_sum over "+v),
			x.RollingSumBy(c("i"), ursus.Every("3i")).Alias("rolling_sum_by "+v),
			x.RollingVarBy(c("i"), ursus.Every("4i")).Alias("rolling_var_by "+v),
			x.EwmMean(ursus.EwmAlpha(0.5)).Alias("ewm_mean "+v),
			x.EwmStd(ursus.EwmAlpha(0.3)).Alias("ewm_std "+v),
			x.EwmSum(ursus.EwmAlpha(0.5)).Over(g).Alias("ewm_sum over "+v),
			x.EwmVar(ursus.EwmAlpha(0.3)).Over(g).Alias("ewm_var over "+v),
			x.EwmMeanBy(c("i"), ursus.Every("2i")).Alias("ewm_mean_by "+v),
			x.Sum().Over(g).Alias("sum over "+v),
			x.Var(1).Over(g).Alias("var over "+v),
		)
	}
	// Over h, a window of numbers alone sums to a NaN where its sums overflow both
	// ways: streamed, in a partition, and by the index.
	h := c("h")
	out = append(out,
		h.RollingSum(4).Alias("rolling_sum h"),
		h.RollingMean(4).Over(g).Alias("rolling_mean over h"),
		h.RollingVar(3).Alias("rolling_var h"),
		h.RollingSkew(4, true).Alias("rolling_skew h"),
		h.RollingSumBy(c("i"), ursus.Every("4i")).Alias("rolling_sum_by h"),
		h.EwmMean(ursus.EwmAlpha(0.5)).Alias("ewm_mean h"),
		ursus.RollingCov(h, c("c"), 3).Alias("rolling_cov h c"),
		ursus.RollingCov(c("a"), c("b"), 3).Alias("rolling_cov a b"),
		ursus.RollingCorr(c("a"), c("b"), 4).Alias("rolling_corr a b"),
		ursus.SumHorizontal(c("a"), c("b")).Alias("sum_horizontal a b"),
		ursus.MeanHorizontal(c("a"), c("b"), c("c")).Alias("mean_horizontal a b c"),
		ursus.SumHorizontal(c("f"), c("f")).Alias("sum_horizontal f f"),
	)
	return out
}

// nanVariants are the configurations: threads {1, 4, 8} by batch sizes {1, 3, 7,
// 1024}.
func nanVariants() []pickConfig {
	var out []pickConfig
	for _, th := range []int{1, 4, 8} {
		for _, b := range []int{1, 3, 7, 1024} {
			out = append(out, pickConfig{threads: th, batch: b})
		}
	}
	return out
}

// nanCell is a cell as recorded: a NaN of any payload N, a null null, else its bits.
func nanCell(c *ursus.Column, i int) string {
	if isNaNOf(c, i) {
		return "N"
	}
	return cellBits(c, i)
}

// isNaNOf reports whether a float cell holds a NaN.
func isNaNOf(c *ursus.Column, i int) bool {
	if !c.IsValid(i) {
		return false
	}
	switch c.DType().ID() {
	case dtype.TypeFloat64:
		return math.IsNaN(data.MustValues[float64](c)[i])
	case dtype.TypeFloat32:
		return math.IsNaN(float64(data.MustValues[float32](c)[i]))
	}
	return false
}

// theOneNaN reports a NaN cell whose bits are not the canonical NaN's: Go's
// math.NaN(), or its Float32.
func theOneNaN(t *testing.T, what string, df *ursus.DataFrame) {
	t.Helper()
	want64, want32 := math.Float64bits(math.NaN()), math.Float32bits(float32(math.NaN()))
	for j, name := range df.Columns() {
		col := df.ColumnAt(j)
		for i := range df.Height() {
			if !isNaNOf(col, i) {
				continue
			}
			ok := true
			switch col.DType().ID() {
			case dtype.TypeFloat64:
				ok = math.Float64bits(data.MustValues[float64](col)[i]) == want64
			case dtype.TypeFloat32:
				ok = math.Float32bits(data.MustValues[float32](col)[i]) == want32
			}
			if !ok {
				t.Errorf("%s: %q row %d is the NaN %s, not math.NaN()", what, name, i, cellBits(col, i))
				break
			}
		}
	}
}

// nanRecord gathers each case's cells, by name, and holds them to the recorded run.
type nanRecord struct {
	got   map[string]string
	names []string
}

func (r *nanRecord) add(what string, df *ursus.DataFrame) {
	for j, name := range df.Columns() {
		col := df.ColumnAt(j)
		cells := make([]string, df.Height())
		for i := range cells {
			cells[i] = nanCell(col, i)
		}
		key := what + " | " + name
		r.got[key] = strings.Join(cells, " ")
		r.names = append(r.names, key)
	}
}

// check writes the record under URSUS_CANONICAL_NAN=write, and else holds every case
// to the one recorded: each cell the same bits, and a NaN where a NaN was.
func (r *nanRecord) check(t *testing.T, file string) {
	t.Helper()
	path := filepath.Join("testdata", "canonicalnan", file)
	if os.Getenv("URSUS_CANONICAL_NAN") == "write" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		b, err := json.MarshalIndent(r.got, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %d cases to %s", len(r.got), path)
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
	if len(want) != len(r.got) {
		t.Fatalf("%d cases recorded, %d run", len(want), len(r.got))
	}
	for _, name := range r.names {
		if g, w := r.got[name], want[name]; g != w && (recordedBits || !sameNaNsAndNulls(g, w)) {
			t.Errorf("%s:\n got      %s\n recorded %s", name, g, w)
		}
	}
}

// sameNaNsAndNulls reports whether two recorded cases hold their NaNs and nulls in
// the same cells, whatever their numbers' bits.
func sameNaNsAndNulls(got, want string) bool {
	g, w := strings.Fields(got), strings.Fields(want)
	if len(g) != len(w) {
		return false
	}
	for i := range g {
		if (g[i] == "N") != (w[i] == "N") || (g[i] == "null") != (w[i] == "null") {
			return false
		}
	}
	return true
}

// TestAFloatAggregateThatIsNaNIsTheOneNaN: every aggregate by arithmetic, keyed and
// keyless, in every configuration, answers math.NaN()'s bits wherever it answers a
// NaN, and else the bits the code before this step answered in that configuration.
func TestAFloatAggregateThatIsNaNIsTheOneNaN(t *testing.T) {
	lf := genNaNs(530, 480, 8).frame()
	rec := &nanRecord{got: map[string]string{}}
	for _, keys := range [][]string{{"g"}, nil} {
		for _, cf := range nanVariants() {
			what := fmt.Sprintf("keys %v %v", keys, cf)
			df := collectPicks(t, pickQuery(lf, keys, false, nanAggs()), cf)
			theOneNaN(t, what, df)
			rec.add(what, df)
		}
	}
	rec.check(t, "aggregates.json")
}

// TestAWindowOfFloatsThatIsNaNIsTheOneNaN: the cumulative, rolling, exponentially
// weighted and horizontal functions answer math.NaN()'s bits wherever they answer a
// NaN, the same bits in every configuration as on one thread in batches of 8192, and
// those the code before this step answered.
func TestAWindowOfFloatsThatIsNaNIsTheOneNaN(t *testing.T) {
	lf := genNaNs(5300, 120, 4).frame()
	q := lf.Select(append([]ursus.Expr{ursus.Col("i")}, nanWindows()...)...)
	ref := collectPicks(t, q, pickReference)
	theOneNaN(t, "reference", ref)
	for _, cf := range nanVariants() {
		got := collectPicks(t, q, cf)
		samePickBits(t, cf.String(), got, ref)
	}
	rec := &nanRecord{got: map[string]string{}}
	rec.add("windows", ref)
	rec.check(t, "windows.json")
}

// TestAStreamedWindowOfFloatsThatIsNaNIsTheOneNaN: the windows that stream their one
// partition past a memory limit, a batch at a time and their state carried, answer
// math.NaN()'s bits wherever they answer a NaN, and in memory the same bits: the
// cumulative ones, the rolling ones of rows and by the index, and the exponentially
// weighted ones.
func TestAStreamedWindowOfFloatsThatIsNaNIsTheOneNaN(t *testing.T) {
	lf := genNaNs(5302, 6000, 4).frame()
	c, h := ursus.Col, ursus.Col("h")
	cols := []ursus.Expr{c("i")}
	for _, v := range []string{"a", "f"} {
		x := c(v)
		cols = append(cols,
			x.CumSum(false).Alias("cum_sum "+v),
			x.CumProd(false).Alias("cum_prod "+v),
			x.RollingSum(3).Alias("rolling_sum "+v),
			x.RollingVar(4).Alias("rolling_var "+v),
			x.RollingSkew(4, false).Alias("rolling_skew "+v),
			x.RollingSumBy(c("i"), ursus.Every("3i")).Alias("rolling_sum_by "+v),
			x.EwmMean(ursus.EwmAlpha(0.5)).Alias("ewm_mean "+v),
			x.EwmVar(ursus.EwmAlpha(0.3)).Alias("ewm_var "+v),
			x.EwmSum(ursus.EwmAlpha(0.5)).Alias("ewm_sum "+v),
		)
	}
	cols = append(cols,
		h.RollingSum(4).Alias("rolling_sum h"),
		h.RollingMean(4, ursus.RollingCenter()).Alias("rolling_mean h"),
		h.RollingSumBy(c("i"), ursus.Every("4i")).Alias("rolling_sum_by h"),
		ursus.RollingCov(h, c("c"), 3).Alias("rolling_cov h c"),
		ursus.RollingCorr(c("a"), c("b"), 4).Alias("rolling_corr a b"),
	)
	q := lf.Select(cols...)
	want, err := q.Collect(t.Context(), ursus.WithBatchSize(64))
	if err != nil {
		t.Fatal(err)
	}
	theOneNaN(t, "in memory", want)
	var st ursus.MemoryStats
	got, err := q.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithMemoryLimit(16<<10),
		ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&st))
	if err != nil {
		t.Fatal(err)
	}
	if st.Spills == 0 {
		t.Fatal("the windows answered without spilling, so nothing streamed")
	}
	theOneNaN(t, "streamed", got)
	samePickBits(t, "streamed", got, want)
}

// TestAFloatCorrelationMatrixThatIsNaNIsTheOneNaN: a frame's Corr, a matrix of
// aggregates by arithmetic, answers math.NaN()'s bits wherever it answers a NaN: a
// column holding a NaN, or one that does not vary, and else the bits it answered
// before this step.
func TestAFloatCorrelationMatrixThatIsNaNIsTheOneNaN(t *testing.T) {
	d := genNaNs(53, 200, 8)
	steady := make([]float64, d.rows)
	df, err := ursus.Frame(
		ursus.Values("a", d.a), ursus.Values("b", d.b), ursus.Values("c", d.c),
		ursus.Values("i", d.i), ursus.Values("steady", steady),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	m, err := df.Corr(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	theOneNaN(t, "corr", m)
	rec := &nanRecord{got: map[string]string{}}
	rec.add("corr", m)
	rec.check(t, "corr.json")
}

// TestANaNLiteralInArithmeticIsTheSameInEveryBatch: a NaN literal added to, taken
// from, multiplied by or divided by a column of NaNs of other payloads, on either
// side, answers the same bits in every batch size as in one of 8192 rows, with the
// optimizer and without. Arithmetic a row at a time hands a NaN's payload on, the
// one of its operands' that IEEE leaves to the machine; a literal on the left was
// the right operand of a batch of many rows and the left of a batch of one, which
// takes no side for the constant.
func TestANaNLiteralInArithmeticIsTheSameInEveryBatch(t *testing.T) {
	lf := genNaNs(5301, 300, 8).frame()
	c := ursus.Col
	var exprs []ursus.Expr
	for k, bits := range canonNaNs64 {
		n := math.Float64frombits(bits)
		n32 := math.Float32frombits(canonNaNs32[k])
		exprs = append(exprs,
			ursus.Lit(n).Add(c("a")).Alias(fmt.Sprintf("L%d+a", k)), c("a").Add(ursus.Lit(n)).Alias(fmt.Sprintf("a+L%d", k)),
			ursus.Lit(n).Sub(c("a")).Alias(fmt.Sprintf("L%d-a", k)), c("a").Sub(ursus.Lit(n)).Alias(fmt.Sprintf("a-L%d", k)),
			ursus.Lit(n).Mul(c("a")).Alias(fmt.Sprintf("L%d*a", k)), c("a").Mul(ursus.Lit(n)).Alias(fmt.Sprintf("a*L%d", k)),
			ursus.Lit(n).Div(c("a")).Alias(fmt.Sprintf("L%d/a", k)), c("a").Div(ursus.Lit(n)).Alias(fmt.Sprintf("a/L%d", k)),
			ursus.Lit(n32).Add(c("f")).Alias(fmt.Sprintf("L%d+f", k)), c("f").Mul(ursus.Lit(n32)).Alias(fmt.Sprintf("f*L%d", k)),
		)
	}
	exprs = append(exprs, c("a").Add(c("b")).Alias("a+b"), c("b").Mul(c("a")).Alias("b*a"))
	q := lf.Select(exprs...)
	ref := collectPicks(t, q, pickReference)
	for _, cf := range nanVariants() {
		got := collectPicks(t, q, cf)
		samePickBits(t, cf.String(), got, ref)
		off, err := q.Collect(t.Context(), ursus.WithThreads(cf.threads), ursus.WithBatchSize(cf.batch),
			ursus.WithOptFlags(ursus.NoOptFlags()))
		if err != nil {
			t.Fatal(err)
		}
		samePickBits(t, cf.String()+" optimizer off", off, ref)
	}
}

package ursus_test

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestADenseBuildJoinsAsTheHashTablesDo: step 204 builds a join's dense integer keys
// through a direct index, in one pass, where a partitioned build hashed them into
// tables. Every join kind, over keys with duplicates and nulls, on eight threads: once
// on keys from 0 to 600, dense, and once on the same keys 2^33 apart, past any index,
// which builds the tables. The rows' values must agree, and so must Validate's
// refusals, which name the first duplicate row.
func TestADenseBuildJoinsAsTheHashTablesDo(t *testing.T) {
	rng := rand.New(rand.NewPCG(204, 1))
	side := func(n, keys int) *ursus.LazyFrame {
		k, kok, v := make([]int64, n), make([]bool, n), make([]int64, n)
		for i := range n {
			k[i], kok[i], v[i] = int64(rng.IntN(keys)), rng.IntN(20) != 0, int64(i)
		}
		return ursus.Frame(ursus.ValuesNullable("k", k, kok), ursus.Values("v", v))
	}
	left, right, unique := side(3_000, 600), side(1_500, 600), side(400, 100_000)
	c := ursus.Col
	scaled := func(lf *ursus.LazyFrame, by int64) *ursus.LazyFrame {
		return lf.WithColumns(c("k").Mul(by).Alias("k"))
	}
	run := func(l, r *ursus.LazyFrame, by int64, opts ...ursus.JoinOption) (*ursus.DataFrame, error) {
		lf := scaled(l, by).Join(scaled(r, by), append([]ursus.JoinOption{ursus.JoinOn(c("k"))}, opts...)...)
		// The values alone: the keys differ by the scale.
		names, err := lf.CollectSchema(t.Context())
		if err != nil {
			return nil, err
		}
		var keep []ursus.Expr
		for _, n := range names.Names() {
			if n != "k" && n != "k_right" {
				keep = append(keep, c(n))
			}
		}
		return lf.Select(keep...).Collect(t.Context(), ursus.WithThreads(8), ursus.WithBatchSize(256))
	}
	for _, how := range []ursus.JoinKind{ursus.JoinInner, ursus.JoinLeft, ursus.JoinRight, ursus.JoinFull,
		ursus.JoinSemi, ursus.JoinAnti} {
		for _, r := range []struct {
			name string
			lf   *ursus.LazyFrame
		}{{"duplicated keys", right}, {"unique keys", unique}} {
			name := fmt.Sprintf("%s on %s", how, r.name)
			dense, err := run(left, r.lf, 1, ursus.JoinHow(how))
			if err != nil {
				t.Fatalf("%s, dense: %v", name, err)
			}
			sparse, err := run(left, r.lf, int64(1)<<33, ursus.JoinHow(how))
			if err != nil {
				t.Fatalf("%s, sparse: %v", name, err)
			}
			if !sameRows(t, dense, sparse) {
				t.Fatalf("%s: %d rows dense, %d through the tables, or they differ", name, dense.Height(), sparse.Height())
			}
		}
	}
	// Validate: a right side with duplicates refused, naming the same first one.
	for _, v := range []ursus.JoinValidation{ursus.ValidateOneToOne, ursus.ValidateManyToOne} {
		_, dense := run(left, right, 1, ursus.JoinValidate(v))
		_, sparse := run(left, right, int64(1)<<33, ursus.JoinValidate(v))
		if dense == nil || !errors.Is(dense, ursus.ErrValue) || sparse == nil || dense.Error() != sparse.Error() {
			t.Fatalf("%v:\ndense:  %v\nsparse: %v", v, dense, sparse)
		}
		if !strings.Contains(dense.Error(), "first duplicate at right row") {
			t.Fatalf("%v: the refusal names no row: %v", v, dense)
		}
	}
}

package data_test

import (
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// TestNBytesDoesNotDoubleCount is the test everything else in step 10 rests on.
//
// If accounting over-reports, a memory budget fires early and unpredictably —
// and every test built on top of it still passes while proving nothing, because
// "the query stayed under its limit" is trivially true when the limit was never
// approached. So the sharing cases are checked before anything uses the number.
func TestNBytesDoesNotDoubleCount(t *testing.T) {
	col := data.NewFixed("v", dtype.Int64, []int64{1, 2, 3, 4}, bitmap.View{})
	one := col.NBytes()
	if one == 0 {
		t.Fatal("a four-row Int64 column reported zero bytes")
	}

	// Rename shares the payload — "O(1): the payload is shared" — so a batch
	// holding both must not count it twice.
	sch, err := dtype.NewSchema(
		dtype.NotNull("v", dtype.Int64),
		dtype.NotNull("alias", dtype.Int64),
	)
	if err != nil {
		t.Fatal(err)
	}
	b, err := data.NewBatch(sch, []*data.Column{col, col.Rename("alias")})
	if err != nil {
		t.Fatal(err)
	}
	if got := b.NBytes(); got != one {
		t.Errorf("a renamed column counted separately: batch = %d, one column = %d",
			got, one)
	}

	// WithDType shares it too: Int64 to Datetime reinterprets, it does not convert.
	if got := col.WithDType(dtype.Datetime(dtype.Micro, "")).NBytes(); got != one {
		t.Errorf("WithDType changed the reported size: %d, want %d", got, one)
	}

	// A BufferSet is what accumulates across operators, so the same batch reaching
	// it twice — which is exactly what two sinks retaining one batch looks like —
	// must not move the total.
	var s data.BufferSet
	s.AddBatch(b)
	first := s.Total()
	s.AddBatch(b)
	if s.Total() != first {
		t.Errorf("adding the same batch twice: %d then %d", first, s.Total())
	}
	if s.Total() != one {
		t.Errorf("set total = %d, want %d", s.Total(), one)
	}

	// And two genuinely distinct payloads do add up, or the test above would pass
	// for a BufferSet that always returned the first number it saw.
	other := data.NewFixed("w", dtype.Int64, []int64{9, 8, 7, 6}, bitmap.View{})
	s.AddColumn(other)
	if s.Total() <= one {
		t.Errorf("a second distinct payload did not add: %d, want more than %d",
			s.Total(), one)
	}
}

// TestNBytesCountsEveryPayloadSlot: a String column holds two buffers and a
// nullable column a third, and none of them is free.
func TestNBytesCountsEveryPayloadSlot(t *testing.T) {
	plain := data.NewString("s", []string{"alpha", "beta", "gamma"}, bitmap.View{})
	if plain.NBytes() == 0 {
		t.Fatal("a String column reported zero bytes")
	}

	// Built, so materialised: bitmap.Zeros has no storage since step 529.
	vb := bitmap.NewBuilder(3)
	vb.AppendMany(false, 3)
	valid := vb.Finish()
	nullable := data.NewString("s", []string{"alpha", "beta", "gamma"}, valid)
	if nullable.NBytes() <= plain.NBytes() {
		t.Errorf("a materialised validity bitmap was not counted: %d vs %d",
			nullable.NBytes(), plain.NBytes())
	}

	// The all-set form has no storage at all, which is the representation of
	// "no nulls" — so it must contribute nothing rather than a phantom byte.
	if data.NewFixed("v", dtype.Int8, []int8{1}, bitmap.AllSet(1)).NBytes() !=
		data.NewFixed("v", dtype.Int8, []int8{1}, bitmap.View{}).NBytes() {
		t.Error("the all-set validity form was charged for storage it does not have")
	}

	// A payload-free null column costs nothing at all, at any length (step 529).
	//
	// "Which makes a typed null literal free" was almost but not quite what NewNull
	// did: the payload was absent, but bitmap.Zeros materialised n bits, 128 bytes
	// for a thousand rows and 128 GiB for the 2^40 a 232-byte IPC stream declares.
	// Zeros is now the all-clear form, which has no storage, as the all-set form
	// has none.
	for _, n := range []int{1, 1000, 1 << 40} {
		for _, dt := range []dtype.DataType{dtype.Null, dtype.Int64} {
			if got := data.NewNull("n", dt, n).NBytes(); got != 0 {
				t.Errorf("NewNull of %d rows of %s holds %d bytes", n, dt, got)
			}
		}
	}
	dense := data.NewFixed("n", dtype.Int64, make([]int64, 1000), bitmap.Zeros(1000))
	if got, payload := dense.NBytes(), data.NewFixed("n", dtype.Int64, make([]int64, 1000),
		bitmap.AllSet(1000)).NBytes(); got != payload {
		t.Errorf("a column of nulls is charged %d bytes, and its payload alone %d", got, payload)
	}
}

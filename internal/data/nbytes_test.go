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

	valid := bitmap.Zeros(3)
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

	// A payload-free null column costs its validity bitmap and nothing else.
	//
	// Worth pinning precisely, because "which makes a typed null literal free" is
	// almost but not quite what NewNull does: the payload really is absent, but
	// bitmap.Zeros materialises n bits. For the length-1 literal that motivated it
	// that is one byte; for a thousand rows it is 128, and a reader of that comment
	// could reasonably expect zero.
	null := data.NewNull("n", dtype.Int64, 1000)
	dense := data.NewFixed("n", dtype.Int64, make([]int64, 1000), bitmap.Zeros(1000))
	if null.NBytes() == 0 {
		t.Error("NewNull reported zero: its validity bitmap is materialised, not all-set")
	}
	if null.NBytes() >= dense.NBytes()/8 {
		t.Errorf("a payload-free column is not much cheaper than a real one: %d vs %d",
			null.NBytes(), dense.NBytes())
	}
	if got := data.NewNull("n", dtype.Int64, 1).NBytes(); got > 64 {
		t.Errorf("a length-1 typed null literal costs %d bytes", got)
	}
}

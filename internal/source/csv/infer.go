package csv

import (
	"strconv"

	"ursus/dtype"
	"ursus/internal/uerr"
)

// inferred is one column's running type during inference. It is ordered as a
// lattice: a column only ever widens, never narrows.
//
//	unknown  →  bool     ─┐
//	         →  int64  →  float64  ─┴→  string
//
// String is the top: it can hold anything, so a column that ever fails to parse
// as something narrower ends there and the file still reads. That is the property
// that matters — inference should never make a file unreadable, only less useful.
type inferred uint8

const (
	infUnknown inferred = iota
	infBool
	infInt
	infFloat
	infString
)

func (i inferred) dtype() dtype.DataType {
	switch i {
	case infBool:
		return dtype.Bool
	case infInt:
		return dtype.Int64
	case infFloat:
		return dtype.Float64
	default:
		// infUnknown means every sampled value was null. String is the safe answer:
		// an all-null column read as String accepts whatever appears later, whereas
		// guessing Int64 would fail on the first non-null value past the sample.
		return dtype.String
	}
}

// widen merges an observation into the running type.
func (i inferred) widen(o inferred) inferred {
	if i == infUnknown {
		return o
	}
	if o == infUnknown || i == o {
		return i
	}
	// int and float unify as float; every other disagreement goes to string.
	// bool + int is string, not int: a column of true/false/1 is not a number.
	if (i == infInt && o == infFloat) || (i == infFloat && o == infInt) {
		return infFloat
	}
	return infString
}

// classify decides what a single field looks like.
func classify(f []byte, isNull func([]byte) bool) inferred {
	if len(f) == 0 || isNull(f) {
		return infUnknown
	}
	if _, err := parseBool(f); err == nil {
		return infBool
	}
	if _, err := strconv.ParseInt(str(f), 10, 64); err == nil {
		return infInt
	}
	if _, err := strconv.ParseFloat(str(f), 64); err == nil {
		// ParseFloat accepts "inf" and "nan", which in a text column are far more
		// likely to be words than numbers. Require a digit somewhere.
		if hasDigit(f) {
			return infFloat
		}
	}
	return infString
}

func hasDigit(f []byte) bool {
	for _, c := range f {
		if c >= '0' && c <= '9' {
			return true
		}
	}
	return false
}

// inferSchema reads up to o.InferRows records and returns the column names and
// types. It consumes from sc, so the caller must re-open the file to read data —
// which is why the source opens twice rather than trying to rewind an io.Reader
// it does not own.
func inferSchema(sc *scanner, o Options, isNull func([]byte) bool) (*dtype.Schema, error) {
	for range o.SkipRows {
		if !sc.Next() {
			break
		}
	}

	var names []string
	if o.HasHeader {
		if !sc.Next() {
			if err := sc.Err(); err != nil {
				return nil, err
			}
			return nil, uerr.New(uerr.KindValue, "scan_csv",
				"the file has no header row").
				Hint("pass WithHasHeader(false) if the data starts immediately")
		}
		names = make([]string, sc.NumFields())
		for i := range names {
			names[i] = string(sc.Field(i))
		}
	}

	// Sample the data rows.
	var types []inferred
	if len(names) > 0 {
		types = make([]inferred, len(names))
	}
	rows := 0
	for (o.InferRows <= 0 || rows < o.InferRows) && sc.Next() {
		for len(types) < sc.NumFields() {
			types = append(types, infUnknown)
		}
		for i := range sc.NumFields() {
			types[i] = types[i].widen(classify(sc.Field(i), isNull))
		}
		rows++
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(types) == 0 {
		return nil, uerr.New(uerr.KindValue, "scan_csv", "the file is empty")
	}

	if len(names) == 0 {
		names = make([]string, len(types))
		for i := range names {
			names[i] = "column_" + strconv.Itoa(i+1)
		}
	}
	names = applyNames(names, o.ColumnNames)

	fields := make([]dtype.Field, len(names))
	for i := range names {
		t := dtype.String
		if i < len(types) {
			t = types[i].dtype()
		}
		if ov, ok := o.SchemaOverrides[names[i]]; ok {
			t = ov
		}
		// Nullable regardless of the sample: nothing about the first 100 rows
		// promises the ten-millionth is not empty, and a column wrongly marked
		// non-nullable is a lie the rest of the engine would act on.
		fields[i] = dtype.Field{Name: names[i], Type: t, Nullable: true}
	}
	return dtype.NewSchema(fields...)
}

// applyNames overrides inferred names positionally.
func applyNames(names, override []string) []string {
	if len(override) == 0 {
		return names
	}
	out := append([]string(nil), names...)
	for i := range out {
		if i < len(override) {
			out[i] = override[i]
		}
	}
	return out
}

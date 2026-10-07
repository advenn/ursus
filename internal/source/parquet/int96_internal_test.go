package parquet

import (
	"encoding/binary"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/parquet"

	"github.com/advenn/ursus/internal/uerr"
)

// int96Of builds an INT96: nanos into the day, then the Julian day.
func int96Of(julianDay uint32, nanos uint64) parquet.Int96 {
	var v parquet.Int96
	binary.LittleEndian.PutUint64(v[:8], nanos)
	binary.LittleEndian.PutUint32(v[8:], julianDay)
	return v
}

// TestInt96Nanos: the instants that fit Datetime(ns) come back exactly, and the
// ones that do not are refused as value errors, not wrapped.
func TestInt96Nanos(t *testing.T) {
	at := func(v parquet.Int96) (n int64, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = uerr.FromPanic(r, "test")
			}
		}()
		return int96Nanos(v), nil
	}
	fits := []struct {
		name string
		v    parquet.Int96
		want time.Time
	}{
		{"the epoch", int96Of(julianUnixEpoch, 0), time.Unix(0, 0).UTC()},
		{"a nanosecond before it", int96Of(julianUnixEpoch-1, 86_399_999_999_999), time.Unix(0, -1).UTC()},
		{"2024-01-02 03:04:05.123456789", int96Of(julianUnixEpoch+19724, (3*3600+4*60+5)*1e9+123456789),
			time.Date(2024, 1, 2, 3, 4, 5, 123456789, time.UTC)},
		{"1900-01-01", int96Of(2_415_021, 0), time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)},
		// The earliest instant Datetime(ns) holds, on 1677-09-21: its day's START is
		// below MinInt64, and day*perDay wrapped (step 119).
		{"the earliest instant", int96Of(2_333_836, 763_145_224_192), time.Unix(0, math.MinInt64).UTC()},
		{"the latest instant", int96Of(2_440_588+106_751, 85_636_854_775_807), time.Unix(0, math.MaxInt64).UTC()},
	}
	for _, tc := range fits {
		got, err := at(tc.v)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got != tc.want.UnixNano() {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want.UnixNano())
		}
	}
	refused := []struct {
		name string
		v    parquet.Int96
	}{
		{"more nanoseconds than a day", int96Of(julianUnixEpoch, 86_400_000_000_000)},
		{"the year 2263", int96Of(julianUnixEpoch+107_000, 0)},
		{"the year 1600", int96Of(2_305_448, 0)},
		// The same day, a nanosecond before the earliest instant. It came back as
		// a value in 2262.
		{"a nanosecond before the earliest", int96Of(2_333_836, 763_145_224_191)},
		{"the earliest day's midnight", int96Of(2_333_836, 0)},
		{"a nanosecond past the latest", int96Of(2_440_588+106_751, 85_636_854_775_808)},
	}
	for _, tc := range refused {
		_, err := at(tc.v)
		if !errors.Is(err, uerr.ErrValue) {
			t.Errorf("%s: want a value error, got %v", tc.name, err)
		}
	}
}

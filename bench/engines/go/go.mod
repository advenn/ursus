module ursusbench

go 1.27

require (
	github.com/advenn/ursus v0.0.0
	github.com/apache/arrow-go/v18 v18.9.0
	github.com/chdb-io/chdb-go v1.12.0
	github.com/duckdb/duckdb-go/v2 v2.10505.0
	github.com/go-gota/gota v0.12.0
	github.com/tobgu/qframe v0.4.0
)

require (
	github.com/andybalholm/brotli v1.2.8 // indirect
	github.com/apache/thrift v0.25.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/duckdb/duckdb-go-bindings v0.10505.0 // indirect
	github.com/duckdb/duckdb-go-bindings/lib/darwin-amd64 v0.10505.0 // indirect
	github.com/duckdb/duckdb-go-bindings/lib/darwin-arm64 v0.10505.0 // indirect
	github.com/duckdb/duckdb-go-bindings/lib/linux-amd64 v0.10505.0 // indirect
	github.com/duckdb/duckdb-go-bindings/lib/linux-arm64 v0.10505.0 // indirect
	github.com/duckdb/duckdb-go-bindings/lib/windows-amd64 v0.10505.0 // indirect
	github.com/ebitengine/purego v0.8.2 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/goccy/go-json v0.12.0 // indirect
	github.com/google/flatbuffers v25.12.19+incompatible // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/klauspost/compress v1.20.3 // indirect
	github.com/klauspost/cpuid/v2 v2.4.2 // indirect
	github.com/mauricelam/genny v0.0.0-20190320071652-0800202903e5 // indirect
	github.com/pierrec/lz4/v4 v4.1.33 // indirect
	github.com/zeebo/xxh3 v1.1.0 // indirect
	golang.org/x/exp v0.0.0-20260112195511-716be5621a96 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	gonum.org/v1/gonum v0.17.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260817212433-ac3dfec99bb1 // indirect
	google.golang.org/grpc v1.85.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

// Benchmark the WORKING TREE, not a published version. Without this, `go test`
// here would resolve github.com/advenn/ursus from the proxy and quietly measure
// whatever was last tagged — the same class of mistake as timing a stale
// bin/runner, and just as invisible in the numbers.
//
// The runners still use nothing but the public API: internal/... is not
// importable from another module, replace or no replace.
replace github.com/advenn/ursus => ../../..

// qcheck, ursus's random queries (step 512): a module of its own, so the root's ./...
// and go.mod gain nothing. Its path is under ursus's, not ursusqcheck as the plan
// named it, because its runs go through internal/fuzzcheck, and Go lets a package
// import an internal one only from under the internal directory's parent, by import
// path (golang.org/issue/23970). Step 513 adds duckdb-go, behind a build tag.
module github.com/advenn/ursus/qcheck

go 1.27

require (
	github.com/advenn/ursus v0.0.0
	github.com/apache/arrow-go/v18 v18.7.0
)

require (
	github.com/andybalholm/brotli v1.2.2 // indirect
	github.com/apache/thrift v0.24.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/goccy/go-json v0.10.6 // indirect
	github.com/google/flatbuffers v25.12.19+incompatible // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/klauspost/compress v1.19.0 // indirect
	github.com/klauspost/cpuid/v2 v2.4.0 // indirect
	github.com/pierrec/lz4/v4 v4.1.27 // indirect
	github.com/zeebo/xxh3 v1.1.0 // indirect
	golang.org/x/exp v0.0.0-20260112195511-716be5621a96 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.37.0 // indirect
)

// Check the WORKING TREE, not a published version, as bench/engines/go does.
replace github.com/advenn/ursus => ..

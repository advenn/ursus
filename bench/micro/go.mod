module ursusmicro

go 1.27

require github.com/advenn/ursus v0.0.0

require (
	github.com/andybalholm/brotli v1.2.2 // indirect
	github.com/apache/arrow-go/v18 v18.7.0 // indirect
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
)

// Same replace as engines/go, for the same reason: measure the working tree
// rather than whatever version the proxy would hand back.
replace github.com/advenn/ursus => ../..

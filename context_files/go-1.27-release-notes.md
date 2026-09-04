# Go 1.27 — Release Notes (context reference)

Source: <https://go.dev/doc/go1.27> · Released **August 2026**, six months after Go 1.26.
This project targets `go 1.27` (see `go.mod`) primarily for **generic methods**.

Go 1 compatibility is maintained; almost all Go programs compile and run as before.

---

## 1. Language changes

### 1.1 Generic methods (the reason this project is on 1.27) — issue #77273

A **method declaration may now declare its own type parameters**. Previously, generic
functions had to live at package scope; now they can live in the namespace of a type.

```go
type Rand struct{ /* ... */ }

// A method with its own type parameter list.
func (r *Rand) N[Int intType](n Int) Int { /* ... */ }
```

`math/rand/v2` is the standard-library exemplar: `(*Rand).N[Int intType](Int) Int` now
exists as a method alongside the pre-existing package-level `N[Int intType](Int) Int`.

**Limitations — important:**

- **Interface methods may not declare type parameters.**
- **A generic method cannot implement an interface method.**

Practical consequence for design: generic methods are for concrete types. If a type needs
to satisfy an interface, that interface's methods must remain non-generic — put the type
parameters on the concrete method and keep a non-generic adapter method for the interface.

### 1.2 Struct literal field selectors — issue #9859

A key in a struct literal may now be **any valid field selector** for the struct type, not
just a top-level field name. This covers promoted/embedded fields:

```go
type Inner struct{ X int }
type Outer struct{ Inner }

o := Outer{Inner.X: 1} // now valid
```

### 1.3 Generalized function type inference — issue #77245

Function type inference now applies in **all contexts where a generic function is assigned
to a variable of, or converted to, a matching function type** — not only at call sites.

```go
func Map[T, U any](s []T, f func(T) U) []U

var f func([]int, func(int) string) []string = Map // inference now works here
```

---

## 2. Tools

### go command

- **`bzr` support removed.** Modules hosted on Bazaar servers can no longer be fetched.
- **Removed `GODEBUG` settings are now recognized** (e.g. `asynctimerchan`). In `go.mod`
  `godebug` lines and `//go:debug` comments, the setting is accepted **if set to the final
  default value** established before removal; setting it to an old value is now an error.
- **Response files (`@file`)** are supported by `compile`, `link`, `asm`, `cgo`, `cover`,
  and `pack`. Whitespace-separated args, single/double-quoted strings, escape sequences,
  backslash-newline continuation — GCC-compatible format.

### go test

- Runs the **`stdversion` vet check by default** — reports use of stdlib symbols newer than
  the Go version in force for the referring file (from the `go` directive and build tags).
- `go test -json` adds an optional **`"OutputType"`** field on `"Action":"output"` lines:
  `"error"`, `"error-continue"`, `"frame"`. See `go doc cmd/test2json`.

### go doc

- Supports **`package@version`**: `go doc example.com/pkg@v1.2.3`.
- New **`-ex`** flag lists executable examples of a package or symbol.
- Passing an example name (`go doc bytes.ExampleBuffer`) prints the example source with comments.

### go fix

- New modernizers: **`atomictypes`**, **`embedlit`**, **`slicesbackward`**, **`unsafefuncs`**.
- **Removed:** `fmtappendf` (stylistic concerns).
- **Renamed:** `waitgroup` → **`waitgroupgo`**.

### go mod tidy

For modules declaring `go 1.27` or later, `go mod tidy` **merges duplicate require blocks**
and enforces at most **two** blocks: one direct, one indirect. Comment blocks attached to
dependencies are preserved; a comment block spanning mixed direct/indirect directives is
attached to the new direct block.

### go tool trace

`-http` with only a port (`-http=:6060`) now **binds localhost only**, matching
`go tool pprof`. To listen on all interfaces, pass the address: `-http=0.0.0.0:6060`.

### Compiler

- **`//line` / `/*line*/` directives:** a relative filename is now resolved against the
  directory of the file containing the directive, matching `go/scanner` (#70478). Absolute
  filenames are unaffected.
- **Simpler function-literal (closure) names.** The name no longer depends on whether the
  containing function was inlined, and identical function literals may be **merged to share
  one body** in the binary.
  - Tests that assert on symbol names may need updating (don't depend on these names).
  - Code that (incorrectly) compares **function code pointers for equality** is more exposed:
    closures with different captured data can now have equal code pointers more often.

### Linker (macOS targets)

- **`-macos`** — OS version written in the `LC_BUILD_VERSION` load command.
- **`-macsdk`** — SDK version written in `LC_BUILD_VERSION`.
- Defaults: oldest supported macOS (**13.0.0**) and a recent SDK (**26.2.0**).

---

## 3. Runtime

### Goroutine labels in tracebacks

For modules with a `go` directive of 1.27 or later, traceback header lines now include
`runtime/pprof` **goroutine labels**. Opt out with `GODEBUG=tracebacklabels=0` (added in
Go 1.26); this opt-out is expected to be kept indefinitely, since labels can carry
sensitive data.

### `asynctimerchan` GODEBUG removed

Removed permanently. **Channels created by package `time` are now always unbuffered
(synchronous)**, regardless of GODEBUG.

### Faster small allocations

The compiler emits calls to **size-specialized allocation routines**, cutting the cost of
small (**< 80 byte**) allocations by up to **30%**; ~1% overall on allocation-heavy programs.
Binary size grows by ~60 KB. Disable with `GOEXPERIMENT=nosizespecializedmalloc`
(expected to be removed in Go 1.28).

### Goroutine leak profile (now GA)

Profile type **`goroutineleak`**, previously a Go 1.26 experiment.

- Available via `runtime/pprof` and the `net/http/pprof` endpoint **`/debug/pprof/goroutineleak`**.
- **Definition:** a goroutine blocked on a concurrency primitive (channel, `sync.Mutex`,
  `sync.Cond`, …) that cannot possibly become unblocked.
- **Detection:** the GC finds them — if goroutine G blocks on primitive P and P is
  unreachable from any runnable goroutine (or any goroutine those could unblock), P can
  never be signalled, so G can never wake.
- **Limitation:** leaks where the primitive is reachable through globals or through locals
  of runnable goroutines may go undetected.
- `GOEXPERIMENT=goroutineleakprofile` is deleted.
- Contributed by Vlad Saioc (Uber).

---

## 4. Standard library — new packages

### `encoding/json/v2` (major revision)

Functions, all taking variadic `Options`:
`Marshal`, `MarshalWrite`, `MarshalEncode`, `Unmarshal`, `UnmarshalRead`, `UnmarshalDecode`.

**Stricter defaults than v1:** rejects invalid UTF-8 in JSON strings; rejects duplicate
names within a JSON object.

**Performance:** Marshal broadly at parity with the old implementation; **Unmarshal is
significantly faster**.

**v1 is now backed by v2.** `encoding/json` keeps its marshal/unmarshal *behavior*, but
**exact error message text may differ**. v1 gains `Options` that configure v2 to use v1
semantics, so full migration is not required. Escape hatch: `GOEXPERIMENT=nojsonv2`
restores the original v1 implementation (expected to be removed in a future release).

**Changes made while v2 was behind the GOEXPERIMENT:**

| Change | Detail |
| --- | --- |
| Removed | `format` tag option (#79071) |
| Removed | `unknown` tag option (#77271) |
| Removed | `DiscardUnknownMembers` marshal option (#77271) |
| Removed | `SkipFunc` sentinel error (#74324) |
| Renamed | `inline` tag option → **`embed`** (#79985) |
| Changed | `string` tag option behavior (#79065) |
| Changed | `MatchCaseInsensitiveNames` behavior (CL 792780) |
| Changed | `jsontext` numeric `Token` accessors now also return errors (#77666) |

Background: proposal #71497. Migration guide: `encoding/json` docs, "Migrating to v2".

### `encoding/json/jsontext`

Lower-level *syntactic* JSON processing: `Encoder`, `Decoder`, `Token`, `Value`. Maintains
a state machine guaranteeing the produced/consumed token sequence is valid JSON text.

### `crypto/mldsa`

Post-quantum **ML-DSA** signatures (FIPS 204). Related:

- `crypto/x509` supports ML-DSA private keys, public keys, and signatures.
- `crypto/tls` supports ML-DSA signatures in TLS 1.3 via new `SignatureScheme` values
  **`MLDSA44`**, **`MLDSA65`**, **`MLDSA87`**.

### `uuid`

New stdlib package to **generate and parse UUIDs**.

### `simd` (experimental — `GOEXPERIMENT=simd`)

Portable, **vector-size-agnostic** SIMD; uses hardware instructions where available.
Available on all architectures. Provides vector types of unspecified size (`Int8s`,
`Float32s`, …) and a "scalable" subset of `simd/archsimd` operations — those that are
hardware-supported or cheaply emulated across architectures and vector widths.
Proposal: #78902.

### `simd/archsimd` (experimental, continued from Go 1.26 — API **not stable**)

- 128-bit vector types: **wasm, arm64, amd64**.
- 256-bit and 512-bit: some amd64 processors.
- New in 1.27: revised amd64 API, **arm64 Neon 128-bit**, **WebAssembly 128-bit**.
- Intentionally architecture-specific and non-portable. Proposal: #73787.

---

## 5. Standard library — minor changes

| Package | Change |
| --- | --- |
| `bytes` | New **`CutLast`** — slice a `[]byte` around the *last* separator occurrence; replaces common `LastIndex` uses. |
| `strings` | New **`CutLast`** — same, for strings. |
| `compress/flate` | Compression **speed improved**; exact encoded output from `Writer` may differ from Go 1.26. Affects `archive/zip`, `compress/gzip`, `compress/zlib`, `image/png` output too. |
| `crypto` | New hash value **`MLDSAMu`** — signaling mechanism for External μ ML-DSA signing. |
| `crypto/ecdsa` | `PrivateKey.Sign` now **checks hash length** when a non-nil `SignerOpts` is given. |
| `crypto/tls` | New `QUICConfig.ClientHelloInfoConn` (the `net.Conn` for `ClientHelloInfo.Conn` during QUIC server handshakes). |
| `crypto/tls` | **`MLKEM1024`** key exchange supported; enable via `Config.CurvePreferences`. |
| `crypto/tls` | PQ hybrid key exchanges can be explicitly enabled in `Config.CurvePreferences` even with `tlsmlkem=0` / `tlssecpmlkem=0` — those GODEBUGs only ever meant to affect the default set used when `CurvePreferences` is nil. |
| `crypto/tls` | New `ConnectionState.LocalCertificate` — the chain presented to the peer. |
| `crypto/tls` | **`Config.Rand` deprecated**; for deterministic testing use `testing/cryptotest.SetGlobalRandom`. |
| `crypto/x509` | Parsing into `pkix.Name` supports a wider range of `pkix.AttributeTypeAndValue.Value` types; unknown types become `asn1.RawValue`. |
| `crypto/x509` | New `Certificate.RawSignatureAlgorithm`, `CertificateRequest.RawSignatureAlgorithm`, `RevocationList.RawSignatureAlgorithm` — DER AlgorithmIdentifier, populated even when `SignatureAlgorithm` is `UnknownSignatureAlgorithm`. |
| `crypto/x509` | `SystemCertPool` now honors **`SSL_CERT_FILE`** and **`SSL_CERT_DIR`**. On Windows/Darwin, when set, roots load from disk and the **native Go verifier** is used instead of platform verification APIs. Disable with `GODEBUG=x509sslcertoverrideplatform=0`. |
| `crypto/x509/pkix` | `RDNSequence.String` (and `Name.String`) render string-typed attribute values as strings even for **unrecognized OIDs** — previously hex-encoded DER (#33093). |
| `database/sql` | New **`ConvertAssign`** — gives drivers access to the type conversions `Rows.Scan` performs. |
| `database/sql/driver` | New **`RowsColumnScanner`** interface — drivers can scan directly into user-provided destinations. |
| `go/constant` | New **`StringLen`** — length of a string `Value` without fully constructing it. |
| `go/scanner` | New **`Scanner.End`** — end position of a token. |
| `go/token` | `File` now has a **`String`** method. |
| `go/types` | New **`Hasher`** (a `maphash.Hasher` for `Type`s, respecting `Identical`) and **`HasherIgnoreTags`** (for `IdenticalIgnoreTags`) — lets `Type`s be used in hash tables. |
| `go/types` | **`gotypesalias` GODEBUG removed permanently**; `go/types` now *always* produces an `Alias` node for alias declarations. |
| `hash/maphash` | New **`Hasher`** interface — contract between values of a type and future hash-based structures (hash tables, Bloom filters) (#70471). New **`ComparableHasher`** — convenient `Hasher` impl for comparable types where `Equal` is `==`. |
| `math/big` | New **`Int.Divide`** — quotient and remainder with rounding modes `Trunc`, `Floor`, `Round`, `Ceil`. |
| `math/rand/v2` | New **generic method `Rand.N`**, matching the top-level `N` function. (Showcase for §1.1.) |
| `net` | `UnixConn` read methods return **`io.EOF` directly** instead of wrapping it in `net.OpError`. |
| `net/http` | `Transport` and `Server` support **TLS ALPN negotiation on user-provided `net.Conn`s** — the conn must implement `ConnectionState() tls.ConnectionState`. |
| `net/http` | HTTP/2 server honors **RFC 9218 client priority signals**. Restore round-robin with `Server.DisableClientPriority = true`. |
| `net/http` | HTTP/1 `Response.Body` **auto-drains unread content on Close** (up to a conservative limit) for better connection reuse. Rarely, programs that don't benefit from reuse (e.g. `Transport.MaxIdleConns = 0`, or many distinct `Client`s) may degrade — set `Transport.DisableKeepAlives = true` to opt out; degradation usually signals misconfigured `Transport`/`Client`. |
| `net/http` | New **`Server.MaxHeaderValueCount`** — cap on accepted header values; defaults to `DefaultMaxHeaderValueCount`. |
| `net/http/httptest` | New **`NewTestServer`** — a `Server` on an in-memory fake network, suitable for `testing/synctest`. |
| `net/url` | New **`URL.Clone`** and **`Values.Clone`** (deep copies). |
| `runtime/secret` | Goroutines created while in [secret mode](https://pkg.go.dev/runtime/secret#Do) now **themselves run in secret mode**. |
| `syscall` | `Errno` is now defined on **Plan 9** and implements `error`. Plan 9 syscalls return `ErrorString`, so `Errno` is never returned there — it exists so portable code referencing `syscall.Errno` builds without build constraints. |
| `testing/synctest` | New **`Sleep`** — combines `time.Sleep` and `synctest.Wait`. |
| `unicode` | Upgraded **Unicode 15 → Unicode 17** (covers the 16.0.0 and 17.0.0 releases). |

---

## 6. Ports

### Darwin

Go 1.27 **requires macOS 13 Ventura or later** (announced in the Go 1.26 notes). Earlier
versions are no longer supported.

### linux/ppc64 (big-endian 64-bit PowerPC)

- The toolchain now generates binaries using the **ELFv2 system ABI** (requires Linux
  kernel 3.13+; RHEL7 backported it to its 3.10 kernel).
- Now supported on this port: **cgo**, **PIE**, **external linking** — each requires an
  ELFv2-compatible runtime (libc and every linked/loaded library).
- Programs not using cgo still get static binaries via internal linking by default. With
  cgo options, set `CGO_ENABLED=0` to force a static pure-Go binary.

---

## 7. GOEXPERIMENT flags in Go 1.27

| Flag | Kind | Effect |
| --- | --- | --- |
| `simd` | opt-in | Enables the `simd` and `simd/archsimd` packages. |
| `nosizespecializedmalloc` | opt-out | Disables size-specialized allocation. **Expected removal in Go 1.28.** |
| `nojsonv2` | opt-out | Restores the original v1 `encoding/json` implementation. Expected removal in a future release. |

---

## 8. Removed / deprecated — quick checklist

**GODEBUG settings removed permanently in 1.27:**
`asynctimerchan` (1.23), `tlsunsafeekm` (1.22), `tlsrsakex` (1.22), `tls3des` (1.23),
`tls10server` (1.22), `x509keypairleaf` (1.23), `gotypesalias` (1.22),
`goroutineleakprofile` (GOEXPERIMENT, now GA).

> Note: `tlskyber` (added 1.23) was removed back in Go 1.24 but undocumented at the time;
> the 1.27 notes document it retroactively.

**Removed elsewhere:** `bzr` VCS support; `go fix` `fmtappendf` analyzer.

**Renamed:** `go fix` `waitgroup` → `waitgroupgo`.

**Deprecated:** `crypto/tls.Config.Rand` → use `testing/cryptotest.SetGlobalRandom`.

---

## 9. Upgrade gotchas relevant to this project

1. **Generic methods can't satisfy interfaces.** Design around it (§1.1).
2. **`time` channels are always synchronous** — no `asynctimerchan` escape hatch left.
3. **JSON error message text may change** even though behavior is preserved — don't assert
   on `encoding/json` error strings. `GOEXPERIMENT=nojsonv2` is the temporary escape hatch.
4. **Never compare function code pointers for equality** — closure merging makes distinct
   closures share code pointers more often.
5. **`go test` runs `stdversion` vet by default** — using a 1.27 symbol in a file whose
   effective Go version is older is now a test-time failure.
6. **`go mod tidy` will rewrite `go.mod`** into exactly two require blocks.
7. **`compress/*` byte output may differ** — don't golden-test compressed bytes.
8. **`go tool trace -http=:PORT` binds localhost only** now.

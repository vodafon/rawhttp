# AGENTS.md - rawhttp Development Guide

rawhttp is a raw HTTP client library designed for security research and MITM proxies. It preserves wire-level fidelity, including header casing, ordering, and raw byte sequences. It's a core component of the prook ecosystem.

## Library Overview

- **Module**: `github.com/vodafon/rawhttp`
- **Go Version**: 1.24
- **Package Structure**: Single flat package `rawhttp`. No subpackages or `client/` directory.
- **Purpose**: High-fidelity HTTP client for prookproxy.
- **Dependencies**:
    - `github.com/andybalholm/brotli v1.2.0`
    - `github.com/vodafon/vgutils`
    - `golang.org/x/net v0.49.0`

## Build & Test Commands

```bash
cd rawhttp && go build ./...
go test ./...
go test -v ./...
go test -v -run TestParseRawdata ./...
```

## Source Files

1. **`client.go`**: `Client` struct and connection pooling. Implements `Do()`, `DoHTTP()`, `DoHTTPS()`, `DoProxy()`, `DoWithProxy()`, and `DoConn()`. Uses a two-phase timeout read loop: `Timeout` for the first byte, then `QuietTimeout` for silence detection to capture smuggled responses.
2. **`request.go`**: `Request` struct for raw HTTP requests. Includes `ParseRawdata()` to split bytes into components and `Bytes()` for serialization. Supports template variables like `||HOST||`, `||PATH||`, and `||CLEN||`.
    - Headers are stored in `[]HeaderLine` slice, preserving order and duplicates natively.
3. **`response.go`**: `Response` struct with timing metrics (`TimeToFirstByte`, `TimeToLastByte`). Handles Content-Encoding decompression (gzip, br, deflate).
    - `ParseRawdata()` uses the custom `ReadResponse()` from `read.go` internally — no `net/http` dependency.
4. **`read.go`**: Wire-level HTTP parsing from `bufio.Reader` streams. Implements `ReadRequest()` and `ReadResponse(br, req)` for MITM proxy use. `ReadResponse` accepts the originating `*Request` (may be nil) to correctly handle HEAD responses per RFC 9110 §9.3.2 — skipping body even when Content-Length is present. Includes helpers: `readLine()`, `readChunkedBody()`, `copyBytes()`. Defines sentinel errors (`ErrMalformedRequestLine`, `ErrMalformedStatusLine`, `ErrMalformedChunkLength`, `ErrMalformedContentLength`).
5. **`accessors.go`**: Getter methods for parsed Request/Response fields. Request: `Method()`, `Host()`, `Path()`, `Version()`, `Header()`, `ContentLength()`, `IsChunked()`, `Body()`. Response: `Header()`. Serialization: `Request.WriteTo()` (absolute-URI form), `Request.WriteOriginForm()` (origin form), `Response.WriteTo()`.
6. **`pool.go`**: `ConnPool` for per-host idle connections. Uses LIFO retrieval and auto-cleanup.
7. **`proxy.go`**: HTTP/HTTPS CONNECT proxy dialer. Implements `golang.org/x/net/proxy.Dialer`. Uses raw byte CONNECT handshake (no `net/http`).
8. **`testdata/`**: Contains raw request fixtures (`req1.txt`, etc.) and `client_test.go` helpers.
## Key Types

```go
type Client struct {
    TransformRequestFunc func(*Request)
    Timeout              time.Duration
    proxyURI             *url.URL
    pool                 *ConnPool
    DisableKeepAlive     bool
    QuietTimeout         time.Duration
}

type Request struct {
    Rawdata  []byte
    URL      string
    URI      *url.URL
    IP       string
    // parsed fields (unexported): httpLine, method, path, version, rawHeaders, body
    headers  []HeaderLine
}

type HeaderLine struct {
    Key, Value []byte
}

type Response struct {
    Rawdata         []byte
    TimeToFirstByte time.Duration
    TimeToLastByte  time.Duration
    // parsed fields (unexported): httpLine, statusCode, preBody, body
}

type ConnPool struct { /* per-host idle connections, LIFO, expiration */ }
```

## API Surface

```go
// Wire parsing (from bufio.Reader streams — used by MITM proxies)
func ReadRequest(br *bufio.Reader) (*Request, error)
func ReadResponse(br *bufio.Reader, req *Request) (*Response, error)  // req may be nil; HEAD-aware

// Request accessors
func (obj *Request) Method() string
func (obj *Request) Host() string
func (obj *Request) Path() string
func (obj *Request) Version() string
func (obj *Request) Header(key string) string
func (obj *Request) ContentLength() int
func (obj *Request) IsChunked() bool
func (obj *Request) Body() []byte
func (obj *Request) WantsClose() bool

// Request serialization
func (obj *Request) WriteTo(w io.Writer) (int64, error)   // absolute-URI form (for proxies)
func (obj *Request) WriteOriginForm(w io.Writer) (int64, error) // origin form (for direct)

// Response accessors
func (obj *Response) StatusCode() int
func (obj *Response) Header(key string) string
func (obj *Response) ConnectionClose() bool

// Response serialization
func (obj *Response) WriteTo(w io.Writer) (int64, error)
```
## Code Style

- **Imports**: Standard library first, then external packages, separated by a blank line.
- **Naming**: `PascalCase` for exported symbols, `camelCase` for unexported. Use `obj` as the receiver name for methods.
- **Testing**: Use the standard library `testing` package with table-driven tests. Do not use testify.
- **Error Handling**: Wrap errors with context using `fmt.Errorf("context: %w", err)`. Define sentinel errors as package variables.
- **Comments**: Use `//` single-line comments before exported functions and types.

## Completed Backlog

1. ~~**Custom Response Parser in `ParseRawdata()`**~~: ✅ Replaced `http.ReadResponse` with custom `ReadResponse()` from `read.go`. No `net/http` dependency.
2. ~~**Ordered Headers**~~: ✅ Migrated from `map[string]HeaderLine` to `[]HeaderLine`. Duplicates and ordering handled natively. Removed `key_N` suffix hack and `Pos` field.
3. ~~**Proxy Refactor**~~: ✅ Replaced `net/http` in `proxy.go` CONNECT handshake with raw byte operations.

**`net/http` is no longer used anywhere in rawhttp** — not in production code, not in proxy, not in response parsing.

## Dependencies

- `github.com/andybalholm/brotli`: Brotli decompression.
- `github.com/vodafon/vgutils`: Utility functions like `RandomHEXString`.
- `golang.org/x/net`: `proxy.Dialer` interface.

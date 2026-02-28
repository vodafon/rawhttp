package rawhttp

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"fmt"
	"testing"

	"github.com/andybalholm/brotli"
)

func TestNormalizeRequest_ConstrainsAcceptEncoding(t *testing.T) {
	req := &Request{
		Rawdata: []byte("GET / HTTP/1.1\r\nHost: example.com\r\nAccept-Encoding: gzip, deflate, br, zstd\r\n\r\n"),
	}

	req.NormalizeRequest()

	// Verify Accept-Encoding is constrained
	acceptEncoding := req.Header("accept-encoding")
	if acceptEncoding != "gzip, deflate, br" {
		t.Errorf("Expected Accept-Encoding to be 'gzip, deflate, br', got %q", acceptEncoding)
	}

	// Verify Rawdata is nil (triggers reconstruction)
	if req.Rawdata != nil {
		t.Errorf("Expected Rawdata to be nil after normalization, got %v", req.Rawdata)
	}
}

func TestNormalizeRequest_StripsProxyHeaders(t *testing.T) {
	req := &Request{
		Rawdata: []byte("GET / HTTP/1.1\r\nHost: example.com\r\nProxy-Connection: keep-alive\r\nProxy-Authorization: Basic user:pass\r\n\r\n"),
	}

	req.NormalizeRequest()

	// Verify Proxy-Connection is removed
	if req.Header("Proxy-Connection") != "" {
		t.Errorf("Expected Proxy-Connection header to be removed, but it exists")
	}

	// Verify Proxy-Authorization is removed
	if req.Header("Proxy-Authorization") != "" {
		t.Errorf("Expected Proxy-Authorization header to be removed, but it exists")
	}

	// Verify Host header still exists
	host := req.Header("host")
	if host != "example.com" {
		t.Errorf("Expected Host to be 'example.com', got %q", host)
	}
}

func TestNormalizeRequest_StripsSecWebSocket(t *testing.T) {
	req := &Request{
		Rawdata: []byte("GET / HTTP/1.1\r\nHost: example.com\r\nSec-WebSocket-Extensions: permessage-deflate\r\nUpgrade: websocket\r\n\r\n"),
	}

	req.NormalizeRequest()

	// Verify Sec-WebSocket-Extensions is removed
	if req.Header("Sec-WebSocket-Extensions") != "" {
		t.Errorf("Expected Sec-WebSocket-Extensions header to be removed, but it exists")
	}

	// Verify other headers like Upgrade are preserved
	upgrade := req.Header("upgrade")
	if upgrade != "websocket" {
		t.Errorf("Expected Upgrade to be 'websocket', got %q", upgrade)
	}
}

func TestNormalizeRequest_PassthroughClean(t *testing.T) {
	req := &Request{
		Rawdata: []byte("GET /path HTTP/1.1\r\nHost: example.com\r\nUser-Agent: test\r\n\r\n"),
	}

	req.NormalizeRequest()

	// Verify clean request is still valid after normalization
	method := req.Method()
	if method != "GET" {
		t.Errorf("Expected method to be 'GET', got %q", method)
	}

	path := req.Path()
	if path != "/path" {
		t.Errorf("Expected path to be '/path', got %q", path)
	}

	host := req.Header("host")
	if host != "example.com" {
		t.Errorf("Expected Host to be 'example.com', got %q", host)
	}

	// Verify User-Agent is preserved
	userAgent := req.Header("user-agent")
	if userAgent != "test" {
		t.Errorf("Expected User-Agent to be 'test', got %q", userAgent)
	}

	// Verify Accept-Encoding was added (not present in original)
	acceptEncoding := req.Header("accept-encoding")
	if acceptEncoding != "gzip, deflate, br" {
		t.Errorf("Expected Accept-Encoding to be added as 'gzip, deflate, br', got %q", acceptEncoding)
	}
}

func TestNormalizeRequest_WriteToAfterNormalize(t *testing.T) {
	req := &Request{
		Rawdata: []byte("GET / HTTP/1.1\r\nHost: example.com\r\nProxy-Connection: keep-alive\r\nAccept-Encoding: gzip\r\n\r\n"),
	}

	req.NormalizeRequest()

	// Serialize the normalized request
	var buf bytes.Buffer
	n, err := req.WriteTo(&buf)

	if err != nil {
		t.Errorf("WriteTo failed: %v", err)
	}

	if n == 0 {
		t.Errorf("Expected WriteTo to write bytes, got 0")
	}

	output := buf.String()

	// Verify the output is valid HTTP
	if !bytes.Contains(buf.Bytes(), []byte("GET / HTTP/1.1")) {
		t.Errorf("Expected output to contain request line, got: %s", output)
	}

	// Verify Proxy-Connection is not in output
	if bytes.Contains(buf.Bytes(), []byte("Proxy-Connection")) {
		t.Errorf("Expected Proxy-Connection to be removed from output")
	}

	// Verify normalized Accept-Encoding is in output
	if !bytes.Contains(buf.Bytes(), []byte("Accept-Encoding: gzip, deflate, br")) {
		t.Errorf("Expected normalized Accept-Encoding in output, got: %s", output)
	}

	// Verify Host header is preserved
	if !bytes.Contains(buf.Bytes(), []byte("Host: example.com")) {
		t.Errorf("Expected Host header in output")
	}
}

// --- Helper functions for NormalizeResponse tests ---

func gzipCompress(data []byte) []byte {
	var buf bytes.Buffer
	gw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	gw.Write(data)
	gw.Close()
	return buf.Bytes()
}

func brotliCompress(data []byte) []byte {
	var buf bytes.Buffer
	bw := brotli.NewWriter(&buf)
	bw.Write(data)
	bw.Close()
	return buf.Bytes()
}

func deflateCompress(data []byte) []byte {
	var buf bytes.Buffer
	fw, _ := flate.NewWriter(&buf, flate.DefaultCompression)
	fw.Write(data)
	fw.Close()
	return buf.Bytes()
}

// buildResponse constructs a raw HTTP response for testing.
// preBody should NOT include the trailing \r\n\r\n separator.
func buildResponse(statusLine string, headers map[string]string, body []byte) *Response {
	var rawBuf bytes.Buffer
	var preBodyBuf bytes.Buffer

	// Status line
	rawBuf.WriteString(statusLine)
	rawBuf.WriteString("\r\n")
	preBodyBuf.WriteString(statusLine)

	// Headers
	for k, v := range headers {
		rawBuf.WriteString(k + ": " + v + "\r\n")
		preBodyBuf.WriteString("\r\n" + k + ": " + v)
	}

	// Empty line separator
	rawBuf.WriteString("\r\n")

	// Body
	rawBuf.Write(body)

	return &Response{
		Rawdata: rawBuf.Bytes(),
	}
}

// buildResponseOrdered constructs a raw HTTP response with ordered headers.
func buildResponseOrdered(statusLine string, headers [][2]string, body []byte) *Response {
	var rawBuf bytes.Buffer

	rawBuf.WriteString(statusLine)
	rawBuf.WriteString("\r\n")

	for _, h := range headers {
		rawBuf.WriteString(h[0] + ": " + h[1] + "\r\n")
	}

	rawBuf.WriteString("\r\n")
	rawBuf.Write(body)

	return &Response{
		Rawdata: rawBuf.Bytes(),
	}
}

// --- NormalizeResponse Tests ---

func TestNormalizeResponse_Gzip(t *testing.T) {
	original := []byte("Hello, World!")
	compressed := gzipCompress(original)

	resp := buildResponseOrdered("HTTP/1.1 200 OK", [][2]string{
		{"Content-Type", "text/plain"},
		{"Content-Encoding", "gzip"},
		{"Content-Length", fmt.Sprintf("%d", len(compressed))},
	}, compressed)

	resp.NormalizeResponse()

	// Body should be decompressed
	if !bytes.Equal(resp.Body(), original) {
		t.Errorf("Expected body %q, got %q", original, resp.Body())
	}

	// Content-Encoding should be removed
	if resp.Header("Content-Encoding") != "" {
		t.Errorf("Expected Content-Encoding to be removed, got %q", resp.Header("Content-Encoding"))
	}

	// Content-Length should reflect decompressed size
	clVal := resp.Header("Content-Length")
	if clVal != fmt.Sprintf("%d", len(original)) {
		t.Errorf("Expected Content-Length %d, got %q", len(original), clVal)
	}

	// Content-Type should be preserved
	if resp.Header("Content-Type") != "text/plain" {
		t.Errorf("Expected Content-Type 'text/plain', got %q", resp.Header("Content-Type"))
	}
}

func TestNormalizeResponse_Brotli(t *testing.T) {
	original := []byte("Brotli compressed content")
	compressed := brotliCompress(original)

	resp := buildResponseOrdered("HTTP/1.1 200 OK", [][2]string{
		{"Content-Type", "text/html"},
		{"Content-Encoding", "br"},
	}, compressed)

	resp.NormalizeResponse()

	if !bytes.Equal(resp.Body(), original) {
		t.Errorf("Expected body %q, got %q", original, resp.Body())
	}

	if resp.Header("Content-Encoding") != "" {
		t.Errorf("Expected Content-Encoding to be removed")
	}

	clVal := resp.Header("Content-Length")
	if clVal != fmt.Sprintf("%d", len(original)) {
		t.Errorf("Expected Content-Length %d, got %q", len(original), clVal)
	}
}

func TestNormalizeResponse_Deflate(t *testing.T) {
	original := []byte("Deflate compressed content")
	compressed := deflateCompress(original)

	resp := buildResponseOrdered("HTTP/1.1 200 OK", [][2]string{
		{"Content-Encoding", "deflate"},
	}, compressed)

	resp.NormalizeResponse()

	if !bytes.Equal(resp.Body(), original) {
		t.Errorf("Expected body %q, got %q", original, resp.Body())
	}

	if resp.Header("Content-Encoding") != "" {
		t.Errorf("Expected Content-Encoding to be removed")
	}

	clVal := resp.Header("Content-Length")
	if clVal != fmt.Sprintf("%d", len(original)) {
		t.Errorf("Expected Content-Length %d, got %q", len(original), clVal)
	}
}

func TestNormalizeResponse_ChunkedGzip(t *testing.T) {
	original := []byte("chunked and gzipped")
	compressed := gzipCompress(original)

	// Build a chunked+gzipped response manually
	var rawBuf bytes.Buffer
	rawBuf.WriteString("HTTP/1.1 200 OK\r\n")
	rawBuf.WriteString("Transfer-Encoding: chunked\r\n")
	rawBuf.WriteString("Content-Encoding: gzip\r\n")
	rawBuf.WriteString("\r\n")
	// Write single chunk
	rawBuf.WriteString(fmt.Sprintf("%x\r\n", len(compressed)))
	rawBuf.Write(compressed)
	rawBuf.WriteString("\r\n0\r\n\r\n")

	resp := &Response{Rawdata: rawBuf.Bytes()}
	resp.NormalizeResponse()

	if !bytes.Equal(resp.Body(), original) {
		t.Errorf("Expected body %q, got %q", original, resp.Body())
	}

	// Both Transfer-Encoding and Content-Encoding should be removed
	if resp.Header("Transfer-Encoding") != "" {
		t.Errorf("Expected Transfer-Encoding to be removed")
	}
	if resp.Header("Content-Encoding") != "" {
		t.Errorf("Expected Content-Encoding to be removed")
	}

	// Content-Length should be set to decompressed size
	clVal := resp.Header("Content-Length")
	if clVal != fmt.Sprintf("%d", len(original)) {
		t.Errorf("Expected Content-Length %d, got %q", len(original), clVal)
	}
}

func TestNormalizeResponse_NoEncoding(t *testing.T) {
	body := []byte("plain text body")

	resp := buildResponseOrdered("HTTP/1.1 200 OK", [][2]string{
		{"Content-Type", "text/plain"},
		{"Content-Length", fmt.Sprintf("%d", len(body))},
	}, body)

	resp.NormalizeResponse()

	// Body should remain unchanged
	if !bytes.Equal(resp.Body(), body) {
		t.Errorf("Expected body %q, got %q", body, resp.Body())
	}

	// Content-Length should be preserved
	clVal := resp.Header("Content-Length")
	if clVal != fmt.Sprintf("%d", len(body)) {
		t.Errorf("Expected Content-Length %d, got %q", len(body), clVal)
	}

	// Content-Type should be preserved
	if resp.Header("Content-Type") != "text/plain" {
		t.Errorf("Expected Content-Type 'text/plain', got %q", resp.Header("Content-Type"))
	}
}

func TestNormalizeResponse_204NoContent(t *testing.T) {
	resp := buildResponseOrdered("HTTP/1.1 204 No Content", [][2]string{
		{"Proxy-Connection", "keep-alive"},
		{"X-Custom", "preserved"},
	}, nil)

	resp.NormalizeResponse()

	// Proxy-Connection should be stripped
	if resp.Header("Proxy-Connection") != "" {
		t.Errorf("Expected Proxy-Connection to be removed for 204")
	}

	// X-Custom should be preserved
	if resp.Header("X-Custom") != "preserved" {
		t.Errorf("Expected X-Custom to be preserved, got %q", resp.Header("X-Custom"))
	}

	// Status code should still be 204
	if resp.StatusCode() != 204 {
		t.Errorf("Expected status 204, got %d", resp.StatusCode())
	}
}

func TestNormalizeResponse_304NotModified(t *testing.T) {
	resp := buildResponseOrdered("HTTP/1.1 304 Not Modified", [][2]string{
		{"ETag", "\"abc123\""},
		{"Proxy-Authorization", "secret"},
		{"Sec-WebSocket-Extensions", "permessage-deflate"},
	}, nil)

	resp.NormalizeResponse()

	// Proxy-Authorization and Sec-WebSocket-Extensions should be stripped
	if resp.Header("Proxy-Authorization") != "" {
		t.Errorf("Expected Proxy-Authorization to be removed for 304")
	}
	if resp.Header("Sec-WebSocket-Extensions") != "" {
		t.Errorf("Expected Sec-WebSocket-Extensions to be removed for 304")
	}

	// ETag should be preserved
	if resp.Header("ETag") != "\"abc123\"" {
		t.Errorf("Expected ETag to be preserved, got %q", resp.Header("ETag"))
	}
}

func TestNormalizeResponse_InvalidGzipFallback(t *testing.T) {
	// Invalid gzip data — normalization should return without changes
	invalidGzip := []byte("this is not valid gzip data")

	resp := buildResponseOrdered("HTTP/1.1 200 OK", [][2]string{
		{"Content-Encoding", "gzip"},
		{"Content-Length", fmt.Sprintf("%d", len(invalidGzip))},
	}, invalidGzip)

	// Save original rawdata for comparison
	originalRawdata := make([]byte, len(resp.Rawdata))
	copy(originalRawdata, resp.Rawdata)

	resp.NormalizeResponse()

	// Should fallback gracefully — Rawdata should be unchanged
	if !bytes.Equal(resp.Rawdata, originalRawdata) {
		t.Errorf("Expected Rawdata to remain unchanged on invalid gzip")
	}
}

func TestNormalizeResponse_UnknownEncoding(t *testing.T) {
	body := []byte("some body")
	resp := buildResponseOrdered("HTTP/1.1 200 OK", [][2]string{
		{"Content-Encoding", "sdch"},
		{"Content-Length", fmt.Sprintf("%d", len(body))},
	}, body)

	originalRawdata := make([]byte, len(resp.Rawdata))
	copy(originalRawdata, resp.Rawdata)

	resp.NormalizeResponse()

	// Should return without changes for unknown encoding
	if !bytes.Equal(resp.Rawdata, originalRawdata) {
		t.Errorf("Expected Rawdata to remain unchanged for unknown encoding")
	}
}

func TestNormalizeResponse_EmptyBody(t *testing.T) {
	// Empty body with no encoding — should normalize cleanly
	resp := buildResponseOrdered("HTTP/1.1 200 OK", [][2]string{
		{"Content-Type", "text/plain"},
		{"Content-Length", "0"},
		{"Proxy-Connection", "keep-alive"},
	}, []byte{})

	resp.NormalizeResponse()

	// Body should be empty
	if len(resp.Body()) != 0 {
		t.Errorf("Expected empty body, got %q", resp.Body())
	}

	// Content-Length should be preserved
	clVal := resp.Header("Content-Length")
	if clVal != "0" {
		t.Errorf("Expected Content-Length '0', got %q", clVal)
	}

	// Proxy-Connection should be stripped
	if resp.Header("Proxy-Connection") != "" {
		t.Errorf("Expected Proxy-Connection to be removed")
	}

	// Content-Type should be preserved
	if resp.Header("Content-Type") != "text/plain" {
		t.Errorf("Expected Content-Type 'text/plain', got %q", resp.Header("Content-Type"))
	}
}

func TestNormalizeResponse_ProxyHeaderStripping(t *testing.T) {
	body := []byte("body")
	resp := buildResponseOrdered("HTTP/1.1 200 OK", [][2]string{
		{"Content-Type", "text/html"},
		{"Proxy-Connection", "keep-alive"},
		{"Proxy-Authorization", "Basic dXNlcjpwYXNz"},
		{"Sec-WebSocket-Extensions", "permessage-deflate"},
		{"Content-Length", fmt.Sprintf("%d", len(body))},
		{"X-Custom", "value"},
	}, body)

	resp.NormalizeResponse()

	// Proxy headers should be stripped
	if resp.Header("Proxy-Connection") != "" {
		t.Errorf("Expected Proxy-Connection to be removed")
	}
	if resp.Header("Proxy-Authorization") != "" {
		t.Errorf("Expected Proxy-Authorization to be removed")
	}
	if resp.Header("Sec-WebSocket-Extensions") != "" {
		t.Errorf("Expected Sec-WebSocket-Extensions to be removed")
	}

	// Content-Type and X-Custom should be preserved
	if resp.Header("Content-Type") != "text/html" {
		t.Errorf("Expected Content-Type 'text/html', got %q", resp.Header("Content-Type"))
	}
	if resp.Header("X-Custom") != "value" {
		t.Errorf("Expected X-Custom 'value', got %q", resp.Header("X-Custom"))
	}

	// Body unchanged
	if !bytes.Equal(resp.Body(), body) {
		t.Errorf("Expected body %q, got %q", body, resp.Body())
	}
}

func TestNormalizeResponse_WriteTo(t *testing.T) {
	original := []byte("WriteTo test body")
	compressed := gzipCompress(original)

	resp := buildResponseOrdered("HTTP/1.1 200 OK", [][2]string{
		{"Content-Type", "text/plain"},
		{"Content-Encoding", "gzip"},
		{"Proxy-Connection", "keep-alive"},
	}, compressed)

	resp.NormalizeResponse()

	var buf bytes.Buffer
	n, err := resp.WriteTo(&buf)
	if err != nil {
		t.Fatalf("WriteTo failed: %v", err)
	}
	if n == 0 {
		t.Fatal("WriteTo wrote 0 bytes")
	}

	output := buf.Bytes()

	// Should contain status line
	if !bytes.Contains(output, []byte("HTTP/1.1 200 OK")) {
		t.Errorf("Expected status line in output")
	}

	// Should NOT contain Content-Encoding
	if bytes.Contains(output, []byte("Content-Encoding")) {
		t.Errorf("Expected Content-Encoding to be removed from output")
	}

	// Should NOT contain Proxy-Connection
	if bytes.Contains(output, []byte("Proxy-Connection")) {
		t.Errorf("Expected Proxy-Connection to be removed from output")
	}

	// Should contain Content-Type
	if !bytes.Contains(output, []byte("Content-Type: text/plain")) {
		t.Errorf("Expected Content-Type in output")
	}

	// Should contain decompressed body
	if !bytes.Contains(output, original) {
		t.Errorf("Expected decompressed body in output")
	}

	// Should contain correct Content-Length
	if !bytes.Contains(output, []byte(fmt.Sprintf("Content-Length: %d", len(original)))) {
		t.Errorf("Expected Content-Length: %d in output", len(original))
	}
}

func TestNormalizeResponse_BodyAccessor(t *testing.T) {
	original := []byte("accessor test")
	compressed := gzipCompress(original)

	resp := buildResponseOrdered("HTTP/1.1 200 OK", [][2]string{
		{"Content-Encoding", "gzip"},
	}, compressed)

	resp.NormalizeResponse()

	// Body() should return decompressed content
	body := resp.Body()
	if !bytes.Equal(body, original) {
		t.Errorf("Expected Body() to return %q, got %q", original, body)
	}

	// StatusCode() should still work
	if resp.StatusCode() != 200 {
		t.Errorf("Expected StatusCode() 200, got %d", resp.StatusCode())
	}
}

func TestNormalizeResponse_MultipleEncodings(t *testing.T) {
	// Multiple content-encoding values should cause fallback (no changes)
	body := []byte("some data")
	resp := buildResponseOrdered("HTTP/1.1 200 OK", [][2]string{
		{"Content-Encoding", "gzip, br"},
	}, body)

	originalRawdata := make([]byte, len(resp.Rawdata))
	copy(originalRawdata, resp.Rawdata)

	resp.NormalizeResponse()

	// Should return without changes for multiple encodings
	if !bytes.Equal(resp.Rawdata, originalRawdata) {
		t.Errorf("Expected Rawdata to remain unchanged for multiple encodings")
	}
}

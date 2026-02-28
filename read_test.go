package rawhttp

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestReadRequest(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantMethod string
		wantPath   string
		wantVer    string
		wantBody   string
		wantErr    bool
		errIs      error
	}{
		{
			name:       "simple GET no body",
			input:      "GET /path HTTP/1.1\r\nHost: example.com\r\n\r\n",
			wantMethod: "GET",
			wantPath:   "/path",
			wantVer:    "HTTP/1.1",
			wantBody:   "",
		},
		{
			name:       "POST with Content-Length body",
			input:      "POST /api HTTP/1.1\r\nHost: example.com\r\nContent-Length: 13\r\n\r\n{\"foo\":\"bar\"}",
			wantMethod: "POST",
			wantPath:   "/api",
			wantVer:    "HTTP/1.1",
			wantBody:   "{\"foo\":\"bar\"}",
		},
		{
			name:       "CONNECT request no body",
			input:      "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n",
			wantMethod: "CONNECT",
			wantPath:   "example.com:443",
			wantVer:    "HTTP/1.1",
			wantBody:   "",
		},
		{
			name:       "header value containing colon",
			input:      "GET / HTTP/1.1\r\nHost: example.com:8080\r\nX-URL: http://foo:9090/bar\r\n\r\n",
			wantMethod: "GET",
			wantPath:   "/",
			wantVer:    "HTTP/1.1",
			wantBody:   "",
		},
		{
			name:       "request with query string",
			input:      "GET /search?q=hello&lang=en HTTP/1.1\r\nHost: example.com\r\n\r\n",
			wantMethod: "GET",
			wantPath:   "/search?q=hello&lang=en",
			wantVer:    "HTTP/1.1",
			wantBody:   "",
		},
		{
			name:    "malformed request line - missing version",
			input:   "GET /path\r\nHost: example.com\r\n\r\n",
			wantErr: true,
		},
		{
			name:    "malformed request line - single word",
			input:   "GET\r\nHost: example.com\r\n\r\n",
			wantErr: true,
		},
		{
			name:    "empty input EOF",
			input:   "",
			wantErr: true,
			errIs:   io.EOF,
		},
		{
			name:       "chunked Transfer-Encoding body",
			input:      "POST /upload HTTP/1.1\r\nHost: example.com\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n6\r\n world\r\n0\r\n\r\n",
			wantMethod: "POST",
			wantPath:   "/upload",
			wantVer:    "HTTP/1.1",
			wantBody:   "hello world",
		},
		{
			name:       "HEAD request no body",
			input:      "HEAD / HTTP/1.1\r\nHost: example.com\r\n\r\n",
			wantMethod: "HEAD",
			wantPath:   "/",
			wantVer:    "HTTP/1.1",
			wantBody:   "",
		},
		{
			name:       "OPTIONS request no body",
			input:      "OPTIONS * HTTP/1.1\r\nHost: example.com\r\n\r\n",
			wantMethod: "OPTIONS",
			wantPath:   "*",
			wantVer:    "HTTP/1.1",
			wantBody:   "",
		},
		{
			name:       "POST with zero Content-Length",
			input:      "POST /api HTTP/1.1\r\nHost: example.com\r\nContent-Length: 0\r\n\r\n",
			wantMethod: "POST",
			wantPath:   "/api",
			wantVer:    "HTTP/1.1",
			wantBody:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			br := bufio.NewReader(strings.NewReader(tt.input))
			req, err := ReadRequest(br)

			if tt.wantErr {
				if err == nil {
					t.Fatal("ReadRequest() expected error, got nil")
				}
				if tt.errIs != nil && err != tt.errIs {
					// Check wrapped errors too
					if !strings.Contains(err.Error(), tt.errIs.Error()) {
						t.Errorf("ReadRequest() error = %v, want %v", err, tt.errIs)
					}
				}
				return
			}

			if err != nil {
				t.Fatalf("ReadRequest() unexpected error: %v", err)
			}

			if string(req.method) != tt.wantMethod {
				t.Errorf("method = %q, want %q", req.method, tt.wantMethod)
			}
			if string(req.path) != tt.wantPath {
				t.Errorf("path = %q, want %q", req.path, tt.wantPath)
			}
			if string(req.version) != tt.wantVer {
				t.Errorf("version = %q, want %q", req.version, tt.wantVer)
			}
			if string(req.body) != tt.wantBody {
				t.Errorf("body = %q, want %q", req.body, tt.wantBody)
			}
			if !req.parsed {
				t.Error("parsed should be true")
			}
		})
	}
}

func TestReadRequest_MultipleHeaders(t *testing.T) {
	input := "GET / HTTP/1.1\r\nHost: example.com\r\nX-Custom: value1\r\nAccept: text/html\r\n\r\n"
	br := bufio.NewReader(strings.NewReader(input))
	req, err := ReadRequest(br)
	if err != nil {
		t.Fatalf("ReadRequest() error: %v", err)
	}

	wantHeaders := []struct {
		key       string
		wantValue string
	}{
		{"host", "example.com"},
		{"x-custom", "value1"},
		{"accept", "text/html"},
	}

	for _, wh := range wantHeaders {
		hl, ok := findHeader(req.headers, wh.key)
		if !ok {
			t.Errorf("header %q not found", wh.key)
			continue
		}
		if string(hl.Value) != wh.wantValue {
			t.Errorf("header[%q] = %q, want %q", wh.key, hl.Value, wh.wantValue)
		}
	}
}

func TestReadRequest_DuplicateHeaders(t *testing.T) {
	input := "GET / HTTP/1.1\r\nHost: example.com\r\nCookie: a=1\r\nCookie: b=2\r\n\r\n"
	br := bufio.NewReader(strings.NewReader(input))
	req, err := ReadRequest(br)
	if err != nil {
		t.Fatalf("ReadRequest() error: %v", err)
	}

	// Count Cookie headers in slice
	var cookieHeaders []HeaderLine
	for _, hl := range req.headers {
		if strings.ToLower(string(hl.Key)) == "cookie" {
			cookieHeaders = append(cookieHeaders, hl)
		}
	}

	if len(cookieHeaders) != 2 {
		t.Fatalf("expected 2 cookie headers, got %d", len(cookieHeaders))
	}
	if string(cookieHeaders[0].Value) != "a=1" {
		t.Errorf("first cookie = %q, want %q", cookieHeaders[0].Value, "a=1")
	}
	if string(cookieHeaders[1].Value) != "b=2" {
		t.Errorf("second cookie = %q, want %q", cookieHeaders[1].Value, "b=2")
	}
}

func TestReadRequest_PreservesHeaderCase(t *testing.T) {
	input := "GET / HTTP/1.1\r\nHost: example.com\r\nX-Custom-Header: value\r\nContent-Type: text/plain\r\n\r\n"
	br := bufio.NewReader(strings.NewReader(input))
	req, err := ReadRequest(br)
	if err != nil {
		t.Fatalf("ReadRequest() error: %v", err)
	}

	// findHeader returns first match; Key should preserve original case
	hl, ok := findHeader(req.headers, "x-custom-header")
	if !ok {
		t.Fatal("x-custom-header not found")
	}
	if string(hl.Key) != "X-Custom-Header" {
		t.Errorf("Key = %q, want %q", hl.Key, "X-Custom-Header")
	}

	hl, ok = findHeader(req.headers, "content-type")
	if !ok {
		t.Fatal("content-type not found")
	}
	if string(hl.Key) != "Content-Type" {
		t.Errorf("Key = %q, want %q", hl.Key, "Content-Type")
	}
}

func TestReadRequest_HeaderOrder(t *testing.T) {
	input := "GET / HTTP/1.1\r\nHost: example.com\r\nAlpha: a\r\nBeta: b\r\nGamma: g\r\n\r\n"
	br := bufio.NewReader(strings.NewReader(input))
	req, err := ReadRequest(br)
	if err != nil {
		t.Fatalf("ReadRequest() error: %v", err)
	}

	expectedOrder := []struct {
		key string
		idx int
	}{
		{"Host", 0},
		{"Alpha", 1},
		{"Beta", 2},
		{"Gamma", 3},
	}

	for _, eo := range expectedOrder {
		if eo.idx >= len(req.headers) {
			t.Errorf("header index %d out of range (len=%d)", eo.idx, len(req.headers))
			continue
		}
		if string(req.headers[eo.idx].Key) != eo.key {
			t.Errorf("headers[%d].Key = %q, want %q", eo.idx, req.headers[eo.idx].Key, eo.key)
		}
	}
}

func TestReadRequest_HeaderWithColonInValue(t *testing.T) {
	input := "GET / HTTP/1.1\r\nHost: example.com:8080\r\nX-URL: http://foo:9090/bar\r\n\r\n"
	br := bufio.NewReader(strings.NewReader(input))
	req, err := ReadRequest(br)
	if err != nil {
		t.Fatalf("ReadRequest() error: %v", err)
	}

	if req.Header("Host") != "example.com:8080" {
		t.Errorf("Host = %q, want %q", req.Header("Host"), "example.com:8080")
	}
	if req.Header("X-URL") != "http://foo:9090/bar" {
		t.Errorf("X-URL = %q, want %q", req.Header("X-URL"), "http://foo:9090/bar")
	}
}

func TestReadRequest_RawdataPreserved(t *testing.T) {
	input := "GET /path HTTP/1.1\r\nHost: example.com\r\n\r\n"
	br := bufio.NewReader(strings.NewReader(input))
	req, err := ReadRequest(br)
	if err != nil {
		t.Fatalf("ReadRequest() error: %v", err)
	}

	if !bytes.Equal(req.Rawdata, []byte(input)) {
		t.Errorf("Rawdata = %q, want %q", req.Rawdata, input)
	}
}

func TestReadRequest_RawdataWithBody(t *testing.T) {
	input := "POST /api HTTP/1.1\r\nHost: example.com\r\nContent-Length: 5\r\n\r\nhello"
	br := bufio.NewReader(strings.NewReader(input))
	req, err := ReadRequest(br)
	if err != nil {
		t.Fatalf("ReadRequest() error: %v", err)
	}

	if !bytes.Equal(req.Rawdata, []byte(input)) {
		t.Errorf("Rawdata = %q, want %q", req.Rawdata, input)
	}
}

func TestReadResponse(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		wantStatusCode int
		wantBody       string
		wantErr        bool
		errIs          error
	}{
		{
			name:           "200 OK with Content-Length body",
			input:          "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello",
			wantStatusCode: 200,
			wantBody:       "hello",
		},
		{
			name:           "204 No Content",
			input:          "HTTP/1.1 204 No Content\r\nContent-Length: 0\r\n\r\n",
			wantStatusCode: 204,
			wantBody:       "",
		},
		{
			name:           "304 Not Modified no body",
			input:          "HTTP/1.1 304 Not Modified\r\nETag: \"abc\"\r\n\r\n",
			wantStatusCode: 304,
			wantBody:       "",
		},
		{
			name:           "301 redirect with empty body",
			input:          "HTTP/1.1 301 Moved Permanently\r\nLocation: /new\r\nContent-Length: 0\r\n\r\n",
			wantStatusCode: 301,
			wantBody:       "",
		},
		{
			name:           "chunked response",
			input:          "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n6\r\n world\r\n0\r\n\r\n",
			wantStatusCode: 200,
			wantBody:       "hello world",
		},
		{
			name:           "100 Continue no body",
			input:          "HTTP/1.1 100 Continue\r\n\r\n",
			wantStatusCode: 100,
			wantBody:       "",
		},
		{
			name:    "malformed status line",
			input:   "INVALID\r\n\r\n",
			wantErr: true,
		},
		{
			name:    "empty input EOF",
			input:   "",
			wantErr: true,
			errIs:   io.EOF,
		},
		{
			name:           "200 with zero Content-Length",
			input:          "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n",
			wantStatusCode: 200,
			wantBody:       "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			br := bufio.NewReader(strings.NewReader(tt.input))
			resp, err := ReadResponse(br)

			if tt.wantErr {
				if err == nil {
					t.Fatal("ReadResponse() expected error, got nil")
				}
				if tt.errIs != nil && err != tt.errIs {
					if !strings.Contains(err.Error(), tt.errIs.Error()) {
						t.Errorf("ReadResponse() error = %v, want %v", err, tt.errIs)
					}
				}
				return
			}

			if err != nil {
				t.Fatalf("ReadResponse() unexpected error: %v", err)
			}

			if resp.statusCode != tt.wantStatusCode {
				t.Errorf("statusCode = %d, want %d", resp.statusCode, tt.wantStatusCode)
			}
			if string(resp.body) != tt.wantBody {
				t.Errorf("body = %q, want %q", resp.body, tt.wantBody)
			}
			if !resp.parsed {
				t.Error("parsed should be true")
			}
		})
	}
}

func TestReadResponse_GzipPreservedRaw(t *testing.T) {
	// Create gzip compressed body
	var gzBuf bytes.Buffer
	gzWriter := gzip.NewWriter(&gzBuf)
	gzWriter.Write([]byte("compressed content"))
	gzWriter.Close()
	gzBody := gzBuf.Bytes()

	// Build raw response
	var rawResp bytes.Buffer
	rawResp.WriteString("HTTP/1.1 200 OK\r\n")
	rawResp.WriteString("Content-Encoding: gzip\r\n")
	rawResp.WriteString(fmt.Sprintf("Content-Length: %d\r\n", len(gzBody)))
	rawResp.WriteString("\r\n")
	rawResp.Write(gzBody)

	br := bufio.NewReader(bytes.NewReader(rawResp.Bytes()))
	resp, err := ReadResponse(br)
	if err != nil {
		t.Fatalf("ReadResponse() error: %v", err)
	}

	// Body should remain compressed (raw bytes preserved)
	if !bytes.Equal(resp.body, gzBody) {
		t.Errorf("body should be raw gzip bytes, got %d bytes, want %d bytes", len(resp.body), len(gzBody))
	}

	// Rawdata should match input
	if !bytes.Equal(resp.Rawdata, rawResp.Bytes()) {
		t.Errorf("Rawdata mismatch")
	}
}

func TestReadResponse_ReadUntilEOF(t *testing.T) {
	// No Content-Length, not chunked, not a no-body status → read until EOF
	input := "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\nsome body data here"
	br := bufio.NewReader(strings.NewReader(input))
	resp, err := ReadResponse(br)
	if err != nil {
		t.Fatalf("ReadResponse() error: %v", err)
	}

	if string(resp.body) != "some body data here" {
		t.Errorf("body = %q, want %q", resp.body, "some body data here")
	}
}

func TestReadResponse_RawdataPreserved(t *testing.T) {
	input := "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello"
	br := bufio.NewReader(strings.NewReader(input))
	resp, err := ReadResponse(br)
	if err != nil {
		t.Fatalf("ReadResponse() error: %v", err)
	}

	if !bytes.Equal(resp.Rawdata, []byte(input)) {
		t.Errorf("Rawdata = %q, want %q", resp.Rawdata, input)
	}
}

func TestReadResponse_Header(t *testing.T) {
	input := "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\nX-Custom: myval\r\nContent-Length: 0\r\n\r\n"
	br := bufio.NewReader(strings.NewReader(input))
	resp, err := ReadResponse(br)
	if err != nil {
		t.Fatalf("ReadResponse() error: %v", err)
	}

	tests := []struct {
		key  string
		want string
	}{
		{"Content-Type", "text/html"},
		{"content-type", "text/html"},
		{"X-Custom", "myval"},
		{"x-custom", "myval"},
		{"Content-Length", "0"},
		{"nonexistent", ""},
	}

	for _, tt := range tests {
		got := resp.Header(tt.key)
		if got != tt.want {
			t.Errorf("Header(%q) = %q, want %q", tt.key, got, tt.want)
		}
	}
}

func TestRequestAccessors(t *testing.T) {
	input := "POST /api/v1 HTTP/1.1\r\nHost: example.com\r\nContent-Type: application/json\r\nContent-Length: 13\r\nTransfer-Encoding: identity\r\n\r\n{\"foo\":\"bar\"}"
	br := bufio.NewReader(strings.NewReader(input))
	req, err := ReadRequest(br)
	if err != nil {
		t.Fatalf("ReadRequest() error: %v", err)
	}

	if req.Method() != "POST" {
		t.Errorf("Method() = %q, want %q", req.Method(), "POST")
	}
	if req.Host() != "example.com" {
		t.Errorf("Host() = %q, want %q", req.Host(), "example.com")
	}
	if req.Path() != "/api/v1" {
		t.Errorf("Path() = %q, want %q", req.Path(), "/api/v1")
	}
	if req.Version() != "HTTP/1.1" {
		t.Errorf("Version() = %q, want %q", req.Version(), "HTTP/1.1")
	}
	if req.Header("Content-Type") != "application/json" {
		t.Errorf("Header(Content-Type) = %q, want %q", req.Header("Content-Type"), "application/json")
	}
	if req.ContentLength() != 13 {
		t.Errorf("ContentLength() = %d, want %d", req.ContentLength(), 13)
	}
	if req.IsChunked() {
		t.Error("IsChunked() = true, want false")
	}
	if string(req.Body()) != "{\"foo\":\"bar\"}" {
		t.Errorf("Body() = %q, want %q", req.Body(), "{\"foo\":\"bar\"}")
	}
}

func TestRequestAccessors_Rawdata(t *testing.T) {
	// Test accessors with Rawdata-parsed request (existing ParseRawdata path)
	rawdata := "GET /test HTTP/1.1\r\nHost: test.com\r\nContent-Length: 4\r\n\r\nbody"
	req := &Request{Rawdata: []byte(rawdata)}

	if req.Method() != "GET" {
		t.Errorf("Method() = %q, want %q", req.Method(), "GET")
	}
	if req.Host() != "test.com" {
		t.Errorf("Host() = %q, want %q", req.Host(), "test.com")
	}
	if req.Path() != "/test" {
		t.Errorf("Path() = %q, want %q", req.Path(), "/test")
	}
	if req.Version() != "HTTP/1.1" {
		t.Errorf("Version() = %q, want %q", req.Version(), "HTTP/1.1")
	}
	if req.ContentLength() != 4 {
		t.Errorf("ContentLength() = %d, want %d", req.ContentLength(), 4)
	}
	if req.IsChunked() {
		t.Error("IsChunked() = true, want false")
	}
}

func TestRequestAccessors_NoContentLength(t *testing.T) {
	rawdata := "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"
	req := &Request{Rawdata: []byte(rawdata)}

	if req.ContentLength() != -1 {
		t.Errorf("ContentLength() = %d, want -1", req.ContentLength())
	}
}

func TestRequestAccessors_IsChunked(t *testing.T) {
	rawdata := "POST / HTTP/1.1\r\nHost: example.com\r\nTransfer-Encoding: chunked\r\n\r\n"
	req := &Request{Rawdata: []byte(rawdata)}

	if !req.IsChunked() {
		t.Error("IsChunked() = false, want true")
	}
}

func TestRequestAccessors_NoHost(t *testing.T) {
	rawdata := "GET / HTTP/1.1\r\n\r\n"
	req := &Request{Rawdata: []byte(rawdata)}

	if req.Host() != "" {
		t.Errorf("Host() = %q, want empty string", req.Host())
	}
}

func TestRequestAccessors_HeaderCaseInsensitive(t *testing.T) {
	rawdata := "GET / HTTP/1.1\r\nHost: example.com\r\nX-Custom: val\r\n\r\n"
	req := &Request{Rawdata: []byte(rawdata)}

	if req.Header("x-custom") != "val" {
		t.Errorf("Header(x-custom) = %q, want %q", req.Header("x-custom"), "val")
	}
	if req.Header("X-Custom") != "val" {
		t.Errorf("Header(X-Custom) = %q, want %q", req.Header("X-Custom"), "val")
	}
	if req.Header("X-CUSTOM") != "val" {
		t.Errorf("Header(X-CUSTOM) = %q, want %q", req.Header("X-CUSTOM"), "val")
	}
}

func TestRequestAccessors_HeaderNotFound(t *testing.T) {
	rawdata := "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"
	req := &Request{Rawdata: []byte(rawdata)}

	if req.Header("X-Nonexistent") != "" {
		t.Errorf("Header(X-Nonexistent) = %q, want empty string", req.Header("X-Nonexistent"))
	}
}

func TestWriteTo_Request(t *testing.T) {
	input := "GET /path HTTP/1.1\r\nHost: example.com\r\n\r\n"
	br := bufio.NewReader(strings.NewReader(input))
	req, err := ReadRequest(br)
	if err != nil {
		t.Fatalf("ReadRequest() error: %v", err)
	}

	var buf bytes.Buffer
	n, err := req.WriteTo(&buf)
	if err != nil {
		t.Fatalf("WriteTo() error: %v", err)
	}

	if n != int64(len(input)) {
		t.Errorf("WriteTo() n = %d, want %d", n, len(input))
	}
	if !bytes.Equal(buf.Bytes(), []byte(input)) {
		t.Errorf("WriteTo() output = %q, want %q", buf.Bytes(), input)
	}
}

func TestWriteTo_RequestWithBody(t *testing.T) {
	input := "POST /api HTTP/1.1\r\nHost: example.com\r\nContent-Length: 5\r\n\r\nhello"
	br := bufio.NewReader(strings.NewReader(input))
	req, err := ReadRequest(br)
	if err != nil {
		t.Fatalf("ReadRequest() error: %v", err)
	}

	var buf bytes.Buffer
	_, err = req.WriteTo(&buf)
	if err != nil {
		t.Fatalf("WriteTo() error: %v", err)
	}

	if !bytes.Equal(buf.Bytes(), []byte(input)) {
		t.Errorf("WriteTo() output = %q, want %q", buf.Bytes(), input)
	}
}

func TestWriteTo_RequestFromBytes(t *testing.T) {
	// Test WriteTo when Rawdata is empty, falls back to Bytes()
	req := &Request{
		Rawdata: []byte("GET /fallback HTTP/1.1\r\nHost: example.com\r\n\r\n"),
	}
	req.ParseRawdata()

	// Clear Rawdata to test fallback path
	req.Rawdata = nil

	var buf bytes.Buffer
	_, err := req.WriteTo(&buf)
	if err != nil {
		t.Fatalf("WriteTo() error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "GET /fallback HTTP/1.1") {
		t.Errorf("WriteTo() fallback output missing request line, got %q", output)
	}
}

func TestWriteTo_Response(t *testing.T) {
	input := "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello"
	br := bufio.NewReader(strings.NewReader(input))
	resp, err := ReadResponse(br)
	if err != nil {
		t.Fatalf("ReadResponse() error: %v", err)
	}

	var buf bytes.Buffer
	n, err := resp.WriteTo(&buf)
	if err != nil {
		t.Fatalf("WriteTo() error: %v", err)
	}

	if n != int64(len(input)) {
		t.Errorf("WriteTo() n = %d, want %d", n, len(input))
	}
	if !bytes.Equal(buf.Bytes(), []byte(input)) {
		t.Errorf("WriteTo() output = %q, want %q", buf.Bytes(), input)
	}
}

func TestWriteTo_ResponseRoundTrip(t *testing.T) {
	input := "HTTP/1.1 404 Not Found\r\nContent-Type: text/plain\r\nContent-Length: 9\r\n\r\nnot found"
	br := bufio.NewReader(strings.NewReader(input))
	resp, err := ReadResponse(br)
	if err != nil {
		t.Fatalf("ReadResponse() error: %v", err)
	}

	var buf bytes.Buffer
	_, err = resp.WriteTo(&buf)
	if err != nil {
		t.Fatalf("WriteTo() error: %v", err)
	}

	if !bytes.Equal(buf.Bytes(), []byte(input)) {
		t.Errorf("round-trip mismatch:\ngot:  %q\nwant: %q", buf.Bytes(), input)
	}
}

func TestReadRequest_URIParsed(t *testing.T) {
	input := "GET /path?key=value HTTP/1.1\r\nHost: example.com\r\n\r\n"
	br := bufio.NewReader(strings.NewReader(input))
	req, err := ReadRequest(br)
	if err != nil {
		t.Fatalf("ReadRequest() error: %v", err)
	}

	if req.URI == nil {
		t.Fatal("URI is nil")
	}
	if req.URI.Path != "/path" {
		t.Errorf("URI.Path = %q, want %q", req.URI.Path, "/path")
	}
	if req.URI.RawQuery != "key=value" {
		t.Errorf("URI.RawQuery = %q, want %q", req.URI.RawQuery, "key=value")
	}
}

func TestReadRequest_ChunkedRawdataPreserved(t *testing.T) {
	input := "POST /upload HTTP/1.1\r\nHost: example.com\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n0\r\n\r\n"
	br := bufio.NewReader(strings.NewReader(input))
	req, err := ReadRequest(br)
	if err != nil {
		t.Fatalf("ReadRequest() error: %v", err)
	}

	// Rawdata should contain the chunked encoding framing
	if !bytes.Contains(req.Rawdata, []byte("5\r\nhello\r\n0\r\n\r\n")) {
		t.Errorf("Rawdata should contain chunked framing, got %q", req.Rawdata)
	}
}

func TestReadResponse_StatusCodeAccessor(t *testing.T) {
	input := "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n"
	br := bufio.NewReader(strings.NewReader(input))
	resp, err := ReadResponse(br)
	if err != nil {
		t.Fatalf("ReadResponse() error: %v", err)
	}

	// StatusCode() calls ParseRawdata() which is already done, should still work
	if resp.StatusCode() != 403 {
		t.Errorf("StatusCode() = %d, want 403", resp.StatusCode())
	}
}

func TestReadResponse_PreBody(t *testing.T) {
	input := "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 5\r\n\r\nhello"
	br := bufio.NewReader(strings.NewReader(input))
	resp, err := ReadResponse(br)
	if err != nil {
		t.Fatalf("ReadResponse() error: %v", err)
	}

	expectedPreBody := "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 5"
	if string(resp.preBody) != expectedPreBody {
		t.Errorf("preBody = %q, want %q", resp.preBody, expectedPreBody)
	}
}

func TestReadRequest_MalformedContentLength(t *testing.T) {
	input := "POST /api HTTP/1.1\r\nHost: example.com\r\nContent-Length: abc\r\n\r\n"
	br := bufio.NewReader(strings.NewReader(input))
	_, err := ReadRequest(br)

	if err == nil {
		t.Fatal("ReadRequest() expected error for non-numeric Content-Length, got nil")
	}
	if !errors.Is(err, ErrMalformedContentLength) {
		t.Errorf("error = %v, want ErrMalformedContentLength", err)
	}
}

func TestReadResponse_MalformedStatusCode(t *testing.T) {
	input := "HTTP/1.1 XYZ OK\r\nContent-Length: 0\r\n\r\n"
	br := bufio.NewReader(strings.NewReader(input))
	_, err := ReadResponse(br)

	if err == nil {
		t.Fatal("ReadResponse() expected error for non-numeric status code, got nil")
	}
	if !errors.Is(err, ErrMalformedStatusLine) {
		t.Errorf("error = %v, want ErrMalformedStatusLine", err)
	}
}

func TestReadRequest_HeaderWithoutColon(t *testing.T) {
	// Header line without colon should be parsed with key only, empty value
	input := "GET / HTTP/1.1\r\nHost: example.com\r\nBadHeader\r\n\r\n"
	br := bufio.NewReader(strings.NewReader(input))
	req, err := ReadRequest(br)
	if err != nil {
		t.Fatalf("ReadRequest() error: %v", err)
	}

	// Should have 2 headers: Host and BadHeader
	if len(req.headers) != 2 {
		t.Fatalf("expected 2 headers, got %d", len(req.headers))
	}

	// BadHeader should have key="BadHeader" and empty value
	if string(req.headers[1].Key) != "BadHeader" {
		t.Errorf("headers[1].Key = %q, want BadHeader", req.headers[1].Key)
	}
	if string(req.headers[1].Value) != "" {
		t.Errorf("headers[1].Value = %q, want empty", req.headers[1].Value)
	}
}

func TestReadResponse_NoBodyStatus(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "1xx informational",
			input: "HTTP/1.1 100 Continue\r\nContent-Length: 100\r\n\r\n",
		},
		{
			name:  "204 No Content",
			input: "HTTP/1.1 204 No Content\r\nContent-Length: 100\r\n\r\n",
		},
		{
			name:  "304 Not Modified",
			input: "HTTP/1.1 304 Not Modified\r\nContent-Length: 100\r\n\r\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			br := bufio.NewReader(strings.NewReader(tt.input))
			resp, err := ReadResponse(br)
			if err != nil {
				t.Fatalf("ReadResponse() error: %v", err)
			}

			if len(resp.body) != 0 {
				t.Errorf("body = %q, want empty (no-body status)", resp.body)
			}
		})
	}
}

func TestReadResponse_ContentLengthIgnoresError(t *testing.T) {
	// Response parser ignores parse errors for non-numeric content-length
	// (unlike request parser which returns ErrMalformedContentLength)
	input := "HTTP/1.1 200 OK\r\nContent-Length: abc\r\n\r\n"
	br := bufio.NewReader(strings.NewReader(input))
	resp, err := ReadResponse(br)

	// Should not error — response parser ignores the parse error
	if err != nil {
		t.Fatalf("ReadResponse() unexpected error: %v", err)
	}

	// With invalid content-length and no chunked encoding, body is read until EOF
	// Since there's nothing after headers, body should be empty
	if len(resp.body) != 0 {
		t.Errorf("body = %q, want empty", resp.body)
	}
}

func TestReadChunkedBody_MalformedChunkLength(t *testing.T) {
	// Chunked body with non-hex chunk size
	input := "POST /api HTTP/1.1\r\nHost: example.com\r\nTransfer-Encoding: chunked\r\n\r\nZZZ\r\ndata\r\n0\r\n\r\n"
	br := bufio.NewReader(strings.NewReader(input))
	_, err := ReadRequest(br)

	if err == nil {
		t.Fatal("ReadRequest() expected error for malformed chunk length, got nil")
	}
	if !strings.Contains(err.Error(), "malformed chunk length") {
		t.Errorf("error = %v, want malformed chunk length error", err)
	}
}

func TestReadChunkedBody_WithExtensions(t *testing.T) {
	// Chunked body with chunk extensions (e.g., "a;ext=val")
	input := "POST /api HTTP/1.1\r\nHost: example.com\r\nTransfer-Encoding: chunked\r\n\r\n5;ext=val\r\nhello\r\n0\r\n\r\n"
	br := bufio.NewReader(strings.NewReader(input))
	req, err := ReadRequest(br)
	if err != nil {
		t.Fatalf("ReadRequest() error: %v", err)
	}

	if string(req.body) != "hello" {
		t.Errorf("body = %q, want hello", req.body)
	}
}

func TestWriteOriginForm(t *testing.T) {
	// Parse a request with absolute URI form
	input := "GET http://example.com/path?q=1 HTTP/1.1\r\nHost: example.com\r\n\r\n"
	br := bufio.NewReader(strings.NewReader(input))
	req, err := ReadRequest(br)
	if err != nil {
		t.Fatalf("ReadRequest() error: %v", err)
	}

	var buf bytes.Buffer
	_, err = req.WriteOriginForm(&buf)
	if err != nil {
		t.Fatalf("WriteOriginForm() error: %v", err)
	}

	output := buf.String()
	// Should write origin form: "GET /path?q=1 HTTP/1.1"
	if !strings.HasPrefix(output, "GET /path?q=1 HTTP/1.1\r\n") {
		t.Errorf("WriteOriginForm() output = %q, want prefix \"GET /path?q=1 HTTP/1.1\\r\\n\"", output)
	}
	// Should NOT contain the absolute URI
	if strings.Contains(output, "http://example.com") {
		t.Errorf("WriteOriginForm() should not contain absolute URI, got %q", output)
	}
	// Should contain Host header
	if !strings.Contains(output, "Host: example.com") {
		t.Errorf("WriteOriginForm() missing Host header, got %q", output)
	}
}

func TestWriteOriginForm_NoURI(t *testing.T) {
	// When URI is nil, WriteOriginForm should fall back to path
	req := &Request{
		parsed:     true,
		method:     []byte("GET"),
		path:       []byte("/fallback"),
		version:    []byte("HTTP/1.1"),
		rawHeaders: []byte("Host: example.com"),
		body:       []byte{},
		headers:    []HeaderLine{{Key: []byte("Host"), Value: []byte("example.com")}},
	}

	var buf bytes.Buffer
	_, err := req.WriteOriginForm(&buf)
	if err != nil {
		t.Fatalf("WriteOriginForm() error: %v", err)
	}

	output := buf.String()
	if !strings.HasPrefix(output, "GET /fallback HTTP/1.1\r\n") {
		t.Errorf("WriteOriginForm() output = %q, want prefix \"GET /fallback HTTP/1.1\\r\\n\"", output)
	}
}

func TestChunkedTrailer(t *testing.T) {
	// Test reading chunked body with multiple trailer headers
	// RFC 9112 §7.1.2: trailers are header lines after the zero-size chunk,
	// terminated by an empty line
	input := "POST /upload HTTP/1.1\r\n" +
		"Host: example.com\r\n" +
		"Transfer-Encoding: chunked\r\n" +
		"\r\n" +
		"5\r\n" +
		"hello\r\n" +
		"0\r\n" +
		"X-Trailer-1: value1\r\n" +
		"X-Trailer-2: value2\r\n" +
		"\r\n"

	br := bufio.NewReader(strings.NewReader(input))
	req, err := ReadRequest(br)
	if err != nil {
		t.Fatalf("ReadRequest() error: %v", err)
	}

	// Body should only contain "hello", not trailers
	if string(req.body) != "hello" {
		t.Errorf("body = %q, want %q", req.body, "hello")
	}

	// Rawdata should preserve entire chunked encoding including trailers
	if !bytes.Contains(req.Rawdata, []byte("X-Trailer-1: value1")) {
		t.Errorf("Rawdata should contain trailer header, got %q", req.Rawdata)
	}
	if !bytes.Contains(req.Rawdata, []byte("X-Trailer-2: value2")) {
		t.Errorf("Rawdata should contain trailer header, got %q", req.Rawdata)
	}
}

func TestChunkedTrailerMultipleChunks(t *testing.T) {
	// Test reading chunked body with multiple chunks and trailers
	input := "POST /upload HTTP/1.1\r\n" +
		"Host: example.com\r\n" +
		"Transfer-Encoding: chunked\r\n" +
		"\r\n" +
		"5\r\n" +
		"hello\r\n" +
		"6\r\n" +
		" world\r\n" +
		"0\r\n" +
		"X-Custom: trailer\r\n" +
		"\r\n"

	br := bufio.NewReader(strings.NewReader(input))
	req, err := ReadRequest(br)
	if err != nil {
		t.Fatalf("ReadRequest() error: %v", err)
	}

	// Body should be concatenation of chunks
	if string(req.body) != "hello world" {
		t.Errorf("body = %q, want %q", req.body, "hello world")
	}

	// Rawdata should preserve trailer
	if !bytes.Contains(req.Rawdata, []byte("X-Custom: trailer")) {
		t.Errorf("Rawdata should contain trailer, got %q", req.Rawdata)
	}
}

package rawhttp

import (
	"bytes"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"
)

func TestParseRawdata(t *testing.T) {
	tests := []struct {
		name       string
		rawdata    string
		wantMethod string
		wantPath   string
		wantErr    bool
	}{
		{
			name:       "simple GET request",
			rawdata:    "GET /path HTTP/1.1\r\nHost: example.com\r\n\r\n",
			wantMethod: "GET",
			wantPath:   "/path",
			wantErr:    false,
		},
		{
			name:       "POST request with query",
			rawdata:    "POST /api?foo=bar HTTP/1.1\r\nHost: example.com\r\n\r\n",
			wantMethod: "POST",
			wantPath:   "/api?foo=bar",
			wantErr:    false,
		},
		{
			name:       "LF only normalized to CRLF",
			rawdata:    "GET /path HTTP/1.1\nHost: example.com\n\n",
			wantMethod: "GET",
			wantPath:   "/path",
			wantErr:    false,
		},
		{
			name:       "invalid HTTP line - missing parts",
			rawdata:    "GET /path\r\nHost: example.com\r\n\r\n",
			wantMethod: "",
			wantPath:   "",
			wantErr:    true,
		},
		{
			name:       "request with extra spaces trimmed",
			rawdata:    "GET   /path   HTTP/1.1\r\nHost: example.com\r\n\r\n",
			wantMethod: "GET",
			wantPath:   "/path",
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &Request{Rawdata: []byte(tt.rawdata)}
			err := req.ParseRawdata()

			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseRawdata() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("ParseRawdata() unexpected error: %v", err)
				return
			}

			if string(req.method) != tt.wantMethod {
				t.Errorf("method = %q, want %q", req.method, tt.wantMethod)
			}

			if string(req.path) != tt.wantPath {
				t.Errorf("path = %q, want %q", req.path, tt.wantPath)
			}
		})
	}
}

func TestParseRawdata_WithBody(t *testing.T) {
	rawdata := "POST /api HTTP/1.1\r\nHost: example.com\r\nContent-Length: 13\r\n\r\n{\"foo\":\"bar\"}"
	req := &Request{Rawdata: []byte(rawdata)}

	err := req.ParseRawdata()
	if err != nil {
		t.Fatalf("ParseRawdata() error: %v", err)
	}

	expectedBody := `{"foo":"bar"}`
	if string(req.body) != expectedBody {
		t.Errorf("body = %q, want %q", req.body, expectedBody)
	}
}

func TestParseRawdata_MultipleBodySections(t *testing.T) {
	// Body containing \r\n\r\n should be preserved
	rawdata := "POST /api HTTP/1.1\r\nHost: example.com\r\n\r\npart1\r\n\r\npart2"
	req := &Request{Rawdata: []byte(rawdata)}

	err := req.ParseRawdata()
	if err != nil {
		t.Fatalf("ParseRawdata() error: %v", err)
	}

	expectedBody := "part1\r\n\r\npart2"
	if string(req.body) != expectedBody {
		t.Errorf("body = %q, want %q", req.body, expectedBody)
	}
}

func TestParseRawdata_Headers(t *testing.T) {
	rawdata := "GET / HTTP/1.1\r\nHost: example.com\r\nX-Custom: value\r\nContent-Type: application/json\r\n\r\n"
	req := &Request{Rawdata: []byte(rawdata)}

	err := req.ParseRawdata()
	if err != nil {
		t.Fatalf("ParseRawdata() error: %v", err)
	}

	tests := []struct {
		key       string
		wantValue string
	}{
		{"host", "example.com"},
		{"x-custom", "value"},
		{"content-type", "application/json"},
	}

	for _, tt := range tests {
		hl, ok := findHeader(req.headers, tt.key)
		if !ok {
			t.Errorf("header %q not found", tt.key)
			continue
		}
		if string(hl.Value) != tt.wantValue {
			t.Errorf("header[%q] = %q, want %q", tt.key, hl.Value, tt.wantValue)
		}
	}
}

func TestParseRawdata_DuplicateHeaders(t *testing.T) {
	rawdata := "GET / HTTP/1.1\r\nHost: example.com\r\nCookie: a=1\r\nCookie: b=2\r\n\r\n"
	req := &Request{Rawdata: []byte(rawdata)}

	err := req.ParseRawdata()
	if err != nil {
		t.Fatalf("ParseRawdata() error: %v", err)
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

func TestRequest_DuplicateHostHeaders_Bytes(t *testing.T) {
	// TDD test: verify that duplicate Host headers are preserved in Bytes() output
	rawdata := "GET / HTTP/1.1\r\nHost: example1.com\r\nHost: example2.com\r\nConnection: close\r\n\r\n"
	req := &Request{Rawdata: []byte(rawdata)}

	err := req.ParseRawdata()
	if err != nil {
		t.Fatalf("ParseRawdata() error: %v", err)
	}

	output := req.Bytes()

	// Count occurrences of "Host:" in output - should be 2
	count := bytes.Count(output, []byte("Host:"))
	if count != 2 {
		t.Errorf("expected 2 Host headers in output, got %d.\nOutput:\n%s", count, output)
	}

	// Verify both specific values are present
	if !bytes.Contains(output, []byte("Host: example1.com")) {
		t.Errorf("missing 'Host: example1.com' in output.\nOutput:\n%s", output)
	}
	if !bytes.Contains(output, []byte("Host: example2.com")) {
		t.Errorf("missing 'Host: example2.com' in output.\nOutput:\n%s", output)
	}
}

func TestParseRawdata_HeaderWithColonInValue(t *testing.T) {
	rawdata := "GET / HTTP/1.1\r\nHost: example.com\r\nX-URL: http://foo:8080/bar\r\n\r\n"
	req := &Request{Rawdata: []byte(rawdata)}

	err := req.ParseRawdata()
	if err != nil {
		t.Fatalf("ParseRawdata() error: %v", err)
	}

	hl, ok := findHeader(req.headers, "x-url")
	if !ok {
		t.Fatal("x-url header not found")
	}

	expected := "http://foo:8080/bar"
	if string(hl.Value) != expected {
		t.Errorf("x-url value = %q, want %q", hl.Value, expected)
	}
}

func TestRequest_Bytes(t *testing.T) {
	rawdata := "GET /path HTTP/1.1\r\nHost: example.com\r\nX-Test: value\r\n\r\nbody"
	req := &Request{Rawdata: []byte(rawdata)}

	err := req.ParseRawdata()
	if err != nil {
		t.Fatalf("ParseRawdata() error: %v", err)
	}

	result := req.Bytes()

	// Should contain all parts
	if !bytes.Contains(result, []byte("GET /path HTTP/1.1")) {
		t.Error("Bytes() missing HTTP line")
	}
	if !bytes.Contains(result, []byte("Host: example.com")) {
		t.Error("Bytes() missing Host header")
	}
	if !bytes.Contains(result, []byte("X-Test: value")) {
		t.Error("Bytes() missing X-Test header")
	}
	if !bytes.HasSuffix(result, []byte("\r\n\r\nbody")) {
		t.Error("Bytes() missing body section")
	}
}

func TestSetHeader(t *testing.T) {
	rawdata := "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"
	req := &Request{Rawdata: []byte(rawdata)}
	req.ParseRawdata()

	// Update existing header
	req.SetHeader("host", []byte("Host"), []byte("newhost.com"))
	hl, ok := findHeader(req.headers, "host")
	if !ok || string(hl.Value) != "newhost.com" {
		t.Error("SetHeader failed to update existing header")
	}

	// Add new header
	req.SetHeader("x-new", []byte("X-New"), []byte("newvalue"))
	hl, ok = findHeader(req.headers, "x-new")
	if !ok || string(hl.Value) != "newvalue" {
		t.Error("SetHeader failed to add new header")
	}
}

func TestSetConnectionClose(t *testing.T) {
	rawdata := "GET / HTTP/1.1\r\nHost: example.com\r\nConnection: keep-alive\r\n\r\n"
	req := &Request{Rawdata: []byte(rawdata)}
	req.ParseRawdata()

	req.SetConnectionClose()

	hl, ok := findHeader(req.headers, "connection")
	if !ok {
		t.Fatal("connection header not found")
	}

	if string(hl.Value) != "close" {
		t.Errorf("connection value = %q, want %q", hl.Value, "close")
	}
}

func TestAddQueryParams(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		params   string
		wantPath string
	}{
		{
			name:     "no existing query",
			path:     "/api",
			params:   "foo=bar",
			wantPath: "/api?foo=bar",
		},
		{
			name:     "existing query",
			path:     "/api?existing=1",
			params:   "foo=bar",
			wantPath: "/api?existing=1&foo=bar",
		},
		{
			name:     "with fragment - fragment appended without hash",
			path:     "/api#section",
			params:   "foo=bar",
			wantPath: "/api?foo=barsection",
		},
		{
			name:     "existing query with fragment - fragment appended without hash",
			path:     "/api?existing=1#section",
			params:   "foo=bar",
			wantPath: "/api?existing=1&foo=barsection",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &Request{path: []byte(tt.path)}
			req.AddQueryParams([]byte(tt.params))

			if string(req.path) != tt.wantPath {
				t.Errorf("path = %q, want %q", req.path, tt.wantPath)
			}
		})
	}
}

func TestWantsClose(t *testing.T) {
	tests := []struct {
		name     string
		rawdata  string
		wantBool bool
	}{
		{
			name:     "connection close",
			rawdata:  "GET / HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n",
			wantBool: true,
		},
		{
			name:     "connection keep-alive",
			rawdata:  "GET / HTTP/1.1\r\nHost: x\r\nConnection: keep-alive\r\n\r\n",
			wantBool: false,
		},
		{
			name:     "no connection header",
			rawdata:  "GET / HTTP/1.1\r\nHost: x\r\n\r\n",
			wantBool: false,
		},
		{
			name:     "connection close case insensitive",
			rawdata:  "GET / HTTP/1.1\r\nHost: x\r\nConnection: CLOSE\r\n\r\n",
			wantBool: true,
		},
		{
			name:     "connection with multiple values",
			rawdata:  "GET / HTTP/1.1\r\nHost: x\r\nConnection: keep-alive, close\r\n\r\n",
			wantBool: true,
		},
		{
			name:     "connection with multiple unmatched values",
			rawdata:  "GET / HTTP/1.1\r\nHost: x\r\nConnection: keep-alive, closeconn\r\n\r\n",
			wantBool: false,
		},
		{
			name:     "connection with multiple unmatched values with - as separator",
			rawdata:  "GET / HTTP/1.1\r\nHost: x\r\nConnection: keep-alive, close-conn\r\n\r\n",
			wantBool: false,
		},
		{
			name:     "connection with multiple unmatched values with _ as separator",
			rawdata:  "GET / HTTP/1.1\r\nHost: x\r\nConnection: keep_alive, close_conn\r\n\r\n",
			wantBool: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &Request{Rawdata: []byte(tt.rawdata)}
			req.ParseRawdata()

			if got := req.WantsClose(); got != tt.wantBool {
				t.Errorf("WantsClose() = %v, want %v", got, tt.wantBool)
			}
		})
	}
}

func TestWantsUpgrade(t *testing.T) {
	tests := []struct {
		name     string
		rawdata  string
		wantBool bool
	}{
		{
			name:     "connection upgrade",
			rawdata:  "GET / HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\n\r\n",
			wantBool: true,
		},
		{
			name:     "connection keep-alive",
			rawdata:  "GET / HTTP/1.1\r\nHost: x\r\nConnection: keep-alive\r\n\r\n",
			wantBool: false,
		},
		{
			name:     "connection upgrade lowercase",
			rawdata:  "GET / HTTP/1.1\r\nHost: x\r\nConnection: upgrade\r\n\r\n",
			wantBool: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &Request{Rawdata: []byte(tt.rawdata)}
			req.ParseRawdata()

			if got := req.WantsUpgrade(); got != tt.wantBool {
				t.Errorf("WantsUpgrade() = %v, want %v", got, tt.wantBool)
			}
		})
	}
}

func TestRawMethod(t *testing.T) {
	tests := []struct {
		name       string
		rawdata    string
		wantMethod string
	}{
		{
			name:       "GET method",
			rawdata:    "GET /path HTTP/1.1\nHost: x\n\n",
			wantMethod: "GET",
		},
		{
			name:       "POST method",
			rawdata:    "POST /path HTTP/1.1\nHost: x\n\n",
			wantMethod: "POST",
		},
		{
			name:       "empty rawdata",
			rawdata:    "",
			wantMethod: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := Request{Rawdata: []byte(tt.rawdata)}
			if got := req.RawMethod(); got != tt.wantMethod {
				t.Errorf("RawMethod() = %q, want %q", got, tt.wantMethod)
			}
		})
	}
}

func TestAddr(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		ip       string
		port     string
		wantAddr string
	}{
		{
			name:     "hostname only",
			hostname: "example.com",
			ip:       "",
			port:     "443",
			wantAddr: "example.com:443",
		},
		{
			name:     "IP override",
			hostname: "example.com",
			ip:       "1.2.3.4",
			port:     "443",
			wantAddr: "1.2.3.4:443",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &Request{
				IP: tt.ip,
			}
			req.URI, _ = parseTestURL("https://" + tt.hostname)

			if got := req.Addr(tt.port); got != tt.wantAddr {
				t.Errorf("Addr() = %q, want %q", got, tt.wantAddr)
			}
		})
	}
}

func TestNewBaseRequest(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{
			name:    "valid https URL",
			url:     "https://example.com/path",
			wantErr: false,
		},
		{
			name:    "valid http URL",
			url:     "http://example.com/path?query=1",
			wantErr: false,
		},
		{
			name:    "invalid URL",
			url:     "://invalid",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := NewBaseRequest(tt.url)

			if tt.wantErr {
				if err == nil {
					t.Error("NewBaseRequest() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("NewBaseRequest() unexpected error: %v", err)
				return
			}

			if req.URL != tt.url {
				t.Errorf("URL = %q, want %q", req.URL, tt.url)
			}

			if req.URI == nil {
				t.Error("URI is nil")
			}
		})
	}
}

func TestPrepareRequestVariables(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		input     string
		wantInOut string
	}{
		{
			name:      "HOST variable",
			url:       "https://example.com/path",
			input:     "||HOST||",
			wantInOut: "example.com",
		},
		{
			name:      "PATH variable",
			url:       "https://example.com/mypath",
			input:     "||PATH||",
			wantInOut: "/mypath",
		},
		{
			name:      "PATH variable empty defaults to /",
			url:       "https://example.com",
			input:     "||PATH||",
			wantInOut: "/",
		},
		{
			name:      "ABSURL variable",
			url:       "https://example.com/path",
			input:     "||ABSURL||",
			wantInOut: "https://example.com/path",
		},
		{
			name:      "CR LF variables",
			url:       "https://example.com/",
			input:     "a||CR||||LF||b",
			wantInOut: "a\r\nb",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &Request{
				URL:  tt.url,
				body: []byte(tt.input),
			}
			req.URI, _ = parseTestURL(tt.url)

			PrepareRequestVariables(req)

			if string(req.body) != tt.wantInOut {
				t.Errorf("body = %q, want %q", req.body, tt.wantInOut)
			}
		})
	}
}

func TestContentLengthCalculation(t *testing.T) {
	tests := []struct {
		name    string
		rawdata string
		wantLen string
	}{
		{
			name:    "with body",
			rawdata: "POST / HTTP/1.1\r\nContent-Length: ||CLEN||\r\n\r\nhello",
			wantLen: "5",
		},
		{
			name:    "empty body",
			rawdata: "GET / HTTP/1.1\r\nContent-Length: ||CLEN||\r\n\r\n",
			wantLen: "0",
		},
		{
			name:    "no body section",
			rawdata: "GET / HTTP/1.1\r\nContent-Length: ||CLEN||",
			wantLen: "0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &Request{Rawdata: []byte(tt.rawdata)}
			ContentLengthCalculation(req)

			if !bytes.Contains(req.Rawdata, []byte(tt.wantLen)) {
				t.Errorf("Rawdata should contain %q, got %q", tt.wantLen, req.Rawdata)
			}
		})
	}
}

func TestTrimSpaces(t *testing.T) {
	input := [][]byte{
		[]byte("  hello  "),
		[]byte(""),
		[]byte("   "),
		[]byte("world"),
	}

	result := trimSpaces(input)

	if len(result) != 2 {
		t.Errorf("len(result) = %d, want 2", len(result))
	}

	if string(result[0]) != "hello" {
		t.Errorf("result[0] = %q, want %q", result[0], "hello")
	}

	if string(result[1]) != "world" {
		t.Errorf("result[1] = %q, want %q", result[1], "world")
	}
}

func TestSetRawdata(t *testing.T) {
	req := &Request{}

	rawdata := "GET /new HTTP/1.1\r\nHost: new.com\r\n\r\n"
	err := req.SetRawdata([]byte(rawdata))
	if err != nil {
		t.Fatalf("SetRawdata() error: %v", err)
	}

	if string(req.method) != "GET" {
		t.Errorf("method = %q, want GET", req.method)
	}

	if string(req.path) != "/new" {
		t.Errorf("path = %q, want /new", req.path)
	}
}

func TestSetMethod(t *testing.T) {
	req := &Request{}
	req.SetMethod([]byte("POST"))

	if string(req.method) != "POST" {
		t.Errorf("method = %q, want POST", req.method)
	}
}

func TestSetBody(t *testing.T) {
	req := &Request{}
	req.SetBody([]byte("test body"))

	if string(req.body) != "test body" {
		t.Errorf("body = %q, want 'test body'", req.body)
	}
}

func TestParsedPath(t *testing.T) {
	req := &Request{path: []byte("/test/path")}

	if string(req.ParsedPath()) != "/test/path" {
		t.Errorf("ParsedPath() = %q, want '/test/path'", req.ParsedPath())
	}
}

// Helper function for tests
func parseTestURL(rawURL string) (*url.URL, error) {
	return url.Parse(rawURL)
}

func TestCacheBusterParam(t *testing.T) {
	req := &Request{path: []byte("/api")}
	req.CacheBusterParam()

	path := string(req.path)
	if !strings.Contains(path, "?") {
		t.Fatalf("CacheBusterParam() path missing '?', got %q", path)
	}

	// Path should be /api?XXXX=XXXX where XXXX is 4-char hex
	parts := strings.SplitN(path, "?", 2)
	if parts[0] != "/api" {
		t.Errorf("base path = %q, want /api", parts[0])
	}

	kv := strings.SplitN(parts[1], "=", 2)
	if len(kv) != 2 {
		t.Fatalf("expected key=value param, got %q", parts[1])
	}
	if kv[0] != kv[1] {
		t.Errorf("cache buster key %q != value %q", kv[0], kv[1])
	}
	if len(kv[0]) != 8 {
		t.Errorf("cache buster param length = %d, want 8", len(kv[0]))
	}
	// Verify it's valid hex
	_, err := hex.DecodeString(kv[0])
	if err != nil {
		t.Errorf("cache buster param %q is not valid hex: %v", kv[0], err)
	}
}

func TestNewRawPathRequest(t *testing.T) {
	req, err := NewRawPathRequest("https://example.com/original", "/custom/path")
	if err != nil {
		t.Fatalf("NewRawPathRequest() error: %v", err)
	}

	if req.Method() != "GET" {
		t.Errorf("Method() = %q, want GET", req.Method())
	}
	if req.Path() != "/custom/path" {
		t.Errorf("Path() = %q, want /custom/path", req.Path())
	}
	if req.Host() != "example.com" {
		t.Errorf("Host() = %q, want example.com", req.Host())
	}
	if req.URI == nil {
		t.Fatal("URI is nil")
	}
	if req.URL != "https://example.com/original" {
		t.Errorf("URL = %q, want https://example.com/original", req.URL)
	}
}

func TestNewRawPathRequest_InvalidURL(t *testing.T) {
	_, err := NewRawPathRequest("://invalid", "/path")
	if err == nil {
		t.Error("NewRawPathRequest() with invalid URL expected error, got nil")
	}
}

func TestFullPath(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		wantPath string
	}{
		{
			name:     "path with query and fragment",
			url:      "https://example.com/path?key=val#section",
			wantPath: "/path?key=val#section",
		},
		{
			name:     "path with query no fragment",
			url:      "https://example.com/path?key=val",
			wantPath: "/path?key=val",
		},
		{
			name:     "path only",
			url:      "https://example.com/path",
			wantPath: "/path",
		},
		{
			name:     "root path",
			url:      "https://example.com/",
			wantPath: "/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uri, err := url.Parse(tt.url)
			if err != nil {
				t.Fatalf("url.Parse() error: %v", err)
			}
			req := &Request{URI: uri}

			got := req.FullPath()
			if got != tt.wantPath {
				t.Errorf("FullPath() = %q, want %q", got, tt.wantPath)
			}
		})
	}
}

func TestFullPath_NoFragment(t *testing.T) {
	uri, _ := url.Parse("https://example.com/search?q=test")
	req := &Request{URI: uri}

	got := req.FullPath()
	if got != "/search?q=test" {
		t.Errorf("FullPath() = %q, want /search?q=test", got)
	}
	if strings.Contains(got, "#") {
		t.Errorf("FullPath() should not contain '#', got %q", got)
	}
}

func TestPrepareRequestVariables_CLEN(t *testing.T) {
	req := &Request{
		URL:  "https://example.com/path",
		body: []byte("hello world"),
	}
	req.URI, _ = url.Parse(req.URL)

	// Put ||CLEN|| in a header value
	req.headers = []HeaderLine{
		{Key: []byte("Content-Length"), Value: []byte("||CLEN||")},
	}

	PrepareRequestVariables(req)

	// Body is 11 bytes ("hello world")
	hl, ok := findHeader(req.headers, "content-length")
	if !ok {
		t.Fatal("Content-Length header not found")
	}
	if string(hl.Value) != "11" {
		t.Errorf("Content-Length = %q, want \"11\"", hl.Value)
	}
}

func TestPrepareRequestVariables_END(t *testing.T) {
	req := &Request{
		URL:  "https://example.com/",
		body: []byte("keep this||END||discard this"),
	}
	req.URI, _ = url.Parse(req.URL)

	PrepareRequestVariables(req)

	if string(req.body) != "keep this" {
		t.Errorf("body = %q, want \"keep this\"", req.body)
	}
}

func TestPrepareRequestVariables_ESCAPEDPATH(t *testing.T) {
	req := &Request{
		URL:  "https://example.com/path with spaces/file",
		body: []byte("||ESCAPEDPATH||"),
	}
	req.URI, _ = url.Parse(req.URL)

	PrepareRequestVariables(req)

	// url.URL.EscapedPath() encodes spaces as %20
	if string(req.body) != "/path%20with%20spaces/file" {
		t.Errorf("body = %q, want /path%%20with%%20spaces/file", req.body)
	}
}

func TestPrepareRequestVariables_FULLPATH(t *testing.T) {
	req := &Request{
		URL:  "https://example.com/path?key=val#frag",
		body: []byte("||FULLPATH||"),
	}
	req.URI, _ = url.Parse(req.URL)

	PrepareRequestVariables(req)

	// FullPath returns RequestURI + "#" + Fragment
	expected := "/path?key=val#frag"
	if string(req.body) != expected {
		t.Errorf("body = %q, want %q", req.body, expected)
	}
}

func TestClient_NewRawPathRequest(t *testing.T) {
	client := NewDefaultClient()
	defer client.Close()

	req, err := client.NewRawPathRequest("https://example.com/original", "/custom")
	if err != nil {
		t.Fatalf("client.NewRawPathRequest() error: %v", err)
	}

	if req.Method() != "GET" {
		t.Errorf("Method() = %q, want GET", req.Method())
	}
	if req.Path() != "/custom" {
		t.Errorf("Path() = %q, want /custom", req.Path())
	}
}

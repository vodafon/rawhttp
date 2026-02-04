package rawhttp

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"testing"
)

func TestResponse_StatusCode(t *testing.T) {
	tests := []struct {
		name           string
		rawdata        string
		wantStatusCode int
	}{
		{
			name:           "200 OK",
			rawdata:        "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello",
			wantStatusCode: 200,
		},
		{
			name:           "404 Not Found",
			rawdata:        "HTTP/1.1 404 Not Found\r\nContent-Length: 9\r\n\r\nnot found",
			wantStatusCode: 404,
		},
		{
			name:           "500 Internal Server Error",
			rawdata:        "HTTP/1.1 500 Internal Server Error\r\nContent-Length: 5\r\n\r\nerror",
			wantStatusCode: 500,
		},
		{
			name:           "301 Redirect",
			rawdata:        "HTTP/1.1 301 Moved Permanently\r\nLocation: /new\r\nContent-Length: 0\r\n\r\n",
			wantStatusCode: 301,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &Response{Rawdata: []byte(tt.rawdata)}

			if got := resp.StatusCode(); got != tt.wantStatusCode {
				t.Errorf("StatusCode() = %d, want %d", got, tt.wantStatusCode)
			}
		})
	}
}

func TestResponse_Body(t *testing.T) {
	tests := []struct {
		name     string
		rawdata  string
		wantBody string
	}{
		{
			name:     "simple body",
			rawdata:  "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello",
			wantBody: "hello",
		},
		{
			name:     "empty body",
			rawdata:  "HTTP/1.1 204 No Content\r\nContent-Length: 0\r\n\r\n",
			wantBody: "",
		},
		{
			name:     "body with newlines",
			rawdata:  "HTTP/1.1 200 OK\r\nContent-Length: 11\r\n\r\nline1\nline2",
			wantBody: "line1\nline2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &Response{Rawdata: []byte(tt.rawdata)}

			if got := string(resp.Body()); got != tt.wantBody {
				t.Errorf("Body() = %q, want %q", got, tt.wantBody)
			}
		})
	}
}

func TestResponse_Bytes(t *testing.T) {
	rawdata := "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\nhello"
	resp := &Response{Rawdata: []byte(rawdata)}

	result := resp.Bytes()

	// Should contain headers
	if !bytes.Contains(result, []byte("HTTP/1.1 200 OK")) {
		t.Error("Bytes() missing status line")
	}
	if !bytes.Contains(result, []byte("Content-Type: text/plain")) {
		t.Error("Bytes() missing Content-Type header")
	}

	// Should contain body after separator
	if !bytes.Contains(result, []byte("\r\n\r\nhello")) {
		t.Error("Bytes() missing body")
	}
}

func TestResponse_Reset(t *testing.T) {
	resp := &Response{
		Rawdata:         []byte("HTTP/1.1 200 OK\r\n\r\nbody"),
		TimeToFirstByte: 100,
		TimeToLastByte:  200,
		parsed:          true,
		httpLine:        []byte("HTTP/1.1 200 OK"),
		statusCode:      200,
		preBody:         []byte("HTTP/1.1 200 OK"),
		body:            []byte("body"),
	}

	resp.Reset()

	if resp.Rawdata != nil {
		t.Error("Reset() did not clear Rawdata")
	}
	if resp.TimeToFirstByte != 0 {
		t.Error("Reset() did not clear TimeToFirstByte")
	}
	if resp.TimeToLastByte != 0 {
		t.Error("Reset() did not clear TimeToLastByte")
	}
	if resp.parsed != false {
		t.Error("Reset() did not clear parsed")
	}
	if resp.httpLine != nil {
		t.Error("Reset() did not clear httpLine")
	}
	if resp.statusCode != 0 {
		t.Error("Reset() did not clear statusCode")
	}
	if resp.preBody != nil {
		t.Error("Reset() did not clear preBody")
	}
	if resp.body != nil {
		t.Error("Reset() did not clear body")
	}
}

func TestResponse_ConnectionClose(t *testing.T) {
	tests := []struct {
		name     string
		rawdata  string
		wantBool bool
	}{
		{
			name:     "connection close present",
			rawdata:  "HTTP/1.1 200 OK\r\nConnection: close\r\nContent-Length: 0\r\n\r\n",
			wantBool: true,
		},
		{
			name:     "connection keep-alive",
			rawdata:  "HTTP/1.1 200 OK\r\nConnection: keep-alive\r\nContent-Length: 0\r\n\r\n",
			wantBool: false,
		},
		{
			name:     "no connection header (HTTP/1.1 default keep-alive)",
			rawdata:  "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n",
			wantBool: false,
		},
		{
			name:     "connection close uppercase",
			rawdata:  "HTTP/1.1 200 OK\r\nCONNECTION: CLOSE\r\nContent-Length: 0\r\n\r\n",
			wantBool: true,
		},
		{
			name:     "empty rawdata",
			rawdata:  "",
			wantBool: true,
		},
		{
			name:     "malformed response - no body separator",
			rawdata:  "HTTP/1.1 200 OK\r\nContent-Length: 0",
			wantBool: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &Response{Rawdata: []byte(tt.rawdata)}

			if got := resp.ConnectionClose(); got != tt.wantBool {
				t.Errorf("ConnectionClose() = %v, want %v", got, tt.wantBool)
			}
		})
	}
}

func TestResponse_GzipDecompression(t *testing.T) {
	// Create gzip compressed body
	var buf bytes.Buffer
	gzWriter := gzip.NewWriter(&buf)
	gzWriter.Write([]byte("compressed content"))
	gzWriter.Close()
	gzBody := buf.Bytes()

	// Build raw response with gzip content
	var rawResp bytes.Buffer
	rawResp.WriteString("HTTP/1.1 200 OK\r\n")
	rawResp.WriteString("Content-Encoding: gzip\r\n")
	rawResp.WriteString("Content-Length: ")
	rawResp.WriteString(fmt.Sprintf("%d", len(gzBody)))
	rawResp.WriteString("\r\n\r\n")
	rawResp.Write(gzBody)

	resp := &Response{Rawdata: rawResp.Bytes()}

	body := resp.Body()

	if string(body) != "compressed content" {
		t.Errorf("Body() = %q, want %q", body, "compressed content")
	}
}

func TestNewResponse(t *testing.T) {
	resp := NewResponse()

	if resp == nil {
		t.Fatal("NewResponse() returned nil")
	}

	if resp.Rawdata != nil {
		t.Error("NewResponse() should have nil Rawdata")
	}

	if resp.parsed != false {
		t.Error("NewResponse() should have parsed = false")
	}
}

func TestResponse_ParseRawdata_Idempotent(t *testing.T) {
	rawdata := "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello"
	resp := &Response{Rawdata: []byte(rawdata)}

	// Parse multiple times
	resp.ParseRawdata()
	resp.ParseRawdata()
	resp.ParseRawdata()

	// Should still return correct values
	if resp.StatusCode() != 200 {
		t.Errorf("StatusCode() = %d, want 200", resp.StatusCode())
	}

	if string(resp.Body()) != "hello" {
		t.Errorf("Body() = %q, want 'hello'", resp.Body())
	}
}

func TestResponse_ParseRawdata_ChunkedEncoding(t *testing.T) {
	// Chunked transfer encoding response
	rawdata := "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n0\r\n\r\n"
	resp := &Response{Rawdata: []byte(rawdata)}

	body := resp.Body()

	if string(body) != "hello" {
		t.Errorf("Body() = %q, want 'hello'", body)
	}
}

func TestClient_NewResponse(t *testing.T) {
	client := NewDefaultClient()
	defer client.Close()

	resp := client.NewResponse()

	if resp == nil {
		t.Fatal("client.NewResponse() returned nil")
	}
}

func TestClient_NewRequestResponse(t *testing.T) {
	client := NewDefaultClient()
	defer client.Close()

	req, resp := client.NewRequestResponse()

	if req == nil {
		t.Error("NewRequestResponse() returned nil request")
	}

	if resp == nil {
		t.Error("NewRequestResponse() returned nil response")
	}
}

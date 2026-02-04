package rawhttp

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestNewDefaultClient(t *testing.T) {
	client := NewDefaultClient()
	defer client.Close()

	if client.TransformRequestFunc == nil {
		t.Error("TransformRequestFunc should not be nil")
	}

	if client.Timeout != 10*time.Second {
		t.Errorf("Timeout = %v, want %v", client.Timeout, 10*time.Second)
	}

	if client.QuietTimeout != DefaultQuietTimeout {
		t.Errorf("QuietTimeout = %v, want %v", client.QuietTimeout, DefaultQuietTimeout)
	}

	if client.pool == nil {
		t.Error("pool should not be nil")
	}
}

func TestNewClientTransferVariables(t *testing.T) {
	client := NewClientTransferVariables()
	defer client.Close()

	if client.TransformRequestFunc == nil {
		t.Error("TransformRequestFunc should not be nil")
	}

	if client.Timeout != 10*time.Second {
		t.Errorf("Timeout = %v, want %v", client.Timeout, 10*time.Second)
	}
}

func TestNewDefaultClientTimeout(t *testing.T) {
	timeout := 30 * time.Second
	client := NewDefaultClientTimeout(timeout)
	defer client.Close()

	if client.Timeout != timeout {
		t.Errorf("Timeout = %v, want %v", client.Timeout, timeout)
	}
}

func TestNewClientWithPool(t *testing.T) {
	tests := []struct {
		name     string
		pool     *ConnPool
		wantPool bool
	}{
		{
			name:     "custom pool",
			pool:     NewConnPool(10, 60*time.Second),
			wantPool: true,
		},
		{
			name:     "nil pool creates default",
			pool:     nil,
			wantPool: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClientWithPool(tt.pool)
			defer client.Close()

			if tt.wantPool && client.pool == nil {
				t.Error("pool should not be nil")
			}
		})
	}
}

func TestClient_CloseIdleConnections(t *testing.T) {
	client := NewDefaultClient()
	key := "https://example.com:443"

	// Add a connection to the pool
	connClient, connServer := net.Pipe()
	defer connServer.Close()

	client.pool.Put(key, connClient)

	if client.pool.Len() != 1 {
		t.Fatalf("pool.Len() = %d, want 1", client.pool.Len())
	}

	// Close idle connections
	client.CloseIdleConnections()

	if client.pool.Len() != 0 {
		t.Errorf("pool.Len() = %d, want 0 after CloseIdleConnections", client.pool.Len())
	}

	// Client should still be usable (pool not closed)
	conn2Client, conn2Server := net.Pipe()
	defer conn2Server.Close()
	if !client.pool.Put(key, conn2Client) {
		t.Error("Put should succeed after CloseIdleConnections")
	}

	client.Close()
}

func TestClient_Close(t *testing.T) {
	client := NewDefaultClient()
	key := "https://example.com:443"

	connClient, connServer := net.Pipe()
	defer connServer.Close()

	client.pool.Put(key, connClient)

	client.Close()

	// Pool should be closed
	if !client.pool.closed {
		t.Error("pool should be closed after client.Close()")
	}
}

func TestClient_Do_InvalidURL(t *testing.T) {
	client := NewDefaultClient()
	defer client.Close()

	tests := []struct {
		name    string
		url     string
		wantErr error
	}{
		{
			name:    "relative URL",
			url:     "/path/only",
			wantErr: InvalidURLError,
		},
		{
			name:    "invalid scheme",
			url:     "ftp://example.com/file",
			wantErr: InvalidURLError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &Request{
				Rawdata: []byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"),
				URL:     tt.url,
			}
			resp := &Response{}

			err := client.Do(req, resp)

			if err != tt.wantErr {
				t.Errorf("Do() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestIsTimeoutError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantBool bool
	}{
		{
			name:     "timeout error",
			err:      &timeoutError{},
			wantBool: true,
		},
		{
			name:     "regular error",
			err:      errors.New("some error"),
			wantBool: false,
		},
		{
			name:     "nil error",
			err:      nil,
			wantBool: false,
		},
		{
			name:     "io.EOF",
			err:      io.EOF,
			wantBool: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTimeoutError(tt.err); got != tt.wantBool {
				t.Errorf("isTimeoutError() = %v, want %v", got, tt.wantBool)
			}
		})
	}
}

func TestIsStaleConnError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantBool bool
	}{
		{
			name:     "nil error",
			err:      nil,
			wantBool: false,
		},
		{
			name:     "io.EOF",
			err:      io.EOF,
			wantBool: true,
		},
		{
			name:     "broken pipe",
			err:      errors.New("write: broken pipe"),
			wantBool: true,
		},
		{
			name:     "connection reset",
			err:      errors.New("read: connection reset by peer"),
			wantBool: true,
		},
		{
			name:     "use of closed network connection",
			err:      errors.New("use of closed network connection"),
			wantBool: true,
		},
		{
			name:     "other error",
			err:      errors.New("some other error"),
			wantBool: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isStaleConnError(tt.err); got != tt.wantBool {
				t.Errorf("isStaleConnError() = %v, want %v", got, tt.wantBool)
			}
		})
	}
}

func TestClient_NewRequest(t *testing.T) {
	client := NewDefaultClient()
	defer client.Close()

	req := client.NewRequest()

	if req == nil {
		t.Error("NewRequest() returned nil")
	}
}

func TestClient_NewBaseRequest(t *testing.T) {
	client := NewDefaultClient()
	defer client.Close()

	req, err := client.NewBaseRequest("https://example.com/path")

	if err != nil {
		t.Fatalf("NewBaseRequest() error: %v", err)
	}

	if req == nil {
		t.Fatal("NewBaseRequest() returned nil")
	}

	if req.URL != "https://example.com/path" {
		t.Errorf("URL = %q, want %q", req.URL, "https://example.com/path")
	}
}

func TestClient_SetProxy(t *testing.T) {
	client := NewDefaultClient()
	defer client.Close()

	proxyURL, _ := parseTestURL("http://proxy.example.com:8080")
	client.SetProxy(proxyURL)

	if client.proxyURI == nil {
		t.Error("proxyURI should be set")
	}

	if client.proxyURI.Host != "proxy.example.com:8080" {
		t.Errorf("proxyURI.Host = %q, want %q", client.proxyURI.Host, "proxy.example.com:8080")
	}
}

func TestInvalidURLError(t *testing.T) {
	if InvalidURLError.Error() != "Invalid URL" {
		t.Errorf("InvalidURLError.Error() = %q, want %q", InvalidURLError.Error(), "Invalid URL")
	}
}

func TestInvalidRequestError(t *testing.T) {
	if InvalidRequestError.Error() != "Invalid Request" {
		t.Errorf("InvalidRequestError.Error() = %q, want %q", InvalidRequestError.Error(), "Invalid Request")
	}
}

// Mock timeout error for testing
type timeoutError struct{}

func (e *timeoutError) Error() string   { return "timeout" }
func (e *timeoutError) Timeout() bool   { return true }
func (e *timeoutError) Temporary() bool { return true }

// Verify timeoutError implements net.Error
var _ net.Error = (*timeoutError)(nil)

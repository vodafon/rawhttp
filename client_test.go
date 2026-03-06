package rawhttp

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/url"
	"strings"
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

// simpleTestRequest creates a minimal request for testing doConnInternal/DoConn.
// TransformRequestFunc is set to a no-op so it doesn't dereference URI.
func simpleTestRequest(rawdata string) *Request {
	req := &Request{Rawdata: []byte(rawdata)}
	req.ParseRawdata()
	return req
}

func TestDoConnInternal_Success(t *testing.T) {
	client := &Client{
		TransformRequestFunc: PrepareRequest,
		Timeout:              2 * time.Second,
		QuietTimeout:         10 * time.Millisecond,
	}

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	// Server goroutine: read request, write response, close
	go func() {
		br := bufio.NewReader(serverConn)
		// Drain the request
		for {
			line, err := br.ReadString('\n')
			if err != nil || strings.TrimSpace(line) == "" {
				break
			}
		}
		// Write response
		serverConn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello"))
		serverConn.Close()
	}()

	req := simpleTestRequest("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")
	resp := &Response{}

	err := client.doConnInternal(clientConn, req, resp)
	if err != nil {
		t.Fatalf("doConnInternal() error: %v", err)
	}

	if !strings.Contains(string(resp.Rawdata), "200 OK") {
		t.Errorf("resp.Rawdata = %q, want to contain '200 OK'", resp.Rawdata)
	}
	if resp.TimeToFirstByte == 0 {
		t.Error("TimeToFirstByte should be > 0")
	}
	if resp.TimeToLastByte == 0 {
		t.Error("TimeToLastByte should be > 0")
	}
}

func TestDoConnInternal_WriteError(t *testing.T) {
	client := &Client{
		TransformRequestFunc: PrepareRequest,
		Timeout:              time.Second,
		QuietTimeout:         10 * time.Millisecond,
	}

	clientConn, serverConn := net.Pipe()
	serverConn.Close()
	clientConn.Close()

	req := simpleTestRequest("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")
	resp := &Response{}

	err := client.doConnInternal(clientConn, req, resp)
	if err == nil {
		t.Error("doConnInternal() expected error on closed conn, got nil")
	}
}

func TestDoConnInternal_EOF(t *testing.T) {
	client := &Client{
		TransformRequestFunc: PrepareRequest,
		Timeout:              time.Second,
		QuietTimeout:         10 * time.Millisecond,
	}

	clientConn, serverConn := net.Pipe()

	// Server reads request then closes without writing response
	go func() {
		buf := make([]byte, 4096)
		serverConn.Read(buf)
		serverConn.Close()
	}()

	req := simpleTestRequest("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")
	resp := &Response{}

	err := client.doConnInternal(clientConn, req, resp)
	if err != io.EOF {
		t.Errorf("doConnInternal() error = %v, want io.EOF", err)
	}
	clientConn.Close()
}

func TestDoConnInternal_SpecBased_IgnoresQuietTimeout(t *testing.T) {
	// Verify that in normal mode (ReadFull=false), spec-based reading uses
	// Content-Length to determine response completeness. QuietTimeout is
	// irrelevant — even when set to 0, the response is read correctly.
	client := &Client{
		TransformRequestFunc: PrepareRequest,
		Timeout:              2 * time.Second,
		QuietTimeout:         0, // irrelevant in normal mode
	}
	
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	
	go func() {
		buf := make([]byte, 4096)
		serverConn.Read(buf)
		serverConn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"))
		serverConn.Close()
	}()
	
	req := simpleTestRequest("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")
	resp := &Response{}
	
	err := client.doConnInternal(clientConn, req, resp)
	if err != nil {
		t.Fatalf("doConnInternal() error: %v", err)
	}
	if !strings.Contains(string(resp.Rawdata), "ok") {
		t.Errorf("resp.Rawdata should contain 'ok', got %q", resp.Rawdata)
	}
	// Verify spec-based reading populates parsed fields via ReadResponsePartial
	if resp.StatusCode() != 200 {
		t.Errorf("StatusCode() = %d, want 200", resp.StatusCode())
	}
	if string(resp.Body()) != "ok" {
		t.Errorf("Body() = %q, want %q", resp.Body(), "ok")
	}
	if resp.TimeToFirstByte == 0 {
		t.Error("TimeToFirstByte should be > 0")
	}
	if resp.TimeToLastByte == 0 {
		t.Error("TimeToLastByte should be > 0")
	}
}

func TestDoConn(t *testing.T) {
	client := &Client{
		TransformRequestFunc: PrepareRequest,
		Timeout:              2 * time.Second,
		QuietTimeout:         10 * time.Millisecond,
	}

	clientConn, serverConn := net.Pipe()

	go func() {
		buf := make([]byte, 4096)
		serverConn.Read(buf)
		serverConn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 4\r\n\r\ndone"))
		serverConn.Close()
	}()

	req := simpleTestRequest("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")
	resp := &Response{}

	err := client.DoConn(clientConn, req, resp)
	if err != nil {
		t.Fatalf("DoConn() error: %v", err)
	}
	if !strings.Contains(string(resp.Rawdata), "done") {
		t.Errorf("resp.Rawdata should contain 'done', got %q", resp.Rawdata)
	}
}

func TestDoConnWithPool_ReusableConnection(t *testing.T) {
	pool := NewDefaultConnPool()
	client := &Client{
		TransformRequestFunc: PrepareRequest,
		Timeout:              2 * time.Second,
		QuietTimeout:         10 * time.Millisecond,
		pool:                 pool,
	}

	clientConn, serverConn := net.Pipe()
	poolKey := "http://example.com:80"

	// Server: read request, write keep-alive response, then keep conn open
	go func() {
		br := bufio.NewReader(serverConn)
		for {
			line, err := br.ReadString('\n')
			if err != nil || strings.TrimSpace(line) == "" {
				break
			}
		}
		serverConn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: keep-alive\r\n\r\nok"))
		// Keep connection open for pooling
	}()

	req := simpleTestRequest("GET / HTTP/1.1\r\nHost: example.com\r\nConnection: keep-alive\r\n\r\n")
	resp := &Response{}

	err := client.doConnWithPool(clientConn, req, resp, poolKey)
	if err != nil {
		t.Fatalf("doConnWithPool() error: %v", err)
	}

	// Connection should be returned to pool
	if pool.LenForHost(poolKey) != 1 {
		t.Errorf("pool.LenForHost(%q) = %d, want 1", poolKey, pool.LenForHost(poolKey))
	}

	pool.CloseAll()
	serverConn.Close()
}

func TestDoConnWithPool_ConnectionClose(t *testing.T) {
	pool := NewDefaultConnPool()
	client := &Client{
		TransformRequestFunc: PrepareRequest,
		Timeout:              2 * time.Second,
		QuietTimeout:         10 * time.Millisecond,
		pool:                 pool,
	}

	clientConn, serverConn := net.Pipe()
	poolKey := "http://example.com:80"

	go func() {
		br := bufio.NewReader(serverConn)
		for {
			line, err := br.ReadString('\n')
			if err != nil || strings.TrimSpace(line) == "" {
				break
			}
		}
		serverConn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok"))
		serverConn.Close()
	}()

	req := simpleTestRequest("GET / HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n")
	resp := &Response{}

	err := client.doConnWithPool(clientConn, req, resp, poolKey)
	if err != nil {
		t.Fatalf("doConnWithPool() error: %v", err)
	}

	// Connection should NOT be in pool (Connection: close)
	if pool.LenForHost(poolKey) != 0 {
		t.Errorf("pool.LenForHost(%q) = %d, want 0", poolKey, pool.LenForHost(poolKey))
	}

	pool.CloseAll()
}

func TestDoConnWithPool_DisableKeepAlive(t *testing.T) {
	pool := NewDefaultConnPool()
	client := &Client{
		TransformRequestFunc: PrepareRequest,
		Timeout:              2 * time.Second,
		QuietTimeout:         10 * time.Millisecond,
		pool:                 pool,
		DisableKeepAlive:     true,
	}

	clientConn, serverConn := net.Pipe()
	poolKey := "http://example.com:80"

	go func() {
		br := bufio.NewReader(serverConn)
		for {
			line, err := br.ReadString('\n')
			if err != nil || strings.TrimSpace(line) == "" {
				break
			}
		}
		serverConn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"))
		serverConn.Close()
	}()

	req := simpleTestRequest("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")
	resp := &Response{}

	err := client.doConnWithPool(clientConn, req, resp, poolKey)
	if err != nil {
		t.Fatalf("doConnWithPool() error: %v", err)
	}

	// Connection should NOT be pooled when DisableKeepAlive=true
	if pool.LenForHost(poolKey) != 0 {
		t.Errorf("pool.LenForHost(%q) = %d, want 0", poolKey, pool.LenForHost(poolKey))
	}

	pool.CloseAll()
}

// startHTTPListener starts a local TCP listener that responds with a fixed HTTP response.
// Returns the listener address and a cleanup function.
func startHTTPListener(t *testing.T, response string) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen error: %v", err)
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				br := bufio.NewReader(c)
				for {
					line, err := br.ReadString('\n')
					if err != nil || strings.TrimSpace(line) == "" {
						break
					}
				}
				c.Write([]byte(response))
				c.Close()
			}(conn)
		}
	}()

	return ln.Addr().String(), func() { ln.Close() }
}

func TestDoHTTP_Success(t *testing.T) {
	addr, cleanup := startHTTPListener(t, "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello")
	defer cleanup()

	client := NewDefaultClientTimeout(2 * time.Second)
	defer client.Close()

	u, _ := url.Parse("http://" + addr + "/test")
	req, _ := NewBaseRequest(u.String())
	resp := NewResponse()

	err := client.DoHTTP(req, resp)
	if err != nil {
		t.Fatalf("DoHTTP() error: %v", err)
	}
	if !strings.Contains(string(resp.Rawdata), "hello") {
		t.Errorf("resp.Rawdata should contain 'hello', got %q", resp.Rawdata)
	}
}

func TestDoHTTP_DefaultPort(t *testing.T) {
	// Test that DoHTTP fills in port 80 when not specified
	client := NewDefaultClientTimeout(500 * time.Millisecond)
	defer client.Close()

	// This will fail to connect but exercises the default port path
	u, _ := url.Parse("http://127.0.0.1/test")
	req := &Request{
		Rawdata: []byte("GET /test HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n"),
		URL:     u.String(),
		URI:     u,
	}
	req.ParseRawdata()
	PrepareRequest(req)
	resp := NewResponse()

	err := client.DoHTTP(req, resp)
	// Expected to fail connecting since nothing is on port 80
	if err == nil {
		t.Error("DoHTTP() to non-listening port should error")
	}
}

// generateSelfSignedCert creates a self-signed TLS certificate for testing.
func generateSelfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyBytes, _ := x509.MarshalECPrivateKey(priv)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("load cert: %v", err)
	}
	return cert
}

func startTLSListener(t *testing.T, response string) (string, func()) {
	t.Helper()
	cert := generateSelfSignedCert(t)
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsConfig)
	if err != nil {
		t.Fatalf("tls.Listen error: %v", err)
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				br := bufio.NewReader(c)
				for {
					line, err := br.ReadString('\n')
					if err != nil || strings.TrimSpace(line) == "" {
						break
					}
				}
				c.Write([]byte(response))
				c.Close()
			}(conn)
		}
	}()

	return ln.Addr().String(), func() { ln.Close() }
}

func TestDoHTTPS_Success(t *testing.T) {
	addr, cleanup := startTLSListener(t, "HTTP/1.1 200 OK\r\nContent-Length: 3\r\n\r\ntls")
	defer cleanup()

	client := NewDefaultClientTimeout(2 * time.Second)
	defer client.Close()

	u, _ := url.Parse("https://" + addr + "/test")
	req, _ := NewBaseRequest(u.String())
	resp := NewResponse()

	err := client.DoHTTPS(req, resp)
	if err != nil {
		t.Fatalf("DoHTTPS() error: %v", err)
	}
	if !strings.Contains(string(resp.Rawdata), "tls") {
		t.Errorf("resp.Rawdata should contain 'tls', got %q", resp.Rawdata)
	}
}

func TestDoHTTPS_DefaultPort(t *testing.T) {
	client := NewDefaultClientTimeout(500 * time.Millisecond)
	defer client.Close()

	u, _ := url.Parse("https://127.0.0.1/test")
	req := &Request{
		Rawdata: []byte("GET /test HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n"),
		URL:     u.String(),
		URI:     u,
	}
	req.ParseRawdata()
	PrepareRequest(req)
	resp := NewResponse()

	err := client.DoHTTPS(req, resp)
	if err == nil {
		t.Error("DoHTTPS() to non-listening port should error")
	}
}

func TestDoProxy_InvalidRequest(t *testing.T) {
	client := NewDefaultClientTimeout(time.Second)
	defer client.Close()

	u, _ := url.Parse("https://example.com:443")
	// Request without \r\n\r\n separator
	req := &Request{
		Rawdata: []byte("CONNECT example.com:443 HTTP/1.1"),
		URL:     "https://example.com:443",
		URI:     u,
	}
	resp := NewResponse()

	err := client.DoProxy(req, resp)
	if err != InvalidRequestError {
		t.Errorf("DoProxy() error = %v, want InvalidRequestError", err)
	}
}

func TestClient_httpDialer(t *testing.T) {
	client := NewDefaultClient()
	defer client.Close()

	d := client.httpDialer()
	if d == nil {
		t.Fatal("httpDialer() returned nil")
	}

	// Verify it can dial a local listener
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen error: %v", err)
	}
	defer ln.Close()

	conn, err := d.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("httpDialer.Dial() error: %v", err)
	}
	conn.Close()
}

func TestClient_httpsDialer(t *testing.T) {
	client := NewDefaultClient()
	defer client.Close()

	d := client.httpsDialer()
	if d == nil {
		t.Fatal("httpsDialer() returned nil")
	}

	// httpsDialer does its own TLS handshake (tls.DialWithDialer),
	// so we need a plain TCP listener that does server-side TLS on Accept.
	cert := generateSelfSignedCert(t)
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen error: %v", err)
	}
	defer ln.Close()

	go func() {
		rawConn, err := ln.Accept()
		if err != nil {
			return
		}
		tlsConn := tls.Server(rawConn, tlsConfig)
		if err := tlsConn.Handshake(); err != nil {
			rawConn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
		tlsConn.Close()
	}()

	conn, err := d.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("httpsDialer.Dial() error: %v", err)
	}
	conn.Close()
}

func TestDoHTTP_ConnectionPooling(t *testing.T) {
	// Start a server that keeps connections open
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen error: %v", err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				// Handle multiple requests on same connection
				for {
					// Read request line and headers
					for {
						line, err := br.ReadString('\n')
						if err != nil {
							return
						}
						if strings.TrimSpace(line) == "" {
							break
						}
					}
					c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: keep-alive\r\n\r\nok"))
				}
			}(conn)
		}
	}()

	client := NewDefaultClientTimeout(2 * time.Second)
	defer client.Close()

	// Make first request
	u, _ := url.Parse(fmt.Sprintf("http://%s/test1", ln.Addr().String()))
	req, _ := NewBaseRequest(u.String())
	resp := NewResponse()

	err = client.DoHTTP(req, resp)
	if err != nil {
		t.Fatalf("first DoHTTP() error: %v", err)
	}

	// Pool should have one connection now
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "80"
	}
	poolKey := PoolKey("http", host, port)
	if client.pool.LenForHost(poolKey) != 1 {
		t.Errorf("pool should have 1 connection for %s, got %d", poolKey, client.pool.LenForHost(poolKey))
	}

	// Second request should reuse pooled connection
	req2, _ := NewBaseRequest(u.String())
	resp2 := NewResponse()
	err = client.DoHTTP(req2, resp2)
	if err != nil {
		t.Fatalf("second DoHTTP() error: %v", err)
	}
	if !strings.Contains(string(resp2.Rawdata), "ok") {
		t.Errorf("second request resp should contain 'ok', got %q", resp2.Rawdata)
	}
}

func TestDoHTTP_StaleConnectionRetry(t *testing.T) {
	// Start listener that accepts one request then closes
	addr, cleanup := startHTTPListener(t, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: keep-alive\r\n\r\nok")
	defer cleanup()

	client := NewDefaultClientTimeout(2 * time.Second)
	defer client.Close()

	u, _ := url.Parse("http://" + addr + "/test")

	// First request - populates pool
	req1, _ := NewBaseRequest(u.String())
	resp1 := NewResponse()
	err := client.DoHTTP(req1, resp1)
	if err != nil {
		t.Fatalf("first DoHTTP() error: %v", err)
	}

	// The pooled connection from first request was closed by server.
	// Second request should detect stale conn and retry with fresh one.
	req2, _ := NewBaseRequest(u.String())
	resp2 := NewResponse()
	err = client.DoHTTP(req2, resp2)
	// This should succeed with retry
	if err != nil {
		t.Fatalf("second DoHTTP() (retry) error: %v", err)
	}
}

func TestClient_Do_HTTP(t *testing.T) {
	addr, cleanup := startHTTPListener(t, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
	defer cleanup()

	client := NewDefaultClientTimeout(2 * time.Second)
	defer client.Close()

	u := "http://" + addr + "/test"
	req, _ := NewBaseRequest(u)
	resp := NewResponse()

	err := client.Do(req, resp)
	if err != nil {
		t.Fatalf("Do() error: %v", err)
	}
	if !strings.Contains(string(resp.Rawdata), "ok") {
		t.Errorf("resp should contain 'ok', got %q", resp.Rawdata)
	}
}

func TestClient_Do_HTTPS(t *testing.T) {
	addr, cleanup := startTLSListener(t, "HTTP/1.1 200 OK\r\nContent-Length: 3\r\n\r\ntls")
	defer cleanup()

	client := NewDefaultClientTimeout(2 * time.Second)
	defer client.Close()

	u := "https://" + addr + "/test"
	req, _ := NewBaseRequest(u)
	resp := NewResponse()

	err := client.Do(req, resp)
	if err != nil {
		t.Fatalf("Do() error: %v", err)
	}
	if !strings.Contains(string(resp.Rawdata), "tls") {
		t.Errorf("resp should contain 'tls', got %q", resp.Rawdata)
	}
}

func TestClient_Do_CONNECT(t *testing.T) {
	// Test that Do routes CONNECT requests to DoProxy
	// This will fail connecting but exercises the routing logic
	client := NewDefaultClientTimeout(500 * time.Millisecond)
	defer client.Close()

	u, _ := url.Parse("https://127.0.0.1:9999")
	req := &Request{
		Rawdata: []byte("CONNECT 127.0.0.1:9999 HTTP/1.1\r\nHost: 127.0.0.1:9999\r\n\r\nextra"),
		URL:     u.String(),
		URI:     u,
	}
	req.ParseRawdata()
	client.TransformRequestFunc(req)
	resp := NewResponse()

	// Should try DoProxy path and fail connecting
	err := client.Do(req, resp)
	if err == nil {
		t.Error("Do() with CONNECT to non-listening addr should error")
	}
}

func TestDoConnInternal_HEAD(t *testing.T) {
	// HEAD responses include Content-Length but no body.
	// The client must correctly parse StatusCode, Header, Body.
	client := &Client{
		TransformRequestFunc: PrepareRequest,
		Timeout:              2 * time.Second,
		QuietTimeout:         10 * time.Millisecond,
	}

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	go func() {
		br := bufio.NewReader(serverConn)
		for {
			line, err := br.ReadString('\n')
			if err != nil || strings.TrimSpace(line) == "" {
				break
			}
		}
		// HEAD response: has Content-Length but NO body
		serverConn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 12345\r\nContent-Type: text/html\r\n\r\n"))
		serverConn.Close()
	}()

	req := simpleTestRequest("HEAD / HTTP/1.1\r\nHost: example.com\r\n\r\n")
	resp := &Response{}

	err := client.doConnInternal(clientConn, req, resp)
	if err != nil {
		t.Fatalf("doConnInternal() error: %v", err)
	}

	// Verify ParseRawdata works (via accessor methods)
	if resp.StatusCode() != 200 {
		t.Errorf("StatusCode() = %d, want 200", resp.StatusCode())
	}

	if resp.Header("Content-Type") != "text/html" {
		t.Errorf("Header(Content-Type) = %q, want text/html", resp.Header("Content-Type"))
	}

	// HEAD response should have empty body
	if len(resp.Body()) != 0 {
		t.Errorf("Body() = %q, want empty for HEAD response", resp.Body())
	}
}

func TestDoConnInternal_HEAD_Chunked(t *testing.T) {
	// HEAD response with Transfer-Encoding: chunked but no body.
	client := &Client{
		TransformRequestFunc: PrepareRequest,
		Timeout:              2 * time.Second,
		QuietTimeout:         10 * time.Millisecond,
	}

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	go func() {
		br := bufio.NewReader(serverConn)
		for {
			line, err := br.ReadString('\n')
			if err != nil || strings.TrimSpace(line) == "" {
				break
			}
		}
		serverConn.Write([]byte("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n"))
		serverConn.Close()
	}()

	req := simpleTestRequest("HEAD / HTTP/1.1\r\nHost: example.com\r\n\r\n")
	resp := &Response{}

	err := client.doConnInternal(clientConn, req, resp)
	if err != nil {
		t.Fatalf("doConnInternal() error: %v", err)
	}

	if resp.StatusCode() != 200 {
		t.Errorf("StatusCode() = %d, want 200", resp.StatusCode())
	}

	if len(resp.Body()) != 0 {
		t.Errorf("Body() = %q, want empty for HEAD response", resp.Body())
	}
}

func TestDoConnInternal_SpecBased_ContentLength(t *testing.T) {
	// Verify that normal mode (ReadFull=false) reads exactly Content-Length bytes
	// and returns without timeout, even when the server keeps the connection open.
	client := &Client{
		TransformRequestFunc: PrepareRequest,
		Timeout:              2 * time.Second,
		QuietTimeout:         10 * time.Millisecond,
	}

	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()

	// Server: read request, write response with Content-Length, keep connection open
	go func() {
		br := bufio.NewReader(serverConn)
		for {
			line, err := br.ReadString('\n')
			if err != nil || strings.TrimSpace(line) == "" {
				break
			}
		}
		serverConn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 5\r\nConnection: keep-alive\r\n\r\nhello"))
		// Keep connection open — do NOT close
	}()

	req := simpleTestRequest("GET / HTTP/1.1\r\nHost: example.com\r\nConnection: keep-alive\r\n\r\n")
	resp := &Response{}

	start := time.Now()
	err := client.doConnInternal(clientConn, req, resp)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("doConnInternal() error: %v", err)
	}

	// Should complete quickly (well under Timeout) because CL reading is exact
	if elapsed > time.Second {
		t.Errorf("took %v, expected < 1s (should not wait for timeout)", elapsed)
	}

	// Verify response body
	if !strings.Contains(string(resp.Rawdata), "hello") {
		t.Errorf("resp.Rawdata = %q, want to contain 'hello'", resp.Rawdata)
	}
	if resp.StatusCode() != 200 {
		t.Errorf("StatusCode() = %d, want 200", resp.StatusCode())
	}
	if string(resp.Body()) != "hello" {
		t.Errorf("Body() = %q, want 'hello'", resp.Body())
	}

	// Timing metrics should be populated
	if resp.TimeToFirstByte == 0 {
		t.Error("TimeToFirstByte should be > 0")
	}
	if resp.TimeToLastByte == 0 {
		t.Error("TimeToLastByte should be > 0")
	}

	clientConn.Close()
}

func TestDoConnInternal_SpecBased_Chunked(t *testing.T) {
	// Verify normal mode correctly reads chunked body and returns
	// without waiting for timeout, even when connection stays open.
	client := &Client{
		TransformRequestFunc: PrepareRequest,
		Timeout:              2 * time.Second,
		QuietTimeout:         10 * time.Millisecond,
	}

	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()

	// Server: read request, write chunked response, keep connection open
	go func() {
		br := bufio.NewReader(serverConn)
		for {
			line, err := br.ReadString('\n')
			if err != nil || strings.TrimSpace(line) == "" {
				break
			}
		}
		chunkedResp := "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nConnection: keep-alive\r\n\r\n" +
			"5\r\nhello\r\n" +
			"6\r\n world\r\n" +
			"0\r\n\r\n"
		serverConn.Write([]byte(chunkedResp))
		// Keep connection open — do NOT close
	}()

	req := simpleTestRequest("GET / HTTP/1.1\r\nHost: example.com\r\nConnection: keep-alive\r\n\r\n")
	resp := &Response{}

	start := time.Now()
	err := client.doConnInternal(clientConn, req, resp)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("doConnInternal() error: %v", err)
	}

	// Should complete quickly (well under Timeout)
	if elapsed > time.Second {
		t.Errorf("took %v, expected < 1s (should not wait for timeout)", elapsed)
	}

	if resp.StatusCode() != 200 {
		t.Errorf("StatusCode() = %d, want 200", resp.StatusCode())
	}

	// Chunked body should be decoded: "hello" + " world" = "hello world"
	if string(resp.Body()) != "hello world" {
		t.Errorf("Body() = %q, want 'hello world'", resp.Body())
	}

	if resp.TimeToFirstByte == 0 {
		t.Error("TimeToFirstByte should be > 0")
	}
	if resp.TimeToLastByte == 0 {
		t.Error("TimeToLastByte should be > 0")
	}

	clientConn.Close()
}

func TestDoConnInternal_ReadFull(t *testing.T) {
	// Verify ReadFull mode reads beyond Content-Length and captures extra bytes.
	// This is used for security research (e.g., HTTP smuggling detection).
	client := &Client{
		TransformRequestFunc: PrepareRequest,
		Timeout:              2 * time.Second,
		QuietTimeout:         50 * time.Millisecond,
		ReadFull:             true,
	}

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	// Server: read request, write CL:5 body "hello" + extra smuggled bytes, then keep open
	go func() {
		br := bufio.NewReader(serverConn)
		for {
			line, err := br.ReadString('\n')
			if err != nil || strings.TrimSpace(line) == "" {
				break
			}
		}
		// Send response with CL:5 but extra data beyond it
		serverConn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhelloSMUGGLED"))
		// Keep connection open so ReadFull mode detects silence via QuietTimeout
	}()

	req := simpleTestRequest("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")
	resp := &Response{}

	err := client.doConnInternal(clientConn, req, resp)
	if err != nil {
		t.Fatalf("doConnInternal() error: %v", err)
	}

	// ReadFull should capture ALL bytes including beyond Content-Length
	rawdata := string(resp.Rawdata)
	if !strings.Contains(rawdata, "hello") {
		t.Errorf("resp.Rawdata should contain 'hello', got %q", rawdata)
	}
	if !strings.Contains(rawdata, "SMUGGLED") {
		t.Errorf("resp.Rawdata should contain 'SMUGGLED', got %q", rawdata)
	}

	// Timing metrics should be populated
	if resp.TimeToFirstByte == 0 {
		t.Error("TimeToFirstByte should be > 0")
	}
	if resp.TimeToLastByte == 0 {
		t.Error("TimeToLastByte should be > 0")
	}

	serverConn.Close()
}

func TestDoConnInternal_ReadFull_NoPool(t *testing.T) {
	// Verify ReadFull mode prevents connection pooling.
	pool := NewDefaultConnPool()
	client := &Client{
		TransformRequestFunc: PrepareRequest,
		Timeout:              2 * time.Second,
		QuietTimeout:         50 * time.Millisecond,
		pool:                 pool,
		ReadFull:             true,
	}

	clientConn, serverConn := net.Pipe()
	poolKey := "http://example.com:80"

	// Server: read request, write keep-alive response, keep conn open
	go func() {
		br := bufio.NewReader(serverConn)
		for {
			line, err := br.ReadString('\n')
			if err != nil || strings.TrimSpace(line) == "" {
				break
			}
		}
		serverConn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: keep-alive\r\n\r\nok"))
		// Keep connection open
	}()

	req := simpleTestRequest("GET / HTTP/1.1\r\nHost: example.com\r\nConnection: keep-alive\r\n\r\n")
	resp := &Response{}

	err := client.doConnWithPool(clientConn, req, resp, poolKey)
	if err != nil {
		t.Fatalf("doConnWithPool() error: %v", err)
	}

	// Connection should NOT be in pool when ReadFull=true
	if pool.LenForHost(poolKey) != 0 {
		t.Errorf("pool.LenForHost(%q) = %d, want 0 (ReadFull disables pooling)", poolKey, pool.LenForHost(poolKey))
	}

	pool.CloseAll()
	serverConn.Close()
}

func TestDoConnInternal_NoContentLength_KeepAlive(t *testing.T) {
	// Edge case: server sends response without Content-Length or chunked encoding
	// but with Connection: keep-alive. Normal mode should handle this correctly
	// (terminate via Timeout, not hang forever).
	client := &Client{
		TransformRequestFunc: PrepareRequest,
		Timeout:              200 * time.Millisecond,
		QuietTimeout:         10 * time.Millisecond,
	}

	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()

	// Server: send response without CL/chunked, keep connection open
	go func() {
		br := bufio.NewReader(serverConn)
		for {
			line, err := br.ReadString('\n')
			if err != nil || strings.TrimSpace(line) == "" {
				break
			}
		}
		serverConn.Write([]byte("HTTP/1.1 200 OK\r\nConnection: keep-alive\r\n\r\nsome body data"))
		// Keep connection open — no CL, no chunked, no EOF
	}()

	req := simpleTestRequest("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")
	resp := &Response{}

	start := time.Now()
	err := client.doConnInternal(clientConn, req, resp)
	elapsed := time.Since(start)

	// Should terminate (via Timeout deadline on conn), not hang forever.
	// The response may be returned as an error or as partial data.
	if elapsed > 2*time.Second {
		t.Fatalf("doConnInternal() took %v, expected to terminate within Timeout", elapsed)
	}

	// The response should contain the body data that was sent.
	// ReadResponsePartial's io.ReadAll hits the deadline, returns partial data + timeout error.
	// The timeout-with-partial-data path re-parses it as a complete response.
	if err == nil {
		// If no error, body should contain the data
		if !strings.Contains(string(resp.Rawdata), "some body data") {
			t.Errorf("resp.Rawdata = %q, want to contain 'some body data'", resp.Rawdata)
		}
	} else {
		// If error, Rawdata should still have partial data
		if len(resp.Rawdata) == 0 {
			t.Errorf("expected resp.Rawdata to contain partial data, got empty; err: %v", err)
		}
	}

	clientConn.Close()
}

package rawhttp

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"
)

// writeFailConn is a net.Conn that always fails on Write.
type writeFailConn struct {
	net.Conn
}

func (c *writeFailConn) Write(b []byte) (int, error) {
	return 0, errors.New("write failed")
}

func (c *writeFailConn) Close() error {
	if c.Conn != nil {
		return c.Conn.Close()
	}
	return nil
}

// writeFailDialer is a proxy.Dialer that returns a writeFailConn.
type writeFailDialer struct{}

func (d *writeFailDialer) Dial(network, addr string) (net.Conn, error) {
	client, server := net.Pipe()
	server.Close() // close server side; client side Write will still succeed on pipe
	return &writeFailConn{Conn: client}, nil
}

// ---------- readConnectResponse ----------

func TestReadConnectResponse_Success(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantCode int
	}{
		{
			name:     "200 OK",
			input:    "HTTP/1.1 200 Connection Established\r\nProxy-Agent: test\r\n\r\n",
			wantCode: 200,
		},
		{
			name:     "200 minimal headers",
			input:    "HTTP/1.1 200 OK\r\n\r\n",
			wantCode: 200,
		},
		{
			name:     "407 proxy auth required",
			input:    "HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic\r\n\r\n",
			wantCode: 407,
		},
		{
			name:     "403 forbidden",
			input:    "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n",
			wantCode: 403,
		},
		{
			name:     "503 service unavailable",
			input:    "HTTP/1.1 503 Service Unavailable\r\n\r\n",
			wantCode: 503,
		},
		{
			name:     "multiple headers",
			input:    "HTTP/1.1 200 OK\r\nX-Foo: bar\r\nX-Baz: qux\r\nConnection: keep-alive\r\n\r\n",
			wantCode: 200,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			br := bufio.NewReader(strings.NewReader(tt.input))
			code, err := readConnectResponse(br)
			if err != nil {
				t.Fatalf("readConnectResponse() error = %v", err)
			}
			if code != tt.wantCode {
				t.Errorf("readConnectResponse() code = %d, want %d", code, tt.wantCode)
			}
		})
	}
}

func TestReadConnectResponse_Errors(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantInErr string
	}{
		{
			name:      "empty input",
			input:     "",
			wantInErr: "reading status line",
		},
		{
			name:      "malformed status line no space",
			input:     "HTTP/1.1\r\n\r\n",
			wantInErr: "malformed status line",
		},
		{
			name:      "non-numeric status code",
			input:     "HTTP/1.1 abc OK\r\n\r\n",
			wantInErr: "invalid status code",
		},
		{
			name:      "truncated after status line",
			input:     "HTTP/1.1 200 OK\r\n",
			wantInErr: "reading header",
		},
		{
			name:      "truncated in middle of headers",
			input:     "HTTP/1.1 200 OK\r\nX-Foo: bar\r\n",
			wantInErr: "reading header",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			br := bufio.NewReader(strings.NewReader(tt.input))
			_, err := readConnectResponse(br)
			if err == nil {
				t.Fatal("readConnectResponse() expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantInErr) {
				t.Errorf("readConnectResponse() error = %q, want containing %q", err.Error(), tt.wantInErr)
			}
		})
	}
}

func TestReadConnectResponse_PreservesTunnelData(t *testing.T) {
	// After reading CONNECT response, remaining data should be available
	input := "HTTP/1.1 200 OK\r\n\r\nHello tunnel"
	br := bufio.NewReader(strings.NewReader(input))

	code, err := readConnectResponse(br)
	if err != nil {
		t.Fatalf("readConnectResponse() error = %v", err)
	}
	if code != 200 {
		t.Fatalf("readConnectResponse() code = %d, want 200", code)
	}

	// Read remaining data from the buffered reader
	remaining := make([]byte, 12)
	n, err := io.ReadFull(br, remaining)
	if err != nil {
		t.Fatalf("reading tunnel data: %v", err)
	}
	if string(remaining[:n]) != "Hello tunnel" {
		t.Errorf("remaining data = %q, want %q", remaining[:n], "Hello tunnel")
	}
}

// ---------- bufferedConn ----------

func TestBufferedConn_Read(t *testing.T) {
	// Create a pipe: write side feeds data, read side is wrapped in bufferedConn
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	// Write data from server side
	go func() {
		server.Write([]byte("hello world"))
		server.Close()
	}()

	br := bufio.NewReader(client)
	bc := &bufferedConn{Conn: client, reader: br}

	buf := make([]byte, 5)
	n, err := bc.Read(buf)
	if err != nil {
		t.Fatalf("bufferedConn.Read() error = %v", err)
	}
	if string(buf[:n]) != "hello" {
		t.Errorf("bufferedConn.Read() = %q, want %q", buf[:n], "hello")
	}

	// Read the rest
	buf2 := make([]byte, 10)
	n2, err := bc.Read(buf2)
	if err != nil {
		t.Fatalf("bufferedConn.Read() second call error = %v", err)
	}
	if string(buf2[:n2]) != " world" {
		t.Errorf("bufferedConn.Read() = %q, want %q", buf2[:n2], " world")
	}
}

func TestBufferedConn_ReadFromBufferedData(t *testing.T) {
	// Simulate what happens in httpProxy.Dial: the bufio.Reader may have
	// buffered data beyond the CONNECT response headers.
	input := "HTTP/1.1 200 OK\r\n\r\ntunnel payload data"
	client, server := net.Pipe()
	defer server.Close()

	go func() {
		client.Write([]byte(input))
		client.Close()
	}()

	br := bufio.NewReader(server)

	// Consume the CONNECT response (this may buffer extra data)
	code, err := readConnectResponse(br)
	if err != nil {
		t.Fatalf("readConnectResponse() error = %v", err)
	}
	if code != 200 {
		t.Fatalf("code = %d, want 200", code)
	}

	// Wrap in bufferedConn — the remaining data should be readable
	bc := &bufferedConn{Conn: server, reader: br}

	buf := make([]byte, 100)
	n, err := bc.Read(buf)
	if err != nil {
		t.Fatalf("bufferedConn.Read() error = %v", err)
	}
	if string(buf[:n]) != "tunnel payload data" {
		t.Errorf("bufferedConn.Read() = %q, want %q", buf[:n], "tunnel payload data")
	}
}

// ---------- newHTTPProxy ----------

func TestNewHTTPProxy_NoAuth(t *testing.T) {
	u, _ := url.Parse("http://proxy.example.com:8080")
	forward := httpDialer{Timeout: 5 * time.Second}

	dialer, err := newHTTPProxy(u, forward)
	if err != nil {
		t.Fatalf("newHTTPProxy() error = %v", err)
	}
	if dialer == nil {
		t.Fatal("newHTTPProxy() returned nil dialer")
	}

	p, ok := dialer.(*httpProxy)
	if !ok {
		t.Fatal("newHTTPProxy() did not return *httpProxy")
	}

	if p.host != "proxy.example.com:8080" {
		t.Errorf("host = %q, want %q", p.host, "proxy.example.com:8080")
	}
	if p.haveAuth {
		t.Error("haveAuth should be false when no user info")
	}
	if p.username != "" {
		t.Errorf("username = %q, want empty", p.username)
	}
	if p.password != "" {
		t.Errorf("password = %q, want empty", p.password)
	}
}

func TestNewHTTPProxy_WithAuth(t *testing.T) {
	u, _ := url.Parse("http://user:pass@proxy.example.com:8080")
	forward := httpDialer{Timeout: 5 * time.Second}

	dialer, err := newHTTPProxy(u, forward)
	if err != nil {
		t.Fatalf("newHTTPProxy() error = %v", err)
	}

	p, ok := dialer.(*httpProxy)
	if !ok {
		t.Fatal("newHTTPProxy() did not return *httpProxy")
	}

	if !p.haveAuth {
		t.Error("haveAuth should be true")
	}
	if p.username != "user" {
		t.Errorf("username = %q, want %q", p.username, "user")
	}
	if p.password != "pass" {
		t.Errorf("password = %q, want %q", p.password, "pass")
	}
}

func TestNewHTTPProxy_UsernameOnly(t *testing.T) {
	u, _ := url.Parse("http://onlyuser@proxy.example.com:8080")
	forward := httpDialer{Timeout: 5 * time.Second}

	dialer, err := newHTTPProxy(u, forward)
	if err != nil {
		t.Fatalf("newHTTPProxy() error = %v", err)
	}

	p := dialer.(*httpProxy)
	if !p.haveAuth {
		t.Error("haveAuth should be true")
	}
	if p.username != "onlyuser" {
		t.Errorf("username = %q, want %q", p.username, "onlyuser")
	}
	if p.password != "" {
		t.Errorf("password = %q, want empty", p.password)
	}
}

// ---------- httpProxy.Dial ----------

func TestProxyDial_Success(t *testing.T) {
	// Start a mock CONNECT proxy server
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		br := bufio.NewReader(conn)
		// Read the CONNECT request line
		line, _ := br.ReadString('\n')
		if !strings.HasPrefix(line, "CONNECT target.example.com:443") {
			return
		}
		// Drain remaining headers
		for {
			hdr, _ := br.ReadString('\n')
			if strings.TrimSpace(hdr) == "" {
				break
			}
		}
		// Send 200 response
		fmt.Fprint(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		// Echo back anything received through the tunnel
		io.Copy(conn, conn)
	}()

	p := &httpProxy{
		host:    ln.Addr().String(),
		forward: httpDialer{Timeout: 5 * time.Second},
	}

	conn, err := p.Dial("tcp", "target.example.com:443")
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer conn.Close()

	// Verify the connection works (tunnel established)
	_, err = conn.Write([]byte("ping"))
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	buf := make([]byte, 4)
	n, err := io.ReadFull(conn, buf)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if string(buf[:n]) != "ping" {
		t.Errorf("echo = %q, want %q", buf[:n], "ping")
	}
}

func TestProxyDial_WithAuth(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer ln.Close()

	var receivedAuth string

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		br := bufio.NewReader(conn)
		// Read CONNECT request
		br.ReadString('\n')
		// Read headers looking for Proxy-Authorization
		for {
			hdr, _ := br.ReadString('\n')
			trimmed := strings.TrimSpace(hdr)
			if trimmed == "" {
				break
			}
			if strings.HasPrefix(trimmed, "Proxy-Authorization:") {
				receivedAuth = trimmed
			}
		}
		fmt.Fprint(conn, "HTTP/1.1 200 OK\r\n\r\n")
	}()

	p := &httpProxy{
		host:     ln.Addr().String(),
		haveAuth: true,
		username: "testuser",
		password: "testpass",
		forward:  httpDialer{Timeout: 5 * time.Second},
	}

	conn, err := p.Dial("tcp", "target.example.com:443")
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	conn.Close()

	// Wait a moment for the goroutine to capture auth
	time.Sleep(50 * time.Millisecond)

	expectedCreds := base64.StdEncoding.EncodeToString([]byte("testuser:testpass"))
	expectedHeader := "Proxy-Authorization: Basic " + expectedCreds
	if receivedAuth != expectedHeader {
		t.Errorf("auth header = %q, want %q", receivedAuth, expectedHeader)
	}
}

func TestProxyDial_ForwardDialError(t *testing.T) {
	// Use an address that won't connect
	p := &httpProxy{
		host:    "127.0.0.1:1", // port 1 should refuse
		forward: httpDialer{Timeout: 100 * time.Millisecond},
	}

	conn, err := p.Dial("tcp", "target.example.com:443")
	if err == nil {
		conn.Close()
		t.Fatal("Dial() expected error for unreachable proxy, got nil")
	}
}

func TestProxyDial_NonOKStatusCode(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		br := bufio.NewReader(conn)
		// Drain request
		for {
			hdr, _ := br.ReadString('\n')
			if strings.TrimSpace(hdr) == "" {
				break
			}
		}
		fmt.Fprint(conn, "HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic\r\n\r\n")
	}()

	p := &httpProxy{
		host:    ln.Addr().String(),
		forward: httpDialer{Timeout: 5 * time.Second},
	}

	conn, err := p.Dial("tcp", "target.example.com:443")
	if err == nil {
		conn.Close()
		t.Fatal("Dial() expected error for 407, got nil")
	}
	if !strings.Contains(err.Error(), "407") {
		t.Errorf("error = %q, want containing '407'", err.Error())
	}
}

func TestProxyDial_WriteError(t *testing.T) {
	// Use a custom dialer that returns a conn which fails on Write
	p := &httpProxy{
		host:    "proxy.example.com:8080",
		forward: &writeFailDialer{},
	}

	conn, err := p.Dial("tcp", "target.example.com:443")
	if err == nil {
		conn.Close()
		t.Fatal("Dial() expected error for write failure, got nil")
	}
}

func TestProxyDial_BadResponseFromProxy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		br := bufio.NewReader(conn)
		// Drain request
		for {
			hdr, _ := br.ReadString('\n')
			if strings.TrimSpace(hdr) == "" {
				break
			}
		}
		// Send malformed response
		fmt.Fprint(conn, "GARBAGE\r\n\r\n")
	}()

	p := &httpProxy{
		host:    ln.Addr().String(),
		forward: httpDialer{Timeout: 5 * time.Second},
	}

	conn, err := p.Dial("tcp", "target.example.com:443")
	if err == nil {
		conn.Close()
		t.Fatal("Dial() expected error for malformed proxy response, got nil")
	}
}

// ---------- ProxyFromURL ----------

func TestProxyFromURL(t *testing.T) {
	// ProxyFromURL delegates to proxy.FromURL. Ensure it returns
	// a dialer for a registered scheme without error.
	u, _ := url.Parse("socks5://127.0.0.1:1080")
	forward := httpDialer{Timeout: 5 * time.Second}

	dialer, err := ProxyFromURL(u, forward)
	if err != nil {
		t.Fatalf("ProxyFromURL() error = %v", err)
	}
	if dialer == nil {
		t.Fatal("ProxyFromURL() returned nil dialer")
	}
}

func TestProxyFromURL_HTTP(t *testing.T) {
	// HTTP scheme is registered via proxy.RegisterDialerType in client.go
	// but only once via sync.Once. Trigger registration first.
	client := NewDefaultClient()
	regURL, _ := url.Parse("http://127.0.0.1:9999")
	client.SetProxy(regURL)
	client.Close()

	u, _ := url.Parse("http://proxy.example.com:8080")
	forward := httpDialer{Timeout: 5 * time.Second}

	dialer, err := ProxyFromURL(u, forward)
	if err != nil {
		t.Fatalf("ProxyFromURL() error = %v", err)
	}
	if dialer == nil {
		t.Fatal("ProxyFromURL() returned nil dialer")
	}
}

// ---------- httpDialer ----------

func TestHTTPDialer_Dial(t *testing.T) {
	// Start a local TCP listener
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		conn.Write([]byte("hello"))
		conn.Close()
	}()

	d := httpDialer{Timeout: 5 * time.Second}
	conn, err := d.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("httpDialer.Dial() error = %v", err)
	}
	defer conn.Close()

	buf := make([]byte, 5)
	n, err := io.ReadFull(conn, buf)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if string(buf[:n]) != "hello" {
		t.Errorf("got %q, want %q", buf[:n], "hello")
	}
}

func TestHTTPDialer_DialTimeout(t *testing.T) {
	// Use a non-routable address to trigger timeout
	d := httpDialer{Timeout: 50 * time.Millisecond}
	_, err := d.Dial("tcp", "192.0.2.1:12345") // RFC 5737 TEST-NET-1, non-routable
	if err == nil {
		t.Fatal("httpDialer.Dial() expected timeout error, got nil")
	}
}

func TestHTTPDialer_DialRefused(t *testing.T) {
	d := httpDialer{Timeout: 1 * time.Second}
	_, err := d.Dial("tcp", "127.0.0.1:1") // port 1 should be refused
	if err == nil {
		t.Fatal("httpDialer.Dial() expected error for refused connection, got nil")
	}
}

// ---------- httpsDialer ----------

func TestHTTPSDialer_DialRefused(t *testing.T) {
	d := httpsDialer{Timeout: 1 * time.Second}
	_, err := d.Dial("tcp", "127.0.0.1:1")
	if err == nil {
		t.Fatal("httpsDialer.Dial() expected error for refused connection, got nil")
	}
}

func TestHTTPSDialer_DialTimeout(t *testing.T) {
	d := httpsDialer{Timeout: 50 * time.Millisecond}
	_, err := d.Dial("tcp", "192.0.2.1:12345")
	if err == nil {
		t.Fatal("httpsDialer.Dial() expected timeout error, got nil")
	}
}

// ---------- integration: full CONNECT flow ----------

func TestProxyDial_FullTunnelIntegration(t *testing.T) {
	// 1. Start a target server
	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("target listener error: %v", err)
	}
	defer targetLn.Close()

	go func() {
		conn, err := targetLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Echo with prefix
		buf := make([]byte, 1024)
		n, _ := conn.Read(buf)
		conn.Write(append([]byte("echo:"), buf[:n]...))
	}()

	// 2. Start a proxy server that actually forwards to the target
	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listener error: %v", err)
	}
	defer proxyLn.Close()

	go func() {
		conn, err := proxyLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		br := bufio.NewReader(conn)
		// Read CONNECT request
		reqLine, _ := br.ReadString('\n')
		parts := strings.Fields(reqLine)
		if len(parts) < 2 || parts[0] != "CONNECT" {
			return
		}
		targetAddr := parts[1]

		// Drain headers
		for {
			hdr, _ := br.ReadString('\n')
			if strings.TrimSpace(hdr) == "" {
				break
			}
		}

		// Connect to target
		targetConn, err := net.DialTimeout("tcp", targetAddr, 5*time.Second)
		if err != nil {
			fmt.Fprintf(conn, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
			return
		}
		defer targetConn.Close()

		fmt.Fprintf(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")

		// Bidirectional copy
		done := make(chan struct{})
		go func() {
			io.Copy(targetConn, br) // Use br to capture any buffered data
			done <- struct{}{}
		}()
		go func() {
			io.Copy(conn, targetConn)
			done <- struct{}{}
		}()
		<-done
	}()

	// 3. Use httpProxy to dial through the proxy
	p := &httpProxy{
		host:    proxyLn.Addr().String(),
		forward: httpDialer{Timeout: 5 * time.Second},
	}

	conn, err := p.Dial("tcp", targetLn.Addr().String())
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer conn.Close()

	// Send data through the tunnel
	_, err = conn.Write([]byte("test"))
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	buf := make([]byte, 100)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if string(buf[:n]) != "echo:test" {
		t.Errorf("tunnel response = %q, want %q", buf[:n], "echo:test")
	}
}

func TestProxyDial_ReturnsBufferedConn(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		br := bufio.NewReader(conn)
		// Drain request
		for {
			hdr, _ := br.ReadString('\n')
			if strings.TrimSpace(hdr) == "" {
				break
			}
		}
		fmt.Fprint(conn, "HTTP/1.1 200 OK\r\n\r\n")
	}()

	p := &httpProxy{
		host:    ln.Addr().String(),
		forward: httpDialer{Timeout: 5 * time.Second},
	}

	conn, err := p.Dial("tcp", "target.example.com:443")
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer conn.Close()

	// Verify returned connection is a *bufferedConn
	_, ok := conn.(*bufferedConn)
	if !ok {
		t.Errorf("Dial() returned %T, want *bufferedConn", conn)
	}
}

func TestProxyDial_ConnectRequestFormat(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer ln.Close()

	var receivedRequest string

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		br := bufio.NewReader(conn)
		// Read the full CONNECT request
		var sb strings.Builder
		for {
			line, _ := br.ReadString('\n')
			sb.WriteString(line)
			if strings.TrimSpace(line) == "" {
				break
			}
		}
		receivedRequest = sb.String()

		fmt.Fprint(conn, "HTTP/1.1 200 OK\r\n\r\n")
	}()

	p := &httpProxy{
		host:    ln.Addr().String(),
		forward: httpDialer{Timeout: 5 * time.Second},
	}

	conn, err := p.Dial("tcp", "target.example.com:443")
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	conn.Close()

	time.Sleep(50 * time.Millisecond)

	// Verify the CONNECT request format
	if !strings.HasPrefix(receivedRequest, "CONNECT target.example.com:443 HTTP/1.1\r\n") {
		t.Errorf("request line = %q, want prefix 'CONNECT target.example.com:443 HTTP/1.1\\r\\n'", receivedRequest)
	}
	if !strings.Contains(receivedRequest, "Host: target.example.com:443\r\n") {
		t.Errorf("request missing Host header, got: %q", receivedRequest)
	}
	if !strings.Contains(receivedRequest, "User-Agent: rawhttp.0.1\r\n") {
		t.Errorf("request missing User-Agent header, got: %q", receivedRequest)
	}
	if strings.Contains(receivedRequest, "Proxy-Authorization") {
		t.Errorf("request should not have Proxy-Authorization without auth, got: %q", receivedRequest)
	}
}

package rawhttp

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

var (
	InvalidURLError     = fmt.Errorf("Invalid URL")
	InvalidRequestError = fmt.Errorf("Invalid Request")
)

type ReadWriteCloseDeadliner interface {
	io.ReadWriteCloser
	SetReadDeadline(time.Time) error
}

type Client struct {
	TransformRequestFunc func(*Request)
	Timeout              time.Duration
	proxyURI             *url.URL

	// Connection pooling
	pool             *ConnPool
	DisableKeepAlive bool

	// QuietTimeout is the duration to wait after receiving data before
	// considering the response complete. This helps detect smuggled responses
	// and ensures full reads. Default: 2 seconds.
	// The read loop resets this timer each time data is received.
	// Total read time is still bounded by Timeout.
	QuietTimeout time.Duration
}

const (
	DefaultQuietTimeout = 10 * time.Millisecond
)

func (obj *Client) SetProxy(u *url.URL) {
	proxy.RegisterDialerType("http", newHTTPProxy)
	proxy.RegisterDialerType("https", newHTTPProxy)
	obj.proxyURI = u
}

func NewDefaultClient() *Client {
	return &Client{
		TransformRequestFunc: PrepareRequest,
		Timeout:              time.Second * 10,
		QuietTimeout:         DefaultQuietTimeout,
		pool:                 NewDefaultConnPool(),
	}
}

func NewClientTransferVariables() *Client {
	return &Client{
		TransformRequestFunc: PrepareRequestVariables,
		Timeout:              time.Second * 10,
		QuietTimeout:         DefaultQuietTimeout,
		pool:                 NewDefaultConnPool(),
	}
}

func NewDefaultClientTimeout(d time.Duration) *Client {
	return &Client{
		TransformRequestFunc: PrepareRequest,
		Timeout:              d,
		QuietTimeout:         DefaultQuietTimeout,
		pool:                 NewDefaultConnPool(),
	}
}

// NewClientWithPool creates a new client with a custom connection pool.
// If pool is nil, a default pool is created.
func NewClientWithPool(pool *ConnPool) *Client {
	if pool == nil {
		pool = NewDefaultConnPool()
	}
	return &Client{
		TransformRequestFunc: PrepareRequest,
		Timeout:              time.Second * 10,
		QuietTimeout:         DefaultQuietTimeout,
		pool:                 pool,
	}
}

// CloseIdleConnections closes all idle connections in the pool.
func (obj *Client) CloseIdleConnections() {
	if obj.pool != nil {
		obj.pool.CloseIdle()
	}
}

// Close closes all connections and shuts down the client's connection pool.
func (obj *Client) Close() {
	if obj.pool != nil {
		obj.pool.CloseAll()
	}
}

func (obj *Client) Do(req *Request, resp *Response) error {
	var err error
	req.URI, err = url.Parse(req.URL)
	if err != nil {
		return err
	}
	if !req.URI.IsAbs() {
		return InvalidURLError
	}
	req.ParseRawdata()
	obj.TransformRequestFunc(req)
	if bytes.HasPrefix(req.Rawdata, []byte("CONNECT ")) {
		return obj.DoProxy(req, resp)
	}

	if obj.proxyURI != nil {
		return obj.DoWithProxy(req, resp)
	}

	switch req.URI.Scheme {
	case "https":
		return obj.DoHTTPS(req, resp)
	case "http":
		return obj.DoHTTP(req, resp)
	default:
		return InvalidURLError
	}
}

func (obj *Client) httpDialer() proxy.Dialer {
	return httpDialer{
		Timeout: obj.Timeout,
	}
}

func (obj *Client) httpsDialer() proxy.Dialer {
	return httpsDialer{
		Timeout: obj.Timeout,
	}
}

func (obj *Client) DoWithProxy(req *Request, resp *Response) error {
	port := req.URI.Port()

	if req.URI.Scheme == "https" {
		if port == "" {
			port = "443"
		}
	} else {
		if port == "" {
			port = "80"
		}
	}
	forward := obj.httpDialer()

	proxy, err := ProxyFromURL(obj.proxyURI, forward)
	if err != nil {
		return fmt.Errorf("ProxyFromURL error: %w", err)
	}

	conn, err := proxy.Dial("tcp", req.Addr(port))
	if err != nil {
		return err
	}

	if req.URI.Scheme == "https" {
		tlsConn := tls.Client(conn, &tls.Config{
			InsecureSkipVerify: true,
		})
		return obj.DoConn(tlsConn, req, resp)
	}
	return obj.DoConn(conn, req, resp)
}

func (obj *Client) DoHTTPS(req *Request, resp *Response) error {
	port := req.URI.Port()
	if port == "" {
		port = "443"
	}

	poolKey := PoolKey("https", req.URI.Hostname(), port)

	// Try pooled connection first
	if obj.pool != nil && !obj.DisableKeepAlive {
		if conn := obj.pool.Get(poolKey); conn != nil {
			err := obj.doConnWithPool(conn, req, resp, poolKey)
			if err == nil {
				return nil
			}
			// If stale connection error, close and retry with fresh connection
			if isStaleConnError(err) {
				conn.Close()
				resp.Reset()
				// Fall through to dial fresh connection
			} else {
				return err // Real error, don't retry
			}
		}
	}

	// Dial fresh connection
	conn, err := obj.httpsDialer().Dial("tcp", req.Addr(port))
	if err != nil {
		return err
	}
	return obj.doConnWithPool(conn, req, resp, poolKey)
}

func (obj *Client) DoHTTP(req *Request, resp *Response) error {
	port := req.URI.Port()
	if port == "" {
		port = "80"
	}

	poolKey := PoolKey("http", req.URI.Hostname(), port)

	// Try pooled connection first
	if obj.pool != nil && !obj.DisableKeepAlive {
		if conn := obj.pool.Get(poolKey); conn != nil {
			err := obj.doConnWithPool(conn, req, resp, poolKey)
			if err == nil {
				return nil
			}
			// If stale connection error, close and retry with fresh connection
			if isStaleConnError(err) {
				conn.Close()
				resp.Reset()
				// Fall through to dial fresh connection
			} else {
				return err // Real error, don't retry
			}
		}
	}

	// Dial fresh connection
	conn, err := obj.httpDialer().Dial("tcp", req.Addr(port))
	if err != nil {
		return err
	}
	return obj.doConnWithPool(conn, req, resp, poolKey)
}

func (obj *Client) DoProxy(req *Request, resp *Response) error {
	parts := bytes.Split(req.Rawdata, []byte("\r\n\r\n"))
	if len(parts) < 2 {
		return InvalidRequestError
	}

	req.Rawdata = append(parts[0], []byte("\r\n\r\n")...)
	port := req.URI.Port()
	if port == "" {
		if req.URI.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	var conn net.Conn
	var err error
	if req.URI.Scheme == "https" {
		dialer := &net.Dialer{Timeout: obj.Timeout}
		conn, err = tls.DialWithDialer(dialer, "tcp", req.Addr(port), &tls.Config{
			InsecureSkipVerify: true,
		})
		if err != nil {
			return err
		}
	} else {
		conn, err = net.DialTimeout("tcp", req.Addr(port), obj.Timeout)
		if err != nil {
			return err
		}
	}
	if _, err := conn.Write(req.Rawdata); err != nil {
		return err
	}
	buf := make([]byte, 1<<21) // 2Mb
	n, err := conn.Read(buf)
	if err != nil && err != io.EOF {
		return err
	}
	if !bytes.Contains(buf, []byte("200")) {
		return fmt.Errorf("can not connect to proxy. resp: %q", buf[:n])
	}
	req.Rawdata = bytes.Join(parts[1:], []byte("\r\n"))
	return obj.DoConn(conn, req, resp)
}

// DoConn performs the HTTP request on the given connection and always closes it.
// This method is kept for backward compatibility and for cases where connection
// reuse is not desired (e.g., proxy connections).
func (obj *Client) DoConn(conn net.Conn, req *Request, resp *Response) error {
	defer conn.Close()
	return obj.doConnInternal(conn, req, resp)
}

// doConnWithPool performs the HTTP request and manages connection pooling.
// The connection will be returned to the pool if reusable, otherwise closed.
func (obj *Client) doConnWithPool(conn net.Conn, req *Request, resp *Response, poolKey string) error {
	err := obj.doConnInternal(conn, req, resp)

	// Determine if we can reuse the connection
	canReuse := err == nil &&
		obj.pool != nil &&
		!obj.DisableKeepAlive &&
		!req.WantsClose() &&
		!req.WantsUpgrade() &&
		!resp.ConnectionClose()

	if canReuse {
		if !obj.pool.Put(poolKey, conn) {
			conn.Close()
		}
	} else {
		conn.Close()
	}

	return err
}

// doConnInternal performs the actual HTTP request/response exchange.
// It uses a two-phase timeout approach:
//  1. Wait up to Timeout for the first response data
//  2. After receiving data, use QuietTimeout to detect end of response
//     (wait for silence before considering response complete)
//
// This helps detect smuggled responses and ensures all data is captured.
// If EOF is received without any data, it returns io.EOF as an error
// (indicating a stale/closed connection rather than a valid empty response).
func (obj *Client) doConnInternal(conn net.Conn, req *Request, resp *Response) error {
	// fmt.Printf("===DEBUG=== RAW:\n%q\n", req.Bytes())
	if _, err := conn.Write(req.Bytes()); err != nil {
		return err
	}

	writeTime := time.Now() // Start timing after write completes

	quietTimeout := obj.QuietTimeout
	if quietTimeout == 0 {
		quietTimeout = DefaultQuietTimeout
	}

	absoluteDeadline := time.Now().Add(obj.Timeout)
	buf := make([]byte, 4096)
	receivedData := false

	for {
		var readDeadline time.Time

		if !receivedData {
			// Phase 1: Waiting for first data - use absolute deadline (Timeout)
			readDeadline = absoluteDeadline
		} else {
			// Phase 2: Already received data - use QuietTimeout for silence detection
			// but still respect the absolute deadline
			readDeadline = time.Now().Add(quietTimeout)
			if readDeadline.After(absoluteDeadline) {
				readDeadline = absoluteDeadline
			}
		}
		conn.SetReadDeadline(readDeadline)

		n, err := conn.Read(buf)
		if n > 0 {
			now := time.Now()
			if !receivedData {
				resp.TimeToFirstByte = now.Sub(writeTime)
			}
			resp.TimeToLastByte = now.Sub(writeTime)

			receivedData = true
			// fmt.Printf("===REC===: %q\n", buf[:n])
			resp.Rawdata = append(resp.Rawdata, buf[:n]...)
			// Data received - continue reading (quiet timer resets on next iteration)
			continue
		}

		if err != nil {
			// Timeout handling
			if isTimeoutError(err) {
				if !receivedData {
					// Phase 1 timeout - no response within Timeout
					return err
				}
				// Phase 2 timeout (QuietTimeout) - response complete
				return nil
			}

			// EOF handling
			if err == io.EOF {
				if receivedData {
					// Got data then EOF - valid response completion
					return nil
				}
				// EOF with no data - connection was closed (stale connection)
				return err
			}

			if strings.HasSuffix(err.Error(), "tls: user canceled") {
				return nil
			}
			return err
		}
	}
}

// isTimeoutError checks if the error is a network timeout error.
func isTimeoutError(err error) bool {
	if netErr, ok := err.(net.Error); ok {
		return netErr.Timeout()
	}
	return false
}

// isStaleConnError returns true if the error indicates a stale/closed connection
// that may have been valid when pooled but is no longer usable.
// This helps detect connections closed by the server due to keep-alive timeout.
func isStaleConnError(err error) bool {
	if err == nil {
		return false
	}
	if err == io.EOF {
		return true
	}
	errStr := err.Error()
	return strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "use of closed network connection")
}

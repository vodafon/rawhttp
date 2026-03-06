package rawhttp

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

type timingReader struct {
	conn           net.Conn
	firstByteTime  time.Time
	lastByteTime   time.Time
	writeEndTime   time.Time
	gotFirstByte   bool
}

func (tr *timingReader) Read(p []byte) (n int, err error) {
	n, err = tr.conn.Read(p)
	if n > 0 {
		if !tr.gotFirstByte {
			tr.firstByteTime = time.Now()
			tr.gotFirstByte = true
		}
		tr.lastByteTime = time.Now()
	}
	return n, err
}

func (tr *timingReader) TTFB() time.Duration {
	if !tr.gotFirstByte {
		return 0
	}
	return tr.firstByteTime.Sub(tr.writeEndTime)
}

func (tr *timingReader) TTLB() time.Duration {
	if !tr.gotFirstByte {
		return 0
	}
	return tr.lastByteTime.Sub(tr.writeEndTime)
}

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
	// Only used when ReadFull is true.
	QuietTimeout time.Duration

	// ReadFull enables the legacy two-phase timeout-based byte accumulation mode.
	// When true, uses Timeout for first byte and QuietTimeout for silence detection.
	// When false (default), uses spec-based ReadResponse for structured reading.
	// ReadFull disables connection pooling (connections are always closed after use).
	ReadFull bool
}

const (
	DefaultQuietTimeout = 10 * time.Millisecond
)

var registerProxyOnce sync.Once

func (obj *Client) SetProxy(u *url.URL) {
	registerProxyOnce.Do(func() {
		proxy.RegisterDialerType("http", newHTTPProxy)
		proxy.RegisterDialerType("https", newHTTPProxy)
	})
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
			ServerName:         req.URI.Hostname(),
		})
		if err := tlsConn.Handshake(); err != nil {
			conn.Close()
			return fmt.Errorf("TLS handshake through proxy error: %w", err)
		}
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
		!obj.ReadFull &&
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
// When ReadFull is true, uses legacy two-phase timeout-based byte accumulation.
// When ReadFull is false (default), uses spec-based ReadResponse for structured reading.
// With timingReader for TTFB/TTLB metrics in both paths.
//
// For responses without Content-Length and without chunked encoding,
// the spec-based ReadResponsePartial calls io.ReadAll which blocks on keep-alive connections.
// The conn.SetReadDeadline covers this — when the deadline fires, io.ReadAll
// returns a timeout error. If partial data was received, we treat the timeout
// as a complete response (same as the QuietTimeout behavior for no-CL case).
//
// If EOF is received without any data, it returns io.EOF as an error
// (indicating a stale/closed connection rather than a valid empty response).
func (obj *Client) doConnInternal(conn net.Conn, req *Request, resp *Response) error {
	resp.req = req // Store request for HEAD-aware response parsing
	if _, err := conn.Write(req.Bytes()); err != nil {
		return err
	}
	
	// Create timingReader wrapping the connection
	tr := &timingReader{
		conn:         conn,
		writeEndTime: time.Now(),
	}
	
	if obj.ReadFull {
		// Legacy two-phase timeout-based byte accumulation mode
		return obj.doConnInternal_ReadFull(conn, tr, req, resp)
	}
	
	// Default: spec-based ReadResponse path
	return obj.doConnInternal_ReadSpec(conn, tr, req, resp)
}

// doConnInternal_ReadFull handles the legacy two-phase timeout-based byte accumulation.
// Phase 1: Timeout for first byte. Phase 2: QuietTimeout for silence detection.
func (obj *Client) doConnInternal_ReadFull(conn net.Conn, tr *timingReader, req *Request, resp *Response) error {
	// Set initial timeout for first byte
	conn.SetReadDeadline(time.Now().Add(obj.Timeout))
	
	buf := make([]byte, 0, 256*1024) // Start with 256KB capacity
	
	for {
		tmp := make([]byte, 4096)
		n, err := conn.Read(tmp)
		
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			
			// After first byte, switch to QuietTimeout for silence detection
			conn.SetReadDeadline(time.Now().Add(obj.QuietTimeout))
		}
		
		if err != nil {
			if isTimeoutError(err) {
				// Timeout is expected after silence period or first byte delay
				if len(buf) > 0 {
					break // We have data, consider response complete
				}
				// Timeout with no data — return error
				return err
			}
			if err == io.EOF {
				if len(buf) == 0 {
					return io.EOF // Stale connection
				}
				break // We have data, consider complete
			}
			return err
		}
	}
	
	resp.Rawdata = buf
	resp.TimeToFirstByte = tr.TTFB()
	resp.TimeToLastByte = tr.TTLB()
	
	return nil
}
// doConnInternal_ReadSpec handles spec-based response reading using ReadResponsePartial.
func (obj *Client) doConnInternal_ReadSpec(conn net.Conn, tr *timingReader, req *Request, resp *Response) error {
	// Set read deadline for the entire response read (first byte + body).
	// This also prevents io.ReadAll from blocking indefinitely on keep-alive
	// connections that have no Content-Length or chunked encoding.
	conn.SetReadDeadline(time.Now().Add(obj.Timeout))
	
	br := bufio.NewReader(tr)
	
	resp2, partial, err := ReadResponsePartial(br, req)
	if err != nil {
		// EOF with no data — stale connection
		if err == io.EOF {
			return io.EOF
		}
		
		// Timeout or other error with no data — return the error
		if partial == nil && resp2 == nil {
			return err
		}
		
		// Error with partial data: check if it's a timeout with enough data
		// to be a complete response (e.g., io.ReadAll timeout on no-CL response).
		// If partial data contains a complete status line + headers, treat as success.
		if isTimeoutError(err) && len(partial) > 0 && bytes.Contains(partial, []byte("\r\n\r\n")) {
			// Timeout with partial data that has complete headers — treat as complete response.
			// Re-parse the partial data as a complete response.
			resp2, _, rerr := ReadResponsePartial(bufio.NewReader(bytes.NewReader(partial)), req)
			if rerr == nil && resp2 != nil {
				// Successfully parsed partial data — fall through to copy fields below
			} else {
				// Couldn't re-parse; store partial data and return original error
				resp.Rawdata = partial
				resp.TimeToFirstByte = tr.TTFB()
				resp.TimeToLastByte = tr.TTLB()
				return err
			}
		} else if partial != nil {
			// Non-timeout error with partial data — store and return error
			resp.Rawdata = partial
			resp.TimeToFirstByte = tr.TTFB()
			resp.TimeToLastByte = tr.TTLB()
			return err
		} else {
			return err
		}
	}
	
	// Copy fields from resp2 into the caller's resp
	resp.Rawdata = resp2.Rawdata
	resp.httpLine = resp2.httpLine
	resp.statusCode = resp2.statusCode
	resp.preBody = resp2.preBody
	resp.body = resp2.body
	resp.parsed = false // CRITICAL: false so ParseRawdata() can trigger decompression later
	
	// Populate timing from timingReader
	resp.TimeToFirstByte = tr.TTFB()
	resp.TimeToLastByte = tr.TTLB()
	
	return nil
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

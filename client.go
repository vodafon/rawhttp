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
}

func (obj *Client) SetProxy(u *url.URL) {
	proxy.RegisterDialerType("http", newHTTPProxy)
	proxy.RegisterDialerType("https", newHTTPProxy)
	obj.proxyURI = u
}

func NewDefaultClient() *Client {
	return &Client{
		TransformRequestFunc: PrepareRequest,
		Timeout:              time.Second * 10,
		pool:                 NewDefaultConnPool(),
	}
}

func NewClientTransferVariables() *Client {
	return &Client{
		TransformRequestFunc: PrepareRequestVariables,
		Timeout:              time.Second * 10,
		pool:                 NewDefaultConnPool(),
	}
}

func NewDefaultClientTimeout(d time.Duration) *Client {
	return &Client{
		TransformRequestFunc: PrepareRequest,
		Timeout:              d,
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

	// Try to get a connection from the pool
	var conn net.Conn
	if obj.pool != nil && !obj.DisableKeepAlive {
		conn = obj.pool.Get(poolKey)
	}

	// If no pooled connection, dial a new one
	if conn == nil {
		var err error
		conn, err = obj.httpsDialer().Dial("tcp", req.Addr(port))
		if err != nil {
			return err
		}
	}

	return obj.doConnWithPool(conn, req, resp, poolKey)
}

func (obj *Client) DoHTTP(req *Request, resp *Response) error {
	port := req.URI.Port()
	if port == "" {
		port = "80"
	}

	poolKey := PoolKey("http", req.URI.Hostname(), port)

	// Try to get a connection from the pool
	var conn net.Conn
	if obj.pool != nil && !obj.DisableKeepAlive {
		conn = obj.pool.Get(poolKey)
	}

	// If no pooled connection, dial a new one
	if conn == nil {
		var err error
		conn, err = obj.httpDialer().Dial("tcp", req.Addr(port))
		if err != nil {
			return err
		}
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
func (obj *Client) doConnInternal(conn net.Conn, req *Request, resp *Response) error {
	// fmt.Printf("===DEBUG=== RAW:\n%q\n", req.Bytes())
	conn.Write(req.Bytes())
	bufReader := bufio.NewReader(conn)

	for {
		// Set a deadline for reading. Read operation will fail if no data
		// is received after deadline.
		conn.SetReadDeadline(time.Now().Add(obj.Timeout))

		// Read tokens delimited by newline
		bytes, err := bufReader.ReadBytes('\n')
		// fmt.Printf("===REC===: %q (%v)\n", bytes, err)
		resp.Rawdata = append(resp.Rawdata, bytes...)

		if err != nil {
			if err == io.EOF || strings.HasSuffix(err.Error(), "tls: user canceled") {
				return nil
			}
			return err
		}
	}
}

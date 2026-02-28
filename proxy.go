package rawhttp

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"

	"golang.org/x/net/proxy"
)

type httpDialer struct {
	Timeout time.Duration
}

func (obj httpDialer) Dial(network, addr string) (net.Conn, error) {
	return net.DialTimeout(network, addr, obj.Timeout)
}

type httpsDialer struct {
	Timeout time.Duration
}

func (obj httpsDialer) Dial(network, addr string) (c net.Conn, err error) {
	dialer := &net.Dialer{
		Timeout: obj.Timeout,
	}
	return tls.DialWithDialer(dialer, network, addr, &tls.Config{
		InsecureSkipVerify: true,
	})
}

// bufferedConn wraps a net.Conn with a buffered reader to preserve any
// data that was buffered during the HTTP CONNECT handshake.
// Without this wrapper, bufio.Reader used in ReadResponse may read ahead
// from the connection, and those extra bytes would be lost when the raw
// conn is returned to the caller.
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) {
	return c.reader.Read(b)
}

// httpProxy is a HTTP/HTTPS connect proxy.
type httpProxy struct {
	host     string
	haveAuth bool
	username string
	password string
	forward  proxy.Dialer
}

func newHTTPProxy(uri *url.URL, forward proxy.Dialer) (proxy.Dialer, error) {
	s := new(httpProxy)
	s.host = uri.Host
	s.forward = forward
	if uri.User != nil {
		s.haveAuth = true
		s.username = uri.User.Username()
		s.password, _ = uri.User.Password()
	}

	return s, nil
}

func (s *httpProxy) Dial(network, addr string) (net.Conn, error) {
	c, err := s.forward.Dial("tcp", s.host)
	if err != nil {
		return nil, err
	}

	// Write CONNECT request directly
	connectReq := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n", addr, addr)
	if s.haveAuth {
		creds := base64.StdEncoding.EncodeToString([]byte(s.username + ":" + s.password))
		connectReq += fmt.Sprintf("Proxy-Authorization: Basic %s\r\n", creds)
	}
	connectReq += "User-Agent: rawhttp.0.1\r\n\r\n"

	_, err = fmt.Fprint(c, connectReq)
	if err != nil {
		c.Close()
		return nil, err
	}

	br := bufio.NewReader(c)
	statusCode, err := readConnectResponse(br)
	if err != nil {
		c.Close()
		return nil, err
	}
	if statusCode != 200 {
		c.Close()
		err = fmt.Errorf("Connect server using proxy error, StatusCode [%d]", statusCode)
		return nil, err
	}

	return &bufferedConn{Conn: c, reader: br}, nil
}

func ProxyFromURL(u *url.URL, forward proxy.Dialer) (proxy.Dialer, error) {
	return proxy.FromURL(u, forward)
}

// readConnectResponse reads a CONNECT tunnel response (status line + headers only).
// CONNECT responses have no body per RFC 7231 Section 4.3.6, so we must not
// read beyond the header terminator to avoid consuming tunnel data.
func readConnectResponse(br *bufio.Reader) (int, error) {
	// Read status line
	statusLine, err := br.ReadBytes('\n')
	if err != nil {
		return 0, fmt.Errorf("reading status line: %w", err)
	}

	// Parse status code from "HTTP/1.1 200 ..."-style line
	trimmed := bytes.TrimRight(statusLine, "\r\n")
	parts := bytes.SplitN(trimmed, []byte(" "), 3)
	if len(parts) < 2 {
		return 0, fmt.Errorf("malformed status line: %q", trimmed)
	}
	code, err := strconv.Atoi(string(parts[1]))
	if err != nil {
		return 0, fmt.Errorf("invalid status code: %q", parts[1])
	}

	// Read headers until empty line
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			return 0, fmt.Errorf("reading header: %w", err)
		}
		if len(bytes.TrimRight(line, "\r\n")) == 0 {
			break
		}
	}

	return code, nil
}

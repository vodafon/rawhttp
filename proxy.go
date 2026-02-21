package rawhttp

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
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

	reqURL, err := url.Parse("https://" + addr)
	if err != nil {
		c.Close()
		return nil, err
	}
	reqURL.Scheme = ""

	req, err := http.NewRequest("CONNECT", reqURL.String(), nil)
	if err != nil {
		c.Close()
		return nil, err
	}
	req.Close = false
	if s.haveAuth {
		req.SetBasicAuth(s.username, s.password)
	}
	req.Header.Set("User-Agent", "rawhttp.0.1")

	err = req.Write(c)
	if err != nil {
		c.Close()
		return nil, err
	}

	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		c.Close()
		return nil, err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		c.Close()
		err = fmt.Errorf("Connect server using proxy error, StatusCode [%d]", resp.StatusCode)
		return nil, err
	}

	return &bufferedConn{Conn: c, reader: br}, nil
}

func ProxyFromURL(u *url.URL, forward proxy.Dialer) (proxy.Dialer, error) {
	return proxy.FromURL(u, forward)
}

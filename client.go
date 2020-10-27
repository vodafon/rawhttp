package rawhttp

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/url"
	"time"
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
}

func NewDefaultClient() *Client {
	return &Client{
		TransformRequestFunc: PrepareRequest,
		Timeout:              time.Second * 10,
	}
}

func NewClientTransferVariables() *Client {
	return &Client{
		TransformRequestFunc: PrepareRequestVariables,
		Timeout:              time.Second * 10,
	}
}

func NewDefaultClientTimeout(d time.Duration) *Client {
	return &Client{
		TransformRequestFunc: PrepareRequest,
		Timeout:              d,
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
	obj.TransformRequestFunc(req)
	if bytes.HasPrefix(req.Rawdata, []byte("CONNECT ")) {
		return obj.DoProxy(req, resp)
	}

	switch req.URI.Scheme {
	case "https":
		return obj.DoHTTPS(req, resp)
	case "http":
		return obj.DoHTTP(req, resp)
	default:
		return InvalidURLError
	}
	return nil
}

func (obj *Client) DoHTTPS(req *Request, resp *Response) error {
	port := req.URI.Port()
	if port == "" {
		port = "443"
	}

	dialer := &net.Dialer{}
	dialer.Timeout = obj.Timeout
	conn, err := tls.DialWithDialer(dialer, "tcp", req.Addr(port), &tls.Config{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return err
	}
	return obj.DoConn(conn, req, resp)
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
	var conn ReadWriteCloseDeadliner
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

func (obj *Client) DoHTTP(req *Request, resp *Response) error {
	port := req.URI.Port()
	if port == "" {
		port = "80"
	}
	conn, err := net.DialTimeout("tcp", req.Addr(port), obj.Timeout)
	if err != nil {
		return err
	}
	return obj.DoConn(conn, req, resp)
}

// TODO: debug flag
func (obj *Client) DoConn(conn ReadWriteCloseDeadliner, req *Request, resp *Response) error {
	defer conn.Close()
	// fmt.Printf("===DEBUG=== RAW:\n%q\n", req.Rawdata)
	conn.Write(req.Rawdata)
	bufReader := bufio.NewReader(conn)

	for {
		// Set a deadline for reading. Read operation will fail if no data
		// is received after deadline.
		conn.SetReadDeadline(time.Now().Add(obj.Timeout))

		// Read tokens delimited by newline
		bytes, err := bufReader.ReadBytes('\n')
		resp.Rawdata = append(resp.Rawdata, bytes...)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
	return nil
}

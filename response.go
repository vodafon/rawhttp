package rawhttp

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
)

type Response struct {
	Rawdata []byte

	// Timing metrics (measured from after request write completes)
	TimeToFirstByte time.Duration // Time until first response byte received
	TimeToLastByte  time.Duration // Time until last response byte received

	parsed     bool
	httpLine   []byte
	statusCode int
	preBody    []byte
	body       []byte
}

func (obj *Client) NewResponse() *Response {
	return NewResponse()
}

func NewResponse() *Response {
	return &Response{}
}

// Reset clears the response state, allowing the Response to be reused.
// This is useful when retrying a request after a stale connection error.
func (obj *Response) Reset() {
	obj.Rawdata = nil
	obj.TimeToFirstByte = 0
	obj.TimeToLastByte = 0
	obj.parsed = false
	obj.httpLine = nil
	obj.statusCode = 0
	obj.preBody = nil
	obj.body = nil
}

func (obj *Response) Body() []byte {
	obj.ParseRawdata()
	return obj.body
}

func (obj *Response) StatusCode() int {
	obj.ParseRawdata()
	return obj.statusCode
}

func (obj *Response) Bytes() []byte {
	obj.ParseRawdata()

	var buf bytes.Buffer
	buf.Write(obj.preBody)
	buf.Write([]byte("\r\n\r\n"))
	buf.Write(obj.body)

	return buf.Bytes()
}

func (obj *Response) ParseRawdata() error {
	if obj.parsed {
		return nil
	}

	parts := bytes.SplitN(obj.Rawdata, []byte("\r\n\r\n"), 2)
	obj.preBody = parts[0]

	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewBuffer(obj.Rawdata)), &http.Request{})
	if err != nil {
		return err
	}

	var bodyReader io.Reader = resp.Body

	// Handle different compression types
	switch resp.Header.Get("Content-Encoding") {
	case "gzip":
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			panic(err)
		}
		defer gzReader.Close()
		bodyReader = gzReader
	case "br":
		bodyReader = brotli.NewReader(resp.Body)
	case "deflate":
		bodyReader = flate.NewReader(resp.Body)
	}

	obj.body, err = io.ReadAll(bodyReader)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	obj.statusCode = resp.StatusCode

	obj.parsed = true

	return nil
}

func (obj *Client) NewRequestResponse() (*Request, *Response) {
	return obj.NewRequest(), obj.NewResponse()
}

// ConnectionClose returns true if the response indicates the connection should be closed.
// This checks for "Connection: close" header in the response.
func (obj *Response) ConnectionClose() bool {
	if len(obj.Rawdata) == 0 {
		return true // No response data, assume connection should be closed
	}

	// Quick check in raw headers before full parsing
	// Look for "Connection: close" in the header section
	headerEnd := bytes.Index(obj.Rawdata, []byte("\r\n\r\n"))
	if headerEnd == -1 {
		return true // Malformed response, close connection
	}

	headers := obj.Rawdata[:headerEnd]

	// Check for Connection header (case-insensitive)
	lines := bytes.Split(headers, []byte("\r\n"))
	for _, line := range lines[1:] { // Skip status line
		if len(line) == 0 {
			continue
		}
		colonIdx := bytes.IndexByte(line, ':')
		if colonIdx == -1 {
			continue
		}
		key := strings.ToLower(string(bytes.TrimSpace(line[:colonIdx])))
		if key == "connection" {
			value := strings.ToLower(string(bytes.TrimSpace(line[colonIdx+1:])))
			return value == "close"
		}
	}

	// No Connection header found - HTTP/1.1 defaults to keep-alive
	return false
}

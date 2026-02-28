package rawhttp

import (
	"bytes"
	"io"
	"strconv"
	"strings"
)

// Method returns the HTTP method (e.g., "GET", "POST").
func (obj *Request) Method() string {
	obj.ParseRawdata()
	return string(obj.method)
}

// Host returns the Host header value.
func (obj *Request) Host() string {
	obj.ParseRawdata()
	hl, ok := findHeader(obj.headers, "host")
	if !ok {
		return ""
	}
	return string(hl.Value)
}

// Path returns the request path.
func (obj *Request) Path() string {
	obj.ParseRawdata()
	return string(obj.path)
}

// Version returns the HTTP version (e.g., "HTTP/1.1").
func (obj *Request) Version() string {
	obj.ParseRawdata()
	return string(obj.version)
}

// Header returns the value of the first header matching the given key (case-insensitive).
// Returns empty string if not found.
func (obj *Request) Header(key string) string {
	obj.ParseRawdata()
	hl, ok := findHeader(obj.headers, key)
	if !ok {
		return ""
	}
	return string(hl.Value)
}

// ContentLength returns the Content-Length header value as an integer.
// Returns -1 if not present or invalid.
func (obj *Request) ContentLength() int {
	obj.ParseRawdata()
	hl, ok := findHeader(obj.headers, "content-length")
	if !ok {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(hl.Value)))
	if err != nil {
		return -1
	}
	return n
}

// IsChunked returns true if Transfer-Encoding includes "chunked".
func (obj *Request) IsChunked() bool {
	obj.ParseRawdata()
	hl, ok := findHeader(obj.headers, "transfer-encoding")
	if !ok {
		return false
	}
	return strings.Contains(strings.ToLower(string(hl.Value)), "chunked")
}

// Body returns the request body bytes.
func (obj *Request) Body() []byte {
	obj.ParseRawdata()
	return obj.body
}

// Header returns the value of a response header by name (case-insensitive).
// Scans raw header bytes since Response doesn't maintain a headers map.
func (obj *Response) Header(key string) string {
	obj.ParseRawdata()
	if len(obj.preBody) == 0 {
		return ""
	}

	lines := bytes.Split(obj.preBody, []byte("\r\n"))
	lowerKey := strings.ToLower(key)

	// Skip status line (first line), scan headers
	for _, line := range lines[1:] {
		if len(line) == 0 {
			continue
		}
		colonIdx := bytes.IndexByte(line, ':')
		if colonIdx == -1 {
			continue
		}
		k := strings.ToLower(string(bytes.TrimSpace(line[:colonIdx])))
		if k == lowerKey {
			return string(bytes.TrimSpace(line[colonIdx+1:]))
		}
	}
	return ""
}

// WriteTo writes the raw request bytes to w.
// It implements io.WriterTo for efficient streaming.
func (obj *Request) WriteTo(w io.Writer) (int64, error) {
	if len(obj.Rawdata) > 0 {
		n, err := w.Write(obj.Rawdata)
		return int64(n), err
	}
	// If no Rawdata, serialize from parsed fields
	data := obj.Bytes()
	n, err := w.Write(data)
	return int64(n), err
}

// WriteTo writes the raw response bytes to w.
// It implements io.WriterTo for efficient streaming.
func (obj *Response) WriteTo(w io.Writer) (int64, error) {
	n, err := w.Write(obj.Rawdata)
	return int64(n), err
}

// WriteOriginForm writes the request in origin form (e.g., "GET /path HTTP/1.1").
// This converts absolute-URI proxy requests to origin form for direct connections.
// Headers and body are written unchanged from the original request.
func (obj *Request) WriteOriginForm(w io.Writer) (int64, error) {
	obj.ParseRawdata()
	originPath := string(obj.path)
	if obj.URI != nil {
		p := obj.URI.RequestURI()
		if p != "" {
			originPath = p
		}
	}

	var buf bytes.Buffer
	buf.Write(obj.method)
	buf.WriteByte(' ')
	buf.WriteString(originPath)
	buf.WriteByte(' ')
	buf.Write(obj.version)
	buf.WriteString("\r\n")
	buf.Write(obj.rawHeaders)
	buf.WriteString("\r\n\r\n")
	buf.Write(obj.body)

	n, err := w.Write(buf.Bytes())
	return int64(n), err
}

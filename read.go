package rawhttp

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
)

var (
	ErrMalformedRequestLine   = fmt.Errorf("malformed request line")
	ErrMalformedStatusLine    = fmt.Errorf("malformed status line")
	ErrMalformedChunkLength   = fmt.Errorf("malformed chunk length")
	ErrMalformedContentLength = fmt.Errorf("malformed content-length value")
)

// ReadRequest parses an HTTP request from a streaming bufio.Reader.
// It reads the request line, headers, and body (based on Content-Length
// or Transfer-Encoding), populates all parsed fields, and builds Rawdata
// from the raw bytes read.
func ReadRequest(br *bufio.Reader) (*Request, error) {
	var rawBuf bytes.Buffer

	// Read request line
	requestLine, err := readLine(br)
	if err != nil {
		if err == io.EOF && len(requestLine) == 0 {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("reading request line: %w", err)
	}
	line := bytes.TrimRight(requestLine, "\r\n")
	rawBuf.Write(line)
	rawBuf.WriteString("\r\n")

	// Parse request line: METHOD PATH VERSION
	parts := bytes.SplitN(line, []byte(" "), 3)
	if len(parts) != 3 {
		return nil, ErrMalformedRequestLine
	}
	method := make([]byte, len(parts[0]))
	copy(method, parts[0])
	path := make([]byte, len(parts[1]))
	copy(path, parts[1])
	version := make([]byte, len(parts[2]))
	copy(version, parts[2])

	// Read headers
	var headers []HeaderLine
	var rawHeadersBuf bytes.Buffer
	var contentLength int64 = -1
	var isChunked bool
	pos := 0

	for {
		headerLine, err := readLine(br)
		if err != nil && len(headerLine) == 0 {
			return nil, fmt.Errorf("reading headers: %w", err)
		}

		trimmed := bytes.TrimRight(headerLine, "\r\n")

		// Empty line signals end of headers
		if len(trimmed) == 0 {
			rawBuf.WriteString("\r\n")
			break
		}

		rawBuf.Write(trimmed)
		rawBuf.WriteString("\r\n")

		if pos > 0 {
			rawHeadersBuf.WriteString("\r\n")
		}
		rawHeadersBuf.Write(trimmed)

		// Parse header: Key: Value
		colonIdx := bytes.IndexByte(trimmed, ':')
		var k, v []byte
		if colonIdx == -1 {
			k = trimmed
			v = []byte{}
		} else {
			k = trimmed[:colonIdx]
			v = bytes.TrimSpace(trimmed[colonIdx+1:])
		}

		headers = append(headers, HeaderLine{
			Key:   copyBytes(k),
			Value: copyBytes(v),
		})

		// Track content-length and transfer-encoding
		lowerKey := strings.ToLower(string(k))
		if lowerKey == "content-length" {
			n, err := strconv.ParseInt(strings.TrimSpace(string(v)), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%w: %s", ErrMalformedContentLength, v)
			}
			contentLength = n
		}
		if lowerKey == "transfer-encoding" && strings.Contains(strings.ToLower(string(v)), "chunked") {
			isChunked = true
		}

		pos++
	}

	// Read body
	var body []byte
	if isChunked {
		body, err = readChunkedBody(br, &rawBuf)
		if err != nil {
			return nil, fmt.Errorf("reading chunked body: %w", err)
		}
	} else if contentLength > 0 {
		body = make([]byte, contentLength)
		_, err = io.ReadFull(br, body)
		if err != nil {
			return nil, fmt.Errorf("reading body: %w", err)
		}
		rawBuf.Write(body)
	} else if contentLength == 0 {
		body = []byte{}
	} else {
		// No Content-Length, not chunked: for requests, assume no body
		// (GET, HEAD, CONNECT, OPTIONS, TRACE typically have no body)
		body = []byte{}
	}

	// Parse URL from path
	uri, _ := url.Parse(string(path))

	req := &Request{
		Rawdata:    rawBuf.Bytes(),
		URI:        uri,
		URL:        string(path),
		parsed:     true,
		httpLine:   line,
		method:     method,
		path:       path,
		version:    version,
		rawHeaders: rawHeadersBuf.Bytes(),
		body:       body,
		headers:    headers,
	}

	return req, nil
}

// ReadResponse parses an HTTP response from a streaming bufio.Reader.
// It reads the status line, headers, and body (based on Content-Length,
// Transfer-Encoding, or EOF), populates all parsed fields, and builds
// Rawdata from the raw bytes read. Body bytes are preserved raw without
// decompression.
func ReadResponse(br *bufio.Reader) (*Response, error) {
	var rawBuf bytes.Buffer
	var preBodyBuf bytes.Buffer

	// Read status line
	statusLine, err := readLine(br)
	if err != nil {
		if err == io.EOF && len(statusLine) == 0 {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("reading status line: %w", err)
	}

	trimmedStatus := bytes.TrimRight(statusLine, "\r\n")
	rawBuf.Write(trimmedStatus)
	rawBuf.WriteString("\r\n")
	preBodyBuf.Write(trimmedStatus)

	// Parse status line: HTTP/1.1 200 OK
	parts := bytes.SplitN(trimmedStatus, []byte(" "), 3)
	if len(parts) < 2 {
		return nil, ErrMalformedStatusLine
	}
	statusCode, err := strconv.Atoi(string(parts[1]))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrMalformedStatusLine, parts[1])
	}

	// Read headers
	var contentLength int64 = -1
	var isChunked bool

	for {
		headerLine, err := readLine(br)
		if err != nil && len(headerLine) == 0 {
			return nil, fmt.Errorf("reading response headers: %w", err)
		}

		trimmed := bytes.TrimRight(headerLine, "\r\n")

		if len(trimmed) == 0 {
			rawBuf.WriteString("\r\n")
			break
		}

		rawBuf.Write(trimmed)
		rawBuf.WriteString("\r\n")
		preBodyBuf.WriteString("\r\n")
		preBodyBuf.Write(trimmed)

		// Parse header for content-length / transfer-encoding
		colonIdx := bytes.IndexByte(trimmed, ':')
		if colonIdx != -1 {
			k := strings.ToLower(string(bytes.TrimSpace(trimmed[:colonIdx])))
			v := bytes.TrimSpace(trimmed[colonIdx+1:])

			if k == "content-length" {
				n, err := strconv.ParseInt(strings.TrimSpace(string(v)), 10, 64)
				if err == nil {
					contentLength = n
				}
			}
			if k == "transfer-encoding" && strings.Contains(strings.ToLower(string(v)), "chunked") {
				isChunked = true
			}
		}
	}

	// Read body
	var body []byte
	noBodyStatus := (statusCode >= 100 && statusCode < 200) || statusCode == 204 || statusCode == 304

	if noBodyStatus {
		body = []byte{}
	} else if isChunked {
		body, err = readChunkedBody(br, &rawBuf)
		if err != nil {
			return nil, fmt.Errorf("reading chunked response body: %w", err)
		}
	} else if contentLength >= 0 {
		body = make([]byte, contentLength)
		if contentLength > 0 {
			_, err = io.ReadFull(br, body)
			if err != nil {
				return nil, fmt.Errorf("reading response body: %w", err)
			}
		}
		rawBuf.Write(body)
	} else {
		// No Content-Length, not chunked, not a no-body status:
		// read until EOF (handles Connection: close responses)
		body, err = io.ReadAll(br)
		if err != nil {
			return nil, fmt.Errorf("reading response body until EOF: %w", err)
		}
		rawBuf.Write(body)
	}

	resp := &Response{
		Rawdata:    rawBuf.Bytes(),
		parsed:     true,
		httpLine:   trimmedStatus,
		statusCode: statusCode,
		preBody:    preBodyBuf.Bytes(),
		body:       body,
	}

	return resp, nil
}

// readLine reads a single line from the reader, handling both \r\n and \n endings.
func readLine(br *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := br.ReadBytes('\n')
		line = append(line, chunk...)
		if err != nil {
			return line, err
		}
		// Got a complete line
		return line, nil
	}
}

// readChunkedBody reads a chunked transfer-encoded body.
// Raw chunked encoding bytes are written to rawBuf.
// Returns the concatenated chunk data (without chunk framing).
func readChunkedBody(br *bufio.Reader, rawBuf *bytes.Buffer) ([]byte, error) {
	var body bytes.Buffer

	for {
		// Read chunk size line
		sizeLine, err := readLine(br)
		if err != nil {
			return nil, fmt.Errorf("reading chunk size: %w", err)
		}
		rawBuf.Write(sizeLine)

		// Parse chunk size (hex)
		sizeStr := strings.TrimSpace(string(bytes.TrimRight(sizeLine, "\r\n")))
		// Handle chunk extensions (e.g., "a;ext=val")
		if idx := strings.IndexByte(sizeStr, ';'); idx != -1 {
			sizeStr = sizeStr[:idx]
		}
		size, err := strconv.ParseInt(sizeStr, 16, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: %q", ErrMalformedChunkLength, sizeStr)
		}

		if size == 0 {
			// Read trailing \r\n after last chunk
			trailer, err := readLine(br)
			if err != nil && err != io.EOF {
				return nil, fmt.Errorf("reading chunk trailer: %w", err)
			}
			rawBuf.Write(trailer)
			break
		}

		// Read chunk data + \r\n
		chunkData := make([]byte, size+2) // +2 for trailing \r\n
		_, err = io.ReadFull(br, chunkData)
		if err != nil {
			return nil, fmt.Errorf("reading chunk data: %w", err)
		}
		rawBuf.Write(chunkData)
		body.Write(chunkData[:size]) // exclude trailing \r\n
	}

	return body.Bytes(), nil
}

// copyBytes returns a copy of the given byte slice.
func copyBytes(b []byte) []byte {
	c := make([]byte, len(b))
	copy(c, b)
	return c
}

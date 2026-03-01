package rawhttp

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)
// NormalizeRequest normalizes a request by:
// 1. Parsing raw data to populate parsed fields
// 2. Constraining Accept-Encoding to supported encodings (gzip, deflate, br)
// 3. Removing proxy-related headers (Proxy-Connection, Proxy-Authorization)
// 4. Removing Sec-WebSocket-Extensions header
// 5. If Transfer-Encoding: chunked, replacing it with Content-Length for the decoded body
// 6. Clearing Rawdata to force reconstruction from parsed fields on WriteTo()
func (obj *Request) NormalizeRequest() {
	// Ensure all parsed fields are populated from Rawdata
	obj.ParseRawdata()

	// Constrain Accept-Encoding to only encodings rawhttp can decompress
	obj.ConstrainAcceptEncoding()

	// Remove proxy-related headers
	obj.RemoveHeader("Proxy-Connection")
	obj.RemoveHeader("Proxy-Authorization")

	// Remove WebSocket extensions header
	obj.RemoveHeader("Sec-WebSocket-Extensions")

	// If request was chunked, replace Transfer-Encoding with Content-Length.
	// ReadRequest/ParseRawdata already decoded the chunked body into obj.body,
	// but Bytes() writes obj.body as-is (no chunked re-encoding). Without this,
	// the target sees Transfer-Encoding: chunked but gets an identity-encoded body.
	if obj.IsChunked() {
		obj.RemoveHeader("Transfer-Encoding")
		if len(obj.body) > 0 {
			obj.SetHeader("content-length", []byte("Content-Length"), []byte(strconv.Itoa(len(obj.body))))
		}
	}

	// Set Rawdata to nil to force WriteTo() to use Bytes() reconstruction
	// This ensures all normalized headers are included in the output
	obj.Rawdata = nil
}

// NormalizeResponse normalizes a response by:
// 1. Parsing raw data to populate preBody, body, statusCode (decompression happens here)
// 2. Removing hop-by-hop and proxy-related headers
// 3. Updating Content-Length to reflect decompressed body size
// 4. Rebuilding Rawdata from normalized components
func (obj *Response) NormalizeResponse() {
	// Force re-parsing to trigger decompression in ParseRawdata()
	// (ReadResponse sets parsed=true but doesn't decompress)
	obj.parsed = false

	// Parse raw data to populate fields. ParseRawdata() handles decompression
	// of gzip/br/deflate bodies. If parsing fails (e.g., invalid gzip),
	// we return without changes (graceful fallback).
	err := obj.ParseRawdata()
	if err != nil {
		return
	}

	statusCode := obj.statusCode
	preBody := obj.preBody

	// For no-body status codes (1xx, 204, 304), only strip headers
	noBody := (statusCode >= 100 && statusCode < 200) || statusCode == 204 || statusCode == 304
	if noBody {
		newPreBody := filterResponseHeaders(preBody, nil)
		obj.preBody = newPreBody
		obj.rebuildRawdata(nil)
		obj.parsed = false
		return
	}

	// Get Content-Encoding from original preBody to determine if decompression occurred.
	// ParseRawdata() already decompressed the body, so obj.body has the decompressed content.
	contentEncoding := responseHeaderValue(preBody, "content-encoding")
	contentEncoding = strings.ToLower(strings.TrimSpace(contentEncoding))

	// Validate encoding — only handle known single encodings
	switch contentEncoding {
	case "", "identity", "gzip", "br", "deflate":
		// Supported — proceed
	default:
		// Unknown or multiple encodings — return without changes
		return
	}
	// Determine whether we need to set Content-Length.
	// We must add Content-Length when filterResponseHeaders strips headers that
	// defined the body framing:
	// - Content-Encoding was present (body was decompressed, size changed)
	// - Transfer-Encoding was present (chunked framing was decoded into plain body)
	var clPtr *string
	transferEncoding := responseHeaderValue(preBody, "transfer-encoding")
	needsContentLength := (contentEncoding != "" && contentEncoding != "identity") ||
		strings.Contains(strings.ToLower(transferEncoding), "chunked")
	if needsContentLength {
		contentLengthVal := strconv.Itoa(len(obj.body))
		clPtr = &contentLengthVal
	}

	newPreBody := filterResponseHeaders(preBody, clPtr)

	obj.preBody = newPreBody
	obj.rebuildRawdata(nil)
	obj.parsed = false
}

// rebuildRawdata reconstructs Rawdata from preBody and body.
func (obj *Response) rebuildRawdata(bodyOverride []byte) {
	body := obj.body
	if bodyOverride != nil {
		body = bodyOverride
	}
	var buf bytes.Buffer
	buf.Write(obj.preBody)
	buf.WriteString("\r\n\r\n")
	buf.Write(body)
	obj.Rawdata = buf.Bytes()
}

// filterResponseHeaders filters preBody lines, removing hop-by-hop/proxy headers.
// If newContentLength is non-nil, Content-Length is replaced/added with that value.
// Headers removed: Content-Encoding, Transfer-Encoding, Proxy-Connection,
// Proxy-Authorization, Sec-WebSocket-Extensions.
func filterResponseHeaders(preBody []byte, newContentLength *string) []byte {
	lines := bytes.Split(preBody, []byte("\r\n"))
	if len(lines) == 0 {
		return preBody
	}

	// Headers to strip (lowercase keys)
	stripHeaders := map[string]bool{
		"content-encoding":        true,
		"transfer-encoding":       true,
		"proxy-connection":        true,
		"proxy-authorization":     true,
		"sec-websocket-extensions": true,
	}

	var result [][]byte
	result = append(result, lines[0]) // Keep status line

	contentLengthFound := false

	for _, line := range lines[1:] {
		if len(line) == 0 {
			continue
		}
		colonIdx := bytes.IndexByte(line, ':')
		if colonIdx == -1 {
			result = append(result, line)
			continue
		}
		key := strings.ToLower(string(bytes.TrimSpace(line[:colonIdx])))
		if stripHeaders[key] {
			continue
		}
		if key == "content-length" && newContentLength != nil {
			// Replace Content-Length with new value
			result = append(result, []byte(fmt.Sprintf("Content-Length: %s", *newContentLength)))
			contentLengthFound = true
			continue
		}
		result = append(result, line)
	}

	// Add Content-Length if not found and we need one
	if newContentLength != nil && !contentLengthFound {
		result = append(result, []byte(fmt.Sprintf("Content-Length: %s", *newContentLength)))
	}

	return bytes.Join(result, []byte("\r\n"))
}

// responseHeaderValue extracts a header value from preBody bytes (case-insensitive).
func responseHeaderValue(preBody []byte, key string) string {
	lines := bytes.Split(preBody, []byte("\r\n"))
	lowerKey := strings.ToLower(key)
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

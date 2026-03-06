package rawhttp

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

type Request struct {
	Rawdata []byte
	URL     string
	URI     *url.URL
	IP      string

	parsed     bool
	httpLine   []byte
	method     []byte
	path       []byte
	version    []byte
	rawHeaders []byte
	body       []byte
	headers    []HeaderLine
}

type HeaderLine struct {
	Key, Value []byte
}

func (obj *Request) SetRawdata(rd []byte) error {
	obj.Rawdata = rd
	obj.parsed = false
	return obj.ParseRawdata()
}

func (obj *Request) SetMethod(method []byte) {
	obj.method = method
}

func (obj *Request) SetBody(body []byte) {
	obj.body = body
}

func (obj *Request) CacheBusterParam() {
	buf := make([]byte, 4)
	rand.Read(buf)
	param := hex.EncodeToString(buf)
	obj.AddQueryParams([]byte(fmt.Sprintf("%s=%s", param, param)))
}

func (obj *Request) ParsedPath() []byte {
	return obj.path
}

func (obj *Request) AddQueryParams(params []byte) {
	fragmentPieces := bytes.Split(obj.path, []byte("#"))
	var buf bytes.Buffer
	buf.Write(fragmentPieces[0])
	if bytes.Contains(fragmentPieces[0], []byte("?")) {
		buf.Write([]byte("&"))
	} else {
		buf.Write([]byte("?"))
	}
	buf.Write(params)
	if len(fragmentPieces) > 1 {
		buf.Write(bytes.Join(fragmentPieces[1:], []byte("#")))
	}
	obj.path = buf.Bytes()
}

func (obj *Request) ParseRawdata() error {
	if obj.parsed {
		return nil
	}
	if !bytes.Contains(obj.Rawdata, []byte("\r\n")) {
		obj.Rawdata = prepareBytes(obj.Rawdata, &Request{})
	}

	pieces := bytes.Split(obj.Rawdata, []byte("\r\n\r\n"))
	headers := bytes.Split(pieces[0], []byte("\r\n"))
	if len(pieces) > 1 {
		obj.body = bytes.Join(pieces[1:], []byte("\r\n\r\n"))
	}
	obj.httpLine = headers[0]
	hlinePieces := trimSpaces(bytes.Split(obj.httpLine, []byte(" ")))
	if len(hlinePieces) != 3 {
		return fmt.Errorf("invalid HTTP line: %q", obj.httpLine)
	}
	obj.method = hlinePieces[0]
	obj.path = hlinePieces[1]
	obj.version = hlinePieces[2]

	obj.rawHeaders = bytes.Join(headers[1:], []byte("\r\n"))
	obj.headers = nil

	for _, line := range headers[1:] {
		linePieces := bytes.Split(line, []byte(":"))
		k := linePieces[0]
		v := []byte{}
		if len(linePieces) > 1 {
			v = bytes.TrimSpace(bytes.Join(linePieces[1:], []byte(":")))
		}
		obj.headers = append(obj.headers, HeaderLine{
			Key:   k,
			Value: v,
		})
	}
	obj.parsed = true
	return nil
}

func (obj *Request) SetHeader(key string, name, value []byte) {
	lowerKey := strings.ToLower(key)
	for i, hl := range obj.headers {
		if strings.ToLower(string(hl.Key)) == lowerKey {
			obj.headers[i].Key = name
			obj.headers[i].Value = value
			return
		}
	}
	obj.headers = append(obj.headers, HeaderLine{
		Key:   name,
		Value: value,
	})
}

// RemoveHeader removes all headers matching the given key (case-insensitive).
// After removal, Rawdata is set to nil so subsequent WriteTo() uses Bytes() reconstruction.
func (obj *Request) RemoveHeader(key string) {
	obj.ParseRawdata()
	lowerKey := strings.ToLower(key)
	// Filter headers: keep those that don't match the key
	filtered := make([]HeaderLine, 0, len(obj.headers))
	for _, hl := range obj.headers {
		if strings.ToLower(string(hl.Key)) != lowerKey {
			filtered = append(filtered, hl)
		}
	}
	obj.headers = filtered
	obj.Rawdata = nil
}

// ConstrainAcceptEncoding constrains the Accept-Encoding header to only gzip, deflate, and br.
// These are the encodings rawhttp can decompress. If Accept-Encoding exists, it is replaced.
// If the request has no Accept-Encoding header, none is added.
func (obj *Request) ConstrainAcceptEncoding() {
	obj.ParseRawdata()
	for _, hl := range obj.headers {
		if strings.ToLower(string(hl.Key)) == "accept-encoding" {
			obj.SetHeader("accept-encoding", []byte("Accept-Encoding"), []byte("gzip, deflate, br"))
			obj.Rawdata = nil
			return
		}
	}
}

func (obj *Request) Bytes() []byte {
	headerSlice := make([][]byte, len(obj.headers))

	for i, v := range obj.headers {
		var hbuf bytes.Buffer
		hbuf.Write(v.Key)
		hbuf.Write([]byte(": "))
		hbuf.Write(v.Value)
		headerSlice[i] = hbuf.Bytes()
	}
	headers := bytes.Join(headerSlice, []byte("\r\n"))

	var buf bytes.Buffer
	buf.Write(obj.method)
	buf.Write([]byte(" "))
	buf.Write(obj.path)
	buf.Write([]byte(" "))
	buf.Write(obj.version)
	buf.Write([]byte("\r\n"))
	buf.Write(headers)
	buf.Write([]byte("\r\n\r\n"))
	buf.Write(obj.body)

	return buf.Bytes()
}

func trimSpaces(sl [][]byte) [][]byte {
	res := [][]byte{}
	for _, v := range sl {
		v1 := bytes.TrimSpace(v)
		if len(v1) == 0 {
			continue
		}
		res = append(res, v1)
	}
	return res
}

func (obj Request) RawMethod() string {
	lines := bytes.Split(obj.Rawdata, []byte("\n"))
	if len(lines) == 0 {
		return ""
	}
	return string(bytes.Split(lines[0], []byte(" "))[0])
}

func (obj *Request) Addr(port string) string {
	if obj.IP == "" {
		return obj.URI.Hostname() + ":" + port
	}
	return obj.IP + ":" + port
}

// WantsClose returns true if the request has "Connection: close" header set.
func (obj *Request) WantsClose() bool {
	return obj.headerHasValue("connection", "close")
}

// WantsUpgrade returns true if the request has "Connection: Upgrade" header set.
func (obj *Request) WantsUpgrade() bool {
	return obj.headerHasValue("connection", "upgrade")
}

// headerHasValue checks if a header contains a specific value
// Values can be separated by non-word characters except hyphen and underscore (spaces, commas, etc.)
// For example: "Connection: host, close, proxy" contains "close" but not "clos"
// Hyphens and underscores are allowed as part of values
func (obj *Request) headerHasValue(header string, value string) bool {
	hl, ok := findHeader(obj.headers, header)
	if !ok || string(hl.Value) == "" {
		return false
	}

	// Split by any non-word characters except hyphen and underscore
	re := regexp.MustCompile(`[^\w\-_]+`)
	values := re.Split(string(hl.Value), -1)

	for _, v := range values {
		if strings.EqualFold(v, value) {
			return true
		}
	}
	return false
}

// SetConnectionClose sets the Connection header to "close", indicating
// the connection should not be reused after this request.
func (obj *Request) SetConnectionClose() {
	obj.SetHeader("connection", []byte("Connection"), []byte("close"))
}

func (obj *Request) FullPath() string {
	path := obj.URI.RequestURI()
	if obj.URI.Fragment == "" {
		return path
	}
	return path + "#" + obj.URI.Fragment
}

func (obj *Client) NewRequest() *Request {
	return &Request{}
}

func (obj *Client) NewBaseRequest(u string) (*Request, error) {
	return NewBaseRequest(u)
}

func NewBaseRequest(u string) (*Request, error) {
	uri, err := url.Parse(u)
	if err != nil {
		return nil, err
	}

	r := &Request{
		Rawdata: baseTemplate(),
		URI:     uri,
		URL:     u,
	}
	err = r.ParseRawdata()
	if err != nil {
		return nil, err
	}
	PrepareRequest(r)
	return r, nil
}

func baseTemplate() []byte {
	t := `GET ||FULLPATH|| HTTP/1.1
Host: ||HOST||
Connection: keep-alive
User-Agent: rh.1.1
Accept: */*

`
	return prepareBytes([]byte(t), &Request{})
}

func (obj *Client) NewRawPathRequest(u, path string) (*Request, error) {
	return NewRawPathRequest(u, path)
}

func NewRawPathRequest(u, path string) (*Request, error) {
	uri, err := url.Parse(u)
	if err != nil {
		return nil, err
	}

	r := &Request{
		Rawdata: rawPathTemplate(path),
		URI:     uri,
		URL:     u,
	}
	err = r.ParseRawdata()
	if err != nil {
		return nil, err
	}
	PrepareRequest(r)
	return r, nil
}

func rawPathTemplate(path string) []byte {
	t := `GET ||FULLPATH|| HTTP/1.1
Host: ||HOST||
Connection: keep-alive
User-Agent: rh.1.1
Accept: */*

`
	t = strings.ReplaceAll(t, "||FULLPATH||", path)
	return prepareBytes([]byte(t), &Request{})
}

func PrepareRequest(req *Request) {
	req.method = prepareBytes(req.method, req)
	req.path = prepareBytes(req.path, req)
	req.version = prepareBytes(req.version, req)
	req.body = prepareBytes(req.body, req)
	for i, v := range req.headers {
		v.Key = prepareBytes(v.Key, req)
		v.Value = prepareBytes(v.Value, req)
		req.headers[i] = v
	}
	PrepareRequestVariables(req)
}

func prepareBytes(data []byte, req *Request) []byte {
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))
}

func PrepareRequestVariables(req *Request) {
	// body first for CLEN
	req.body = prepareBytesVariables(req.body, req)
	idx := bytes.Index(req.body, []byte("||END||"))
	if idx != -1 {
		req.body = req.body[:idx]
	}

	req.method = prepareBytesVariables(req.method, req)
	req.path = prepareBytesVariables(req.path, req)
	req.version = prepareBytesVariables(req.version, req)
	for i, v := range req.headers {
		v.Key = prepareBytesVariables(v.Key, req)
		v.Value = prepareBytesVariables(v.Value, req)
		req.headers[i] = v
	}
}

func prepareBytesVariables(data []byte, req *Request) []byte {
	path := req.URI.Path
	if path == "" {
		path = "/"
	}
	data = bytes.ReplaceAll(data, []byte("||CR||"), []byte("\r"))
	data = bytes.ReplaceAll(data, []byte("||LF||"), []byte("\n"))
	data = bytes.ReplaceAll(data, []byte("||ABSURL||"), []byte(req.URL))
	data = bytes.ReplaceAll(data, []byte("||HOST||"), []byte(req.URI.Hostname()))
	data = bytes.ReplaceAll(data, []byte("||PATH||"), []byte(path))
	data = bytes.ReplaceAll(data, []byte("||ESCAPEDPATH||"), []byte(req.URI.EscapedPath()))
	data = bytes.ReplaceAll(data, []byte("||FULLPATH||"), []byte(req.FullPath()))
	return bytes.ReplaceAll(data, []byte("||CLEN||"), []byte(fmt.Sprintf("%d", len(req.body))))
}

func ContentLengthCalculation(req *Request) {
	parts := bytes.Split(bytes.TrimSpace(req.Rawdata), []byte("\r\n\r\n"))
	if len(parts) < 2 {
		req.Rawdata = bytes.ReplaceAll(req.Rawdata, []byte("||CLEN||"), []byte("0"))
		return
	}
	l := len(bytes.Join(parts[1:], []byte("\r\n\r\n")))
	req.Rawdata = bytes.ReplaceAll(req.Rawdata, []byte("||CLEN||"), []byte(fmt.Sprintf("%d", l)))
}


// findHeader returns the first header matching the given key (case-insensitive).
func findHeader(headers []HeaderLine, key string) (HeaderLine, bool) {
	lowerKey := strings.ToLower(key)
	for _, hl := range headers {
		if strings.ToLower(string(hl.Key)) == lowerKey {
			return hl, true
		}
	}
	return HeaderLine{}, false
}
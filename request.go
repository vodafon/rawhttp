package rawhttp

import (
	"bytes"
	"fmt"
	"net/url"
	"strings"

	"github.com/vodafon/vgutils"
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
	headers    map[string]HeaderLine
}

type HeaderLine struct {
	Key, Value []byte
	Pos        int
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
	param := vgutils.RandomHEXString(4)
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
	obj.headers = make(map[string]HeaderLine)

	for i, line := range headers[1:] {
		linePieces := bytes.Split(line, []byte(":"))
		k := linePieces[0]
		v := []byte{}
		if len(linePieces) > 1 {
			v = bytes.TrimSpace(bytes.Join(linePieces[1:], []byte(":")))
		}
		key := strings.ToLower(string(k))
		_, ok := obj.headers[key]
		if ok {
			key = fmt.Sprintf("%s_%d", key, i)
		}
		obj.headers[key] = HeaderLine{
			Pos:   i,
			Key:   k,
			Value: v,
		}
	}
	obj.parsed = true
	return nil
}

func (obj *Request) SetHeader(key string, name, value []byte) {
	hl, ok := obj.headers[key]
	if !ok {
		hl.Pos = len(obj.headers)
	}
	hl.Key = name
	hl.Value = value
	obj.headers[key] = hl
}

func (obj *Request) Bytes() []byte {
	headerSlice := make([][]byte, len(obj.headers))

	for _, v := range obj.headers {
		var hbuf bytes.Buffer
		hbuf.Write(v.Key)
		hbuf.Write([]byte(": "))
		hbuf.Write(v.Value)
		headerSlice[v.Pos] = hbuf.Bytes()
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
Connection: close
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
Connection: close
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
	for k, v := range req.headers {
		v.Key = prepareBytes(v.Key, req)
		v.Value = prepareBytes(v.Value, req)
		req.headers[k] = v
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
	for k, v := range req.headers {
		v.Key = prepareBytesVariables(v.Key, req)
		v.Value = prepareBytesVariables(v.Value, req)
		req.headers[k] = v
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

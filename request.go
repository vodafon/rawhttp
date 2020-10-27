package rawhttp

import (
	"bytes"
	"fmt"
	"net/url"
)

type Request struct {
	Rawdata []byte
	URL     string
	URI     *url.URL
	IP      string
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
	return []byte(t)
}

func PrepareRequest(req *Request) {
	req.Rawdata = bytes.ReplaceAll(req.Rawdata, []byte("\r\n"), []byte("\n"))
	req.Rawdata = bytes.ReplaceAll(req.Rawdata, []byte("\n"), []byte("\r\n"))
	PrepareRequestVariables(req)
}

func PrepareRequestVariables(req *Request) {
	path := req.URI.Path
	if path == "" {
		path = "/"
	}
	req.Rawdata = bytes.ReplaceAll(req.Rawdata, []byte("||CR||"), []byte("\r"))
	req.Rawdata = bytes.ReplaceAll(req.Rawdata, []byte("||LF||"), []byte("\n"))
	req.Rawdata = bytes.ReplaceAll(req.Rawdata, []byte("||ABSURL||"), []byte(req.URL))
	req.Rawdata = bytes.ReplaceAll(req.Rawdata, []byte("||HOST||"), []byte(req.URI.Hostname()))
	req.Rawdata = bytes.ReplaceAll(req.Rawdata, []byte("||PATH||"), []byte(path))
	req.Rawdata = bytes.ReplaceAll(req.Rawdata, []byte("||ESCAPEDPATH||"), []byte(req.URI.EscapedPath()))
	req.Rawdata = bytes.ReplaceAll(req.Rawdata, []byte("||FULLPATH||"), []byte(req.FullPath()))

	idx := bytes.Index(req.Rawdata, []byte("||END||"))
	if idx != -1 {
		req.Rawdata = req.Rawdata[:idx]
	}

	if bytes.Contains(req.Rawdata, []byte("||CLEN||")) {
		ContentLengthCalculation(req)
	}
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

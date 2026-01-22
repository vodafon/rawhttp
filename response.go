package rawhttp

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"io"
	"net/http"

	"github.com/andybalholm/brotli"
)

type Response struct {
	Rawdata []byte

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

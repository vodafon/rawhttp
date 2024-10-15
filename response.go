package rawhttp

import (
	"bufio"
	"bytes"
	"io/ioutil"
	"net/http"
)

type Response struct {
	Rawdata []byte

	parsed     bool
	httpLine   []byte
	statusCode int
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

func (obj *Response) ParseRawdata() error {
	if obj.parsed {
		return nil
	}

	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewBuffer(obj.Rawdata)), &http.Request{})
	if err != nil {
		return err
	}

	obj.body, err = ioutil.ReadAll(resp.Body)
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

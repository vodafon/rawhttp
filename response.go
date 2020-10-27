package rawhttp

type Response struct {
	Rawdata []byte
}

func (obj *Client) NewResponse() *Response {
	return NewResponse()
}

func NewResponse() *Response {
	return &Response{}
}

func (obj *Client) NewRequestResponse() (*Request, *Response) {
	return obj.NewRequest(), obj.NewResponse()
}

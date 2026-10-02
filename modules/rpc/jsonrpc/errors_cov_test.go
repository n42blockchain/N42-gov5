package jsonrpc

import "testing"

func TestHTTPErrorError(t *testing.T) {
	e := HTTPError{StatusCode: 500, Status: "500 Internal Server Error"}
	if e.Error() != "500 Internal Server Error" {
		t.Errorf("got %q", e.Error())
	}

	e2 := HTTPError{StatusCode: 400, Status: "400 Bad Request", Body: []byte("bad input")}
	want := "400 Bad Request: bad input"
	if e2.Error() != want {
		t.Errorf("got %q want %q", e2.Error(), want)
	}
}

func TestRPCErrorTypes(t *testing.T) {
	cases := []struct {
		err  Error
		code int
	}{
		{&methodNotFoundError{method: "foo_bar"}, -32601},
		{&subscriptionNotFoundError{namespace: "eth", subscription: "newHeads"}, -32601},
		{&parseError{message: "bad json"}, -32700},
		{&invalidRequestError{message: "bad request"}, -32600},
		{&invalidMessageError{message: "bad message"}, -32700},
		{&invalidParamsError{message: "bad params"}, -32602},
	}
	for _, c := range cases {
		if c.err.ErrorCode() != c.code {
			t.Errorf("%T: ErrorCode() = %d, want %d", c.err, c.err.ErrorCode(), c.code)
		}
		if c.err.Error() == "" {
			t.Errorf("%T: Error() returned empty string", c.err)
		}
	}

	mnf := &methodNotFoundError{method: "eth_foo"}
	if mnf.Error() != "the method eth_foo does not exist/is not available" {
		t.Errorf("unexpected message: %q", mnf.Error())
	}

	snf := &subscriptionNotFoundError{namespace: "eth", subscription: "logs"}
	if snf.Error() != `no "logs" subscription in eth namespace` {
		t.Errorf("unexpected message: %q", snf.Error())
	}
}

func TestDefaultErrorCode(t *testing.T) {
	if defaultErrorCode != -32000 {
		t.Errorf("defaultErrorCode = %d want -32000", defaultErrorCode)
	}
}

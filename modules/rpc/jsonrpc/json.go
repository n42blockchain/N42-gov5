// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// JSON-RPC wire protocol codec.
// Defines vsn ("2.0"), serviceMethodSeparator and subscribe/
// notification/unsubscribe suffixes used for routing. maxBatchSize
// (1000) caps inbound batch requests and defaultWriteTimeout is 10s.
// jsonrpcMessage is the canonical request/response envelope and
// subscriptionResult wraps server-sent subscription notifications.

package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"time"
)

const (
	vsn                      = "2.0"
	serviceMethodSeparator   = "_"
	subscribeMethodSuffix    = "_subscribe"
	notificationMethodSuffix = "_subscription"
	unsubscribeMethodSuffix  = "_unsubscribe"

	defaultWriteTimeout = 10 * time.Second
	maxBatchSize        = 1000
)

var null = json.RawMessage("null")

type subscriptionResult struct {
	ID     string          `json:"subscription"`
	Result json.RawMessage `json:"result,omitempty"`
}

type jsonrpcMessage struct {
	Version string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Error   *jsonError      `json:"error,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`

	// resultMarshaled: Result is encoding/json.Marshal output, which is
	// already compact and HTML-escaped exactly as json.Encoder would write
	// it, so writeJSON may copy it instead of re-validating it.
	resultMarshaled bool
}

func (msg *jsonrpcMessage) isNotification() bool {
	return msg.ID == nil && msg.Method != ""
}

func (msg *jsonrpcMessage) isCall() bool {
	return msg.hasValidID() && msg.Method != ""
}

func (msg *jsonrpcMessage) isResponse() bool {
	return msg.hasValidID() && msg.Method == "" && msg.Params == nil && (msg.Result != nil || msg.Error != nil)
}

func (msg *jsonrpcMessage) hasValidID() bool {
	return len(msg.ID) > 0 && msg.ID[0] != '{' && msg.ID[0] != '['
}

func (msg *jsonrpcMessage) isSubscribe() bool {
	return strings.HasSuffix(msg.Method, subscribeMethodSuffix)
}

func (msg *jsonrpcMessage) isUnsubscribe() bool {
	return strings.HasSuffix(msg.Method, unsubscribeMethodSuffix)
}

func (msg *jsonrpcMessage) namespace() string {
	elem := strings.SplitN(msg.Method, serviceMethodSeparator, 2)
	return elem[0]
}

func (msg *jsonrpcMessage) String() string {
	b, _ := json.Marshal(msg)
	return string(b)
}

func (msg *jsonrpcMessage) errorResponse(err error) *jsonrpcMessage {
	resp := errorMessage(err)
	resp.ID = msg.ID
	return resp
}

func (msg *jsonrpcMessage) response(result interface{}) *jsonrpcMessage {
	enc, err := json.Marshal(result)
	if err != nil {
		// TODO: wrap with 'internal server error'
		return msg.errorResponse(err)
	}
	return &jsonrpcMessage{Version: vsn, ID: msg.ID, Result: enc, resultMarshaled: true}
}

func errorMessage(err error) *jsonrpcMessage {
	msg := &jsonrpcMessage{Version: vsn, ID: null, Error: &jsonError{
		Code:    defaultErrorCode,
		Message: err.Error(),
	}}
	ec, ok := err.(Error)
	if ok {
		msg.Error.Code = ec.ErrorCode()
	}
	de, ok := err.(DataError)
	if ok {
		msg.Error.Data = de.ErrorData()
	}
	return msg
}

type jsonError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

func (err *jsonError) Error() string {
	if err.Message == "" {
		return fmt.Sprintf("json-rpc error %d", err.Code)
	}
	return err.Message
}

func (err *jsonError) ErrorCode() int {
	return err.Code
}

func (err *jsonError) ErrorData() interface{} {
	return err.Data
}

type Conn interface {
	io.ReadWriteCloser
	SetWriteDeadline(time.Time) error
}

type deadlineCloser interface {
	io.Closer
	SetWriteDeadline(time.Time) error
}

type ConnRemoteAddr interface {
	RemoteAddr() string
}

type jsonCodec struct {
	remote  string
	closer  sync.Once
	closeCh chan interface{}
	decode  func(v interface{}) error
	encMu   sync.Mutex
	encode  func(v interface{}) error
	conn    deadlineCloser
	// raw, when set, is the stream encode writes to (NewCodec): plain
	// responses are written to it directly (see appendResponses).
	raw io.Writer
}

func NewFuncCodec(conn deadlineCloser, encode, decode func(v interface{}) error) ServerCodec {
	codec := &jsonCodec{
		closeCh: make(chan interface{}),
		encode:  encode,
		decode:  decode,
		conn:    conn,
	}
	if ra, ok := conn.(ConnRemoteAddr); ok {
		codec.remote = ra.RemoteAddr()
	}
	return codec
}

func NewCodec(conn Conn) ServerCodec {
	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)
	dec.UseNumber()
	codec := NewFuncCodec(conn, enc.Encode, dec.Decode).(*jsonCodec)
	codec.raw = conn
	return codec
}

// appendResponses appends the exact bytes json.Encoder.Encode writes for v
// when v is a response message or a batch of them whose results came from
// json.Marshal, and reports whether it could. Encoding such a message with
// encoding/json re-scans and re-compacts the whole RawMessage result, which
// for a block or trace batch is megabytes of work per call.
func appendResponses(buf []byte, v interface{}) ([]byte, bool) {
	switch m := v.(type) {
	case *jsonrpcMessage:
		buf, ok := appendResponse(buf, m)
		return append(buf, '\n'), ok
	case []*jsonrpcMessage:
		buf = append(buf, '[')
		for i, msg := range m {
			if i > 0 {
				buf = append(buf, ',')
			}
			var ok bool
			if buf, ok = appendResponse(buf, msg); !ok {
				return buf, false
			}
		}
		return append(buf, ']', '\n'), true
	}
	return buf, false
}

func appendResponse(buf []byte, m *jsonrpcMessage) ([]byte, bool) {
	if m == nil || !m.resultMarshaled || m.Version != vsn || m.Method != "" || m.Params != nil ||
		m.Error != nil || len(m.Result) == 0 || !plainJSONID(m.ID) {
		return buf, false
	}
	buf = append(buf, `{"jsonrpc":"2.0"`...)
	if len(m.ID) > 0 {
		buf = append(buf, `,"id":`...)
		buf = append(buf, m.ID...)
	}
	buf = append(buf, `,"result":`...)
	buf = append(buf, m.Result...)
	return append(buf, '}'), true
}

// plainJSONID reports whether a request id is a JSON number or a string of
// characters that json.Encoder writes back unchanged (no escapes, no space).
func plainJSONID(id json.RawMessage) bool {
	if len(id) == 0 {
		return true
	}
	if id[0] == '"' {
		if len(id) < 2 || id[len(id)-1] != '"' {
			return false
		}
		for _, c := range id[1 : len(id)-1] {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' || c == '_' || c == '.' || c == ':') {
				return false
			}
		}
		return true
	}
	for i, c := range id {
		if !(c >= '0' && c <= '9' || i == 0 && c == '-') {
			return false
		}
	}
	return !(len(id) == 1 && id[0] == '-')
}

func (c *jsonCodec) remoteAddr() string {
	return c.remote
}

func (c *jsonCodec) readBatch() (messages []*jsonrpcMessage, batch bool, err error) {
	var rawmsg json.RawMessage
	if err := c.decode(&rawmsg); err != nil {
		return nil, false, err
	}
	messages, batch, err = parseMessage(rawmsg)
	if err != nil {
		return nil, false, err
	}
	for i, msg := range messages {
		if msg == nil {
			// Message is JSON 'null'. Replace with zero value so it
			// will be treated like any other invalid message.
			messages[i] = new(jsonrpcMessage)
		}
	}
	return messages, batch, nil
}

func (c *jsonCodec) writeJSON(ctx context.Context, v interface{}) error {
	c.encMu.Lock()
	defer c.encMu.Unlock()

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(defaultWriteTimeout)
	}
	c.conn.SetWriteDeadline(deadline)
	if c.raw != nil {
		if buf, ok := appendResponses(nil, v); ok {
			_, err := c.raw.Write(buf)
			return err
		}
	}
	return c.encode(v)
}

func (c *jsonCodec) close() {
	c.closer.Do(func() {
		close(c.closeCh)
		c.conn.Close()
	})
}

func (c *jsonCodec) closed() <-chan interface{} {
	return c.closeCh
}

func parseMessage(raw json.RawMessage) ([]*jsonrpcMessage, bool, error) {
	if !isBatch(raw) {
		msgs := []*jsonrpcMessage{{}}
		if err := json.Unmarshal(raw, &msgs[0]); err != nil {
			// Return an empty message on parse error, let caller handle validation
			msgs[0] = &jsonrpcMessage{}
		}
		return msgs, false, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		return nil, true, err
	}
	var msgs []*jsonrpcMessage
	for dec.More() {
		if len(msgs) >= maxBatchSize {
			return nil, true, fmt.Errorf("batch request too large, max %d", maxBatchSize)
		}
		msgs = append(msgs, new(jsonrpcMessage))
		if err := dec.Decode(&msgs[len(msgs)-1]); err != nil {
			return nil, true, err
		}
	}
	if _, err := dec.Token(); err != nil {
		return nil, true, err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return nil, true, fmt.Errorf("unexpected trailing data after batch")
		}
		return nil, true, err
	}
	return msgs, true, nil
}

func isBatch(raw json.RawMessage) bool {
	for _, c := range raw {
		// Skip insignificant whitespace per RFC 4627.
		if c == 0x20 || c == 0x09 || c == 0x0a || c == 0x0d {
			continue
		}
		return c == '['
	}
	return false
}

func parsePositionalArguments(rawArgs json.RawMessage, types []reflect.Type) ([]reflect.Value, error) {
	dec := json.NewDecoder(bytes.NewReader(rawArgs))
	var args []reflect.Value
	tok, err := dec.Token()
	switch {
	case err == io.EOF || tok == nil && err == nil:
	case err != nil:
		return nil, err
	case tok == json.Delim('['):
		if args, err = parseArgumentArray(dec, types); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("non-array args")
	}
	for i := len(args); i < len(types); i++ {
		if types[i].Kind() != reflect.Ptr {
			return nil, fmt.Errorf("missing value for required argument %d", i)
		}
		args = append(args, reflect.Zero(types[i]))
	}
	return args, nil
}

func parseArgumentArray(dec *json.Decoder, types []reflect.Type) ([]reflect.Value, error) {
	args := make([]reflect.Value, 0, len(types))
	for i := 0; dec.More(); i++ {
		if i >= len(types) {
			return args, fmt.Errorf("too many arguments, want at most %d", len(types))
		}
		argval := reflect.New(types[i])
		if err := dec.Decode(argval.Interface()); err != nil {
			return args, fmt.Errorf("invalid argument %d: %v", i, err)
		}
		if argval.IsNil() && types[i].Kind() != reflect.Ptr {
			return args, fmt.Errorf("missing value for required argument %d", i)
		}
		args = append(args, argval.Elem())
	}
	_, err := dec.Token()
	return args, err
}

func parseSubscriptionName(rawArgs json.RawMessage) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(rawArgs))
	if tok, _ := dec.Token(); tok != json.Delim('[') {
		return "", errors.New("non-array args")
	}
	v, _ := dec.Token()
	method, ok := v.(string)
	if !ok {
		return "", errors.New("expected subscription name as first argument")
	}
	return method, nil
}

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

package v5wire

import (
	"testing"

	"github.com/n42blockchain/N42/common/rlp"
)

// msgNameKind is satisfied by every Packet implementation and lets the
// table test below drive Name/Kind/RequestID/SetRequestID/AppendLogInfo
// uniformly.
func TestPacketAccessors(t *testing.T) {
	packets := []Packet{
		&Whoareyou{},
		&Unknown{},
		&Ping{ReqID: []byte{1}},
		&Pong{ReqID: []byte{1}},
		&Findnode{ReqID: []byte{1}, OpID: 7},
		&Nodes{ReqID: []byte{1}},
		&TalkRequest{ReqID: []byte{1}, Protocol: "p"},
		&TalkResponse{ReqID: []byte{1}},
	}
	for _, p := range packets {
		if p.Name() == "" {
			t.Errorf("%T: Name() is empty", p)
		}
		_ = p.Kind()
		_ = p.AppendLogInfo(nil)
		p.SetRequestID([]byte{9})
		if got := p.RequestID(); got != nil && string(got) != string([]byte{9}) {
			t.Errorf("%T: RequestID() = %x after SetRequestID", p, got)
		}
	}
}

func TestFindnodeAppendLogInfoNoOpID(t *testing.T) {
	f := &Findnode{ReqID: []byte{1}}
	info := f.AppendLogInfo(nil)
	for i := 0; i < len(info); i++ {
		if info[i] == "opid" {
			t.Fatal("did not expect opid in log info when OpID is zero")
		}
	}
}

func TestDecodeMessageDispatch(t *testing.T) {
	tests := []struct {
		name  string
		ptype byte
		body  func() []byte
		want  Packet
	}{
		{"ping", PingMsg, func() []byte { b, _ := rlp.EncodeToBytes(&Ping{ReqID: []byte{1}}); return b }, &Ping{}},
		{"pong", PongMsg, func() []byte { b, _ := rlp.EncodeToBytes(&Pong{ReqID: []byte{1}}); return b }, &Pong{}},
		{"findnode", FindnodeMsg, func() []byte { b, _ := rlp.EncodeToBytes(&Findnode{ReqID: []byte{1}}); return b }, &Findnode{}},
		{"nodes", NodesMsg, func() []byte { b, _ := rlp.EncodeToBytes(&Nodes{ReqID: []byte{1}}); return b }, &Nodes{}},
		{"talkreq", TalkRequestMsg, func() []byte { b, _ := rlp.EncodeToBytes(&TalkRequest{ReqID: []byte{1}}); return b }, &TalkRequest{}},
		{"talkresp", TalkResponseMsg, func() []byte { b, _ := rlp.EncodeToBytes(&TalkResponse{ReqID: []byte{1}}); return b }, &TalkResponse{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := DecodeMessage(tt.ptype, tt.body())
			if err != nil {
				t.Fatalf("DecodeMessage: %v", err)
			}
			if p.Kind() != tt.want.Kind() {
				t.Fatalf("Kind() = %d, want %d", p.Kind(), tt.want.Kind())
			}
		})
	}
}

func TestDecodeMessageUnknownType(t *testing.T) {
	if _, err := DecodeMessage(0x99, nil); err == nil {
		t.Fatal("expected error for unknown packet type")
	}
}

func TestDecodeMessageInvalidReqID(t *testing.T) {
	body, _ := rlp.EncodeToBytes(&Ping{ReqID: make([]byte, 9)})
	if _, err := DecodeMessage(PingMsg, body); err != ErrInvalidReqID {
		t.Fatalf("got %v, want ErrInvalidReqID", err)
	}
}

func TestDecodeMessageBadRLP(t *testing.T) {
	if _, err := DecodeMessage(PingMsg, []byte{0xff, 0xff}); err == nil {
		t.Fatal("expected RLP decode error")
	}
}

func TestIsInvalidHeader(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errTooShort, true},
		{errInvalidHeader, true},
		{errMsgTooShort, true},
		{errUnexpectedHandshake, false},
		{nil, false},
	}
	for _, c := range cases {
		if got := IsInvalidHeader(c.err); got != c.want {
			t.Errorf("IsInvalidHeader(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

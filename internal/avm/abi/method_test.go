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

package abi

import (
	"strings"
	"testing"
)

const methoddata = `
[
	{"type": "function", "name": "balance", "stateMutability": "view"},
	{"type": "function", "name": "send", "inputs": [{ "name": "amount", "type": "uint256" }]},
	{"type": "function", "name": "transfer", "inputs": [{"name": "from", "type": "address"}, {"name": "to", "type": "address"}, {"name": "value", "type": "uint256"}], "outputs": [{"name": "success", "type": "bool"}]},
	{"constant":false,"inputs":[{"components":[{"name":"x","type":"uint256"},{"name":"y","type":"uint256"}],"name":"a","type":"tuple"}],"name":"tuple","outputs":[],"payable":false,"stateMutability":"nonpayable","type":"function"},
	{"constant":false,"inputs":[{"components":[{"name":"x","type":"uint256"},{"name":"y","type":"uint256"}],"name":"a","type":"tuple[]"}],"name":"tupleSlice","outputs":[],"payable":false,"stateMutability":"nonpayable","type":"function"},
	{"constant":false,"inputs":[{"components":[{"name":"x","type":"uint256"},{"name":"y","type":"uint256"}],"name":"a","type":"tuple[5]"}],"name":"tupleArray","outputs":[],"payable":false,"stateMutability":"nonpayable","type":"function"},
	{"constant":false,"inputs":[{"components":[{"name":"x","type":"uint256"},{"name":"y","type":"uint256"}],"name":"a","type":"tuple[5][]"}],"name":"complexTuple","outputs":[],"payable":false,"stateMutability":"nonpayable","type":"function"},
	{"stateMutability":"nonpayable","type":"fallback"},
	{"stateMutability":"payable","type":"receive"}
]`

func TestMethodString(t *testing.T) {
	tests := []struct {
		method string
		want   string
	}{
		{"balance", "function balance() view returns()"},
		{"send", "function send(uint256 amount) returns()"},
		{"transfer", "function transfer(address from, address to, uint256 value) returns(bool success)"},
		{"tuple", "function tuple((uint256,uint256) a) returns()"},
		{"tupleArray", "function tupleArray((uint256,uint256)[5] a) returns()"},
		{"tupleSlice", "function tupleSlice((uint256,uint256)[] a) returns()"},
		{"complexTuple", "function complexTuple((uint256,uint256)[5][] a) returns()"},
		{"fallback", "fallback() returns()"},
		{"receive", "receive() payable returns()"},
	}

	abi, err := JSON(strings.NewReader(methoddata))
	if err != nil {
		t.Fatal(err)
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			var got string
			switch tt.method {
			case "fallback":
				got = abi.Fallback.String()
			case "receive":
				got = abi.Receive.String()
			default:
				got = abi.Methods[tt.method].String()
			}
			if got != tt.want {
				t.Errorf("expected %s, got %s", tt.want, got)
			}
		})
	}
}

func TestMethodSig(t *testing.T) {
	tests := []struct {
		method string
		want   string
	}{
		{"balance", "balance()"},
		{"send", "send(uint256)"},
		{"transfer", "transfer(address,address,uint256)"},
		{"tuple", "tuple((uint256,uint256))"},
		{"tupleArray", "tupleArray((uint256,uint256)[5])"},
		{"tupleSlice", "tupleSlice((uint256,uint256)[])"},
		{"complexTuple", "complexTuple((uint256,uint256)[5][])"},
	}

	abi, err := JSON(strings.NewReader(methoddata))
	if err != nil {
		t.Fatal(err)
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			got := abi.Methods[tt.method].Sig
			if got != tt.want {
				t.Errorf("expected %s, got %s", tt.want, got)
			}
		})
	}
}

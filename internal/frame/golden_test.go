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

package frame

import (
	"testing"

	"github.com/n42blockchain/N42/crypto"
)

// Golden values of a fixed v1 frame: the FrameHash and keccak of the whole
// encoding (header, payload and signature). They pin the v1 wire bytes and
// hash so a change to the hash definition cannot silently alter them.
const (
	goldenKeyHex     = "4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318"
	goldenV1Hash     = "0xd8fa4cdb53e336cfa1bd78037df06ad30e8cb01c8daab7e2db5971bc8fb5840b"
	goldenV1RawKecc  = "0xaef441b8f994e0391f81024826f804a13381226d7793b74f0a55e95476382df1"
	goldenV1RawBytes = 621
)

func goldenFrame(t *testing.T) []byte {
	t.Helper()
	key, err := crypto.HexToECDSA(goldenKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := Encode(key, 7, TxTypeEthereum, testTxs(5))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestV1Golden(t *testing.T) {
	defer SetHashV2ForTest(false)()
	raw := goldenFrame(t)
	f, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	h, err := f.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if got := h.Hex(); got != goldenV1Hash {
		t.Fatalf("v1 frame hash %s, want %s", got, goldenV1Hash)
	}
	if got := crypto.Keccak256Hash(raw).Hex(); got != goldenV1RawKecc {
		t.Fatalf("v1 encoding keccak %s, want %s", got, goldenV1RawKecc)
	}
	if len(raw) != goldenV1RawBytes {
		t.Fatalf("v1 encoding %d bytes, want %d", len(raw), goldenV1RawBytes)
	}
}

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

package devp2p

import (
	"bytes"
	"testing"

	gethp2p "github.com/ethereum/go-ethereum/p2p"

	n42block "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/rlp"
)

// balFakeProvider implements BlockProvider + the optional balServer.
type balFakeProvider struct {
	bals map[types.Hash][]byte
}

func (p *balFakeProvider) CurrentHead() (*n42block.Header, types.Hash, error) {
	return nil, types.Hash{}, nil
}
func (p *balFakeProvider) GetHeaderByNumber(uint64) (*n42block.Header, error) { return nil, nil }
func (p *balFakeProvider) GetHeaderByHash(types.Hash) (*n42block.Header, error) {
	return nil, nil
}
func (p *balFakeProvider) BlockAccessList(hash types.Hash) []byte { return p.bals[hash] }

func TestHandleGetBlockAccessListsServes(t *testing.T) {
	h1 := types.HexToHash("0x11")
	h2 := types.HexToHash("0x22") // not held
	h3 := types.HexToHash("0x33")
	provider := &balFakeProvider{bals: map[types.Hash][]byte{
		h1: {0xaa, 0xbb},
		h3: {0xcc},
	}}
	h := &EthHandler{provider: provider}

	// Build the incoming request message.
	enc, err := rlp.EncodeToBytes(&getBlockAccessListsPacket{RequestID: 42, Hashes: []types.Hash{h1, h2, h3}})
	if err != nil {
		t.Fatal(err)
	}
	msg := gethp2p.Msg{Code: 18, Size: uint32(len(enc)), Payload: bytes.NewReader(enc)}

	// Pipe: the handler sends the response on one end; we read it on the other.
	rw1, rw2 := gethp2p.MsgPipe()
	defer rw1.Close()
	defer rw2.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- h.handleGetBlockAccessLists(rw1, msg) }()

	respMsg, err := rw2.ReadMsg()
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if respMsg.Code != 19 {
		t.Fatalf("response code = %d, want 19 (BlockAccessLists)", respMsg.Code)
	}
	var resp blockAccessListsPacket
	if err := respMsg.Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("handler error: %v", err)
	}

	if resp.RequestID != 42 {
		t.Fatalf("RequestID = %d, want 42", resp.RequestID)
	}
	if len(resp.BALs) != 3 {
		t.Fatalf("BALs len = %d, want 3 (request order preserved)", len(resp.BALs))
	}
	if !bytes.Equal(resp.BALs[0], []byte{0xaa, 0xbb}) {
		t.Fatalf("h1 BAL = %x, want aabb", resp.BALs[0])
	}
	if len(resp.BALs[1]) != 0 {
		t.Fatalf("h2 (not held) should be empty, got %x", resp.BALs[1])
	}
	if !bytes.Equal(resp.BALs[2], []byte{0xcc}) {
		t.Fatalf("h3 BAL = %x, want cc", resp.BALs[2])
	}
}

// balNoServeProvider implements BlockProvider but NOT balServer.
type balNoServeProvider struct{}

func (balNoServeProvider) CurrentHead() (*n42block.Header, types.Hash, error) {
	return nil, types.Hash{}, nil
}
func (balNoServeProvider) GetHeaderByNumber(uint64) (*n42block.Header, error) { return nil, nil }
func (balNoServeProvider) GetHeaderByHash(types.Hash) (*n42block.Header, error) {
	return nil, nil
}

func TestHandleGetBlockAccessListsEmptyWhenUnsupported(t *testing.T) {
	h := &EthHandler{provider: balNoServeProvider{}}
	enc, _ := rlp.EncodeToBytes(&getBlockAccessListsPacket{RequestID: 1, Hashes: []types.Hash{types.HexToHash("0x11")}})
	msg := gethp2p.Msg{Code: 18, Size: uint32(len(enc)), Payload: bytes.NewReader(enc)}

	rw1, rw2 := gethp2p.MsgPipe()
	defer rw1.Close()
	defer rw2.Close()
	go func() { _ = h.handleGetBlockAccessLists(rw1, msg) }()

	respMsg, err := rw2.ReadMsg()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var resp blockAccessListsPacket
	if err := respMsg.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.BALs) != 1 || len(resp.BALs[0]) != 0 {
		t.Fatalf("provider without balServer must answer empty entries, got %+v", resp.BALs)
	}
}

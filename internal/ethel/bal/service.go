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
// Out-of-band Block Access List service (eth-el only). EIP-7928 binds only the
// BAL hash into the header; the full BAL travels out of band so a consumer can
// prefetch state and validate without re-deriving. This mirrors geth's eth/71
// GetBlockAccessLists (0x12) / BlockAccessLists (0x13) request-response and
// reth's RawBal-with-block: request full BALs by block hash, answer with the
// stored raw RLP per hash. The devp2p wire registration is layered on top; this
// file is the codec + serving core, storage-backend-agnostic (a lookup closure).

package bal

import (
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/rlp"
)

// MaxBALRequest caps how many block access lists one request may ask for, so a
// peer cannot force an unbounded response (mirrors eth/6x batch caps).
const MaxBALRequest = 256

// GetBlockAccessLists is the request: the block hashes whose full BALs are wanted.
type GetBlockAccessLists struct {
	Hashes []types.Hash
}

// balEntry is one answered (blockHash, raw BAL RLP) pair. An empty Raw means the
// responder does not have that block's BAL.
type balEntry struct {
	Hash types.Hash
	Raw  []byte
}

// BlockAccessLists is the response: one raw BAL per requested hash, in request
// order. Absent BALs carry empty Raw so the requester can pair by index.
type BlockAccessLists struct {
	Entries []balEntry
}

// EncodeRequest / DecodeRequest / EncodeResponse / DecodeResponse are the wire
// codecs for the two messages.
func (r *GetBlockAccessLists) Encode() ([]byte, error) { return rlp.EncodeToBytes(r) }

func DecodeGetBlockAccessLists(b []byte) (*GetBlockAccessLists, error) {
	r := new(GetBlockAccessLists)
	if err := rlp.DecodeBytes(b, r); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *BlockAccessLists) Encode() ([]byte, error) { return rlp.EncodeToBytes(r) }

func DecodeBlockAccessLists(b []byte) (*BlockAccessLists, error) {
	r := new(BlockAccessLists)
	if err := rlp.DecodeBytes(b, r); err != nil {
		return nil, err
	}
	return r, nil
}

// Get returns the raw BAL for hash and whether it was present (non-empty).
func (r *BlockAccessLists) Get(hash types.Hash) ([]byte, bool) {
	for i := range r.Entries {
		if r.Entries[i].Hash == hash {
			return r.Entries[i].Raw, len(r.Entries[i].Raw) > 0
		}
	}
	return nil, false
}

// ServeGetBlockAccessLists answers a request by looking up each requested block's
// stored raw BAL via lookup (e.g. a rawdb read). lookup returns nil for a hash it
// does not have; that hash is answered with an empty entry. The request is capped
// at MaxBALRequest hashes.
func ServeGetBlockAccessLists(req *GetBlockAccessLists, lookup func(types.Hash) []byte) *BlockAccessLists {
	n := len(req.Hashes)
	if n > MaxBALRequest {
		n = MaxBALRequest
	}
	resp := &BlockAccessLists{Entries: make([]balEntry, 0, n)}
	for _, h := range req.Hashes[:n] {
		resp.Entries = append(resp.Entries, balEntry{Hash: h, Raw: lookup(h)})
	}
	return resp
}

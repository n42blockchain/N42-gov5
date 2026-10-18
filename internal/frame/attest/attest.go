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

// Package attest is frame-level sender attestation (P8b,
// docs/QS_MULTI_MILLION_DESIGN.md): each frame is assigned to a committee of
// validators who recover its senders and sign (frame hash, senders digest);
// a node holding f+1 matching attestations and the senders list takes the
// senders without recovering them. f+1 agreeing results of a deterministic
// function include at least one honest validator, so the list is the one
// an honest recovery gives. A frame without enough attestations is
// recovered locally, so liveness never depends on the committee, and
// nothing here touches a block's hash or state root.
package attest

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strconv"
	"unsafe"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/frame"
)

// Environment.
const (
	// EnvEnable turns attestation on (N42_FRAME_ATTEST=1; default off).
	EnvEnable = "N42_FRAME_ATTEST"
	// EnvCommittee is the committee size (default 3; raised to f+1).
	EnvCommittee = "N42_FRAME_ATTEST_COMMITTEE"
	// EnvFlushMs is how long a committee member batches attestations
	// before signing and sending them (default 20 ms).
	EnvFlushMs = "N42_FRAME_ATTEST_FLUSH_MS"
	// EnvPeers lists the ingest addresses attestations are pushed to
	// (default N42_FRAME_PULL_PEERS); this node's own listener is skipped.
	EnvPeers = "N42_FRAME_ATTEST_PEERS"
)

// Magic ("FAT1" little-endian) selects an attestation batch on the ingest
// listener. The message is Magic u32 | body length u32 | body, with no
// reply.
const Magic uint32 = 0x31544146

// Domain separates the attestation signature from every other use of the
// validator key.
const Domain = "n42-frame-attest-v1"

// MaxEntries bounds the frames of one batch; MaxBody bounds its encoding.
const (
	MaxEntries = 256
	MaxBody    = 8 << 20
)

// Enabled reports N42_FRAME_ATTEST=1.
func Enabled() bool { return os.Getenv(EnvEnable) == "1" }

// CommitteeSizeFromEnv reads N42_FRAME_ATTEST_COMMITTEE (default 3).
func CommitteeSizeFromEnv() int {
	if n, err := strconv.Atoi(os.Getenv(EnvCommittee)); err == nil && n > 0 {
		return n
	}
	return 3
}

// Committee returns frame h's committee as indexes into the validator set
// sorted by address ascending (n validators): start = h mod n (h read as a
// big-endian 256-bit integer), then start+1, ..., start+size-1 mod n.
// Every node computes the same committee from the same set.
func Committee(h types.Hash, n, size int) []int {
	if n <= 0 || size <= 0 {
		return nil
	}
	if size > n {
		size = n
	}
	var r uint64
	for _, b := range h {
		r = (r<<8 | uint64(b)) % uint64(n)
	}
	out := make([]int, size)
	for k := range out {
		out[k] = (int(r) + k) % n
	}
	return out
}

// Digest is keccak256 of the concatenated 20-byte senders in transaction
// order (a zero address marks an entry whose signature did not recover).
func Digest(addrs []types.Address) types.Hash {
	return crypto.Keccak256Hash(addressBytes(addrs))
}

// addressBytes views addrs as the concatenation of its 20-byte addresses
// (an array of [20]byte has no padding), so a digest hashes the list in
// place instead of copying it into a fresh buffer first.
func addressBytes(addrs []types.Address) []byte {
	if len(addrs) == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(&addrs[0])), len(addrs)*types.AddressLength) // #nosec G103 -- [20]byte elements, contiguous
}

// bytesAddresses views b (a multiple of 20 bytes) as addresses without
// copying; the result aliases b. types.Address has alignment 1.
func bytesAddresses(b []byte) []types.Address {
	if len(b) == 0 {
		return nil
	}
	return unsafe.Slice((*types.Address)(unsafe.Pointer(&b[0])), len(b)/types.AddressLength) // #nosec G103 -- alignment 1, length checked by the caller
}

// Entry attests one frame: its hash, the digest of its senders and, when
// carried, the senders themselves (nil = digest only).
type Entry struct {
	Frame   types.Hash
	Digest  types.Hash
	Senders []types.Address
}

// Batch is one signed message of a validator's attestations.
type Batch struct {
	Attester types.Address
	Entries  []Entry
	Sig      []byte
}

// SigningHash is what the attester signs: keccak256(Domain || attester ||
// count u32 || (frame || digest) per entry). The senders are bound by the
// digest.
func SigningHash(attester types.Address, entries []Entry) types.Hash {
	buf := make([]byte, 0, len(Domain)+types.AddressLength+4+len(entries)*2*types.HashLength)
	buf = append(buf, Domain...)
	buf = append(buf, attester[:]...)
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(entries))) // #nosec G115 -- bounded by MaxEntries
	for _, e := range entries {
		buf = append(buf, e.Frame[:]...)
		buf = append(buf, e.Digest[:]...)
	}
	return crypto.Keccak256Hash(buf)
}

// EncodeBody encodes b: attester 20 | count u16 | entries | sigLen u16 |
// sig, an entry being frame 32 | digest 32 | n u16 | n x 20-byte sender.
func EncodeBody(b *Batch) []byte {
	size := types.AddressLength + 2 + 2 + len(b.Sig)
	for _, e := range b.Entries {
		size += 2*types.HashLength + 2 + len(e.Senders)*types.AddressLength
	}
	out := make([]byte, 0, size)
	out = append(out, b.Attester[:]...)
	out = binary.LittleEndian.AppendUint16(out, uint16(len(b.Entries))) // #nosec G115 -- bounded by MaxEntries
	for _, e := range b.Entries {
		out = append(out, e.Frame[:]...)
		out = append(out, e.Digest[:]...)
		out = binary.LittleEndian.AppendUint16(out, uint16(len(e.Senders))) // #nosec G115 -- bounded by frame.MaxTxs
		for _, a := range e.Senders {
			out = append(out, a[:]...)
		}
	}
	out = binary.LittleEndian.AppendUint16(out, uint16(len(b.Sig))) // #nosec G115 -- a BLS signature
	return append(out, b.Sig...)
}

// EncodeWire is the stream form: Magic | body length | body.
func EncodeWire(b *Batch) []byte {
	body := EncodeBody(b)
	out := make([]byte, 0, 8+len(body))
	out = binary.LittleEndian.AppendUint32(out, Magic)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(body))) // #nosec G115 -- bounded by MaxBody
	return append(out, body...)
}

var errShort = errors.New("attest: short batch")

// DecodeBody parses EncodeBody's output. The entries' Senders alias body
// (no per-entry copy: most carried lists are only hashed, or skipped
// because the list is already known), so body must not be modified while
// the batch is in use; Pool.Add copies the lists it keeps.
func DecodeBody(body []byte) (*Batch, error) {
	if len(body) > MaxBody {
		return nil, fmt.Errorf("attest: batch of %d bytes", len(body))
	}
	b := &Batch{}
	if len(body) < types.AddressLength+2 {
		return nil, errShort
	}
	copy(b.Attester[:], body)
	p := body[types.AddressLength:]
	count := int(binary.LittleEndian.Uint16(p))
	p = p[2:]
	if count > MaxEntries {
		return nil, fmt.Errorf("attest: %d entries", count)
	}
	b.Entries = make([]Entry, count)
	for i := range b.Entries {
		if len(p) < 2*types.HashLength+2 {
			return nil, errShort
		}
		e := &b.Entries[i]
		copy(e.Frame[:], p)
		copy(e.Digest[:], p[types.HashLength:])
		n := int(binary.LittleEndian.Uint16(p[2*types.HashLength:]))
		p = p[2*types.HashLength+2:]
		if n > frame.MaxTxs {
			return nil, fmt.Errorf("attest: %d senders", n)
		}
		if len(p) < n*types.AddressLength {
			return nil, errShort
		}
		if n > 0 {
			e.Senders = bytesAddresses(p[: n*types.AddressLength : n*types.AddressLength])
		}
		p = p[n*types.AddressLength:]
	}
	if len(p) < 2 {
		return nil, errShort
	}
	sl := int(binary.LittleEndian.Uint16(p))
	p = p[2:]
	if len(p) != sl {
		return nil, errShort
	}
	b.Sig = append([]byte(nil), p...)
	return b, nil
}

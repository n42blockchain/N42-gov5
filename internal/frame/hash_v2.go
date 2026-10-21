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
	"os"
	"sync/atomic"
	"unsafe"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// Frame hash v2 (N42_FRAME_HASH_V2=1).
//
// v1 hashes every payload byte into the FrameHash, and the import hashes
// every transaction again for its own hash: each transaction byte goes
// through keccak twice. v2 hashes each entry once and builds the FrameHash
// over the entry hashes:
//
//	EntryHash_i = keccak256(tx bytes of entry i)        (without its u16 length)
//	FrameHash   = keccak256(DomainV2 || header || EntryHash_1 || ... || EntryHash_n)
//
// header is the 36-byte wire header exactly as encoded (version, tx type,
// gateway, seq, tx count, payload length), so the count, the type and the
// payload length are bound. For an Ethereum frame's legacy, EIP-2930 and
// EIP-1559 entries EntryHash is the transaction hash itself, which the
// import then takes instead of hashing again (Transaction.SeedHashFromEncoding).
//
// Binding: keccak's collision resistance makes each EntryHash bind its
// entry's exact bytes and length; the sequence binds their order and, with
// the header's tx count, their number. The u16 length prefixes are implied
// by the entries, and the payload length in the header must equal their sum
// exactly: Decode and every v2 hash walk reject a payload with bytes not
// covered by any entry (ErrPayloadLayout) and an encoding with bytes past
// the signature (ErrTrailingBytes). The domain tag keeps v2 preimages apart
// from v1 ones (a v1 preimage starts with the version byte, never 'N').
//
// The wire format is unchanged (the version byte stays 1): only the hash
// differs, so every node of a network, and every gateway (cmd/txflood
// links this package and reads the same variable), must run the same
// setting. It is read once at start.

// EnvFrameHashV2 is N42_FRAME_HASH_V2: "1" selects frame hash v2.
const EnvFrameHashV2 = "N42_FRAME_HASH_V2"

// DomainV2 prefixes every v2 FrameHash preimage.
const DomainV2 = "N42/frame-hash/v2"

var frameHashV2On atomic.Bool

func init() { frameHashV2On.Store(os.Getenv(EnvFrameHashV2) == "1") }

// HashV2Enabled reports N42_FRAME_HASH_V2.
func HashV2Enabled() bool { return frameHashV2On.Load() }

// SetHashV2ForTest switches the frame hash version and returns the restore.
func SetHashV2ForTest(on bool) func() {
	prev := frameHashV2On.Swap(on)
	return func() { frameHashV2On.Store(prev) }
}

// hashV2 is the v2 FrameHash of an encoded header and its entry hashes.
func hashV2(header []byte, entries []types.Hash) types.Hash {
	return crypto.Keccak256Hash([]byte(DomainV2), header[:HeaderSize], hashBytes(entries))
}

// hashBytes views hs as the concatenation of its 32-byte hashes.
func hashBytes(hs []types.Hash) []byte {
	if len(hs) == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(&hs[0])), len(hs)*types.HashLength) // #nosec G103 -- [32]byte elements, contiguous
}

// entryHasher computes keccak256 of each entry, four single-block entries at
// a time on the AVX2 four-way permutation when it is available. Results are
// identical to one crypto.Keccak256Hash per entry.
type entryHasher struct {
	out  []types.Hash
	idx  [4]int
	in   [4][]byte
	res  [4]types.Hash
	n    int
	four bool
}

func newEntryHasher(out []types.Hash) entryHasher {
	return entryHasher{out: out, four: crypto.KeccakOneBlockEnabled && crypto.KeccakX4Enabled}
}

// add hashes entry i (b) into out[i], possibly later (see flush).
func (e *entryHasher) add(i int, b []byte) {
	if !e.four || len(b) >= crypto.Keccak256OneBlockMax {
		e.out[i] = crypto.Keccak256HashOne(b)
		return
	}
	e.idx[e.n], e.in[e.n] = i, b
	e.n++
	if e.n == 4 {
		crypto.Keccak256x4OneBlock(&e.in, &e.res)
		for k := 0; k < 4; k++ {
			e.out[e.idx[k]] = e.res[k]
			e.in[k] = nil
		}
		e.n = 0
	}
}

// flush hashes what add left pending.
func (e *entryHasher) flush() {
	for k := 0; k < e.n; k++ {
		e.out[e.idx[k]] = crypto.Keccak256HashOne(e.in[k])
		e.in[k] = nil
	}
	e.n = 0
}

// entryHashes walks f's payload (checking its layout, trailing bytes
// included) and returns keccak256 of every entry, in order.
func entryHashes(f *Frame) ([]types.Hash, error) {
	out := make([]types.Hash, f.TxCount)
	eh := newEntryHasher(out)
	if err := walkPayload(f.Payload, int(f.TxCount), eh.add); err != nil {
		return nil, err
	}
	eh.flush()
	return out, nil
}

// EntryHashesOf returns keccak256 of every entry of an encoded frame
// (checking its envelope and payload layout), whatever the hash version.
func EntryHashesOf(raw []byte) ([]types.Hash, error) {
	var f Frame
	if err := decodeEnvelope(raw, &f); err != nil {
		return nil, err
	}
	return entryHashes(&f)
}

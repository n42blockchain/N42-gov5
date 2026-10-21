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

// Package frame implements the gateway-signed transaction frame: the unit in
// which transactions enter a node on the order-first ingest path
// (docs/QS_MULTI_MILLION_DESIGN.md section 4.1). A node admits a frame on
// the gateway signature alone; per-transaction signatures are checked later,
// in bulk, off the network path.
//
// Wire layout (all integers little endian):
//
//	header  (36 B) version u8 | tx type u8 | gateway address [20]B |
//	               seq u64 | tx count u16 | payload length u32
//	payload        tx count x (tx length u16 | tx bytes)
//	sig     (65 B) secp256k1 [R || S || V] over FrameHash
//
// FrameHash = keccak256(header || payload). The signature covers the hash,
// which commits to every header field and every payload byte, so one keccak
// pass serves as both identity and signed digest. Under N42_FRAME_HASH_V2=1
// the FrameHash is built over per-entry hashes instead (see hash_v2.go); the
// wire layout is the same.
package frame

import (
	"crypto/ecdsa"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

const (
	// Version is the only frame version this package produces and accepts.
	Version = 1
	// HeaderSize is the fixed header length in bytes.
	HeaderSize = 1 + 1 + types.AddressLength + 8 + 2 + 4
	// SigSize is the secp256k1 recoverable signature length.
	SigSize = 65
	// MaxTxs bounds the transaction count of one frame.
	MaxTxs = 500
	// MaxPayload bounds the payload of one frame (1 MiB).
	MaxPayload = 1 << 20
	// MaxFrameSize is the largest encoded frame.
	MaxFrameSize = HeaderSize + MaxPayload + SigSize
	// txLenSize is the per-transaction length prefix.
	txLenSize = 2
)

// Transaction type tags carried in the header. The gateway types a frame so
// the executor can route it without looking inside (design 4.3).
const (
	// TxTypeEthereum: each entry is an Ethereum-encoded signed transaction
	// (legacy RLP or EIP-2718 typed envelope), what eth_sendRawTransaction takes.
	TxTypeEthereum uint8 = 0
	// TxTypeNative: each entry is the node's native transaction codec.
	TxTypeNative uint8 = 1
)

var (
	ErrShort         = errors.New("frame: truncated")
	ErrVersion       = errors.New("frame: unsupported version")
	ErrTooManyTxs    = errors.New("frame: too many transactions")
	ErrPayloadTooBig = errors.New("frame: payload too large")
	ErrPayloadLayout = errors.New("frame: payload does not match tx count")
	ErrTrailingBytes = errors.New("frame: trailing bytes")
	ErrBadSignature  = errors.New("frame: bad gateway signature")
	ErrWrongGateway  = errors.New("frame: signature does not match gateway")
	ErrEmptyTx       = errors.New("frame: empty transaction")
	ErrTxTooBig      = errors.New("frame: transaction too large")
)

// Header is the fixed-size frame header.
type Header struct {
	Version    uint8
	TxType     uint8
	Gateway    types.Address
	Seq        uint64
	TxCount    uint16
	PayloadLen uint32
}

// Put writes h into b, which must hold HeaderSize bytes.
func (h *Header) Put(b []byte) {
	_ = b[HeaderSize-1]
	b[0] = h.Version
	b[1] = h.TxType
	copy(b[2:22], h.Gateway[:])
	binary.LittleEndian.PutUint64(b[22:30], h.Seq)
	binary.LittleEndian.PutUint16(b[30:32], h.TxCount)
	binary.LittleEndian.PutUint32(b[32:36], h.PayloadLen)
}

// ParseHeader reads and bounds-checks a header. It does not look at the
// payload; a reader uses PayloadLen to size the rest of the frame.
func ParseHeader(b []byte) (Header, error) {
	var h Header
	if len(b) < HeaderSize {
		return h, ErrShort
	}
	h.Version = b[0]
	h.TxType = b[1]
	copy(h.Gateway[:], b[2:22])
	h.Seq = binary.LittleEndian.Uint64(b[22:30])
	h.TxCount = binary.LittleEndian.Uint16(b[30:32])
	h.PayloadLen = binary.LittleEndian.Uint32(b[32:36])
	if h.Version != Version {
		return h, ErrVersion
	}
	if h.TxCount > MaxTxs {
		return h, ErrTooManyTxs
	}
	if h.PayloadLen > MaxPayload {
		return h, ErrPayloadTooBig
	}
	// Every transaction costs at least its prefix and one byte.
	if uint64(h.TxCount)*(txLenSize+1) > uint64(h.PayloadLen) {
		return h, ErrPayloadLayout
	}
	return h, nil
}

// Size is the encoded length of a frame with this header.
func (h *Header) Size() int { return HeaderSize + int(h.PayloadLen) + SigSize }

// GatewayOf returns the gateway address in an encoded frame's header
// without decoding the rest. It does not verify anything.
func GatewayOf(raw []byte) (types.Address, bool) {
	var a types.Address
	if len(raw) < HeaderSize {
		return a, false
	}
	copy(a[:], raw[2:22])
	return a, true
}

// Frame is a decoded view over one encoded frame. Raw holds the whole
// encoding (header || payload || sig); Payload and Sig alias into it, so a
// frame costs one allocation however many transactions it carries.
type Frame struct {
	Header
	Raw     []byte
	Payload []byte
	Sig     []byte
	// TxHashes are keccak256 of each entry, in payload order, filled by
	// Hash (and so Verify) under frame hash v2; nil under v1. For the
	// Ethereum entry types that hash their encoding they are the
	// transaction hashes, kept so the import does not hash them again.
	TxHashes []types.Hash
}

// Decode parses an encoded frame, checks every bound and walks the payload
// once to confirm it holds exactly TxCount well-formed entries. It never
// panics on malformed input and does not verify the signature (see Verify).
func Decode(raw []byte) (*Frame, error) {
	var f Frame
	if err := decodeEnvelope(raw, &f); err != nil {
		return nil, err
	}
	if err := walkPayload(f.Payload, int(f.TxCount), nil); err != nil {
		return nil, err
	}
	return &f, nil
}

// decodeEnvelope is Decode without the payload walk: the header, its
// bounds and the total length. Every caller walks the payload after it.
func decodeEnvelope(raw []byte, f *Frame) error {
	h, err := ParseHeader(raw)
	if err != nil {
		return err
	}
	n := h.Size()
	if len(raw) < n {
		return ErrShort
	}
	if len(raw) > n {
		return ErrTrailingBytes
	}
	*f = Frame{
		Header:  h,
		Raw:     raw,
		Payload: raw[HeaderSize : HeaderSize+int(h.PayloadLen)],
		Sig:     raw[HeaderSize+int(h.PayloadLen):],
	}
	return nil
}

// walkPayload checks the payload layout and, when fn is non-nil, hands it
// each transaction's bytes (aliasing the payload).
func walkPayload(p []byte, count int, fn func(i int, tx []byte)) error {
	off := 0
	for i := 0; i < count; i++ {
		if len(p)-off < txLenSize {
			return ErrPayloadLayout
		}
		l := int(binary.LittleEndian.Uint16(p[off:]))
		off += txLenSize
		if l == 0 {
			return ErrEmptyTx
		}
		if len(p)-off < l {
			return ErrPayloadLayout
		}
		if fn != nil {
			fn(i, p[off:off+l])
		}
		off += l
	}
	if off != len(p) {
		return ErrPayloadLayout
	}
	return nil
}

// Hash returns the FrameHash: keccak256(header || payload) under v1; under
// v2 the hash over the entry hashes, which it computes once into f.TxHashes.
// Under v2 a payload whose layout is invalid (which Decode rejects) hashes
// to the zero hash, which no signature recovers to a gateway.
func (f *Frame) Hash() types.Hash {
	if !HashV2Enabled() {
		return crypto.Keccak256Hash(f.Raw[:HeaderSize+int(f.PayloadLen)])
	}
	if f.TxHashes == nil || len(f.TxHashes) != int(f.TxCount) {
		hs, err := entryHashes(f)
		if err != nil {
			return types.Hash{}
		}
		f.TxHashes = hs
	}
	return hashV2(f.Raw, f.TxHashes)
}

// Txs returns the transaction encodings, aliasing the frame's buffer.
func (f *Frame) Txs() [][]byte {
	out := make([][]byte, 0, f.TxCount)
	// Decode already validated the layout; the error cannot occur.
	_ = walkPayload(f.Payload, int(f.TxCount), func(_ int, tx []byte) { out = append(out, tx) })
	return out
}

// Verify checks the gateway signature: one ecrecover over the FrameHash,
// compared with the header's gateway address. It returns the FrameHash so
// the caller pays for the keccak once. Under v2 the payload is hashed
// entry by entry only (f.TxHashes), never as a whole.
func (f *Frame) Verify() (types.Hash, error) {
	h := f.Hash()
	if h == (types.Hash{}) {
		return h, ErrPayloadLayout
	}
	pub, err := crypto.SigToPub(h[:], f.Sig)
	if err != nil {
		return h, fmt.Errorf("%w: %v", ErrBadSignature, err)
	}
	if crypto.PubkeyToAddress(*pub) != f.Gateway {
		return h, ErrWrongGateway
	}
	return h, nil
}

// Encode packs txs into a frame signed by key. The gateway address in the
// header is the key's address. It returns the encoding and the FrameHash.
func Encode(key *ecdsa.PrivateKey, seq uint64, txType uint8, txs [][]byte) ([]byte, types.Hash, error) {
	return EncodeWith(KeySigner(key), seq, txType, txs)
}

// Signer signs frames: Address goes into the header's gateway field and
// SignHash returns the 65-byte recoverable secp256k1 signature of a 32-byte
// FrameHash under Address's key.
type Signer struct {
	Address  types.Address
	SignHash func(hash []byte) ([]byte, error)
}

// KeySigner returns a Signer over a private key.
func KeySigner(key *ecdsa.PrivateKey) *Signer {
	return &Signer{
		Address:  crypto.PubkeyToAddress(key.PublicKey),
		SignHash: func(hash []byte) ([]byte, error) { return crypto.Sign(hash, key) },
	}
}

// EncodeWith is Encode with the signature produced by s.
func EncodeWith(s *Signer, seq uint64, txType uint8, txs [][]byte) ([]byte, types.Hash, error) {
	if len(txs) > MaxTxs {
		return nil, types.Hash{}, ErrTooManyTxs
	}
	payload := 0
	for _, tx := range txs {
		if len(tx) == 0 {
			return nil, types.Hash{}, ErrEmptyTx
		}
		if len(tx) > 0xFFFF {
			return nil, types.Hash{}, ErrTxTooBig
		}
		payload += txLenSize + len(tx)
	}
	if payload > MaxPayload {
		return nil, types.Hash{}, ErrPayloadTooBig
	}
	h := Header{
		Version:    Version,
		TxType:     txType,
		Gateway:    s.Address,
		Seq:        seq,
		TxCount:    uint16(len(txs)), // #nosec G115 -- bounded by MaxTxs
		PayloadLen: uint32(payload),  // #nosec G115 -- bounded by MaxPayload
	}
	raw := make([]byte, h.Size())
	h.Put(raw)
	off := HeaderSize
	for _, tx := range txs {
		binary.LittleEndian.PutUint16(raw[off:], uint16(len(tx))) // #nosec G115 -- checked above
		off += txLenSize
		off += copy(raw[off:], tx)
	}
	var hash types.Hash
	if HashV2Enabled() {
		hs := make([]types.Hash, len(txs))
		eh := newEntryHasher(hs)
		for i, tx := range txs {
			eh.add(i, tx)
		}
		eh.flush()
		hash = hashV2(raw, hs)
	} else {
		hash = crypto.Keccak256Hash(raw[:off])
	}
	sig, err := s.SignHash(hash[:])
	if err != nil {
		return nil, types.Hash{}, err
	}
	if len(sig) != SigSize {
		return nil, types.Hash{}, fmt.Errorf("%w: signature of %d bytes", ErrBadSignature, len(sig))
	}
	copy(raw[off:], sig)
	return raw, hash, nil
}

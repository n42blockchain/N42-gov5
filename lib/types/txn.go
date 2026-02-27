/*
   Copyright 2021 The Erigon contributors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package types

import (
	"encoding/binary"
	"fmt"
	"hash"
	"io"
	"math/bits"

	gokzg4844 "github.com/crate-crypto/go-kzg-4844"
	"github.com/erigontech/secp256k1"
	"github.com/holiman/uint256"
	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/lib/common"
	"github.com/n42blockchain/N42/lib/common/fixedgas"
	"github.com/n42blockchain/N42/lib/common/u256"
	"github.com/n42blockchain/N42/lib/crypto"
	"github.com/n42blockchain/N42/lib/rlp"
)

// TxParseContext is object that is required to parse transactions and turn transaction payload into TxSlot objects
// usage of TxContext helps avoid extra memory allocations
type TxParseContext struct {
	Signature
	Keccak2         hash.Hash
	Keccak1         hash.Hash
	validateRlp     func([]byte) error
	cfg             TxParseConfig
	buf             [65]byte // buffer needs to be enough for hashes (32 bytes) and for public key (65 bytes)
	Sig             [65]byte
	Sighash         [32]byte
	withSender      bool
	allowPreEip2s   bool // Allow s > secp256k1n/2; see EIP-2
	chainIDRequired bool
}

func NewTxParseContext(chainID uint256.Int) *TxParseContext {
	if chainID.IsZero() {
		panic("wrong chainID")
	}
	ctx := &TxParseContext{
		withSender: true,
		Keccak1:    sha3.NewLegacyKeccak256(),
		Keccak2:    sha3.NewLegacyKeccak256(),
	}
	ctx.cfg.ChainID.Set(&chainID)
	return ctx
}

func (ctx *TxParseContext) ValidateRLP(f func(txnRlp []byte) error) { ctx.validateRlp = f }
func (ctx *TxParseContext) WithSender(v bool)                       { ctx.withSender = v }
func (ctx *TxParseContext) WithAllowPreEip2s(v bool)                { ctx.allowPreEip2s = v }

func (ctx *TxParseContext) ChainIDRequired() *TxParseContext {
	ctx.chainIDRequired = true
	return ctx
}

// ParseTransaction extracts all the information from the transactions's payload (RLP) necessary to build TxSlot.
// It also performs syntactic validation of the transactions.
// wrappedWithBlobs means that for blob (type 3) transactions the full version with blobs/commitments/proofs is expected
// (see https://eips.ethereum.org/EIPS/eip-4844#networking).
func (ctx *TxParseContext) ParseTransaction(payload []byte, pos int, slot *TxSlot, sender []byte, hasEnvelope, wrappedWithBlobs bool, validateHash func([]byte) error) (p int, err error) {
	if len(payload) == 0 {
		return 0, fmt.Errorf("%w: empty rlp", ErrParseTxn)
	}
	if ctx.withSender && len(sender) != 20 {
		return 0, fmt.Errorf("%w: expect sender buffer of len 20", ErrParseTxn)
	}

	// Legacy transactions have list Prefix, whereas EIP-2718 transactions have string Prefix
	// therefore we assign the first returned value of Prefix function (list) to legacy variable
	dataPos, dataLen, legacy, err := rlp.Prefix(payload, pos)
	if err != nil {
		return 0, fmt.Errorf("%w: size Prefix: %s", ErrParseTxn, err) //nolint
	}
	if dataLen == 0 {
		return 0, fmt.Errorf("%w: transaction must be either 1 list or 1 string", ErrParseTxn)
	}
	if dataLen == 1 && !legacy {
		if hasEnvelope {
			return 0, fmt.Errorf("%w: expected envelope in the payload, got %x", ErrParseTxn, payload[dataPos:dataPos+dataLen])
		}
	}

	p = dataPos

	var wrapperDataPos, wrapperDataLen int

	if !legacy {
		slot.Type = payload[p]
		if slot.Type > SetCodeTxType {
			return 0, fmt.Errorf("%w: unknown transaction type: %d", ErrParseTxn, slot.Type)
		}
		p++
		if p >= len(payload) {
			return 0, fmt.Errorf("%w: unexpected end of payload after txType", ErrParseTxn)
		}
		dataPos, dataLen, err = rlp.ParseList(payload, p)
		if err != nil {
			return 0, fmt.Errorf("%w: envelope Prefix: %s", ErrParseTxn, err) //nolint
		}
		slot.Rlp = payload[p-1 : dataPos+dataLen]

		if slot.Type == BlobTxType && wrappedWithBlobs {
			p = dataPos
			wrapperDataPos = dataPos
			wrapperDataLen = dataLen
			dataPos, dataLen, err = rlp.ParseList(payload, dataPos)
			if err != nil {
				return 0, fmt.Errorf("%w: wrapped blob tx: %s", ErrParseTxn, err) //nolint
			}
		}
	} else {
		slot.Type = LegacyTxType
		slot.Rlp = payload[pos : dataPos+dataLen]
	}

	p, err = ctx.parseTransactionBody(payload, pos, p, slot, sender, validateHash)
	if err != nil {
		return p, err
	}

	if slot.Type == BlobTxType && wrappedWithBlobs {
		if p != dataPos+dataLen {
			return 0, fmt.Errorf("%w: unexpected leftover after blob tx body", ErrParseTxn)
		}
		p, err = ctx.parseBlobWrapper(payload, p, slot)
		if err != nil {
			return 0, err
		}
		if p != wrapperDataPos+wrapperDataLen {
			return 0, fmt.Errorf("%w: extraneous elements in blobs wrapper", ErrParseTxn)
		}
	}

	slot.Size = uint32(len(slot.Rlp))

	return p, err
}

// parseBlobWrapper parses the blobs, commitments, and proofs from a wrapped blob transaction.
func (ctx *TxParseContext) parseBlobWrapper(payload []byte, pos int, slot *TxSlot) (p int, err error) {
	p = pos

	// Parse blobs
	dataPos, dataLen, err := rlp.ParseList(payload, p)
	if err != nil {
		return 0, fmt.Errorf("%w: blobs len: %s", ErrParseTxn, err) //nolint
	}
	blobPos := dataPos
	for blobPos < dataPos+dataLen {
		blobPos, err = rlp.StringOfLen(payload, blobPos, fixedgas.BlobSize)
		if err != nil {
			return 0, fmt.Errorf("%w: blob: %s", ErrParseTxn, err) //nolint
		}
		slot.Blobs = append(slot.Blobs, payload[blobPos:blobPos+fixedgas.BlobSize])
		blobPos += fixedgas.BlobSize
	}
	if blobPos != dataPos+dataLen {
		return 0, fmt.Errorf("%w: extraneous space in blobs", ErrParseTxn)
	}
	p = blobPos

	// Parse commitments
	dataPos, dataLen, err = rlp.ParseList(payload, p)
	if err != nil {
		return 0, fmt.Errorf("%w: commitments len: %s", ErrParseTxn, err) //nolint
	}
	commitmentPos := dataPos
	for commitmentPos < dataPos+dataLen {
		commitmentPos, err = rlp.StringOfLen(payload, commitmentPos, 48)
		if err != nil {
			return 0, fmt.Errorf("%w: commitment: %s", ErrParseTxn, err) //nolint
		}
		var commitment gokzg4844.KZGCommitment
		copy(commitment[:], payload[commitmentPos:commitmentPos+48])
		slot.Commitments = append(slot.Commitments, commitment)
		commitmentPos += 48
	}
	if commitmentPos != dataPos+dataLen {
		return 0, fmt.Errorf("%w: extraneous space in commitments", ErrParseTxn)
	}
	p = commitmentPos

	// Parse proofs
	dataPos, dataLen, err = rlp.ParseList(payload, p)
	if err != nil {
		return 0, fmt.Errorf("%w: proofs len: %s", ErrParseTxn, err) //nolint
	}
	proofPos := dataPos
	for proofPos < dataPos+dataLen {
		proofPos, err = rlp.StringOfLen(payload, proofPos, 48)
		if err != nil {
			return 0, fmt.Errorf("%w: proof: %s", ErrParseTxn, err) //nolint
		}
		var proof gokzg4844.KZGProof
		copy(proof[:], payload[proofPos:proofPos+48])
		slot.Proofs = append(slot.Proofs, proof)
		proofPos += 48
	}
	if proofPos != dataPos+dataLen {
		return 0, fmt.Errorf("%w: extraneous space in proofs", ErrParseTxn)
	}
	p = proofPos

	return p, nil
}

func parseSignature(payload []byte, pos int, legacy bool, cfgChainId *uint256.Int, sig *Signature) (p int, yParity byte, err error) {
	p = pos

	// Parse V / yParity
	p, err = rlp.U256(payload, p, &sig.V)
	if err != nil {
		return 0, 0, fmt.Errorf("v: %w", err)
	}
	if legacy {
		preEip155 := sig.V.Eq(u256.N27) || sig.V.Eq(u256.N28)
		if preEip155 {
			yParity = byte(sig.V.Uint64() - 27)
			sig.ChainID.Set(cfgChainId)
		} else {
			// EIP-155: Simple replay attack protection
			// V = ChainID * 2 + 35 + yParity
			if sig.V.LtUint64(35) {
				return 0, 0, fmt.Errorf("EIP-155 implies V>=35 (was %d)", sig.V.Uint64())
			}
			sig.ChainID.Sub(&sig.V, u256.N35)
			yParity = byte(sig.ChainID.Uint64() % 2)
			sig.ChainID.Rsh(&sig.ChainID, 1)
			if !sig.ChainID.Eq(cfgChainId) {
				return 0, 0, fmt.Errorf("invalid chainID %s (expected %s)", &sig.ChainID, cfgChainId)
			}
		}
	} else {
		if !sig.V.LtUint64(1 << 8) {
			return 0, 0, fmt.Errorf("v is too big: %s", &sig.V)
		}
		yParity = byte(sig.V.Uint64())
	}

	// Next follows R of the signature
	p, err = rlp.U256(payload, p, &sig.R)
	if err != nil {
		return 0, 0, fmt.Errorf("r: %w", err)
	}
	// Next follows S of the signature
	p, err = rlp.U256(payload, p, &sig.S)
	if err != nil {
		return 0, 0, fmt.Errorf("s: %w", err)
	}

	return p, yParity, nil
}

func (ctx *TxParseContext) parseTransactionBody(payload []byte, pos, p0 int, slot *TxSlot, sender []byte, validateHash func([]byte) error) (p int, err error) {
	p = p0
	legacy := slot.Type == LegacyTxType

	// Compute transaction hash
	ctx.Keccak1.Reset()
	ctx.Keccak2.Reset()
	if !legacy {
		typeByte := []byte{slot.Type}
		if _, err = ctx.Keccak1.Write(typeByte); err != nil {
			return 0, fmt.Errorf("%w: computing IdHash (hashing type Prefix): %s", ErrParseTxn, err) //nolint
		}
		if _, err = ctx.Keccak2.Write(typeByte); err != nil {
			return 0, fmt.Errorf("%w: computing signHash (hashing type Prefix): %s", ErrParseTxn, err) //nolint
		}
		dataPos, dataLen, err := rlp.ParseList(payload, p)
		if err != nil {
			return 0, fmt.Errorf("%w: envelope Prefix: %s", ErrParseTxn, err) //nolint
		}
		if _, err = ctx.Keccak1.Write(payload[p : dataPos+dataLen]); err != nil {
			return 0, fmt.Errorf("%w: computing IdHash (hashing the envelope): %s", ErrParseTxn, err) //nolint
		}
		p = dataPos
	}

	if ctx.validateRlp != nil {
		if err := ctx.validateRlp(slot.Rlp); err != nil {
			return p, err
		}
	}

	// Remember where signing hash data begins (it will need to be wrapped in an RLP list)
	sigHashPos := p
	if !legacy {
		p, err = rlp.U256(payload, p, &ctx.ChainID)
		if err != nil {
			return 0, fmt.Errorf("%w: chainId len: %s", ErrParseTxn, err) //nolint
		}
		if ctx.ChainID.IsZero() {
			if ctx.chainIDRequired {
				return 0, fmt.Errorf("%w: chainID is required", ErrParseTxn)
			}
			ctx.ChainID.Set(&ctx.cfg.ChainID)
		}
		if !ctx.ChainID.Eq(&ctx.cfg.ChainID) {
			return 0, fmt.Errorf("%w: %s, %d (expected %d)", ErrParseTxn, "invalid chainID", ctx.ChainID.Uint64(), ctx.cfg.ChainID.Uint64())
		}
	}
	// Next follows the nonce
	p, slot.Nonce, err = rlp.U64(payload, p)
	if err != nil {
		return 0, fmt.Errorf("%w: nonce: %s", ErrParseTxn, err) //nolint
	}
	// Next follows gas price or tip
	p, err = rlp.U256(payload, p, &slot.Tip)
	if err != nil {
		return 0, fmt.Errorf("%w: tip: %s", ErrParseTxn, err) //nolint
	}
	// Next follows feeCap, but only for dynamic fee transactions
	if slot.Type < DynamicFeeTxType {
		slot.FeeCap = slot.Tip
	} else {
		p, err = rlp.U256(payload, p, &slot.FeeCap)
		if err != nil {
			return 0, fmt.Errorf("%w: feeCap: %s", ErrParseTxn, err) //nolint
		}
	}
	// Next follows gas
	p, slot.Gas, err = rlp.U64(payload, p)
	if err != nil {
		return 0, fmt.Errorf("%w: gas: %s", ErrParseTxn, err) //nolint
	}
	// Next follows the destination address (if present)
	dataPos, dataLen, err := rlp.ParseString(payload, p)
	if err != nil {
		return 0, fmt.Errorf("%w: to len: %s", ErrParseTxn, err) //nolint
	}
	if dataLen != 0 && dataLen != 20 {
		return 0, fmt.Errorf("%w: unexpected length of to field: %d", ErrParseTxn, dataLen)
	}

	slot.Creation = dataLen == 0
	p = dataPos + dataLen
	// Next follows value
	p, err = rlp.U256(payload, p, &slot.Value)
	if err != nil {
		return 0, fmt.Errorf("%w: value: %s", ErrParseTxn, err) //nolint
	}
	// Next goes data, but we are only interesting in its length
	dataPos, dataLen, err = rlp.ParseString(payload, p)
	if err != nil {
		return 0, fmt.Errorf("%w: data len: %s", ErrParseTxn, err) //nolint
	}
	slot.DataLen = dataLen

	// Zero and non-zero bytes are priced differently
	slot.DataNonZeroLen = 0
	for _, byt := range payload[dataPos : dataPos+dataLen] {
		if byt != 0 {
			slot.DataNonZeroLen++
		}
	}

	p = dataPos + dataLen

	// Next follows access list for non-legacy transactions
	if !legacy {
		p, err = ctx.parseAccessList(payload, p, slot)
		if err != nil {
			return 0, err
		}
	}
	if slot.Type == SetCodeTxType {
		p, err = ctx.parseAuthorizations(payload, p, slot)
		if err != nil {
			return 0, err
		}
	}
	if slot.Type == BlobTxType {
		p, err = rlp.U256(payload, p, &slot.BlobFeeCap)
		if err != nil {
			return 0, fmt.Errorf("%w: blob fee cap: %s", ErrParseTxn, err) //nolint
		}
		dataPos, dataLen, err = rlp.ParseList(payload, p)
		if err != nil {
			return 0, fmt.Errorf("%w: blob hashes len: %s", ErrParseTxn, err) //nolint
		}
		hashPos := dataPos
		for hashPos < dataPos+dataLen {
			var hash common.Hash
			hashPos, err = rlp.ParseHash(payload, hashPos, hash[:])
			if err != nil {
				return 0, fmt.Errorf("%w: blob hash: %s", ErrParseTxn, err) //nolint
			}
			slot.BlobHashes = append(slot.BlobHashes, hash)
		}
		if hashPos != dataPos+dataLen {
			return 0, fmt.Errorf("%w: extraneous space in the blob versioned hashes", ErrParseTxn)
		}
		p = dataPos + dataLen
	}
	// This is where the data for Sighash ends
	// Next follows the signature
	var vByte byte
	sigHashEnd := p
	sigHashLen := uint(sigHashEnd - sigHashPos)
	var chainIDBits, chainIDLen int
	p, vByte, err = parseSignature(payload, p, legacy, &ctx.cfg.ChainID, &ctx.Signature)
	if err != nil {
		return 0, fmt.Errorf("%w: %s", ErrParseTxn, err) //nolint
	}

	if legacy {
		preEip155 := ctx.V.Eq(u256.N27) || ctx.V.Eq(u256.N28)
		if !preEip155 {
			chainIDBits = ctx.ChainID.BitLen()
			if chainIDBits <= 7 {
				chainIDLen = 1
			} else {
				chainIDLen = common.BitLenToByteLen(chainIDBits)
				sigHashLen++
			}
			sigHashLen += uint(chainIDLen)
			sigHashLen += 2 // For two extra zeros
		}
	} else {
		if ctx.Signature.V.GtUint64(1) {
			return 0, fmt.Errorf("%w: v is too big: %s", ErrParseTxn, &ctx.Signature.V)
		}
	}

	// For legacy transactions, hash the full payload
	if legacy {
		if _, err = ctx.Keccak1.Write(payload[pos:p]); err != nil {
			return 0, fmt.Errorf("%w: computing IdHash: %s", ErrParseTxn, err) //nolint
		}
	}
	_, _ = ctx.Keccak1.(io.Reader).Read(slot.IDHash[:32])
	if validateHash != nil {
		if err := validateHash(slot.IDHash[:32]); err != nil {
			return p, err
		}
	}

	if !ctx.withSender {
		return p, nil
	}

	if !crypto.TransactionSignatureIsValid(vByte, &ctx.R, &ctx.S, ctx.allowPreEip2s && legacy) {
		return 0, fmt.Errorf("%w: invalid v, r, s: %d, %s, %s", ErrParseTxn, vByte, &ctx.R, &ctx.S)
	}

	// Computing sigHash (hash used to recover sender from the signature)
	if err = ctx.computeAndWriteSigHash(payload, sigHashPos, sigHashEnd, sigHashLen, chainIDBits, chainIDLen, legacy); err != nil {
		return 0, err
	}

	// Squeeze Sighash
	_, _ = ctx.Keccak2.(io.Reader).Read(ctx.Sighash[:32])
	binary.BigEndian.PutUint64(ctx.Sig[0:8], ctx.R[3])
	binary.BigEndian.PutUint64(ctx.Sig[8:16], ctx.R[2])
	binary.BigEndian.PutUint64(ctx.Sig[16:24], ctx.R[1])
	binary.BigEndian.PutUint64(ctx.Sig[24:32], ctx.R[0])
	binary.BigEndian.PutUint64(ctx.Sig[32:40], ctx.S[3])
	binary.BigEndian.PutUint64(ctx.Sig[40:48], ctx.S[2])
	binary.BigEndian.PutUint64(ctx.Sig[48:56], ctx.S[1])
	binary.BigEndian.PutUint64(ctx.Sig[56:64], ctx.S[0])
	ctx.Sig[64] = vByte
	// recover sender
	if _, err = secp256k1.RecoverPubkeyWithContext(secp256k1.DefaultContext, ctx.Sighash[:], ctx.Sig[:], ctx.buf[:0]); err != nil {
		return 0, fmt.Errorf("%w: recovering sender from signature: %s", ErrParseTxn, err) //nolint
	}
	// apply keccak to the public key
	ctx.Keccak2.Reset()
	if _, err = ctx.Keccak2.Write(ctx.buf[1:65]); err != nil {
		return 0, fmt.Errorf("%w: computing sender from public key: %s", ErrParseTxn, err) //nolint
	}
	// squeeze the hash of the public key
	_, _ = ctx.Keccak2.(io.Reader).Read(ctx.buf[:32])
	// take last 20 bytes as address
	copy(sender, ctx.buf[12:32])

	return p, nil
}

// computeAndWriteSigHash writes the RLP list prefix and signing data to ctx.Keccak2.
func (ctx *TxParseContext) computeAndWriteSigHash(payload []byte, sigHashPos, sigHashEnd int, sigHashLen uint, chainIDBits, chainIDLen int, legacy bool) error {
	// Write len Prefix to the Sighash
	if sigHashLen < 56 {
		ctx.buf[0] = byte(sigHashLen) + 192
		if _, err := ctx.Keccak2.Write(ctx.buf[:1]); err != nil {
			return fmt.Errorf("%w: computing signHash (hashing len Prefix): %s", ErrParseTxn, err) //nolint
		}
	} else {
		beLen := common.BitLenToByteLen(bits.Len(sigHashLen))
		binary.BigEndian.PutUint64(ctx.buf[1:], uint64(sigHashLen))
		ctx.buf[8-beLen] = byte(beLen) + 247
		if _, err := ctx.Keccak2.Write(ctx.buf[8-beLen : 9]); err != nil {
			return fmt.Errorf("%w: computing signHash (hashing len Prefix): %s", ErrParseTxn, err) //nolint
		}
	}
	if _, err := ctx.Keccak2.Write(payload[sigHashPos:sigHashEnd]); err != nil {
		return fmt.Errorf("%w: computing signHash: %s", ErrParseTxn, err) //nolint
	}
	if legacy && chainIDLen > 0 {
		if chainIDBits <= 7 {
			ctx.buf[0] = byte(ctx.ChainID.Uint64())
			if _, err := ctx.Keccak2.Write(ctx.buf[:1]); err != nil {
				return fmt.Errorf("%w: computing signHash (hashing legacy chainId): %s", ErrParseTxn, err) //nolint
			}
		} else {
			binary.BigEndian.PutUint64(ctx.buf[1:9], ctx.ChainID[3])
			binary.BigEndian.PutUint64(ctx.buf[9:17], ctx.ChainID[2])
			binary.BigEndian.PutUint64(ctx.buf[17:25], ctx.ChainID[1])
			binary.BigEndian.PutUint64(ctx.buf[25:33], ctx.ChainID[0])
			ctx.buf[32-chainIDLen] = 128 + byte(chainIDLen)
			if _, err := ctx.Keccak2.Write(ctx.buf[32-chainIDLen : 33]); err != nil {
				return fmt.Errorf("%w: computing signHash (hashing legacy chainId): %s", ErrParseTxn, err) //nolint
			}
		}
		// Encode two zeros
		ctx.buf[0] = 128
		ctx.buf[1] = 128
		if _, err := ctx.Keccak2.Write(ctx.buf[:2]); err != nil {
			return fmt.Errorf("%w: computing signHash (hashing zeros after legacy chainId): %s", ErrParseTxn, err) //nolint
		}
	}
	return nil
}

// parseAccessList parses the EIP-2930 access list from the transaction payload.
func (ctx *TxParseContext) parseAccessList(payload []byte, pos int, slot *TxSlot) (p int, err error) {
	dataPos, dataLen, err := rlp.ParseList(payload, pos)
	if err != nil {
		return 0, fmt.Errorf("%w: access list len: %s", ErrParseTxn, err) //nolint
	}
	tuplePos := dataPos
	for tuplePos < dataPos+dataLen {
		var tupleLen int
		tuplePos, tupleLen, err = rlp.ParseList(payload, tuplePos)
		if err != nil {
			return 0, fmt.Errorf("%w: tuple len: %s", ErrParseTxn, err) //nolint
		}
		var addrPos int
		addrPos, err = rlp.StringOfLen(payload, tuplePos, 20)
		if err != nil {
			return 0, fmt.Errorf("%w: tuple addr len: %s", ErrParseTxn, err) //nolint
		}
		slot.AlAddrCount++
		var storagePos, storageLen int
		storagePos, storageLen, err = rlp.ParseList(payload, addrPos+20)
		if err != nil {
			return 0, fmt.Errorf("%w: storage key list len: %s", ErrParseTxn, err) //nolint
		}
		sKeyPos := storagePos
		for sKeyPos < storagePos+storageLen {
			sKeyPos, err = rlp.StringOfLen(payload, sKeyPos, 32)
			if err != nil {
				return 0, fmt.Errorf("%w: tuple storage key len: %s", ErrParseTxn, err) //nolint
			}
			slot.AlStorCount++
			sKeyPos += 32
		}
		if sKeyPos != storagePos+storageLen {
			return 0, fmt.Errorf("%w: unexpected storage key items", ErrParseTxn)
		}
		tuplePos += tupleLen
		if tuplePos != sKeyPos {
			return 0, fmt.Errorf("%w: extraneous space in the tuple after storage key list", ErrParseTxn)
		}
	}
	if tuplePos != dataPos+dataLen {
		return 0, fmt.Errorf("%w: extraneous space in the access list after all tuples", ErrParseTxn)
	}
	return dataPos + dataLen, nil
}

// parseAuthorizations parses the EIP-7702 authorization list from the transaction payload.
func (ctx *TxParseContext) parseAuthorizations(payload []byte, pos int, slot *TxSlot) (p int, err error) {
	dataPos, dataLen, err := rlp.ParseList(payload, pos)
	if err != nil {
		return 0, fmt.Errorf("%w: authorizations len: %s", ErrParseTxn, err) //nolint
	}
	authPos := dataPos
	for authPos < dataPos+dataLen {
		var authLen int
		authPos, authLen, err = rlp.ParseList(payload, authPos)
		if err != nil {
			return 0, fmt.Errorf("%w: authorization: %s", ErrParseTxn, err) //nolint
		}
		var sig Signature
		p2 := authPos
		rawStart := p2
		p2, err = rlp.U256(payload, p2, &sig.ChainID)
		if err != nil {
			return 0, fmt.Errorf("%w: authorization chainId: %s", ErrParseTxn, err) //nolint
		}
		if !sig.ChainID.IsUint64() {
			// https://github.com/ethereum/EIPs/pull/8929
			return 0, fmt.Errorf("%w: authorization chainId is too big: %s", ErrParseTxn, &sig.ChainID)
		}
		p2, err = rlp.StringOfLen(payload, p2, 20) // address
		if err != nil {
			return 0, fmt.Errorf("%w: authorization address: %s", ErrParseTxn, err) //nolint
		}
		p2 += 20
		p2, _, err = rlp.U64(payload, p2) // nonce
		if err != nil {
			return 0, fmt.Errorf("%w: authorization nonce: %s", ErrParseTxn, err) //nolint
		}
		rawEnd := p2
		p2, _, err = parseSignature(payload, p2, false /* legacy */, nil /* cfgChainId */, &sig)
		if err != nil {
			return 0, fmt.Errorf("%w: authorization signature: %s", ErrParseTxn, err) //nolint
		}
		slot.Authorizations = append(slot.Authorizations, sig)
		slot.AuthRaw = append(slot.AuthRaw, common.CopyBytes(payload[rawStart:rawEnd]))
		authPos += authLen
		if authPos != p2 {
			return 0, fmt.Errorf("%w: authorization: unexpected list items", ErrParseTxn)
		}
	}
	if authPos != dataPos+dataLen {
		return 0, fmt.Errorf("%w: extraneous space in the authorizations", ErrParseTxn)
	}
	return dataPos + dataLen, nil
}

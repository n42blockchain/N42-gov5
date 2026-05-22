package mptproof

import (
	"errors"
	"fmt"

	"github.com/holiman/uint256"
)

// RethAccount mirrors reth_primitives_traits::Account.
type RethAccount struct {
	Nonce        uint64
	Balance      uint256.Int
	BytecodeHash [32]byte // zero if absent
	HasBytecode  bool
}

// DecodeRethAccount parses a row from reth's HashedAccounts MDBX
// table. Encoding spec is derived from
// reth_codecs_derive::compact at 0.3.1:
//
//	[bitflag bytes][nonce bytes][balance bytes][bytecode_hash bytes if present]
//
// Bitflags occupy 2 bytes (Account.bitflag_encoded_bytes() == 2)
// laid out by modular_bitfield with B<n> fields packed LSB-first:
//
//	bits  0..3  : nonce_len     (B4) — # of BE bytes for nonce u64
//	bits  4..9  : balance_len   (B6) — # of BE bytes for balance U256
//	bit   10    : bytecode_hash present
//	bits 11..15 : unused (skip)
//
// modular_bitfield packs B<n> fields LSB-first within each byte AND
// across byte boundaries (little-endian bit order). Concretely, for
// a 2-byte flag word `f0f1`:
//
//	nonce_len     = f0 & 0x0f                                 (low 4 bits of byte 0)
//	balance_len   = ((f0 >> 4) & 0x0f) | ((f1 & 0x03) << 4)   (6 bits straddling)
//	hasBytecode   = (f1 >> 2) & 0x01                          (bit 2 of byte 1)
//
// After the bitflags, each numeric field is BIG-ENDIAN with leading
// zero bytes stripped; the byte count came from the matching length
// flag. A length of 0 → value is 0. bytecode_hash, when present, is
// 32 raw bytes (B256 is a fixed-size array — NOT length-prefixed).
func DecodeRethAccount(buf []byte) (RethAccount, error) {
	if len(buf) < 2 {
		return RethAccount{}, errors.New("DecodeRethAccount: buf too short for bitflags")
	}
	nonceLen := int(buf[0] & 0x0f)
	balanceLen := int(((buf[0] >> 4) & 0x0f) | ((buf[1] & 0x03) << 4))
	hasBytecode := buf[1]&0x04 != 0

	if nonceLen > 8 || balanceLen > 32 {
		return RethAccount{}, fmt.Errorf("DecodeRethAccount: out-of-range field lengths (nonce=%d balance=%d)",
			nonceLen, balanceLen)
	}
	expected := 2 + nonceLen + balanceLen
	if hasBytecode {
		expected += 32
	}
	if len(buf) < expected {
		return RethAccount{}, fmt.Errorf("DecodeRethAccount: buf too short (need %d got %d)",
			expected, len(buf))
	}

	pos := 2

	var nonce uint64
	for i := 0; i < nonceLen; i++ {
		nonce = (nonce << 8) | uint64(buf[pos+i])
	}
	pos += nonceLen

	var balanceBytes [32]byte
	if balanceLen > 0 {
		copy(balanceBytes[32-balanceLen:], buf[pos:pos+balanceLen])
	}
	pos += balanceLen
	var balance uint256.Int
	balance.SetBytes(balanceBytes[:])

	out := RethAccount{Nonce: nonce, Balance: balance}
	if hasBytecode {
		copy(out.BytecodeHash[:], buf[pos:pos+32])
		out.HasBytecode = true
	}
	return out, nil
}

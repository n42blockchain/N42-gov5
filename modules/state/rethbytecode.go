// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// rethbytecode.go — decode reth's Compact-wrapped bytecode ON READ, mirroring
// reth's own code_by_hash (which unwraps the stored Bytecode value), instead of
// transcoding the whole Code table. This keeps the migrated Code table in reth's
// native form (no batch rewrite, no data mutation) while serving the raw deployed
// bytecode the EVM expects.
//
// reth stores a Bytecode as Compact: [u32 BE len L][stored bytecode][trailer].
// LegacyRaw / Eip7702 / Eof store the raw code directly (keccak == codeHash).
// LegacyAnalyzed stores raw||padding (zero bytes appended for jumpdest analysis);
// the raw code is stored[:L-p], found by matching keccak against the codeHash key
// (robust without vendoring reth-codecs' exact struct codec).

package state

import (
	"encoding/binary"
	"sync"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// rethCodeCache caches decoded raw bytecode by codeHash. Bytecode-by-codeHash is
// immutable, so a decoded entry is valid forever; this amortises the keccak-search
// to at most once per contract across the whole sync.
var rethCodeCache sync.Map // types.Hash -> []byte

// decodeRethBytecode returns raw deployed bytecode from a reth Compact value, or
// nil if it can't be decoded against codeHash. Every returned slice is verified
// keccak == codeHash, so matches are exact (no false positives). reth's Bytecode
// Compact is macro-derived (no readable codec), and the encoding differs per
// variant, so we try the known shapes in turn:
//   1. raw (no wrapper).
//   2. LegacyRaw/Analyzed: [u32 BE len L][stored][padding] — raw = stored[:L-p].
//   3. EIP-7702: 0xef0100||addr (23B) behind a small header — scan for it.
//   4. short codes (delegations, tiny contracts): bounded substring brute force.
func decodeRethBytecode(codeHash types.Hash, v []byte) []byte {
	if crypto.Keccak256Hash(v) == codeHash {
		return v
	}
	// (2) Legacy [u32 BE len][stored][trailer].
	if len(v) >= 4 {
		if L := int(binary.BigEndian.Uint32(v[0:4])); 4+L <= len(v) {
			stored := v[4 : 4+L]
			if crypto.Keccak256Hash(stored) == codeHash {
				return stored
			}
			for p := 1; p <= 64 && p <= L; p++ {
				if crypto.Keccak256Hash(stored[:L-p]) == codeHash {
					return stored[:L-p]
				}
			}
		}
	}
	// (3) EIP-7702 delegation designator: 0xef 0x01 0x00 || 20-byte address.
	for skip := 0; skip+23 <= len(v); skip++ {
		if v[skip] == 0xef && v[skip+1] == 0x01 && v[skip+2] == 0x00 &&
			crypto.Keccak256Hash(v[skip:skip+23]) == codeHash {
			return v[skip : skip+23]
		}
	}
	// (4) Short-code substring brute force (keccak-verified, bounded).
	if len(v) <= 256 {
		for skip := 0; skip < len(v); skip++ {
			for end := len(v); end > skip; end-- {
				if crypto.Keccak256Hash(v[skip:end]) == codeHash {
					return v[skip:end]
				}
			}
		}
	}
	return nil
}

// codeFromTable returns the raw bytecode for codeHash given the Code-table value,
// transparently unwrapping reth's Compact encoding if present. Works whether the
// table holds raw, reth-wrapped, or a mix of both.
func codeFromTable(codeHash types.Hash, raw []byte) []byte {
	if len(raw) == 0 {
		return nil
	}
	if crypto.Keccak256Hash(raw) == codeHash {
		return raw // already raw deployed bytecode
	}
	if cached, ok := rethCodeCache.Load(codeHash); ok {
		return cached.([]byte)
	}
	if dec := decodeRethBytecode(codeHash, raw); dec != nil {
		cp := make([]byte, len(dec))
		copy(cp, dec)
		rethCodeCache.Store(codeHash, cp)
		return cp
	}
	return raw // best effort: return as-is if undecodable
}

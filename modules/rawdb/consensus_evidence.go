// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"encoding/binary"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"lukechampine.com/blake3"
)

// ConsensusEvidence holds per-block consensus data (QC + optional mobile BLS).
// Stored in the ConsensusEvidence MDBX table, keyed by block number.
//
// Binary layout:
//
//	view:                u64 LE           (8B)
//	block_hash:          [32]byte         (32B)
//	aggregate_signature: [96]byte         (96B)
//	signer_count:        u16 LE           (2B)
//	signers_packed:      [⌈n/8⌉]byte     (1-63B)
//	has_mobile:          u8               (1B)
//	[if has_mobile = 1:]
//	  mob_receipts_root:   [32]byte       (32B)
//	  mob_agg_signature:   [96]byte       (96B)
//	  mob_participant_cnt: u16 LE         (2B)
//	  mob_participants:    [⌈m/8⌉]byte   (packed)
//	  mob_created_at_ms:   u64 LE         (8B)
type ConsensusEvidence struct {
	View               uint64
	BlockHash          types.Hash
	AggregateSignature [96]byte
	SignerCount        uint16
	SignersPacked      []byte // ⌈SignerCount/8⌉ bytes

	HasMobile             bool
	MobReceiptsRoot       types.Hash
	MobAggSignature       [96]byte
	MobParticipantCount   uint16
	MobParticipantsPacked []byte // ⌈MobParticipantCount/8⌉ bytes
	MobCreatedAtMs        uint64
}

// Marshal encodes ConsensusEvidence to compact binary.
func (ce *ConsensusEvidence) Marshal() []byte {
	packedLen := (int(ce.SignerCount) + 7) / 8
	size := 8 + 32 + 96 + 2 + packedLen + 1
	if ce.HasMobile {
		mobPackedLen := (int(ce.MobParticipantCount) + 7) / 8
		size += 32 + 96 + 2 + mobPackedLen + 8
	}

	buf := make([]byte, size)
	pos := 0

	binary.LittleEndian.PutUint64(buf[pos:], ce.View)
	pos += 8
	copy(buf[pos:], ce.BlockHash[:])
	pos += 32
	copy(buf[pos:], ce.AggregateSignature[:])
	pos += 96
	binary.LittleEndian.PutUint16(buf[pos:], ce.SignerCount)
	pos += 2
	copy(buf[pos:], ce.SignersPacked[:packedLen])
	pos += packedLen

	if ce.HasMobile {
		buf[pos] = 1
		pos++
		copy(buf[pos:], ce.MobReceiptsRoot[:])
		pos += 32
		copy(buf[pos:], ce.MobAggSignature[:])
		pos += 96
		binary.LittleEndian.PutUint16(buf[pos:], ce.MobParticipantCount)
		pos += 2
		mobPackedLen := (int(ce.MobParticipantCount) + 7) / 8
		copy(buf[pos:], ce.MobParticipantsPacked[:mobPackedLen])
		pos += mobPackedLen
		binary.LittleEndian.PutUint64(buf[pos:], ce.MobCreatedAtMs)
	} else {
		buf[pos] = 0
	}

	return buf
}

// Unmarshal decodes ConsensusEvidence from compact binary.
func (ce *ConsensusEvidence) Unmarshal(data []byte) error {
	if len(data) < 8+32+96+2+1 {
		return fmt.Errorf("consensus evidence too short: %d bytes", len(data))
	}
	pos := 0

	ce.View = binary.LittleEndian.Uint64(data[pos:])
	pos += 8
	copy(ce.BlockHash[:], data[pos:])
	pos += 32
	copy(ce.AggregateSignature[:], data[pos:])
	pos += 96
	ce.SignerCount = binary.LittleEndian.Uint16(data[pos:])
	pos += 2
	packedLen := (int(ce.SignerCount) + 7) / 8
	if pos+packedLen+1 > len(data) {
		return fmt.Errorf("consensus evidence truncated at signers: need %d, have %d", pos+packedLen+1, len(data))
	}
	ce.SignersPacked = make([]byte, packedLen)
	copy(ce.SignersPacked, data[pos:pos+packedLen])
	pos += packedLen

	ce.HasMobile = data[pos] == 1
	pos++

	if ce.HasMobile {
		if pos+32+96+2+8 > len(data) {
			return fmt.Errorf("consensus evidence truncated at mobile section")
		}
		copy(ce.MobReceiptsRoot[:], data[pos:])
		pos += 32
		copy(ce.MobAggSignature[:], data[pos:])
		pos += 96
		ce.MobParticipantCount = binary.LittleEndian.Uint16(data[pos:])
		pos += 2
		mobPackedLen := (int(ce.MobParticipantCount) + 7) / 8
		if pos+mobPackedLen+8 > len(data) {
			return fmt.Errorf("consensus evidence truncated at mob participants")
		}
		ce.MobParticipantsPacked = make([]byte, mobPackedLen)
		copy(ce.MobParticipantsPacked, data[pos:pos+mobPackedLen])
		pos += mobPackedLen
		ce.MobCreatedAtMs = binary.LittleEndian.Uint64(data[pos:])
	}

	return nil
}

// Hash returns keccak256 of the marshaled evidence.
func (ce *ConsensusEvidence) Hash() types.Hash {
	return crypto.Keccak256Hash(ce.Marshal())
}

// BeaconRoot returns Blake3(Marshal) — the value a CHILD block carries as its
// ParentBeaconRoot (EIP-4788), committing to this block's consensus evidence.
// This is the canonical CE→ParentBeaconRoot derivation shared by the replay
// resealer and live block production. It is deliberately Blake3, NOT Hash()'s
// keccak256, so that resealed and live-produced chains link byte-identically.
func (ce *ConsensusEvidence) BeaconRoot() types.Hash {
	return types.Hash(blake3.Sum256(ce.Marshal()))
}

// ParentBeaconRoot reads the consensus evidence of blockNum-1 and returns its
// BeaconRoot — the ParentBeaconRoot a block at blockNum must carry. Returns the
// zero hash for genesis/block 1 or when no parent evidence exists (legacy
// chains), matching the replay resealer's behavior.
func ParentBeaconRoot(tx kv.Getter, blockNum uint64) types.Hash {
	if blockNum <= 1 {
		return types.Hash{}
	}
	parentCE, err := ReadConsensusEvidence(tx, blockNum-1)
	if err != nil || parentCE == nil {
		return types.Hash{}
	}
	return parentCE.BeaconRoot()
}

// WriteConsensusEvidence writes evidence for a block number.
func WriteConsensusEvidence(tx kv.Putter, blockNum uint64, ce *ConsensusEvidence) error {
	var key [8]byte
	binary.BigEndian.PutUint64(key[:], blockNum)
	return tx.Put(modules.ConsensusEvidence, key[:], ce.Marshal())
}

// ReadConsensusEvidence reads evidence for a block number.
func ReadConsensusEvidence(tx kv.Getter, blockNum uint64) (*ConsensusEvidence, error) {
	var key [8]byte
	binary.BigEndian.PutUint64(key[:], blockNum)
	v, err := tx.GetOne(modules.ConsensusEvidence, key[:])
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil
	}
	ce := &ConsensusEvidence{}
	if err := ce.Unmarshal(v); err != nil {
		return nil, err
	}
	return ce, nil
}

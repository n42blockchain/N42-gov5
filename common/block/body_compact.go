// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Compact STORAGE codec for a full block body — the encoding the freezer uses
// when it serializes a body out of the hot database, and the last place in the
// tree that produced protobuf as a stored artifact.
//
// A body is a transaction list plus the verifier set, the reward list and an
// optional ZK proof. The transactions dominate it, and they already have a
// compact form: MarshalCompactStorage, the same encoding the BlockTransaction
// table is written in. Each transaction is stored here as a length-prefixed
// blob in whichever form that returns, with the protobuf form as the fallback
// for transaction types it does not cover. Transaction.Unmarshal dispatches
// between them on its own marker, so a body can hold a mixture and still
// decode.
//
//	[0] 0xFF  — format marker (a valid protobuf message can never begin with
//	            0xFF: field 31 / wire type 7 is illegal)
//	[1] 0x01  — codec version
//	[2..]
//	      uvarint tx count,       per tx: uvarint length, bytes
//	      uvarint verifier count, per verifier: address 20 B, public key 48 B
//	      uvarint reward count,   per reward: address 20 B, uvarint amount
//	                              length, amount big-endian
//	      uvarint ZK proof length, bytes
//
// Reward amounts are stored as a minimal big-endian run rather than a fixed 32
// bytes: they are block rewards, so the top bytes are zero in every realistic
// case.

package block

import (
	"encoding/binary"
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

const (
	compactBodyMarker  = 0xFF
	compactBodyVersion = 0x01
)

// MarshalCompact encodes the body in the compact storage format.
func (b *Body) MarshalCompact() ([]byte, error) {
	buf := make([]byte, 0, 2+3*binary.MaxVarintLen64+len(b.Txs)*192)
	buf = append(buf, compactBodyMarker, compactBodyVersion)

	buf = rcAppendUvarint(buf, uint64(len(b.Txs)))
	for i, tx := range b.Txs {
		if tx == nil {
			return nil, fmt.Errorf("compact body: transaction %d is nil", i)
		}
		enc := tx.MarshalCompactStorage()
		if enc == nil {
			// Transaction type the compact codec does not cover; the protobuf
			// form still round-trips and Unmarshal dispatches on the marker.
			var err error
			enc, err = tx.Marshal()
			if err != nil {
				return nil, fmt.Errorf("compact body: transaction %d: %w", i, err)
			}
		}
		buf = rcAppendUvarint(buf, uint64(len(enc)))
		buf = append(buf, enc...)
	}

	buf = rcAppendUvarint(buf, uint64(len(b.Verifiers)))
	for _, v := range b.Verifiers {
		buf = append(buf, v.Address[:]...)
		buf = append(buf, v.PublicKey[:]...)
	}

	buf = rcAppendUvarint(buf, uint64(len(b.Rewards)))
	for i, r := range b.Rewards {
		if r == nil {
			return nil, fmt.Errorf("compact body: reward %d is nil", i)
		}
		buf = append(buf, r.Address[:]...)
		if r.Amount == nil {
			buf = rcAppendUvarint(buf, 0)
			continue
		}
		amount := r.Amount.Bytes() // minimal big-endian, empty for zero
		buf = rcAppendUvarint(buf, uint64(len(amount)))
		buf = append(buf, amount...)
	}

	buf = rcAppendUvarint(buf, uint64(len(b.ZkProof)))
	buf = append(buf, b.ZkProof...)

	return buf, nil
}

// IsCompactBody reports whether data is in the compact body format.
func IsCompactBody(data []byte) bool {
	return len(data) >= 2 && data[0] == compactBodyMarker && data[1] == compactBodyVersion
}

// UnmarshalCompact decodes the compact body format.
func (b *Body) UnmarshalCompact(data []byte) error {
	if !IsCompactBody(data) {
		return fmt.Errorf("not a compact body record")
	}
	pos := 2

	take := func(n int) ([]byte, error) {
		if n < 0 || pos+n > len(data) {
			return nil, fmt.Errorf("compact body truncated: need %d bytes at offset %d of %d", n, pos, len(data))
		}
		v := data[pos : pos+n]
		pos += n
		return v, nil
	}
	uvarint := func() (uint64, error) {
		if pos >= len(data) {
			return 0, fmt.Errorf("compact body truncated at offset %d", pos)
		}
		v, n := binary.Uvarint(data[pos:])
		if n <= 0 {
			return 0, fmt.Errorf("compact body: bad uvarint at offset %d", pos)
		}
		pos += n
		return v, nil
	}
	// Every element costs at least one byte, so a count larger than what is
	// left is malformed. Checking before allocating keeps a corrupt length from
	// turning into a huge make().
	checkCount := func(count uint64, unit int, what string) error {
		if unit < 1 {
			unit = 1
		}
		if count > uint64((len(data)-pos)/unit) {
			return fmt.Errorf("compact body: %s count %d exceeds remaining %d bytes", what, count, len(data)-pos)
		}
		return nil
	}

	txCount, err := uvarint()
	if err != nil {
		return err
	}
	if err := checkCount(txCount, 1, "transaction"); err != nil {
		return err
	}
	txs := make([]*transaction.Transaction, 0, txCount)
	for i := uint64(0); i < txCount; i++ {
		n, err := uvarint()
		if err != nil {
			return err
		}
		raw, err := take(int(n))
		if err != nil {
			return err
		}
		tx := new(transaction.Transaction)
		if err := tx.Unmarshal(raw); err != nil {
			return fmt.Errorf("compact body: transaction %d: %w", i, err)
		}
		txs = append(txs, tx)
	}
	b.Txs = txs

	verifierCount, err := uvarint()
	if err != nil {
		return err
	}
	if err := checkCount(verifierCount, types.AddressLength+types.PublicKeyLength, "verifier"); err != nil {
		return err
	}
	verifiers := make([]*Verify, 0, verifierCount)
	for i := uint64(0); i < verifierCount; i++ {
		v := new(Verify)
		addr, err := take(types.AddressLength)
		if err != nil {
			return err
		}
		copy(v.Address[:], addr)
		pub, err := take(types.PublicKeyLength)
		if err != nil {
			return err
		}
		copy(v.PublicKey[:], pub)
		verifiers = append(verifiers, v)
	}
	b.Verifiers = verifiers

	rewardCount, err := uvarint()
	if err != nil {
		return err
	}
	if err := checkCount(rewardCount, types.AddressLength+1, "reward"); err != nil {
		return err
	}
	rewards := make([]*Reward, 0, rewardCount)
	for i := uint64(0); i < rewardCount; i++ {
		r := &Reward{Amount: uint256.NewInt(0)}
		addr, err := take(types.AddressLength)
		if err != nil {
			return err
		}
		copy(r.Address[:], addr)
		n, err := uvarint()
		if err != nil {
			return err
		}
		if n > 32 {
			return fmt.Errorf("compact body: reward %d amount is %d bytes", i, n)
		}
		amount, err := take(int(n))
		if err != nil {
			return err
		}
		r.Amount.SetBytes(amount)
		rewards = append(rewards, r)
	}
	b.Rewards = rewards

	proofLen, err := uvarint()
	if err != nil {
		return err
	}
	proof, err := take(int(proofLen))
	if err != nil {
		return err
	}
	if proofLen > 0 {
		b.ZkProof = append([]byte(nil), proof...)
	} else {
		b.ZkProof = nil
	}

	if pos != len(data) {
		return fmt.Errorf("compact body: %d trailing bytes", len(data)-pos)
	}
	return nil
}

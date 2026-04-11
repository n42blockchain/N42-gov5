// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// LtHashCommitment: 2048-byte lattice-hash state digest.
// Maintains an incrementally updated lthash.Digest spanning accounts
// and storage, with domain separation via ltHashTagAccount ('A') and
// ltHashTagStorage ('S'). UpdateAccount handles create/update/delete
// transitions by Add/Remove/Update on encoded elements, enabling
// O(changes) root updates with no tree traversal.

package commitment

import (
	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/lthash"
)

// LtHash element tags for domain separation.
const (
	ltHashTagAccount byte = 'A' // 0x41
	ltHashTagStorage byte = 'S' // 0x53
)

// LtHashCommitment maintains a 2048-byte lattice hash digest that tracks
// the state of all accounts and storage slots. It is updated incrementally
// alongside the JMT — only changed elements are re-hashed.
type LtHashCommitment struct {
	digest *lthash.Digest
}

// NewLtHashCommitment creates a new LtHash commitment from an existing digest.
func NewLtHashCommitment(digest *lthash.Digest) *LtHashCommitment {
	if digest == nil {
		digest = lthash.New()
	}
	return &LtHashCommitment{digest: digest}
}

// UpdateAccount incrementally updates the digest for an account change.
// Removes the old account encoding and adds the new one.
func (c *LtHashCommitment) UpdateAccount(addr types.Address, oldAcct, newAcct *account.StateAccount) {
	keyHash := AccountKeyHash(addr)
	var kh [32]byte
	copy(kh[:], keyHash[:])

	oldEmpty := oldAcct == nil || isAccountEmpty(oldAcct)
	newEmpty := newAcct == nil || isAccountEmpty(newAcct)

	if !oldEmpty && !newEmpty {
		// Update: remove old, add new in single pass.
		oldEnc := EncodeAccountValue(oldAcct)
		newEnc := EncodeAccountValue(newAcct)
		c.digest.Update(
			lthash.EncodeElement(ltHashTagAccount, kh, oldEnc[:]),
			lthash.EncodeElement(ltHashTagAccount, kh, newEnc[:]),
		)
	} else if !oldEmpty {
		// Deletion: remove old element.
		oldEnc := EncodeAccountValue(oldAcct)
		c.digest.Remove(lthash.EncodeElement(ltHashTagAccount, kh, oldEnc[:]))
	} else if !newEmpty {
		// Creation: add new element.
		newEnc := EncodeAccountValue(newAcct)
		c.digest.Add(lthash.EncodeElement(ltHashTagAccount, kh, newEnc[:]))
	}
}

// UpdateStorage incrementally updates the digest for a storage slot change.
func (c *LtHashCommitment) UpdateStorage(addr types.Address, slot types.Hash, oldVal, newVal *uint256.Int) {
	keyHash := StorageKeyHash(addr, slot)
	var kh [32]byte
	copy(kh[:], keyHash[:])

	oldZero := oldVal == nil || oldVal.IsZero()
	newZero := newVal == nil || newVal.IsZero()

	if !oldZero && !newZero {
		// Update.
		oldBytes := oldVal.Bytes32()
		newBytes := newVal.Bytes32()
		c.digest.Update(
			lthash.EncodeElement(ltHashTagStorage, kh, oldBytes[:]),
			lthash.EncodeElement(ltHashTagStorage, kh, newBytes[:]),
		)
	} else if !oldZero {
		// Deletion.
		oldBytes := oldVal.Bytes32()
		c.digest.Remove(lthash.EncodeElement(ltHashTagStorage, kh, oldBytes[:]))
	} else if !newZero {
		// Creation.
		newBytes := newVal.Bytes32()
		c.digest.Add(lthash.EncodeElement(ltHashTagStorage, kh, newBytes[:]))
	}
}

// Root returns the 32-byte BLAKE3 summary of the 2048-byte digest.
func (c *LtHashCommitment) Root() types.Hash {
	sum := c.digest.Sum()
	var h types.Hash
	copy(h[:], sum[:])
	return h
}

// Digest returns the underlying 2048-byte digest.
func (c *LtHashCommitment) Digest() *lthash.Digest {
	return c.digest
}

// IsZero returns true if the digest is empty (no elements tracked).
func (c *LtHashCommitment) IsZero() bool {
	return c.digest.IsZero()
}

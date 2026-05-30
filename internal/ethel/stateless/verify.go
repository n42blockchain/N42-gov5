package stateless

import (
	"bytes"
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

// This file is the minimal client's state-root verification layer. It consumes
// a BlockProof and checks the block's state transition against trusted anchors,
// without the full state trie.
//
// NOTE on proof encoding: BlockProof carries proofs as [][]byte standard-MPT
// (EIP-1186 / RLP) node sets — the form NewStateRootUpdater/AddStorageProof
// consume and that TestTwoLevelStateRoot proves correct. The compact wire
// (proofwire.go) is a size optimization that still needs a faithful-encoding fix
// (it must preserve each child's inline-vs-hash boundary exactly, else the
// decoded trie re-hashes to a different root); it is NOT yet used here.

// StorageChange is one SSTORE in a block changeset (hashed slot -> new value).
type StorageChange struct {
	SlotHash types.Hash
	Value    []byte // big-endian word; empty/zero deletes the slot
}

// AccountChange is one account's post-state in a block changeset. StorageProof
// (RLP node set) is required iff Storage is non-empty. Deleted overrides all.
type AccountChange struct {
	AddrHash     types.Hash
	Nonce        uint64
	Balance      uint256.Int
	CodeHash     []byte   // 32B; nil keeps existing/empty
	StorageRoot  []byte   // 32B; the pre-state storageRoot the storage proof is rooted at
	StorageProof [][]byte // RLP node set for this account's storage subtree
	Storage      []StorageChange
	Deleted      bool
}

// BlockProof is the complete stateless proof for one block: the account-trie
// multiproof at the pre-state, plus per-account changes (each with its own
// storage proof when storage changed).
type BlockProof struct {
	Number       uint64
	AccountProof [][]byte // RLP node set of the account trie multiproof at preStateRoot
	Changes      []AccountChange
}

// VerifyStateRoot recomputes the post-state root from a BlockProof and checks it
// against trustedPostRoot (the HeaderChain-anchored header.stateRoot of the
// block). preStateRoot is the trusted header.stateRoot of block Number-1.
// Returns nil iff the recomputed root matches — the block's state transition is
// proven against the trusted anchors WITHOUT the full state trie.
func VerifyStateRoot(preStateRoot, trustedPostRoot []byte, bp *BlockProof) error {
	u, err := NewStateRootUpdater(preStateRoot, bp.AccountProof)
	if err != nil {
		return fmt.Errorf("block %d account proof: %w", bp.Number, err)
	}
	for i := range bp.Changes {
		c := &bp.Changes[i]
		if c.Deleted {
			u.DeleteAccount(c.AddrHash)
			continue
		}
		if len(c.Storage) > 0 {
			if err := u.AddStorageProof(c.AddrHash, c.StorageRoot, c.StorageProof); err != nil {
				return fmt.Errorf("block %d: %w", bp.Number, err)
			}
			for _, sc := range c.Storage {
				if err := u.SetStorage(c.AddrHash, sc.SlotHash, sc.Value); err != nil {
					return fmt.Errorf("block %d: %w", bp.Number, err)
				}
			}
		}
		e := &AccountEdit{Nonce: c.Nonce, CodeHash: c.CodeHash}
		e.Balance.Set(&c.Balance)
		u.SetAccount(c.AddrHash, e)
	}
	got, err := u.Root()
	if err != nil {
		return fmt.Errorf("block %d root: %w", bp.Number, err)
	}
	if !bytes.Equal(got, trustedPostRoot) {
		return fmt.Errorf("block %d STATE ROOT MISMATCH: recomputed %x != trusted %x",
			bp.Number, got[:8], trustedPostRoot[:8])
	}
	return nil
}

// VerifyAgainstChain is the minimal-client entry point: it pulls the trusted
// pre/post state roots for bp.Number from a HeaderChain (so both anchors trace
// to the checkpoint) and verifies the state transition.
func VerifyAgainstChain(hc *HeaderChain, bp *BlockProof) error {
	post, ok := hc.TrustedStateRoot(bp.Number)
	if !ok {
		return fmt.Errorf("block %d: post stateRoot not in trusted header chain", bp.Number)
	}
	pre, ok := hc.TrustedStateRoot(bp.Number - 1)
	if !ok {
		return fmt.Errorf("block %d: pre stateRoot (block %d) not in trusted header chain", bp.Number, bp.Number-1)
	}
	return VerifyStateRoot(pre[:], post[:], bp)
}

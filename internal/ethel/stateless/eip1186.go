package stateless

import (
	"bytes"
	"fmt"
	"math/big"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// This file bridges N42's PRODUCTION EIP-1186 proof type
// (common/account.AccProofResult, as produced by lib/trie's ProofRetainer +
// FlatDBTrieLoader — the same machinery that computes eth-el's header.Root) into
// P8's stateless consumer. It lets a minimal client verify a real per-account
// multiproof received from a server against a trusted state root, reusing the
// partialTrie engine that TestAnchor* proved byte-identical to production.
//
// Two-level model reminder: the account proof is a standard MPT path to a leaf
// RLP[nonce,balance,storageRoot,codeHash]; each storage proof is the per-account
// storage subtree rooted at that leaf's storageRoot. See doc.go / stateroot.go.

// VerifiedAccount is the decoded, root-anchored outcome of an EIP-1186 account
// proof. Exists=false means the proof is a valid EXCLUSION proof (the account is
// proven absent at the state root).
type VerifiedAccount struct {
	AddrHash    types.Hash
	Nonce       uint64
	Balance     uint256.Int
	StorageRoot []byte // 32B
	CodeHash    []byte // 32B
	Exists      bool
}

// VerifyAccountInclusion verifies an EIP-1186 account proof against a trusted
// state root, then every storage-slot proof against the account's storageRoot.
//
// The account proof nodes are loaded into a partialTrie keyed by keccak; the root
// must resolve from them, so a proof that does not hash up to stateRoot fails at
// construction (newPartialTrie). The decoded leaf fields are then cross-checked
// against the AccProofResult's claimed Nonce / Balance / CodeHash / StorageHash,
// and each storage slot's proof is independently anchored at StorageHash.
//
// Returns the decoded account on success. A nil error with Exists=false is a
// proven-absent account.
func VerifyAccountInclusion(stateRoot []byte, res *account.AccProofResult) (*VerifiedAccount, error) {
	if res == nil {
		return nil, fmt.Errorf("eip1186: nil proof result")
	}
	addrHash := keccak(res.Address[:])

	at, err := newPartialTrie(stateRoot, res.AccountProof)
	if err != nil {
		return nil, fmt.Errorf("eip1186: account proof does not anchor to state root: %w", err)
	}
	leaf, found, err := at.get(keybytesToHex(addrHash))
	if err != nil {
		return nil, fmt.Errorf("eip1186: account walk: %w", err)
	}

	va := &VerifiedAccount{}
	copy(va.AddrHash[:], addrHash)
	if !found {
		// Proven absent. Cross-check the result agrees it's empty.
		if res.Nonce != 0 || (res.StorageHash != types.Hash{} && res.StorageHash != types.BytesToHash(emptyRootHash)) {
			return nil, fmt.Errorf("eip1186: exclusion proof but result claims a populated account (nonce=%d)", res.Nonce)
		}
		va.Exists = false
		return va, nil
	}

	al, err := decodeAccountLeaf(leaf)
	if err != nil {
		return nil, fmt.Errorf("eip1186: decode account leaf: %w", err)
	}
	va.Exists = true
	va.Nonce = al.nonce
	va.Balance.Set(&al.balance)
	va.StorageRoot = append([]byte(nil), al.storageRoot...)
	va.CodeHash = append([]byte(nil), al.codeHash...)

	// Cross-check the proven leaf against the result's claimed scalar fields.
	if al.nonce != res.Nonce {
		return nil, fmt.Errorf("eip1186: nonce mismatch: proof leaf %d != result %d", al.nonce, res.Nonce)
	}
	if !bytes.Equal(al.codeHash, res.CodeHash[:]) {
		return nil, fmt.Errorf("eip1186: codeHash mismatch: proof leaf %x != result %x", al.codeHash[:8], res.CodeHash[:8])
	}
	if !bytes.Equal(al.storageRoot, res.StorageHash[:]) {
		return nil, fmt.Errorf("eip1186: storageRoot mismatch: proof leaf %x != result StorageHash %x", al.storageRoot[:8], res.StorageHash[:8])
	}
	wantBal, ok := new(big.Int).SetString(res.Balance, 10)
	if !ok {
		return nil, fmt.Errorf("eip1186: bad result Balance decimal %q", res.Balance)
	}
	if al.balance.ToBig().Cmp(wantBal) != 0 {
		return nil, fmt.Errorf("eip1186: balance mismatch: proof leaf %s != result %s", al.balance.ToBig().String(), res.Balance)
	}

	// Verify each storage-slot proof against the account's storageRoot.
	for i := range res.StorageProof {
		if err := verifyStorageSlot(al.storageRoot, &res.StorageProof[i]); err != nil {
			return nil, fmt.Errorf("eip1186: storage slot %x: %w", res.StorageProof[i].Key[:8], err)
		}
	}
	return va, nil
}

// VerifyAccountMultiproof verifies a MERGED account multiproof: one deduplicated
// account-trie node set proving many accounts at once (the shared upper trie is
// stored once, ~30% smaller than N separate proofs). It builds the partialTrie
// once (which fails if the nodes don't hash to stateRoot), then walks each
// address to its leaf. Returns one VerifiedAccount per address, in input order
// (Exists=false = proven absent). This is the per-block layer-③ artifact: prove
// all touched accounts in a single request.
func VerifyAccountMultiproof(stateRoot []byte, proofNodes [][]byte, addrs []types.Address) ([]*VerifiedAccount, error) {
	at, err := newPartialTrie(stateRoot, proofNodes)
	if err != nil {
		return nil, fmt.Errorf("eip1186: multiproof does not anchor to state root: %w", err)
	}
	out := make([]*VerifiedAccount, len(addrs))
	for i := range addrs {
		addrHash := keccak(addrs[i][:])
		va := &VerifiedAccount{}
		copy(va.AddrHash[:], addrHash)
		leaf, found, err := at.get(keybytesToHex(addrHash))
		if err != nil {
			return nil, fmt.Errorf("eip1186: account %x walk: %w", addrs[i][:4], err)
		}
		if !found {
			out[i] = va // Exists=false (proven absent)
			continue
		}
		al, err := decodeAccountLeaf(leaf)
		if err != nil {
			return nil, fmt.Errorf("eip1186: account %x leaf: %w", addrs[i][:4], err)
		}
		va.Exists = true
		va.Nonce = al.nonce
		va.Balance.Set(&al.balance)
		va.StorageRoot = append([]byte(nil), al.storageRoot...)
		va.CodeHash = append([]byte(nil), al.codeHash...)
		out[i] = va
	}
	return out, nil
}

// verifyStorageSlot anchors one slot proof at storageRoot and checks the proven
// value matches the result's claimed (decimal) value.
func verifyStorageSlot(storageRoot []byte, sp *account.StorProofResult) error {
	emptyStorage := bytes.Equal(storageRoot, emptyRootHash)
	wantVal, ok := new(big.Int).SetString(sp.Value, 10)
	if !ok {
		return fmt.Errorf("bad result Value decimal %q", sp.Value)
	}

	// EmptyRoot account / zero-value slot: EIP-1186 leaves the proof empty
	// (ProofRetainer emits Value "0" and no proof bytes). Nothing to anchor.
	if emptyStorage || len(sp.Proof) == 0 {
		if wantVal.Sign() != 0 {
			return fmt.Errorf("empty storage proof but result claims non-zero value %s", sp.Value)
		}
		return nil
	}

	st, err := newPartialTrie(storageRoot, sp.Proof)
	if err != nil {
		return fmt.Errorf("proof does not anchor to storageRoot: %w", err)
	}
	slotHash := keccak(sp.Key[:])
	raw, found, err := st.get(keybytesToHex(slotHash))
	if err != nil {
		return fmt.Errorf("slot walk: %w", err)
	}
	gotVal := new(big.Int)
	if found {
		// Leaf value is RLP(trimmed big-endian word); decode the inner string.
		dec, derr := rlpDecodeBytes(raw)
		if derr != nil {
			return fmt.Errorf("decode slot value: %w", derr)
		}
		gotVal.SetBytes(dec)
	}
	if gotVal.Cmp(wantVal) != 0 {
		return fmt.Errorf("value mismatch: proof %s != result %s", gotVal.String(), wantVal.String())
	}
	return nil
}

// rlpDecodeBytes decodes a single RLP byte-string (the storage leaf value) into
// its raw bytes. A storage-trie leaf value is RLP(trimmed-BE-word) — at most a
// 32-byte word, so always a single-byte or short-string encoding.
func rlpDecodeBytes(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, nil
	}
	switch {
	case b[0] < 0x80:
		// single byte that encodes itself
		return b[:1], nil
	case b[0] <= 0xb7:
		n := int(b[0] - 0x80)
		if 1+n > len(b) {
			return nil, fmt.Errorf("short string overrun (prefix 0x%02x, have %d)", b[0], len(b))
		}
		return b[1 : 1+n], nil
	default:
		return nil, fmt.Errorf("unexpected RLP prefix 0x%02x for storage value", b[0])
	}
}

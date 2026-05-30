package stateless

import (
	"fmt"

	"github.com/holiman/uint256"
)

// accountLeaf is the decoded form of an account-trie leaf value, which N42 (like
// go-ethereum / erigon) stores as RLP([nonce, balance, storageRoot, codeHash])
// for trie hashing (common/account.StateAccount.EncodeForHashing). The stateless
// two-level updater needs to read storageRoot out (to re-root a changed storage
// subtree) and write it back (to re-hash the account leaf).
type accountLeaf struct {
	nonce       uint64
	balance     uint256.Int
	storageRoot []byte // 32B
	codeHash    []byte // 32B
}

// decodeAccountLeaf parses an RLP([nonce, balance, storageRoot, codeHash]) leaf
// value. This is the value sitting in an account-trie leaf node (valueNode).
func decodeAccountLeaf(b []byte) (*accountLeaf, error) {
	items, isList, err := splitListItems(b)
	if err != nil {
		return nil, fmt.Errorf("account leaf: %w", err)
	}
	if len(items) != 4 {
		return nil, fmt.Errorf("account leaf: %d items (want 4)", len(items))
	}
	for i, l := range isList {
		if l {
			return nil, fmt.Errorf("account leaf: item %d is a list", i)
		}
	}
	a := &accountLeaf{}
	// nonce: RLP integer (big-endian, minimal)
	for _, x := range items[0] {
		a.nonce = a.nonce<<8 | uint64(x)
	}
	a.balance.SetBytes(items[1])
	if len(items[2]) != 32 {
		return nil, fmt.Errorf("account leaf: storageRoot len %d", len(items[2]))
	}
	if len(items[3]) != 32 {
		return nil, fmt.Errorf("account leaf: codeHash len %d", len(items[3]))
	}
	a.storageRoot = append([]byte(nil), items[2]...)
	a.codeHash = append([]byte(nil), items[3]...)
	return a, nil
}

// encode returns RLP([nonce, balance, storageRoot, codeHash]) — byte-identical
// to common/account.StateAccount.EncodeForHashing (verified by test against the
// account package), so a re-hashed account leaf matches what the production
// HashBuilder produced.
func (a *accountLeaf) encode() []byte {
	var payload []byte
	payload = append(payload, rlpUint(a.nonce)...)
	payload = append(payload, rlpStr(u256Bytes(&a.balance))...)
	payload = append(payload, rlpStr(a.storageRoot)...) // 0xa0 || 32B
	payload = append(payload, rlpStr(a.codeHash)...)    // 0xa0 || 32B
	return rlpList(payload)
}

// rlpUint encodes a uint64 as a minimal-length RLP integer (no leading zeros;
// 0 -> 0x80 empty string; <0x80 -> single byte).
func rlpUint(n uint64) []byte {
	if n == 0 {
		return []byte{0x80}
	}
	var be []byte
	for x := n; x > 0; x >>= 8 {
		be = append([]byte{byte(x)}, be...)
	}
	return rlpStr(be)
}

// u256Bytes returns the minimal big-endian bytes of a uint256 (empty for zero).
func u256Bytes(v *uint256.Int) []byte {
	if v.IsZero() {
		return nil
	}
	b := v.Bytes() // already trimmed of leading zeros
	return b
}

package trie

import (
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// Account field bit flags for accountLeaf/accountLeafHash.
const (
	AccountFieldNonceOnly   uint32 = 0x01
	AccountFieldBalanceOnly uint32 = 0x02
	AccountFieldStorageOnly uint32 = 0x04
	AccountFieldCodeOnly    uint32 = 0x08
)

// Package-level scratch buffers for incarnation hex encoding (used by stream.go).
var bytes8 [8]byte
var bytes16 [16]byte

// EmptyRoot is the known root hash of an empty trie: keccak256(RLP("")) = keccak256(0x80).
var EmptyRoot = types.BytesToHash(crypto.Keccak256([]byte{0x80}))

// copyBytes returns an exact copy of the provided bytes.
func copyBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp
}

// Trie is a minimal stub for stream.go compatibility.
// Full Trie implementation not needed for CalcTrieRoot.
type Trie struct {
	root node
}

func New(root node) *Trie { return &Trie{root: root} }

// ByteArrayWriter writes into a pre-allocated buffer at a given offset.
type ByteArrayWriter struct {
	dest []byte
	pos  int
}

func (w *ByteArrayWriter) Setup(dest []byte, pos int) {
	w.dest = dest
	w.pos = pos
}

func (w *ByteArrayWriter) Write(data []byte) (int, error) {
	copy(w.dest[w.pos:], data)
	w.pos += len(data)
	return len(data), nil
}

// generateCompositeStorageKey creates key for HashedStorage table.
func generateCompositeStorageKey(addrHash types.Hash, incarnation uint64, secKey types.Hash) []byte {
	compositeKey := make([]byte, 72) // 32 + 8 + 32
	copy(compositeKey, addrHash[:])
	for i := 7; i >= 0; i-- {
		compositeKey[32+i] = byte(incarnation)
		incarnation >>= 8
	}
	copy(compositeKey[40:], secKey[:])
	return compositeKey
}

// generateStoragePrefix creates prefix for HashedStorage table.
func generateStoragePrefix(addrHash []byte, incarnation uint64) []byte {
	prefix := make([]byte, 40) // 32 + 8
	copy(prefix, addrHash)
	for i := 7; i >= 0; i-- {
		prefix[32+i] = byte(incarnation)
		incarnation >>= 8
	}
	return prefix
}

// dbutils2 is a namespace for dbutils functions used by trie_root.go.
var dbutils2 = struct {
	NextNibblesSubtree           func([]byte, *[]byte) bool
	GenerateCompositeStoragePrefix func([]byte, uint64, []byte) []byte
}{
	NextNibblesSubtree:           nextNibblesSubtree,
	GenerateCompositeStoragePrefix: generateCompositeStoragePrefix2,
}

// nextNibblesSubtree does []byte++ on nibbles. Returns false if overflow.
func nextNibblesSubtree(in []byte, out *[]byte) bool {
	r := (*out)[:len(in)]
	copy(r, in)
	for i := len(r) - 1; i >= 0; i-- {
		if r[i] != 15 {
			r[i]++
			*out = r
			return true
		}
		r = r[:i]
	}
	*out = r
	return false
}

func generateCompositeStoragePrefix2(addressHash []byte, incarnation uint64, prefix []byte) []byte {
	key := make([]byte, 32+8+len(prefix))
	copy(key, addressHash)
	for i := 7; i >= 0; i-- {
		key[32+i] = byte(incarnation)
		incarnation >>= 8
	}
	copy(key[40:], prefix)
	return key
}

// libtypes is a namespace for erigon-lib/common functions.
var libtypes = struct {
	Stopped   func(<-chan struct{}) error
	CopyBytes func([]byte) []byte
}{
	Stopped:   stopped,
	CopyBytes: copyBytes,
}

// MarshalAndPutTrieNode serializes trie node data and writes to the given bucket.
// keyHex is stored as-is (nibble format) — CalcTrieRoot expects this format.
func MarshalAndPutTrieNode(tx interface{ Put(string, []byte, []byte) error }, bucket string, keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
	if len(keyHex) == 0 {
		return nil
	}
	if hasState == 0 {
		// Node deleted — skip writing (caller handles deletion separately).
		return nil
	}
	buf := make([]byte, len(hashes)+len(rootHash)+6)
	v := MarshalTrieNode(hasState, hasTree, hasHash, hashes, rootHash, buf)
	return tx.Put(bucket, keyHex, v)
}

func stopped(ch <-chan struct{}) error {
	if ch == nil {
		return nil
	}
	select {
	case <-ch:
		return fmt.Errorf("stopped")
	default:
	}
	return nil
}

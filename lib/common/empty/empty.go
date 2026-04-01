package empty

import (
	"github.com/n42blockchain/N42/common/types"
	"golang.org/x/crypto/sha3"
)

var (
	CodeHash = func() types.Hash {
		h := sha3.NewLegacyKeccak256()
		var hash types.Hash
		h.Sum(hash[:0])
		return hash
	}()
	
	RootHash = func() types.Hash {
		h := sha3.NewLegacyKeccak256()
		// RLP of empty trie: keccak256(0x80)
		h.Write([]byte{0x80})
		var hash types.Hash
		h.Sum(hash[:0])
		return hash
	}()
)

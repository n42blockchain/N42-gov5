package txlookup

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/big"

	"github.com/n42blockchain/N42/common/types"
)

const canonicalNamespace = "canonical-transactions/v1"

// CanonicalChainIdentity uses a fixed namespace so segment directories may be
// moved between nodes on the same chain without changing their identity.
func CanonicalChainIdentity(chainID *big.Int, genesis types.Hash) (types.Hash, error) {
	if chainID == nil || chainID.Sign() < 0 || genesis == (types.Hash{}) {
		return types.Hash{}, fmt.Errorf("valid chain ID and genesis hash required")
	}
	chainBytes := chainID.Bytes()
	if uint64(len(chainBytes)) > uint64(^uint32(0)) {
		return types.Hash{}, fmt.Errorf("chain ID too large")
	}
	var size [4]byte
	h := sha256.New()
	h.Write([]byte("N42/TXLOOKUP/CHAIN/v1\x00"))
	binary.BigEndian.PutUint32(size[:], uint32(len(chainBytes)))
	h.Write(size[:])
	h.Write(chainBytes)
	h.Write(genesis[:])
	binary.BigEndian.PutUint32(size[:], uint32(len(canonicalNamespace)))
	h.Write(size[:])
	h.Write([]byte(canonicalNamespace))
	var identity types.Hash
	copy(identity[:], h.Sum(nil))
	return identity, nil
}

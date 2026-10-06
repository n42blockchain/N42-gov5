package transaction

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// A transaction decoded from Ethereum RLP (the generators' submission) and
// the same transaction decoded from the native codec (the block on the wire)
// must hash identically, or the hint feed's cache entries can never be found
// by the import.
func TestHashAgreesAcrossCodecs(t *testing.T) {
	key, _ := crypto.GenerateKey()
	signer := LatestSignerForChainID(big.NewInt(94))
	to := types.Address{0xaa}
	signed, err := SignNewTx(key, signer, &LegacyTx{Nonce: 7, GasPrice: uint256.NewInt(1), Gas: 21000, To: &to, Value: uint256.NewInt(1)})
	if err != nil {
		t.Fatal(err)
	}
	rlpBytes, err := EncodeEthereumTransaction(signed)
	if err != nil {
		t.Fatal(err)
	}
	fromRLP, err := DecodeEthereumTransaction(rlpBytes)
	if err != nil {
		t.Fatal(err)
	}
	from := crypto.PubkeyToAddress(key.PublicKey)
	signed.SetFrom(from) // the wire block carries From
	native, err := signed.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	fromNative := new(Transaction)
	if err := fromNative.Unmarshal(native); err != nil {
		t.Fatal(err)
	}
	t.Logf("signed %x rlp %x native %x", signed.Hash().Bytes()[:8], fromRLP.Hash().Bytes()[:8], fromNative.Hash().Bytes()[:8])
	if fromRLP.Hash() != fromNative.Hash() {
		t.Fatalf("hash differs: rlp %x native %x", fromRLP.Hash(), fromNative.Hash())
	}
}

package transaction

import (
	"bytes"
	"crypto/ecdsa"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/proto/types_pb"
)

func gossipRLPTestKey(t *testing.T) *ecdsa.PrivateKey {
	k, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// TestEthereumRLPRoundTripPreservesIdentity covers the encoding transactions
// travel in on the gossip mesh.
//
// The wire format used to be SSZ over the generated protobuf type, which meant
// converting every transaction into a schema-limited proto struct on the way
// out and back on the way in. That form is 55-66% larger than the standard
// Ethereum encoding for the same transaction (41% once snappy has run over the
// gossip frame), and it carries two fields the
// standard one does not: From, which a receiver must never trust from the wire
// (this codebase has already shipped one bug from doing so), and Sign, which
// nothing in the tree reads.
//
// Dropping them is safe by construction because neither participates in the
// transaction hash -- inner.hash() is an RLP hash over the standard fields
// only. This test pins that: hash and recovered sender must survive the round
// trip for every transaction type.
func TestEthereumRLPRoundTripPreservesIdentity(t *testing.T) {
	key := gossipRLPTestKey(t)
	to := types.Address{0x11, 0x22}
	chainID := uint256.NewInt(94)
	data := bytes.Repeat([]byte{0xab}, 68)
	al := AccessList{{Address: to, StorageKeys: []types.Hash{{0x01}}}}

	cases := []struct {
		name  string
		inner TxData
	}{
		{"legacy", &LegacyTx{Nonce: 7, Gas: 21000, To: &to, Value: uint256.NewInt(1e9), Data: data, GasPrice: uint256.NewInt(1e9)}},
		{"accesslist", &AccessListTx{ChainID: chainID, Nonce: 8, GasPrice: uint256.NewInt(1e9), Gas: 30000, To: &to, Value: uint256.NewInt(2), Data: data, AccessList: al}},
		{"dynamicfee", &DynamicFeeTx{ChainID: chainID, Nonce: 9, GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(1e10), Gas: 40000, To: &to, Value: uint256.NewInt(3), Data: data, AccessList: al}},
		{"contract-create", &DynamicFeeTx{ChainID: chainID, Nonce: 10, GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(1e10), Gas: 500000, To: nil, Value: uint256.NewInt(0), Data: data}},
	}

	signer := LatestSignerForChainID(chainID.ToBig())
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tx, err := SignNewTx(key, signer, c.inner)
			if err != nil {
				t.Fatalf("sign: %v", err)
			}
			enc, err := EncodeEthereumTransaction(tx)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			back, err := DecodeEthereumTransaction(enc)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if back.Hash() != tx.Hash() {
				t.Fatalf("hash changed: %s -> %s", tx.Hash(), back.Hash())
			}
			f1, err := Sender(signer, tx)
			if err != nil {
				t.Fatalf("sender orig: %v", err)
			}
			f2, err := Sender(signer, back)
			if err != nil {
				t.Fatalf("sender back: %v", err)
			}
			if f1 != f2 {
				t.Fatalf("sender changed: %s -> %s", f1, f2)
			}
			// size comparison against the current gossip encoding
			pb, err := tx.Marshal()
			if err != nil {
				t.Fatalf("proto marshal: %v", err)
			}
			pbm := tx.ToProtoMessage().(*types_pb.Transaction)
			ssz, err := pbm.MarshalSSZ()
			if err != nil {
				t.Fatalf("ssz marshal: %v", err)
			}
			t.Logf("%-16s RLP=%4d B  proto=%4d B  SSZ(gossip)=%5d B   RLP vs SSZ: %.1f%% smaller", c.name, len(enc), len(pb), len(ssz), float64(len(ssz)-len(enc))*100/float64(len(ssz)))
		})
	}
}

package transaction

import (
	"math/big"
	"reflect"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/types"
)

func TestAsMessageRecoversSenderFromSignature(t *testing.T) {
	key, err := crypto.HexToECDSA("4c0883a69102937d6231471b5dbb6204fe5129617082797f9d2f2e8a6f5f7c2f")
	if err != nil {
		t.Fatalf("HexToECDSA() error = %v", err)
	}
	signer := NewLondonSigner(big.NewInt(1))
	to := types.HexToAddress("0x1111111111111111111111111111111111111111")
	signed, err := SignTx(NewTx(&LegacyTx{
		Nonce:    1,
		GasPrice: uint256.NewInt(10),
		Gas:      21000,
		To:       &to,
		Value:    uint256.NewInt(2),
		Data:     []byte{0xaa},
	}), signer, key)
	if err != nil {
		t.Fatalf("SignTx() error = %v", err)
	}

	raw, err := EncodeEthereumTransaction(signed)
	if err != nil {
		t.Fatalf("EncodeEthereumTransaction() error = %v", err)
	}
	decoded, err := DecodeEthereumTransaction(raw)
	if err != nil {
		t.Fatalf("DecodeEthereumTransaction() error = %v", err)
	}
	if decoded.From() != nil {
		t.Fatal("DecodeEthereumTransaction() unexpectedly preserved sender")
	}

	msg, err := decoded.AsMessage(signer, nil)
	if err != nil {
		t.Fatalf("AsMessage() error = %v", err)
	}

	want := crypto.PubkeyToAddress(key.PublicKey)
	if msg.From() != want {
		t.Fatalf("Message.From() = %s, want %s", msg.From(), want)
	}
}

func TestAsMessageRejectsMissingSignerWhenSenderAbsent(t *testing.T) {
	tx := NewTx(&BlobTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      1,
		GasTipCap:  uint256.NewInt(1),
		GasFeeCap:  uint256.NewInt(2),
		Gas:        21000,
		To:         types.HexToAddress("0x2222222222222222222222222222222222222222"),
		Value:      uint256.NewInt(0),
		BlobFeeCap: uint256.NewInt(1),
		V:          uint256.NewInt(0),
		R:          uint256.NewInt(1),
		S:          uint256.NewInt(1),
	})

	if _, err := tx.AsMessage(nil, nil); err == nil {
		t.Fatal("AsMessage() error = nil, want missing signer error")
	}
}

func TestAsMessagePreservesBlobFields(t *testing.T) {
	key, err := crypto.HexToECDSA("4c0883a69102937d6231471b5dbb6204fe5129617082797f9d2f2e8a6f5f7c2f")
	if err != nil {
		t.Fatalf("HexToECDSA() error = %v", err)
	}
	signer := NewLondonSigner(big.NewInt(1))
	to := types.HexToAddress("0x2222222222222222222222222222222222222222")
	blobHash := types.HexToHash("0x010203")
	signed, err := SignTx(NewTx(&BlobTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      1,
		GasTipCap:  uint256.NewInt(1),
		GasFeeCap:  uint256.NewInt(2),
		Gas:        21000,
		To:         to,
		Value:      uint256.NewInt(0),
		BlobFeeCap: uint256.NewInt(3),
		BlobHashes: []types.Hash{blobHash},
		V:          uint256.NewInt(0),
		R:          uint256.NewInt(1),
		S:          uint256.NewInt(1),
	}), signer, key)
	if err != nil {
		t.Fatalf("SignTx() error = %v", err)
	}

	msg, err := signed.AsMessage(signer, nil)
	if err != nil {
		t.Fatalf("AsMessage() error = %v", err)
	}
	if msg.BlobFeeCap() == nil || msg.BlobFeeCap().Cmp(uint256.NewInt(3)) != 0 {
		t.Fatalf("Message.BlobFeeCap() = %v, want 3", msg.BlobFeeCap())
	}
	if len(msg.BlobHashes()) != 1 || msg.BlobHashes()[0] != blobHash {
		t.Fatalf("Message.BlobHashes() = %v, want [%v]", msg.BlobHashes(), blobHash)
	}
}

// AsMessageInto fills a reused Message with what AsMessage returns, over a
// dirty destination, for legacy, dynamic-fee and contract-creation
// transactions, and zeroes it on error.
func TestAsMessageIntoMatchesAsMessage(t *testing.T) {
	key, err := crypto.HexToECDSA("4c0883a69102937d6231471b5dbb6204fe5129617082797f9d2f2e8a6f5f7c2f")
	if err != nil {
		t.Fatalf("HexToECDSA() error = %v", err)
	}
	signer := NewLondonSigner(big.NewInt(1))
	to := types.HexToAddress("0x2222222222222222222222222222222222222222")
	chainID := uint256.NewInt(1)
	inners := []TxData{
		&LegacyTx{Nonce: 3, GasPrice: uint256.NewInt(10), Gas: 21000, To: &to, Value: uint256.NewInt(5)},
		&DynamicFeeTx{ChainID: chainID, Nonce: 4, GasTipCap: uint256.NewInt(2), GasFeeCap: uint256.NewInt(30), Gas: 50000, To: &to, Value: uint256.NewInt(7), Data: []byte{1, 2},
			AccessList: AccessList{{Address: to, StorageKeys: []types.Hash{{1}}}}},
		&LegacyTx{Nonce: 5, GasPrice: uint256.NewInt(10), Gas: 90000, Value: uint256.NewInt(0), Data: []byte{0x60, 0x00}},
	}
	var dst Message
	for i, inner := range inners {
		signed, err := SignTx(NewTx(inner), signer, key)
		if err != nil {
			t.Fatalf("tx %d: SignTx() error = %v", i, err)
		}
		for _, baseFee := range []*uint256.Int{nil, uint256.NewInt(7)} {
			want, err := signed.AsMessage(signer, baseFee)
			if err != nil {
				t.Fatalf("tx %d: AsMessage() error = %v", i, err)
			}
			dst.SetCheckNonce(true)
			dst.SetIsFree(true)
			if err := signed.AsMessageInto(&dst, signer, baseFee); err != nil {
				t.Fatalf("tx %d: AsMessageInto() error = %v", i, err)
			}
			if !reflect.DeepEqual(want, dst) {
				t.Fatalf("tx %d baseFee %v: AsMessageInto = %+v, AsMessage = %+v", i, baseFee, dst, want)
			}
		}
	}
	unsigned := NewTx(&LegacyTx{Nonce: 1, GasPrice: uint256.NewInt(1), Gas: 21000, To: &to})
	if err := unsigned.AsMessageInto(&dst, nil, nil); err == nil {
		t.Fatal("AsMessageInto without a signer or sender: want an error")
	}
	if !reflect.DeepEqual(dst, Message{}) {
		t.Fatalf("AsMessageInto left %+v after an error, want the zero Message", dst)
	}
}

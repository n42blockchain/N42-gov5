package transaction

import (
	"math/big"
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

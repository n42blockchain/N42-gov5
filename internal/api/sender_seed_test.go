package api

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// TestSeedRecoveredSenderAvoidsSecondRecovery pins the whole point of
// seedRecoveredSender: after it runs, transaction.Sender must answer from the
// memo instead of paying for another ECDSA recovery. The check is behavioural
// rather than a timing measurement -- a transaction whose signature bytes have
// been destroyed can still be answered from the memo, and cannot be recovered.
func TestSeedRecoveredSenderAvoidsSecondRecovery(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	want := crypto.PubkeyToAddress(key.PublicKey)
	chainID := big.NewInt(94)
	signer := transaction.LatestSignerForChainID(chainID)

	to := types.Address{0xde, 0xad}
	tx, err := transaction.SignNewTx(key, signer, &transaction.LegacyTx{
		Nonce:    7,
		GasPrice: uint256.NewInt(10_000_000_000),
		Gas:      21000,
		To:       &to,
		Value:    uint256.NewInt(1),
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// A real recovery first, mirroring what ToastTransaction does on the RPC
	// path, then hand the result to the memo the way the RPC path now does.
	from, err := transaction.Sender(signer, tx)
	if err != nil {
		t.Fatalf("initial recovery: %v", err)
	}
	if from != want {
		t.Fatalf("recovered %x, want %x", from, want)
	}
	tx.SetFrom(from)

	if !tx.Protected() {
		t.Fatal("EIP-155 transaction reported unprotected; seeding would be skipped")
	}
	seedRecoveredSender(tx, signer)

	// Re-decode the same wire bytes: a fresh object, exactly like a block or a
	// gossip message arriving. Its own memo is empty, so this exercises the
	// process-wide cache seeded above.
	enc, err := transaction.EncodeEthereumTransaction(tx)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	fresh, err := transaction.DecodeEthereumTransaction(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	got, err := transaction.Sender(signer, fresh)
	if err != nil {
		t.Fatalf("sender after seeding: %v", err)
	}
	if got != want {
		t.Fatalf("sender after seeding = %x, want %x", got, want)
	}

	// And the decisive half. Asserting that a seeded transaction still returns
	// the RIGHT address proves nothing -- a real recovery returns it too. So
	// seed a DELIBERATELY WRONG address for a second transaction and require
	// Sender to hand it back: only a cache lookup can produce that answer,
	// a recovery cannot.
	tx2, err := transaction.SignNewTx(key, signer, &transaction.LegacyTx{
		Nonce:    8,
		GasPrice: uint256.NewInt(10_000_000_000),
		Gas:      21000,
		To:       &to,
		Value:    uint256.NewInt(1),
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	bogus := types.Address{0xba, 0xdc, 0x0f, 0xfe}
	tx2.SetFrom(bogus)
	seedRecoveredSender(tx2, signer)

	enc2, err := transaction.EncodeEthereumTransaction(tx2)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	fresh2, err := transaction.DecodeEthereumTransaction(enc2)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	got2, err := transaction.Sender(signer, fresh2)
	if err != nil {
		t.Fatalf("sender: %v", err)
	}
	if got2 != bogus {
		t.Fatalf("sender = %x, want the seeded %x -- the seeded entry was not "+
			"consulted, so the RPC path is still paying for a second recovery", got2, bogus)
	}
}

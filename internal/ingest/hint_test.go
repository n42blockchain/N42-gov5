package ingest

import (
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// Hint-only mode: the batch is decoded, each sender is recovered from the
// signature into the sender cache, nothing reaches the pool, and the
// client's sender field -- deliberately wrong here -- is never trusted.
func TestIngestServer_HintOnlyRecoversFromSignature(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	want := crypto.PubkeyToAddress(key.PublicKey)
	signer := transaction.LatestSignerForChainID(big.NewInt(94))
	to := types.Address{0xaa}
	signed, err := transaction.SignNewTx(key, signer, &transaction.LegacyTx{
		Nonce: 7, GasPrice: uint256.NewInt(1), Gas: 21000, To: &to, Value: uint256.NewInt(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	// The generators submit Ethereum RLP; that is what the feed carries.
	raw, err := transaction.EncodeEthereumTransaction(signed)
	if err != nil {
		t.Fatal(err)
	}

	pool := &mockTxPool{}
	srv := NewServer("127.0.0.1:0", pool, 1000, 2000)
	srv.EnableHintOnly(signer, 2)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	conn, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if got := writeBatch(t, conn, raw, 3); got != 3 {
		t.Fatalf("queued %d of 3", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for srv.Hinted() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if srv.Hinted() != 3 {
		t.Fatalf("hinted %d of 3", srv.Hinted())
	}
	if pool.Len() != 0 {
		t.Fatalf("hint-only mode admitted %d transactions to the pool", pool.Len())
	}
	// A fresh decode -- no memo on the object -- recovers through the cache
	// to the signer's address, not to the bogus sender the client sent.
	fresh, err := transaction.DecodeEthereumTransaction(raw)
	if err != nil {
		t.Fatal(err)
	}
	got, err := transaction.RecoverSenderFromSig(signer, fresh)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("recovered %s, want %s", got.Hex(), want.Hex())
	}
}

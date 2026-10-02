package sync

import (
	"bytes"
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

type syncURecordingTxPool struct {
	added []*transaction.Transaction
	errs  []error
}

func (p *syncURecordingTxPool) AddRemotes(txs []*transaction.Transaction) []error {
	p.added = append(p.added, txs...)
	if p.errs != nil {
		return p.errs
	}
	return make([]error, len(txs))
}
func (p *syncURecordingTxPool) GetTx(types.Hash) *transaction.Transaction { return nil }

func newSignedTestTx(t *testing.T, nonce uint64) *transaction.Transaction {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	chainID := uint256.NewInt(94)
	to := types.Address{0x11, 0x22, 0x33}
	signer := transaction.LatestSignerForChainID(chainID.ToBig())
	tx, err := transaction.SignNewTx(key, signer, &transaction.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     nonce,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(1e10),
		Gas:       21000,
		To:        &to,
		Value:     uint256.NewInt(1e18),
	})
	if err != nil {
		t.Fatalf("SignNewTx: %v", err)
	}
	return tx
}

// TestTxSubscriberAcceptsSingleTransaction covers txSubscriber's single
// (non-batch) RLP decode path and the pool-accepted counting branch.
func TestTxSubscriberAcceptsSingleTransaction(t *testing.T) {
	tx := newSignedTestTx(t, 1)
	rlpBytes, err := transaction.EncodeEthereumTransaction(tx)
	if err != nil {
		t.Fatalf("EncodeEthereumTransaction: %v", err)
	}

	pool := &syncURecordingTxPool{}
	svc := &Service{cfg: &config{txPool: pool}}

	if err := svc.txSubscriber(context.Background(), &rawSSZBytes{data: rlpBytes}); err != nil {
		t.Fatalf("txSubscriber: %v", err)
	}
	if len(pool.added) != 1 || pool.added[0].Hash() != tx.Hash() {
		t.Fatalf("expected the decoded tx to reach AddRemotes, got %v", pool.added)
	}
}

// TestTxSubscriberIgnoresWrongDataType covers the type-assertion guard.
func TestTxSubscriberIgnoresWrongDataType(t *testing.T) {
	svc := &Service{cfg: &config{txPool: &syncURecordingTxPool{}}}
	if err := svc.txSubscriber(context.Background(), "not-raw-bytes"); err != nil {
		t.Fatalf("txSubscriber() on wrong type = %v, want nil", err)
	}
}

// TestTxSubscriberSwallowsDecodeError covers the undecodable-payload branch:
// neither a valid batch nor a valid single transaction, so the message is
// dropped without penalizing the peer (nil error, no pool call).
func TestTxSubscriberSwallowsDecodeError(t *testing.T) {
	pool := &syncURecordingTxPool{}
	svc := &Service{cfg: &config{txPool: pool}}
	garbage := bytes.Repeat([]byte{0xff}, 16)
	if err := svc.txSubscriber(context.Background(), &rawSSZBytes{data: garbage}); err != nil {
		t.Fatalf("txSubscriber() on garbage payload = %v, want nil", err)
	}
	if len(pool.added) != 0 {
		t.Fatalf("expected no pool calls for an undecodable payload, got %v", pool.added)
	}
}

// TestTxSubscriberLogsRejectedTransactions covers the per-tx rejection
// branch when the pool returns an error for an accepted-into-pool call.
func TestTxSubscriberLogsRejectedTransactions(t *testing.T) {
	tx := newSignedTestTx(t, 2)
	rlpBytes, err := transaction.EncodeEthereumTransaction(tx)
	if err != nil {
		t.Fatalf("EncodeEthereumTransaction: %v", err)
	}
	pool := &syncURecordingTxPool{errs: []error{context.DeadlineExceeded}}
	svc := &Service{cfg: &config{txPool: pool}}

	if err := svc.txSubscriber(context.Background(), &rawSSZBytes{data: rlpBytes}); err != nil {
		t.Fatalf("txSubscriber: %v", err)
	}
	if len(pool.added) != 1 {
		t.Fatalf("expected the tx to still reach AddRemotes, got %v", pool.added)
	}
}

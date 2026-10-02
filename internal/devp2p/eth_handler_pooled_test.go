package devp2p

import (
	"bytes"
	"testing"

	gethp2p "github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"

	n42block "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/internal/network/eth69"
)

// pooledTxPoolStub is a minimal common.ITxsPool stand-in for exercising the
// pooled-transaction request/response and announcement paths without a real
// mempool.
type pooledTxPoolStub struct {
	byHash  map[types.Hash]*transaction.Transaction
	pending []*transaction.Transaction
	added   []*transaction.Transaction
}

func (s *pooledTxPoolStub) Stop() error { return nil }
func (s *pooledTxPoolStub) Has(hash types.Hash) bool {
	_, ok := s.byHash[hash]
	return ok
}
func (s *pooledTxPoolStub) Pending(bool) map[types.Address][]*transaction.Transaction { return nil }
func (s *pooledTxPoolStub) GetTransaction() ([]*transaction.Transaction, error) {
	return s.pending, nil
}
func (s *pooledTxPoolStub) GetTx(hash types.Hash) *transaction.Transaction {
	return s.byHash[hash]
}
func (s *pooledTxPoolStub) AddRemotes(txs []*transaction.Transaction) []error {
	s.added = append(s.added, txs...)
	return make([]error, len(txs))
}
func (s *pooledTxPoolStub) AddLocal(*transaction.Transaction) error    { return nil }
func (s *pooledTxPoolStub) AddLocals([]*transaction.Transaction) []error { return nil }
func (s *pooledTxPoolStub) Stats() (int, int, int, int)                { return 0, 0, 0, 0 }
func (s *pooledTxPoolStub) Nonce(types.Address) uint64                 { return 0 }
func (s *pooledTxPoolStub) Content() (map[types.Address][]*transaction.Transaction, map[types.Address][]*transaction.Transaction) {
	return nil, nil
}

func pooledTestSignedTx(t *testing.T, nonce uint64) *transaction.Transaction {
	t.Helper()
	key, err := crypto.HexToECDSA("4c0883a69102937d6231471b5dbb6204fe512961708279c7b07b8d2f2b1dd1c7")
	if err != nil {
		t.Fatal(err)
	}
	tx := transaction.NewTx(&transaction.LegacyTx{
		Nonce:    nonce,
		GasPrice: uint256.NewInt(1),
		Gas:      21000,
		Value:    uint256.NewInt(0),
	})
	signed, err := transaction.SignTx(tx, transaction.FrontierSigner{}, key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func TestPooledTransactionRawValueRoundTrip(t *testing.T) {
	signed := pooledTestSignedTx(t, 1)
	encoded, err := transaction.EncodeEthereumPooledTransaction(signed)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := pooledTransactionRawValue(encoded)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodePooledTransactionRaw(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Hash() != signed.Hash() {
		t.Fatalf("decoded hash = %s, want %s", decoded.Hash(), signed.Hash())
	}
}

func TestPooledTransactionRawValueRejectsEmpty(t *testing.T) {
	if _, err := pooledTransactionRawValue(nil); err == nil {
		t.Fatal("expected error for empty encoded transaction")
	}
	if _, err := decodePooledTransactionRaw(nil); err == nil {
		t.Fatal("expected error for empty raw value")
	}
}

func TestHandleGetPooledTransactionsServesKnownHashes(t *testing.T) {
	signed := pooledTestSignedTx(t, 2)
	pool := &pooledTxPoolStub{byHash: map[types.Hash]*transaction.Transaction{signed.Hash(): signed}}
	h := &EthHandler{txpool: pool}

	missing := types.HexToHash("0xdead")
	req := eth69.GetPooledTransactionsPacket{RequestID: 9, Hashes: []types.Hash{signed.Hash(), missing}}
	encoded, err := rlp.EncodeToBytes(&req)
	if err != nil {
		t.Fatal(err)
	}
	msg := gethp2p.Msg{Code: 9, Size: uint32(len(encoded)), Payload: bytes.NewReader(encoded)}
	rw1, rw2 := gethp2p.MsgPipe()
	defer rw1.Close()
	defer rw2.Close()
	errCh := make(chan error, 1)
	go func() { errCh <- h.handleGetPooledTransactions(rw1, msg) }()

	respMsg, err := rw2.ReadMsg()
	if err != nil {
		t.Fatal(err)
	}
	var resp pooledTransactionsPacket
	if err := respMsg.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if resp.RequestID != 9 || len(resp.Transactions) != 1 {
		t.Fatalf("response = id %d, %d txs, want 1", resp.RequestID, len(resp.Transactions))
	}
}

func TestHandleGetPooledTransactionsNoTxPool(t *testing.T) {
	h := &EthHandler{}
	req := eth69.GetPooledTransactionsPacket{RequestID: 1, Hashes: []types.Hash{types.HexToHash("0x01")}}
	encoded, err := rlp.EncodeToBytes(&req)
	if err != nil {
		t.Fatal(err)
	}
	msg := gethp2p.Msg{Code: 9, Size: uint32(len(encoded)), Payload: bytes.NewReader(encoded)}
	rw1, rw2 := gethp2p.MsgPipe()
	defer rw1.Close()
	defer rw2.Close()
	errCh := make(chan error, 1)
	go func() { errCh <- h.handleGetPooledTransactions(rw1, msg) }()

	respMsg, err := rw2.ReadMsg()
	if err != nil {
		t.Fatal(err)
	}
	var resp pooledTransactionsPacket
	if err := respMsg.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if len(resp.Transactions) != 0 {
		t.Fatalf("transactions = %d, want 0 with nil txpool", len(resp.Transactions))
	}
}

// acceptPooledHeadStub is the minimal BlockProvider used to drive
// acceptPooledTransactions's signer selection.
type acceptPooledHeadStub struct{}

func (acceptPooledHeadStub) CurrentHead() (*n42block.Header, types.Hash, error) {
	return &n42block.Header{Number: uint256.NewInt(0), Time: 0}, types.Hash{}, nil
}
func (acceptPooledHeadStub) GetHeaderByNumber(uint64) (*n42block.Header, error) { return nil, nil }
func (acceptPooledHeadStub) GetHeaderByHash(types.Hash) (*n42block.Header, error) {
	return nil, nil
}

func TestAcceptPooledTransactionsAddsValidAndSkipsMalformed(t *testing.T) {
	signed := pooledTestSignedTx(t, 3)
	encoded, err := transaction.EncodeEthereumPooledTransaction(signed)
	if err != nil {
		t.Fatal(err)
	}
	good, err := pooledTransactionRawValue(encoded)
	if err != nil {
		t.Fatal(err)
	}
	pool := &pooledTxPoolStub{byHash: map[types.Hash]*transaction.Transaction{}}
	h := &EthHandler{txpool: pool, provider: acceptPooledHeadStub{}}

	if err := h.acceptPooledTransactions([]rlp.RawValue{good, {0x80}}); err != nil {
		t.Fatalf("acceptPooledTransactions() error = %v", err)
	}
	if len(pool.added) != 1 {
		t.Fatalf("added = %d, want 1 (malformed entry should be skipped)", len(pool.added))
	}
	if pool.added[0].Hash() != signed.Hash() {
		t.Fatalf("added hash = %s, want %s", pool.added[0].Hash(), signed.Hash())
	}
}

func TestAcceptPooledTransactionsNoopWithoutTxPool(t *testing.T) {
	h := &EthHandler{}
	if err := h.acceptPooledTransactions([]rlp.RawValue{{0xc0}}); err != nil {
		t.Fatalf("acceptPooledTransactions() error = %v, want nil", err)
	}
}

func TestAnnouncePooledTransactionsSendsNewHashes(t *testing.T) {
	signed := pooledTestSignedTx(t, 4)
	pool := &pooledTxPoolStub{pending: []*transaction.Transaction{signed}}
	h := &EthHandler{txpool: pool}

	rw1, rw2 := gethp2p.MsgPipe()
	defer rw1.Close()
	defer rw2.Close()
	done := make(chan struct{})
	go h.announcePooledTransactions(rw1, done)
	defer close(done)

	msg, err := rw2.ReadMsg()
	if err != nil {
		t.Fatal(err)
	}
	if msg.Code != 8 {
		t.Fatalf("msg code = %d, want 8", msg.Code)
	}
	var packet eth69.NewPooledTransactionHashesPacket
	if err := msg.Decode(&packet); err != nil {
		t.Fatal(err)
	}
	if len(packet.Hashes) != 1 || packet.Hashes[0] != signed.Hash() {
		t.Fatalf("announced hashes = %v, want [%s]", packet.Hashes, signed.Hash())
	}
}

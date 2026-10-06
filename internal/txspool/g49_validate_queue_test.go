package txspool

import (
	"context"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/params"
)

// g49NewValidationPool builds a bare *TxsPool sufficient for
// validateTx: chain config + a mock ReadState, no running goroutines,
// no IBlockChain needed at all (validateTx never touches pool.bc).
func g49NewValidationPool() *TxsPool {
	current := &txpoolBlockStub{
		header: &txpoolHeaderStub{number: uint256.NewInt(1), hash: types.HexToHash("0x1")},
	}
	return &TxsPool{
		chainconfig:   params.TestChainConfig,
		currentState:  newMockReadState(),
		currentMaxGas: 30_000_000,
		gasPrice:      uint256.NewInt(1),
		eip2718:       true,
		eip1559:       true,
		bc:            &txpoolChainStub{current: current},
	}
}

func TestValidateTx_RejectsUnsupportedTypeBeforeEIP2718(t *testing.T) {
	pool := g49NewValidationPool()
	pool.eip2718 = false

	key, from := generateTestKey()
	tx := newSignedDynamicFeeTx(key, 0, 1, 1)
	tx.SetFrom(from)

	if err := pool.validateTx(tx, true, nil); err != internal.ErrTxTypeNotSupported {
		t.Errorf("expected ErrTxTypeNotSupported, got %v", err)
	}
}

func TestValidateTx_RejectsDynamicFeeBeforeEIP1559(t *testing.T) {
	pool := g49NewValidationPool()
	pool.eip1559 = false

	key, from := generateTestKey()
	tx := newSignedDynamicFeeTx(key, 0, 1, 1)
	tx.SetFrom(from)

	if err := pool.validateTx(tx, true, nil); err != internal.ErrTxTypeNotSupported {
		t.Errorf("expected ErrTxTypeNotSupported, got %v", err)
	}
}

func TestValidateTx_RejectsNegativeValueAndNilSender(t *testing.T) {
	pool := g49NewValidationPool()

	key, from := generateTestKey()
	pool.currentState.(*mockReadState).balances[from] = uint256.NewInt(1_000_000_000_000)
	tx := newSignedTestTx(key, 0, 1000)
	tx.SetFrom(from)
	if err := pool.validateTx(tx, true, nil); err != nil {
		t.Fatalf("expected a well-formed signed tx to validate, got %v", err)
	}

	// No From at all.
	inner := &transaction.LegacyTx{
		Nonce:    0,
		GasPrice: uint256.NewInt(1000),
		Gas:      21000,
		To:       &types.Address{0x01},
		Value:    uint256.NewInt(100),
	}
	noFrom := transaction.NewTx(inner)
	if err := pool.validateTx(noFrom, true, nil); err != ErrInvalidSender {
		t.Errorf("expected ErrInvalidSender, got %v", err)
	}
}

func TestValidateTx_RejectsGasLimitAboveBlock(t *testing.T) {
	pool := g49NewValidationPool()
	pool.currentMaxGas = 1000 // below the 21000 a basic transfer needs

	key, from := generateTestKey()
	tx := newSignedTestTx(key, 0, 1000)
	tx.SetFrom(from)

	if err := pool.validateTx(tx, true, nil); err != ErrGasLimit {
		t.Errorf("expected ErrGasLimit, got %v", err)
	}
}

func TestValidateTx_RejectsUnderpricedRemote(t *testing.T) {
	pool := g49NewValidationPool()
	pool.gasPrice = uint256.NewInt(1_000_000)

	key, from := generateTestKey()
	pool.currentState.(*mockReadState).balances[from] = uint256.NewInt(1_000_000_000_000)
	tx := newSignedTestTx(key, 0, 100) // gasPrice=100, below pool.gasPrice
	tx.SetFrom(from)

	if err := pool.validateTx(tx, false /* not local */, nil); err != ErrUnderpriced {
		t.Errorf("expected ErrUnderpriced for a cheap remote tx, got %v", err)
	}
	// The same tx submitted as LOCAL bypasses the underpriced gate.
	if err := pool.validateTx(tx, true, nil); err != nil {
		t.Errorf("expected local tx to bypass underpriced gate, got %v", err)
	}
}

func TestValidateTx_RejectsNonceTooLow(t *testing.T) {
	pool := g49NewValidationPool()
	key, from := generateTestKey()
	mrs := pool.currentState.(*mockReadState)
	mrs.nonces[from] = 5

	tx := newSignedTestTx(key, 2, 1000) // nonce 2 < current nonce 5
	tx.SetFrom(from)
	if err := pool.validateTx(tx, true, nil); err != ErrNonceTooLow {
		t.Errorf("expected ErrNonceTooLow, got %v", err)
	}
}

// TestQueueTxEvent_DeliversAndRespectsCancellation covers both branches
// of queueTxEvent's select: a successful send/receive pair, and the
// ctx.Done() path once the pool is canceled.
func TestQueueTxEvent_DeliversAndRespectsCancellation(t *testing.T) {
	pool := g49NewValidationPool()
	pool.queueTxEventCh = make(chan *transaction.Transaction)
	pool.ctx, pool.cancel = context.WithCancel(context.Background())

	key, from := generateTestKey()
	tx := newSignedTestTx(key, 0, 1000)
	tx.SetFrom(from)

	done := make(chan struct{})
	go func() {
		pool.queueTxEvent(tx)
		close(done)
	}()
	select {
	case got := <-pool.queueTxEventCh:
		if got != tx {
			t.Error("received a different tx than sent")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queueTxEvent to deliver")
	}
	<-done

	// Cancel the pool context; queueTxEvent must return promptly via
	// the ctx.Done() branch instead of blocking forever (nothing reads
	// queueTxEventCh anymore).
	pool.cancel()
	done2 := make(chan struct{})
	go func() {
		pool.queueTxEvent(tx)
		close(done2)
	}()
	select {
	case <-done2:
	case <-time.After(2 * time.Second):
		t.Fatal("queueTxEvent did not return after context cancellation")
	}
}

// g49LinearExtChainStub implements just enough of common.IBlockChain
// (via embedding, for the unused methods) plus headerByHashReader for
// linearExtension's ancestor walk.
type g49LinearExtChainStub struct {
	txpoolChainStub
	headers map[types.Hash]block.IHeader
}

func (c *g49LinearExtChainStub) GetHeaderByHash(hash types.Hash) (block.IHeader, error) {
	return c.headers[hash], nil
}

func TestLinearExtension_DetectsAncestorAndNonAncestor(t *testing.T) {
	// Chain: old(#1, hash=0x1) -> mid(#2, hash=0x2) -> new(#3, hash=0x3)
	oldHash := types.HexToHash("0x1")
	midHash := types.HexToHash("0x2")
	newHash := types.HexToHash("0x3")

	midHeader := &block.Header{Number: uint256.NewInt(2)}
	midHeader.ParentHash = oldHash
	// Header.Hash() depends on RLP content, so we key the stub map by
	// the hash the test expects rather than computed hash — the stub
	// returns whatever GetHeaderByHash(h) is asked for using the
	// requested hash as the key, matching linearExtension's own walk
	// (it looks up by the hash it is chasing, not by asserting the
	// returned header's own hash).
	stub := &g49LinearExtChainStub{
		headers: map[types.Hash]block.IHeader{
			midHash: &block.Header{Number: uint256.NewInt(2), ParentHash: oldHash},
		},
	}

	pool := &TxsPool{bc: stub}

	newBlock := &txpoolBlockStub{
		header:     &txpoolHeaderStub{number: uint256.NewInt(3), hash: newHash},
		parentHash: midHash,
	}

	if !pool.linearExtension(oldHash, 1, newBlock, 3) {
		t.Error("expected old(#1) to be recognized as an ancestor of new(#3) via mid(#2)")
	}

	// Non-ancestor: mid's parent does NOT match oldHash.
	stub.headers[midHash] = &block.Header{Number: uint256.NewInt(2), ParentHash: types.HexToHash("0xDEAD")}
	if pool.linearExtension(oldHash, 1, newBlock, 3) {
		t.Error("expected false when the walked chain does not lead back to oldHash")
	}

	// newNum <= oldNum short-circuits to false without consulting bc.
	if pool.linearExtension(oldHash, 5, newBlock, 3) {
		t.Error("expected false when newNum <= oldNum")
	}
}

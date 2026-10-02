package filters

import (
	"math/big"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

func g41NewEventSystem() (*EventSystem, Api) {
	api := &filterAPIStub{bc: &filterChainStub{}}
	return NewEventSystem(api), api
}

func TestSubscribeLogsRouting(t *testing.T) {
	es, _ := g41NewEventSystem()

	// pending/pending -> subscribePendingLogs
	logsCh := make(chan []*block.Log)
	sub, err := es.SubscribeLogs(FilterCriteria{
		FromBlock: big.NewInt(jsonrpc.PendingBlockNumber.Int64()),
		ToBlock:   big.NewInt(jsonrpc.PendingBlockNumber.Int64()),
	}, logsCh)
	if err != nil {
		t.Fatalf("SubscribeLogs(pending/pending) error: %v", err)
	}
	if sub.f.typ != PendingLogsSubscription {
		t.Fatalf("expected PendingLogsSubscription, got %v", sub.f.typ)
	}
	sub.Unsubscribe()

	// latest/latest -> subscribeLogs
	sub, err = es.SubscribeLogs(FilterCriteria{}, logsCh)
	if err != nil {
		t.Fatalf("SubscribeLogs(latest/latest) error: %v", err)
	}
	if sub.f.typ != LogsSubscription {
		t.Fatalf("expected LogsSubscription, got %v", sub.f.typ)
	}
	sub.Unsubscribe()

	// explicit range from>=0, to>=from -> subscribeLogs
	sub, err = es.SubscribeLogs(FilterCriteria{FromBlock: big.NewInt(1), ToBlock: big.NewInt(5)}, logsCh)
	if err != nil {
		t.Fatalf("SubscribeLogs(range) error: %v", err)
	}
	if sub.f.typ != LogsSubscription {
		t.Fatalf("expected LogsSubscription for range, got %v", sub.f.typ)
	}
	sub.Unsubscribe()

	// from latest, to pending -> subscribeMinedPendingLogs
	sub, err = es.SubscribeLogs(FilterCriteria{
		FromBlock: big.NewInt(jsonrpc.LatestBlockNumber.Int64()),
		ToBlock:   big.NewInt(jsonrpc.PendingBlockNumber.Int64()),
	}, logsCh)
	if err != nil {
		t.Fatalf("SubscribeLogs(latest/pending) error: %v", err)
	}
	if sub.f.typ != MinedAndPendingLogsSubscription {
		t.Fatalf("expected MinedAndPendingLogsSubscription, got %v", sub.f.typ)
	}
	sub.Unsubscribe()

	// from specific, to latest -> subscribeLogs
	sub, err = es.SubscribeLogs(FilterCriteria{FromBlock: big.NewInt(1), ToBlock: big.NewInt(jsonrpc.LatestBlockNumber.Int64())}, logsCh)
	if err != nil {
		t.Fatalf("SubscribeLogs(from/latest) error: %v", err)
	}
	if sub.f.typ != LogsSubscription {
		t.Fatalf("expected LogsSubscription, got %v", sub.f.typ)
	}
	sub.Unsubscribe()

	// invalid: from > to
	_, err = es.SubscribeLogs(FilterCriteria{FromBlock: big.NewInt(10), ToBlock: big.NewInt(1)}, logsCh)
	if err == nil {
		t.Fatal("expected error for from > to")
	}
}

func TestSubscribeNewHeadsAndPendingTxs(t *testing.T) {
	es, _ := g41NewEventSystem()

	headers := make(chan block.IHeader)
	headSub := es.SubscribeNewHeads(headers)
	if headSub.f.typ != BlocksSubscription {
		t.Fatalf("expected BlocksSubscription, got %v", headSub.f.typ)
	}
	headSub.Unsubscribe()

	hashes := make(chan []types.Hash)
	txSub := es.SubscribePendingTxs(hashes)
	if txSub.f.typ != PendingTransactionsSubscription {
		t.Fatalf("expected PendingTransactionsSubscription, got %v", txSub.f.typ)
	}
	txSub.Unsubscribe()
}

func TestHandleLogsDeliversMatchingLogs(t *testing.T) {
	es, _ := g41NewEventSystem()
	addr := g41Addr(1)
	logsCh := make(chan []*block.Log, 1)
	sub := es.subscribeLogs(FilterCriteria{Addresses: []types.Address{addr}}, logsCh)
	defer sub.Unsubscribe()

	// Give eventLoop a moment to install the filter.
	time.Sleep(20 * time.Millisecond)

	es.logsCh <- common.NewLogsEvent{Logs: []*block.Log{g41Log(addr, 1), g41Log(g41Addr(2), 1)}}

	select {
	case got := <-logsCh:
		if len(got) != 1 || got[0].Address != addr {
			t.Fatalf("expected 1 matching log for addr, got %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for matched logs")
	}

	// Empty log batch should not panic or block.
	es.logsCh <- common.NewLogsEvent{Logs: nil}
	time.Sleep(20 * time.Millisecond)
}

func TestHandlePendingLogsDeliversMatchingLogs(t *testing.T) {
	es, _ := g41NewEventSystem()
	addr := g41Addr(3)
	logsCh := make(chan []*block.Log, 1)
	sub := es.subscribePendingLogs(FilterCriteria{Addresses: []types.Address{addr}}, logsCh)
	defer sub.Unsubscribe()
	time.Sleep(20 * time.Millisecond)

	es.pendingLogsCh <- common.NewPendingLogsEvent{Logs: []*block.Log{g41Log(addr, 1)}}

	select {
	case got := <-logsCh:
		if len(got) != 1 {
			t.Fatalf("expected 1 pending log, got %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for pending logs")
	}

	es.pendingLogsCh <- common.NewPendingLogsEvent{Logs: nil}
	time.Sleep(20 * time.Millisecond)
}

func TestHandleRemovedLogsDeliversMatchingLogs(t *testing.T) {
	es, _ := g41NewEventSystem()
	addr := g41Addr(4)
	logsCh := make(chan []*block.Log, 1)
	sub := es.subscribeLogs(FilterCriteria{Addresses: []types.Address{addr}}, logsCh)
	defer sub.Unsubscribe()
	time.Sleep(20 * time.Millisecond)

	es.rmLogsCh <- common.RemovedLogsEvent{Logs: []*block.Log{g41Log(addr, 1)}}

	select {
	case got := <-logsCh:
		if len(got) != 1 {
			t.Fatalf("expected 1 removed log, got %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for removed logs")
	}
}

func TestHandleTxsEventDeliversHashes(t *testing.T) {
	es, _ := g41NewEventSystem()
	hashes := make(chan []types.Hash, 1)
	sub := es.SubscribePendingTxs(hashes)
	defer sub.Unsubscribe()
	time.Sleep(20 * time.Millisecond)

	tx := transaction.NewTx(&transaction.LegacyTx{Nonce: 1})
	es.txsCh <- common.NewTxsEvent{Txs: []*transaction.Transaction{tx}}

	select {
	case got := <-hashes:
		if len(got) != 1 {
			t.Fatalf("expected 1 tx hash, got %d", len(got))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for tx hashes")
	}
}

func TestHandleChainEventDeliversHeader(t *testing.T) {
	es, _ := g41NewEventSystem()
	headers := make(chan block.IHeader, 1)
	sub := es.SubscribeNewHeads(headers)
	defer sub.Unsubscribe()
	time.Sleep(20 * time.Millisecond)

	h := g41MakeHeader(1, block.Bloom{})
	b := block.NewBlock(h, nil)
	bPtr, ok := b.(*block.Block)
	if !ok {
		t.Fatal("NewBlock did not return *block.Block")
	}

	es.chainCh <- common.ChainHighestBlock{Block: *bPtr, Inserted: true}

	select {
	case got := <-headers:
		if got == nil {
			t.Fatal("expected a non-nil header")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for new head")
	}

	// Non-inserted events should be ignored by eventLoop.
	es.chainCh <- common.ChainHighestBlock{Block: *bPtr, Inserted: false}
	time.Sleep(20 * time.Millisecond)
	select {
	case <-headers:
		t.Fatal("did not expect a header for a non-inserted chain event")
	default:
	}
}

func TestUninstallMinedAndPendingLogsSubscription(t *testing.T) {
	es, _ := g41NewEventSystem()
	logsCh := make(chan []*block.Log)
	sub := es.subscribeMinedPendingLogs(FilterCriteria{}, logsCh)
	if sub.f.typ != MinedAndPendingLogsSubscription {
		t.Fatalf("expected MinedAndPendingLogsSubscription, got %v", sub.f.typ)
	}
	sub.Unsubscribe()
}

func TestSubscriptionErrChannel(t *testing.T) {
	es, _ := g41NewEventSystem()
	logsCh := make(chan []*block.Log)
	sub := es.subscribeLogs(FilterCriteria{}, logsCh)

	select {
	case <-sub.Err():
		t.Fatal("Err() channel should not be closed before Unsubscribe")
	default:
	}

	sub.Unsubscribe()

	select {
	case _, open := <-sub.Err():
		if open {
			t.Fatal("expected Err() channel to be closed after Unsubscribe")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Err() channel to close")
	}

	// Calling Unsubscribe a second time must be a safe no-op.
	sub.Unsubscribe()
}

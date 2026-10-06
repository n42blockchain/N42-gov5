package txspool

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// TestTxDifference_PureSet covers txDifference: elements of a not present in
// b are kept, elements present in both are dropped, in sender-and-hash
// terms rather than object identity.
func TestTxDifference_PureSet(t *testing.T) {
	pool := &TxsPool{}

	key1, from1 := generateTestKey()
	tx1 := newSignedTestTx(key1, 0, 1000)
	tx1.SetFrom(from1)

	key2, from2 := generateTestKey()
	tx2 := newSignedTestTx(key2, 0, 1000)
	tx2.SetFrom(from2)

	key3, from3 := generateTestKey()
	tx3 := newSignedTestTx(key3, 0, 1000)
	tx3.SetFrom(from3)

	a := []*transaction.Transaction{tx1, tx2, tx3}
	b := []*transaction.Transaction{tx2}

	got := pool.txDifference(a, b)
	if len(got) != 2 {
		t.Fatalf("txDifference len = %d, want 2", len(got))
	}
	for _, tx := range got {
		if tx.Hash() == tx2.Hash() {
			t.Fatal("txDifference kept a transaction present in b")
		}
	}

	// Empty b: difference is a unchanged (by hash).
	gotAll := pool.txDifference(a, nil)
	if len(gotAll) != len(a) {
		t.Fatalf("txDifference(a, nil) len = %d, want %d", len(gotAll), len(a))
	}

	// a empty: always empty.
	if got := pool.txDifference(nil, b); len(got) != 0 {
		t.Fatalf("txDifference(nil, b) len = %d, want 0", len(got))
	}
}

// TestRunReorg_PromoteOnlyPath drives runReorg directly with reset==nil and
// a populated dirtyAccounts set, covering the promote-only branch (no
// pool.reset, no demoteUnexecutables/baseFee/nonce-reset work) that
// scheduleLoop takes for a pure "new local/remote tx" promotion cycle.
func TestRunReorg_PromoteOnlyPath(t *testing.T) {
	current := &txpoolBlockStub{
		header:   &txpoolHeaderStub{number: uint256.NewInt(1), hash: types.HexToHash("0x1")},
		body:     &txpoolBodyStub{},
		gasLimit: 30_000_000,
	}
	stub := &g49ConstructableChainStub{
		txpoolChainStub: txpoolChainStub{current: current, blocks: map[types.Hash]block.IBlock{}},
		db:              memdb.NewTestPoolDB(t),
	}
	cfg := DefaultTxPoolConfig
	cfg.DynamicSizing = false
	cfg.NoLocals = true

	poolI, err := NewTxsPoolWithConfig(context.Background(), stub, nil, cfg)
	if err != nil {
		t.Fatalf("NewTxsPoolWithConfig: %v", err)
	}
	t.Cleanup(func() { _ = poolI.Stop() })
	pool := poolI.(*TxsPool)

	_, from := generateTestKey()
	dirty := newAccountSet(from)

	done := make(chan struct{})
	// Direct call bypasses scheduleLoop's channel plumbing but exercises the
	// exact same function the loop calls; scheduleLoop itself is idle here
	// (blocked on its select, nothing sent on its channels), so there is no
	// concurrent access to pool state.
	pool.runReorg(done, nil, dirty, map[types.Address]*txsSortedMap{})
	<-done
}

// TestRunReorg_ResetPath drives runReorg with a non-nil reset request over
// two trivial same-block "old == new" transitions, covering the reset-path
// branches (events forwarding, promoteAddrs from pool.queue, demote,
// nonces-from-pending, truncate, snapshot) without needing a real forked
// chain.
func TestRunReorg_ResetPath(t *testing.T) {
	current := &txpoolBlockStub{
		header:   &txpoolHeaderStub{number: uint256.NewInt(1), hash: types.HexToHash("0x1")},
		body:     &txpoolBodyStub{},
		gasLimit: 30_000_000,
	}
	stub := &g49ConstructableChainStub{
		txpoolChainStub: txpoolChainStub{current: current, blocks: map[types.Hash]block.IBlock{}},
		db:              memdb.NewTestPoolDB(t),
	}
	cfg := DefaultTxPoolConfig
	cfg.DynamicSizing = false
	cfg.NoLocals = true

	poolI, err := NewTxsPoolWithConfig(context.Background(), stub, nil, cfg)
	if err != nil {
		t.Fatalf("NewTxsPoolWithConfig: %v", err)
	}
	t.Cleanup(func() { _ = poolI.Stop() })
	pool := poolI.(*TxsPool)

	done := make(chan struct{})
	reset := &txspoolResetRequest{oldBlock: current, newBlock: current}
	pool.runReorg(done, reset, nil, map[types.Address]*txsSortedMap{})
	<-done
}

// TestStateCli_GetAccountsInfo covers the real (non-mock) ReadState
// implementation's batch accessor against a memdb-backed Account table: one
// address with a stored account, one unknown address.
func TestStateCli_GetAccountsInfo(t *testing.T) {
	db := memdb.NewTestDB(t)
	ctx := context.Background()

	known := types.Address{0x01, 0x02}
	unknown := types.Address{0x03, 0x04}

	tx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatalf("BeginRw: %v", err)
	}
	sc := &account.StateAccount{
		Nonce:   7,
		Balance: *uint256.NewInt(12345),
	}
	buf := make([]byte, sc.EncodingLengthForStorage())
	sc.EncodeForStorage(buf)
	if err := tx.Put(modules.Account, known.Bytes(), buf); err != nil {
		tx.Rollback()
		t.Fatalf("Put: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	cli := StateClient(ctx, db)
	infos := cli.GetAccountsInfo([]types.Address{known, unknown})

	got, ok := infos[known]
	if !ok {
		t.Fatal("expected an entry for the known address")
	}
	if got.Nonce != 7 {
		t.Errorf("Nonce = %d, want 7", got.Nonce)
	}
	if got.Balance.Cmp(uint256.NewInt(12345)) != 0 {
		t.Errorf("Balance = %s, want 12345", got.Balance.String())
	}

	gotUnknown, ok := infos[unknown]
	if !ok {
		t.Fatal("expected a zero-value entry for the unknown address")
	}
	if gotUnknown.Nonce != 0 || gotUnknown.Balance.Sign() != 0 {
		t.Errorf("unknown address entry = %+v, want zero value", gotUnknown)
	}
}

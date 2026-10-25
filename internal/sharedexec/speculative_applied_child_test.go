package sharedexec

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

type speculativePanicReadDB struct {
	kv.RwDB
	armed  atomic.Bool
	opened atomic.Int64
	closed atomic.Int64
}

type speculativePanicReadTx struct {
	kv.Tx
	db *speculativePanicReadDB
}

func (db *speculativePanicReadDB) BeginRo(ctx context.Context) (kv.Tx, error) {
	tx, err := db.RwDB.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	db.opened.Add(1)
	return &speculativePanicReadTx{Tx: tx, db: db}, nil
}

func (tx *speculativePanicReadTx) GetOne(table string, key []byte) ([]byte, error) {
	if tx.db.armed.CompareAndSwap(true, false) {
		panic("injected speculative read panic")
	}
	return tx.Tx.GetOne(table, key)
}

func (tx *speculativePanicReadTx) Rollback() {
	tx.db.closed.Add(1)
	tx.Tx.Rollback()
}

// This fixture uses the ordinary owner build and import path: prepared-own-
// build reuse is OFF. The second transaction is funded independently so it
// exercises a real Block-STM/EVM execution on the applied first block.
func speculativeAppliedFixture(t *testing.T) (*internal.BlockChain, *block.Block, *block.Block, *block.Block) {
	t.Helper()
	ctx := context.Background()
	sender, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	childFrom := crypto.PubkeyToAddress(sender.PublicKey)
	var genesis *conf.Genesis
	var keys []blscommon.SecretKey
	chain, _, leader, template, _, _ := unsignedBuildFixtureWithSet(t, 7, func(k []blscommon.SecretKey) { keys = k }, func(g *conf.Genesis) {
		genesis = g
		g.Alloc[childFrom] = conf.GenesisAccount{Balance: "0x1000000000000000000"}
	})
	first, _, err := chain.BuildUnsignedCandidate(ctx, template)
	if err != nil {
		t.Fatal(err)
	}
	sealed := make(chan block.IBlock, 1)
	if err := leader.Seal(chain, first, sealed, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	parent := (<-sealed).(*block.Block)
	if _, err := chain.InsertPerformanceExtension(ctx, parent); err != nil {
		t.Fatal(err)
	}
	if err := chain.DB().View(ctx, func(tx kv.Tx) error {
		n, hash, ok, err := rawdb.ReadQMDBApplied(tx)
		if err != nil {
			return err
		}
		if !ok || n != 1 || hash != parent.Hash() {
			t.Fatalf("first block lacks durable applied marker: %d %x %v", n, hash, ok)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	view := hotstuff.ViewNumber(2)
	childLeader := hotstuff.New(genesis.Config.HotStuff, genesis.Config)
	t.Cleanup(func() { _ = childLeader.Close() })
	// The validator set is available through the public owner verifier.
	public := chain.Engine().(*hotstuff.HotStuff)
	validatorSet := public.Engine().ValidatorSetForView(view)
	index := hotstuff.LeaderForView(view, validatorSet)
	address, err := validatorSet.GetAddress(index)
	if err != nil {
		t.Fatal(err)
	}
	childLeader.Authorize(address, keys[int(index)])
	if err := childLeader.InitEngineFromConfig(); err != nil {
		t.Fatal(err)
	}
	to := types.Address{0x33}
	txn, err := transaction.SignNewTx(sender, transaction.NewLondonSigner(genesis.Config.ChainID), &transaction.LegacyTx{
		Nonce: 0, To: &to, Value: uint256.NewInt(5), Gas: 21000, GasPrice: uint256.NewInt(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	h := &block.Header{ParentHash: parent.Hash(), Number: uint256.NewInt(2), GasLimit: parent.Header().(*block.Header).GasLimit, Difficulty: uint256.NewInt(0)}
	if genesis.Config.IsLondon(2) {
		h.BaseFee, _ = uint256.FromBig(misc.CalcBaseFee(genesis.Config, parent.Header().(*block.Header)))
	}
	if err := childLeader.PrepareUnsignedForView(chain, h, view); err != nil {
		t.Fatal(err)
	}
	h.TxHash = block.TxRootAt([]*transaction.Transaction{txn}, h.Time)
	h.ResetHashCache()
	advisory := block.NewBlock(h, []*transaction.Transaction{txn}).(*block.Block)
	// Simulate the later public CommitQC changing only the child's Extra.
	qc := queueQC(keys, parent.Hash(), 1, 5)
	childLeader.Engine().RestoreState(1, hotstuff.GenesisQC(), *qc, 0)
	actualHeader := block.CopyHeader(h)
	if err := childLeader.PrepareUnsignedForView(chain, actualHeader, view); err != nil {
		t.Fatal(err)
	}
	actualHeader.TxHash = block.TxRootAt([]*transaction.Transaction{txn}, actualHeader.Time)
	actualHeader.ResetHashCache()
	actual := block.NewBlock(actualHeader, []*transaction.Transaction{txn}).(*block.Block)
	if bytes.Equal(advisory.Header().(*block.Header).Extra, actual.Header().(*block.Header).Extra) {
		t.Fatal("fixture did not change HotStuff QC Extra")
	}
	if err := public.ValidateUnsignedCandidateHeader(chain, advisory.Header().(*block.Header)); err != nil {
		t.Fatalf("advisory header invalid: %v", err)
	}
	if err := public.ValidateUnsignedCandidateHeader(chain, actual.Header().(*block.Header)); err != nil {
		t.Fatalf("actual header invalid: %v", err)
	}
	return chain, parent, advisory, actual
}

func TestSpeculativeAppliedChildReusesEVMWithChangedQC(t *testing.T) {
	ctx := context.Background()
	chain, parent, advisory, actual := speculativeAppliedFixture(t)
	binding := types.Hash{0xa1}
	before := chain.PerformanceExecutionAttempts()
	child, err := chain.BeginSpeculativeAppliedChild(ctx, parent.Hash(), 1, binding, advisory)
	if err != nil {
		t.Fatal(err)
	}
	if got := chain.PerformanceExecutionAttempts(); got != before+1 {
		t.Fatalf("advisory did not execute one real EVM attempt: %d -> %d", before, got)
	}
	built, result, _, err := chain.CompleteSpeculativeAppliedChild(ctx, child, binding, actual, false)
	if err != nil || built == nil {
		t.Fatalf("child completion failed: %v", err)
	}
	if chain.PerformanceExecutionAttempts() != before+1 {
		t.Fatal("completion reran EVM")
	}
	normal, normalResult, err := chain.BuildUnsignedCandidate(ctx, actual)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := built.Marshal()
	want, _ := normal.Marshal()
	if result != normalResult || !bytes.Equal(encoded, want) {
		t.Fatal("speculative result differs from ordinary exact candidate build")
	}
	if _, _, _, err := chain.CompleteSpeculativeAppliedChild(ctx, child, binding, actual, false); !errors.Is(err, internal.ErrSpeculativeChildMismatch) {
		t.Fatalf("consumed child reused: %v", err)
	}
}

func TestSpeculativeAppliedChildRejectsChangedInputAndHead(t *testing.T) {
	ctx := context.Background()
	chain, parent, advisory, actual := speculativeAppliedFixture(t)
	binding := types.Hash{0xb2}
	child, err := chain.BeginSpeculativeAppliedChild(ctx, parent.Hash(), 1, binding, advisory)
	if err != nil {
		t.Fatal(err)
	}
	changedHeader := block.CopyHeader(actual.Header().(*block.Header))
	changedHeader.Coinbase = types.Address{0xff}
	changed := block.NewBlock(changedHeader, actual.Transactions()).(*block.Block)
	if _, _, _, err := chain.CompleteSpeculativeAppliedChild(ctx, child, binding, changed, false); !errors.Is(err, internal.ErrSpeculativeChildMismatch) {
		t.Fatalf("changed EVM input accepted: %v", err)
	}
	if _, _, _, err := chain.CompleteSpeculativeAppliedChild(ctx, child, binding, actual, false); !errors.Is(err, internal.ErrSpeculativeChildMismatch) {
		t.Fatalf("mismatched child not consumed: %v", err)
	}
	child, err = chain.BeginSpeculativeAppliedChild(ctx, parent.Hash(), 1, binding, advisory)
	if err != nil {
		t.Fatal(err)
	}
	if err := chain.DB().Update(ctx, func(tx kv.RwTx) error { return rawdb.WriteQMDBApplied(tx, 1, types.Hash{0xee}) }); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := chain.CompleteSpeculativeAppliedChild(ctx, child, binding, actual, false); !errors.Is(err, internal.ErrPerformanceParentNotHead) {
		t.Fatalf("switched applied sibling accepted: %v", err)
	}
	child.Close()
}

func TestSpeculativeAppliedChildRejectsBindingConfigAndClose(t *testing.T) {
	ctx := context.Background()
	chain, parent, advisory, actual := speculativeAppliedFixture(t)
	binding := types.Hash{0xc3}
	child, err := chain.BeginSpeculativeAppliedChild(ctx, parent.Hash(), 1, binding, advisory)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := chain.CompleteSpeculativeAppliedChild(ctx, child, types.Hash{0xc4}, actual, false); !errors.Is(err, internal.ErrSpeculativeChildMismatch) {
		t.Fatalf("wrong lease binding accepted: %v", err)
	}
	child, err = chain.BeginSpeculativeAppliedChild(ctx, parent.Hash(), 1, binding, advisory)
	if err != nil {
		t.Fatal(err)
	}
	chain.Config().HotStuff.DevBlockReward++
	_, _, _, err = chain.CompleteSpeculativeAppliedChild(ctx, child, binding, actual, false)
	chain.Config().HotStuff.DevBlockReward--
	if !errors.Is(err, internal.ErrSpeculativeChildMismatch) {
		t.Fatalf("changed execution config accepted: %v", err)
	}
	child, err = chain.BeginSpeculativeAppliedChild(ctx, parent.Hash(), 1, binding, advisory)
	if err != nil {
		t.Fatal(err)
	}
	child.Close()
	child.Close()
	if _, _, _, err := chain.CompleteSpeculativeAppliedChild(ctx, child, binding, actual, false); !errors.Is(err, internal.ErrSpeculativeChildMismatch) {
		t.Fatalf("closed child completed: %v", err)
	}
}

func TestSpeculativeAppliedChildClosesSnapshotOnPanicBeforeTransfer(t *testing.T) {
	chain, parent, advisory, _ := speculativeAppliedFixture(t)
	db := &speculativePanicReadDB{RwDB: chain.ChainDB}
	chain.ChainDB = db
	db.armed.Store(true)
	defer func() {
		if recover() == nil {
			t.Fatal("expected injected read panic")
		}
		if db.opened.Load() != 1 || db.closed.Load() != 1 {
			t.Fatalf("speculative RO lease leaked after panic: opened=%d closed=%d", db.opened.Load(), db.closed.Load())
		}
	}()
	_, _ = chain.BeginSpeculativeAppliedChild(context.Background(), parent.Hash(), 1, types.Hash{0x44}, advisory)
}

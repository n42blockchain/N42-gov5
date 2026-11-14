// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

// ruleObservation is what a state-root computation saw: the block, whether
// the unchanged-account rule was on, the accounts the root wrote and the root.
type ruleObservation struct {
	number   uint64
	skip     bool
	accounts map[types.Address]*account.StateAccount
	root     types.Hash
}

// ruleEngine is the fixture engine with a positional QMDB root: like an APoS
// engine it computes the root in Finalize (import) and FinalizeAndAssemble
// (production) from the IntraBlockState the chain or the miner prepared, so
// the unchanged-account rule those paths set is the one in effect. Each root
// is over a fresh QMDB tree: two roots agree exactly when the two paths wrote
// the same leaves. Import compares its root with the sealed header's, so a
// mined block that would not validate fails InsertChain.
type ruleEngine struct {
	mnTFakerEngine
	// legacy computes every root with the rule off, as the code did before
	// the APoS paths applied it.
	legacy   bool
	mined    []ruleObservation
	imported []ruleObservation
}

func (e *ruleEngine) root(header *block.Header, ibs *state.IntraBlockState) ruleObservation {
	if e.legacy {
		ibs.SetSkipUnchangedAccounts(false)
	}
	obs := ruleObservation{number: header.Number.Uint64(), skip: ibs.SkipUnchangedAccounts()}
	ibs.SetRootComputer(commitment.NewQMDBRootComputer())
	obs.root = ibs.IntermediateRoot()
	obs.accounts, _ = ibs.LastRootDirtySet()
	return obs
}

func (e *ruleEngine) Finalize(_ consensus.ChainHeaderReader, header block.IHeader, ibs *state.IntraBlockState, _ []*transaction.Transaction, _ []block.IHeader) ([]*block.Reward, map[types.Address]*uint256.Int, error) {
	h, ok := header.(*block.Header)
	if !ok {
		return nil, nil, errMnTNotAHeader
	}
	obs := e.root(h, ibs)
	e.imported = append(e.imported, obs)
	if obs.root != h.Root {
		return nil, nil, errRuleRootMismatch
	}
	return nil, nil, nil
}

func (e *ruleEngine) FinalizeAndAssemble(chain consensus.ChainHeaderReader, header block.IHeader, ibs *state.IntraBlockState, txs []*transaction.Transaction, uncles []block.IHeader, receipts []*block.Receipt) (block.IBlock, []*block.Reward, map[types.Address]*uint256.Int, error) {
	h, ok := header.(*block.Header)
	if !ok {
		return nil, nil, nil, errMnTNotAHeader
	}
	obs := e.root(h, ibs)
	e.mined = append(e.mined, obs)
	blk, rewards, nopay, err := e.mnTFakerEngine.FinalizeAndAssemble(chain, header, ibs, txs, uncles, receipts)
	if err != nil {
		return nil, nil, nil, err
	}
	// The fixture engine recomputes the root without a computer; seal ours.
	sealed := blk.Header().(*block.Header)
	sealed.Root = obs.root
	return block.NewBlockFromReceipt(sealed, txs, nil, receipts, nil), rewards, nopay, nil
}

type ruleError string

func (e ruleError) Error() string { return string(e) }

const errRuleRootMismatch = ruleError("import root differs from the mined block's root")

// ruleBlockTimes are the three blocks' timestamps; with the rule configured it
// activates at the second (the boundary block) and holds for the third.
var ruleBlockTimes = []uint64{1_000, 2_000, 3_000}

const ruleActivation = 2_000

// mineAndImportRuleBlocks mines three blocks through commitWork, each holding
// one zero-tip, zero-value transfer: the funded coinbase is credited zero, the
// touched-but-unchanged case the rule is about. It imports each through
// InsertChain and returns the engine's observations.
func mineAndImportRuleBlocks(t *testing.T, activation *big.Int, legacy bool) (*ruleEngine, *mnTChainFixture) {
	t.Helper()
	engine := &ruleEngine{legacy: legacy}
	f := mnTNewChainFixtureWithEngine(t, engine, func(cfg *params.ChainConfig, alloc conf.GenesisAlloc) {
		cfg.UnchangedAccountNoOpTime = activation
		// A funded coinbase: a zero tip leaves it as it was.
		alloc[mnTFixtureCoinbase] = conf.GenesisAccount{Balance: "0x1000"}
	})
	for i, at := range ruleBlockTimes {
		zero := f.ruleZeroTipTransfer(t, 0, f.Senders[2+i%2])
		pool := &mnTStubTxsPool{pending: mnTByAddr(f.Senders[0], zero)}
		w := mnTBareWorker(t, f, pool)
		w.coinbase = f.Coinbase
		w.resubmitAdjustCh = make(chan *intervalAdjust, 10)
		atomic.StoreInt32(&w.running, 1)
		require.NoError(t, w.commitWork(nil, false, int64(at), types.Hash{}, false, time.Time{}))
		var tk *task
		select {
		case tk = <-w.taskCh:
		default:
			t.Fatal("commitWork did not send a task")
		}
		require.Len(t, tk.receipts, 1, "block %d includes the transfer", i+1)
		require.Equal(t, at, tk.block.Time(), "block %d time", i+1)
		n, err := f.Chain.InsertChain([]block.IBlock{tk.block})
		require.NoError(t, err, "block %d mined by the miner imports", i+1)
		require.Equal(t, 1, n)
	}
	require.Len(t, engine.mined, len(ruleBlockTimes))
	require.Len(t, engine.imported, len(ruleBlockTimes))
	return engine, f
}

// ruleZeroTipTransfer is a zero-value transfer paying no tip: the coinbase is
// credited zero and the recipient receives zero.
func (f *mnTChainFixture) ruleZeroTipTransfer(tb testing.TB, senderIdx int, to types.Address) *transaction.Transaction {
	tb.Helper()
	from := f.Senders[senderIdx]
	nonce := f.nonces[from]
	f.nonces[from] = nonce + 1
	inner := &transaction.DynamicFeeTx{
		ChainID:   uint256.MustFromBig(f.Config.ChainID),
		Nonce:     nonce,
		GasTipCap: uint256.NewInt(0),
		GasFeeCap: uint256.NewInt(1_000_000),
		Gas:       21_000,
		To:        &to,
		Value:     uint256.NewInt(0),
	}
	signed, err := transaction.SignTx(transaction.NewTx(inner), f.Signer, f.SenderKeys[senderIdx])
	require.NoError(tb, err)
	return signed
}

// TestUnchangedAccountRuleUnsetLeavesAPoSRootsUnchanged: with
// UnchangedAccountNoOpTime unset (every shipped config), block production and
// import keep the rule off, and every root is the one the code computed
// before the APoS paths applied the rule (the flag at its default).
func TestUnchangedAccountRuleUnsetLeavesAPoSRootsUnchanged(t *testing.T) {
	current, _ := mineAndImportRuleBlocks(t, nil, false)
	legacy, _ := mineAndImportRuleBlocks(t, nil, true)
	for i := range ruleBlockTimes {
		for _, obs := range []ruleObservation{current.mined[i], current.imported[i]} {
			require.False(t, obs.skip, "block %d: the rule is off without an activation time", obs.number)
		}
		require.Equal(t, current.mined[i].root, current.imported[i].root, "block %d: mined and imported roots", i+1)
		require.Equal(t, len(legacy.mined[i].accounts), len(current.mined[i].accounts), "block %d: leaves written", i+1)
		require.Equal(t, len(legacy.imported[i].accounts), len(current.imported[i].accounts), "block %d: leaves written", i+1)
		// The old rule: the coinbase, credited a zero tip, is written.
		for _, obs := range []ruleObservation{current.mined[i], current.imported[i], legacy.mined[i], legacy.imported[i]} {
			_, wrote := obs.accounts[mnTFixtureCoinbase]
			require.True(t, wrote, "block %d: the zero-credited coinbase is written without the rule", obs.number)
		}
	}
}

// TestUnchangedAccountRuleAppliesOnAPoSProductionAndImport: with the time set,
// production and import take the rule from the activation block on and not
// before it, agree on every root (each mined block imports), leave the
// touched but unchanged coinbase out once active, and compute the root the
// HotStuff Finalize and replay paths compute: the same helper over the same
// executed state, which is also the root of the block's state change alone.
func TestUnchangedAccountRuleAppliesOnAPoSProductionAndImport(t *testing.T) {
	engine, f := mineAndImportRuleBlocks(t, big.NewInt(ruleActivation), false)
	for i, at := range ruleBlockTimes {
		active := at >= ruleActivation
		mined, imported := engine.mined[i], engine.imported[i]
		require.Equal(t, active, mined.skip, "block %d (time %d): rule on production", i+1, at)
		require.Equal(t, active, imported.skip, "block %d (time %d): rule on import", i+1, at)
		require.Equal(t, mined.root, imported.root, "block %d: mined and imported roots", i+1)

		// HotStuff's Finalize and replay decide the flag with the same helper
		// from the chain config and the block's time.
		probe := state.New(nil)
		probe.ApplyUnchangedAccountRule(f.Config, at)
		require.Equal(t, active, probe.SkipUnchangedAccounts(), "block %d: HotStuff/replay decision", i+1)

		_, minedWrote := mined.accounts[mnTFixtureCoinbase]
		_, importWrote := imported.accounts[mnTFixtureCoinbase]
		require.Equal(t, minedWrote, importWrote, "block %d: the coinbase's leaf", i+1)
		// Before the activation block the old rule holds: the coinbase whose
		// first access was a zero credit is written; from it on, it is not.
		require.Equal(t, !active, minedWrote, "block %d (time %d): the unchanged coinbase's leaf", i+1, at)
		// The sender (nonce and fee) is written on every block either way.
		_, senderWrote := mined.accounts[f.Senders[0]]
		require.True(t, senderWrote, "block %d: the sender's leaf", i+1)
	}
}

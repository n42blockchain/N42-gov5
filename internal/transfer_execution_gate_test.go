package internal

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	vm "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
)

type transferProcessorEngine struct {
	transferTestEngine
	finalized int
}

func (e *transferProcessorEngine) Finalize(_ consensus.ChainHeaderReader, _ block.IHeader, s *state.IntraBlockState, _ []*transaction.Transaction, _ []block.IHeader) ([]*block.Reward, map[types.Address]*uint256.Int, error) {
	e.finalized++
	s.AddBalance(e.author, uint256.NewInt(19))
	return nil, nil, nil
}

func TestTransferProcessorPath(t *testing.T) {
	cfg, header, txs, reader, _ := transferFixtureN(t, 9, 2048)
	// This test isolates import execution and engine finalization. Full fork
	// system-call/root validation remains a separate integration requirement.
	cfg.PragueTime, cfg.OsakaTime = nil, nil
	b := block.NewBlock(header, txs).(*block.Block)
	var receipts []block.Receipts
	var writes []*transferCommitCapture
	for _, workers := range []int{0, 2} {
		_, tx := memdb.NewTestTx(t)
		s := state.New(state.NewPlainStateReader(tx))
		for a, data := range reader.accounts {
			s.SetBalance(a, &data.Balance)
		}
		require.NoError(t, s.FinalizeTx(cfg.RulesWithTimestamp(1, 1), state.NewNoopWriter()))
		engine := &transferProcessorEngine{transferTestEngine: transferTestEngine{author: types.Address{18: 3}}}
		p := NewStateProcessor(cfg, &BlockChain{}, engine)
		p.transferWorkers = workers
		r, _, logs, gas, err := p.Process(b, s, s.GetStateReader(), nil, func(uint64) types.Hash { return types.Hash{} })
		require.NoError(t, err)
		require.Equal(t, workers > 0, p.LastPhases().TransferComponents)
		require.Equal(t, header.GasUsed, gas)
		require.Empty(t, logs)
		require.Equal(t, 1, engine.finalized)
		require.Equal(t, len(txs)-1, s.TxIndex())
		capture := newTransferCommitCapture()
		require.NoError(t, s.CommitBlock(cfg.RulesWithTimestamp(1, 1), capture))
		receipts = append(receipts, r)
		writes = append(writes, capture)
	}
	require.Equal(t, receipts[0], receipts[1])
	require.Equal(t, writes[0], writes[1])
}

func TestTransferProcessorRejectsForgedSender(t *testing.T) {
	cfg, h, txs, _, _ := transferFixtureN(t, 9, 2048)
	txs[0].SetFrom(types.Address{18: 9})
	_, tx := memdb.NewTestTx(t)
	s := state.New(state.NewPlainStateReader(tx))
	engine := &transferProcessorEngine{}
	p := NewStateProcessor(cfg, &BlockChain{}, engine)
	p.transferWorkers = 2
	_, _, _, _, err := p.Process(block.NewBlock(h, txs).(*block.Block), s, s.GetStateReader(), nil, func(uint64) types.Hash { return types.Hash{} })
	require.Error(t, err)
	require.Empty(t, s.DirtyAddresses())
	require.Zero(t, engine.finalized)
}

func TestTransferExecutionGateRejectsObservers(t *testing.T) {
	for _, mode := range []string{"unknown-reader", "balance-hook", "snapshot", "pending-tx", "vm-config"} {
		t.Run(mode, func(t *testing.T) {
			cfg, h, txs, reader, keys := transferFixture(t, 9)
			_, tx := memdb.NewTestTx(t)
			s := state.New(state.NewPlainStateReader(tx))
			var vmCfg vm.Config
			switch mode {
			case "unknown-reader":
				s.SetStateReader(reader)
			case "balance-hook":
				s.SetBalanceReadHook(func(types.Address) { t.Error("observer was invoked") })
			case "snapshot":
				s.BeginWriteSnapshot()
			case "pending-tx":
				s.SetNonce(keys[0], 1)
			case "vm-config":
				vmCfg.NoBaseFee = true
			}
			r, installed, err := tryTransferExecution(cfg, transferTestEngine{}, h, types.Hash{}, txs, s, vmCfg, 2)
			require.NoError(t, err)
			require.False(t, installed)
			require.Nil(t, r)
		})
	}
}

func TestParseTransferExecutionWorkers(t *testing.T) {
	for input, want := range map[string]int{"": 0, "off": 0, "1": 0, "2": 2, "32": 32, "33": 0, "-1": 0} {
		require.Equal(t, want, parseTransferExecutionWorkers(input))
	}
}

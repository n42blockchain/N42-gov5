package internal

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

// Uses the actual QMDB computer, with a test reward policy. This does not model
// HotStuff voting, reward distribution, crash recovery or canonical fork choice.
type transferQMDBEngine struct {
	transferTestEngine
	root types.Hash
}

func (e *transferQMDBEngine) Finalize(_ consensus.ChainHeaderReader, _ block.IHeader, s *state.IntraBlockState, _ []*transaction.Transaction, _ []block.IHeader) ([]*block.Reward, map[types.Address]*uint256.Int, error) {
	s.AddBalance(e.author, uint256.NewInt(19))
	e.root = s.IntermediateRoot()
	return nil, nil, s.Error()
}

type transferDiskRow struct{ key, value []byte }

func transferReadTable(t *testing.T, tx kv.Tx, name string) []transferDiskRow {
	t.Helper()
	cursor, err := tx.Cursor(name)
	require.NoError(t, err)
	defer cursor.Close()
	var rows []transferDiskRow
	for k, v, err := cursor.First(); ; k, v, err = cursor.Next() {
		require.NoError(t, err)
		if k == nil {
			break
		}
		rows = append(rows, transferDiskRow{append([]byte(nil), k...), append([]byte(nil), v...)})
	}
	return rows
}

func TestTransferProcessorQMDBPersistence(t *testing.T) {
	cfg, header, txs, reader, _ := transferFixtureN(t, 9, 2048)
	cfg.PragueTime, cfg.OsakaTime = nil, nil
	beaconRoot := types.Hash{31: 0x42}
	header.ParentBeaconRoot = &beaconRoot
	beaconAccount := account.NewAccount()
	beaconAccount.Initialised, beaconAccount.Nonce = true, 1
	reader.accounts[params.BeaconRootsAddress] = &beaconAccount
	blk := block.NewBlock(header, txs).(*block.Block)
	var roots []types.Hash
	var disk []map[string][]transferDiskRow
	var receipts []block.Receipts
	for _, workers := range []int{0, 2} {
		db := newRealignTestDB(t)
		rc := commitment.NewQMDBRootComputer()
		// Seed a shared positional QMDB history before executing the block.
		require.NoError(t, db.Update(t.Context(), func(tx kv.RwTx) error {
			rc.SetCold(tx)
			defer rc.SetCold(nil)
			for a, data := range reader.accounts {
				if err := tx.Put(modules.Account, a[:], data.MarshalV2()); err != nil {
					return err
				}
			}
			if _, err := rc.ComputeRoot(reader.accounts, nil); err != nil {
				return err
			}
			_, err := rc.FlushTo(tx)
			return err
		}))
		rc.CommitFlushed()
		engine := &transferQMDBEngine{transferTestEngine: transferTestEngine{author: types.Address{18: 3}}}
		require.NoError(t, db.Update(t.Context(), func(tx kv.RwTx) error {
			rc.SetCold(tx)
			defer rc.SetCold(nil)
			s := state.New(state.NewPlainStateReader(tx))
			s.SetRootComputer(rc)
			writer := state.NewPlainStateWriter(tx, tx, 1)
			p := NewStateProcessor(cfg, &BlockChain{}, engine)
			p.transferWorkers = workers
			r, _, _, gas, err := p.Process(blk, s, s.GetStateReader(), writer, func(uint64) types.Hash { return types.Hash{} })
			if err != nil {
				return err
			}
			require.Equal(t, workers > 0, p.LastPhases().TransferComponents)
			require.Equal(t, header.GasUsed, gas)
			receipts = append(receipts, r)
			if err := s.CommitBlock(cfg.RulesWithTimestamp(1, 1), writer); err != nil {
				return err
			}
			if err := writer.WriteChangeSets(); err != nil {
				return err
			}
			if err := writer.WriteHistory(); err != nil {
				return err
			}
			_, err = rc.FlushTo(tx)
			return err
		}))
		rc.CommitFlushed()
		require.NotEqual(t, types.Hash{}, engine.root)
		require.Equal(t, engine.root, rc.Root())
		roots = append(roots, engine.root)
		// Rebuild the tree from committed rows, not the in-memory executor.
		fresh := commitment.NewQMDBRootComputer()
		tables := make(map[string][]transferDiskRow)
		require.NoError(t, db.View(t.Context(), func(tx kv.Tx) error {
			fresh.SetCold(tx)
			defer fresh.SetCold(nil)
			if err := fresh.LoadFrom(tx); err != nil {
				return err
			}
			for _, name := range []string{modules.Account, modules.Storage, modules.AccountChangeSet, modules.StorageChangeSet, modules.AccountsHistory, modules.StorageHistory} {
				tables[name] = transferReadTable(t, tx, name)
			}
			return nil
		}))
		require.Equal(t, engine.root, fresh.Root())
		require.Len(t, tables[modules.Storage], 2, "beacon-root prefix was not persisted")
		require.NotEmpty(t, tables[modules.AccountChangeSet])
		require.NotEmpty(t, tables[modules.StorageChangeSet])
		require.NotEmpty(t, tables[modules.AccountsHistory])
		require.NotEmpty(t, tables[modules.StorageHistory])
		disk = append(disk, tables)
	}
	require.Equal(t, receipts[0], receipts[1])
	require.Equal(t, roots[0], roots[1])
	require.Equal(t, disk[0], disk[1])
}

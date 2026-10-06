package state

import (
	"context"
	"errors"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/params"
)

// MDBX can return (nil, nil, error) on either Seek or Next. A nil key only
// denotes successful exhaustion when the error is also nil.
type storageScanFailureTx struct {
	kv.RwTx
	stage  string
	fault  error
	closed int
}

// These tests exercise capture during execution, before root computation.
type storageScanRootComputer struct{}

func (storageScanRootComputer) ComputeRoot(map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) (types.Hash, error) {
	return types.Hash{}, errors.New("unexpected root computation in storage scan test")
}

func TestStorageScanFailureReachesStateError(t *testing.T) {
	for _, buffered := range []bool{false, true} {
		for _, operation := range []string{"collision", "selfdestruct"} {
			for _, stage := range []string{"seek", "next"} {
				if operation == "collision" && stage == "next" {
					// Collision detection may legitimately stop after the first nonzero slot.
					continue
				}
				readerName := "plain"
				if buffered {
					readerName = "buffered"
				}
				t.Run(readerName+"/"+operation+"/"+stage, func(t *testing.T) {
					db := memdb.NewTestDB(t)
					tx, err := db.BeginRw(context.Background())
					require.NoError(t, err)
					defer tx.Rollback()
					address, slot := types.Address{19: 0x91}, types.Hash{31: 42}
					acc := account.NewAccount()
					acc.Balance.SetUint64(1)
					require.NoError(t, tx.Put(modules.Account, address[:], acc.MarshalV2()))
					require.NoError(t, tx.Put(modules.Storage,
						modules.PlainGenerateCompositeStorageKey(address[:], slot[:]), []byte{7}))
					fault := errors.New("storage scan failed during execution")
					failing := &storageScanFailureTx{RwTx: tx, stage: stage, fault: fault}
					var reader StateReader = NewPlainStateReader(failing)
					if buffered {
						reader = NewBufferedPlainStateReader(NewPlainStateBuffer(), failing)
					}
					ibs := New(reader)
					ibs.SetRootComputer(storageScanRootComputer{})
					if operation == "collision" {
						ibs.HasNonEmptyStorage(address)
					} else {
						ibs.Selfdestruct(address)
					}
					require.ErrorIs(t, ibs.Error(), fault, "storage failure must invalidate block execution")
					require.Empty(t, ibs.wipedStorageSlots, "failed scan published partial capture")
					require.ErrorIs(t, ibs.FinalizeTx(&params.Rules{}, NewNoopWriter()), fault)
					require.ErrorIs(t, ibs.CommitBlock(&params.Rules{}, NewPlainStateWriter(tx, tx, 1)), fault)
				})
			}
		}
	}
}

func (tx *storageScanFailureTx) storageCursor(table string) (kv.RwCursor, error) {
	if table == modules.Storage && tx.stage == "open" {
		return nil, tx.fault
	}
	cursor, err := tx.RwTx.RwCursor(table)
	if err != nil || table != modules.Storage {
		return cursor, err
	}
	return &storageScanFailureCursor{RwCursor: cursor, owner: tx}, nil
}

func (tx *storageScanFailureTx) Cursor(table string) (kv.Cursor, error) {
	return tx.storageCursor(table)
}

func (tx *storageScanFailureTx) RwCursor(table string) (kv.RwCursor, error) {
	return tx.storageCursor(table)
}

type storageScanFailureCursor struct {
	kv.RwCursor
	owner *storageScanFailureTx
}

func (cursor *storageScanFailureCursor) Seek(key []byte) ([]byte, []byte, error) {
	if cursor.owner.stage == "seek" {
		return nil, nil, cursor.owner.fault
	}
	return cursor.RwCursor.Seek(key)
}

func (cursor *storageScanFailureCursor) Next() ([]byte, []byte, error) {
	if cursor.owner.stage == "next" {
		return nil, nil, cursor.owner.fault
	}
	return cursor.RwCursor.Next()
}

func (cursor *storageScanFailureCursor) Close() {
	cursor.owner.closed++
	cursor.RwCursor.Close()
}

func TestStorageScanFailureAbortsTransaction(t *testing.T) {
	var address, marker types.Address
	address[19], marker[19] = 0x81, 0x82
	slot := types.Hash{31: 1}
	key := modules.PlainGenerateCompositeStorageKey(address[:], slot[:])
	operations := []struct {
		name string
		run  func(*testing.T, kv.RwTx) error
	}{
		{"plain-reader", func(t *testing.T, tx kv.RwTx) error {
			return NewPlainStateReader(tx).ForEachStorage(address, func(types.Hash, []byte) bool { return true })
		}},
		{"buffered-reader", func(t *testing.T, tx kv.RwTx) error {
			return NewBufferedPlainStateReader(NewPlainStateBuffer(), tx).ForEachStorage(address, func(types.Hash, []byte) bool { return true })
		}},
		{"plain-writer-history", func(t *testing.T, tx kv.RwTx) error {
			writer := NewPlainStateWriter(tx, tx, 1)
			err := writer.CreateContract(address)
			require.Empty(t, writer.ChangeSetWriter().storageChanges, "failed scan must not publish partial undo")
			return err
		}},
		{"plain-writer-wipe", func(t *testing.T, tx kv.RwTx) error {
			return NewPlainStateWriterNoHistory(tx).CreateContract(address)
		}},
		{"buffered-writer-history", func(t *testing.T, tx kv.RwTx) error {
			buffer := NewPlainStateBuffer()
			writer := NewBufferedPlainStateWriter(buffer, tx, 1)
			err := writer.CreateContract(address)
			require.Empty(t, buffer.ContractWipes(), "failed scan must not publish a wipe")
			require.Empty(t, writer.ChangeSetWriter().storageChanges, "failed scan must not publish partial undo")
			return err
		}},
		{"snapshot-flush-wipe", func(t *testing.T, tx kv.RwTx) error {
			snapshot := &BufferSnapshot{
				accounts:      map[types.Address][]byte{marker: {2}},
				contractWipes: []types.Address{address},
			}
			return snapshot.ApplyTo(tx)
		}},
	}
	for _, operation := range operations {
		for _, stage := range []string{"open", "seek", "next"} {
			t.Run(operation.name+"/"+stage, func(t *testing.T) {
				db := memdb.NewTestDB(t)
				ctx := context.Background()
				require.NoError(t, db.Update(ctx, func(tx kv.RwTx) error {
					return tx.Put(modules.Storage, key, []byte{7})
				}))
				fault := errors.New("storage scan I/O failure")
				var failing *storageScanFailureTx
				err := db.Update(ctx, func(tx kv.RwTx) error {
					// This preceding write must also roll back when enumeration fails.
					if err := tx.Put(modules.Account, marker[:], []byte{1}); err != nil {
						return err
					}
					failing = &storageScanFailureTx{RwTx: tx, stage: stage, fault: fault}
					return operation.run(t, failing)
				})
				require.ErrorIs(t, err, fault)
				expectedClosed := 1
				if stage == "open" {
					expectedClosed = 0
				}
				require.Equal(t, expectedClosed, failing.closed)
				require.NoError(t, db.View(ctx, func(tx kv.Tx) error {
					value, err := tx.GetOne(modules.Storage, key)
					require.NoError(t, err)
					require.Equal(t, []byte{7}, value, "failed wipe changed persistent storage")
					value, err = tx.GetOne(modules.Account, marker[:])
					require.NoError(t, err)
					require.Empty(t, value, "scan error committed an earlier account write")
					return nil
				}))
			})
		}
	}
}

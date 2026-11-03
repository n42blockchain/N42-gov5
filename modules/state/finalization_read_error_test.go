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

type finalizationFailureReader struct {
	StateReader
	address types.Address
	fault   error
}

func (reader finalizationFailureReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	if address == reader.address {
		return nil, reader.fault
	}
	return reader.StateReader.ReadAccountData(address)
}

func TestStateReadErrorAbortsFinalization(t *testing.T) {
	for _, boundary := range []string{"transaction", "block", "write-set"} {
		for _, timing := range []string{"execution", "deferred-credit"} {
			if boundary == "write-set" && timing == "deferred-credit" {
				continue // MakeWriteSet is the post-materialization boundary.
			}
			t.Run(boundary+"/"+timing, func(t *testing.T) {
				db := memdb.NewTestDB(t)
				good, bad := types.Address{19: 1}, types.Address{19: 2}
				fault := errors.New("account read I/O failure")
				err := db.Update(context.Background(), func(tx kv.RwTx) error {
					reader := finalizationFailureReader{StateReader: NewPlainStateReader(tx), address: bad, fault: fault}
					ibs := New(reader)
					ibs.SetNonce(good, 1)
					if timing == "execution" {
						ibs.GetBalance(bad)
						require.ErrorIs(t, ibs.Error(), fault)
					} else {
						ibs.AddBalance(bad, uint256.NewInt(1))
						require.NoError(t, ibs.Error(), "credit must remain deferred until finalization")
					}
					epoch := ibs.storageEpoch
					writer := NewPlainStateWriter(tx, tx, 1)
					var err error
					switch boundary {
					case "transaction":
						err = ibs.FinalizeTx(&params.Rules{}, writer)
					case "block":
						err = ibs.CommitBlock(&params.Rules{}, writer)
					case "write-set":
						err = ibs.MakeWriteSet(&params.Rules{}, writer)
					}
					require.ErrorIs(t, err, fault)
					require.Equal(t, epoch, ibs.storageEpoch, "failed finalization advanced transaction state")
					require.NotEmpty(t, ibs.journal.dirties, "failed finalization consumed pending changes")
					value, readErr := tx.GetOne(modules.Account, good[:])
					require.NoError(t, readErr)
					require.Empty(t, value, "read failure emitted writes before returning")
					return err
				})
				require.ErrorIs(t, err, fault)
				require.NoError(t, db.View(context.Background(), func(tx kv.Tx) error {
					for _, address := range []types.Address{good, bad} {
						value, err := tx.GetOne(modules.Account, address[:])
						require.NoError(t, err)
						require.Empty(t, value, "failed finalization changed persistent accounts")
					}
					return nil
				}))
			})
		}
	}
}

package state

import (
	"errors"
	"sync"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// This reader deliberately returns its own pointers. Capture must not retain
// them or let worker mutations escape into another worker's input.
type snapshotSource struct {
	StateReader
	accounts map[types.Address]*account.StateAccount
	err      error
}

func (r snapshotSource) ReadAccountData(a types.Address) (*account.StateAccount, error) {
	return r.accounts[a], r.err
}

func TestAccountSnapshotCurrentViewAndIsolation(t *testing.T) {
	a, absent, deleted := types.Address{19: 1}, types.Address{19: 2}, types.Address{19: 3}
	initial := account.NewAccount()
	initial.Initialised = true
	initial.Balance.SetUint64(7)
	s := New(snapshotSource{accounts: map[types.Address]*account.StateAccount{a: &initial}})
	s.SetBalance(a, uint256.NewInt(11))
	s.SetNonce(a, 5)
	s.SetNonce(deleted, 0)
	require.NoError(t, s.FinalizeTx(&params.Rules{IsSpuriousDragon: true}, NewNoopWriter()))
	snap, err := s.SnapshotAccounts([]types.Address{a, absent, deleted, a})
	require.NoError(t, err)
	s.SetBalance(a, uint256.NewInt(99))
	s.SetNonce(a, 6)
	initial.Balance.SetUint64(123)
	got, err := snap.ReadAccountData(a)
	require.NoError(t, err)
	require.Equal(t, uint64(11), got.Balance.Uint64())
	require.Equal(t, uint64(5), got.Nonce)
	got.Balance.SetUint64(456)
	got.CodeHash[0] ^= 1
	again, err := snap.ReadAccountData(a)
	require.NoError(t, err)
	require.Equal(t, uint64(11), again.Balance.Uint64())
	require.NotEqual(t, got.CodeHash, again.CodeHash)
	for _, addr := range []types.Address{absent, deleted} {
		got, err := snap.ReadAccountData(addr)
		require.NoError(t, err)
		require.Nil(t, got)
	}
	_, err = snap.ReadAccountData(types.Address{19: 4})
	require.Error(t, err)
	_, err = snap.ReadAccountStorage(a, &types.Hash{})
	require.Error(t, err)
	_, err = snap.ReadAccountCode(a, types.Hash{})
	require.Error(t, err)
	_, err = snap.ReadAccountCodeSize(a, types.Hash{})
	require.Error(t, err)
}

func TestAccountSnapshotDeferredCreditAndRevert(t *testing.T) {
	for _, exists := range []bool{false, true} {
		a := types.Address{19: 1}
		accounts := map[types.Address]*account.StateAccount{}
		if exists {
			initial := account.NewAccount()
			initial.Initialised = true
			initial.Balance.SetUint64(7)
			accounts[a] = &initial
		}
		s := New(snapshotSource{accounts: accounts})
		rev := s.Snapshot()
		s.AddBalance(a, uint256.NewInt(3))
		snap, err := s.SnapshotAccounts([]types.Address{a})
		require.NoError(t, err)
		got, err := snap.ReadAccountData(a)
		require.NoError(t, err)
		want := uint64(3)
		if exists {
			want += 7
		}
		require.Equal(t, want, got.Balance.Uint64())
		s.RevertToSnapshot(rev)
		require.Equal(t, want-3, s.GetBalance(a).Uint64())
		require.Equal(t, exists, s.Exist(a))
		got, err = snap.ReadAccountData(a)
		require.NoError(t, err)
		require.Equal(t, want, got.Balance.Uint64())
	}
}

func TestAccountSnapshotReadFailure(t *testing.T) {
	fault := errors.New("snapshot read failed")
	s := New(snapshotSource{err: fault})
	s.SetNonce(types.Address{19: 1}, 5)
	// The failed setter leaves a sticky reader error. Even an empty capture
	// must reject it rather than making an apparently usable snapshot.
	snap, err := s.SnapshotAccounts(nil)
	require.ErrorIs(t, err, fault)
	require.Nil(t, snap)
	s = New(snapshotSource{err: fault})
	snap, err = s.SnapshotAccounts([]types.Address{{19: 1}})
	require.ErrorIs(t, err, fault)
	require.ErrorIs(t, s.Error(), fault)
	require.Nil(t, snap)
}

func TestAccountSnapshotConcurrentReads(t *testing.T) {
	a := types.Address{19: 1}
	s := New(snapshotSource{})
	s.SetBalance(a, uint256.NewInt(7))
	snap, err := s.SnapshotAccounts([]types.Address{a})
	require.NoError(t, err)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				got, err := snap.ReadAccountData(a)
				if err != nil || got == nil || got.Balance.Uint64() != 7 {
					t.Errorf("snapshot changed: %v, %v", got, err)
					return
				}
				got.Balance.SetUint64(99)
			}
		}()
	}
	// The live state is no longer shared with the captured reader.
	for j := 0; j < 100; j++ {
		s.SetBalance(a, uint256.NewInt(uint64(j)))
	}
	wg.Wait()
}

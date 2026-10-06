package state

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func TestMergeEOAAccountsRejectsBeforeWrites(t *testing.T) {
	for _, mode := range []string{"stale", "pending-tx", "revision", "missing-input", "root-change", "code-change", "empty-live", "nonce-regression", "aura"} {
		t.Run(mode, func(t *testing.T) {
			a, b := types.Address{19: 1}, types.Address{19: 2}
			s := New(snapshotSource{})
			s.SetBalance(a, uint256.NewInt(11))
			s.SetNonce(a, 1)
			rules := &params.Rules{IsSpuriousDragon: true}
			require.NoError(t, s.FinalizeTx(rules, NewNoopWriter()))
			base, err := s.SnapshotAccounts([]types.Address{a, b})
			require.NoError(t, err)
			after, err := base.ReadAccountData(a)
			require.NoError(t, err)
			after.Balance.SetUint64(22)
			changes := map[types.Address]*account.StateAccount{a: after, b: nil}
			switch mode {
			case "aura":
				rules.IsAura = true
			case "stale":
				s.SetBalance(b, uint256.NewInt(3)) // Not only changed output a.
				require.NoError(t, s.FinalizeTx(rules, NewNoopWriter()))
			case "pending-tx":
				s.SetNonce(b, 1)
			case "revision":
				s.Snapshot()
			case "missing-input":
				changes[types.Address{19: 3}] = nil
			case "root-change":
				after.Root[0] ^= 1
			case "code-change":
				after.CodeHash[0] ^= 1
			case "empty-live":
				after.Nonce = 0
				after.Balance.Clear()
			case "nonce-regression":
				after.Nonce = 0
			}
			beforeBalance, beforeNonce := *s.GetBalance(b), s.GetNonce(b)
			merged, err := s.MergeEOAAccounts(base, changes, rules)
			require.NoError(t, err)
			require.False(t, merged)
			require.Equal(t, uint64(11), s.GetBalance(a).Uint64())
			require.Equal(t, beforeBalance, *s.GetBalance(b))
			require.Equal(t, beforeNonce, s.GetNonce(b))
		})
	}
}

func TestMergeEOAAccountsKeepsUnchangedTouches(t *testing.T) {
	a, empty := types.Address{19: 1}, types.Address{19: 2}
	initial := account.NewAccount()
	initial.Initialised = true
	initial.Balance.SetUint64(7)
	s := New(snapshotSource{accounts: map[types.Address]*account.StateAccount{a: &initial}})
	base, err := s.SnapshotAccounts([]types.Address{a, empty})
	require.NoError(t, err)
	data, err := base.ReadAccountData(a)
	require.NoError(t, err)
	merged, err := s.MergeEOAAccounts(base, map[types.Address]*account.StateAccount{a: data, empty: nil}, &params.Rules{IsSpuriousDragon: true})
	require.NoError(t, err)
	require.True(t, merged)
	require.ElementsMatch(t, []types.Address{a, empty}, s.DirtyAddresses())
	require.Equal(t, uint64(7), s.GetBalance(a).Uint64())
	require.False(t, s.Exist(empty))
}

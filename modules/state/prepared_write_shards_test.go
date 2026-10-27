// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// prepShardsState is a block's state at the write with every kind of call
// CommitBlock makes: debited senders, credited and fresh recipients, an
// emptied account (EIP-161 removal), storage writes and clears, new
// contracts with code (CreateContract) and a selfdestruct.
func prepShardsState(t *testing.T, n int, seed int) *IntraBlockState {
	accts := make(map[types.Address]*account.StateAccount, 2*n)
	for i := 0; i < n; i++ {
		a := new(account.StateAccount)
		a.Initialised = true
		a.Nonce = uint64(i)
		a.Balance.SetUint64(1e18)
		accts[testAddr("s", i)] = a
		if i%3 != 0 {
			r := new(account.StateAccount)
			r.Initialised = true
			r.Balance.SetUint64(5)
			accts[testAddr("r", i)] = r
		}
	}
	ibs := New(mapStateReader{accts: accts})
	for i := 0; i < n; i++ {
		s := testAddr("s", i)
		ibs.SubBalance(s, uint256.NewInt(21000+uint64(seed)))
		ibs.SetNonce(s, uint64(i)+1)
		ibs.AddBalance(testAddr("r", i), uint256.NewInt(7+uint64(i%5)))
		switch i % 97 {
		case 3:
			// Storage on a sender: a write and a clear.
			k1, k2 := types.Hash{byte(i)}, types.Hash{byte(i), 1}
			ibs.SetState(s, &k1, *uint256.NewInt(uint64(i + seed)))
			ibs.SetState(s, &k2, uint256.Int{})
		case 5:
			c := testAddr("c", i)
			ibs.CreateAccount(c, true)
			ibs.SetCode(c, []byte{0x60, byte(i), 0x00})
			k := types.Hash{9, byte(i)}
			ibs.SetState(c, &k, *uint256.NewInt(uint64(seed + 1)))
		case 7:
			// Emptied: removed at the write.
			ibs.SubBalance(testAddr("r", i), ibs.GetBalance(testAddr("r", i)))
		case 11:
			ibs.Selfdestruct(s)
		}
	}
	require.NoError(t, ibs.FinalizeTx(&params.Rules{IsSpuriousDragon: true}, NewNoopWriter()))
	return ibs
}

// TestPrepareStateWriteShardsSame: the sharded preparation
// (N42_LAG_WRITE_PREP_SHARDS) produces the serial pass's rows in the same
// order, the same changeset rows, counts, fast flag and replay log, over
// many blocks and shard counts.
func TestPrepareStateWriteShardsSame(t *testing.T) {
	defer SetHistoryIndexDeferredForTest(true)()
	rules := &params.Rules{IsSpuriousDragon: true}
	for seed := 0; seed < 12; seed++ {
		n := 200 + 137*seed
		for _, k := range []int{2, 3, 8, 64} {
			restore := SetPrepShardsForTest(0, 0)
			want, err := PrepareStateWrite(prepShardsState(t, n, seed), rules, uint64(100+seed))
			restore()
			require.NoError(t, err)
			restore = SetPrepShardsForTest(k, 16)
			got, err := PrepareStateWrite(prepShardsState(t, n, seed), rules, uint64(100+seed))
			restore()
			require.NoError(t, err)
			require.Greater(t, got.Shards, 1, "seed %d k %d: not sharded", seed, k)
			require.Equal(t, want.fast, got.fast)
			require.Equal(t, want.rows.rows, got.rows.rows, "seed %d k %d: rows", seed, k)
			require.Equal(t, want.acct, got.acct, "seed %d k %d: account changesets", seed, k)
			require.Equal(t, want.stor, got.stor, "seed %d k %d: storage changesets", seed, k)
			require.Equal(t, want.nAccts, got.nAccts)
			require.Equal(t, want.nSlots, got.nSlots)
			require.Equal(t, want.log == nil, got.log == nil)
			require.NotEmpty(t, want.stor)
		}
	}
}

// TestPrepareStateWriteShardsFallback: a log whose addresses are not in
// order, or too short, is prepared in one pass.
func TestPrepareStateWriteShardsFallback(t *testing.T) {
	l := &StateWriteLog{}
	for i := 0; i < 64; i++ {
		var a account.StateAccount
		require.NoError(t, l.UpdateAccountData(testAddr("x", i), nil, &a))
	}
	require.Nil(t, l.shardOps(4, 4), "unsorted addresses")
	require.Nil(t, l.shardOps(4, 64), "too few calls")
	require.Nil(t, l.shardOps(1, 1), "one part")
	s := &StateWriteLog{}
	for i := 0; i < 64; i++ {
		var a account.StateAccount
		addr := types.Address{byte(i / 4)}
		require.NoError(t, s.UpdateAccountData(addr, nil, &a))
	}
	parts := s.shardOps(5, 4)
	require.Greater(t, len(parts), 1)
	total := 0
	for i, p := range parts {
		total += len(p)
		if i > 0 {
			// No address spans two parts.
			require.NotEqual(t, s.opAddr(parts[i-1][len(parts[i-1])-1]), s.opAddr(p[0]))
		}
	}
	require.Equal(t, 64, total)
}

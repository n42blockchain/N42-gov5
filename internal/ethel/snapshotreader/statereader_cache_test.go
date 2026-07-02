package snapshotreader

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
)

// TestStateReaderColdCache verifies the cross-block LRU on the immutable cold
// reader returns byte-identical results to the uncached path for present and
// absent accounts/storage, and that repeated reads (the cache-hit path) match
// the first (cache-miss) read. Since minimal snapshot-direct skips per-block
// state-root verification, a cache bug would otherwise go undetected — this is
// the guard.
func TestStateReaderColdCache(t *testing.T) {
	dir := t.TempDir()

	dict := [][32]byte{{0xaa, 0x01}, {0xbb, 0x02}}
	var ch0 types.Hash
	copy(ch0[:], dict[0][:])

	var caddr [20]byte // contract present
	caddr[0] = 1
	cval := snapAcctValue(7, uint256.NewInt(4242), &ch0, 0)
	var eaddr [20]byte // EOA present
	eaddr[0] = 2
	eval := snapAcctValue(3, uint256.NewInt(99), nil, -1)
	writeMiniTable(t, dir, "accounts", [][2][]byte{{caddr[:], cval}, {eaddr[:], eval}})
	writeCodeDict(t, dir, "accounts", dict)

	// storage: one present slot, plus a dummy so the table is non-empty.
	var saddr [20]byte
	saddr[0] = 3
	var sslot types.Hash
	sslot[31] = 0x0a
	sval := []byte{0xde, 0xad}
	dummy := make([]byte, 52)
	dummy[0] = 9
	writeMiniTable(t, dir, "storage", [][2][]byte{
		{append(append([]byte(nil), saddr[:]...), sslot[:]...), sval},
		{dummy, []byte{0x01}},
	})

	seg, err := OpenSegment(dir, "accounts", "storage")
	require.NoError(t, err)
	defer seg.Close()

	// absent keys
	var ghostAddr types.Address
	ghostAddr[0] = 0xff
	ghostAddr[19] = 0xee
	var ghostSlot types.Hash
	ghostSlot[0] = 0x77

	check := func(t *testing.T, r *StateReader) {
		t.Helper()
		var cAddr, eAddr, sAddr types.Address
		copy(cAddr[:], caddr[:])
		copy(eAddr[:], eaddr[:])
		copy(sAddr[:], saddr[:])

		// Read each key TWICE: first = miss (populates cache), second = hit.
		for i := 0; i < 2; i++ {
			a, err := r.ReadAccountData(cAddr)
			require.NoError(t, err)
			require.NotNil(t, a, "contract present (pass %d)", i)
			require.Equal(t, uint64(7), a.Nonce)
			require.Equal(t, uint64(4242), a.Balance.Uint64())
			require.Equal(t, ch0, a.CodeHash)

			a, err = r.ReadAccountData(eAddr)
			require.NoError(t, err)
			require.NotNil(t, a)
			require.Equal(t, uint64(3), a.Nonce)

			a, err = r.ReadAccountData(ghostAddr)
			require.NoError(t, err)
			require.Nil(t, a, "absent account must stay nil on cached read (pass %d)", i)

			v, err := r.ReadAccountStorage(sAddr, &sslot)
			require.NoError(t, err)
			require.Equal(t, sval, v, "storage hit (pass %d)", i)

			v, err = r.ReadAccountStorage(ghostAddr, &ghostSlot)
			require.NoError(t, err)
			require.Nil(t, v, "absent storage must stay nil on cached read (pass %d)", i)
		}
	}

	t.Run("cache-on", func(t *testing.T) {
		check(t, NewStateReader(seg, nil)) // default: cache enabled
	})
	t.Run("cache-off", func(t *testing.T) {
		t.Setenv("N42_SNAP_ACC_CACHE", "0")
		t.Setenv("N42_SNAP_STO_CACHE", "0")
		r := NewStateReader(seg, nil)
		require.Nil(t, r.accCache)
		require.Nil(t, r.stoCache)
		check(t, r)
	})
}

// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/parallel"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// fkStore is a chain's plain state between blocks: accounts and storage,
// enumerable (a self-destruct captures its pre-block slots).
type fkStore struct {
	accts map[types.Address]*account.StateAccount
	stor  map[types.Address]map[types.Hash]uint256.Int
}

func (s *fkStore) ReadAccountData(a types.Address) (*account.StateAccount, error) {
	acc, ok := s.accts[a]
	if !ok {
		return nil, nil
	}
	c := new(account.StateAccount)
	c.Copy(acc)
	return c, nil
}

func (s *fkStore) ReadAccountStorage(a types.Address, k *types.Hash) ([]byte, error) {
	if v, ok := s.stor[a][*k]; ok {
		return v.Bytes(), nil
	}
	return nil, nil
}
func (*fkStore) ReadAccountCode(types.Address, types.Hash) ([]byte, error)  { return nil, nil }
func (*fkStore) ReadAccountCodeSize(types.Address, types.Hash) (int, error) { return 0, nil }
func (s *fkStore) ForEachStorage(a types.Address, f func(types.Hash, []byte) bool) error {
	for k, v := range s.stor[a] {
		if !f(k, v.Bytes()) {
			break
		}
	}
	return nil
}

// fkWriter applies CommitBlock's writes to the store and records them
// (the plain-state rows with their previous values: the changesets).
type fkWriter struct {
	s   *fkStore
	log []string
}

func (w *fkWriter) UpdateAccountData(a types.Address, o, n *account.StateAccount) error {
	w.log = append(w.log, fmt.Sprintf("upd %x %d/%s/%x -> %d/%s/%x", a, o.Nonce, o.Balance.String(), o.CodeHash, n.Nonce, n.Balance.String(), n.CodeHash))
	c := new(account.StateAccount)
	c.Copy(n)
	w.s.accts[a] = c
	return nil
}
func (w *fkWriter) UpdateAccountCode(a types.Address, h types.Hash, _ []byte) error {
	w.log = append(w.log, fmt.Sprintf("code %x %x", a, h))
	return nil
}
func (w *fkWriter) DeleteAccount(a types.Address, _ *account.StateAccount) error {
	w.log = append(w.log, fmt.Sprintf("del %x", a))
	delete(w.s.accts, a)
	delete(w.s.stor, a)
	return nil
}
func (w *fkWriter) WriteAccountStorage(a types.Address, k types.Hash, o, v uint256.Int) error {
	w.log = append(w.log, fmt.Sprintf("sto %x %x %s -> %s", a, k, o.String(), v.String()))
	if v.IsZero() {
		delete(w.s.stor[a], k)
		return nil
	}
	if w.s.stor[a] == nil {
		w.s.stor[a] = map[types.Hash]uint256.Int{}
	}
	w.s.stor[a][k] = v
	return nil
}
func (w *fkWriter) CreateContract(a types.Address) error {
	w.log = append(w.log, fmt.Sprintf("create %x", a))
	delete(w.s.stor, a)
	return nil
}

// fkBlockOut is what one block of the chain leaves.
type fkBlockOut struct {
	afterEnd string
	root     types.Hash
	rootRows []string
	post     *state.PostState
	rows     []string
	keys     int
}

// fkChain is a chain of random blocks over one evolving store.
type fkChain struct {
	store     *fkStore
	users     []types.Address // externally owned, sending and receiving
	contracts []types.Address // with code and storage
	next      int
	seed      int64
}

func newFKChain(seed int64, users, contracts int) *fkChain {
	c := &fkChain{store: &fkStore{accts: map[types.Address]*account.StateAccount{}, stor: map[types.Address]map[types.Hash]uint256.Int{}}, seed: seed}
	for i := 0; i < users; i++ {
		a := c.fresh()
		acc := account.NewAccount()
		acc.Initialised = true
		acc.Balance.SetUint64(1e15)
		c.store.accts[a] = &acc
		c.users = append(c.users, a)
	}
	for i := 0; i < contracts; i++ {
		c.contracts = append(c.contracts, c.newContract(byte(i)))
	}
	return c
}

func (c *fkChain) fresh() types.Address {
	c.next++
	return bulkTestAddr(int(c.seed)*100_000_000 + c.next)
}

func (c *fkChain) newContract(tag byte) types.Address {
	a := c.fresh()
	acc := account.NewAccount()
	acc.Initialised = true
	acc.Balance.SetUint64(7)
	acc.Nonce = 1
	acc.CodeHash = crypto.Keccak256Hash([]byte{0x60, tag})
	c.store.accts[a] = &acc
	slots := map[types.Hash]uint256.Int{}
	for k := 0; k < 3; k++ {
		slots[types.Hash{byte(k + 1)}] = *uint256.NewInt(uint64(k + 11))
	}
	c.store.stor[a] = slots
	return a
}

// block builds block n's final MVS over the store: transfers between
// users (each address in one role per block), credits to untouched and to
// absent accounts, storage writes and clears, contract creations with code
// and storage, self-destructs of contracts with storage and recreations
// over a wipe. It also returns the absent and untouched accounts the block
// end credits.
func (c *fkChain) block(n, numTxs int) (mvs *parallel.MVS, known bulkKnownCache, extra []types.Address) {
	rng := rand.New(rand.NewSource(c.seed*1000 + int64(n)))
	mvs = parallel.NewMVS()
	known = bulkKnownCache{}
	for a, acc := range c.store.accts {
		if rng.Intn(40) != 0 { // a few are left to the base reader
			cp := new(account.StateAccount)
			cp.Copy(acc)
			known[a] = cp
		}
	}
	perm := rng.Perm(len(c.users))
	half := len(perm) / 2
	senders, recips := perm[:half], perm[half:]
	type cur struct {
		bal   uint256.Int
		nonce uint64
	}
	sent := map[types.Address]*cur{}
	full := func(a types.Address, tx int, acc *account.StateAccount) {
		mvs.Write(parallel.LocationKey{Address: a, Field: parallel.FieldBalance}, tx, 0, acc.MarshalV2())
	}
	emptyHash := crypto.Keccak256Hash(nil)
	destroyed := map[types.Address]bool{}
	pickContract := func() (types.Address, bool) {
		ct := c.contracts[rng.Intn(len(c.contracts))]
		return ct, !destroyed[ct]
	}
	var created []types.Address
	for tx := 0; tx < numTxs; tx++ {
		from := c.users[senders[rng.Intn(len(senders))]]
		st := sent[from]
		if st == nil {
			st = &cur{}
			acc := c.store.accts[from]
			st.bal, st.nonce = acc.Balance, acc.Nonce
			sent[from] = st
		}
		st.nonce++
		st.bal.SubUint64(&st.bal, 21000)
		acc := account.NewAccount()
		acc.Balance, acc.Nonce, acc.CodeHash = st.bal, st.nonce, emptyHash
		full(from, tx, &acc)
		switch r := rng.Intn(100); {
		case r < 55: // a transfer to a user who sends nothing this block
			to := c.users[recips[rng.Intn(len(recips))]]
			mvs.WriteDelta(parallel.LocationKey{Address: to, Field: parallel.FieldBalance}, tx, 0, uint256.NewInt(uint64(rng.Intn(3))))
		case r < 65: // a transfer to an absent account (it stays a user)
			to := c.fresh()
			known[to] = nil
			mvs.WriteDelta(parallel.LocationKey{Address: to, Field: parallel.FieldBalance}, tx, 0, uint256.NewInt(uint64(1+rng.Intn(5))))
			c.users = append(c.users, to) // committed with its credit
		case r < 80: // a storage write or clear
			ct, ok := pickContract()
			if !ok {
				continue
			}
			slot := types.Hash{byte(1 + rng.Intn(6))}
			val := []byte{byte(1 + rng.Intn(200))}
			if rng.Intn(4) == 0 {
				val = nil
			}
			mvs.Write(parallel.LocationKey{Address: ct, Field: parallel.FieldStorage, Slot: slot}, tx, 0, val)
		case r < 85: // a creation with code and storage
			ct := c.fresh()
			known[ct] = nil
			code := []byte{0x60, byte(tx), byte(n)}
			cacc := account.NewAccount()
			cacc.Balance.SetUint64(uint64(rng.Intn(9)))
			cacc.Nonce = 1
			cacc.CodeHash = crypto.Keccak256Hash(code)
			full(ct, tx, &cacc)
			mvs.Write(parallel.LocationKey{Address: ct, Field: parallel.FieldCode}, tx, 0, code)
			mvs.Write(parallel.LocationKey{Address: ct, Field: parallel.FieldStorage, Slot: types.Hash{1}}, tx, 0, []byte{byte(tx) | 1})
			created = append(created, ct)
		case r < 88: // a self-destruct of a contract with storage
			ct, ok := pickContract()
			if !ok {
				continue
			}
			destroyed[ct] = true
			mvs.Write(parallel.LocationKey{Address: ct, Field: parallel.FieldBalance}, tx, 0, nil)
		case r < 90: // recreated over a wipe
			ct, ok := pickContract()
			if !ok {
				continue
			}
			destroyed[ct] = true // no later write this block
			code := []byte{0x61, byte(tx), byte(n)}
			cacc := account.NewAccount()
			cacc.Balance.SetUint64(5)
			cacc.Nonce = 1
			cacc.CodeHash = crypto.Keccak256Hash(code)
			mvs.Write(parallel.LocationKey{Address: ct, Field: parallel.FieldStorageWipe}, tx, 0, []byte{1})
			full(ct, tx, &cacc)
			mvs.Write(parallel.LocationKey{Address: ct, Field: parallel.FieldCode}, tx, 0, code)
			mvs.Write(parallel.LocationKey{Address: ct, Field: parallel.FieldStorage, Slot: types.Hash{3}}, tx, 0, []byte{4})
		}
	}
	var live []types.Address
	for _, ct := range c.contracts {
		if !destroyed[ct] {
			live = append(live, ct)
		}
	}
	c.contracts = append(live, created...)
	if len(c.contracts) < 4 {
		for i := 0; i < 4; i++ {
			c.contracts = append(c.contracts, c.newContract(byte(100+i)))
		}
	}
	// The block end's credits: an absent account, a user the block did not
	// touch at all (if any), a credited recipient.
	extra = []types.Address{bulkTestAddr(-int(c.seed)*1000 - n), c.users[recips[0]]}
	return mvs, known, extra
}

// fkRunBlock folds block n of chain c as the import does (no-journal fold,
// keys tracked when the flag is on), runs the block end -- the system
// call's finalize, rewards, and on some blocks a reverted credit or
// creation that breaks the keys -- the root and CommitBlock.
func fkRunBlock(t *testing.T, c *fkChain, n, numTxs int) fkBlockOut {
	t.Helper()
	mvs, known, extra := c.block(n, numTxs)
	pf := state.NewAccountPrefetch(c.store)
	pf.SetKnown(known)
	ibs := state.New(pf)
	ibs.SetAccountPrefetch(pf)
	ibs.SetDetachedPendingCredits(true)
	ibs.GetBalance(c.contracts[0]) // a block-start system call's object
	ibs.AddBalance(extra[1], uint256.NewInt(1))
	ibs.SetFoldNoJournal(true)
	ibs.BeginFoldKeys()
	require.NoError(t, applyMVSToIBS(mvs, numTxs, ibs))
	ibs.SetFoldNoJournal(false)
	ibs.CaptureFoldKeys()

	rules := &params.Rules{IsSpuriousDragon: true, IsByzantium: true, IsLondon: true}
	require.NoError(t, ibs.FinalizeTx(rules, state.NewNoopWriter()))
	ibs.AddBalance(extra[0], uint256.NewInt(2))
	ibs.AddBalance(extra[1], uint256.NewInt(3))
	switch n % 4 {
	case 2: // a credit inserted and reverted: a balanceInc delete
		snap := ibs.Snapshot()
		ibs.AddBalance(bulkTestAddr(-int(c.seed)*1000-500-n), uint256.NewInt(9))
		ibs.RevertToSnapshot(snap)
	case 3: // a creation reverted: a stateObjectsDirty delete
		snap := ibs.Snapshot()
		ibs.CreateAccount(bulkTestAddr(-int(c.seed)*1000-700-n), true)
		ibs.RevertToSnapshot(snap)
	}
	var out fkBlockOut
	out.afterEnd = ibs.FinalizeDigestForTest()
	rc := &hashingRootComputer{}
	ibs.SetRootComputer(rc)
	ibs.SetRootPostStateHook(func(ps *state.PostState) { out.post = ps })
	out.root = ibs.IntermediateRoot()
	out.rootRows = append([]string(nil), rc.rows...)
	out.keys = ibs.LastRootPrepPhases().FoldKeys
	w := &fkWriter{s: c.store}
	require.NoError(t, ibs.CommitBlock(rules, w))
	sort.Strings(w.log)
	out.rows = w.log
	return out
}

// TestFinalizeFoldKeysMatchesMaps runs chains of random blocks with
// N42_FINALIZE_FOLD_KEYS off and on (every use cross-checked against the
// maps) and requires the same state after the block end, root, root dirty
// set, post-state, committed rows with their previous values and final
// store, block by block, for the serial and the sharded fold and the fast
// finalize on and off. The keys must be used on the blocks that keep both
// maps' keys, and fall back on those that delete one.
func TestFinalizeFoldKeysMatchesMaps(t *testing.T) {
	blocks, numTxs := 6, 9000
	if testing.Short() {
		blocks = 4
	}
	defer state.SetFoldKeysVerifyForTest(true)()
	defer state.SetFinalizeFastMinForTest(512)()
	for _, shard := range []int{0, 16} {
		for _, fast := range []bool{true, false} {
			t.Run(fmt.Sprintf("shard=%d fast=%v", shard, fast), func(t *testing.T) {
				prev := collectShardWorkers
				collectShardWorkers = shard
				defer func() { collectShardWorkers = prev }()
				defer state.SetFinalizeFastForTest(fast)()
				off, on := newFKChain(7, 6000, 24), newFKChain(7, 6000, 24)
				for n := 0; n < blocks; n++ {
					restore := state.SetFinalizeFoldKeysForTest(false)
					want := fkRunBlock(t, off, n, numTxs)
					restore()
					restore = state.SetFinalizeFoldKeysForTest(true)
					got := fkRunBlock(t, on, n, numTxs)
					restore()
					tag := fmt.Sprintf("block %d", n)
					require.Zero(t, want.keys, tag)
					if n%4 < 2 {
						require.Equal(t, 1, got.keys, "%s: the fold keys were not used", tag)
					} else {
						require.Equal(t, -1, got.keys, "%s: the fold keys survived a delete", tag)
					}
					require.Equal(t, want.afterEnd, got.afterEnd, "%s: after the block end", tag)
					require.Equal(t, want.rootRows, got.rootRows, "%s: root dirty set", tag)
					require.Equal(t, want.root, got.root, "%s: root", tag)
					require.Equal(t, want.post, got.post, "%s: post-state", tag)
					require.Equal(t, want.rows, got.rows, "%s: committed rows", tag)
					require.Greater(t, len(want.rows), numTxs/4, "%s: few rows committed", tag)
				}
				require.Equal(t, off.store, on.store, "final store")
			})
		}
	}
}

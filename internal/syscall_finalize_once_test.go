// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/u256"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/modules/state"
)

// sfoReader is an in-memory pre-block store with code and storage.
type sfoReader struct {
	accts map[types.Address]*account.StateAccount
	code  map[types.Hash][]byte
	stor  map[types.Address]map[types.Hash]uint256.Int
}

func (r sfoReader) ReadAccountData(a types.Address) (*account.StateAccount, error) {
	if v := r.accts[a]; v != nil {
		c := new(account.StateAccount)
		c.Copy(v)
		return c, nil
	}
	return nil, nil
}

func (r sfoReader) ReadAccountStorage(a types.Address, k *types.Hash) ([]byte, error) {
	if v, ok := r.stor[a][*k]; ok {
		return v.Bytes(), nil
	}
	return nil, nil
}

func (r sfoReader) ReadAccountCode(_ types.Address, h types.Hash) ([]byte, error) {
	return r.code[h], nil
}

func (r sfoReader) ReadAccountCodeSize(_ types.Address, h types.Hash) (int, error) {
	return len(r.code[h]), nil
}

// sfoRootComputer captures the dirty set the root is computed from.
type sfoRootComputer struct{ digest string }

func (c *sfoRootComputer) ComputeRoot(a map[types.Address]*account.StateAccount, s map[types.Address]map[types.Hash]*uint256.Int) (types.Hash, error) {
	var b bytes.Buffer
	for _, addr := range sortedAddrKeys(a) {
		if v := a[addr]; v == nil {
			fmt.Fprintf(&b, "%x deleted\n", addr)
		} else {
			fmt.Fprintf(&b, "%x %d %s %x\n", addr, v.Nonce, v.Balance.String(), v.CodeHash)
		}
	}
	for _, addr := range sortedAddrKeys(s) {
		slots := s[addr]
		keys := make([]types.Hash, 0, len(slots))
		for k := range slots {
			keys = append(keys, k)
		}
		sortHashes(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "%x %x %s\n", addr, k, slots[k].String())
		}
	}
	c.digest = b.String()
	return crypto.Keccak256Hash(b.Bytes()), nil
}

func sortedAddrKeys[V any](m map[types.Address]V) []types.Address {
	out := make([]types.Address, 0, len(m))
	for a := range m {
		out = append(out, a)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && bytes.Compare(out[j][:], out[j-1][:]) < 0; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func sortHashes(h []types.Hash) {
	for i := 1; i < len(h); i++ {
		for j := i; j > 0 && bytes.Compare(h[j][:], h[j-1][:]) < 0; j-- {
			h[j], h[j-1] = h[j-1], h[j]
		}
	}
}

// sfoWriter is a real (non-no-op) writer that records its calls.
type sfoWriter struct{ log bytes.Buffer }

func (w *sfoWriter) UpdateAccountData(a types.Address, o, n *account.StateAccount) error {
	fmt.Fprintf(&w.log, "upd %x %d %s %x\n", a, n.Nonce, n.Balance.String(), n.CodeHash)
	return nil
}
func (w *sfoWriter) UpdateAccountCode(a types.Address, h types.Hash, _ []byte) error {
	fmt.Fprintf(&w.log, "code %x %x\n", a, h)
	return nil
}
func (w *sfoWriter) DeleteAccount(a types.Address, _ *account.StateAccount) error {
	fmt.Fprintf(&w.log, "del %x\n", a)
	return nil
}
func (w *sfoWriter) WriteAccountStorage(a types.Address, k types.Hash, o, v uint256.Int) error {
	fmt.Fprintf(&w.log, "sto %x %x %s %s\n", a, k, o.String(), v.String())
	return nil
}
func (w *sfoWriter) CreateContract(a types.Address) error {
	fmt.Fprintf(&w.log, "create %x\n", a)
	return nil
}

// sfoOutcome is what one block leaves: the 7002/7251 request bytes, the
// state after the block-end finalize, the root's dirty set and
// CommitBlock's rows.
type sfoOutcome struct {
	requests []hexutil.Bytes
	fin      string
	root     types.Hash
	rootSet  string
	rows     string
}

// sfoCall runs one user transaction's call into contract with value.
func sfoCall(t *testing.T, ibs *state.IntraBlockState, header *block.Header, from, to types.Address, value uint64, data []byte) {
	t.Helper()
	cfg := testPragueConfig()
	v := uint256.NewInt(value)
	msg := transaction.NewMessage(from, &to, 0, v, 1_000_000, u256.Num0, nil, nil, nil, nil, data, nil, false, true)
	policy := newExecutionChainPolicy(cfg, "")
	author, txContext := policy.systemCallContext(header, msg)
	blockContext := NewEVMBlockContext(header, GetHashFn(header, nil), nil, cfg, author)
	evm := vm.NewEVM(blockContext, txContext, ibs, cfg, vm.Config{NoReceipts: true})
	ibs.PrepareAccessList(from, &to, vm.ActivePrecompiles(evm.ChainRules()), nil)
	_, _, err := evm.Call(vm.AccountRef(from), to, data, 1_000_000, v, false)
	require.NoError(t, err)
}

// txFinalize is how the block's transactions are finalized before the
// block end: "fold" (none, the parallel processor's one journal), "noop"
// or "real" (a FinalizeTx after every transaction with that writer).
func runSFOBlock(t *testing.T, once, detach bool, txFinalize string, nRecipients int, sysTouch bool) sfoOutcome {
	t.Helper()
	defer state.SetSyscallFinalizeOnceForTest(once)()
	cfg := testPragueConfig()
	header := &block.Header{
		Number: uint256.NewInt(1), Time: 1, GasLimit: 30_000_000,
		BaseFee: uint256.NewInt(7), Difficulty: uint256.NewInt(0),
		Coinbase: types.HexToAddress("0xc0ffee"),
	}
	rules := cfg.RulesWithTimestamp(1, 1)
	addr := func(tag string, i int) types.Address {
		return types.BytesToAddress(crypto.Keccak256([]byte(fmt.Sprintf("sfo-%s-%d", tag, i))))
	}
	r := sfoReader{accts: map[types.Address]*account.StateAccount{}, code: map[types.Hash][]byte{}, stor: map[types.Address]map[types.Hash]uint256.Int{}}
	putCode := func(a types.Address, code []byte, bal uint64) {
		acc := account.NewAccount()
		acc.Nonce = 1
		acc.Balance.SetUint64(bal)
		acc.CodeHash = crypto.Keccak256Hash(code)
		r.code[acc.CodeHash] = code
		r.accts[a] = &acc
	}
	putCode(vm.WithdrawalRequestsAddress, vm.WithdrawalRequestQueueCode, 0)
	putCode(vm.ConsolidationRequestsAddress, vm.ConsolidationRequestQueueCode, 0)
	victim := addr("victim", 0) // self-destructed with storage
	putCode(victim, []byte{0x60, 0x00, 0x00}, 5)
	r.stor[victim] = map[types.Hash]uint256.Int{{1}: *uint256.NewInt(3)}
	empty := addr("empty", 0) // exists, empty, touched
	ea := account.NewAccount()
	r.accts[empty] = &ea
	users := make([]types.Address, 4)
	for i := range users {
		users[i] = addr("user", i)
		a := account.NewAccount()
		a.Balance.SetUint64(1e18)
		r.accts[users[i]] = &a
	}
	recipients := make([]types.Address, nRecipients)
	for i := range recipients {
		recipients[i] = addr("rcpt", i)
		if i%3 != 0 { // every third absent
			a := account.NewAccount()
			a.Balance.SetUint64(uint64(i + 1))
			r.accts[recipients[i]] = &a
		}
	}

	var ibs *state.IntraBlockState
	if detach {
		pf := state.NewAccountPrefetch(r)
		ibs = state.New(pf)
		ibs.SetAccountPrefetch(pf)
		ibs.SetDetachedPendingCredits(true)
	} else {
		ibs = state.New(r)
	}
	rc := &sfoRootComputer{}
	ibs.SetRootComputer(rc)
	txEnd := func() {
		switch txFinalize {
		case "noop":
			require.NoError(t, ibs.FinalizeTx(rules, state.NewNoopWriter()))
		case "real":
			require.NoError(t, ibs.FinalizeTx(rules, &sfoWriter{}))
		}
	}

	// Transactions touching the system contracts: two withdrawal requests
	// and one consolidation request (fee 1 wei at zero excess).
	wd := func(i int) []byte {
		return append(bytes.Repeat([]byte{byte(0xa0 + i)}, 48), make([]byte, 8)...)
	}
	sfoCall(t, ibs, header, users[0], vm.WithdrawalRequestsAddress, 1, wd(0))
	txEnd()
	sfoCall(t, ibs, header, users[1], vm.WithdrawalRequestsAddress, 1, wd(1))
	txEnd()
	sfoCall(t, ibs, header, users[2], vm.ConsolidationRequestsAddress, 1, bytes.Repeat([]byte{0xcc}, 96))
	txEnd()
	// A self-destruct, an empty account touched, a contract created.
	ibs.Selfdestruct(victim)
	ibs.AddBalance(users[3], uint256.NewInt(5))
	txEnd()
	ibs.GetBalance(empty)
	ibs.AddBalance(empty, uint256.NewInt(0))
	created := addr("created", 0)
	ibs.CreateAccount(created, true)
	ibs.SetCode(created, []byte{0x60, 0x01, 0x00})
	ibs.SetState(created, &types.Hash{2}, *uint256.NewInt(4))
	txEnd()
	if sysTouch {
		ibs.AddBalance(state.SystemAddress, uint256.NewInt(0))
		ibs.GetBalance(state.SystemAddress)
		txEnd()
	}
	// Credited-only recipients (pending credits).
	for i := 0; i < 3*nRecipients; i++ {
		ibs.SubBalance(users[3], uint256.NewInt(21000))
		ibs.AddBalance(recipients[(i*7919)%nRecipients], uint256.NewInt(uint64(1+i%5)))
		if i%500 == 499 {
			txEnd()
		}
	}
	txEnd()
	if pf := ibs.AccountPrefetch(); pf != nil {
		seed := map[types.Address]*account.StateAccount{}
		for _, a := range ibs.PendingBalanceIncreases() {
			v, _ := r.ReadAccountData(a)
			seed[a] = v
		}
		pf.Seed(seed)
	}

	var out sfoOutcome
	reqs, err := ProcessPragueSystemCalls(cfg, ibs, header, nil)
	require.NoError(t, err)
	out.requests = reqs
	out.fin = ibs.FinalizeDigestForTest()
	ibs.AddBalance(header.Coinbase, uint256.NewInt(2)) // the reward
	out.root = ibs.IntermediateRoot()
	out.rootSet = rc.digest
	w := &sfoWriter{}
	require.NoError(t, ibs.CommitBlock(rules, w))
	out.rows = w.log.String()
	return out
}

// TestSyscallFinalizeOnceMatchesPerCall: N42_SYSCALL_FINALIZE_ONCE (one
// FinalizeTx after both Prague system calls, the parallel no-op walk)
// leaves the same request bytes, object flags, stateObjectsDirty, journal
// dirty set, root dirty set and CommitBlock rows as the per-call finalize,
// with detached credits on and off, the block's transactions folded into
// one journal or finalized per transaction with the no-op or a real
// writer, the system address touched by a transaction or not, and the
// parallel walk's threshold at 1 and at its default.
func TestSyscallFinalizeOnceMatchesPerCall(t *testing.T) {
	nRecipients := 3000
	if testing.Short() {
		nRecipients = 400
	}
	for _, detach := range []bool{false, true} {
		for _, txFin := range []string{"fold", "noop", "real"} {
			for _, sysTouch := range []bool{false, true} {
				tag := fmt.Sprintf("detach=%v tx=%s sysTouch=%v", detach, txFin, sysTouch)
				want := runSFOBlock(t, false, detach, txFin, nRecipients, sysTouch)
				require.Len(t, want.requests, 2, tag)
				require.Equal(t, byte(vm.WithdrawalRequestType), want.requests[0][0], tag)
				require.Equal(t, 1+2*76, len(want.requests[0]), tag)
				require.Equal(t, byte(vm.ConsolidationRequestType), want.requests[1][0], tag)
				require.Equal(t, 1+116, len(want.requests[1]), tag)
				for _, parMin := range []int{1, 2048} {
					func() {
						defer state.SetSyscallFinalizeParMinForTest(parMin)()
						w0 := state.SyscallFinalizeParWalks()
						got := runSFOBlock(t, true, detach, txFin, nRecipients, sysTouch)
						sub := fmt.Sprintf("%s parMin=%d", tag, parMin)
						if parMin == 1 {
							require.Greater(t, state.SyscallFinalizeParWalks(), w0, sub)
						}
						require.Equal(t, want.requests, got.requests, sub)
						require.Equal(t, want.fin, got.fin, sub)
						require.Equal(t, want.rootSet, got.rootSet, sub)
						require.Equal(t, want.root, got.root, sub)
						require.Equal(t, want.rows, got.rows, sub)
					}()
				}
			}
		}
	}
}

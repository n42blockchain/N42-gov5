package state

import (
	"fmt"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// AccountSnapshot holds account metadata for an explicitly captured address set.
// It supports concurrent reads and returns independent copies. Code and storage
// are not captured: their reads fail, as do reads outside the captured set.
// It is suitable for restricted speculative execution, not general EVM replay.
type AccountSnapshot struct {
	accounts map[types.Address]*account.StateAccount
}

var _ StateReader = (*AccountSnapshot)(nil)

// SnapshotAccounts captures the current account view, including earlier block
// execution and deferred balance credits. Call it on the state's owning
// goroutine; construction accesses the reader, caches and journal. The returned
// snapshot can then be shared without sharing the underlying reader or state.
//
// Capture does not finalize transactions or copy execution metadata (refunds,
// access lists, transient storage, selfdestruct flags). Callers must establish
// a transaction boundary and restrict execution accordingly. On a read error it
// returns no snapshot and leaves the state's normal sticky error intact.
func (sdb *IntraBlockState) SnapshotAccounts(addresses []types.Address) (*AccountSnapshot, error) {
	if err := sdb.Error(); err != nil {
		return nil, err
	}
	snapshot := &AccountSnapshot{accounts: make(map[types.Address]*account.StateAccount, len(addresses))}
	for _, addr := range addresses {
		if _, captured := snapshot.accounts[addr]; captured {
			continue
		}
		sdb.traceAccountRead(addr)
		obj := sdb.getStateObject(addr)
		if err := sdb.Error(); err != nil {
			return nil, err
		}
		var data *account.StateAccount
		if obj != nil && !obj.deleted {
			data = obj.data.SelfCopy()
		}
		snapshot.accounts[addr] = data
	}
	return snapshot, nil
}

func (s *AccountSnapshot) ReadAccountData(addr types.Address) (*account.StateAccount, error) {
	data, captured := s.accounts[addr]
	if !captured {
		return nil, fmt.Errorf("account snapshot: uncaptured address %s", addr)
	}
	if data == nil {
		return nil, nil
	}
	return data.SelfCopy(), nil
}

func (*AccountSnapshot) ReadAccountStorage(types.Address, *types.Hash) ([]byte, error) {
	return nil, fmt.Errorf("account snapshot: storage not captured")
}

func (*AccountSnapshot) ReadAccountCode(types.Address, types.Hash) ([]byte, error) {
	return nil, fmt.Errorf("account snapshot: code not captured")
}

func (*AccountSnapshot) ReadAccountCodeSize(types.Address, types.Hash) (int, error) {
	return 0, fmt.Errorf("account snapshot: code not captured")
}

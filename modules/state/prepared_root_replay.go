package state

import (
	"errors"
	"unsafe"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

type preparedRootAccount struct {
	address types.Address
	data    account.StateAccount
	deleted bool
}
type preparedRootSlot struct {
	address types.Address
	key     types.Hash
	value   uint256.Int
}

// PreparedRootReplay owns the exact root dirty set without retaining the source
// IBS, account prefetcher or read transaction. It does not authenticate a block.
type PreparedRootReplay struct {
	accounts []preparedRootAccount
	storage  []preparedRootSlot
	root     types.Hash
}

func PrepareRootReplay(ibs *IntraBlockState) (*PreparedRootReplay, error) {
	if ibs == nil {
		return nil, errors.New("prepared replay requires a state")
	}
	root, ok := ibs.LastIntermediateRoot()
	if !ok {
		return nil, errors.New("prepared replay requires a computed root")
	}
	accounts, storage := ibs.LastRootDirtySet()
	if accounts == nil && storage == nil {
		return nil, errors.New("prepared replay requires root dirty snapshot")
	}
	p := &PreparedRootReplay{root: root, accounts: make([]preparedRootAccount, 0, len(accounts))}
	for address, data := range accounts {
		a := preparedRootAccount{address: address, deleted: data == nil}
		if data != nil {
			a.data = *data
		}
		p.accounts = append(p.accounts, a)
	}
	for address, slots := range storage {
		for key, value := range slots {
			slot := preparedRootSlot{address: address, key: key}
			if value != nil {
				slot.value = *value
			}
			p.storage = append(p.storage, slot)
		}
	}
	return p, nil
}

// State reconstructs only the root-replay carrier. Callers must supply the
// prepared write instead of calling CommitBlock on this deliberately empty IBS.
func (p *PreparedRootReplay) State(computer RootComputer) *IntraBlockState {
	s := New(nil)
	s.rootComputer = computer
	s.lastIntermediateRoot, s.lastIntermediateRootSet = p.root, true
	s.lastRootAccounts = make(map[types.Address]*account.StateAccount, len(p.accounts))
	for _, a := range p.accounts {
		if a.deleted {
			s.lastRootAccounts[a.address] = nil
		} else {
			data := a.data
			s.lastRootAccounts[a.address] = &data
		}
	}
	s.lastRootStorage = make(map[types.Address]map[types.Hash]*uint256.Int)
	for _, slot := range p.storage {
		slots := s.lastRootStorage[slot.address]
		if slots == nil {
			slots = make(map[types.Hash]*uint256.Int)
			s.lastRootStorage[slot.address] = slots
		}
		value := slot.value
		slots[slot.key] = &value
	}
	return s
}

func (p *PreparedRootReplay) RetainedBytes() int {
	return int(unsafe.Sizeof(*p)) + cap(p.accounts)*int(unsafe.Sizeof(preparedRootAccount{})) + cap(p.storage)*int(unsafe.Sizeof(preparedRootSlot{}))
}

// RetainedBytes accounts for backing capacities and variable-length payloads;
// shared/static table names are conservatively counted as owned as well.
func (p *PreparedStateWrite) RetainedBytes() int {
	n := int(unsafe.Sizeof(*p)) + cap(p.rows.rows)*int(unsafe.Sizeof(stateRow{})) + (cap(p.acct)+cap(p.stor))*int(unsafe.Sizeof(ChangeSetRow{}))
	for _, r := range p.rows.rows {
		n += len(r.table) + cap(r.key) + cap(r.value)
	}
	for _, rows := range [][]ChangeSetRow{p.acct, p.stor} {
		for _, r := range rows {
			n += cap(r.K) + cap(r.V)
		}
	}
	if l := p.log; l != nil {
		n += int(unsafe.Sizeof(*l)) + cap(l.ops)*int(unsafe.Sizeof(stateWriteOp{})) + cap(l.accts)*int(unsafe.Sizeof(stateWriteAccount{})) + cap(l.slots)*int(unsafe.Sizeof(stateWriteSlot{})) + cap(l.codes)*int(unsafe.Sizeof(stateWriteCode{})) + cap(l.creates)*int(unsafe.Sizeof(types.Address{}))
		for _, c := range l.codes {
			n += cap(c.code)
		}
	}
	return n
}

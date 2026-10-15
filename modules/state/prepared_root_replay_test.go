package state

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

func TestPreparedRootReplayOwnsValuesAndRetainsNoSourceReader(t *testing.T) {
	address := types.HexToAddress("0x1234")
	key := types.Hash{1}
	ibs := New(mapStateReader{})
	ibs.lastIntermediateRoot, ibs.lastIntermediateRootSet = types.Hash{7}, true
	data := account.NewAccount()
	data.Balance.SetUint64(9)
	ibs.lastRootAccounts = map[types.Address]*account.StateAccount{address: &data}
	ibs.lastRootStorage = map[types.Address]map[types.Hash]*uint256.Int{address: {key: uint256.NewInt(11), types.Hash{2}: nil}}
	prepared, err := PrepareRootReplay(ibs)
	if err != nil {
		t.Fatal(err)
	}
	data.Balance.SetUint64(999)
	ibs.lastRootStorage[address][key].SetUint64(999)
	carrier := prepared.State(&capturingRootComputer{})
	if carrier.GetStateReader() != nil {
		t.Fatal("prepared snapshot retained source reader")
	}
	accounts, storage := carrier.LastRootDirtySet()
	if accounts[address].Balance.Uint64() != 9 || storage[address][key].Uint64() != 11 || !storage[address][types.Hash{2}].IsZero() {
		t.Fatal("snapshot aliases source values or lost zero deletion")
	}
	if root, ok := carrier.LastIntermediateRoot(); !ok || root != (types.Hash{7}) {
		t.Fatal("snapshot lost computed root")
	}
	accounts[address].Balance.SetUint64(5)
	second := prepared.State(&capturingRootComputer{})
	again, _ := second.LastRootDirtySet()
	if again[address].Balance.Uint64() != 9 || prepared.RetainedBytes() <= 0 {
		t.Fatal("carrier mutation corrupted cached snapshot")
	}
	if _, err := PrepareRootReplay(nil); err == nil {
		t.Fatal("nil state accepted")
	}
}

func TestPreparedWriteBudgetCountsBackingCapacityAndPayload(t *testing.T) {
	p := &PreparedStateWrite{log: &StateWriteLog{codes: make([]stateWriteCode, 1, 8)}}
	before := p.RetainedBytes()
	p.log.codes[0].code = make([]byte, 1, 1<<20)
	if p.RetainedBytes()-before != 1<<20 {
		t.Fatal("prepared budget counted payload length instead of backing capacity")
	}
}

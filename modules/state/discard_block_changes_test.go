package state

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

type discardChangesReader struct {
	value []byte
}

type discardChangesWriter struct {
	storageWrites int
	original      uint256.Int
	value         uint256.Int
}

func (*discardChangesWriter) UpdateAccountData(types.Address, *account.StateAccount, *account.StateAccount) error {
	return nil
}
func (*discardChangesWriter) UpdateAccountCode(types.Address, types.Hash, []byte) error { return nil }
func (*discardChangesWriter) DeleteAccount(types.Address, *account.StateAccount) error  { return nil }
func (*discardChangesWriter) CreateContract(types.Address) error                        { return nil }
func (*discardChangesWriter) WriteChangeSets() error                                    { return nil }
func (*discardChangesWriter) WriteHistory() error                                       { return nil }
func (w *discardChangesWriter) WriteAccountStorage(_ types.Address, _ types.Hash, original, value uint256.Int) error {
	w.storageWrites++
	w.original = original
	w.value = value
	return nil
}

func (r discardChangesReader) ReadAccountData(types.Address) (*account.StateAccount, error) {
	a := account.NewAccount()
	return &a, nil
}

func (r discardChangesReader) ReadAccountStorage(types.Address, *types.Hash) ([]byte, error) {
	return r.value, nil
}

func (discardChangesReader) ReadAccountCode(types.Address, types.Hash) ([]byte, error) {
	return nil, nil
}

func (discardChangesReader) ReadAccountCodeSize(types.Address, types.Hash) (int, error) {
	return 0, nil
}

func TestDiscardBlockChangesKeepsExecutionStateWithoutBlockOriginals(t *testing.T) {
	addr := types.HexToAddress("0x1234")
	slot := types.HexToHash("0x42")
	sdb := New(discardChangesReader{value: []byte{7}})
	sdb.SetDiscardBlockChanges(true)

	var got uint256.Int
	sdb.GetState(addr, &slot, &got)
	if got.Uint64() != 7 {
		t.Fatalf("initial value = %d, want 7", got.Uint64())
	}
	obj := sdb.stateObjects[addr]
	if _, ok := obj.originStorage[slot]; !ok {
		t.Fatal("committed value was not cached for later execution")
	}
	if _, ok := obj.blockOriginStorage[slot]; ok {
		t.Fatal("validation-only mode retained a block-level original")
	}

	sdb.SetState(addr, &slot, *uint256.NewInt(9))
	writer := new(discardChangesWriter)
	if err := sdb.FinalizeTx(&params.Rules{}, writer); err != nil {
		t.Fatal(err)
	}
	sdb.GetState(addr, &slot, &got)
	if got.Uint64() != 9 {
		t.Fatalf("post-finalize value = %d, want 9", got.Uint64())
	}
	if _, ok := obj.blockOriginStorage[slot]; ok {
		t.Fatal("FinalizeTx populated a block-level original")
	}
	if writer.storageWrites != 1 || !writer.original.IsZero() || writer.value.Uint64() != 9 {
		t.Fatalf("writer calls=%d original=%s value=%s, want 1/0/9",
			writer.storageWrites, writer.original.String(), writer.value.String())
	}
}

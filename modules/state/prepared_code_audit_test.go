package state

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

type auditCodeWriter struct {
	*NoopWriter
	code []byte
}

func (w *auditCodeWriter) UpdateAccountCode(_ types.Address, _ types.Hash, code []byte) error {
	w.code = append([]byte(nil), code...)
	return nil
}

func TestAuditStateWriteLogOwnsCodeBuffer(t *testing.T) {
	code := []byte{0x60, 1}
	l := new(StateWriteLog)
	if err := l.UpdateAccountCode(types.Address{}, types.Hash{}, code); err != nil {
		t.Fatal(err)
	}
	code[1] = 2
	w := &auditCodeWriter{}
	if err := l.Replay(w); err != nil {
		t.Fatal(err)
	}
	if len(w.code) != 2 || w.code[1] != 1 {
		t.Fatalf("recorded code was changed by its caller: %x", w.code)
	}
}

type auditArgsWriter struct {
	*NoopWriter
	orig, acct account.StateAccount
	key        types.Hash
	value      uint256.Int
}

func (w *auditArgsWriter) UpdateAccountData(_ types.Address, original, acct *account.StateAccount) error {
	w.orig, w.acct = *original, *acct
	return nil
}

func (w *auditArgsWriter) WriteAccountStorage(_ types.Address, key types.Hash, _, value uint256.Int) error {
	w.key, w.value = key, value
	return nil
}

// The other recorded arguments are held by value: an account or slot the
// caller changes after the call does not change what is replayed.
func TestAuditStateWriteLogOwnsAccountAndSlotArgs(t *testing.T) {
	orig := &account.StateAccount{Nonce: 1, CodeHash: types.Hash{1}}
	acct := &account.StateAccount{Nonce: 2, CodeHash: types.Hash{2}}
	acct.Balance.SetUint64(7)
	key := types.Hash{3}
	value := *uint256.NewInt(9)
	l := new(StateWriteLog)
	if err := l.UpdateAccountData(types.Address{}, orig, acct); err != nil {
		t.Fatal(err)
	}
	if err := l.WriteAccountStorage(types.Address{}, key, uint256.Int{}, value); err != nil {
		t.Fatal(err)
	}
	orig.Nonce, acct.Nonce = 10, 20
	acct.Balance.SetUint64(70)
	acct.CodeHash[0] = 0xff
	key[0] = 0xff
	value.SetUint64(90)
	w := &auditArgsWriter{}
	if err := l.Replay(w); err != nil {
		t.Fatal(err)
	}
	if w.orig.Nonce != 1 || w.acct.Nonce != 2 || w.acct.Balance.Uint64() != 7 || w.acct.CodeHash != (types.Hash{2}) {
		t.Fatalf("recorded account was changed by its caller: %+v %+v", w.orig, w.acct)
	}
	if w.key != (types.Hash{3}) || w.value.Uint64() != 9 {
		t.Fatalf("recorded slot was changed by its caller: %x %v", w.key, w.value.Uint64())
	}
}

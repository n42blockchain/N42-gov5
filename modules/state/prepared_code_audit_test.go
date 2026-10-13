package state

import (
	"testing"

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

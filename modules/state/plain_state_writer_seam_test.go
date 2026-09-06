package state

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

type recordingPutDel struct{ puts, dels map[string]int }

func (r *recordingPutDel) Put(table string, _ []byte, _ []byte) error { r.puts[table]++; return nil }
func (r *recordingPutDel) Delete(table string, _ []byte) error        { r.dels[table]++; return nil }

type sourceAdapter struct{}

func (sourceAdapter) LatestAccount(_ kv.Getter, _ []byte) ([]byte, error) { return nil, nil }

// TestPlainStateWriter_SkipsAccountWhenSourceInstalled: the writer stops
// touching `Account` (put and delete) for post-genesis blocks once a head-state
// source is installed, keeps writing it at genesis, and never skips `Storage`.
func TestPlainStateWriter_SkipsAccountWhenSourceInstalled(t *testing.T) {
	addr := types.BytesToAddress([]byte{1})
	acc := account.NewAccount()
	a := &acc
	a.Nonce = 1
	a.Balance = *uint256.NewInt(1)

	run := func(genesis bool) (puts, dels int) {
		rec := &recordingPutDel{puts: map[string]int{}, dels: map[string]int{}}
		w := &PlainStateWriter{db: rec, genesis: genesis}
		if err := w.UpdateAccountData(addr, nil, a); err != nil {
			t.Fatal(err)
		}
		if err := w.DeleteAccount(addr, a); err != nil {
			t.Fatal(err)
		}
		if err := w.WriteAccountStorage(addr, types.Hash{1}, *uint256.NewInt(0), *uint256.NewInt(9)); err != nil {
			t.Fatal(err)
		}
		if rec.puts[modules.Storage] != 1 {
			t.Fatalf("Storage must always be written, got %d", rec.puts[modules.Storage])
		}
		return rec.puts[modules.Account], rec.dels[modules.Account]
	}

	if p, d := run(false); p != 1 || d != 1 {
		t.Fatalf("no source: want Account put+delete, got %d/%d", p, d)
	}
	modules.SetLatestAccountSource(sourceAdapter{})
	t.Cleanup(func() { modules.SetLatestAccountSource(nil) })
	if p, d := run(false); p != 0 || d != 0 {
		t.Fatalf("source installed: Account must not be written, got %d/%d", p, d)
	}
	if p, d := run(true); p != 1 || d != 1 {
		t.Fatalf("genesis writer must still write Account, got %d/%d", p, d)
	}
}

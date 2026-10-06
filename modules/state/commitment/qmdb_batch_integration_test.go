package commitment

import (
	"bytes"
	"encoding/binary"
	"sort"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/qmdb"
)

func BenchmarkQMDBComputerSpread(b *testing.B) {
	accounts := make(map[types.Address]*account.StateAccount, 163000)
	for i := 0; i < 163000; i++ {
		var addr types.Address
		binary.BigEndian.PutUint64(addr[12:], uint64(i))
		accounts[addr] = qmAcct(1, uint64(i+1))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		rc := NewQMDBRootComputer()
		rc.EnableUndoRecording()
		b.StartTimer()
		if _, err := rc.ComputeRoot(accounts, nil); err != nil {
			b.Fatal(err)
		}
	}
}

// Compare the real root computer against eager Tree.Set/Delete, including
// undo bytes, reverts and proofs. The dirty set crosses many twig boundaries
// and mixes old-key deactivation with new appends and storage deletion.
func TestQMDBComputerBatchMatchesEagerUndo(t *testing.T) {
	rc := NewQMDBRootComputer()
	rc.EnableUndoRecording()
	eager := qmdb.New()
	for round := 0; round < 4; round++ {
		accounts := make(map[types.Address]*account.StateAccount)
		storage := make(map[types.Address]map[types.Hash]*uint256.Int)
		type operation struct {
			key   qmdb.Hash
			value []byte
		}
		var ops []operation
		for i := 0; i < 5000; i++ {
			addr := rlAddr(i + round*1000)
			acct := qmAcct(uint64(round+1), uint64(i+1))
			if round > 0 && i%11 == 0 {
				acct = nil
			}
			accounts[addr] = acct
			var value []byte
			if acct != nil {
				value = EncodeAccountValue(acct)
			}
			ops = append(ops, operation{qmdb.Hash(AccountKeyHash(addr)), value})
			if i%17 == 0 {
				slot := types.Hash{0x55}
				v := uint256.NewInt(uint64(round))
				storage[addr] = map[types.Hash]*uint256.Int{slot: v}
				var encoded []byte
				if !v.IsZero() {
					encoded = make([]byte, 32)
					v.WriteToSlice(encoded)
				}
				ops = append(ops, operation{qmdb.Hash(StorageKeyHash(addr, slot)), encoded})
			}
		}
		sort.Slice(ops, func(i, j int) bool { return bytes.Compare(ops[i].key[:], ops[j].key[:]) < 0 })
		before := eager.Root()
		var undo1, undo2 *qmdb.BlockUndo
		for replay := 0; replay < 2; replay++ {
			eager.StartUndoRecording()
			for _, op := range ops {
				if op.value == nil {
					eager.Delete(op.key)
				} else {
					eager.Set(op.key, op.value)
				}
			}
			undo1 = eager.StopUndoRecording()
			root, err := rc.ComputeRoot(accounts, storage)
			if err != nil {
				t.Fatal(err)
			}
			undo2 = rc.TakeUndo()
			if root != types.Hash(eager.Root()) || !bytes.Equal(undo1.Marshal(), undo2.Marshal()) {
				t.Fatalf("round %d replay %d: root or undo differs", round, replay)
			}
			for _, op := range ops {
				if op.value == nil {
					continue
				}
				proof, ok := rc.Tree().GetProof(op.key)
				if !ok || !qmdb.VerifyProof(qmdb.Hash(root), proof) {
					t.Fatal("invalid batched proof")
				}
				break
			}
			if replay == 0 {
				if err := eager.ApplyUndo(undo1); err != nil {
					t.Fatal(err)
				}
				if err := rc.Tree().ApplyUndo(undo2); err != nil {
					t.Fatal(err)
				}
				if eager.Root() != before || rc.Tree().Root() != before {
					t.Fatal("revert did not restore pre-block root")
				}
			}
		}
	}
}

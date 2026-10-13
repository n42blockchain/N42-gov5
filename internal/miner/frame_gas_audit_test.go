package miner

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame/store"
)

func TestAuditPickFramesCannotWrapGasBudget(t *testing.T) {
	st := store.New(1 << 20)
	for i, gas := range []uint64{21000, ^uint64(0)} {
		if !st.PutEntry(types.Hash{31: byte(i + 1)}, store.Entry{Raw: []byte{1}, Gas: gas, Txs: 1}) {
			t.Fatal("fixture admission failed")
		}
	}
	p := pickFrames(st, nil, types.Address{}, 42000, 10)
	if len(p.hashes) != 1 || p.gas != 21000 {
		t.Fatalf("over-budget frame selected: frames=%d gas=%d", len(p.hashes), p.gas)
	}
}

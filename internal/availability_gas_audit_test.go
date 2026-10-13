package internal

import (
	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"strings"
	"testing"
)

func TestAuditFrameAvailabilityRejectsGasOverflow(t *testing.T) {
	defer block.SetDeferredDepthForTest(2, 0)()
	c := newAvailChain(t)
	key := frametest.Keys(t, 1, 217)[0]
	addr := types.Address{19: 1}
	var raws [][]byte
	for _, gas := range []uint64{1, ^uint64(0), 21000} {
		tx := transaction.NewTransaction(0, addr, &addr, uint256.NewInt(0), gas, uint256.NewInt(1), nil)
		raw, err := tx.EthEncoded()
		if err != nil {
			t.Fatal(err)
		}
		raws = append(raws, raw)
	}
	raw, hash, err := frame.Encode(key, 0, frame.TxTypeEthereum, raws)
	if err != nil {
		t.Fatal(err)
	}
	c.st.Put(hash, raw)
	if err := c.bc.ChainDB.Update(c.bc.ctx, func(tx kv.RwTx) error { rawdb.WriteHeader(tx, c.p); return nil }); err != nil {
		t.Fatal(err)
	}
	for _, availability := range []bool{false, true} {
		restore := block.SetVoteOnAvailabilityForTest(availability)
		blk := c.child(gResult(c.g), []types.Hash{hash})
		checked, retry, err := c.bc.CheckDeferredBlock(blk)
		restore()
		if !checked || retry || err == nil || !strings.Contains(err.Error(), "gas limit") {
			t.Fatalf("availability=%v checked=%v retry=%v err=%v", availability, checked, retry, err)
		}
	}
}

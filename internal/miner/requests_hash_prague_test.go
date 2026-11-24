package miner

import (
	"crypto/sha256"
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/params"
)

// A chain on Prague rules through PectraTime that sets PragueTime later: the
// miner seals no RequestsHash before PragueTime and the EIP-7685 hash of the
// block's requests from it, the mined blocks import, and import refuses a
// header from PragueTime on that carries another value or none.
func TestMinerSealsRequestsHashFromPragueTime(t *testing.T) {
	const prague = 2_000
	f := mnTNewChainFixtureWithEngine(t, &mnTFakerEngine{}, func(cfg *params.ChainConfig, alloc conf.GenesisAlloc) {
		cfg.ShanghaiBlock = big.NewInt(0)
		cfg.CancunBlock = big.NewInt(0)
		cfg.PectraTime = big.NewInt(0)
		cfg.PragueTime = big.NewInt(prague)
		for _, d := range internal.PragueSystemContractDeployments() {
			alloc[d.Address] = d.Account
		}
	})
	noRequests := types.Hash(sha256.Sum256(nil))

	var last *task
	for i, at := range []uint64{1_000, prague, 3_000, 4_000} {
		pool := &mnTStubTxsPool{pending: mnTByAddr(f.Senders[0], f.ruleZeroTipTransfer(t, 0, f.Senders[1]))}
		w := mnTBareWorker(t, f, pool)
		w.coinbase = f.Coinbase
		w.resubmitAdjustCh = make(chan *intervalAdjust, 10)
		atomic.StoreInt32(&w.running, 1)
		require.NoError(t, w.commitWork(nil, false, int64(at), types.Hash{}, false, time.Time{}))
		select {
		case last = <-w.taskCh:
		default:
			t.Fatal("commitWork did not send a task")
		}
		got := last.block.Header().(*block.Header).RequestsHash
		if at < prague {
			require.Nil(t, got, "block %d (time %d): no requests hash before the Prague time", i+1, at)
		} else {
			require.NotNil(t, got, "block %d (time %d): requests hash from the Prague time", i+1, at)
			require.Equal(t, noRequests, *got, "block %d: the EIP-7685 hash of no requests", i+1)
		}
		if at == 4_000 {
			break // the last block is the one tampered with below
		}
		_, err := f.Chain.InsertChain([]block.IBlock{last.block})
		require.NoError(t, err, "block %d mined by the miner imports", i+1)
	}

	emptyRoot := hash.EmptyRootHash
	for name, rh := range map[string]*types.Hash{"the empty trie root": &emptyRoot, "no hash": nil} {
		header := block.CopyHeader(last.block.Header().(*block.Header))
		header.RequestsHash = rh
		tampered := block.NewBlockFromReceipt(header, last.block.Transactions(), nil, last.receipts, nil)
		_, err := f.Chain.InsertChain([]block.IBlock{tampered})
		require.ErrorContains(t, err, "requests hash mismatch", "a header with %s", name)
	}
}

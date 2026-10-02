package internal

import (
	"context"
	"os"
	"sync"
	"testing"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// resetBlockCachePrimeSwitch clears the sync.Once-memoised flag so a test can
// flip N42_BLOCK_CACHE_PRIME and have it take effect within the same process.
func resetBlockCachePrimeSwitch(t *testing.T, val string) {
	t.Helper()
	if val == "" {
		t.Setenv("N42_BLOCK_CACHE_PRIME", "")
		os.Unsetenv("N42_BLOCK_CACHE_PRIME")
	} else {
		t.Setenv("N42_BLOCK_CACHE_PRIME", val)
	}
	blockCachePrimeOnce = sync.Once{}
	blockCachePrimeOn = false
}

// TestCommitToCanonicalPrimesBlockCacheWhenEnabled covers S64
// (docs/QS_QUEUE.md): CommitToCanonicalWith decodes the committed block from
// MDBX on a cache miss, and with N42_BLOCK_CACHE_PRIME=1 that decoded
// instance must be added to bc.blockCache so GetBlock(hash) on cache afterward
// returns it without touching the database. With the switch unset, the cache
// must stay empty -- today's behaviour, byte for byte.
func TestCommitToCanonicalPrimesBlockCacheWhenEnabled(t *testing.T) {
	for _, tc := range []struct {
		name    string
		envVal  string
		wantLen int
	}{
		{"unset preserves today's behaviour", "", 0},
		{"enabled primes the cache", "1", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetBlockCachePrimeSwitch(t, tc.envVal)

			db := newRealignTestDB(t)
			ctx := context.Background()
			parent := block.NewBlock(&block.Header{
				Number:     uint256.NewInt(8),
				Difficulty: uint256.NewInt(1),
				Root:       types.Hash{0x08},
			}, nil).(*block.Block)
			from := types.HexToAddress("0x100")
			to := types.HexToAddress("0x200")
			txn := transaction.NewTransaction(0, from, &to, uint256.NewInt(1), 21000, uint256.NewInt(1), nil)
			child := block.NewBlock(&block.Header{
				Number:     uint256.NewInt(9),
				ParentHash: parent.Hash(),
				Difficulty: uint256.NewInt(1),
				Root:       types.Hash{0x09},
			}, []*transaction.Transaction{txn}).(*block.Block)

			if err := db.Update(ctx, func(tx kv.RwTx) error {
				if err := rawdb.WriteBlock(tx, parent); err != nil {
					return err
				}
				if err := rawdb.WriteBlock(tx, child); err != nil {
					return err
				}
				if err := rawdb.WriteCanonicalHash(tx, parent.Hash(), 8); err != nil {
					return err
				}
				rawdb.WriteHeadBlockHash(tx, parent.Hash())
				return rawdb.WriteHeadHeaderHash(tx, parent.Hash())
			}); err != nil {
				t.Fatal(err)
			}

			cache, _ := lru.New[types.Hash, *block.Block](2)
			bc := &BlockChain{ChainDB: db, ctx: ctx, blockCache: cache}
			bc.currentBlock.Store(parent)

			// The block is NOT pre-seeded into the cache: this forces
			// CommitToCanonicalWith's cache-miss decode path, the one S64 primes.
			if err := bc.CommitToCanonical(child.Hash()); err != nil {
				t.Fatal(err)
			}

			if got := cache.Len(); got != tc.wantLen {
				t.Fatalf("blockCache.Len() = %d, want %d", got, tc.wantLen)
			}
			if tc.wantLen > 0 {
				cached, ok := cache.Get(child.Hash())
				if !ok || cached == nil {
					t.Fatal("expected committed block primed into blockCache")
				}
				if cached.Hash() != child.Hash() {
					t.Fatalf("cached block hash = %s, want %s", cached.Hash(), child.Hash())
				}
				// GetBlock must now serve this hash straight from the cache,
				// without a DB round trip -- the readers S64 targets
				// (sync writeBodiesRangeToStream, api getBlockByNumber, miner
				// GetBlocksFromHash) all resolve through GetBlock(hash, number).
				got := bc.GetBlock(child.Hash(), 9)
				if got == nil || got.Hash() != child.Hash() {
					t.Fatalf("GetBlock after prime = %v, want block %s", got, child.Hash())
				}
			}
		})
	}
}

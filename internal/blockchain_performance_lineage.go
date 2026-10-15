package internal

import (
	"context"
	"errors"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

var ErrPerformanceParentNotHead = errors.New("performance import parent is not actual applied head; retry")

func (bc *BlockChain) performanceAppliedHead(tx kv.Tx) (uint64, types.Hash, error) {
	if bc.chainConfig == nil || bc.chainConfig.Performance == nil || !bc.qmdbEnabled || bc.chainConfig.DeferredExecutionTime != nil {
		return 0, types.Hash{}, errors.New("performance lineage requires independent QMDB chain")
	}
	number, hash, ok, err := rawdb.ReadQMDBApplied(tx)
	if err != nil {
		return 0, types.Hash{}, err
	}
	if !ok {
		genesis := bc.GenesisBlock()
		if genesis == nil || rawdb.ReadHeadBlockHash(tx) != genesis.Hash() || bc.qmdbRootComputer == nil || bc.qmdbRootComputer.Root() != genesis.StateRoot() {
			return 0, types.Hash{}, errors.New("performance applied marker missing above genesis")
		}
		return 0, genesis.Hash(), nil
	}
	return number, types.Hash(hash), nil
}

// InsertPerformanceExtension never queues competing siblings for a later
// authorized branch switch. Its actual-parent check and import share bc.lock.
// Historical durable retries are handled by the service before this method.
func (bc *BlockChain) InsertPerformanceExtension(ctx context.Context, b *block.Block) (int, error) {
	bc.lock.Lock()
	defer bc.lock.Unlock()
	if b == nil {
		return 0, errors.New("performance extension block missing")
	}
	body, bodyOK := b.Body().(*block.Body)
	if !bodyOK || body == nil || body.IsFrameBody() {
		return 0, errors.New("performance extension body invalid")
	}
	header, ok := b.Header().(*block.Header)
	if !ok || header == nil || header.Number == nil || !header.Number.IsUint64() || header.Number.IsZero() {
		return 0, errors.New("performance extension header/body invalid")
	}
	if err := bc.joinLagWrite(); err != nil {
		return 0, err
	}
	if err := bc.ChainDB.View(ctx, func(tx kv.Tx) error {
		number, hash, err := bc.performanceAppliedHead(tx)
		if err != nil {
			return err
		}
		if number == ^uint64(0) || b.Number64().Uint64() != number+1 || b.ParentHash() != hash {
			return ErrPerformanceParentNotHead
		}
		return nil
	}); err != nil {
		return 0, err
	}
	// Even a header/body error that the generic importer classifies as
	// retryable cannot leave this RPC's payload in the authorized future
	// queue. Clients retry through this exact-parent gate instead.
	defer bc.futureBlocks.Remove(b.Hash())
	return bc.insertChain([]block.IBlock{b}, false)
}

// PerformanceAppliedChild returns the already executed child on the single
// owner lineage. It cannot rewind state or select a competing fork. Returning
// an older sealed header for a later proposal view does not sign a new header.
func (bc *BlockChain) PerformanceAppliedChild(ctx context.Context, parent types.Hash, number uint64, view hotstuff.ViewNumber) (*block.Block, error) {
	bc.lock.Lock()
	defer bc.lock.Unlock()
	if number == 0 || view == 0 || parent == (types.Hash{}) {
		return nil, errors.New("performance child requires parent, height and view")
	}
	engine, ok := bc.engine.(*hotstuff.HotStuff)
	if !ok {
		return nil, errors.New("performance child requires public HotStuff verifier")
	}
	if err := bc.joinLagWrite(); err != nil {
		return nil, err
	}
	var child *block.Block
	err := bc.ChainDB.View(ctx, func(tx kv.Tx) error {
		height, hash, err := bc.performanceAppliedHead(tx)
		if err != nil {
			return err
		}
		if height < number {
			if height == number-1 && hash == parent {
				return nil
			}
			return errors.New("performance child parent unavailable on applied lineage")
		}
		if height-number >= 256 {
			return errors.New("performance child exceeds retained lineage window")
		}
		for height > number {
			header := rawdb.ReadHeader(tx, hash, height)
			if header == nil {
				return errors.New("performance applied ancestry missing")
			}
			hash, height = header.ParentHash, height-1
		}
		child, err = rawdb.ReadBlockByHash(tx, hash)
		if err != nil {
			return err
		}
		if child == nil || child.ParentHash() != parent || child.Number64().Uint64() != number {
			return errors.New("performance requested parent conflicts with applied lineage")
		}
		return nil
	})
	if err != nil || child == nil {
		return child, err
	}
	header := child.Header().(*block.Header)
	headerView, err := hotstuff.ExtractViewFromExtra(header.Extra)
	if err != nil || hotstuff.ViewNumber(headerView) > view {
		return nil, errors.New("performance applied child belongs to future proposal view")
	}
	if err := engine.VerifyPerformanceSealedHeader(bc, header); err != nil {
		return nil, err
	}
	return child, nil
}

package initialsync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/paulbellamy/ratecounter"

	"github.com/n42blockchain/N42/common/block"
	astLog "github.com/n42blockchain/N42/log"
)

const (
	// counterSeconds is an interval over which an average rate will be calculated.
	counterSeconds = 20

	// maxNoProgressBatches bounds how many consecutive orphan (parent-missing)
	// batches may be processed without the head advancing before initial sync
	// aborts, so one bad peer serving an orphan range cannot spin it forever.
	maxNoProgressBatches = 32
)

// batchBlockReceiverFn defines batch receiving function.
type batchBlockReceiverFn func(chain []block.IBlock) (int, error)

// Round Robin sync looks at the latest peer statuses and syncs up to the highest known epoch.
//
// Step 1 - Sync to finalized epoch.
// Sync with peers having the majority on best finalized epoch greater than node's head state.
func (s *Service) roundRobinSync(highestExpectedBlockNr *uint256.Int) error {
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()

	s.counter = ratecounter.NewRateCounter(counterSeconds * time.Second)
	s.highestExpectedBlockNr = highestExpectedBlockNr.Clone()
	return s.syncToFinalizedBlockNr(ctx, highestExpectedBlockNr)
}

// syncToFinalizedBlockNr sync from head to best known finalized epoch.
func (s *Service) syncToFinalizedBlockNr(ctx context.Context, highestExpectedBlockNr *uint256.Int) error {
	currentBlock, err := requireCurrentBlockNumber(s.cfg.Chain, "current block number unavailable")
	if err != nil {
		return err
	}
	if currentBlock.Cmp(highestExpectedBlockNr) >= 0 {
		log.Debug("Already synced to finalized block number")
		return nil
	}
	queue := newBlocksQueue(ctx, &blocksQueueConfig{
		p2p:                    s.cfg.P2P,
		chain:                  s.cfg.Chain,
		highestExpectedBlockNr: highestExpectedBlockNr,
		mode:                   modeStopOnFinalizedEpoch,
	})
	if err := queue.start(); err != nil {
		return err
	}

	// Guard against an orphan-range spin: a peer serving blocks whose parent we
	// never hold makes every batch fail with errParentDoesNotExist while the
	// head never advances, and the queue keeps re-fetching the same range
	// forever (observed: the same orphan reprocessed 1400+ times). Bound the
	// consecutive no-progress batches, then abort so the outer sync can
	// restart/fall back rather than hang.
	guard := noProgressGuard{lastHead: currentBlockNumberOrZero(s.cfg.Chain)}
	for data := range queue.fetchedData {
		if ctx.Err() != nil {
			continue // drain channel but skip processing
		}
		currentBlock, err := requireCurrentBlockNumber(s.cfg.Chain, "current block number unavailable")
		if err != nil {
			log.Warn("Skipping fetched data with unavailable current block number", "err", err)
			continue
		}
		procErr := s.processFetchedData(ctx, currentBlock, data)

		if guard.observe(currentBlockNumberOrZero(s.cfg.Chain), errors.Is(procErr, errParentDoesNotExist)) {
			queue.stop()
			return fmt.Errorf("initial sync stalled: %d consecutive orphan batches at head %d without progress: %w",
				guard.count, guard.lastHead, errParentDoesNotExist)
		}
	}

	if ctx.Err() != nil {
		queue.stop()
		return ctx.Err()
	}

	log.Info("Synced to finalized block number - now syncing blocks up to current head", "syncedBlockNr", currentBlockNumberOrZero(s.cfg.Chain), "highestExpectedBlockNr", highestExpectedBlockNr.Uint64())
	if err := queue.stop(); err != nil {
		log.Debug("Error stopping queue", "err", err)
	}

	return nil
}

// noProgressGuard bounds how many consecutive parent-missing (orphan) batches
// may be processed without the head advancing before the sync aborts.
type noProgressGuard struct {
	lastHead uint64
	count    int
}

// observe records one processed batch: head is the chain head after processing,
// parentMissing is whether the batch failed with errParentDoesNotExist. It
// returns true when the sync should abort (too many consecutive orphan batches
// with no progress). Any head advance resets the counter.
func (g *noProgressGuard) observe(head uint64, parentMissing bool) bool {
	if head > g.lastHead {
		g.lastHead = head
		g.count = 0
		return false
	}
	if parentMissing {
		g.count++
		return g.count >= maxNoProgressBatches
	}
	return false
}

// processFetchedData processes data received from queue. It returns the
// processing error (nil on success) so the caller can detect a batch that could
// not be applied — notably errParentDoesNotExist, which repeats every fetch of
// an orphan range and, unguarded, spins the sync loop forever.
func (s *Service) processFetchedData(ctx context.Context, startBlockNr *uint256.Int, data *blocksQueueFetchedData) error {
	defer s.updatePeerScorerStats(data.pid, startBlockNr)

	if ctx.Err() != nil {
		return ctx.Err()
	}

	if _, err := s.processBatchedBlocks(ctx, data.blocks, s.cfg.Chain.InsertChain); err != nil {
		if ctx.Err() != nil {
			return ctx.Err() // suppress errors during shutdown
		}
		log.Warn("Skipped processing batched blocks", "err", err)
		return err
	}
	return nil
}

func (s *Service) processBatchedBlocks(ctx context.Context, blks []*block.Block, bFunc batchBlockReceiverFn) (int, error) {
	if len(blks) == 0 {
		return 0, errors.New("0 blocks provided into method")
	}

	blocks := make([]block.IBlock, 0, len(blks))
	for _, blk := range blks {
		blocks = append(blocks, blk)
	}

	blocks, err := s.skipProcessedBlocks(ctx, blocks)
	if err != nil {
		return 0, err
	}
	if len(blocks) == 0 {
		return 0, nil
	}

	firstBlock := blocks[0]
	blockNumber, err := requireBlockNumber(firstBlock, "block number unavailable")
	if err != nil {
		return 0, err
	}
	blockNum := blockNumber.Uint64()
	if blockNum > 0 && !s.cfg.Chain.HasBlock(firstBlock.ParentHash(), blockNum-1) {
		return 0, fmt.Errorf("%w: %s (in processBatchedBlocks, Number=%d)", errParentDoesNotExist, firstBlock.ParentHash(), blockNum)
	}

	if ctx.Err() != nil {
		return 0, ctx.Err()
	}

	s.logBatchSyncStatus(blks)

	return bFunc(blocks)
}

// skipProcessedBlocks filters out blocks that have already been processed by the chain.
// It removes leading blocks whose number is at or below the chain's current head,
// returning the remaining unprocessed blocks. Returns nil if all blocks
// have already been processed.
func (s *Service) skipProcessedBlocks(ctx context.Context, blocks []block.IBlock) ([]block.IBlock, error) {
	headNumber, err := requireCurrentBlockNumber(s.cfg.Chain, "current block number unavailable")
	if err != nil {
		return nil, err
	}
	headNum := headNumber.Uint64()

	skip := 0
	for skip < len(blocks) {
		blockNumber, err := requireBlockNumber(blocks[skip], "block number unavailable")
		if err != nil {
			return nil, err
		}
		if headNum < blockNumber.Uint64() {
			break
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		skip++
	}

	if skip == 0 {
		return blocks, nil
	}
	if skip == len(blocks) {
		lastBlockNumber, err := requireBlockNumber(blocks[len(blocks)-1], "block number unavailable")
		if err != nil {
			return nil, err
		}
		log.Debug("All blocks in batch already processed, skipping",
			"currentBlock", headNum,
			"batchBlock", lastBlockNumber.Uint64())
		return nil, nil
	}
	return blocks[skip:], nil
}

// updatePeerScorerStats adjusts monitored metrics for a peer.
func (s *Service) updatePeerScorerStats(pid peer.ID, startBlockNr *uint256.Int) {
	if pid == "" {
		return
	}
	headNumber, err := requireCurrentBlockNumber(s.cfg.Chain, "current block number unavailable")
	if err != nil {
		return
	}
	headNum := headNumber.Uint64()
	startNum := startBlockNr.Uint64()
	if startNum >= headNum {
		return
	}
	scorer := s.cfg.P2P.Peers().Scorers().BlockProviderScorer()
	scorer.IncrementProcessedBlocks(pid, headNum-startNum)
}

// logBatchSyncStatus increments the block processing counter and logs sync progress.
// Throttled to log every 5 seconds or every 10000 blocks to reduce log spam.
func (s *Service) logBatchSyncStatus(blks []*block.Block) {
	s.counter.Incr(int64(len(blks)))

	lastBlock := blks[len(blks)-1]
	currentBlockNum := lastBlock.Number64().Uint64()

	if s.syncStartTime.IsZero() {
		s.syncStartTime = time.Now()
		s.syncStartBlock = currentBlockNum
		s.lastLogTime = time.Now()
		s.lastLogBlock = currentBlockNum
	}

	now := time.Now()
	blocksSinceLog := currentBlockNum - s.lastLogBlock
	timeSinceLog := now.Sub(s.lastLogTime)

	if timeSinceLog < 5*time.Second && blocksSinceLog < 10000 {
		return
	}

	s.lastLogTime = now
	s.lastLogBlock = currentBlockNum

	rate := float64(s.counter.Rate()) / counterSeconds
	if rate == 0 {
		rate = 1
	}

	targetNum := s.highestExpectedBlockNr.Uint64()
	remaining := targetNum - currentBlockNum

	eta := "calculating..."
	if rate > 0 && remaining > 0 {
		etaSecs := float64(remaining) / rate
		eta = formatDuration(time.Duration(etaSecs) * time.Second)
	}

	astLog.PrintProgressBar("Syncing", currentBlockNum, targetNum, rate, eta, len(s.cfg.P2P.Peers().Connected()))
}

// formatDuration formats a duration in a human-readable compact format.
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	if d < 24*time.Hour {
		hours := int(d.Hours())
		mins := int(d.Minutes()) % 60
		return fmt.Sprintf("%dh%dm", hours, mins)
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	return fmt.Sprintf("%dd%dh", days, hours)
}

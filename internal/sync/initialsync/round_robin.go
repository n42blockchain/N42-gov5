package initialsync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/paulbellamy/ratecounter"

	"github.com/n42blockchain/N42/api/protocol/types_pb"
	"github.com/n42blockchain/N42/common/block"
	astLog "github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/utils"
)

const (
	// counterSeconds is an interval over which an average rate will be calculated.
	counterSeconds = 20
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
	if s.cfg.Chain.CurrentBlock().Number64().Cmp(highestExpectedBlockNr) >= 0 {
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

	for data := range queue.fetchedData {
		if ctx.Err() != nil {
			continue // drain channel but skip processing
		}
		s.processFetchedData(ctx, s.cfg.Chain.CurrentBlock().Number64(), data)
	}

	if ctx.Err() != nil {
		queue.stop()
		return ctx.Err()
	}

	log.Info("Synced to finalized block number - now syncing blocks up to current head", "syncedBlockNr", s.cfg.Chain.CurrentBlock().Number64().Uint64(), "highestExpectedBlockNr", highestExpectedBlockNr.Uint64())
	if err := queue.stop(); err != nil {
		log.Debug("Error stopping queue", "err", err)
	}

	return nil
}

// processFetchedData processes data received from queue.
func (s *Service) processFetchedData(ctx context.Context, startBlockNr *uint256.Int, data *blocksQueueFetchedData) {
	defer s.updatePeerScorerStats(data.pid, startBlockNr)

	if ctx.Err() != nil {
		return
	}

	if _, err := s.processBatchedBlocks(ctx, data.blocks, s.cfg.Chain.InsertChain); err != nil {
		if ctx.Err() != nil {
			return // suppress errors during shutdown
		}
		log.Warn("Skipped processing batched blocks", "err", err)
	}
}

func (s *Service) processBatchedBlocks(ctx context.Context, blks []*types_pb.Block, bFunc batchBlockReceiverFn) (int, error) {
	if len(blks) == 0 {
		return 0, errors.New("0 blocks provided into method")
	}

	blocks := make([]block.IBlock, 0, len(blks))
	for _, blk := range blks {
		block := new(block.Block)
		if err := block.FromProtoMessage(blk); err != nil {
			return 0, err
		}
		blocks = append(blocks, block)
	}

	blocks, err := s.skipProcessedBlocks(ctx, blocks)
	if err != nil {
		return 0, err
	}
	if len(blocks) == 0 {
		return 0, nil
	}

	firstBlock := blocks[0]
	blockNum := firstBlock.Number64().Uint64()
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
	headNum := s.cfg.Chain.CurrentBlock().Number64().Uint64()

	skip := 0
	for skip < len(blocks) && headNum >= blocks[skip].Number64().Uint64() {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		skip++
	}

	if skip == 0 {
		return blocks, nil
	}
	if skip == len(blocks) {
		log.Debug("All blocks in batch already processed, skipping",
			"currentBlock", headNum,
			"batchBlock", blocks[len(blocks)-1].Number64().Uint64())
		return nil, nil
	}
	return blocks[skip:], nil
}

// updatePeerScorerStats adjusts monitored metrics for a peer.
func (s *Service) updatePeerScorerStats(pid peer.ID, startBlockNr *uint256.Int) {
	if pid == "" {
		return
	}
	headNum := s.cfg.Chain.CurrentBlock().Number64().Uint64()
	startNum := startBlockNr.Uint64()
	if startNum >= headNum {
		return
	}
	scorer := s.cfg.P2P.Peers().Scorers().BlockProviderScorer()
	scorer.IncrementProcessedBlocks(pid, headNum-startNum)
}

// logBatchSyncStatus increments the block processing counter and logs sync progress.
// Throttled to log every 5 seconds or every 10000 blocks to reduce log spam.
func (s *Service) logBatchSyncStatus(blks []*types_pb.Block) {
	s.counter.Incr(int64(len(blks)))

	lastBlock := blks[len(blks)-1]
	currentBlockNum := utils.ConvertH256ToUint256Int(lastBlock.Header.Number).Uint64()

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

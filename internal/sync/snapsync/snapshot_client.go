// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package snapsync

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"golang.org/x/sync/semaphore"

	"github.com/n42blockchain/N42/proto/sync_pb"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/snapshot"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

// SnapshotManager orchestrates snapshot-based state downloads. Unlike the
// original Manager (which reads current PlainState from peers), this manager:
// 1. Discovers available snapshots from peers
// 2. Downloads state at a chosen snapshot height (via WalkAsOf on the server)
// 3. Receives zstd-compressed batches and writes them to local DB
// 4. After state download, the caller replays blocks from snapshot→HEAD
type SnapshotManager struct {
	cfg *conf.SnapSyncConfig
	p2p p2p.P2P
	db  kv.RwDB

	snapshotBlock uint64 // selected snapshot height

	// Task queues (protected by mu).
	accountTasks []*RangeTask
	storageTasks []*RangeTask
	codeTasks    []*RangeTask

	// Progress counters (protected by mu).
	accountsDone  uint64
	storagesDone  uint64
	codesDone     uint64
	bytesReceived uint64

	mu     sync.Mutex
	wg     sync.WaitGroup
	dbSem  *semaphore.Weighted
	scorer *peerScorer
}

// NewSnapshotManager creates a snapshot-based download manager.
func NewSnapshotManager(cfg *conf.SnapSyncConfig, p2pProvider p2p.P2P, db kv.RwDB) *SnapshotManager {
	return &SnapshotManager{
		cfg:    cfg,
		p2p:    p2pProvider,
		db:     db,
		dbSem:  semaphore.NewWeighted(maxConcurrentDBWrites),
		scorer: newPeerScorer(),
	}
}

// DiscoverSnapshot queries connected peers for their available snapshots and
// selects the highest snapshot height that at least minPeers can serve.
func (sm *SnapshotManager) DiscoverSnapshot(ctx context.Context, minPeers int) (uint64, error) {
	if minPeers <= 0 {
		minPeers = 2
	}

	peers := sm.p2p.Peers()
	if peers == nil {
		return 0, fmt.Errorf("no peer info available")
	}
	connected := peers.Connected()
	if len(connected) < minPeers {
		return 0, fmt.Errorf("not enough peers: have %d, need %d", len(connected), minPeers)
	}

	// Query each peer for snapshot info.
	type peerResult struct {
		pid     peer.ID
		heights []uint64
	}
	results := make(chan peerResult, len(connected))

	var wg sync.WaitGroup
	for _, pid := range connected {
		wg.Add(1)
		go func(pid peer.ID) {
			defer wg.Done()
			info, err := sendGetSnapshotInfo(ctx, sm.p2p, pid)
			if err != nil {
				log.Debug("Failed to get snapshot info from peer", "peer", pid, "err", err)
				results <- peerResult{pid: pid}
				return
			}
			var heights []uint64
			for _, s := range info.Snapshots {
				heights = append(heights, s.BlockNumber)
			}
			results <- peerResult{pid: pid, heights: heights}
		}(pid)
	}

	// Close results channel after all goroutines complete to prevent goroutine leaks.
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results.
	heightVotes := make(map[uint64]int)
	for r := range results {
		for _, h := range r.heights {
			heightVotes[h]++
		}
	}

	// Find the highest snapshot that enough peers can serve.
	type candidate struct {
		height uint64
		votes  int
	}
	var candidates []candidate
	for h, v := range heightVotes {
		if v >= minPeers {
			candidates = append(candidates, candidate{h, v})
		}
	}
	if len(candidates) == 0 {
		return 0, fmt.Errorf("no snapshot available from at least %d peers", minPeers)
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].height > candidates[j].height
	})

	best := candidates[0]
	log.Info("Snapshot discovered",
		"height", best.height,
		"peers", best.votes,
		"candidates", len(candidates),
	)
	return best.height, nil
}

// Run downloads state at the given snapshot height and blocks until completion.
func (sm *SnapshotManager) Run(ctx context.Context, snapshotBlock uint64) error {
	sm.snapshotBlock = snapshotBlock

	// Initialize task: full account range.
	sm.mu.Lock()
	sm.accountTasks = []*RangeTask{{
		Kind:     TaskAccount,
		StartKey: nil,
		EndKey:   nil,
	}}
	sm.storageTasks = nil
	sm.codeTasks = nil
	sm.mu.Unlock()

	// Mark as running.
	if err := sm.db.Update(ctx, func(tx kv.RwTx) error {
		if err := SavePivotBlock(tx, snapshotBlock); err != nil {
			return err
		}
		return SaveState(tx, StateRunning)
	}); err != nil {
		return err
	}

	log.Info("Snapshot sync download started", "snapshotBlock", snapshotBlock)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	progressTicker := time.NewTicker(30 * time.Second)
	defer progressTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			sm.wg.Wait()
			return ctx.Err()
		case <-ticker.C:
			sm.processTimeouts()
		case <-progressTicker.C:
			sm.mu.Lock()
			log.Info("Snapshot sync progress",
				"accounts", sm.accountsDone,
				"storage", sm.storagesDone,
				"codes", sm.codesDone,
				"bytes", sm.bytesReceived,
				"pending", len(sm.accountTasks)+len(sm.storageTasks)+len(sm.codeTasks),
			)
			sm.mu.Unlock()
		default:
		}

		sm.mu.Lock()
		allDone := len(sm.accountTasks) == 0 && len(sm.storageTasks) == 0 && len(sm.codeTasks) == 0
		sm.mu.Unlock()
		if allDone {
			sm.wg.Wait()
			sm.mu.Lock()
			allDone = len(sm.accountTasks) == 0 && len(sm.storageTasks) == 0 && len(sm.codeTasks) == 0
			sm.mu.Unlock()
			if allDone {
				break
			}
			continue
		}

		task := sm.pickTask()
		if task == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		pid := sm.pickPeer()
		if pid == "" {
			task.Reset()
			time.Sleep(500 * time.Millisecond)
			continue
		}

		task.Assigned = pid
		task.AssignedAt = time.Now()

		sm.wg.Add(1)
		go func(t *RangeTask) {
			defer sm.wg.Done()
			sm.executeTask(ctx, t)
		}(task)
	}

	// Mark completed.
	if err := sm.db.Update(ctx, func(tx kv.RwTx) error {
		return SaveState(tx, StateCompleted)
	}); err != nil {
		return err
	}

	sm.mu.Lock()
	log.Info("Snapshot sync download completed",
		"snapshotBlock", snapshotBlock,
		"accounts", sm.accountsDone,
		"storage", sm.storagesDone,
		"codes", sm.codesDone,
		"bytes", sm.bytesReceived,
	)
	sm.mu.Unlock()
	return nil
}

func (sm *SnapshotManager) pickTask() *RangeTask {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for _, task := range sm.accountTasks {
		if !task.IsAssigned() && task.CanRetry() {
			return task
		}
	}
	for _, task := range sm.storageTasks {
		if !task.IsAssigned() && task.CanRetry() {
			return task
		}
	}
	for _, task := range sm.codeTasks {
		if !task.IsAssigned() && task.CanRetry() {
			return task
		}
	}
	return nil
}

func (sm *SnapshotManager) pickPeer() peer.ID {
	peers := sm.p2p.Peers()
	if peers == nil {
		return ""
	}
	connected := peers.Connected()
	if len(connected) == 0 {
		return ""
	}
	return sm.scorer.bestPeer(connected)
}

func (sm *SnapshotManager) processTimeouts() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	resetTimedOut := func(tasks []*RangeTask) {
		for _, task := range tasks {
			if task.IsTimedOut() {
				task.Retries++
				task.Reset()
				snapTaskTimeouts.Inc()
			}
		}
	}
	resetTimedOut(sm.accountTasks)
	resetTimedOut(sm.storageTasks)
	resetTimedOut(sm.codeTasks)
}

func (sm *SnapshotManager) executeTask(ctx context.Context, task *RangeTask) {
	switch task.Kind {
	case TaskAccount:
		sm.executeSnapshotAccountTask(ctx, task)
	case TaskStorage:
		sm.executeSnapshotStorageTask(ctx, task)
	case TaskCode:
		sm.executeCodeTaskFromPeer(ctx, task)
	}
}

func (sm *SnapshotManager) failTask(task *RangeTask) {
	snapTaskErrors.Inc()
	sm.scorer.recordFailure(task.Assigned)
	sm.mu.Lock()
	task.Retries++
	task.Reset()
	sm.mu.Unlock()
}

// executeSnapshotAccountTask fetches accounts at the snapshot height via
// the compressed batch protocol.
func (sm *SnapshotManager) executeSnapshotAccountTask(ctx context.Context, task *RangeTask) {
	start := time.Now()

	req := &sync_pb.GetSnapshotAccountRangeRequest{
		SnapshotBlock: sm.snapshotBlock,
		Start:         task.StartKey,
		End:           task.EndKey,
		MaxBytes:      sm.cfg.MaxBytesPerReq,
	}

	resp, err := sendGetSnapshotAccountRange(ctx, sm.p2p, task.Assigned, req)
	if err != nil {
		log.Debug("Snapshot account range request failed", "peer", task.Assigned, "err", err)
		sm.failTask(task)
		return
	}

	// Decompress batch.
	entries, err := snapshot.DecodeBatch(resp.Data)
	if err != nil {
		log.Warn("Failed to decode compressed batch", "peer", task.Assigned, "err", err)
		sm.failTask(task)
		return
	}

	// Validate batch integrity.
	if err := validateAccountBatch(resp, entries, task.StartKey, task.EndKey); err != nil {
		log.Warn("Invalid account batch from peer", "peer", task.Assigned, "err", err)
		sm.scorer.recordInvalid(task.Assigned)
		sm.failTask(task)
		return
	}

	latency := time.Since(start)

	// Write to DB.
	var newStorageTasks []*RangeTask
	var newCodeHashes [][]byte
	var lastAddress []byte
	var skipped int

	if err := sm.dbSem.Acquire(ctx, 1); err != nil {
		sm.failTask(task)
		return
	}
	defer sm.dbSem.Release(1)

	if err := sm.db.Update(ctx, func(tx kv.RwTx) error {
		for _, e := range entries {
			if len(e.Key) != 20 {
				snapInvalidAccounts.Inc()
				skipped++
				continue
			}
			valid, incarnation, codeHash := parseAccountForTasks(e.Value)
			if !valid {
				snapInvalidAccounts.Inc()
				skipped++
				continue
			}

			if err := tx.Put(modules.Account, e.Key, e.Value); err != nil {
				return err
			}
			lastAddress = e.Key

			if incarnation > 0 {
				addr := make([]byte, 20)
				copy(addr, e.Key)
				newStorageTasks = append(newStorageTasks, &RangeTask{
					Kind:    TaskStorage,
					Account: addr,
				})
			}
			if codeHash != nil {
				newCodeHashes = append(newCodeHashes, codeHash)
			}
		}
		if lastAddress != nil {
			return SaveAccountCursor(tx, lastAddress)
		}
		return nil
	}); err != nil {
		log.Error("Failed to write snapshot account data", "err", err)
		sm.failTask(task)
		return
	}

	sm.scorer.recordSuccess(task.Assigned, latency)

	sm.mu.Lock()
	defer sm.mu.Unlock()

	written := uint64(len(entries) - skipped)
	sm.accountsDone += written
	snapAccountsDownloaded.Add(int(written))
	sm.bytesReceived += uint64(len(resp.Data))
	snapBytesReceived.Add(len(resp.Data))

	sm.removeTask(&sm.accountTasks, task)

	if !resp.Completed && len(resp.NextStart) > 0 {
		sm.accountTasks = append(sm.accountTasks, &RangeTask{
			Kind:     TaskAccount,
			StartKey: resp.NextStart,
			EndKey:   task.EndKey,
		})
	}

	sm.storageTasks = append(sm.storageTasks, newStorageTasks...)
	if len(newCodeHashes) > 0 {
		sm.codeTasks = append(sm.codeTasks, &RangeTask{
			Kind:       TaskCode,
			CodeHashes: newCodeHashes,
		})
	}
}

// executeSnapshotStorageTask fetches storage at snapshot height via compressed batch.
func (sm *SnapshotManager) executeSnapshotStorageTask(ctx context.Context, task *RangeTask) {
	start := time.Now()

	// Parse incarnation from account data in local DB.
	var incarnation uint16
	_ = sm.db.View(ctx, func(tx kv.Tx) error {
		data, err := tx.GetOne(modules.Account, task.Account)
		if err != nil || data == nil {
			return err
		}
		var acc account.StateAccount
		if err := acc.DecodeForStorage(data); err != nil {
			return nil
		}
		incarnation = acc.Incarnation
		return nil
	})

	req := &sync_pb.GetSnapshotStorageRangeRequest{
		SnapshotBlock: sm.snapshotBlock,
		Account:       task.Account,
		Incarnation:   incarnation,
		Start:         task.StartKey,
		End:           task.EndKey,
		MaxBytes:      sm.cfg.MaxBytesPerReq,
	}

	resp, err := sendGetSnapshotStorageRange(ctx, sm.p2p, task.Assigned, req)
	if err != nil {
		log.Debug("Snapshot storage range request failed", "peer", task.Assigned, "err", err)
		sm.failTask(task)
		return
	}

	entries, err := snapshot.DecodeBatch(resp.Data)
	if err != nil {
		log.Warn("Failed to decode storage batch", "peer", task.Assigned, "err", err)
		sm.failTask(task)
		return
	}

	// Validate storage batch integrity.
	if err := validateStorageBatch(resp, entries, task.StartKey, task.EndKey); err != nil {
		log.Warn("Invalid storage batch from peer", "peer", task.Assigned, "err", err)
		sm.scorer.recordInvalid(task.Assigned)
		sm.failTask(task)
		return
	}

	if err := sm.dbSem.Acquire(ctx, 1); err != nil {
		sm.failTask(task)
		return
	}
	defer sm.dbSem.Release(1)

	if err := sm.db.Update(ctx, func(tx kv.RwTx) error {
		for _, e := range entries {
			// Server sends loc(32) as key — reconstruct full storage key.
			fullKey := modules.PlainGenerateCompositeStorageKey(task.Account, incarnation, e.Key)
			if err := tx.Put(modules.Storage, fullKey, e.Value); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		log.Error("Failed to write snapshot storage data", "err", err)
		sm.failTask(task)
		return
	}

	sm.scorer.recordSuccess(task.Assigned, time.Since(start))

	sm.mu.Lock()
	defer sm.mu.Unlock()

	scount := uint64(len(entries))
	sm.storagesDone += scount
	snapStorageDownloaded.Add(int(scount))
	sm.bytesReceived += uint64(len(resp.Data))
	snapBytesReceived.Add(len(resp.Data))

	sm.removeTask(&sm.storageTasks, task)

	if !resp.Completed && len(resp.NextStart) > 0 {
		sm.storageTasks = append(sm.storageTasks, &RangeTask{
			Kind:     TaskStorage,
			Account:  task.Account,
			StartKey: resp.NextStart,
			EndKey:   task.EndKey,
		})
	}
}

// executeCodeTaskFromPeer fetches contract codes (same as original, code doesn't need snapshot height).
func (sm *SnapshotManager) executeCodeTaskFromPeer(ctx context.Context, task *RangeTask) {
	start := time.Now()
	req := &sync_pb.GetCodeRequest{Hashes: task.CodeHashes}

	resp, err := sendGetCode(ctx, sm.p2p, task.Assigned, req)
	if err != nil {
		log.Debug("Code request failed", "peer", task.Assigned, "err", err)
		sm.failTask(task)
		return
	}

	// Validate code response.
	if err := validateCodeResponse(resp, task.CodeHashes); err != nil {
		log.Warn("Invalid code response from peer", "peer", task.Assigned, "err", err)
		sm.scorer.recordInvalid(task.Assigned)
		sm.failTask(task)
		return
	}

	received := make(map[string]struct{}, len(resp.Codes))
	for _, entry := range resp.Codes {
		received[string(entry.Hash)] = struct{}{}
	}
	var missing [][]byte
	for _, h := range task.CodeHashes {
		if _, ok := received[string(h)]; !ok {
			missing = append(missing, h)
		}
	}

	if err := sm.dbSem.Acquire(ctx, 1); err != nil {
		sm.failTask(task)
		return
	}
	defer sm.dbSem.Release(1)

	if err := sm.db.Update(ctx, func(tx kv.RwTx) error {
		for _, entry := range resp.Codes {
			if err := tx.Put(modules.Code, entry.Hash, entry.Code); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		log.Error("Failed to write code data", "err", err)
		sm.failTask(task)
		return
	}

	sm.scorer.recordSuccess(task.Assigned, time.Since(start))

	sm.mu.Lock()
	defer sm.mu.Unlock()

	ccount := uint64(len(resp.Codes))
	sm.codesDone += ccount
	snapCodesDownloaded.Add(int(ccount))
	var cBytes uint64
	for _, entry := range resp.Codes {
		cBytes += uint64(len(entry.Code))
	}
	sm.bytesReceived += cBytes
	snapBytesReceived.Add(int(cBytes))

	sm.removeTask(&sm.codeTasks, task)

	if len(missing) > 0 {
		snapCodesMissing.Add(len(missing))
		if task.Retries < maxTaskRetries-1 {
			sm.codeTasks = append(sm.codeTasks, &RangeTask{
				Kind:       TaskCode,
				CodeHashes: missing,
				Retries:    task.Retries + 1,
			})
		}
	}
}

func (sm *SnapshotManager) removeTask(list *[]*RangeTask, task *RangeTask) {
	for i, t := range *list {
		if t == task {
			*list = append((*list)[:i], (*list)[i+1:]...)
			return
		}
	}
}

// --- P2P send functions (package-level vars for testability) ---

var sendGetSnapshotInfo = func(ctx context.Context, p2pProvider p2p.SenderEncoder, pid peer.ID) (*sync_pb.SnapshotInfoResponse, error) {
	topic, err := p2p.TopicFromMessage(p2p.GetSnapshotInfoMessageName)
	if err != nil {
		return nil, err
	}
	req := &sync_pb.GetSnapshotInfoRequest{}
	stream, err := p2pProvider.Send(ctx, req, topic, pid)
	if err != nil {
		return nil, err
	}
	defer func() { _ = stream.Close() }()

	code, errMsg, err := readSnapStatusCode(stream)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, errorFromCode(code, errMsg)
	}

	resp := new(sync_pb.SnapshotInfoResponse)
	if err := p2pProvider.Encoding().DecodeWithMaxLength(stream, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

var sendGetSnapshotAccountRange = func(ctx context.Context, p2pProvider p2p.SenderEncoder, pid peer.ID, req *sync_pb.GetSnapshotAccountRangeRequest) (*sync_pb.CompressedBatchResponse, error) {
	topic, err := p2p.TopicFromMessage(p2p.GetSnapshotAccountRangeMessageName)
	if err != nil {
		return nil, err
	}
	stream, err := p2pProvider.Send(ctx, req, topic, pid)
	if err != nil {
		return nil, err
	}
	defer func() { _ = stream.Close() }()

	code, errMsg, err := readSnapStatusCode(stream)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, errorFromCode(code, errMsg)
	}

	resp := new(sync_pb.CompressedBatchResponse)
	if err := p2pProvider.Encoding().DecodeWithMaxLength(stream, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

var sendGetSnapshotStorageRange = func(ctx context.Context, p2pProvider p2p.SenderEncoder, pid peer.ID, req *sync_pb.GetSnapshotStorageRangeRequest) (*sync_pb.CompressedBatchResponse, error) {
	topic, err := p2p.TopicFromMessage(p2p.GetSnapshotStorageRangeMessageName)
	if err != nil {
		return nil, err
	}
	stream, err := p2pProvider.Send(ctx, req, topic, pid)
	if err != nil {
		return nil, err
	}
	defer func() { _ = stream.Close() }()

	code, errMsg, err := readSnapStatusCode(stream)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, errorFromCode(code, errMsg)
	}

	resp := new(sync_pb.CompressedBatchResponse)
	if err := p2pProvider.Encoding().DecodeWithMaxLength(stream, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

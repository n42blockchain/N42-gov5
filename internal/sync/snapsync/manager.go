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
//
// Manager orchestrates snap-sync state downloads from multiple peers.
// Uses semaphore-gated writes (maxConcurrentDBWrites) and batch caps
// (maxResponseEntries) to pull accounts, storage slots and bytecodes
// from the network, with emptyCodeHash used to skip empty accounts.

package snapsync

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"golang.org/x/sync/semaphore"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/proto/sync_pb"
)

// maxConcurrentDBWrites limits concurrent database transactions to avoid
// resource exhaustion on the MDBX backend.
const maxConcurrentDBWrites = 8

// maxResponseEntries is the maximum number of entries accepted in a single
// response from a peer. This prevents OOM from malicious responses.
const maxResponseEntries = 8192

// emptyCodeHash is keccak256("").
var emptyCodeHash = []byte{
	0xc5, 0xd2, 0x46, 0x01, 0x86, 0xf7, 0x23, 0x3c,
	0x92, 0x7e, 0x7d, 0xb2, 0xdc, 0xc7, 0x03, 0xc0,
	0xe5, 0x00, 0xb6, 0x53, 0xca, 0x82, 0x27, 0x3b,
	0x7b, 0xfa, 0xd8, 0x04, 0x5d, 0x85, 0xa4, 0x70,
}

// Manager orchestrates snap sync state downloads from multiple peers.
type Manager struct {
	cfg *conf.SnapSyncConfig
	p2p p2p.P2P
	db  kv.RwDB

	pivotBlock uint64
	stateRoot  []byte // 32 bytes

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
	wg     sync.WaitGroup      // tracks all task goroutines
	dbSem  *semaphore.Weighted // limits concurrent DB writes
	scorer *peerScorer         // peer performance tracker
}

// NewManager creates a new snap sync download manager.
func NewManager(cfg *conf.SnapSyncConfig, p2pProvider p2p.P2P, db kv.RwDB, pivotBlock uint64, stateRoot []byte) *Manager {
	return &Manager{
		cfg:        cfg,
		p2p:        p2pProvider,
		db:         db,
		pivotBlock: pivotBlock,
		stateRoot:  stateRoot,
		dbSem:      semaphore.NewWeighted(maxConcurrentDBWrites),
		scorer:     newPeerScorer(),
	}
}

// Progress holds the current download progress.
type Progress struct {
	AccountsDone  uint64
	StoragesDone  uint64
	CodesDone     uint64
	BytesReceived uint64
	PendingTasks  int
}

// GetProgress returns the current download progress.
func (m *Manager) GetProgress() Progress {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Progress{
		AccountsDone:  m.accountsDone,
		StoragesDone:  m.storagesDone,
		CodesDone:     m.codesDone,
		BytesReceived: m.bytesReceived,
		PendingTasks:  len(m.accountTasks) + len(m.storageTasks) + len(m.codeTasks),
	}
}

// Run starts the download process and blocks until completion or error.
// The provided context controls cancellation; Run waits for all in-flight
// task goroutines before returning.
func (m *Manager) Run(ctx context.Context) error {
	// Try to resume from previous progress.
	if err := m.resume(ctx); err != nil {
		log.Warn("Failed to resume snap sync progress, starting fresh", "err", err)
		m.initFresh()
	}

	// Mark as running.
	if err := m.db.Update(ctx, func(tx kv.RwTx) error {
		return SaveState(tx, StateRunning)
	}); err != nil {
		return err
	}

	log.Info("Snap sync download started",
		"pivot", m.pivotBlock,
		"accountTasks", len(m.accountTasks),
		"storageTasks", len(m.storageTasks),
		"codeTasks", len(m.codeTasks),
	)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	progressTicker := time.NewTicker(30 * time.Second)
	defer progressTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.wg.Wait()
			return ctx.Err()
		case <-ticker.C:
			m.processTimeouts()
		case <-progressTicker.C:
			p := m.GetProgress()
			log.Info("Snap sync progress",
				"accounts", p.AccountsDone,
				"storage", p.StoragesDone,
				"codes", p.CodesDone,
				"bytes", p.BytesReceived,
				"pendingTasks", p.PendingTasks,
			)
		default:
		}

		// Check if all tasks are complete and no goroutines are in-flight.
		m.mu.Lock()
		allDone := len(m.accountTasks) == 0 && len(m.storageTasks) == 0 && len(m.codeTasks) == 0
		m.mu.Unlock()
		if allDone {
			// Wait for all in-flight goroutines to finish before declaring done.
			m.wg.Wait()
			// Re-check after goroutines finish (they may have added new tasks).
			m.mu.Lock()
			allDone = len(m.accountTasks) == 0 && len(m.storageTasks) == 0 && len(m.codeTasks) == 0
			m.mu.Unlock()
			if allDone {
				break
			}
			continue
		}

		// Pick and execute the next unassigned task.
		task := m.pickTask()
		if task == nil {
			// No assignable task. If every remaining task has exhausted its
			// retries and nothing is in flight, we are permanently wedged — fail
			// hard so the caller can fall back rather than spin forever.
			if m.stuck() {
				m.wg.Wait()
				m.mu.Lock()
				remaining := len(m.accountTasks) + len(m.storageTasks) + len(m.codeTasks)
				m.mu.Unlock()
				return fmt.Errorf("snap sync stuck: %d task(s) exhausted %d retries with no assignable work", remaining, maxTaskRetries)
			}
			// Tasks are in flight; wait for completions or timeouts.
			time.Sleep(100 * time.Millisecond)
			continue
		}

		pid := m.pickPeer()
		if pid == "" {
			task.Reset()
			time.Sleep(500 * time.Millisecond)
			continue
		}

		task.Assigned = pid
		task.AssignedAt = time.Now()

		// Execute task in a tracked goroutine.
		m.wg.Add(1)
		go func(t *RangeTask) {
			defer m.wg.Done()
			m.executeTask(ctx, t)
		}(task)
	}

	// Mark as completed.
	if err := m.db.Update(ctx, func(tx kv.RwTx) error {
		return SaveState(tx, StateCompleted)
	}); err != nil {
		return err
	}

	p := m.GetProgress()
	log.Info("Snap sync download completed",
		"accounts", p.AccountsDone,
		"storageSlots", p.StoragesDone,
		"codes", p.CodesDone,
		"bytes", p.BytesReceived,
	)
	return nil
}

// Stop cancels the download manager and waits for in-flight goroutines.
func (m *Manager) Stop() {
	// Caller should cancel the context passed to Run().
	m.wg.Wait()
}

// initFresh initializes the task list for a fresh download.
func (m *Manager) initFresh() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accountTasks = []*RangeTask{
		{
			Kind:     TaskAccount,
			StartKey: nil, // from beginning
			EndKey:   nil, // to end
		},
	}
	m.storageTasks = nil
	m.codeTasks = nil
}

// resume tries to restore progress from the database.
func (m *Manager) resume(ctx context.Context) error {
	var cursor []byte
	var state string
	if err := m.db.View(ctx, func(tx kv.Tx) error {
		var err error
		state, err = LoadState(tx)
		if err != nil {
			return err
		}
		cursor, err = LoadAccountCursor(tx)
		return err
	}); err != nil {
		return err
	}

	if state == StateCompleted {
		// Already completed, nothing to do.
		m.accountTasks = nil
		m.storageTasks = nil
		m.codeTasks = nil
		return nil
	}

	if cursor == nil {
		// No progress saved, start fresh.
		m.initFresh()
		return nil
	}

	// Resume from the saved cursor.
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accountTasks = []*RangeTask{
		{
			Kind:     TaskAccount,
			StartKey: cursor,
			EndKey:   nil,
		},
	}
	m.storageTasks = nil
	m.codeTasks = nil
	log.Info("Resuming snap sync from cursor", "cursor", cursor)
	return nil
}

// pickTask returns the next unassigned task, or nil if none available.
func (m *Manager) pickTask() *RangeTask {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Priority: accounts > storage > code
	for _, task := range m.accountTasks {
		if !task.IsAssigned() && task.CanRetry() {
			return task
		}
	}
	for _, task := range m.storageTasks {
		if !task.IsAssigned() && task.CanRetry() {
			return task
		}
	}
	for _, task := range m.codeTasks {
		if !task.IsAssigned() && task.CanRetry() {
			return task
		}
	}
	return nil
}

// stuck reports whether snap sync can make no further progress: no task is
// assigned (nothing in flight will complete or fail-then-retry) and every
// remaining task has exhausted its retries. In that state pickTask returns nil
// forever while allDone (all lists empty) never becomes true, so Run would spin
// indefinitely — a single flaky or malicious peer failing one range
// maxTaskRetries times is enough to trigger it. The caller turns this into a
// hard error so the outer sync can fall back instead of hanging.
func (m *Manager) stuck() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, list := range [][]*RangeTask{m.accountTasks, m.storageTasks, m.codeTasks} {
		for _, t := range list {
			if t.IsAssigned() || t.CanRetry() {
				return false
			}
		}
	}
	// No assignable or in-flight work. If any task remains, we are wedged.
	return len(m.accountTasks)+len(m.storageTasks)+len(m.codeTasks) > 0
}

// pickPeer returns the best-scoring connected peer for the next task.
func (m *Manager) pickPeer() peer.ID {
	peers := m.p2p.Peers()
	if peers == nil {
		return ""
	}
	connected := peers.Connected()
	if len(connected) == 0 {
		return ""
	}
	return m.scorer.bestPeer(connected)
}

// processTimeouts checks for timed-out tasks and resets them.
func (m *Manager) processTimeouts() {
	m.mu.Lock()
	defer m.mu.Unlock()

	resetTimedOut := func(tasks []*RangeTask, kind string) {
		for _, task := range tasks {
			if task.IsTimedOut() {
				log.Debug("Task timed out, resetting", "kind", kind, "peer", task.Assigned)
				task.Retries++
				task.Reset()
				snapTaskTimeouts.Inc()
			}
		}
	}

	resetTimedOut(m.accountTasks, "account")
	resetTimedOut(m.storageTasks, "storage")
	resetTimedOut(m.codeTasks, "code")
}

// executeTask runs a single download task.
func (m *Manager) executeTask(ctx context.Context, task *RangeTask) {
	switch task.Kind {
	case TaskAccount:
		m.executeAccountTask(ctx, task)
	case TaskStorage:
		m.executeStorageTask(ctx, task)
	case TaskCode:
		m.executeCodeTask(ctx, task)
	}
}

// failTask marks a task as failed by incrementing retries and resetting assignment.
func (m *Manager) failTask(task *RangeTask) {
	snapTaskErrors.Inc()
	m.scorer.recordFailure(task.Assigned)
	m.mu.Lock()
	task.Retries++
	task.Reset()
	m.mu.Unlock()
}

// executeAccountTask fetches and processes an account range.
func (m *Manager) executeAccountTask(ctx context.Context, task *RangeTask) {
	start := time.Now()

	req := &sync_pb.GetAccountRangeRequest{
		Root:     m.stateRoot,
		Start:    task.StartKey,
		End:      task.EndKey,
		MaxBytes: m.cfg.MaxBytesPerReq,
	}

	resp, err := sendGetAccountRange(ctx, m.p2p, task.Assigned, req)
	if err != nil {
		log.Debug("Account range request failed", "peer", task.Assigned, "err", err)
		m.failTask(task)
		return
	}

	latency := time.Since(start)

	// Validate response size.
	if len(resp.Accounts) > maxResponseEntries {
		log.Warn("Peer returned too many account entries", "peer", task.Assigned, "count", len(resp.Accounts))
		m.failTask(task)
		return
	}

	// Write accounts to database.
	var newStorageTasks []*RangeTask
	var newCodeHashes [][]byte
	var lastAddress []byte
	var skipped int

	// Acquire DB write semaphore.
	if err := m.dbSem.Acquire(ctx, 1); err != nil {
		m.failTask(task)
		return
	}
	defer m.dbSem.Release(1)

	if err := m.db.Update(ctx, func(tx kv.RwTx) error {
		for _, entry := range resp.Accounts {
			// Validate address length.
			if len(entry.Address) != 20 {
				log.Warn("Skipping account with invalid address length",
					"peer", task.Assigned, "len", len(entry.Address))
				snapInvalidAccounts.Inc()
				skipped++
				continue
			}

			// Validate and parse account data.
			valid, hasStorage, codeHash := parseAccountForTasks(entry.Account)
			if !valid {
				log.Warn("Skipping account with invalid protobuf data",
					"peer", task.Assigned, "addr", entry.Address)
				snapInvalidAccounts.Inc()
				skipped++
				continue
			}

			if err := tx.Put(modules.Account, entry.Address, entry.Account); err != nil {
				return err
			}
			lastAddress = entry.Address

			if hasStorage {
				newStorageTasks = append(newStorageTasks, &RangeTask{
					Kind:    TaskStorage,
					Account: entry.Address,
				})
			}
			if codeHash != nil {
				newCodeHashes = append(newCodeHashes, codeHash)
			}
		}

		// Save cursor for crash recovery.
		if lastAddress != nil {
			return SaveAccountCursor(tx, lastAddress)
		}
		return nil
	}); err != nil {
		log.Error("Failed to write account data", "err", err)
		m.failTask(task)
		return
	}

	m.scorer.recordSuccess(task.Assigned, latency)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Update progress after successful write.
	written := uint64(len(resp.Accounts) - skipped)
	m.accountsDone += written
	snapAccountsDownloaded.Add(int(written))
	var batchBytes uint64
	for _, entry := range resp.Accounts {
		batchBytes += uint64(len(entry.Address) + len(entry.Account))
	}
	m.bytesReceived += batchBytes
	snapBytesReceived.Add(int(batchBytes))

	// Remove completed task.
	m.removeTask(&m.accountTasks, task)

	// If not completed, add continuation task.
	if !resp.Completed && len(resp.NextStart) > 0 {
		m.accountTasks = append(m.accountTasks, &RangeTask{
			Kind:     TaskAccount,
			StartKey: resp.NextStart,
			EndKey:   task.EndKey,
		})
	}

	// Add discovered storage and code tasks.
	m.storageTasks = append(m.storageTasks, newStorageTasks...)
	if len(newCodeHashes) > 0 {
		m.codeTasks = append(m.codeTasks, &RangeTask{
			Kind:       TaskCode,
			CodeHashes: newCodeHashes,
		})
	}

	log.Debug("Account range processed",
		"received", len(resp.Accounts),
		"skipped", skipped,
		"completed", resp.Completed,
		"totalAccounts", m.accountsDone,
		"pendingStorage", len(m.storageTasks),
		"pendingCode", len(m.codeTasks),
	)
}

// executeStorageTask fetches and processes a storage range.
func (m *Manager) executeStorageTask(ctx context.Context, task *RangeTask) {
	start := time.Now()

	req := &sync_pb.GetStorageRangeRequest{
		Root:     m.stateRoot,
		Account:  task.Account,
		Start:    task.StartKey,
		End:      task.EndKey,
		MaxBytes: m.cfg.MaxBytesPerReq,
	}

	resp, err := sendGetStorageRange(ctx, m.p2p, task.Assigned, req)
	if err != nil {
		log.Debug("Storage range request failed", "peer", task.Assigned, "err", err)
		m.failTask(task)
		return
	}

	if len(resp.Slots) > maxResponseEntries {
		log.Warn("Peer returned too many storage entries", "peer", task.Assigned, "count", len(resp.Slots))
		m.failTask(task)
		return
	}

	if err := m.dbSem.Acquire(ctx, 1); err != nil {
		m.failTask(task)
		return
	}
	defer m.dbSem.Release(1)

	if err := m.db.Update(ctx, func(tx kv.RwTx) error {
		for _, entry := range resp.Slots {
			if err := tx.Put(modules.Storage, entry.Key, entry.Value); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		log.Error("Failed to write storage data", "err", err)
		m.failTask(task)
		return
	}

	m.scorer.recordSuccess(task.Assigned, time.Since(start))

	m.mu.Lock()
	defer m.mu.Unlock()

	scount := uint64(len(resp.Slots))
	m.storagesDone += scount
	snapStorageDownloaded.Add(int(scount))
	var sBytes uint64
	for _, entry := range resp.Slots {
		sBytes += uint64(len(entry.Key) + len(entry.Value))
	}
	m.bytesReceived += sBytes
	snapBytesReceived.Add(int(sBytes))

	m.removeTask(&m.storageTasks, task)

	if !resp.Completed && len(resp.NextStart) > 0 {
		m.storageTasks = append(m.storageTasks, &RangeTask{
			Kind:     TaskStorage,
			Account:  task.Account,
			StartKey: resp.NextStart,
			EndKey:   task.EndKey,
		})
	}
}

// executeCodeTask fetches and processes contract bytecodes.
func (m *Manager) executeCodeTask(ctx context.Context, task *RangeTask) {
	start := time.Now()

	req := &sync_pb.GetCodeRequest{
		Hashes: task.CodeHashes,
	}

	resp, err := sendGetCode(ctx, m.p2p, task.Assigned, req)
	if err != nil {
		log.Debug("Code request failed", "peer", task.Assigned, "err", err)
		m.failTask(task)
		return
	}

	if len(resp.Codes) > maxResponseEntries {
		log.Warn("Peer returned too many code entries", "peer", task.Assigned, "count", len(resp.Codes))
		m.failTask(task)
		return
	}

	// Track which code hashes were received for completeness verification.
	received := make(map[string]struct{}, len(resp.Codes))
	for _, entry := range resp.Codes {
		received[string(entry.Hash)] = struct{}{}
	}
	var missingHashes [][]byte
	for _, hash := range task.CodeHashes {
		if _, ok := received[string(hash)]; !ok {
			missingHashes = append(missingHashes, hash)
		}
	}

	if err := m.dbSem.Acquire(ctx, 1); err != nil {
		m.failTask(task)
		return
	}
	defer m.dbSem.Release(1)

	if err := m.db.Update(ctx, func(tx kv.RwTx) error {
		for _, entry := range resp.Codes {
			if err := tx.Put(modules.Code, entry.Hash, entry.Code); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		log.Error("Failed to write code data", "err", err)
		m.failTask(task)
		return
	}

	m.scorer.recordSuccess(task.Assigned, time.Since(start))

	m.mu.Lock()
	defer m.mu.Unlock()

	ccount := uint64(len(resp.Codes))
	m.codesDone += ccount
	snapCodesDownloaded.Add(int(ccount))
	var cBytes uint64
	for _, entry := range resp.Codes {
		cBytes += uint64(len(entry.Code))
	}
	m.bytesReceived += cBytes
	snapBytesReceived.Add(int(cBytes))

	m.removeTask(&m.codeTasks, task)

	// If some requested codes were not found, create a retry task with a different peer.
	if len(missingHashes) > 0 {
		snapCodesMissing.Add(len(missingHashes))
		log.Warn("Some contract codes not found on peer",
			"peer", task.Assigned,
			"missing", len(missingHashes),
			"received", len(resp.Codes),
		)
		if task.Retries < maxTaskRetries-1 {
			m.codeTasks = append(m.codeTasks, &RangeTask{
				Kind:       TaskCode,
				CodeHashes: missingHashes,
				Retries:    task.Retries + 1,
			})
		} else {
			log.Error("Gave up on missing contract codes after max retries",
				"missing", len(missingHashes))
		}
	}
}

// removeTask removes a task from the given task list. Caller must hold m.mu.
func (m *Manager) removeTask(list *[]*RangeTask, task *RangeTask) {
	for i, t := range *list {
		if t == task {
			*list = append((*list)[:i], (*list)[i+1:]...)
			return
		}
	}
}

// parseAccountForTasks validates encoded account data and reports whether the
// account needs a storage fetch (it carries code) and which code to fetch.
//
// Returns valid=false if the data cannot be decoded as a StateAccount.
// codeHash is returned when it is non-empty.
//
// Account data in the Account table uses V2 variable-length encoding
// (Erigon-style fieldBits+varint). DecodeForStorage auto-detects legacy protobuf.
func parseAccountForTasks(encoded []byte) (valid bool, hasStorage bool, codeHash []byte) {
	if len(encoded) == 0 {
		return true, false, nil
	}

	var acc account.StateAccount
	if err := acc.DecodeForStorage(encoded); err != nil {
		return false, false, nil
	}

	if !acc.IsEmptyCodeHash() {
		codeHash = make([]byte, 32)
		copy(codeHash, acc.CodeHash[:])
		hasStorage = true
	}

	return true, hasStorage, codeHash
}

// sendGetAccountRange wraps the sync package's SendGetAccountRange.
// This indirection allows for easier testing.
var sendGetAccountRange = func(ctx context.Context, p2pProvider p2p.SenderEncoder, pid peer.ID, req *sync_pb.GetAccountRangeRequest) (*sync_pb.AccountRangeResponse, error) {
	topic, err := p2p.TopicFromMessage(p2p.GetAccountRangeMessageName)
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

	resp := new(sync_pb.AccountRangeResponse)
	if err := p2pProvider.Encoding().DecodeWithMaxLength(stream, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

var sendGetStorageRange = func(ctx context.Context, p2pProvider p2p.SenderEncoder, pid peer.ID, req *sync_pb.GetStorageRangeRequest) (*sync_pb.StorageRangeResponse, error) {
	topic, err := p2p.TopicFromMessage(p2p.GetStorageRangeMessageName)
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

	resp := new(sync_pb.StorageRangeResponse)
	if err := p2pProvider.Encoding().DecodeWithMaxLength(stream, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

var sendGetCode = func(ctx context.Context, p2pProvider p2p.SenderEncoder, pid peer.ID, req *sync_pb.GetCodeRequest) (*sync_pb.CodeResponse, error) {
	topic, err := p2p.TopicFromMessage(p2p.GetCodeMessageName)
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

	resp := new(sync_pb.CodeResponse)
	if err := p2pProvider.Encoding().DecodeWithMaxLength(stream, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// readSnapStatusCode reads the 1-byte status code from a snap sync response stream.
func readSnapStatusCode(stream interface{ Read([]byte) (int, error) }) (byte, string, error) {
	b := make([]byte, 1)
	if _, err := stream.Read(b); err != nil {
		return 0, "", err
	}
	if b[0] == 0 {
		return 0, "", nil
	}
	return b[0], "remote error", nil
}

func errorFromCode(code byte, msg string) error {
	return &snapError{code: code, msg: msg}
}

type snapError struct {
	code byte
	msg  string
}

func (e *snapError) Error() string {
	return e.msg
}

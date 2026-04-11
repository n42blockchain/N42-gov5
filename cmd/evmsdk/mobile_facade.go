// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Mobile-friendly package-level facade. Every function in this file is
// designed to be cleanly bindable by `gomobile bind` — parameters and
// return values are restricted to:
//
//	string, int / int64, []byte, error
//
// Mobile apps (iOS / Android via gomobile) call these functions through
// the generated .xcframework / .aar instead of touching `EvmEngine` or
// `WebSocketService` directly. The richer Go API is still available for
// in-process callers (tests, server-side code).
//
// Hidden state lives in a package-level singleton; this matches the
// shape of the Rust reference implementation
// D:\N42\n42-26\crates\n42-mobile-ffi (one VerifierContext per process).

package evmsdk

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
)

// Mobile state strings returned by MobileState. EvmEngine.State() emits
// "started"/"stopped"; we add "uninitialized" for the pre-MobileInit phase.
const (
	mobileStateStarted       = "started"
	mobileStateStopped       = "stopped"
	mobileStateUninitialized = "uninitialized"

	mobileVersionString = "n42-mobile-verifier/v1 (StreamPacket V2, ReadLog v1, BLS12-381)"
)

// mobileSingleton is the hidden VerifierContext-equivalent. All facade
// functions read/write this. Lazily initialized by MobileInit.
var (
	mobileMu        sync.Mutex
	mobileSingleton *mobileVerifier
)

// mobileVerifier holds the per-process verification state. All fields
// are unexported so gomobile never sees them.
//
// Connection model: ONE active producer at a time. Multiple producers
// can be registered as candidates; the rotation monitor switches
// between them when the active leader is too slow. This matches the
// HotStuff-2 multi-leader rotation deployment where a phone follows
// whichever node is currently producing instead of opening N parallel
// WebSocket connections.
type mobileVerifier struct {
	chainID    int64
	privKeyHex string                    // shared by all producer engines
	pubkey     string                    // hex-encoded BLS public key
	defaultAcc string                    // account from MobileSetServer
	producers  map[string]*producerEntry // candidate registry, keyed on URI
	order      []string                  // insertion order for stable rotation
	activeURI  string                    // currently connected producer URI
	policy     rotationPolicy            // rotation thresholds
	lastBlock  atomic.Int64              // unix-ms of last verified block
	monitorCtx context.Context           // cancellable monitor goroutine context
	cancelMon  context.CancelFunc        // monitor cancel
	dedup      *BlockDedup               // shared across producer rotations
	cached     map[blockDedupKey][]byte  // signed payload cache for dedup
	stats      mobileStats               // global counters across producers
	lastInfo   *mobileLastInfo
}

// producerEntry is a registered candidate producer. The engine field
// holds the EvmEngine pre-configured with this URI; only the active
// producer's engine is actually started at any given time.
type producerEntry struct {
	uri     string
	account string
	engine  *EvmEngine
}

// rotationPolicy bounds how the monitor decides to rotate.
//
//	BlockTimeMs       — expected interval between blocks (e.g. 1000 for 1s)
//	SlowLeaderTimeoutMs — no block for this long → consider rotating
//	RotationCycleMs   — committee-wide rotation period; if shorter than
//	                    SlowLeaderTimeoutMs, the active node will likely
//	                    be re-elected before any rotation finishes, so
//	                    the monitor STAYS instead of churning the connection
//	MonitorPeriodMs   — how often the monitor goroutine checks
type rotationPolicy struct {
	BlockTimeMs         int64
	SlowLeaderTimeoutMs int64
	RotationCycleMs     int64
	MonitorPeriodMs     int64
}

// defaultRotationPolicy is what MobileInit installs. Tuned for a
// typical 1s block time + 7-validator committee. Override via
// MobileSetRotationPolicy.
var defaultRotationPolicy = rotationPolicy{
	BlockTimeMs:         1000,
	SlowLeaderTimeoutMs: 3000,
	RotationCycleMs:     7000,
	MonitorPeriodMs:     500,
}

// dedupCacheCapacity bounds how many distinct (number, hash) pairs the
// shared dedup remembers. 1024 covers ~3.4 hours of unique blocks at
// 12s block time.
const dedupCacheCapacity = 1024

// mobileStats accumulates per-block verification metrics across the
// process lifetime. The mutex protects the counters; snapshot() returns
// a lock-free value copy for JSON encoding.
type mobileStats struct {
	mu             sync.Mutex
	BlocksVerified int64
	SuccessCount   int64
	FailureCount   int64
	TotalVerifyMs  int64
}

// mobileStatsSnapshot is the JSON-shaped value-copy of mobileStats with
// derived fields. Has no mutex; safe to return by value.
type mobileStatsSnapshot struct {
	BlocksVerified int64 `json:"blocks_verified"`
	SuccessCount   int64 `json:"success_count"`
	FailureCount   int64 `json:"failure_count"`
	TotalVerifyMs  int64 `json:"total_verify_ms"`
	SuccessRate    int64 `json:"success_rate"`
	AvgTimeMs      int64 `json:"avg_time_ms"`
}

func (s *mobileStats) snapshot() mobileStatsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := mobileStatsSnapshot{
		BlocksVerified: s.BlocksVerified,
		SuccessCount:   s.SuccessCount,
		FailureCount:   s.FailureCount,
		TotalVerifyMs:  s.TotalVerifyMs,
	}
	if out.BlocksVerified > 0 {
		out.SuccessRate = (out.SuccessCount * 100) / out.BlocksVerified
		out.AvgTimeMs = out.TotalVerifyMs / out.BlocksVerified
	}
	return out
}

// mobileLastInfo describes the most recent verification attempt.
// JSON-encoded by MobileGetLastVerifyInfo.
type mobileLastInfo struct {
	BlockNumber          int64  `json:"block_number"`
	BlockHash            string `json:"block_hash"`
	ComputedReceiptsRoot string `json:"computed_receipts_root"`
	TxCount              int    `json:"tx_count"`
	WitnessReadLogLen    int    `json:"witness_read_log_len"`
	UncachedBytecodes    int    `json:"uncached_bytecodes"`
	PacketSizeBytes      int    `json:"packet_size_bytes"`
	VerifyTimeMs         int64  `json:"verify_time_ms"`
	Success              bool   `json:"success"`
	ErrorMsg             string `json:"error_msg"`
	Signature            string `json:"signature"`
}

// errString returns "" if err is nil, else err.Error(). gomobile-friendly.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// MobileInit initializes the package-level verifier with the given chain
// ID. Idempotent — calling twice resets state. Returns "" on success or
// an error message string.
//
// MobileInit also installs the package-level dedup hooks so that vertify
// (V1) and verifyV2 (V2) skip blocks already signed via another producer
// connection. ClearVerifyHooks runs in MobileFree.
func MobileInit(chainID int64) string {
	mobileMu.Lock()
	defer mobileMu.Unlock()
	mobileSingleton = &mobileVerifier{
		chainID:   chainID,
		producers: make(map[string]*producerEntry),
		policy:    defaultRotationPolicy,
		dedup:     NewBlockDedup(dedupCacheCapacity),
		cached:    make(map[blockDedupKey][]byte, dedupCacheCapacity),
	}
	installMobileVerifyHooks()
	return ""
}

// MobileSetRotationPolicy overrides the rotation thresholds. All
// arguments are millisecond values; pass 0 to keep the current default
// for that field.
func MobileSetRotationPolicy(blockTimeMs, slowLeaderTimeoutMs, rotationCycleMs, monitorPeriodMs int64) string {
	mobileMu.Lock()
	defer mobileMu.Unlock()
	if mobileSingleton == nil {
		return "verifier not initialized"
	}
	if blockTimeMs > 0 {
		mobileSingleton.policy.BlockTimeMs = blockTimeMs
	}
	if slowLeaderTimeoutMs > 0 {
		mobileSingleton.policy.SlowLeaderTimeoutMs = slowLeaderTimeoutMs
	}
	if rotationCycleMs > 0 {
		mobileSingleton.policy.RotationCycleMs = rotationCycleMs
	}
	if monitorPeriodMs > 0 {
		mobileSingleton.policy.MonitorPeriodMs = monitorPeriodMs
	}
	return ""
}

// MobileGetRotationPolicy returns the current policy as JSON.
func MobileGetRotationPolicy() string {
	mobileMu.Lock()
	defer mobileMu.Unlock()
	if mobileSingleton == nil {
		return "{}"
	}
	b, _ := json.Marshal(struct {
		BlockTimeMs         int64 `json:"block_time_ms"`
		SlowLeaderTimeoutMs int64 `json:"slow_leader_timeout_ms"`
		RotationCycleMs     int64 `json:"rotation_cycle_ms"`
		MonitorPeriodMs     int64 `json:"monitor_period_ms"`
	}{
		BlockTimeMs:         mobileSingleton.policy.BlockTimeMs,
		SlowLeaderTimeoutMs: mobileSingleton.policy.SlowLeaderTimeoutMs,
		RotationCycleMs:     mobileSingleton.policy.RotationCycleMs,
		MonitorPeriodMs:     mobileSingleton.policy.MonitorPeriodMs,
	})
	return string(b)
}

// installMobileVerifyHooks wires the dedup-aware skip + cache callbacks
// into the package-level verify hooks. The cache hook also updates
// lastBlock so the rotation monitor knows the active producer is alive.
func installMobileVerifyHooks() {
	shouldSkip := VerifyShouldSkipFunc(func(num uint64, hash types.Hash) (bool, []byte) {
		mobileMu.Lock()
		defer mobileMu.Unlock()
		if mobileSingleton == nil {
			return false, nil
		}
		key := blockDedupKey{number: num, hash: hash}
		if cached, ok := mobileSingleton.cached[key]; ok {
			return true, cached
		}
		return false, nil
	})
	cache := VerifyCacheFunc(func(num uint64, hash types.Hash, payload []byte) {
		mobileMu.Lock()
		v := mobileSingleton
		mobileMu.Unlock()
		if v == nil {
			return
		}
		// Active producer just verified a block — bump the liveness timestamp
		// without holding the big mutex (atomic write).
		v.lastBlock.Store(time.Now().UnixMilli())

		mobileMu.Lock()
		defer mobileMu.Unlock()
		if mobileSingleton == nil {
			return
		}
		if mobileSingleton.dedup.Mark(num, hash) {
			cp := make([]byte, len(payload))
			copy(cp, payload)
			mobileSingleton.cached[blockDedupKey{number: num, hash: hash}] = cp
			if len(mobileSingleton.cached) > dedupCacheCapacity*2 {
				mobileSingleton.trimCached()
			}
		}
	})
	SetVerifyHooks(shouldSkip, cache)
}

// trimCached drops cached payloads whose dedup entries have been evicted.
// O(n) over the cache; called only when the cache exceeds 2x window.
// Caller must hold mobileMu.
func (v *mobileVerifier) trimCached() {
	for k := range v.cached {
		if !v.dedup.Has(k.number, k.hash) {
			delete(v.cached, k)
		}
	}
}

// MobileSetPrivKey sets the BLS12-381 secret key (hex string, no 0x
// prefix). Shared by every producer engine. Derives and caches the
// public key.
func MobileSetPrivKey(privKeyHex string) string {
	mobileMu.Lock()
	defer mobileMu.Unlock()
	if mobileSingleton == nil {
		return "verifier not initialized; call MobileInit first"
	}
	pubIfce, err := BlsPublicKey(privKeyHex)
	if err != nil {
		return "derive public key failed: " + err.Error()
	}
	pubStr, ok := pubIfce.(string)
	if !ok {
		return "BlsPublicKey returned non-string"
	}
	mobileSingleton.privKeyHex = privKeyHex
	mobileSingleton.pubkey = pubStr
	// Propagate to existing producer engines.
	for _, p := range mobileSingleton.producers {
		p.engine.PrivKey = privKeyHex
	}
	return ""
}

// MobileSetServer configures the WebSocket server URI and verifier
// account. Convenience for the single-candidate case: registers ONE
// producer (or updates an existing entry's account). Multi-candidate
// callers should use MobileAddProducer instead.
func MobileSetServer(serverURI, account string) string {
	mobileMu.Lock()
	defer mobileMu.Unlock()
	if mobileSingleton == nil {
		return "verifier not initialized; call MobileInit first"
	}
	mobileSingleton.defaultAcc = account
	if p, ok := mobileSingleton.producers[serverURI]; ok {
		p.account = account
		p.engine.Account = account
		return ""
	}
	mobileSingleton.addProducerLocked(serverURI, account)
	return ""
}

// MobileAddProducer registers a new candidate producer. Candidates are
// rotated through by the active-producer monitor when the current
// active leader is too slow. The new producer is NOT immediately
// connected; only the active producer's engine is running at any time.
//
// Returns "" on success, or an error message if the verifier is not
// initialized or the URI is already registered.
func MobileAddProducer(serverURI, account string) string {
	mobileMu.Lock()
	defer mobileMu.Unlock()
	if mobileSingleton == nil {
		return "verifier not initialized; call MobileInit first"
	}
	if _, exists := mobileSingleton.producers[serverURI]; exists {
		return "producer already registered: " + serverURI
	}
	mobileSingleton.addProducerLocked(serverURI, account)
	return ""
}

// addProducerLocked appends a candidate. Caller holds mobileMu.
func (v *mobileVerifier) addProducerLocked(uri, account string) {
	v.producers[uri] = &producerEntry{
		uri:     uri,
		account: account,
		engine: &EvmEngine{
			PrivKey:   v.privKeyHex,
			ServerUri: uri,
			Account:   account,
		},
	}
	v.order = append(v.order, uri)
}

// MobileRemoveProducer unregisters a candidate. If it is currently
// active, stops it and (if other candidates remain) rotates to the next.
func MobileRemoveProducer(serverURI string) string {
	mobileMu.Lock()
	defer mobileMu.Unlock()
	if mobileSingleton == nil {
		return "verifier not initialized"
	}
	p, ok := mobileSingleton.producers[serverURI]
	if !ok {
		return "producer not found: " + serverURI
	}
	wasActive := mobileSingleton.activeURI == serverURI
	_ = p.engine.Stop()
	delete(mobileSingleton.producers, serverURI)
	for i, u := range mobileSingleton.order {
		if u == serverURI {
			mobileSingleton.order = append(mobileSingleton.order[:i], mobileSingleton.order[i+1:]...)
			break
		}
	}
	if wasActive {
		mobileSingleton.activeURI = ""
		// Try to start the next available candidate, if any.
		if len(mobileSingleton.order) > 0 {
			next := mobileSingleton.order[0]
			if err := mobileSingleton.activateLocked(next); err != nil {
				return "removed " + serverURI + " but next start failed: " + err.Error()
			}
		}
	}
	return ""
}

// MobileListProducers returns a JSON array of registered candidates:
// [{"uri":"...", "account":"...", "state":"started"|"stopped", "active":true|false}, ...]
// Order matches insertion order.
func MobileListProducers() string {
	mobileMu.Lock()
	defer mobileMu.Unlock()
	if mobileSingleton == nil {
		return "[]"
	}
	type entry struct {
		URI     string `json:"uri"`
		Account string `json:"account"`
		State   string `json:"state"`
		Active  bool   `json:"active"`
	}
	out := make([]entry, 0, len(mobileSingleton.order))
	for _, uri := range mobileSingleton.order {
		p := mobileSingleton.producers[uri]
		out = append(out, entry{
			URI:     p.uri,
			Account: p.account,
			State:   p.engine.State(),
			Active:  uri == mobileSingleton.activeURI,
		})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// MobileGetActiveProducer returns the URI of the currently active
// producer, or "" if none is connected.
func MobileGetActiveProducer() string {
	mobileMu.Lock()
	defer mobileMu.Unlock()
	if mobileSingleton == nil {
		return ""
	}
	return mobileSingleton.activeURI
}

// MobileGetPubkey returns the BLS12-381 public key as a hex string,
// or "" if MobileSetPrivKey has not been called.
func MobileGetPubkey() string {
	mobileMu.Lock()
	defer mobileMu.Unlock()
	if mobileSingleton == nil {
		return ""
	}
	return mobileSingleton.pubkey
}

// MobileStart picks the FIRST registered candidate as active, starts
// its engine, and launches the rotation monitor goroutine. Returns ""
// on success or an error message.
func MobileStart() string {
	mobileMu.Lock()
	defer mobileMu.Unlock()
	if mobileSingleton == nil {
		return "verifier not initialized"
	}
	if len(mobileSingleton.order) == 0 {
		return "no producers registered; call MobileSetServer or MobileAddProducer first"
	}
	if mobileSingleton.activeURI != "" {
		return "already started; call MobileStop first"
	}
	first := mobileSingleton.order[0]
	if err := mobileSingleton.activateLocked(first); err != nil {
		return err.Error()
	}
	mobileSingleton.startMonitorLocked()
	return ""
}

// MobileStop stops the active producer engine + the rotation monitor.
func MobileStop() string {
	mobileMu.Lock()
	defer mobileMu.Unlock()
	if mobileSingleton == nil {
		return "verifier not initialized"
	}
	mobileSingleton.stopMonitorLocked()
	if mobileSingleton.activeURI != "" {
		if p, ok := mobileSingleton.producers[mobileSingleton.activeURI]; ok {
			_ = p.engine.Stop()
		}
		mobileSingleton.activeURI = ""
	}
	return ""
}

// MobileState reports the active connection's state:
//   - "uninitialized" if MobileInit not called
//   - "stopped"       if no active producer
//   - "started"       if the active producer is running
func MobileState() string {
	mobileMu.Lock()
	defer mobileMu.Unlock()
	if mobileSingleton == nil {
		return mobileStateUninitialized
	}
	if mobileSingleton.activeURI == "" {
		return mobileStateStopped
	}
	if p, ok := mobileSingleton.producers[mobileSingleton.activeURI]; ok {
		return p.engine.State()
	}
	return mobileStateStopped
}

// activateLocked sets `uri` as active and starts its engine. Caller
// holds mobileMu. Resets the lastBlock liveness timestamp so the
// monitor gives the new producer a fresh window.
func (v *mobileVerifier) activateLocked(uri string) error {
	p, ok := v.producers[uri]
	if !ok {
		return errors.New("activateLocked: producer not registered: " + uri)
	}
	if err := p.engine.Start(); err != nil {
		return errors.New(uri + ": " + err.Error())
	}
	v.activeURI = uri
	v.lastBlock.Store(time.Now().UnixMilli())
	return nil
}

// rotateNextLocked picks the next candidate in insertion order after
// the current active and activates it. If only one candidate exists or
// the active one is the only choice, this is a no-op. Caller holds
// mobileMu.
func (v *mobileVerifier) rotateNextLocked() error {
	if len(v.order) <= 1 {
		return nil
	}
	currentIdx := -1
	for i, u := range v.order {
		if u == v.activeURI {
			currentIdx = i
			break
		}
	}
	if currentIdx < 0 {
		// Active not in order list (race) — start the first one.
		return v.activateLocked(v.order[0])
	}
	nextIdx := (currentIdx + 1) % len(v.order)
	nextURI := v.order[nextIdx]
	// Stop current.
	if p, ok := v.producers[v.activeURI]; ok {
		_ = p.engine.Stop()
	}
	v.activeURI = ""
	return v.activateLocked(nextURI)
}

// startMonitorLocked launches the rotation monitor goroutine. Caller
// holds mobileMu. Cancels any previous monitor first.
func (v *mobileVerifier) startMonitorLocked() {
	if v.cancelMon != nil {
		v.cancelMon()
	}
	v.monitorCtx, v.cancelMon = context.WithCancel(context.Background())
	go runRotationMonitor(v.monitorCtx)
}

// stopMonitorLocked cancels the rotation monitor goroutine.
// Caller holds mobileMu.
func (v *mobileVerifier) stopMonitorLocked() {
	if v.cancelMon != nil {
		v.cancelMon()
		v.cancelMon = nil
		v.monitorCtx = nil
	}
}

// runRotationMonitor is the background loop that detects "slow leader"
// and rotates to the next candidate when appropriate. Detached from
// the singleton so cancellation is goroutine-clean.
func runRotationMonitor(ctx context.Context) {
	// Initial period read — protected because facade may be reset between
	// MobileInit and MobileStart.
	mobileMu.Lock()
	v := mobileSingleton
	period := time.Duration(0)
	if v != nil && v.policy.MonitorPeriodMs > 0 {
		period = time.Duration(v.policy.MonitorPeriodMs) * time.Millisecond
	}
	mobileMu.Unlock()
	if period <= 0 {
		period = 500 * time.Millisecond
	}
	ticker := time.NewTicker(period)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rotationMonitorTick()
		}
	}
}

// rotationMonitorTick is one liveness check. Decides to rotate iff:
//   - the active producer hasn't delivered a verifiable block in
//     SlowLeaderTimeoutMs, AND
//   - RotationCycleMs > SlowLeaderTimeoutMs (i.e. waiting for the
//     active node's next leadership turn is NOT cheaper than rotating).
func rotationMonitorTick() {
	mobileMu.Lock()
	defer mobileMu.Unlock()
	v := mobileSingleton
	if v == nil || v.activeURI == "" || len(v.order) <= 1 {
		return
	}
	policy := v.policy
	if policy.SlowLeaderTimeoutMs <= 0 {
		return
	}
	last := v.lastBlock.Load()
	if last == 0 {
		// No block yet; activateLocked sets last = now, so this only
		// triggers if a tick races MobileStart. Skip.
		return
	}
	elapsed := time.Now().UnixMilli() - last
	if elapsed < policy.SlowLeaderTimeoutMs {
		return // active producer is still healthy
	}

	// "Rotation is fast" check: if the committee-wide cycle is shorter
	// than the slow-leader timeout, the active node will be re-elected
	// soon enough that switching to a different producer wastes more
	// than waiting. Stay put.
	if policy.RotationCycleMs > 0 && policy.RotationCycleMs <= policy.SlowLeaderTimeoutMs {
		return
	}

	// Rotate.
	if err := v.rotateNextLocked(); err != nil {
		// Best-effort: log via simpleLog and try again next tick.
		simpleLog("rotation failed", "err", err.Error())
	}
}

// MobileGetStats returns the verifier statistics as a JSON string.
func MobileGetStats() string {
	mobileMu.Lock()
	v := mobileSingleton
	mobileMu.Unlock()
	if v == nil {
		return `{"blocks_verified":0,"success_count":0,"failure_count":0,"total_verify_ms":0,"success_rate":0,"avg_time_ms":0}`
	}
	snap := v.stats.snapshot()
	b, _ := json.Marshal(&snap)
	return string(b)
}

// MobileGetLastVerifyInfo returns JSON describing the most recent
// verification attempt, or "{}" if none yet.
func MobileGetLastVerifyInfo() string {
	mobileMu.Lock()
	defer mobileMu.Unlock()
	if mobileSingleton == nil || mobileSingleton.lastInfo == nil {
		return "{}"
	}
	b, _ := json.Marshal(mobileSingleton.lastInfo)
	return string(b)
}

// MobileVerifyPacket synchronously re-executes a single StreamPacket
// using the package-level code cache, then BLS-signs the result.
//
// Returns a JSON object with the same shape as MobileGetLastVerifyInfo.
func MobileVerifyPacket(packetData []byte) string {
	mobileMu.Lock()
	v := mobileSingleton
	mobileMu.Unlock()
	if v == nil {
		return `{"success":false,"error_msg":"verifier not initialized"}`
	}

	t0 := time.Now()
	pkt, err := DecodeStreamPacket(packetData)
	if err != nil {
		return v.recordFailure(t0, len(packetData), 0, types.Hash{}, err)
	}

	// Dedup short-circuit: if a producer engine already signed this
	// (number, hash) via the WebSocket loop, return the cached payload
	// instead of re-running the EVM.
	if blockNumber, herr := pkt.HeaderInfo(); herr == nil {
		if skip, cached := callShouldSkip(blockNumber, pkt.BlockHash); skip {
			return string(cached)
		}
	}

	res, err := ExecuteAndVerifyV2(pkt, globalCodeCache, nil)
	if err != nil {
		return v.recordFailure(t0, len(packetData), 0, pkt.BlockHash, err)
	}

	sk, sigErr := decodeSecretKey(v.privKeyHex)
	if sigErr != nil {
		return v.recordFailure(t0, len(packetData), int64(res.BlockNumber), pkt.BlockHash, sigErr)
	}
	receipt := VerificationReceipt{
		BlockHash:            res.BlockHash,
		BlockNumber:          res.BlockNumber,
		ComputedReceiptsRoot: res.ComputedReceiptsRoot,
		TimestampMs:          uint64(time.Now().UnixMilli()),
	}
	if pubBytes, derr := hex.DecodeString(v.pubkey); derr == nil {
		copy(receipt.VerifierPubkey[:], pubBytes)
	}
	copy(receipt.Signature[:], sk.Sign(receipt.SigningMessage()).Marshal())

	verifyMs := time.Since(t0).Milliseconds()
	info := &mobileLastInfo{
		BlockNumber:          int64(res.BlockNumber),
		BlockHash:            hexutil.Encode(res.BlockHash[:]),
		ComputedReceiptsRoot: hexutil.Encode(res.ComputedReceiptsRoot[:]),
		TxCount:              res.TxCount,
		WitnessReadLogLen:    res.WitnessReadLogLen,
		UncachedBytecodes:    res.UncachedBytecodes,
		PacketSizeBytes:      len(packetData),
		VerifyTimeMs:         verifyMs,
		Success:              true,
		Signature:            hexutil.Encode(receipt.Signature[:]),
	}

	mobileMu.Lock()
	v.lastInfo = info
	v.stats.mu.Lock()
	v.stats.BlocksVerified++
	v.stats.SuccessCount++
	v.stats.TotalVerifyMs += verifyMs
	v.stats.mu.Unlock()
	mobileMu.Unlock()

	out, _ := json.Marshal(info)
	// Populate dedup so a subsequent producer push of the same block
	// short-circuits inside vertify/verifyV2 in the WebSocket loop.
	callCache(res.BlockNumber, res.BlockHash, out)
	return string(out)
}

// recordFailure stores a failed verification attempt in lastInfo + stats
// and returns the JSON-encoded result.
func (v *mobileVerifier) recordFailure(started time.Time, packetSize int, blockNumber int64, blockHash types.Hash, err error) string {
	if err == nil {
		err = errors.New("unknown verification error")
	}
	verifyMs := time.Since(started).Milliseconds()
	info := &mobileLastInfo{
		BlockNumber:     blockNumber,
		BlockHash:       hexutil.Encode(blockHash[:]),
		PacketSizeBytes: packetSize,
		VerifyTimeMs:    verifyMs,
		Success:         false,
		ErrorMsg:        err.Error(),
	}
	mobileMu.Lock()
	v.lastInfo = info
	v.stats.mu.Lock()
	v.stats.BlocksVerified++
	v.stats.FailureCount++
	v.stats.TotalVerifyMs += verifyMs
	v.stats.mu.Unlock()
	mobileMu.Unlock()
	out, _ := json.Marshal(info)
	return string(out)
}

// MobileFree releases the package-level verifier. Safe to call when
// already freed. Stops the active producer + the rotation monitor and
// clears the verify hooks.
func MobileFree() string {
	mobileMu.Lock()
	defer mobileMu.Unlock()
	if mobileSingleton != nil {
		mobileSingleton.stopMonitorLocked()
		for _, p := range mobileSingleton.producers {
			_ = p.engine.Stop()
		}
	}
	mobileSingleton = nil
	ClearVerifyHooks()
	return ""
}

// MobileVersion returns the wire protocol version this binding speaks.
// Use it from mobile apps to detect mismatches against an IDC server.
func MobileVersion() string {
	return mobileVersionString
}

// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// MobileVerifyMiner runs the whole mobile verification loop inside the
// SDK. The app starts it from a long-lived host (an Android foreground
// service, an iOS background task) and only reads its status: the loop
// does not depend on the UI, or on the app's Dart runtime, staying alive.
//
// Once started it keeps the device registered, verifies every new head
// whose packet the node holds (re-execute, sign, submit), and later reads
// each verified block's certificate to count the blocks the device's
// receipt helped certify. Transient failures are logged and retried; only
// Stop ends the loop.

package evmsdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/n42blockchain/N42/params"
)

// Miner phases, as Status reports them.
const (
	MinerPhaseIdle        = "idle"
	MinerPhaseConnecting  = "connecting"
	MinerPhaseRegistering = "registering"
	MinerPhaseMining      = "mining"
	MinerPhaseRetrying    = "retrying"
)

const minerLogLimit = 100

// minerTiming paces the loop.
type minerTiming struct {
	poll         time.Duration // head polling
	registerPoll time.Duration // waiting for the epoch to commit the key
	retry        time.Duration // after an error outside a block
	certDelay    time.Duration // a block's window has closed by then
	certRecheck  time.Duration
	certTimeout  time.Duration // uncertified after this long
	callTimeout  time.Duration // each call to the node
}

var defaultMinerTiming = minerTiming{
	poll:         time.Second,
	registerPoll: 10 * time.Second,
	retry:        10 * time.Second,
	certDelay:    20 * time.Second,
	certRecheck:  10 * time.Second,
	certTimeout:  2 * time.Minute,
	callTimeout:  30 * time.Second,
}

// minerCert is the part of a certificate the miner reads.
type minerCert struct {
	ReceiptsRoot string `json:"receiptsRoot"`
	Signers      int    `json:"signers"`
}

// minerNode is the node as the loop sees it: the real client in the app,
// a scripted one in tests.
type minerNode interface {
	register(ctx context.Context) (uint32, bool, error)
	latest(ctx context.Context) (*MobileVerifyHead, error)
	verify(ctx context.Context, blockHashHex string) (*mobileVerifyResult, error)
	certs(ctx context.Context, blockHashHex string) ([]minerCert, error)
}

type clientNode struct {
	client *MobileVerifyClient
	codes  *CodeCache
	cfg    *params.ChainConfig
}

func (n *clientNode) register(ctx context.Context) (uint32, bool, error) {
	return n.client.RegisterStatus(ctx)
}

func (n *clientNode) latest(ctx context.Context) (*MobileVerifyHead, error) {
	return n.client.Latest(ctx)
}

func (n *clientNode) verify(ctx context.Context, blockHashHex string) (*mobileVerifyResult, error) {
	return verifyAndSubmit(ctx, n.client, n.codes, n.cfg, blockHashHex)
}

func (n *clientNode) certs(ctx context.Context, blockHashHex string) ([]minerCert, error) {
	raw, err := n.client.Cert(ctx, blockHashHex)
	if errors.Is(err, ErrMobileVerifyNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var certs []minerCert
	if err := json.Unmarshal(raw, &certs); err != nil {
		return nil, fmt.Errorf("mobileverify: cert: %w", err)
	}
	return certs, nil
}

type pendingCert struct {
	verified  *mobileVerifyResult
	at        time.Time
	nextCheck time.Time
}

// minerStatus is the JSON Status returns.
type minerStatus struct {
	Running     bool                `json:"running"`
	Phase       string              `json:"phase"`
	Endpoint    string              `json:"endpoint"`
	Pubkey      string              `json:"pubkey"`
	DeviceIndex *uint32             `json:"deviceIndex,omitempty"`
	Verified    int                 `json:"verified"`
	Failed      int                 `json:"failed"`
	Certified   int                 `json:"certified"`
	Uncertified int                 `json:"uncertified"`
	LastBlock   *mobileVerifyResult `json:"lastBlock,omitempty"`
	LastSigners int                 `json:"lastSigners"`
	LastError   string              `json:"lastError,omitempty"`
	StartedAtMs int64               `json:"startedAtMs,omitempty"`
	Log         []string            `json:"log"`
}

// MobileVerifyMiner runs the verification loop against one node. Its
// methods are safe for concurrent use.
type MobileVerifyMiner struct {
	endpoint string
	pubkey   string
	node     minerNode
	timing   minerTiming
	now      func() time.Time

	mu      sync.Mutex
	st      minerStatus
	pending []*pendingCert
	last    uint64 // highest block number tried; 0 before the first
	tried   bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewMobileVerifyMiner binds a device identity to a node's mobileverify
// base URL. chainName must name the chain the node runs (see
// NewMobileVerifySession).
func NewMobileVerifyMiner(baseURL, privKeyHex, chainName string) (*MobileVerifyMiner, error) {
	cfg := params.ChainConfigByChainName(chainName)
	if cfg == nil {
		return nil, fmt.Errorf("mobileverify miner: unknown chain %q", chainName)
	}
	baseURL = strings.TrimRight(baseURL, "/")
	client, err := NewMobileVerifyClient(baseURL, privKeyHex)
	if err != nil {
		return nil, err
	}
	pub := client.Pubkey()
	node := &clientNode{client: client, codes: NewCodeCache(mobileVerifySessionCodeCache), cfg: cfg}
	return newMobileVerifyMiner(baseURL, fmt.Sprintf("%x", pub[:]), node, defaultMinerTiming, time.Now), nil
}

func newMobileVerifyMiner(endpoint, pubkey string, node minerNode, timing minerTiming, now func() time.Time) *MobileVerifyMiner {
	m := &MobileVerifyMiner{endpoint: endpoint, pubkey: pubkey, node: node, timing: timing, now: now}
	m.st = minerStatus{Phase: MinerPhaseIdle, Endpoint: endpoint, Pubkey: pubkey, Log: []string{}}
	return m
}

// PubkeyHex returns the device's 48-byte BLS public key hex.
func (m *MobileVerifyMiner) PubkeyHex() string { return m.pubkey }

// SetIntervalsMs sets how often the head is polled, how often an
// uncommitted registration is checked, and how long after verifying a
// block its certificate is first read; 0 or less keeps a value. It fails
// while the loop runs.
func (m *MobileVerifyMiner) SetIntervalsMs(pollMs, registerPollMs, certDelayMs int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		return errors.New("mobileverify miner: set intervals before Start")
	}
	for _, v := range []struct {
		ms  int64
		dst *time.Duration
	}{{pollMs, &m.timing.poll}, {registerPollMs, &m.timing.registerPoll}, {certDelayMs, &m.timing.certDelay}} {
		if v.ms > 0 {
			*v.dst = time.Duration(v.ms) * time.Millisecond
		}
	}
	return nil
}

// Start starts the loop; it does nothing while the loop runs. Counters
// carry over from an earlier run of the same miner.
func (m *MobileVerifyMiner) Start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.done = make(chan struct{})
	m.st.Running = true
	m.st.LastError = ""
	m.st.StartedAtMs = m.now().UnixMilli()
	go m.run(ctx, m.done)
}

// Stop ends the loop, aborting the call in flight, and returns once it
// has unwound.
func (m *MobileVerifyMiner) Stop() {
	m.mu.Lock()
	cancel, done := m.cancel, m.done
	m.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

// Running reports whether the loop runs.
func (m *MobileVerifyMiner) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cancel != nil
}

// Status returns the loop's state as JSON: {"running","phase","endpoint",
// "pubkey","deviceIndex","verified","failed","certified","uncertified",
// "lastBlock":{...},"lastSigners","lastError","startedAtMs","log":[...]},
// log being the newest 100 lines, oldest first.
func (m *MobileVerifyMiner) Status() string {
	m.mu.Lock()
	st := m.st
	st.Log = append([]string(nil), m.st.Log...)
	m.mu.Unlock()
	b, err := json.Marshal(st)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func (m *MobileVerifyMiner) logf(format string, args ...any) {
	line := m.now().Format("15:04:05") + " " + fmt.Sprintf(format, args...)
	m.mu.Lock()
	m.st.Log = append(m.st.Log, line)
	if n := len(m.st.Log); n > minerLogLimit {
		m.st.Log = append([]string(nil), m.st.Log[n-minerLogLimit:]...)
	}
	m.mu.Unlock()
}

func (m *MobileVerifyMiner) update(f func(st *minerStatus)) {
	m.mu.Lock()
	f(&m.st)
	m.mu.Unlock()
}

func (m *MobileVerifyMiner) setPhase(phase string) {
	m.update(func(st *minerStatus) { st.Phase = phase })
}

// sleep waits d or until ctx ends, reporting whether it waited it out.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (m *MobileVerifyMiner) run(ctx context.Context, done chan struct{}) {
	defer func() {
		m.mu.Lock()
		m.cancel, m.done = nil, nil
		m.st.Running = false
		m.st.Phase = MinerPhaseIdle
		m.mu.Unlock()
		close(done)
	}()
	for ctx.Err() == nil {
		err := m.session(ctx)
		if ctx.Err() != nil {
			return
		}
		m.update(func(st *minerStatus) {
			st.Phase = MinerPhaseRetrying
			st.LastError = err.Error()
		})
		m.logf("error: %v; retrying in %s", err, m.timing.retry)
		if !sleep(ctx, m.timing.retry) {
			return
		}
	}
}

// call bounds one call to the node.
func (m *MobileVerifyMiner) call(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, m.timing.callTimeout)
}

// session registers and then mines until ctx ends or a call outside a
// block fails, which it returns.
func (m *MobileVerifyMiner) session(ctx context.Context) error {
	m.setPhase(MinerPhaseConnecting)
	register := func() (bool, error) {
		cctx, cancel := m.call(ctx)
		defer cancel()
		idx, committed, err := m.node.register(cctx)
		if err != nil {
			return false, err
		}
		m.update(func(st *minerStatus) { st.DeviceIndex = &idx })
		if !committed {
			m.logf("registered as device %d, waiting for the epoch", idx)
		} else {
			m.logf("committed as device %d", idx)
		}
		return committed, nil
	}
	committed, err := register()
	if err != nil {
		return err
	}
	m.setPhase(MinerPhaseRegistering)
	for !committed {
		if !sleep(ctx, m.timing.registerPoll) {
			return ctx.Err()
		}
		if committed, err = register(); err != nil {
			return err
		}
	}
	m.setPhase(MinerPhaseMining)
	for {
		if err := m.tick(ctx); err != nil {
			return err
		}
		if !sleep(ctx, m.timing.poll) {
			return ctx.Err()
		}
	}
}

// tick verifies the head if it is new and its packet is there, then reads
// the certificates that are due. A failed block is counted, not returned.
func (m *MobileVerifyMiner) tick(ctx context.Context) error {
	cctx, cancel := m.call(ctx)
	head, err := m.node.latest(cctx)
	cancel()
	if err != nil {
		return err
	}
	if head.Packet && (!m.tried || head.Number > m.last) {
		m.tried, m.last = true, head.Number
		cctx, cancel := m.call(ctx)
		v, err := m.node.verify(cctx, head.Hash)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			m.update(func(st *minerStatus) {
				st.Failed++
				st.LastError = err.Error()
			})
			m.logf("block %d failed: %v", head.Number, err)
		} else {
			now := m.now()
			m.pending = append(m.pending, &pendingCert{verified: v, at: now, nextCheck: now.Add(m.timing.certDelay)})
			m.update(func(st *minerStatus) {
				st.Verified++
				st.LastBlock = v
			})
			m.logf("verified block %d (%d txs, %d ms)", v.BlockNumber, v.Txs, v.ElapsedMs)
		}
	}
	return m.checkCerts(ctx)
}

func (m *MobileVerifyMiner) checkCerts(ctx context.Context) error {
	now := m.now()
	kept := m.pending[:0]
	for i, p := range m.pending {
		if now.Before(p.nextCheck) {
			kept = append(kept, p)
			continue
		}
		cctx, cancel := m.call(ctx)
		certs, err := m.node.certs(cctx, p.verified.BlockHash)
		cancel()
		if err != nil {
			// Keep this and the unchecked rest for the next session.
			m.pending = append(kept, m.pending[i:]...)
			return err
		}
		signers := 0
		for _, c := range certs {
			if strings.EqualFold(c.ReceiptsRoot, p.verified.ReceiptsRoot) && c.Signers > signers {
				signers = c.Signers
			}
		}
		switch {
		case signers >= 1:
			m.update(func(st *minerStatus) {
				st.Certified++
				st.LastSigners = signers
			})
			m.logf("block %d certified, %d signers", p.verified.BlockNumber, signers)
		case now.Sub(p.at) >= m.timing.certTimeout:
			m.update(func(st *minerStatus) { st.Uncertified++ })
			m.logf("block %d not certified in time", p.verified.BlockNumber)
		default:
			p.nextCheck = now.Add(m.timing.certRecheck)
			kept = append(kept, p)
		}
	}
	m.pending = kept
	return nil
}

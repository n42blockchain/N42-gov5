package evmsdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMinerLogClockIsExplicitUTC(t *testing.T) {
	m := &MobileVerifyMiner{now: func() time.Time {
		return time.Date(2026, 11, 1, 23, 59, 58, 0, time.FixedZone("test", -5*60*60))
	}}
	m.logf("verified block %d (%d txs, %d ms)", 42, 3, 15)
	var status minerStatus
	if err := json.Unmarshal([]byte(m.Status()), &status); err != nil {
		t.Fatal(err)
	}
	if !status.LogTimeUTC || len(status.Log) != 1 || status.Log[0] != "04:59:58 verified block 42 (3 txs, 15 ms)" {
		t.Fatalf("ambiguous log clock: %+v", status)
	}
}

// scriptedNode plays a node: heads are served in order (the last one
// repeats), the registration commits on the commitOn-th call, certs maps a
// block hash to its certificates and hashes in failing do not verify.
type scriptedNode struct {
	mu           sync.Mutex
	heads        []MobileVerifyHead
	commitOn     int
	certMap      map[string][]minerCert
	failing      map[string]bool
	latestErrors int
	blockVerify  bool // verify waits for its context to end

	registers  int
	latests    int
	certCalls  int
	verified   []string
	verifyCtxs int
}

func (n *scriptedNode) register(context.Context) (uint32, bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.registers++
	return 5, n.registers >= max(n.commitOn, 1), nil
}

func (n *scriptedNode) latest(context.Context) (*MobileVerifyHead, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.latestErrors > 0 {
		n.latestErrors--
		return nil, errors.New("node unreachable")
	}
	i := min(n.latests, len(n.heads)-1)
	n.latests++
	h := n.heads[i]
	return &h, nil
}

func (n *scriptedNode) verify(ctx context.Context, hash string) (*mobileVerifyResult, error) {
	n.mu.Lock()
	n.verified = append(n.verified, hash)
	block, fail := n.blockVerify, n.failing[hash]
	n.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if fail {
		return nil, errors.New("receipts root mismatch")
	}
	var number uint64
	fmt.Sscanf(hash, "0x%d", &number)
	return &mobileVerifyResult{BlockNumber: number, BlockHash: hash, ReceiptsRoot: "0xroot" + hash, Txs: 1, Index: 5, ElapsedMs: 7}, nil
}

func (n *scriptedNode) certs(_ context.Context, hash string) ([]minerCert, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.certCalls++
	return n.certMap[hash], nil
}

func (n *scriptedNode) snapshot() (registers, latests, certCalls int, verified []string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.registers, n.latests, n.certCalls, append([]string(nil), n.verified...)
}

func head(number uint64, packet bool) MobileVerifyHead {
	return MobileVerifyHead{Number: number, Hash: fmt.Sprintf("0x%d", number), Packet: packet}
}

var fastTiming = minerTiming{
	poll:         time.Millisecond,
	registerPoll: time.Millisecond,
	retry:        time.Millisecond,
	certDelay:    2 * time.Millisecond,
	certRecheck:  time.Millisecond,
	certTimeout:  time.Hour,
	callTimeout:  5 * time.Second,
}

func newTestMiner(node minerNode, timing minerTiming) *MobileVerifyMiner {
	return newMobileVerifyMiner("http://node", "bb", node, timing, time.Now)
}

func status(t *testing.T, m *MobileVerifyMiner) minerStatus {
	t.Helper()
	var st minerStatus
	if err := json.Unmarshal([]byte(m.Status()), &st); err != nil {
		t.Fatal(err)
	}
	return st
}

// waitFor polls cond until it holds, failing the test after 5 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestMinerWaitsForTheRegistrationToCommit(t *testing.T) {
	node := &scriptedNode{heads: []MobileVerifyHead{head(100, true)}, commitOn: 3}
	m := newTestMiner(node, fastTiming)
	m.Start()
	defer m.Stop()
	waitFor(t, "the head to be verified", func() bool { return status(t, m).Verified == 1 })
	registers, _, _, verified := node.snapshot()
	if registers != 3 {
		t.Fatalf("registered %d times, want 3", registers)
	}
	st := status(t, m)
	if st.Phase != MinerPhaseMining || st.DeviceIndex == nil || *st.DeviceIndex != 5 {
		t.Fatalf("status %+v", st)
	}
	if len(verified) != 1 || verified[0] != "0x100" {
		t.Fatalf("verified %v", verified)
	}
}

func TestMinerVerifiesEachNewHeadOnceWhenItsPacketIsThere(t *testing.T) {
	node := &scriptedNode{heads: []MobileVerifyHead{
		head(99, false), head(100, true), head(100, true), head(101, true),
		head(99, true), head(102, false), head(102, true),
	}}
	m := newTestMiner(node, fastTiming)
	m.Start()
	waitFor(t, "the script to run out", func() bool { _, latests, _, _ := node.snapshot(); return latests > 10 })
	m.Stop()
	_, _, _, verified := node.snapshot()
	if strings.Join(verified, ",") != "0x100,0x101,0x102" {
		t.Fatalf("verified %v", verified)
	}
	st := status(t, m)
	if st.Verified != 3 || st.Failed != 0 || st.LastBlock == nil || st.LastBlock.BlockNumber != 102 {
		t.Fatalf("status %+v", st)
	}
}

func TestMinerCountsAFailedBlockAndGoesOn(t *testing.T) {
	node := &scriptedNode{
		heads:   []MobileVerifyHead{head(100, true), head(101, true), head(102, true)},
		failing: map[string]bool{"0x101": true},
	}
	m := newTestMiner(node, fastTiming)
	m.Start()
	defer m.Stop()
	waitFor(t, "block 102", func() bool { return status(t, m).Verified == 2 })
	registers, _, _, verified := node.snapshot()
	st := status(t, m)
	if strings.Join(verified, ",") != "0x100,0x101,0x102" || st.Failed != 1 || registers != 1 {
		t.Fatalf("verified %v, status %+v, registers %d", verified, st, registers)
	}
	if !strings.Contains(st.LastError, "receipts root mismatch") {
		t.Fatalf("last error %q", st.LastError)
	}
}

func TestMinerCountsCertifiedBlocksAndGivesUpOnTheRest(t *testing.T) {
	node := &scriptedNode{
		heads: []MobileVerifyHead{head(100, true), head(101, true)},
		certMap: map[string][]minerCert{
			"0x100": {{ReceiptsRoot: "0xother", Signers: 1}, {ReceiptsRoot: "0xroot0x100", Signers: 3}},
			// Certified, but over a root this device did not attest.
			"0x101": {{ReceiptsRoot: "0xother", Signers: 4}},
		},
	}
	timing := fastTiming
	timing.certTimeout = 30 * time.Millisecond
	m := newTestMiner(node, timing)
	m.Start()
	defer m.Stop()
	waitFor(t, "both blocks to settle", func() bool { st := status(t, m); return st.Certified+st.Uncertified == 2 })
	st := status(t, m)
	if st.Certified != 1 || st.Uncertified != 1 || st.LastSigners != 3 {
		t.Fatalf("status %+v", st)
	}
}

func TestMinerDoesNotGiveUpBeforeTheTimeout(t *testing.T) {
	node := &scriptedNode{heads: []MobileVerifyHead{head(100, true)}}
	m := newTestMiner(node, fastTiming)
	m.Start()
	waitFor(t, "certificate checks", func() bool { _, _, certCalls, _ := node.snapshot(); return certCalls >= 5 })
	m.Stop()
	if st := status(t, m); st.Certified != 0 || st.Uncertified != 0 {
		t.Fatalf("status %+v", st)
	}
}

func TestMinerReconnectsAfterAnErrorOutsideABlock(t *testing.T) {
	node := &scriptedNode{heads: []MobileVerifyHead{head(100, true)}, latestErrors: 1}
	m := newTestMiner(node, fastTiming)
	m.Start()
	defer m.Stop()
	waitFor(t, "the head to be verified", func() bool { return status(t, m).Verified == 1 })
	registers, _, _, _ := node.snapshot()
	st := status(t, m)
	if registers != 2 || !strings.Contains(st.LastError, "node unreachable") {
		t.Fatalf("registers %d, status %+v", registers, st)
	}
	if !strings.Contains(strings.Join(st.Log, "\n"), "retrying") {
		t.Fatalf("log %v", st.Log)
	}
}

func TestMinerStopAbortsTheCallInFlight(t *testing.T) {
	node := &scriptedNode{heads: []MobileVerifyHead{head(100, true)}, blockVerify: true}
	m := newTestMiner(node, fastTiming)
	m.Start()
	waitFor(t, "the verify call", func() bool { _, _, _, v := node.snapshot(); return len(v) == 1 })
	stopped := make(chan struct{})
	go func() { m.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not abort the verify call")
	}
	st := status(t, m)
	if m.Running() || st.Running || st.Phase != MinerPhaseIdle || st.Failed != 0 {
		t.Fatalf("after Stop: running %v, status %+v", m.Running(), st)
	}
}

func TestMinerStartsOnceAndRestartsAfterStop(t *testing.T) {
	node := &scriptedNode{heads: []MobileVerifyHead{head(100, true)}}
	m := newTestMiner(node, fastTiming)
	m.Start()
	m.Start()
	waitFor(t, "the head to be verified", func() bool { return status(t, m).Verified == 1 })
	if err := m.SetIntervalsMs(5, 5, 5); err == nil {
		t.Fatal("intervals changed while running")
	}
	if registers, _, _, _ := node.snapshot(); registers != 1 {
		t.Fatalf("two loops: registered %d times", registers)
	}
	m.Stop()
	m.Stop()
	if err := m.SetIntervalsMs(2, 0, 0); err != nil {
		t.Fatal(err)
	}
	m.Start()
	defer m.Stop()
	waitFor(t, "the second registration", func() bool { r, _, _, _ := node.snapshot(); return r == 2 })
	// The same head is not verified again, and counters carry over.
	time.Sleep(20 * time.Millisecond)
	if _, _, _, verified := node.snapshot(); len(verified) != 1 {
		t.Fatalf("verified %v", verified)
	}
	if st := status(t, m); st.Verified != 1 || !st.Running {
		t.Fatalf("status %+v", st)
	}
}

func TestMinerLogIsBounded(t *testing.T) {
	m := newTestMiner(&scriptedNode{}, fastTiming)
	for i := range minerLogLimit + 10 {
		m.logf("line %d", i)
	}
	st := status(t, m)
	if len(st.Log) != minerLogLimit || !strings.HasSuffix(st.Log[0], "line 10") {
		t.Fatalf("log has %d lines, first %q", len(st.Log), st.Log[0])
	}
}

func TestNewMobileVerifyMinerRejectsAnUnknownChain(t *testing.T) {
	if _, err := NewMobileVerifyMiner("http://node", "00", "no-such-chain"); err == nil {
		t.Fatal("unknown chain accepted")
	}
}

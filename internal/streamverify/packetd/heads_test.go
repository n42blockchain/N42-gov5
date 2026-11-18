// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package packetd_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/streamverify/packetd"
	"github.com/n42blockchain/N42/internal/streamverify/packetd/packetdtest"
)

// fakeHeads is a node's WebSocket endpoint that answers eth_subscribe
// ["newHeads"] and sends one notification per value pushed on push. Each
// connection is closed after drop notifications when drop > 0.
type fakeHeads struct {
	push chan struct{}
	drop int

	mu    sync.Mutex
	conns int
}

func (h *fakeHeads) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	up := websocket.Upgrader{}
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	h.mu.Lock()
	h.conns++
	h.mu.Unlock()
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params []string        `json:"params"`
	}
	if err := conn.ReadJSON(&req); err != nil || req.Method != "eth_subscribe" || len(req.Params) != 1 || req.Params[0] != "newHeads" {
		return
	}
	_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": "0xfeed"})
	sent := 0
	for range h.push {
		// A notification for another subscription is not a head.
		_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "method": "eth_subscription",
			"params": map[string]any{"subscription": "0xother", "result": map[string]any{}}})
		if err := conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "method": "eth_subscription",
			"params": map[string]any{"subscription": "0xfeed", "result": map[string]any{"number": "0x1"}}}); err != nil {
			return
		}
		sent++
		if h.drop > 0 && sent == h.drop {
			return
		}
	}
}

func wsURL(s *httptest.Server) string { return "ws" + strings.TrimPrefix(s.URL, "http") }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Every head of the subscription wakes the follower; a notification for
// another subscription does not; a dropped connection is redialled.
func TestHeadSubscriberWakesOnEveryHeadAndReconnects(t *testing.T) {
	fake := &fakeHeads{push: make(chan struct{}), drop: 2}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wake := make(chan struct{}, 1)
	sub := &packetd.HeadSubscriber{URL: wsURL(srv), Backoff: 10 * time.Millisecond}
	done := make(chan struct{})
	go func() { sub.Run(ctx, wake); close(done) }()

	waitFor(t, "the first subscription", func() bool { return sub.Connects() == 1 })
	for i := 1; i <= 3; i++ {
		fake.push <- struct{}{}
		select {
		case <-wake:
		case <-time.After(10 * time.Second):
			t.Fatalf("head %d woke nothing", i)
		}
		waitFor(t, "the head count", func() bool { return sub.Heads() == uint64(i) })
	}
	// The server closed the first connection after two heads; the third head
	// came over the second subscription.
	if got := sub.Connects(); got != 2 {
		t.Fatalf("connects %d, want 2 (one redial)", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// With a poll far longer than the test, an idle follower builds the next
// block's packet as soon as the subscription announces it.
func TestFollowerBuildsOnNewHeadWithoutWaitingForThePoll(t *testing.T) {
	base := fixtureCases(t)[0] // empty block, mainnet rules: no state reads
	var chain []packetdtest.Block
	parent := types.Hash{0x01}
	for i := uint64(1); i <= 2; i++ {
		h := block.CopyHeader(base.hdr)
		h.Number = uint256.NewInt(i)
		h.ParentHash = parent
		h.ResetHashCache()
		parent = h.Hash()
		chain = append(chain, packetdtest.Block{Header: h})
	}
	node := &packetdtest.Node{First: 1, Blocks: chain[:1]}
	rpc := httptest.NewServer(node)
	defer rpc.Close()
	fake := &fakeHeads{push: make(chan struct{})}
	ws := httptest.NewServer(fake)
	defer ws.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	wake := make(chan struct{}, 1)
	sub := &packetd.HeadSubscriber{URL: wsURL(ws)}
	go sub.Run(ctx, wake)
	waitFor(t, "the subscription", func() bool { return sub.Connects() == 1 })

	var (
		mu       sync.Mutex
		got      []uint64
		appended time.Time
		latency  time.Duration
	)
	f := &packetd.Follower{
		RPC:    packetd.NewClient(rpc.URL, 5*time.Second),
		Chain:  base.cfg,
		OutDir: t.TempDir(),
		Poll:   time.Minute,
		Wake:   wake,
		Built: func(n uint64, _ *packetd.Block, _ *packetd.Built, _ *packetd.IngestResult) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, n)
			switch n {
			case 1:
				// Let the follower go idle on its one-minute poll, then
				// land block 2 and announce it.
				go func() {
					time.Sleep(200 * time.Millisecond)
					mu.Lock()
					appended = time.Now()
					mu.Unlock()
					node.Append(chain[1])
					fake.push <- struct{}{}
				}()
			case 2:
				latency = time.Since(appended)
				cancel()
			}
		},
	}
	_ = f.Run(ctx, 1)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("built %v, want blocks 1 and 2", got)
	}
	if latency > 5*time.Second {
		t.Fatalf("block 2 took %v after its head: the follower waited for the poll", latency)
	}
	t.Logf("block 2 packet %v after its newHeads notification", latency)
}

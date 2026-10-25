package sharedvalidator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
)

func recoveryProducer(t *testing.T) *Producer {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	p := &Producer{ctx: ctx, cancel: cancel, done: make(chan struct{}), replica: &Replica{identity: sharedexec.Identity{Version: 1, Namespace: "drain-fixture", ChainID: "42", GenesisHash: types.Hash{1}}}, ownerQueue: &OwnerQueueBinding{Epoch: [16]byte{1}, ManifestHash: types.Hash{2}}}
	t.Cleanup(p.Stop)
	return p
}
func TestRecoveryOnlyBarrierJoinsFreshBuildAndCancellationLatches(t *testing.T) {
	p := recoveryProducer(t)
	p.produceMu.Lock()
	var once sync.Once
	unlock := func() { once.Do(p.produceMu.Unlock) }
	defer unlock()
	if err := p.admitFreshBuild(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := p.EnterRecoveryOnly(ctx); done <- err }()
	deadline := time.Now().Add(time.Second)
	for {
		p.mu.Lock()
		latched := p.recoveryOnly
		p.mu.Unlock()
		if latched {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("mode did not latch")
		}
		time.Sleep(time.Millisecond)
	}
	if err := p.admitFreshBuild(); !errors.Is(err, ErrRecoveryOnly) {
		t.Fatalf("fresh build admitted: %v", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel fabricated ACK: %v", err)
	}
	p.mu.Lock()
	ack := p.drainAcknowledged
	barrier := p.drainDone
	p.mu.Unlock()
	if ack {
		t.Fatal("ACK before current Produce returned")
	}
	unlock()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			state, err := p.EnterRecoveryOnly(context.Background())
			if err != nil || !state.RecoveryOnly || !state.DrainAcknowledged || state.FreshBuilds != 1 {
				t.Errorf("invalid shared barrier: %+v %v", state, err)
			}
		}()
	}
	wg.Wait()
	p.mu.Lock()
	same := barrier == p.drainDone
	p.mu.Unlock()
	if !same {
		t.Fatal("duplicate requests created another barrier")
	}
	for i := 0; i < 8; i++ {
		if !errors.Is(p.admitFreshBuild(), ErrRecoveryOnly) {
			t.Fatal("fresh build after ACK")
		}
	}
}
func TestRecoveryOnlyHTTPBindingBeforeModeChange(t *testing.T) {
	for _, field := range []string{"identity", "epoch", "manifest"} {
		t.Run(field, func(t *testing.T) {
			p := recoveryProducer(t)
			a, _ := NewAPI(p.replica, p)
			request := RecoveryOnlyRequest{p.replica.identity, p.ownerQueue.Epoch, p.ownerQueue.ManifestHash}
			switch field {
			case "identity":
				request.Identity.ChainID = "43"
			case "epoch":
				request.Epoch[0]++
			case "manifest":
				request.ManifestHash[0]++
			}
			raw, _ := json.Marshal(request)
			w := httptest.NewRecorder()
			a.ServeHTTP(w, httptest.NewRequest("POST", "/v1/recovery-only", bytes.NewReader(raw)))
			if w.Code == 200 {
				t.Fatal("wrong binding acknowledged")
			}
			p.mu.Lock()
			changed := p.recoveryOnly || p.drainDone != nil
			p.mu.Unlock()
			if changed {
				t.Fatal("wrong binding changed mode")
			}
		})
	}
	p := recoveryProducer(t)
	a, _ := NewAPI(p.replica, p)
	request := RecoveryOnlyRequest{p.replica.identity, p.ownerQueue.Epoch, p.ownerQueue.ManifestHash}
	raw, _ := json.Marshal(request)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, httptest.NewRequest("POST", "/v1/recovery-only", bytes.NewReader(raw)))
	var ack RecoveryOnlyResult
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &ack) != nil || ack.RecoveryOnlyRequest != request || !ack.DrainAcknowledged || !ack.RecoveryOnly || ack.FreshBuilds != 0 {
		t.Fatalf("bad acknowledgement %s", w.Body.String())
	}
}
func TestRecoveryOnlyStoppedProducerNeverAcknowledges(t *testing.T) {
	p := recoveryProducer(t)
	p.Stop()
	if _, err := p.EnterRecoveryOnly(context.Background()); err == nil {
		t.Fatal("stopped producer ACKed")
	}
}

func TestRecoveryOnlySpeculativeBarrierWaitsForOwnerDrain(t *testing.T) {
	p := recoveryProducer(t)
	p.started = true
	p.speculativeEnabled = true
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "owner.sock"))
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/drain-prepare-queue" {
			http.NotFound(w, r)
			return
		}
		var request sharedexec.DrainSpeculativeQueueRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Identity != p.replica.identity || request.Epoch != p.ownerQueue.Epoch {
			http.Error(w, "bad drain binding", http.StatusBadRequest)
			return
		}
		close(entered)
		<-release
		w.WriteHeader(http.StatusNoContent)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		_ = server.Close()
	})
	p.replica.client = sharedexec.NewClient(listener.Addr().String(), p.replica.identity)
	result := make(chan error, 1)
	go func() { _, err := p.EnterRecoveryOnly(context.Background()); result <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("recovery barrier did not reach bound owner drain")
	}
	p.PrepareSpeculativeBlockForView(types.Hash{3}, 1)
	p.mu.Lock()
	ack, busy := p.drainAcknowledged, p.speculativeBusy
	p.mu.Unlock()
	if ack || busy {
		t.Fatal("recovery barrier ACKed early or admitted a new hint")
	}
	select {
	case err := <-result:
		t.Fatalf("recovery barrier returned before owner drain: %v", err)
	default:
	}
	close(release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("recovery barrier did not ACK after owner drain")
	}
}

package sharedvalidator

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
)

var ErrRecoveryOnly = errors.New("producer recovery-only mode forbids fresh candidate builds")

type RecoveryOnlyState struct {
	RecoveryOnly      bool   `json:"recovery_only"`
	DrainAcknowledged bool   `json:"drain_acknowledged"`
	FreshBuilds       uint64 `json:"fresh_builds"`
}
type RecoveryOnlyRequest struct {
	Identity     sharedexec.Identity `json:"identity"`
	Epoch        [16]byte            `json:"epoch"`
	ManifestHash types.Hash          `json:"manifest_hash"`
}
type RecoveryOnlyResult struct {
	RecoveryOnlyRequest
	RecoveryOnlyState
}

// EnterRecoveryOnly is irreversible for this producer. Its shared barrier joins
// the current Produce, but deliberately keeps the service and future WAL / owner
// AppliedChild reproposals alive. An ACK is not a finality certificate.
func (p *Producer) EnterRecoveryOnly(ctx context.Context) (RecoveryOnlyState, error) {
	if ctx == nil {
		return RecoveryOnlyState{}, errors.New("recovery-only context required")
	}
	if err := ctx.Err(); err != nil {
		return RecoveryOnlyState{}, err
	}
	p.mu.Lock()
	if p.closed || p.ctx.Err() != nil || p.ownerQueue == nil {
		p.mu.Unlock()
		return RecoveryOnlyState{}, errors.New("recovery-only requires live owner-queue producer")
	}
	if p.drainDone == nil {
		p.recoveryOnly = true
		if p.speculativeCancel != nil {
			p.speculativeCancel()
		}
		p.drainDone = make(chan struct{})
		p.wg.Add(1)
		go p.finishRecoveryBarrier()
	}
	done := p.drainDone
	p.mu.Unlock()
	select {
	case <-ctx.Done():
		return RecoveryOnlyState{}, ctx.Err()
	case <-p.ctx.Done():
		return RecoveryOnlyState{}, p.ctx.Err()
	case <-done:
	}
	if err := ctx.Err(); err != nil {
		return RecoveryOnlyState{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.ctx.Err() != nil || !p.drainAcknowledged {
		return RecoveryOnlyState{}, errors.New("recovery-only barrier did not complete on live producer")
	}
	return p.recoveryStateLocked(), nil
}
func (p *Producer) finishRecoveryBarrier() {
	defer p.wg.Done()
	p.produceMu.Lock()
	defer p.produceMu.Unlock()
	// The owner tombstone is the remote half of the barrier. It rejects an
	// advisory HTTP request that reaches the owner after local cancellation,
	// and drains a worker installed before this call. No ACK precedes it.
	p.mu.Lock()
	binding := p.ownerQueue
	p.mu.Unlock()
	var drainErr error
	if binding != nil && p.speculativeEnabled {
		if p.replica == nil || p.replica.client == nil {
			drainErr = errors.New("speculative owner drain client unavailable")
		} else {
			drainErr = p.replica.client.DrainQueueSpeculative(p.ctx, binding.Epoch)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.drainAcknowledged = drainErr == nil && !p.closed && p.ctx.Err() == nil
	close(p.drainDone)
}
func (p *Producer) recoveryStateLocked() RecoveryOnlyState {
	return RecoveryOnlyState{p.recoveryOnly, p.drainAcknowledged, p.freshBuilds}
}
func (p *Producer) admitFreshBuild() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.ctx.Err() != nil {
		return errors.New("producer stopped before fresh build")
	}
	if p.recoveryOnly {
		return ErrRecoveryOnly
	}
	if p.freshBuilds == ^uint64(0) {
		return errors.New("fresh build counter overflow")
	}
	p.freshBuilds++
	return nil
}
func (a *API) serveRecoveryOnly(w http.ResponseWriter, r *http.Request, ctx context.Context) {
	var request RecoveryOnlyRequest
	if err := decodeAPI(w, r, 4096, &request); err != nil || request.Identity != a.replica.identity {
		http.Error(w, "invalid recovery-only identity/request", http.StatusBadRequest)
		return
	}
	binding := a.producer.OwnerQueueBinding()
	if binding == nil || request.Epoch != binding.Epoch || request.ManifestHash != binding.ManifestHash {
		http.Error(w, "recovery-only owner binding changed", http.StatusConflict)
		return
	}
	state, err := a.producer.EnterRecoveryOnly(ctx)
	if err != nil {
		http.Error(w, "recovery-only barrier unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(RecoveryOnlyResult{request, state})
}
func (c *APIClient) EnterRecoveryOnly(ctx context.Context, binding OwnerQueueBinding) (RecoveryOnlyResult, error) {
	request := RecoveryOnlyRequest{c.identity, binding.Epoch, binding.ManifestHash}
	var result RecoveryOnlyResult
	if binding.Epoch == ([16]byte{}) || binding.ManifestHash == (types.Hash{}) {
		return result, errors.New("recovery-only owner binding required")
	}
	if err := c.call(ctx, "/v1/recovery-only", request, &result); err != nil {
		return result, err
	}
	if result.RecoveryOnlyRequest != request || !result.RecoveryOnly || !result.DrainAcknowledged {
		return RecoveryOnlyResult{}, errors.New("recovery-only acknowledgement binding invalid")
	}
	return result, nil
}

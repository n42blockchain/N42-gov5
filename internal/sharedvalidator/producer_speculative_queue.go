package sharedvalidator

import (
	"context"
	"errors"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/internal/sharedexec"
)

// EnableSpeculativeOwnerQueue is a typed startup-only opt-in, after the
// explicit owner queue has been bound. It cannot change a live producer.
func (p *Producer) EnableSpeculativeOwnerQueue() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.started || p.ownerQueue == nil {
		return errors.New("speculative owner queue requires an unused bound producer")
	}
	p.speculativeEnabled = true
	return nil
}

// PrepareSpeculativeBlockForView is a consensus advisory, never a proposal.
// It leaves the producer's durable candidate and queue-request WAL untouched.
func (p *Producer) PrepareSpeculativeBlockForView(parentHash types.Hash, view hotstuff.ViewNumber) {
	if !p.speculativeEnabled || parentHash == (types.Hash{}) || view == 0 {
		return
	}
	p.mu.Lock()
	if p.closed || !p.started || p.recoveryOnly || p.ownerQueue == nil || p.speculativeBusy {
		p.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(p.ctx)
	p.speculativeBusy = true
	p.speculativeParent = parentHash
	p.speculativeView = view
	p.speculativeCancel = cancel
	p.wg.Add(1)
	p.mu.Unlock()
	go func() {
		defer p.wg.Done()
		defer cancel()
		defer func() {
			p.mu.Lock()
			p.speculativeBusy = false
			p.speculativeParent = types.Hash{}
			p.speculativeView = 0
			p.speculativeCancel = nil
			p.mu.Unlock()
		}()
		request, err := p.speculativeQueueRequest(ctx, parentHash, view)
		if err != nil {
			return
		}
		admitCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		_ = p.replica.client.PrepareQueueSpeculative(admitCtx, request)
	}()
}

func (p *Producer) speculativeQueueRequest(ctx context.Context, parentHash types.Hash, view hotstuff.ViewNumber) (sharedexec.QueueBuildRequest, error) {
	binding := p.OwnerQueueBinding()
	if binding == nil || ctx.Err() != nil {
		return sharedexec.QueueBuildRequest{}, errors.New("speculative owner queue unavailable")
	}
	ih, err := p.replica.GetHeaderByHash(parentHash)
	if err != nil {
		return sharedexec.QueueBuildRequest{}, err
	}
	parent, ok := ih.(*block.Header)
	if !ok || parent == nil || parent.Number == nil || !parent.Number.IsUint64() || !p.replica.BlockApplied(parentHash, parent.Number.Uint64()) {
		return sharedexec.QueueBuildRequest{}, errors.New("speculative parent lacks durable local import")
	}
	header := &block.Header{ParentHash: parentHash, Number: uint256.NewInt(parent.Number.Uint64() + 1), GasLimit: parent.GasLimit, Difficulty: uint256.NewInt(0), BaseFee: uint256.NewInt(0)}
	if p.replica.cfg.IsLondon(header.Number.Uint64()) {
		header.BaseFee, _ = uint256.FromBig(misc.CalcBaseFee(p.replica.cfg, parent))
	}
	if err := p.replica.engine.PrepareUnsignedForView(p.replica, header, view); err != nil {
		return sharedexec.QueueBuildRequest{}, err
	}
	if ctx.Err() != nil {
		return sharedexec.QueueBuildRequest{}, ctx.Err()
	}
	header.TxHash = block.TxRootAt(nil, header.Time)
	header.ResetHashCache()
	raw, err := block.NewBlock(header, nil).Marshal()
	if err != nil {
		return sharedexec.QueueBuildRequest{}, err
	}
	if len(raw) > sharedexec.MaxQueuePreparedBytes {
		return sharedexec.QueueBuildRequest{}, errors.New("speculative owner queue header exceeds limit")
	}
	return sharedexec.QueueBuildRequest{Identity: p.replica.identity, Epoch: binding.Epoch, PreparedHash: crypto.Keccak256Hash(raw), PreparedRLP: raw, MaxTransactions: binding.MaxTransactions, MaxEncodedBytes: binding.MaxEncodedBytes}, nil
}

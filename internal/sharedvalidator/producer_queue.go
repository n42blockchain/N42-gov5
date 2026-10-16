package sharedvalidator

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

const DefaultOwnerQueueTransactions = 163000

var ErrOwnerQueueSubmissionDisabled = errors.New("owner queue mode rejects local submissions; use the owner's strict ingress socket")
var ownQueueRequestPrefix = []byte("performance-own-queue-request/")

type OwnerQueueOptions struct {
	MaxTransactions int
	MaxEncodedBytes int
}
type OwnerQueueBinding struct {
	Epoch           [16]byte   `json:"epoch"`
	ManifestHash    types.Hash `json:"manifest_hash"`
	MaxTransactions int        `json:"max_transactions"`
	MaxEncodedBytes int        `json:"max_encoded_bytes"`
}
type ownQueueRequest struct {
	ManifestHash types.Hash                   `json:"manifest_hash"`
	Request      sharedexec.QueueBuildRequest `json:"request"`
}

// UseOwnerQueue is startup-only and pins one volatile owner epoch. It never
// silently refreshes an epoch or accepts a local pool that would be unconsumed.
func (p *Producer) UseOwnerQueue(ctx context.Context, options OwnerQueueOptions) error {
	if ctx == nil {
		return errors.New("owner queue context required")
	}
	if options.MaxTransactions == 0 {
		options.MaxTransactions = DefaultOwnerQueueTransactions
	}
	if options.MaxEncodedBytes == 0 {
		options.MaxEncodedBytes = sharedexec.MaxQueueBuildEncodedBytes
	}
	if options.MaxTransactions < 1 || options.MaxTransactions > sharedexec.MaxTransactions || options.MaxEncodedBytes < 1 || options.MaxEncodedBytes > sharedexec.MaxQueueBuildEncodedBytes {
		return errors.New("owner queue selection limits invalid")
	}
	p.produceMu.Lock()
	defer p.produceMu.Unlock()
	p.mu.Lock()
	idle := !p.started && !p.closed && !p.productionStarted && p.ownerQueue == nil && len(p.pending) == 0
	p.mu.Unlock()
	if !idle {
		return errors.New("owner queue requires unused producer")
	}
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stop := context.AfterFunc(p.ctx, cancel)
	defer stop()
	status, err := p.replica.client.StrictIngressStatus(probe)
	if err != nil {
		return err
	}
	if status.Reconciling {
		return errors.New("owner queue is reconciling")
	}
	binding := &OwnerQueueBinding{Epoch: status.Epoch, ManifestHash: status.ManifestHash, MaxTransactions: options.MaxTransactions, MaxEncodedBytes: options.MaxEncodedBytes}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started || p.closed || p.productionStarted || p.ownerQueue != nil || len(p.pending) > 0 {
		return errors.New("owner queue producer changed during startup")
	}
	p.ownerQueue = binding
	return nil
}
func (p *Producer) OwnerQueueBinding() *OwnerQueueBinding {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ownerQueue == nil {
		return nil
	}
	copy := *p.ownerQueue
	return &copy
}
func queueRequestKey(view hotstuff.ViewNumber) []byte {
	key := append([]byte(nil), ownQueueRequestPrefix...)
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], uint64(view))
	return append(key, raw[:]...)
}

func (p *Producer) ownerQueueRequest(ctx context.Context, view hotstuff.ViewNumber, parent *block.Header) (sharedexec.QueueBuildRequest, error) {
	binding := p.OwnerQueueBinding()
	if binding == nil {
		return sharedexec.QueueBuildRequest{}, errors.New("owner queue not configured")
	}
	var stored []byte
	if err := p.replica.db.View(ctx, func(tx kv.Tx) error {
		raw, err := tx.GetOne(modules.HotStuffState, queueRequestKey(view))
		if len(raw) > 2*sharedexec.MaxQueuePreparedBytes+4096 {
			return errors.New("own queue request WAL exceeds limit")
		}
		stored = append([]byte(nil), raw...)
		return err
	}); err != nil {
		return sharedexec.QueueBuildRequest{}, err
	}
	if len(stored) > 0 {
		if len(stored) > 2*sharedexec.MaxQueuePreparedBytes+4096 {
			return sharedexec.QueueBuildRequest{}, errors.New("own queue request WAL exceeds limit")
		}
		var record ownQueueRequest
		decoder := json.NewDecoder(bytes.NewReader(stored))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil {
			return sharedexec.QueueBuildRequest{}, err
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return sharedexec.QueueBuildRequest{}, errors.New("own queue request WAL trailing data")
		}
		request := record.Request
		if record.ManifestHash != binding.ManifestHash || request.Identity != p.replica.identity || request.Epoch != binding.Epoch || request.MaxTransactions != binding.MaxTransactions || request.MaxEncodedBytes != binding.MaxEncodedBytes || len(request.PreparedRLP) == 0 || len(request.PreparedRLP) > sharedexec.MaxQueuePreparedBytes || crypto.Keccak256Hash(request.PreparedRLP) != request.PreparedHash {
			return sharedexec.QueueBuildRequest{}, errors.New("own queue request WAL binding changed; reconcile original session")
		}
		var prepared block.Block
		if err := prepared.Unmarshal(request.PreparedRLP); err != nil {
			return sharedexec.QueueBuildRequest{}, err
		}
		header, ok := prepared.Header().(*block.Header)
		body, bodyOK := prepared.Body().(*block.Body)
		if !ok || header == nil || header.Number == nil || !header.Number.IsUint64() || header.Number.Uint64() != parent.Number.Uint64()+1 || header.ParentHash != parent.Hash() || !bodyOK || body == nil || body.IsFrameBody() || len(prepared.Transactions()) > 0 || len(body.Verifiers) > 0 || len(body.Rewards) > 0 || len(body.ZkProof) > 0 || header.TxHash != block.BodyTxRoot(body, header.Time) {
			return sharedexec.QueueBuildRequest{}, errors.New("own queue request WAL parent/body mismatch")
		}
		storedView, err := hotstuff.ExtractViewFromExtra(header.Extra)
		if err != nil || storedView != view {
			return sharedexec.QueueBuildRequest{}, errors.New("own queue request WAL view mismatch")
		}
		return request, nil
	}
	header := &block.Header{ParentHash: parent.Hash(), Number: uint256.NewInt(parent.Number.Uint64() + 1), GasLimit: parent.GasLimit, Difficulty: uint256.NewInt(0), BaseFee: uint256.NewInt(0)}
	if p.replica.cfg.IsLondon(header.Number.Uint64()) {
		header.BaseFee, _ = uint256.FromBig(misc.CalcBaseFee(p.replica.cfg, parent))
	}
	if err := p.replica.engine.Prepare(p.replica, header); err != nil {
		return sharedexec.QueueBuildRequest{}, err
	}
	preparedView, err := hotstuff.ExtractViewFromExtra(header.Extra)
	if err != nil || preparedView != view || p.replica.engine.Engine().CurrentView() != view || !p.replica.engine.IsCurrentLeader() {
		return sharedexec.QueueBuildRequest{}, errors.New("owner queue view changed during Prepare")
	}
	if err := ctx.Err(); err != nil {
		return sharedexec.QueueBuildRequest{}, err
	}
	header.TxHash = block.TxRootAt(nil, header.Time)
	header.ResetHashCache()
	raw, err := block.NewBlock(header, nil).Marshal()
	if err != nil {
		return sharedexec.QueueBuildRequest{}, err
	}
	if len(raw) > sharedexec.MaxQueuePreparedBytes {
		return sharedexec.QueueBuildRequest{}, errors.New("own queue prepared header exceeds limit")
	}
	request := sharedexec.QueueBuildRequest{Identity: p.replica.identity, Epoch: binding.Epoch, PreparedHash: crypto.Keccak256Hash(raw), PreparedRLP: raw, MaxTransactions: binding.MaxTransactions, MaxEncodedBytes: binding.MaxEncodedBytes}
	record, err := json.Marshal(ownQueueRequest{ManifestHash: binding.ManifestHash, Request: request})
	if err != nil {
		return sharedexec.QueueBuildRequest{}, err
	}
	// The exact leader request is durable before its first remote selection.
	if err := p.replica.db.Update(ctx, func(tx kv.RwTx) error {
		existing, err := tx.GetOne(modules.HotStuffState, queueRequestKey(view))
		if err != nil {
			return err
		}
		if len(existing) > 0 && !bytes.Equal(existing, record) {
			return errors.New("own queue view cannot replace persisted request")
		}
		return tx.Put(modules.HotStuffState, queueRequestKey(view), record)
	}); err != nil {
		return sharedexec.QueueBuildRequest{}, err
	}
	return request, nil
}

// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/log"
)

// DecideFollower makes a node that runs no consensus engine (a profile-1
// non-validator: no BLS key, no --engine.miner) follow the committed chain.
// Under HotStuff the canonical head moves only by a commit
// (CommitToCanonical), which a validator takes from its engine's Decide; a
// follower has no engine, so its head never left genesis although it imported
// and executed every block. The follower subscribes to the chain-bound H2-v4
// Decide gossip the validators publish, verifies each CommitQC against the
// fixed profile-1 validator set and commits the named block exactly as a
// validator does. A Decide that arrives before its block is imported and
// applied is kept and retried.
type DecideFollower struct {
	chain    DecideFollowerChain
	p2p      DecideFollowerP2P
	set      *ValidatorSet
	identity H2V4ChainIdentity

	// catchUp, when set, asks the sync layer to range-fetch up to a
	// committed block the follower does not hold (a node that joined or
	// restarted behind the chain); without it the follower only waits for
	// the gossip to deliver the gap, which it never does.
	catchUp func(types.Hash)

	mu      sync.Mutex
	pending map[types.Hash]decidePending

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// DecideFollowerChain is the commit authority of the local chain.
type DecideFollowerChain interface {
	CommitToCanonical(hash types.Hash) error
}

// DecideFollowerP2P subscribes to a gossip topic.
type DecideFollowerP2P interface {
	SubscribeToTopic(topic string, opts ...pubsub.SubOpt) (*pubsub.Subscription, error)
}

type decidePending struct {
	view  ViewNumber
	since time.Time
	asked time.Time // last catch-up request for this block
}

const (
	decideFollowerRetry      = 200 * time.Millisecond
	decideFollowerPendingTTL = 2 * time.Minute
	decideFollowerMaxPending = 256
	decideFollowerAskEvery   = 5 * time.Second
)

// SetCatchUp installs the sync layer's catch-up for committed blocks the
// follower does not hold (sync.Service.CatchUpToHash). Call before Start.
func (f *DecideFollower) SetCatchUp(catchUp func(types.Hash)) { f.catchUp = catchUp }

// NewDecideFollower builds the follower for a profile-1 chain; it fails when
// the chain is not profile 1 (the only chain with a fixed, engine-free
// validator set to verify CommitQCs against).
func NewDecideFollower(h *HotStuff, chain DecideFollowerChain, p2p DecideFollowerP2P, identity H2V4ChainIdentity) (*DecideFollower, error) {
	if h == nil || chain == nil || p2p == nil {
		return nil, errors.New("hotstuff decide follower: missing engine, chain or p2p")
	}
	set := h.Profile1ValidatorSet()
	if set == nil {
		return nil, errors.New("hotstuff decide follower: profile-1 validator set unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &DecideFollower{
		chain: chain, p2p: p2p, set: set, identity: identity,
		pending: make(map[types.Hash]decidePending),
		ctx:     ctx, cancel: cancel,
	}, nil
}

// Start subscribes to the H2-v4 Decide topic and runs the commit loop.
func (f *DecideFollower) Start() error {
	// AM36: validate, then forward. Registered before the subscription so
	// nothing is relayed undecoded.
	unregister, err := registerTopicValidator(f.p2p, h2V4GossipTopic, f.decideTopicValidator())
	if err != nil {
		return fmt.Errorf("hotstuff decide follower: register validator for %s: %w", h2V4GossipTopic, err)
	}
	sub, err := f.p2p.SubscribeToTopic(h2V4GossipTopic)
	if err != nil {
		if unregister != nil {
			unregister()
		}
		return fmt.Errorf("hotstuff decide follower: subscribe %s: %w", h2V4GossipTopic, err)
	}
	f.wg.Add(2)
	go func() {
		defer f.wg.Done()
		defer sub.Cancel()
		if unregister != nil {
			defer unregister()
		}
		for {
			msg, err := sub.Next(f.ctx)
			if err != nil {
				return // context cancelled or subscription closed
			}
			if env, ok := msg.ValidatorData.(*H2V4Envelope); ok && env != nil {
				// Decoded by the topic validator: verify and commit only.
				if err := f.handleEnvelope(env); err != nil {
					log.Debug("hotstuff decide follower: message refused", "err", err)
				}
				continue
			}
			if err := f.HandleMessage(msg.Data); err != nil {
				log.Debug("hotstuff decide follower: message refused", "err", err)
			}
		}
	}()
	go func() {
		defer f.wg.Done()
		t := time.NewTicker(decideFollowerRetry)
		defer t.Stop()
		for {
			select {
			case <-f.ctx.Done():
				return
			case <-t.C:
				f.retryPending()
			}
		}
	}()
	log.Info("hotstuff: following committed blocks through H2-v4 Decides (non-validator)",
		"topic", h2V4GossipTopic, "validators", f.set.Len())
	return nil
}

// Stop ends the follower.
func (f *DecideFollower) Stop() {
	f.cancel()
	f.wg.Wait()
}

// HandleMessage decodes and verifies one H2-v4 Decide gossip message and
// commits its block, or keeps it pending until the block is applied.
func (f *DecideFollower) HandleMessage(data []byte) error {
	env, err := DecodeH2V4Gossip(data, f.identity)
	if err != nil {
		return err
	}
	return f.handleEnvelope(env)
}

// handleEnvelope verifies a decoded H2-v4 Decide and commits its block.
func (f *DecideFollower) handleEnvelope(env *H2V4Envelope) error {
	decide, err := VerifyH2V4Decide(env, f.set)
	if err != nil {
		return err
	}
	if decide.CommitQC.SignerCount() < f.set.QuorumSize() {
		return fmt.Errorf("H2-v4 Decide has %d signers, quorum is %d", decide.CommitQC.SignerCount(), f.set.QuorumSize())
	}
	f.commit(decide.View, decide.BlockHash)
	return nil
}

// commit canonicalizes hash; a block not yet imported or applied is kept for
// the retry loop. A commit that conflicts with the committed chain is logged
// and dropped: the canonical rows were not rewritten.
func (f *DecideFollower) commit(view ViewNumber, hash types.Hash) {
	err := f.chain.CommitToCanonical(hash)
	f.mu.Lock()
	defer f.mu.Unlock()
	var ce *consensus.ConflictingCommitError
	switch {
	case err == nil:
		delete(f.pending, hash)
		log.Info("hotstuff: block committed (follower)", "view", view, "hash", hash)
	case errors.As(err, &ce):
		delete(f.pending, hash)
		log.Error("hotstuff: CONFLICTING COMMIT on a follower -- verified CommitQC names a block that does not extend the committed chain; canonical rows were NOT rewritten",
			"view", view, "hash", hash, "height", ce.Height, "committed", ce.Committed)
	default:
		now := time.Now()
		p, ok := f.pending[hash]
		if !ok {
			if len(f.pending) >= decideFollowerMaxPending {
				f.dropOldestLocked()
			}
			p = decidePending{view: view, since: now}
			log.Debug("hotstuff: follower commit deferred", "view", view, "hash", hash, "err", err)
		}
		if f.catchUp != nil && now.Sub(p.asked) >= decideFollowerAskEvery {
			p.asked = now
			go f.catchUp(hash)
		}
		f.pending[hash] = p
	}
}

func (f *DecideFollower) dropOldestLocked() {
	var oldest types.Hash
	var at time.Time
	for h, p := range f.pending {
		if at.IsZero() || p.since.Before(at) {
			oldest, at = h, p.since
		}
	}
	delete(f.pending, oldest)
}

// retryPending re-attempts every pending commit, lowest view first so a
// lineage canonicalizes in order, and expires the stale ones.
func (f *DecideFollower) retryPending() {
	f.mu.Lock()
	type item struct {
		hash types.Hash
		view ViewNumber
	}
	items := make([]item, 0, len(f.pending))
	now := time.Now()
	for h, p := range f.pending {
		if now.Sub(p.since) > decideFollowerPendingTTL {
			delete(f.pending, h)
			continue
		}
		items = append(items, item{h, p.view})
	}
	f.mu.Unlock()
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].view < items[j-1].view; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
	for _, it := range items {
		f.commit(it.view, it.hash)
	}
}

// PendingCount is the number of verified Decides waiting for their block.
func (f *DecideFollower) PendingCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pending)
}

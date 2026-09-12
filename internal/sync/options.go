package sync

import (
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/p2p"
)

type Option func(s *Service) error

func WithP2P(p2p p2p.P2P) Option {
	return func(s *Service) error {
		s.cfg.p2p = p2p
		return nil
	}
}

func WithChainService(chain common.IBlockChain) Option {
	return func(s *Service) error {
		s.cfg.chain = chain
		return nil
	}
}

func WithInitialSync(initialSync Checker) Option {
	return func(s *Service) error {
		s.cfg.initialSync = initialSync
		return nil
	}
}

// WithOverrideGenesisHash overrides the genesis hash used for fork digest
// calculation. Used in compat mode where the actual genesis hash differs
// from the legacy value due to Header struct changes.
func WithOverrideGenesisHash(h types.Hash) Option {
	return func(s *Service) error {
		s.cfg.overrideGenesisHash = &h
		return nil
	}
}

// WithEarliestBlock sets a function that returns the earliest available block
// number. P2P range requests for blocks before this number are rejected.
func WithEarliestBlock(fn func() uint64) Option {
	return func(s *Service) error {
		s.cfg.earliestBlock = fn
		return nil
	}
}

// BlockImportNotifier is called after a gossip block is imported into the chain.
// Used by HotStuff to learn that a proposed block is now locally available.
type BlockImportNotifier interface {
	NotifyBlockImported(hash types.Hash, txHash types.Hash)
	// NotifyBlockChecked: deferred execution -- the block passed the
	// pre-execution check (DeferredBlockChecker) and can be voted for once
	// its parent is imported.
	NotifyBlockChecked(hash types.Hash, parent types.Hash)
}

// DeferredBlockChecker is implemented by the chain under deferred
// execution: checked is false before the fork; retry asks for the check to
// run again once the block's parent is applied.
type DeferredBlockChecker interface {
	CheckDeferredBlock(blk block.IBlock) (checked, retry bool, err error)
}

// WithBlockImportNotifier sets a notifier called after gossip blocks are imported.
func WithBlockImportNotifier(n BlockImportNotifier) Option {
	return func(s *Service) error {
		s.cfg.blockImportNotifier = n
		return nil
	}
}

// WithTxPool enables transaction gossip subscription. Received transactions
// are added to the pool via AddRemotes.
func WithTxPool(pool TxPool) Option {
	return func(s *Service) error {
		s.cfg.txPool = pool
		s.cfg.txGossipEnabled = pool != nil
		return nil
	}
}

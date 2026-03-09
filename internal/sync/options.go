package sync

import (
	"github.com/n42blockchain/N42/common"
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

// WithEarliestBlock sets a function that returns the earliest available block
// number. P2P range requests for blocks before this number are rejected.
func WithEarliestBlock(fn func() uint64) Option {
	return func(s *Service) error {
		s.cfg.earliestBlock = fn
		return nil
	}
}

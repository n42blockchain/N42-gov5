// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package node

import (
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
	n42sync "github.com/n42blockchain/N42/internal/sync"
	"github.com/n42blockchain/N42/internal/syncanchor"
	"github.com/n42blockchain/N42/params"
)

// applyProfile1Limits installs the process-wide limits a Profile-1 chain
// takes from its genesis, before the p2p and sync services start:
//   - B15/AM2: maxBlockBytes is the only block-size bound on every carrier
//     (gossip, pubsub router, push, by-hash, ranges);
//   - standard §3: anchored ranges enforced, observe/off unreachable;
//   - standard §3: the sync servers' profile-1 rules (block_by_hash miss 0x03);
//   - G2/AM34: only the G2 gossip topics are subscribed or accepted.
//
// A chain outside the profile never calls it.
func applyProfile1Limits(cfg *params.ChainConfig) {
	if !cfg.IsProfile1() || cfg.HotStuff == nil {
		return
	}
	encoder.SetMaxBlockBytes(cfg.HotStuff.MaxBlockBytes)
	syncanchor.EnforceForProfile1()
	n42sync.ConfigureProfile1(cfg)
	p2p.ConfigureProfile1Topics(true)
}

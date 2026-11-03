package api

import (
	"github.com/n42blockchain/N42/params"
)

// engineFork is the Engine API generation a payload timestamp belongs to.
// The Engine API versions its methods by fork, so every method checks the
// generation of the timestamp it is called with and answers -38005
// (Unsupported fork) outside its range, as geth's checkFork does.
type engineFork int

const (
	engineForkParis engineFork = iota
	engineForkShanghai
	engineForkCancun
	engineForkPrague
	engineForkOsaka
	engineForkAmsterdam
)

// engineLatestFork returns the latest Engine API generation active at the
// given block number and timestamp.
//
// Prague is IsPrague or IsPectra: Ethereum chains schedule it through
// PragueTime (PectraTime is nil on Ethereum mainnet), N42 native chains
// through PectraTime. Osaka covers the BPO forks after it, which change only
// blob parameters and keep the Osaka methods.
//
// Amsterdam is Glamsterdam on a chain that commits to the EIP-7928 block
// access list, which Amsterdam payloads carry; N42 native chains keep the
// Osaka methods.
func engineLatestFork(cfg *params.ChainConfig, number, timestamp uint64) engineFork {
	switch {
	case cfg.UsesEIP7928AccessList(timestamp):
		return engineForkAmsterdam
	case cfg.IsOsaka(timestamp):
		return engineForkOsaka
	case cfg.IsPrague(timestamp) || cfg.IsPectra(timestamp):
		return engineForkPrague
	case cfg.IsCancunAt(number, timestamp):
		return engineForkCancun
	case isShanghaiActive(cfg, number, timestamp):
		return engineForkShanghai
	default:
		return engineForkParis
	}
}

// engineForkIn reports whether the generation at (number, timestamp) is one
// of forks. A node without a chain config accepts every call, as before.
func engineForkIn(cfg *params.ChainConfig, number, timestamp uint64, forks ...engineFork) bool {
	if cfg == nil {
		return true
	}
	latest := engineLatestFork(cfg, number, timestamp)
	for _, fork := range forks {
		if latest == fork {
			return true
		}
	}
	return false
}

// builtPayloadPosition returns the block number and timestamp of a built
// payload, from the newest representation it carries.
func builtPayloadPosition(built *engineBuiltPayload) (uint64, uint64, bool) {
	switch {
	case built == nil:
		return 0, 0, false
	case built.v4 != nil:
		return uint64(built.v4.BlockNumber), uint64(built.v4.Timestamp), true
	case built.v3 != nil:
		return uint64(built.v3.BlockNumber), uint64(built.v3.Timestamp), true
	case built.v2 != nil:
		return uint64(built.v2.BlockNumber), uint64(built.v2.Timestamp), true
	case built.v1 != nil:
		return uint64(built.v1.BlockNumber), uint64(built.v1.Timestamp), true
	}
	return 0, 0, false
}

// builtPayloadForkIn reports whether a built payload belongs to one of forks.
func builtPayloadForkIn(cfg *params.ChainConfig, built *engineBuiltPayload, forks ...engineFork) bool {
	number, timestamp, ok := builtPayloadPosition(built)
	if !ok {
		return true
	}
	return engineForkIn(cfg, number, timestamp, forks...)
}

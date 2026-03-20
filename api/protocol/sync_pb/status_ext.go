// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync_pb

// StatusExt extends the proto-generated Status with eth/69-style fields
// that are not yet in the generated Go code (pending proto regeneration).
//
// Per EIP-7642 (eth/69), the Status handshake should include:
//   - earliestBlock: the oldest block the node can serve
//   - latestBlock: the newest block (already in CurrentHeight)
//
// These fields enable peers to know each other's available block range,
// supporting EIP-4444 history expiry discovery.
type StatusExt struct {
	*Status
	// EarliestBlock is the oldest available block number (0 = all history available).
	EarliestBlock uint64
}

// NewStatusExt wraps a proto Status with the earliest block extension.
func NewStatusExt(s *Status, earliest uint64) *StatusExt {
	return &StatusExt{Status: s, EarliestBlock: earliest}
}

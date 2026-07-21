// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"encoding/binary"

	"github.com/n42blockchain/N42/common/types"
)

var h2V4DomainPrefix = [7]byte{'N', '4', '2', 'H', '2', 'V', '4'}

type H2V4ChainIdentity struct {
	ChainID     uint64
	GenesisHash types.Hash
}

const (
	h2V4Proposal byte = 1
	h2V4Vote     byte = 2
	h2V4Commit   byte = 3
	h2V4Timeout  byte = 4
	h2V4NewView  byte = 5
)

func h2V4Base(identity H2V4ChainIdentity, phase byte, view ViewNumber) []byte {
	msg := make([]byte, 56)
	copy(msg[:7], h2V4DomainPrefix[:])
	msg[7] = phase
	binary.LittleEndian.PutUint64(msg[8:16], identity.ChainID)
	copy(msg[16:48], identity.GenesisHash[:])
	binary.LittleEndian.PutUint64(msg[48:56], view)
	return msg
}

func H2V4ProposalSigningMessage(identity H2V4ChainIdentity, view ViewNumber, blockHash, changesHash types.Hash) []byte {
	msg := h2V4Base(identity, h2V4Proposal, view)
	msg = append(msg, blockHash[:]...)
	return append(msg, changesHash[:]...)
}

func H2V4VoteSigningMessage(identity H2V4ChainIdentity, view ViewNumber, blockHash types.Hash) []byte {
	msg := h2V4Base(identity, h2V4Vote, view)
	return append(msg, blockHash[:]...)
}

func H2V4CommitSigningMessage(identity H2V4ChainIdentity, view ViewNumber, blockHash, changesHash types.Hash) []byte {
	msg := h2V4Base(identity, h2V4Commit, view)
	msg = append(msg, blockHash[:]...)
	return append(msg, changesHash[:]...)
}

func H2V4TimeoutSigningMessage(identity H2V4ChainIdentity, view ViewNumber) []byte {
	return h2V4Base(identity, h2V4Timeout, view)
}

func H2V4NewViewSigningMessage(identity H2V4ChainIdentity, view ViewNumber) []byte {
	return h2V4Base(identity, h2V4NewView, view)
}

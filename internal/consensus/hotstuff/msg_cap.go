// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Profile-1 consensus message cap (mixed production standard G5).

package hotstuff

import (
	"fmt"

	"github.com/golang/snappy"

	"github.com/n42blockchain/N42/internal/p2p/encoder"
)

// MaxConsensusMsg is MAX_CONSENSUS_MSG of the standard (G5): the largest
// DECODED consensus message a Profile-1 node accepts, on gossip and on the
// direct stream. A 512-member bitmap QC plus a TC fits with margin. On
// Profile 1 the gossip router cap is raised to maxBlockBytes (B15), so this
// per-message check is the consensus topic's only size bound there.
const MaxConsensusMsg = 64 << 10

// MaxConsensusWireMsg is the largest snappy encoding of a message within
// MaxConsensusMsg: the read bound of a direct consensus stream.
var MaxConsensusWireMsg = snappy.MaxEncodedLen(MaxConsensusMsg)

// decodeConsensusGossip decodes one inbound consensus message. On a
// Profile-1 chain it refuses a message whose decoded size exceeds
// MaxConsensusMsg, checking the snappy length header before decompressing;
// elsewhere it is enc.DecodeGossip unchanged (the shared gossip cap).
func (s *Service) decodeConsensusGossip(enc encoder.NetworkEncoding, data []byte, dst *rawSSZMarshaler) error {
	if s.engine == nil || !s.engine.chainConfig.IsProfile1() {
		return enc.DecodeGossip(data, dst)
	}
	if n, err := snappy.DecodedLen(data); err == nil && n > MaxConsensusMsg {
		return fmt.Errorf("consensus message of %d bytes exceeds the %d byte cap", n, MaxConsensusMsg)
	}
	if err := enc.DecodeGossip(data, dst); err != nil {
		return err
	}
	if len(dst.data) > MaxConsensusMsg {
		return fmt.Errorf("consensus message of %d bytes exceeds the %d byte cap", len(dst.data), MaxConsensusMsg)
	}
	return nil
}

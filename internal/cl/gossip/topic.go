//go:build n42el

package gossip

import (
	common "github.com/n42blockchain/N42/internal/cl/depshim/common"
)

type GossipTopic struct {
	ForkDigest common.Bytes4
	Name       string
	CodecStr   string
}

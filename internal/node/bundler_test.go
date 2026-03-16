package node

import (
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/params"
)

func TestBundlerChainIDRejectsMissingChainID(t *testing.T) {
	if _, err := bundlerChainID(nil); err == nil || err.Error() != "bundler requires chain ID" {
		t.Fatalf("bundlerChainID(nil) error = %v", err)
	}

	if _, err := bundlerChainID(&params.ChainConfig{}); err == nil || err.Error() != "bundler requires chain ID" {
		t.Fatalf("bundlerChainID(empty cfg) error = %v", err)
	}
}

func TestBundlerChainIDAcceptsValue(t *testing.T) {
	got, err := bundlerChainID(&params.ChainConfig{ChainID: big.NewInt(9)})
	if err != nil {
		t.Fatalf("bundlerChainID(cfg) error = %v", err)
	}
	if got != 9 {
		t.Fatalf("bundlerChainID(cfg) = %d, want 9", got)
	}
}

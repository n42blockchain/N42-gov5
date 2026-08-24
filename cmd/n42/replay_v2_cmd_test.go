package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/internal/node"
	"github.com/n42blockchain/N42/params"
)

func TestReplayTargetNetworkBinding(t *testing.T) {
	t.Parallel()

	target := t.TempDir()
	const chainName = "mainnet_qmdb_staggered"
	chainCfg := params.ChainConfigByChainName(chainName)
	if chainCfg == nil {
		t.Fatal("missing test chain config")
	}

	if err := validateReplayTargetNetworkBinding(target, chainName, chainCfg); err != nil {
		t.Fatalf("empty target validation: %v", err)
	}
	if err := persistReplayTargetNetworkBinding(target, chainName, chainCfg); err != nil {
		t.Fatalf("persist target binding: %v", err)
	}
	if err := validateReplayTargetNetworkBinding(target, chainName, chainCfg); err != nil {
		t.Fatalf("persisted target validation: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(target, "network.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(raw)
	for _, want := range []string{
		`"chain": "mainnet_qmdb_staggered"`,
		`"commitment": "qmdb"`,
		`"consensus": "hotstuff"`,
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("network manifest missing %s:\n%s", want, manifest)
		}
	}

	mainnetCfg := params.ChainConfigByChainName("mainnet")
	err = validateReplayTargetNetworkBinding(target, "mainnet", mainnetCfg)
	if !errors.Is(err, node.ErrDatadirNetworkMismatch) {
		t.Fatalf("mismatched target error = %v, want %v", err, node.ErrDatadirNetworkMismatch)
	}
}

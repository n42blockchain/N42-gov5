package main

import (
	"flag"
	"testing"
)

func TestOwnerQueueFlagsReachRuntimeConfig(t *testing.T) {
	original := DefaultConfig.NodeCfg
	t.Cleanup(func() { DefaultConfig.NodeCfg = original })
	set := flag.NewFlagSet("queue", flag.ContinueOnError)
	for _, f := range settingFlag {
		if err := f.Apply(set); err != nil {
			t.Fatal(err)
		}
	}
	if err := set.Parse([]string{"--performance.owner-queue", "--performance.queue-max-transactions=163000", "--performance.queue-max-encoded-bytes=33554432", "--performance.role=validator", "--performance.execution-socket=/explicit/execute.sock", "--performance.genesis=/explicit/genesis.json", "--chain=private", "--profile=n42"}); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig.NodeCfg
	if !cfg.PerformanceOwnerQueue || cfg.PerformanceQueueMaxTransactions != 163000 || cfg.PerformanceQueueMaxEncodedBytes != 32<<20 || cfg.PerformanceRole != "validator" || cfg.PerformanceExecutionSocket != "/explicit/execute.sock" || cfg.PerformanceGenesis != "/explicit/genesis.json" {
		t.Fatal("typed registered flags did not reach NodeCfg")
	}
}

package conf

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func queueConfigForTest() *Config {
	return &Config{NodeCfg: NodeConfig{Chain: "private", Profile: "n42", PerformanceRole: "validator", PerformanceGenesis: "/explicit/genesis.json", PerformanceExecutionSocket: "/explicit/execute.sock", PerformanceOwnerQueue: true}}
}
func TestOwnerQueueConfigurationIsExplicitAndBounded(t *testing.T) {
	for name, change := range map[string]func(*Config){
		"disabled limits": func(c *Config) {
			c.NodeCfg.PerformanceOwnerQueue = false
			c.NodeCfg.PerformanceQueueMaxTransactions = 1
		},
		"pipeline without queue": func(c *Config) {
			c.NodeCfg.PerformanceOwnerQueue = false
			c.NodeCfg.PerformanceOwnerChildPipeline = true
		},
		"role":            func(c *Config) { c.NodeCfg.PerformanceRole = "" },
		"executor role":   func(c *Config) { c.NodeCfg.PerformanceRole = "executor" },
		"socket":          func(c *Config) { c.NodeCfg.PerformanceExecutionSocket = "" },
		"genesis":         func(c *Config) { c.NodeCfg.PerformanceGenesis = "" },
		"mainnet":         func(c *Config) { c.NodeCfg.Chain = "mainnet" },
		"eth":             func(c *Config) { c.NodeCfg.Profile = "eth" },
		"negative count":  func(c *Config) { c.NodeCfg.PerformanceQueueMaxTransactions = -1 },
		"oversized count": func(c *Config) { c.NodeCfg.PerformanceQueueMaxTransactions = 200001 },
		"negative bytes":  func(c *Config) { c.NodeCfg.PerformanceQueueMaxEncodedBytes = -1 },
		"oversized bytes": func(c *Config) { c.NodeCfg.PerformanceQueueMaxEncodedBytes = (32 << 20) + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			c := queueConfigForTest()
			change(c)
			if err := NormalizeNetworkSelection(c); err == nil {
				t.Fatal("invalid owner queue silently accepted")
			}
		})
	}
	c := queueConfigForTest()
	if err := NormalizeNetworkSelection(c); err != nil {
		t.Fatal(err)
	}
	for _, selection := range [][2]string{{"private", "n42"}, {"mainnet", "n42"}, {"private", "eth"}, {"eth-mainnet", "eth"}} {
		c := &Config{NodeCfg: NodeConfig{Chain: selection[0], Profile: selection[1]}}
		if err := NormalizeNetworkSelection(c); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(c.NodeCfg)
		if strings.Contains(string(raw), "performance_owner_queue") || strings.Contains(string(raw), "performance_owner_child_pipeline") || c.NodeCfg.PerformanceOwnerQueue || c.NodeCfg.PerformanceOwnerChildPipeline {
			t.Fatal("default node silently enabled owner queue")
		}
	}
}
func TestOwnerQueueYAMLReachesActualConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.yaml")
	raw := `node:
  chain: private
  profile: n42
  performance_role: validator
  performance_genesis: /explicit/genesis.json
  performance_execution_socket: /explicit/execute.sock
  performance_owner_queue: true
  performance_owner_child_pipeline: true
  performance_queue_max_transactions: 163000
  performance_queue_max_encoded_bytes: 33554432
`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	var c Config
	if err := LoadConfigFromFile(path, &c); err != nil {
		t.Fatal(err)
	}
	if err := Validate(&c); err != nil {
		t.Fatal(err)
	}
	if !c.NodeCfg.PerformanceOwnerQueue || !c.NodeCfg.PerformanceOwnerChildPipeline || c.NodeCfg.PerformanceQueueMaxTransactions != 163000 || c.NodeCfg.PerformanceQueueMaxEncodedBytes != 32<<20 {
		t.Fatal("YAML queue settings were lost")
	}
	c.NodeCfg.PerformanceRole = ""
	if err := Validate(&c); err == nil {
		t.Fatal("YAML queue without runtime role accepted")
	}
}

func TestRecoveryOnlyConfigurationRequiresActiveOwnerQueue(t *testing.T) {
	for _, disable := range []string{"miner", "queue"} {
		c := queueConfigForTest()
		c.NodeCfg.Miner = true
		c.NodeCfg.PerformanceRecoveryOnly = true
		if disable == "miner" {
			c.NodeCfg.Miner = false
		} else {
			c.NodeCfg.PerformanceOwnerQueue = false
		}
		if err := NormalizeNetworkSelection(c); err == nil {
			t.Fatalf("recovery-only allowed without %s", disable)
		}
	}
	c := queueConfigForTest()
	c.NodeCfg.Miner = true
	c.NodeCfg.PerformanceRecoveryOnly = true
	if err := NormalizeNetworkSelection(c); err != nil {
		t.Fatal(err)
	}
}

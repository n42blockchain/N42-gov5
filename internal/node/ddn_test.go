package node

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/ddn/native"
	"github.com/n42blockchain/N42/internal/ddn/receipt"
	"github.com/n42blockchain/N42/internal/ddn/transformer"
	d "github.com/n42blockchain/N42/internal/ddn/types"
	"github.com/n42blockchain/N42/params"
)

func TestDDNRuntimeOptInAndAuthentication(t *testing.T) {
	cfg := &conf.Config{AICfg: conf.DefaultAICfg(), ChainCfg: &params.ChainConfig{ChainID: big.NewInt(94)}}
	n := &Node{config: cfg, runtimePlan: auxiliaryRuntimePlan{startDistributed: true}}
	n.startDDNRuntime()
	if n.ddnGateway != nil {
		t.Fatal("default config started DDN")
	}
	cfg.AICfg.DDN.Enabled = true
	cfg.AICfg.DDN.GatewayEnabled = true
	cfg.AICfg.DDN.Backend = "http"
	cfg.AICfg.DDN.SidecarURL = "http://127.0.0.1:65530/decide"
	cfg.AICfg.DDN.ProviderDID = "did:n42:test"
	cfg.AICfg.DDN.ModelHash = "0x0100000000000000000000000000000000000000000000000000000000000000"
	n.startDDNRuntime()
	if n.ddnGateway == nil {
		t.Fatal("enabled gateway failed to start")
	}
	defer n.ddnGateway.Stop()
	if len(n.rpcAPIs) != 1 || n.rpcAPIs[0].Namespace != "n42" || !n.rpcAPIs[0].Authenticated {
		t.Fatal("DDN RPC must be authenticated")
	}
}

func TestDDNQuorumConfiguration(t *testing.T) {
	c := conf.DefaultAICfg().DDN
	c.ProviderDID = "did:n42:aggregate"
	c.QuorumSize = 2
	c.Backend = "http"
	n := &Node{}
	if _, err := n.ddnProvider(c); err == nil {
		t.Fatal("missing quorum sidecars accepted")
	}
	for i := 0; i < 2; i++ {
		c.Sidecars = append(c.Sidecars, conf.DDNSidecarCfg{URL: "http://127.0.0.1:65530/decide", ProviderDID: fmt.Sprintf("did:n42:provider%d", i), ModelHash: fmt.Sprintf("0x%064x", i+1), ModelFamily: fmt.Sprintf("family%d", i), Tasks: c.Tasks, Schemas: c.Schemas})
	}
	p, err := n.ddnProvider(c)
	if err != nil {
		t.Fatal(err)
	}
	if p.Identity().Model != "DDN-quorum-v1" {
		t.Fatal("group not wired")
	}
	c.RequireRegisteredProvider = true
	if _, err = n.ddnProvider(c); err == nil {
		t.Fatal("registry requirement silently bypassed")
	}
}

func TestDDNNativeRuntimeSignedRoundTrip(t *testing.T) {
	cfg := &conf.Config{AICfg: conf.DefaultAICfg(), ChainCfg: &params.ChainConfig{ChainID: big.NewInt(94)}}
	cfg.AICfg.DDN.Enabled = true
	cfg.AICfg.DDN.GatewayEnabled = true
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := receipt.NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	defer signer.Close()
	cfg.AICfg.DDN.ProviderDID = signer.DID()
	n := &Node{config: cfg, runtimePlan: auxiliaryRuntimePlan{startDistributed: true}}
	n.startDDNRuntime()
	if n.ddnGateway == nil {
		t.Fatal("native runtime did not start without sidecar")
	}
	defer n.ddnGateway.Stop()
	if err = n.ddnGateway.SetSigner(signer); err != nil {
		t.Fatal(err)
	}
	input := "finality stalled"
	r := d.DecisionRequest{Version: 1, ChainID: 94, Task: "node.anomaly", SchemaID: "health-v1", InputHash: crypto.Keccak256Hash([]byte(input)), PrivacyMode: "public", Quorum: 1, MaxLatencyMs: 500, MaxCost: "0", Deadline: uint64(time.Now().Add(time.Minute).UnixMilli()), Requester: "did:n42:tester", Nonce: 1}
	info := n.ddnGateway.Info()
	r.ModelRequirements.ModelHash = info.Provider.ModelHash
	id, err := n.ddnGateway.Submit(r, input)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		record, err := n.ddnGateway.GetReceipt(id)
		if err != nil {
			t.Fatal(err)
		}
		if record.Status != "pending" {
			if record.Receipt == nil || record.Receipt.Result.Label != "CONSENSUS" || !record.NeedEscalation {
				t.Fatal(record)
			}
			if err = receipt.Verify(*record.Receipt, r, crypto.PubkeyToAddress(key.PublicKey), uint64(time.Now().UnixMilli())); err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("native decision did not complete")
}

func TestDDNLoadsNativeWeightsAndQuorum(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	bayes, err := native.Train(ctx, "health", "1", "node.anomaly", "health-v1", []native.Example{{Label: "NORMAL", Text: "healthy synchronized peers"}, {Label: "STORAGE", Text: "database corrupted disk full"}}, 800000, 500000)
	if err != nil {
		t.Fatal(err)
	}
	nb, _ := bayes.Bytes()
	if err = os.WriteFile(filepath.Join(dir, "bayes.json"), nb, 0600); err != nil {
		t.Fatal(err)
	}
	tf, err := transformer.Initialize("health", "1", "node.anomaly", "health-v1", []string{"NORMAL", "STORAGE"}, transformer.Config{Hidden: 4, Heads: 1, Layers: 1, FeedForward: 8, MaxSequence: 32}, 800000, 42)
	if err != nil {
		t.Fatal(err)
	}
	tf, _, err = transformer.Train(ctx, tf, []transformer.Example{{Label: "NORMAL", Text: "healthy"}, {Label: "STORAGE", Text: "corrupted"}}, transformer.TrainConfig{Epochs: 2, LearningRate: 0.01, ClipNorm: 1})
	if err != nil {
		t.Fatal(err)
	}
	tb, _ := tf.Bytes()
	if err = os.WriteFile(filepath.Join(dir, "transformer.json"), tb, 0600); err != nil {
		t.Fatal(err)
	}
	n := &Node{config: &conf.Config{NodeCfg: conf.NodeConfig{DataDir: dir}}}
	c := conf.DefaultAICfg().DDN
	c.ProviderDID = "did:n42:local"
	for backend, file := range map[string]string{"native-bayes": "bayes.json", "native-transformer": "transformer.json"} {
		c.Backend = backend
		c.ModelFile = file
		c.ModelHash = ""
		p, err := n.ddnProvider(c)
		if err != nil {
			t.Fatal(err)
		}
		r, err := p.Decide(ctx, d.DecisionRequest{Task: "node.anomaly", SchemaID: "health-v1"}, "disk full")
		if err != nil || r.Label != "STORAGE" || !r.NeedEscalation {
			t.Fatal(r, err)
		}
		c.ModelHash = p.Identity().ModelHash.Hex()
		if _, err = n.ddnProvider(c); err != nil {
			t.Fatal("valid hash pin failed", err)
		}
		c.ModelHash = "0x0100000000000000000000000000000000000000000000000000000000000000"
		if _, err = n.ddnProvider(c); err == nil {
			t.Fatal("wrong hash pin accepted")
		}
	}
	c.ModelHash = ""
	c.QuorumSize = 2
	c.Sidecars = []conf.DDNSidecarCfg{{Backend: "native-rules", ProviderDID: "did:n42:rules"}, {Backend: "native-bayes", ModelFile: "bayes.json", ProviderDID: "did:n42:bayes"}}
	p, err := n.ddnProvider(c)
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.Decide(ctx, d.DecisionRequest{Task: "node.anomaly", SchemaID: "health-v1", Quorum: 2, MaxCost: "0"}, "disk full")
	if err != nil || r.Label != "STORAGE" || !r.NeedEscalation {
		t.Fatal(r, err)
	}
	c.Sidecars = nil
	c.QuorumSize = 1
	c.Backend = "native-transformer"
	c.ModelFile = "bayes.json"
	if _, err = n.ddnProvider(c); err == nil {
		t.Fatal("mismatched artifact fell back to network")
	}
}

func TestDDNLearnedModelSignedReceipt(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	a, err := native.Train(ctx, "colors", "1", "test.color", "colors-v1", []native.Example{{Label: "RED", Text: "red red"}, {Label: "BLUE", Text: "blue blue"}}, 600000, 500000)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := a.Bytes()
	os.WriteFile(filepath.Join(dir, "bayes.json"), b, 0600)
	tf, err := transformer.Initialize("colors", "1", "test.color", "colors-v1", []string{"RED", "BLUE"}, transformer.Config{Hidden: 4, Heads: 2, Layers: 1, FeedForward: 8, MaxSequence: 16}, 600000, 42)
	if err != nil {
		t.Fatal(err)
	}
	tf, _, err = transformer.Train(ctx, tf, []transformer.Example{{Label: "RED", Text: "red"}, {Label: "BLUE", Text: "blue"}, {Label: "RED", Text: "red red"}, {Label: "BLUE", Text: "blue blue"}}, transformer.TrainConfig{Epochs: 45, LearningRate: 0.015, ClipNorm: 1})
	if err != nil {
		t.Fatal(err)
	}
	b, _ = tf.Bytes()
	os.WriteFile(filepath.Join(dir, "transformer.json"), b, 0600)
	for backend, file := range map[string]string{"native-bayes": "bayes.json", "native-transformer": "transformer.json"} {
		t.Run(backend, func(t *testing.T) {
			key, err := crypto.GenerateKey()
			if err != nil {
				t.Fatal(err)
			}
			s, err := receipt.NewSigner(key)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			cfg := &conf.Config{AICfg: conf.DefaultAICfg(), ChainCfg: &params.ChainConfig{ChainID: big.NewInt(94)}, NodeCfg: conf.NodeConfig{DataDir: dir}}
			cfg.AICfg.DDN.Enabled = true
			cfg.AICfg.DDN.GatewayEnabled = true
			cfg.AICfg.DDN.Backend = backend
			cfg.AICfg.DDN.ModelFile = file
			cfg.AICfg.DDN.ProviderDID = s.DID()
			n := &Node{config: cfg, runtimePlan: auxiliaryRuntimePlan{startDistributed: true}}
			n.startDDNRuntime()
			if n.ddnGateway == nil {
				t.Fatal("native model runtime did not start")
			}
			defer n.ddnGateway.Stop()
			if err = n.ddnGateway.SetSigner(s); err != nil {
				t.Fatal(err)
			}
			info := n.ddnGateway.Info()
			if len(info.Labels) != 2 || info.Labels[0] != "BLUE" {
				t.Fatal(info)
			}
			r := d.DecisionRequest{Version: 1, ChainID: 94, Task: "test.color", SchemaID: "colors-v1", InputHash: crypto.Keccak256Hash([]byte("red")), PrivacyMode: "public", Quorum: 1, MaxLatencyMs: 500, MaxCost: "0", Deadline: uint64(time.Now().Add(time.Minute).UnixMilli()), Requester: "did:n42:tester", Nonce: 1}
			r.ModelRequirements.ModelHash = info.Provider.ModelHash
			id, err := n.ddnGateway.Submit(r, "red")
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				rec, err := n.ddnGateway.GetReceipt(id)
				if err != nil {
					t.Fatal(err)
				}
				if rec.Status != "pending" {
					if rec.Receipt == nil || rec.Receipt.Result.Label != "RED" || len(rec.Receipt.Result.Answers) != 1 || rec.Receipt.Result.Answers[0].Selected != 1 {
						t.Fatal(rec)
					}
					if err = receipt.Verify(*rec.Receipt, r, crypto.PubkeyToAddress(key.PublicKey), uint64(time.Now().UnixMilli())); err != nil {
						t.Fatal(err)
					}
					return
				}
				time.Sleep(time.Millisecond)
			}
			t.Fatal("learned model receipt timed out")
		})
	}
}

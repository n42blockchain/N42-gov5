// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package params

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func performanceEncodingTestConfig(t *testing.T, height uint64) *ChainConfig {
	t.Helper()
	cfg := performanceTestConfig()
	cfg.Performance.Version = PerformanceEd25519Version
	cfg.Performance.Transactions = PerformanceTransactionsEd25519
	cfg.Performance.SignatureRule = PerformanceEd25519RuleID
	cfg.Performance.Ed25519ActivationBlock = height
	ns, _, err := cfg.PerformanceEncodingIdentity()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Performance.NetworkNamespace = ns
	return cfg
}
func TestPerformanceEncodingIdentityAndRuntimeClosed(t *testing.T) {
	// Fixed independent binary-descriptor vectors; zero activation is explicit.
	for _, test := range []struct {
		height uint64
		digest string
	}{{0, "62ae0ba9eea2f07c240f46cf2ebed288113317e90f234aafb76b917ea38bc95d"}, {7, "14e95a0a556f9e570ed57ecdb053a9cc22783d43272d90b79df85fbd30da4560"}} {
		cfg := performanceEncodingTestConfig(t, test.height)
		ns, marker, err := cfg.PerformanceEncodingIdentity()
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(marker[:]) != test.digest || !strings.HasSuffix(ns, test.digest) || ns == PerformanceNamespace {
			t.Fatal("protocol identity vector mismatch")
		}
		if err := cfg.ValidatePerformanceEncoding(); err != nil {
			t.Fatal(err)
		}
		for _, profileName := range []string{"n42", "eth-el"} {
			profile, err := ResolveExecutionProfile(profileName)
			if err != nil {
				t.Fatal(err)
			}
			if err := cfg.ValidatePerformanceSelection("private", profile); err == nil {
				t.Fatal("offline v2 activated runtime")
			}
		}
		changed := performanceEncodingTestConfig(t, test.height+1)
		if PerformanceConfigsEqual(cfg.Performance, changed.Performance) {
			t.Fatal("activation omitted from immutable binding")
		}
		for _, height := range []uint64{0, 1, 100} {
			if err := cfg.CheckCompatible(changed, height); err == nil {
				t.Fatal("database allowed rule drift")
			}
		}
		// Namespace itself cannot be substituted, even if metadata fields are valid.
		cfg.Performance.NetworkNamespace = PerformanceNamespace
		if err := cfg.ValidatePerformanceEncoding(); err == nil {
			t.Fatal("legacy namespace accepted")
		}
	}
	legacy := performanceTestConfig()
	before, _ := json.Marshal(legacy.Performance)
	if strings.Contains(string(before), "signatureRule") || strings.Contains(string(before), "ed25519ActivationBlock") {
		t.Fatal("legacy serialization changed")
	}
	for _, mutate := range []func(*PerformanceChainConfig){func(p *PerformanceChainConfig) { p.SignatureRule = PerformanceEd25519RuleID }, func(p *PerformanceChainConfig) { p.Ed25519ActivationBlock = 1 }} {
		cfg := performanceTestConfig()
		mutate(cfg.Performance)
		if err := cfg.ValidatePerformanceEncoding(); err == nil {
			t.Fatal("v1 silently ignored new metadata")
		}
	}
}

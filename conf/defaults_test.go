package conf

import "testing"

func TestApplyDefaultsSetsNodeProfile(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)
	if cfg.NodeCfg.Profile != "n42" {
		t.Fatalf("profile = %q, want %q", cfg.NodeCfg.Profile, "n42")
	}
}

func TestValidateRejectsUnknownNodeProfile(t *testing.T) {
	cfg := &Config{
		NodeCfg: NodeConfig{
			Profile: "mystery",
			Chain:   "private",
		},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected Validate to reject unknown profile")
	}
}

package discover

import (
	"testing"

	"github.com/n42blockchain/N42/common/mclock"
	"github.com/n42blockchain/N42/internal/p2p/enode"
	"github.com/n42blockchain/N42/log"
)

func TestConfigWithDefaults(t *testing.T) {
	var cfg Config
	out := cfg.withDefaults()

	if out.Log == nil {
		t.Fatal("expected default Log to be set")
	}
	if out.ValidSchemes == nil {
		t.Fatal("expected default ValidSchemes to be set")
	}
	if out.Clock == nil {
		t.Fatal("expected default Clock to be set")
	}
}

func TestConfigWithDefaultsPreservesExplicitValues(t *testing.T) {
	customLog := log.New()
	customClock := mclock.System{}
	cfg := Config{
		Log:          customLog,
		ValidSchemes: enode.ValidSchemesForTesting,
		Clock:        customClock,
	}
	out := cfg.withDefaults()

	if out.Log != customLog {
		t.Fatal("expected explicit Log to be preserved")
	}
	if out.ValidSchemes == nil {
		t.Fatal("expected explicit ValidSchemes to be preserved")
	}
}

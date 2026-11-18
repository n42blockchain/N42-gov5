package conf

import (
	"strings"
	"testing"

	"github.com/n42blockchain/N42/params"
)

func pruneCfg(mode PruneMode, retention, undoWindow uint64) *Config {
	return &Config{
		NodeCfg:  NodeConfig{Profile: "n42", Chain: "private", QMDBUndoWindow: undoWindow},
		ChainCfg: &params.ChainConfig{Consensus: params.Faker},
		PruneCfg: PruneConfig{Mode: mode, BlockRetention: retention},
	}
}

func TestValidatePruneRetentionCoversUnwindReach(t *testing.T) {
	cases := []struct {
		mode       PruneMode
		retention  uint64
		undoWindow uint64
		ok         bool
	}{
		{PruneModeFull, 90000, 0, true},
		{PruneModeFull, 640, 0, true},     // 512 realign depth + 128
		{PruneModeFull, 639, 0, false},    // one short
		{PruneModeFull, 300, 0, false},    // below the realign depth
		{PruneModeFull, 640, 1000, false}, // below a larger undo window
		{PruneModeFull, 1128, 1000, true},
		{PruneModeArchive, 10, 0, true}, // retention unused in archive mode
	}
	for _, c := range cases {
		err := Validate(pruneCfg(c.mode, c.retention, c.undoWindow))
		if (err == nil) != c.ok {
			t.Fatalf("mode=%s retention=%d window=%d: err=%v, want ok=%v", c.mode, c.retention, c.undoWindow, err, c.ok)
		}
		if err != nil && !strings.Contains(err.Error(), "prune retention") {
			t.Fatalf("unexpected error %v", err)
		}
	}
	if got := MinPruneRetention(0); got != QMDBRealignMaxDepth+PruneRetentionMargin {
		t.Fatalf("MinPruneRetention(0) = %d", got)
	}
}

func TestValidateRejectsHugeQMDBUndoWindow(t *testing.T) {
	if err := Validate(pruneCfg(PruneModeArchive, 0, MaxQMDBUndoWindow)); err != nil {
		t.Fatalf("max window: %v", err)
	}
	if err := Validate(pruneCfg(PruneModeArchive, 0, MaxQMDBUndoWindow+1)); err == nil {
		t.Fatal("window above the maximum accepted")
	}
}

// Defaults (retention 90000) pass in full mode, so config files that only
// set mode=full keep working.
func TestValidateAndApplyDefaultsFullModeDefaultRetention(t *testing.T) {
	cfg := pruneCfg(PruneModeFull, 0, 0)
	ApplyDefaults(cfg)
	if err := Validate(cfg); err != nil {
		t.Fatal(err)
	}
}

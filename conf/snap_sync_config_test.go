package conf

import "testing"

func TestSnapSyncConfig_Validate_Defaults(t *testing.T) {
	cfg := &SnapSyncConfig{Enable: true}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error with defaults, got %v", err)
	}
	if cfg.PivotDistance != DefaultSnapPivotDistance {
		t.Errorf("PivotDistance: want %d, got %d", DefaultSnapPivotDistance, cfg.PivotDistance)
	}
	if cfg.MaxBytesPerReq != DefaultSnapMaxBytesPerReq {
		t.Errorf("MaxBytesPerReq: want %d, got %d", DefaultSnapMaxBytesPerReq, cfg.MaxBytesPerReq)
	}
	if cfg.MinSnapPeers != DefaultSnapMinPeers {
		t.Errorf("MinSnapPeers: want %d, got %d", DefaultSnapMinPeers, cfg.MinSnapPeers)
	}
}

func TestSnapSyncConfig_Validate_Disabled(t *testing.T) {
	cfg := &SnapSyncConfig{Enable: false, PivotDistance: 9999}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("disabled config should not be validated, got %v", err)
	}
}

func TestSnapSyncConfig_Validate_PivotTooHigh(t *testing.T) {
	cfg := &SnapSyncConfig{Enable: true, PivotDistance: 500}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for PivotDistance > 256")
	}
}

func TestSnapSyncConfig_Validate_MaxBytesTooLow(t *testing.T) {
	cfg := &SnapSyncConfig{Enable: true, MaxBytesPerReq: 1024} // 1KB < 64KB min
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for MaxBytesPerReq < 64KB")
	}
}

func TestSnapSyncConfig_Validate_MaxBytesTooHigh(t *testing.T) {
	cfg := &SnapSyncConfig{Enable: true, MaxBytesPerReq: 16 * 1024 * 1024} // 16MB > 8MB max
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for MaxBytesPerReq > 8MB")
	}
}

func TestSnapSyncConfig_Validate_MinPeersTooHigh(t *testing.T) {
	cfg := &SnapSyncConfig{Enable: true, MinSnapPeers: 100}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for MinSnapPeers > 32")
	}
}

func TestSnapSyncConfig_Validate_ConcurrencyTooHigh(t *testing.T) {
	cfg := &SnapSyncConfig{Enable: true, MaxConcurrency: 128}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for MaxConcurrency > 64")
	}
}

func TestSnapshotConfig_Validate_Defaults(t *testing.T) {
	cfg := &SnapshotConfig{Enable: true}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if cfg.Interval != DefaultSnapshotInterval {
		t.Errorf("Interval: want %d, got %d", DefaultSnapshotInterval, cfg.Interval)
	}
	if cfg.MaxRetained != DefaultSnapshotMaxRetained {
		t.Errorf("MaxRetained: want %d, got %d", DefaultSnapshotMaxRetained, cfg.MaxRetained)
	}
}

func TestSnapshotConfig_Validate_IntervalTooLow(t *testing.T) {
	cfg := &SnapshotConfig{Enable: true, Interval: 10}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for Interval < 100")
	}
}

func TestSnapshotConfig_Validate_MaxRetainedOutOfRange(t *testing.T) {
	cfg := &SnapshotConfig{Enable: true, MaxRetained: 200}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for MaxRetained > 100")
	}
}

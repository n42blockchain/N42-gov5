package conf

import "testing"

func TestDefaultTorrentDistCfg(t *testing.T) {
	cfg := DefaultTorrentDistCfg()
	if cfg.Enabled {
		t.Fatal("should be disabled by default")
	}
	if cfg.DataDir == "" {
		t.Fatal("empty data dir")
	}
	if cfg.ListenAddr == "" {
		t.Fatal("empty listen addr")
	}
	if cfg.MaxSeedBytes <= 0 {
		t.Fatal("invalid max seed bytes")
	}
	if cfg.MaxActiveTorrents <= 0 {
		t.Fatal("invalid max active torrents")
	}
	if !cfg.EnableDHT {
		t.Fatal("DHT should be enabled by default")
	}
	if !cfg.EnablePEX {
		t.Fatal("PEX should be enabled by default")
	}
	if !cfg.SeedOnStore {
		t.Fatal("seed-on-store should be enabled by default")
	}
	if cfg.PieceSize <= 0 {
		t.Fatal("invalid piece size")
	}
}

func TestDefaultEd2kCfg(t *testing.T) {
	cfg := DefaultEd2kCfg()
	if cfg.Enabled {
		t.Fatal("should be disabled by default")
	}
	if cfg.MaxFileSize <= 0 {
		t.Fatal("invalid max file size")
	}
	if cfg.ChunkSize != 9728000 {
		t.Fatalf("chunk size = %d, want 9728000", cfg.ChunkSize)
	}
	if !cfg.EnableLinks {
		t.Fatal("links should be enabled by default")
	}
}

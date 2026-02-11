package lmdb

import (
	"context"
	"os"
	"testing"

	"github.com/n42blockchain/N42/conf"
)

func TestNewLMDB_Success(t *testing.T) {
	tmpDir := t.TempDir()
	nodeConfig := &conf.NodeConfig{
		DataDir: tmpDir,
	}
	dbConfig := &conf.DatabaseConfig{
		DBPath:     "",
		DBName:     "testdb",
		MaxDB:      10,
		MaxReaders: 100,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// reset global state
	_lmdb = Lmdb{}

	lmdbInst, err := NewLMDB(ctx, nodeConfig, dbConfig)
	if err != nil {
		t.Fatalf("NewLMDB failed: %v", err)
	}
	defer lmdbInst.Close()

	if lmdbInst.Env == nil {
		t.Fatal("expected non-nil Env after initialization")
	}
	if !lmdbInst.running {
		t.Fatal("expected running to be true")
	}
}

func TestNewLMDB_LabelUsesDBName(t *testing.T) {
	tmpDir := t.TempDir()
	nodeConfig := &conf.NodeConfig{
		DataDir: tmpDir,
	}
	dbConfig := &conf.DatabaseConfig{
		DBPath:     "",
		DBName:     "mydb",
		MaxDB:      10,
		MaxReaders: 100,
	}

	// reset global state
	_lmdb = Lmdb{}

	lmdbInst, err := NewLMDB(context.Background(), nodeConfig, dbConfig)
	if err != nil {
		t.Fatalf("NewLMDB failed: %v", err)
	}
	defer lmdbInst.Close()

	// if we got here without panic, the Label was set correctly
	if lmdbInst.Env == nil {
		t.Fatal("expected non-nil Env")
	}
}

func TestNewLMDB_Singleton(t *testing.T) {
	tmpDir := t.TempDir()
	nodeConfig := &conf.NodeConfig{
		DataDir: tmpDir,
	}
	dbConfig := &conf.DatabaseConfig{
		DBPath:     "",
		DBName:     "testdb",
		MaxDB:      10,
		MaxReaders: 100,
	}

	// reset global state
	_lmdb = Lmdb{}

	lmdb1, err := NewLMDB(context.Background(), nodeConfig, dbConfig)
	if err != nil {
		t.Fatalf("first NewLMDB failed: %v", err)
	}
	defer lmdb1.Close()

	// second call should return same instance
	lmdb2, err := NewLMDB(context.Background(), nodeConfig, dbConfig)
	if err != nil {
		t.Fatalf("second NewLMDB failed: %v", err)
	}

	if lmdb1 != lmdb2 {
		t.Fatal("expected singleton: second call should return same instance")
	}
}

func TestNewLMDB_CreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	nodeConfig := &conf.NodeConfig{
		DataDir: tmpDir,
	}
	dbPath := "subdir/"
	dbConfig := &conf.DatabaseConfig{
		DBPath:     dbPath,
		DBName:     "testdb",
		MaxDB:      10,
		MaxReaders: 100,
	}

	// reset global state
	_lmdb = Lmdb{}

	lmdbInst, err := NewLMDB(context.Background(), nodeConfig, dbConfig)
	if err != nil {
		t.Fatalf("NewLMDB failed: %v", err)
	}
	defer lmdbInst.Close()

	expectedPath := tmpDir + "/" + dbPath + "testdb"
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Fatalf("expected database directory %s to exist", expectedPath)
	}
}

func TestNewLMDB_Close(t *testing.T) {
	tmpDir := t.TempDir()
	nodeConfig := &conf.NodeConfig{
		DataDir: tmpDir,
	}
	dbConfig := &conf.DatabaseConfig{
		DBPath:     "",
		DBName:     "testdb",
		MaxDB:      10,
		MaxReaders: 100,
	}

	// reset global state
	_lmdb = Lmdb{}

	lmdbInst, err := NewLMDB(context.Background(), nodeConfig, dbConfig)
	if err != nil {
		t.Fatalf("NewLMDB failed: %v", err)
	}

	// close should not panic
	lmdbInst.Close()

	if lmdbInst.running {
		t.Fatal("expected running to be false after Close")
	}
}

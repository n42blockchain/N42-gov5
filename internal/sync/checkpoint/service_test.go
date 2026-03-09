// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package checkpoint

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/sync/snapsync"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func init() {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
}

func TestCheckpointConfig_Defaults(t *testing.T) {
	cfg := &conf.CheckpointConfig{}

	if cfg.IsEnabled() {
		t.Error("default config should not be enabled")
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("disabled config should validate without error, got: %v", err)
	}
}

func TestCheckpointConfig_ParseHash(t *testing.T) {
	tests := []struct {
		name    string
		cfg     conf.CheckpointConfig
		wantErr bool
	}{
		{
			name: "valid config",
			cfg: conf.CheckpointConfig{
				Enable:      true,
				BlockNumber: 1000,
				BlockHash:   "0xabcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
			},
			wantErr: false,
		},
		{
			name: "missing 0x prefix",
			cfg: conf.CheckpointConfig{
				Enable:      true,
				BlockNumber: 1000,
				BlockHash:   "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
			},
			wantErr: true,
		},
		{
			name: "hash too short",
			cfg: conf.CheckpointConfig{
				Enable:      true,
				BlockNumber: 1000,
				BlockHash:   "0xabcdef",
			},
			wantErr: true,
		},
		{
			name: "zero block number",
			cfg: conf.CheckpointConfig{
				Enable:      true,
				BlockNumber: 0,
				BlockHash:   "0xabcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
			},
			wantErr: true,
		},
		{
			name: "empty hash",
			cfg: conf.CheckpointConfig{
				Enable:      true,
				BlockNumber: 1000,
				BlockHash:   "",
			},
			wantErr: true,
		},
		{
			name:    "disabled with no params",
			cfg:     conf.CheckpointConfig{Enable: false},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCheckpointService_Disabled(t *testing.T) {
	svc := NewService(&Config{
		Checkpoint: &conf.CheckpointConfig{Enable: false},
	})

	pivot, err := svc.Start(context.Background())
	if err != nil {
		t.Fatalf("disabled service should not return error, got: %v", err)
	}
	if pivot != 0 {
		t.Errorf("disabled service should return pivot 0, got: %d", pivot)
	}
}

func TestCheckpointService_NilConfig(t *testing.T) {
	svc := NewService(&Config{
		Checkpoint: nil,
	})

	pivot, err := svc.Start(context.Background())
	if err != nil {
		t.Fatalf("nil config should not return error, got: %v", err)
	}
	if pivot != 0 {
		t.Errorf("nil config should return pivot 0, got: %d", pivot)
	}
}

func TestCheckpointService_AlreadyHaveBlock(t *testing.T) {
	db := memdb.NewTestDB(t)
	ctx := context.Background()

	// Pre-populate DB with a canonical hash at block 1000.
	expectedHash := types.HexToHash("0xabcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	err := db.Update(ctx, func(tx kv.RwTx) error {
		return rawdb.WriteCanonicalHash(tx, expectedHash, 1000)
	})
	if err != nil {
		t.Fatalf("failed to pre-populate DB: %v", err)
	}

	svc := NewService(&Config{
		Checkpoint: &conf.CheckpointConfig{
			Enable:      true,
			BlockNumber: 1000,
			BlockHash:   "0xabcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		},
		DB: db,
	})

	pivot, err := svc.Start(ctx)
	if err != nil {
		t.Fatalf("should skip download when block exists, got error: %v", err)
	}
	if pivot != 1000 {
		t.Errorf("expected pivot 1000, got: %d", pivot)
	}

	// Verify pivot was saved.
	var savedPivot uint64
	err = db.View(ctx, func(tx kv.Tx) error {
		savedPivot, err = snapsync.LoadPivotBlock(tx)
		return err
	})
	if err != nil {
		t.Fatalf("failed to load pivot: %v", err)
	}
	if savedPivot != 1000 {
		t.Errorf("expected saved pivot 1000, got: %d", savedPivot)
	}
}

func TestCheckpointService_HashMismatch(t *testing.T) {
	db := memdb.NewTestDB(t)
	ctx := context.Background()

	// Pre-populate DB with a DIFFERENT hash at block 1000.
	wrongHash := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	err := db.Update(ctx, func(tx kv.RwTx) error {
		return rawdb.WriteCanonicalHash(tx, wrongHash, 1000)
	})
	if err != nil {
		t.Fatalf("failed to pre-populate DB: %v", err)
	}

	svc := NewService(&Config{
		Checkpoint: &conf.CheckpointConfig{
			Enable:      true,
			BlockNumber: 1000,
			BlockHash:   "0xabcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		},
		DB: db,
	})

	_, err = svc.Start(ctx)
	if err == nil {
		t.Fatal("should return error when existing block hash mismatches checkpoint")
	}
	t.Logf("Got expected error: %v", err)
}

func TestCheckpointConfig_LatestCheckpoint(t *testing.T) {
	// Mainnet and testnet checkpoints are currently empty.
	if cp := conf.LatestCheckpoint("mainnet"); cp != nil {
		t.Error("expected nil for empty mainnet checkpoints")
	}
	if cp := conf.LatestCheckpoint("testnet"); cp != nil {
		t.Error("expected nil for empty testnet checkpoints")
	}
	if cp := conf.LatestCheckpoint("unknown"); cp != nil {
		t.Error("expected nil for unknown chain")
	}
}

func TestCheckpointConfig_IsEnabled(t *testing.T) {
	tests := []struct {
		name    string
		cfg     conf.CheckpointConfig
		enabled bool
	}{
		{
			name:    "fully configured",
			cfg:     conf.CheckpointConfig{Enable: true, BlockNumber: 100, BlockHash: "0xabc"},
			enabled: true,
		},
		{
			name:    "enable false",
			cfg:     conf.CheckpointConfig{Enable: false, BlockNumber: 100, BlockHash: "0xabc"},
			enabled: false,
		},
		{
			name:    "zero block number",
			cfg:     conf.CheckpointConfig{Enable: true, BlockNumber: 0, BlockHash: "0xabc"},
			enabled: false,
		},
		{
			name:    "empty hash",
			cfg:     conf.CheckpointConfig{Enable: true, BlockNumber: 100, BlockHash: ""},
			enabled: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.IsEnabled(); got != tt.enabled {
				t.Errorf("IsEnabled() = %v, want %v", got, tt.enabled)
			}
		})
	}
}

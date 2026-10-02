// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"context"
	"math/big"
	"os"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
)

// apiTNewEvidenceAPI creates a ConsensusAPI backed by a real memdb so that
// GetConsensusEvidence / GetCommittee / GetValidatorDuties can read rows
// written via rawdb.WriteConsensusEvidence.
func apiTNewEvidenceAPI(t *testing.T) (*ConsensusAPI, kv.RwDB) {
	t.Helper()
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() {
		kv.ChaindataTablesCfg = prevTables
	})

	db := memdb.NewTestDB(t)
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	a := &API{db: db, chainConfig: cfg}
	return NewConsensusAPI(a), db
}

func apiTWriteEvidence(t *testing.T, db kv.RwDB, num uint64, ce *rawdb.ConsensusEvidence) {
	t.Helper()
	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return rawdb.WriteConsensusEvidence(tx, num, ce)
	})
	if err != nil {
		t.Fatalf("WriteConsensusEvidence: %v", err)
	}
}

func TestConsensusAPI_GetConsensusEvidence_NotFound(t *testing.T) {
	c, _ := apiTNewEvidenceAPI(t)
	res, err := c.GetConsensusEvidence(context.Background(), jsonrpc.BlockNumber(5))
	if err != nil {
		t.Fatalf("GetConsensusEvidence() error = %v", err)
	}
	if res != nil {
		t.Fatalf("GetConsensusEvidence() = %+v, want nil (no evidence)", res)
	}
}

func TestConsensusAPI_GetConsensusEvidence_Basic(t *testing.T) {
	c, db := apiTNewEvidenceAPI(t)
	ce := &rawdb.ConsensusEvidence{
		View:          7,
		BlockHash:     types.HexToHash("0xabcd"),
		SignerCount:   3,
		SignersPacked: []byte{0x05},
	}
	apiTWriteEvidence(t, db, 3, ce)

	res, err := c.GetConsensusEvidence(context.Background(), jsonrpc.BlockNumber(3))
	if err != nil {
		t.Fatalf("GetConsensusEvidence() error = %v", err)
	}
	if res == nil {
		t.Fatal("GetConsensusEvidence() = nil, want result")
	}
	if res.View != 7 || res.SignerCount != 3 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.HasMobile {
		t.Fatal("HasMobile = true, want false")
	}
}

func TestConsensusAPI_GetConsensusEvidence_WithMobile(t *testing.T) {
	c, db := apiTNewEvidenceAPI(t)
	ce := &rawdb.ConsensusEvidence{
		View:                  1,
		BlockHash:             types.HexToHash("0x01"),
		SignerCount:           1,
		SignersPacked:         []byte{0x01},
		HasMobile:             true,
		MobReceiptsRoot:       types.HexToHash("0x02"),
		MobParticipantCount:   2,
		MobParticipantsPacked: []byte{0x03},
		MobCreatedAtMs:        1234,
	}
	apiTWriteEvidence(t, db, 1, ce)

	res, err := c.GetConsensusEvidence(context.Background(), jsonrpc.BlockNumber(1))
	if err != nil {
		t.Fatalf("GetConsensusEvidence() error = %v", err)
	}
	if res == nil || !res.HasMobile {
		t.Fatalf("expected mobile evidence, got %+v", res)
	}
	if res.MobileReceiptsRoot == nil || *res.MobileReceiptsRoot != ce.MobReceiptsRoot {
		t.Fatalf("MobileReceiptsRoot mismatch: %+v", res.MobileReceiptsRoot)
	}
	if res.MobileParticipantCount == nil || *res.MobileParticipantCount != 2 {
		t.Fatalf("MobileParticipantCount mismatch: %+v", res.MobileParticipantCount)
	}
}

func TestConsensusAPI_EnsureConfig_Defaults(t *testing.T) {
	os.Unsetenv("N42_BLS_POOL_SIZE")
	os.Unsetenv("N42_BLS_COMMITTEE")
	os.Unsetenv("N42_BLS_RAMP_BLOCKS")
	c, _ := apiTNewEvidenceAPI(t)
	c.ensureConfig()
	if c.poolSize != 200000 || c.committee != 512 || c.rampBlocks != 1000000 {
		t.Fatalf("unexpected defaults: poolSize=%d committee=%d ramp=%d", c.poolSize, c.committee, c.rampBlocks)
	}
}

func TestConsensusAPI_EnsurePool_NoSeed(t *testing.T) {
	os.Unsetenv("N42_BLS_POOL_SEED")
	c, _ := apiTNewEvidenceAPI(t)
	if err := c.ensurePool(); err == nil {
		t.Fatal("ensurePool() error = nil, want error (no seed configured)")
	}
}

func TestConsensusAPI_EnsurePool_BadSeed(t *testing.T) {
	os.Setenv("N42_BLS_POOL_SEED", "zz")
	defer os.Unsetenv("N42_BLS_POOL_SEED")
	c, _ := apiTNewEvidenceAPI(t)
	if err := c.ensurePool(); err == nil {
		t.Fatal("ensurePool() error = nil, want error (bad hex)")
	}
}

func TestConsensusAPI_EnsurePool_Success(t *testing.T) {
	os.Setenv("N42_BLS_POOL_SEED", "0x000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	os.Setenv("N42_BLS_POOL_SIZE", "4")
	os.Setenv("N42_BLS_COMMITTEE", "2")
	defer func() {
		os.Unsetenv("N42_BLS_POOL_SEED")
		os.Unsetenv("N42_BLS_POOL_SIZE")
		os.Unsetenv("N42_BLS_COMMITTEE")
	}()
	c, _ := apiTNewEvidenceAPI(t)
	if err := c.ensurePool(); err != nil {
		t.Fatalf("ensurePool() error = %v", err)
	}
	if len(c.poolPks) != 4 {
		t.Fatalf("poolPks len = %d, want 4", len(c.poolPks))
	}
	// Second call is a no-op (sync.Once) and must not error.
	if err := c.ensurePool(); err != nil {
		t.Fatalf("ensurePool() second call error = %v", err)
	}
}

func TestConsensusAPI_GetCommittee(t *testing.T) {
	os.Setenv("N42_BLS_POOL_SEED", "0x000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	os.Setenv("N42_BLS_POOL_SIZE", "4")
	os.Setenv("N42_BLS_COMMITTEE", "2")
	os.Setenv("N42_BLS_RAMP_BLOCKS", "1")
	defer func() {
		os.Unsetenv("N42_BLS_POOL_SEED")
		os.Unsetenv("N42_BLS_POOL_SIZE")
		os.Unsetenv("N42_BLS_COMMITTEE")
		os.Unsetenv("N42_BLS_RAMP_BLOCKS")
	}()
	c, db := apiTNewEvidenceAPI(t)
	ce := &rawdb.ConsensusEvidence{
		View:          10,
		BlockHash:     types.HexToHash("0xbeef"),
		SignerCount:   2,
		SignersPacked: []byte{0x03},
	}
	apiTWriteEvidence(t, db, 10, ce)

	res, err := c.GetCommittee(context.Background(), jsonrpc.BlockNumber(10))
	if err != nil {
		t.Fatalf("GetCommittee() error = %v", err)
	}
	if res == nil {
		t.Fatal("GetCommittee() = nil, want result")
	}
	if len(res.Members) == 0 {
		t.Fatal("GetCommittee() members empty")
	}

	// Not found path.
	res2, err := c.GetCommittee(context.Background(), jsonrpc.BlockNumber(99))
	if err != nil {
		t.Fatalf("GetCommittee(not found) error = %v", err)
	}
	if res2 != nil {
		t.Fatalf("GetCommittee(not found) = %+v, want nil", res2)
	}
}

func TestConsensusAPI_GetCommittee_PoolError(t *testing.T) {
	os.Unsetenv("N42_BLS_POOL_SEED")
	c, _ := apiTNewEvidenceAPI(t)
	if _, err := c.GetCommittee(context.Background(), jsonrpc.BlockNumber(0)); err == nil {
		t.Fatal("GetCommittee() error = nil, want pool error")
	}
}

func TestConsensusAPI_GetValidatorPool(t *testing.T) {
	os.Unsetenv("N42_BLS_POOL_SEED")
	c, _ := apiTNewEvidenceAPI(t)
	res, err := c.GetValidatorPool(context.Background(), jsonrpc.BlockNumber(0))
	if err != nil {
		t.Fatalf("GetValidatorPool() error = %v", err)
	}
	if res.TotalPoolSize != 200000 {
		t.Fatalf("TotalPoolSize = %d, want 200000", res.TotalPoolSize)
	}
}

func TestConsensusAPI_GetValidator(t *testing.T) {
	os.Setenv("N42_BLS_POOL_SEED", "0x000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	os.Setenv("N42_BLS_POOL_SIZE", "2")
	defer func() {
		os.Unsetenv("N42_BLS_POOL_SEED")
		os.Unsetenv("N42_BLS_POOL_SIZE")
	}()
	c, _ := apiTNewEvidenceAPI(t)

	v, err := c.GetValidator(context.Background(), 0)
	if err != nil {
		t.Fatalf("GetValidator(0) error = %v", err)
	}
	if len(v.PubKey) == 0 {
		t.Fatal("GetValidator(0) empty pubkey")
	}

	if _, err := c.GetValidator(context.Background(), 99); err == nil {
		t.Fatal("GetValidator(out of range) error = nil, want error")
	}
}

func TestConsensusAPI_GetValidatorDuties(t *testing.T) {
	c, db := apiTNewEvidenceAPI(t)
	apiTWriteEvidence(t, db, 1, &rawdb.ConsensusEvidence{
		View:          1,
		BlockHash:     types.HexToHash("0x01"),
		SignerCount:   1,
		SignersPacked: []byte{0x01},
	})
	apiTWriteEvidence(t, db, 2, &rawdb.ConsensusEvidence{
		View:          2,
		BlockHash:     types.HexToHash("0x02"),
		SignerCount:   1,
		SignersPacked: []byte{0x00},
	})

	duties, err := c.GetValidatorDuties(context.Background(), 0, 0, 5)
	if err != nil {
		t.Fatalf("GetValidatorDuties() error = %v", err)
	}
	_ = duties // membership of validator 0 depends on sampling; just exercise the path

	if _, err := c.GetValidatorDuties(context.Background(), 0, 5, 2); err == nil {
		t.Fatal("GetValidatorDuties(toBlock<fromBlock) error = nil, want error")
	}
	if _, err := c.GetValidatorDuties(context.Background(), 0, 0, maxDutiesRange+1); err == nil {
		t.Fatal("GetValidatorDuties(range too large) error = nil, want error")
	}
}

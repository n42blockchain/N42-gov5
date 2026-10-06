package consensusrest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// The ConsensusEvidence table (and other N42-specific tables) are only
// registered into kv.ChaindataTablesCfg by modules.N42Init() plus this
// assignment — normally done once by the node's/cmd's main(). Without it,
// writes to ConsensusEvidence fail with "mdbx_cursor_open: permission
// denied" because the table falls back to unregistered/zero-value flags.
func init() {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
}

func consensusRestTestConfig() Config {
	var seed [32]byte
	seed[0] = 0x42
	return Config{Seed: seed, HasSeed: true, PoolSize: 8, Committee: 4, RampBlocks: 1}
}

func TestExplorerHandlerServesHTML(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/n42/explorer", nil)
	rec := httptest.NewRecorder()
	ExplorerHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html prefix", ct)
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "N42 Mobile-BLS Consensus") {
		t.Fatalf("body missing expected title, got %d bytes", len(body))
	}
}

func TestConfigFromEnvDefaultsAndOverrides(t *testing.T) {
	t.Setenv("N42_BLS_POOL_SIZE", "")
	t.Setenv("N42_BLS_COMMITTEE", "")
	t.Setenv("N42_BLS_RAMP_BLOCKS", "")
	t.Setenv("N42_BLS_POOL_SEED", "")
	cfg := ConfigFromEnv()
	if cfg.PoolSize != 200000 || cfg.Committee != 512 || cfg.RampBlocks != 1000000 {
		t.Fatalf("defaults = %+v", cfg)
	}
	if cfg.HasSeed {
		t.Fatal("HasSeed should be false without N42_BLS_POOL_SEED")
	}

	t.Setenv("N42_BLS_POOL_SIZE", "10")
	t.Setenv("N42_BLS_COMMITTEE", "4")
	t.Setenv("N42_BLS_RAMP_BLOCKS", "2")
	t.Setenv("N42_BLS_POOL_SEED", "0x"+strings.Repeat("ab", 32))
	cfg2 := ConfigFromEnv()
	if cfg2.PoolSize != 10 || cfg2.Committee != 4 || cfg2.RampBlocks != 2 {
		t.Fatalf("overrides = %+v", cfg2)
	}
	if !cfg2.HasSeed {
		t.Fatal("HasSeed should be true with a valid 32-byte seed")
	}
}

func TestConfigFromEnvIgnoresMalformedSeed(t *testing.T) {
	t.Setenv("N42_BLS_POOL_SEED", "0xnothex")
	cfg := ConfigFromEnv()
	if cfg.HasSeed {
		t.Fatal("HasSeed should be false for a malformed seed")
	}
	t.Setenv("N42_BLS_POOL_SEED", "0xaabb") // too short
	cfg = ConfigFromEnv()
	if cfg.HasSeed {
		t.Fatal("HasSeed should be false for a short seed")
	}
}

func TestConfigFromEnvIgnoresNonPositiveOverride(t *testing.T) {
	t.Setenv("N42_BLS_POOL_SIZE", "-5")
	cfg := ConfigFromEnv()
	if cfg.PoolSize != 200000 {
		t.Fatalf("PoolSize = %d, want default when override is non-positive", cfg.PoolSize)
	}
}

func TestNewServerAppliesZeroValueDefaults(t *testing.T) {
	s := NewServer(nil, Config{})
	if s.cfg.PoolSize != 200000 || s.cfg.Committee != 512 || s.cfg.RampBlocks != 1000000 {
		t.Fatalf("cfg = %+v, want defaults applied", s.cfg)
	}
}

func TestHealthEndpoint(t *testing.T) {
	s := NewServer(memdb.NewTestDB(t), consensusRestTestConfig())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Fatalf("body = %+v", body)
	}
}

func TestHandleEvidenceNotFound(t *testing.T) {
	s := NewServer(memdb.NewTestDB(t), consensusRestTestConfig())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n42/consensus/v1/block/5/evidence", nil))
	if rec.Code != 404 {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandleEvidenceBadID(t *testing.T) {
	s := NewServer(memdb.NewTestDB(t), consensusRestTestConfig())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n42/consensus/v1/block/not-a-number/evidence", nil))
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func seedEvidence(t *testing.T, db kv.RwDB, num uint64, ce *rawdb.ConsensusEvidence) {
	t.Helper()
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := rawdb.WriteConsensusEvidence(tx, num, ce); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestHandleEvidenceFound(t *testing.T) {
	db := memdb.NewTestDB(t)
	ce := &rawdb.ConsensusEvidence{
		View:          3,
		BlockHash:     types.HexToHash("0xabcd"),
		SignerCount:   4,
		SignersPacked: []byte{0x0F},
	}
	seedEvidence(t, db, 7, ce)

	s := NewServer(db, consensusRestTestConfig())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n42/consensus/v1/block/7/evidence", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["hasMobile"] != false {
		t.Fatalf("hasMobile = %v, want false", body["hasMobile"])
	}
}

func TestHandleEvidenceWithMobile(t *testing.T) {
	db := memdb.NewTestDB(t)
	ce := &rawdb.ConsensusEvidence{
		View:                  3,
		BlockHash:             types.HexToHash("0xabcd"),
		SignerCount:           4,
		SignersPacked:         []byte{0x0F},
		HasMobile:             true,
		MobReceiptsRoot:       types.HexToHash("0xbeef"),
		MobParticipantCount:   2,
		MobParticipantsPacked: []byte{0x03},
		MobCreatedAtMs:        12345,
	}
	seedEvidence(t, db, 8, ce)

	s := NewServer(db, consensusRestTestConfig())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n42/consensus/v1/block/8/evidence", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["hasMobile"] != true || body["mobileParticipantCount"] == nil {
		t.Fatalf("body = %+v, want mobile fields present", body)
	}
}

func TestResolveIDGenesisAndNumeric(t *testing.T) {
	s := NewServer(memdb.NewTestDB(t), consensusRestTestConfig())
	n, err := s.resolveID(context.Background(), "genesis")
	if err != nil || n != 0 {
		t.Fatalf("genesis -> (%d,%v), want (0,nil)", n, err)
	}
	n, err = s.resolveID(context.Background(), "42")
	if err != nil || n != 42 {
		t.Fatalf("42 -> (%d,%v), want (42,nil)", n, err)
	}
	if _, err := s.resolveID(context.Background(), "not-a-number"); err == nil {
		t.Fatal("expected error for non-numeric id")
	}
}

func TestResolveIDLatestWithoutHeadErrors(t *testing.T) {
	s := NewServer(memdb.NewTestDB(t), consensusRestTestConfig())
	if _, err := s.resolveID(context.Background(), "latest"); err == nil {
		t.Fatal("expected error when head is unavailable")
	}
	if _, err := s.resolveID(context.Background(), "head"); err == nil {
		t.Fatal("expected error when head is unavailable")
	}
}

func TestResolveIDLatestWithHead(t *testing.T) {
	db := memdb.NewTestDB(t)
	hash := types.HexToHash("0x0102")
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := rawdb.WriteHeaderNumber(tx, hash, 99); err != nil {
		t.Fatal(err)
	}
	rawdb.WriteHeadBlockHash(tx, hash)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	s := NewServer(db, consensusRestTestConfig())
	n, err := s.resolveID(context.Background(), "latest")
	if err != nil || n != 99 {
		t.Fatalf("latest -> (%d,%v), want (99,nil)", n, err)
	}
}

func TestHandleCommitteeWithoutSeed(t *testing.T) {
	db := memdb.NewTestDB(t)
	seedEvidence(t, db, 1, &rawdb.ConsensusEvidence{View: 1, SignerCount: 4, SignersPacked: []byte{0x01}})
	s := NewServer(db, Config{}) // no seed
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n42/consensus/v1/block/1/committee", nil))
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400 (no seed configured)", rec.Code)
	}
}

func TestHandleCommitteeWithSeed(t *testing.T) {
	db := memdb.NewTestDB(t)
	seedEvidence(t, db, 1, &rawdb.ConsensusEvidence{
		View: 1, BlockHash: types.HexToHash("0x01"), SignerCount: 4, SignersPacked: []byte{0x05},
	})
	s := NewServer(db, consensusRestTestConfig())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n42/consensus/v1/block/1/committee", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["committeeSize"] == nil {
		t.Fatalf("body = %+v, want committeeSize", body)
	}
}

func TestHandleCommitteeNotFound(t *testing.T) {
	s := NewServer(memdb.NewTestDB(t), consensusRestTestConfig())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n42/consensus/v1/block/1/committee", nil))
	if rec.Code != 404 {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandleVerifyNoSigners(t *testing.T) {
	db := memdb.NewTestDB(t)
	// SignersPacked all-zero -> no signer bits set -> "no signers" branch,
	// since AggregateSignature decodes (all-zero is rejected earlier though);
	// use a non-zero but still unparsable signature to hit "bad aggregate
	// signature bytes" deterministically regardless of bls's zero handling.
	seedEvidence(t, db, 2, &rawdb.ConsensusEvidence{
		View: 1, BlockHash: types.HexToHash("0x02"), SignerCount: 4, SignersPacked: []byte{0x00},
	})
	s := NewServer(db, consensusRestTestConfig())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n42/consensus/v1/block/2/verify", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["valid"] != false {
		t.Fatalf("body = %+v, want valid=false", body)
	}
}

func TestHandleVerifyNotFound(t *testing.T) {
	s := NewServer(memdb.NewTestDB(t), consensusRestTestConfig())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n42/consensus/v1/block/1/verify", nil))
	if rec.Code != 404 {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandlePool(t *testing.T) {
	s := NewServer(memdb.NewTestDB(t), consensusRestTestConfig())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n42/consensus/v1/pool/100", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["totalPoolSize"].(float64) != 8 {
		t.Fatalf("body = %+v", body)
	}
}

func TestHandlePoolBadID(t *testing.T) {
	s := NewServer(memdb.NewTestDB(t), consensusRestTestConfig())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n42/consensus/v1/pool/nope", nil))
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleValidatorWithoutSeed(t *testing.T) {
	s := NewServer(memdb.NewTestDB(t), Config{})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n42/consensus/v1/validator/0", nil))
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleValidatorInvalidIndex(t *testing.T) {
	s := NewServer(memdb.NewTestDB(t), consensusRestTestConfig())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n42/consensus/v1/validator/-1", nil))
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleValidatorOutOfRange(t *testing.T) {
	s := NewServer(memdb.NewTestDB(t), consensusRestTestConfig())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n42/consensus/v1/validator/1000", nil))
	if rec.Code != 404 {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandleValidatorFound(t *testing.T) {
	s := NewServer(memdb.NewTestDB(t), consensusRestTestConfig())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n42/consensus/v1/validator/0", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["pubkey"] == nil || body["address"] == nil {
		t.Fatalf("body = %+v", body)
	}
}

func TestHandleDutiesInvalidIndex(t *testing.T) {
	s := NewServer(memdb.NewTestDB(t), consensusRestTestConfig())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n42/consensus/v1/validator/-1/duties?from=0&to=1", nil))
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleDutiesMissingRange(t *testing.T) {
	s := NewServer(memdb.NewTestDB(t), consensusRestTestConfig())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n42/consensus/v1/validator/0/duties", nil))
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleDutiesToBeforeFrom(t *testing.T) {
	s := NewServer(memdb.NewTestDB(t), consensusRestTestConfig())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n42/consensus/v1/validator/0/duties?from=5&to=1", nil))
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleDutiesRangeTooLarge(t *testing.T) {
	s := NewServer(memdb.NewTestDB(t), consensusRestTestConfig())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n42/consensus/v1/validator/0/duties?from=0&to=100000", nil))
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleDutiesWalksRange(t *testing.T) {
	db := memdb.NewTestDB(t)
	seedEvidence(t, db, 1, &rawdb.ConsensusEvidence{View: 1, BlockHash: types.HexToHash("0x01"), SignerCount: 4, SignersPacked: []byte{0x0F}})
	seedEvidence(t, db, 2, &rawdb.ConsensusEvidence{View: 2, BlockHash: types.HexToHash("0x02"), SignerCount: 4, SignersPacked: []byte{0x00}})
	s := NewServer(db, consensusRestTestConfig())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n42/consensus/v1/validator/0/duties?from=0&to=5", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["duties"] == nil {
		t.Fatalf("body = %+v", body)
	}
}

func TestExplorerRouteMounted(t *testing.T) {
	s := NewServer(memdb.NewTestDB(t), consensusRestTestConfig())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n42/explorer", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}


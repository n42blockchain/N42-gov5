// Package consensusrest serves the N42 consensus evidence over a small
// Beacon-API-style REST surface, for block explorers and beaconcha.in-style
// tooling that prefer REST over JSON-RPC. It reads the ConsensusEvidence MDBX
// table directly and re-derives committee membership through internal/blspool,
// so it agrees exactly with the re-sealed chain's QCs.
//
// Routes (all GET):
//
//	/n42/consensus/v1/block/{id}/evidence       — the block's BLS QC + mobile votes
//	/n42/consensus/v1/block/{id}/committee       — committee members (pubkeys + who signed)
//	/n42/consensus/v1/block/{id}/verify          — cryptographically verify the QC
//	/n42/consensus/v1/pool/{id}                  — voter-pool sizing at a block
//	/n42/consensus/v1/validator/{index}          — a pool member's pubkey + address
//	/n42/consensus/v1/validator/{index}/duties   — ?from=&to= bounded duty scan
//	/health
//
// {id} is a block number, "latest"/"head" or "genesis".
package consensusrest

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal/blspool"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

const maxDutiesRange = 50000

// Config holds the pool parameters needed to resolve committees. Seed is
// optional: without it, pubkey routes are disabled but evidence/committee-index/
// pool routes still work.
type Config struct {
	Seed       [32]byte
	HasSeed    bool
	PoolSize   int
	Committee  int
	RampBlocks uint64
}

// Server serves the consensus REST API over a read-only chain DB.
type Server struct {
	db  kv.RoDB
	cfg Config

	poolOnce sync.Once
	poolPks  []common.PublicKey
	poolErr  error
}

// ConfigFromEnv reads the pool config from N42_BLS_POOL_SEED/SIZE/COMMITTEE/
// RAMP_BLOCKS. The seed is optional (without it, pubkey/verify routes are off).
func ConfigFromEnv() Config {
	cfg := Config{
		PoolSize:   envInt("N42_BLS_POOL_SIZE", 200000),
		Committee:  envInt("N42_BLS_COMMITTEE", 512),
		RampBlocks: uint64(envInt("N42_BLS_RAMP_BLOCKS", 1000000)),
	}
	if sh := strings.TrimPrefix(os.Getenv("N42_BLS_POOL_SEED"), "0x"); sh != "" {
		if b, err := hex.DecodeString(sh); err == nil && len(b) == 32 {
			copy(cfg.Seed[:], b)
			cfg.HasSeed = true
		}
	}
	return cfg
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// NewServer builds the REST server. PoolSize/Committee/RampBlocks default to
// 200000/512/1000000 when zero.
func NewServer(db kv.RoDB, cfg Config) *Server {
	if cfg.PoolSize == 0 {
		cfg.PoolSize = 200000
	}
	if cfg.Committee == 0 {
		cfg.Committee = 512
	}
	if cfg.RampBlocks == 0 {
		cfg.RampBlocks = 1000000
	}
	return &Server{db: db, cfg: cfg}
}

// Handler returns the HTTP mux with all routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /n42/consensus/v1/block/{id}/evidence", s.handleEvidence)
	mux.HandleFunc("GET /n42/consensus/v1/block/{id}/committee", s.handleCommittee)
	mux.HandleFunc("GET /n42/consensus/v1/block/{id}/verify", s.handleVerify)
	mux.HandleFunc("GET /n42/consensus/v1/pool/{id}", s.handlePool)
	mux.HandleFunc("GET /n42/consensus/v1/validator/{index}", s.handleValidator)
	mux.HandleFunc("GET /n42/consensus/v1/validator/{index}/duties", s.handleDuties)
	mux.Handle("GET /n42/explorer", ExplorerHandler())
	return mux
}

func (s *Server) handleEvidence(w http.ResponseWriter, r *http.Request) {
	num, err := s.resolveID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	ce, err := s.readCE(r.Context(), num)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if ce == nil {
		writeErr(w, 404, fmt.Errorf("no evidence for block %d", num))
		return
	}
	out := map[string]any{
		"blockNumber":        num,
		"view":               ce.View,
		"blockHash":          ce.BlockHash.Hex(),
		"aggregateSignature": hexStr(ce.AggregateSignature[:]),
		"signerCount":        ce.SignerCount,
		"signers":            hexStr(ce.SignersPacked),
		"hasMobile":          ce.HasMobile,
	}
	if ce.HasMobile {
		out["mobileReceiptsRoot"] = ce.MobReceiptsRoot.Hex()
		out["mobileAggregateSignature"] = hexStr(ce.MobAggSignature[:])
		out["mobileParticipantCount"] = ce.MobParticipantCount
		out["mobileParticipants"] = hexStr(ce.MobParticipantsPacked)
		out["mobileCreatedAtMs"] = ce.MobCreatedAtMs
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleCommittee(w http.ResponseWriter, r *http.Request) {
	num, err := s.resolveID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	ce, err := s.readCE(r.Context(), num)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if ce == nil {
		writeErr(w, 404, fmt.Errorf("no evidence for block %d", num))
		return
	}
	if err := s.ensurePool(); err != nil {
		writeErr(w, 400, err)
		return
	}
	active := blspool.ActivePool(ce.View, s.cfg.PoolSize, s.cfg.Committee, s.cfg.RampBlocks)
	members := blspool.Committee(ce.View, ce.BlockHash, active, s.cfg.Committee)
	memOut := make([]map[string]any, 0, len(members))
	var signed int
	for i, idx := range members {
		isSigned := i < len(ce.SignersPacked)*8 && ce.SignersPacked[i/8]&(1<<uint(i%8)) != 0
		if isSigned {
			signed++
		}
		var pk string
		if idx >= 0 && idx < len(s.poolPks) {
			pk = hexStr(s.poolPks[idx].Marshal())
		}
		memOut = append(memOut, map[string]any{"index": idx, "pubkey": pk, "signed": isSigned})
	}
	writeJSON(w, 200, map[string]any{
		"blockNumber": num, "view": ce.View, "blockHash": ce.BlockHash.Hex(),
		"activePoolSize": active, "committeeSize": len(members), "signedCount": signed,
		"members": memOut,
	})
}

// handleVerify cryptographically verifies a block's QC: it re-derives the
// committee, aggregates the public keys of the signers, and checks the
// aggregate signature against SigningMessage(view, blockHash) — exactly as the
// live HotStuff engine does. This is the e2e proof that the re-sealed chain's
// consensus is valid.
func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	num, err := s.resolveID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	ce, err := s.readCE(r.Context(), num)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if ce == nil {
		writeErr(w, 404, fmt.Errorf("no evidence for block %d", num))
		return
	}
	if err := s.ensurePool(); err != nil {
		writeErr(w, 400, err)
		return
	}
	active := blspool.ActivePool(ce.View, s.cfg.PoolSize, s.cfg.Committee, s.cfg.RampBlocks)
	members := blspool.Committee(ce.View, ce.BlockHash, active, s.cfg.Committee)
	pubs := make([]common.PublicKey, 0, len(members))
	for i := range members {
		if i < len(ce.SignersPacked)*8 && ce.SignersPacked[i/8]&(1<<uint(i%8)) != 0 {
			if members[i] >= 0 && members[i] < len(s.poolPks) {
				pubs = append(pubs, s.poolPks[members[i]])
			}
		}
	}
	valid := false
	reason := ""
	if aggSig, serr := bls.SignatureFromBytes(ce.AggregateSignature[:]); serr != nil {
		reason = "bad aggregate signature bytes"
	} else if len(pubs) == 0 {
		reason = "no signers"
	} else {
		aggPK := bls.AggregateMultiplePubkeys(pubs)
		msg := hotstuff.SigningMessage(ce.View, ce.BlockHash)
		valid = aggSig.Verify(aggPK, msg)
		if !valid {
			reason = "aggregate signature does not verify"
		}
	}
	writeJSON(w, 200, map[string]any{
		"blockNumber": num, "view": ce.View, "blockHash": ce.BlockHash.Hex(),
		"signerCount": len(pubs), "committeeSize": len(members), "valid": valid, "reason": reason,
	})
}

func (s *Server) handlePool(w http.ResponseWriter, r *http.Request) {
	num, err := s.resolveID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	active := blspool.ActivePool(num, s.cfg.PoolSize, s.cfg.Committee, s.cfg.RampBlocks)
	writeJSON(w, 200, map[string]any{
		"blockNumber": num, "activePoolSize": active, "committeeSize": s.cfg.Committee,
		"totalPoolSize": s.cfg.PoolSize, "rampBlocks": s.cfg.RampBlocks, "fullyRamped": active >= s.cfg.PoolSize,
	})
}

func (s *Server) handleValidator(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || idx < 0 {
		writeErr(w, 400, fmt.Errorf("invalid index"))
		return
	}
	if err := s.ensurePool(); err != nil {
		writeErr(w, 400, err)
		return
	}
	if idx >= len(s.poolPks) {
		writeErr(w, 404, fmt.Errorf("index %d out of range [0,%d)", idx, len(s.poolPks)))
		return
	}
	pub := s.poolPks[idx].Marshal()
	addr := crypto.Keccak256(pub)[12:]
	writeJSON(w, 200, map[string]any{
		"index": idx, "pubkey": hexStr(pub), "address": "0x" + hex.EncodeToString(addr),
	})
}

func (s *Server) handleDuties(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || idx < 0 {
		writeErr(w, 400, fmt.Errorf("invalid index"))
		return
	}
	from, ferr := strconv.ParseUint(r.URL.Query().Get("from"), 10, 64)
	to, terr := strconv.ParseUint(r.URL.Query().Get("to"), 10, 64)
	if ferr != nil || terr != nil || to < from {
		writeErr(w, 400, fmt.Errorf("from/to query params required (to >= from)"))
		return
	}
	if to-from+1 > maxDutiesRange {
		writeErr(w, 400, fmt.Errorf("range too large (max %d blocks)", maxDutiesRange))
		return
	}
	tx, err := s.db.BeginRo(r.Context())
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	defer tx.Rollback()
	duties := make([]map[string]any, 0, 64)
	for num := from; num <= to; num++ {
		ce, err := rawdb.ReadConsensusEvidence(tx, num)
		if err != nil || ce == nil {
			continue
		}
		active := blspool.ActivePool(ce.View, s.cfg.PoolSize, s.cfg.Committee, s.cfg.RampBlocks)
		members := blspool.Committee(ce.View, ce.BlockHash, active, s.cfg.Committee)
		for pos, m := range members {
			if m == idx {
				signed := pos < len(ce.SignersPacked)*8 && ce.SignersPacked[pos/8]&(1<<uint(pos%8)) != 0
				duties = append(duties, map[string]any{"blockNumber": num, "view": ce.View, "position": pos, "signed": signed})
				break
			}
		}
	}
	writeJSON(w, 200, map[string]any{"validator": idx, "from": from, "to": to, "duties": duties})
}

// resolveID maps a block id ("latest"/"head"/"genesis"/number) to a block number.
func (s *Server) resolveID(ctx context.Context, id string) (uint64, error) {
	switch id {
	case "latest", "head":
		tx, err := s.db.BeginRo(ctx)
		if err != nil {
			return 0, err
		}
		defer tx.Rollback()
		n := rawdb.ReadCurrentFullBlockNumber(tx)
		if n == nil {
			return 0, fmt.Errorf("head unavailable")
		}
		return *n, nil
	case "genesis":
		return 0, nil
	default:
		return strconv.ParseUint(id, 10, 64)
	}
}

func (s *Server) readCE(ctx context.Context, num uint64) (*rawdb.ConsensusEvidence, error) {
	tx, err := s.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return rawdb.ReadConsensusEvidence(tx, num)
}

func (s *Server) ensurePool() error {
	s.poolOnce.Do(func() {
		if !s.cfg.HasSeed {
			s.poolErr = fmt.Errorf("public-key resolution disabled: pool seed not configured")
			return
		}
		_, pks, err := blspool.DeriveKeys(s.cfg.Seed, s.cfg.PoolSize, false)
		if err != nil {
			s.poolErr = err
			return
		}
		s.poolPks = pks
	})
	return s.poolErr
}

func hexStr(b []byte) string { return "0x" + hex.EncodeToString(b) }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

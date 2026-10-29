// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// eest_blockchain_test.go — runs the Ethereum execution-spec-tests (EEST)
// blockchain_tests and blockchain_tests_engine fixtures through the eth-el
// block import paths.
//
// Opt-in: set N42_EEST_DIR to an extracted EEST "fixtures" directory; both
// tests skip when it is unset (see tests/eest for the other env knobs).
//
// The node under test is wired exactly as internal/ethel/engineapi.Service
// wires eth-el: api.NewAPI with no BlockChain, the Engine API namespaces
// from EngineAPIs, an EngineStateAdapter over an MDBX chaindata with the
// eth-el table config, and ethel's replay consensus engine. The genesis is
// written as `n42 init` does and its commitment seeded as the eth-el node
// does on start.
//
//   - blockchain_tests: each block's RLP is decoded with ethel.DecodeRawBlock
//     and imported with EngineAPIV1.ImportSyncedBlock, the path the devp2p
//     downloader uses for blocks it fetches from peers (header checks against
//     the parent, execution, state-root and receipt checks), then made head
//     with forkchoiceUpdated.
//   - blockchain_tests_engine: each engineNewPayloads entry is sent to the
//     engine_newPayloadVN method the fixture names, followed by the named
//     engine_forkchoiceUpdatedVN for valid payloads.
//
// A block or payload with expectException / validationError must be
// rejected; every other one must be VALID. Finally the head must be the
// fixture's lastblockhash and the listed postState accounts must match what
// the chaindata holds.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	internalcore "github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
	"github.com/n42blockchain/N42/tests/eest"
)

type eestGenesisHeader struct {
	Hash          types.Hash    `json:"hash"`
	Coinbase      types.Address `json:"coinbase"`
	StateRoot     types.Hash    `json:"stateRoot"`
	Difficulty    *eest.Big     `json:"difficulty"`
	Number        eest.Uint64   `json:"number"`
	GasLimit      eest.Uint64   `json:"gasLimit"`
	GasUsed       eest.Uint64   `json:"gasUsed"`
	Timestamp     eest.Uint64   `json:"timestamp"`
	ExtraData     hexutil.Bytes `json:"extraData"`
	MixHash       types.Hash    `json:"mixHash"`
	Nonce         string        `json:"nonce"`
	BaseFee       *eest.Big     `json:"baseFeePerGas"`
	BlobGasUsed   *eest.Uint64  `json:"blobGasUsed"`
	ExcessBlobGas *eest.Uint64  `json:"excessBlobGas"`
}

type eestAccountJSON struct {
	Balance string            `json:"balance"`
	Code    hexutil.Bytes     `json:"code"`
	Nonce   eest.Uint64       `json:"nonce"`
	Storage map[string]string `json:"storage"`
}

type eestBlockJSON struct {
	RLP             hexutil.Bytes `json:"rlp"`
	ExpectException string        `json:"expectException"`
}

type eestEngineCallJSON struct {
	Params                   []json.RawMessage `json:"params"`
	NewPayloadVersion        string            `json:"newPayloadVersion"`
	ForkchoiceUpdatedVersion string            `json:"forkchoiceUpdatedVersion"`
	ValidationError          string            `json:"validationError"`
	ErrorCode                json.RawMessage   `json:"errorCode"`
}

type eestBlockchainFixture struct {
	Network           string                            `json:"network"`
	Config            *eest.FixtureConfig               `json:"config"`
	GenesisHeader     eestGenesisHeader                 `json:"genesisBlockHeader"`
	Pre               map[types.Address]eestAccountJSON `json:"pre"`
	PostState         map[types.Address]eestAccountJSON `json:"postState"`
	PostStateHash     *types.Hash                       `json:"postStateHash"`
	LastBlockHash     types.Hash                        `json:"lastblockhash"`
	Blocks            []eestBlockJSON                   `json:"blocks"`
	EngineNewPayloads []eestEngineCallJSON              `json:"engineNewPayloads"`
}

func TestEESTBlockchainTests(t *testing.T) {
	runEESTBlockchainSuite(t, "blockchain_tests", "blockchain")
}

func TestEESTBlockchainEngineTests(t *testing.T) {
	runEESTBlockchainSuite(t, "blockchain_tests_engine", "engine")
}

// eestHarnessSkip marks a fixture the harness cannot run.
type eestHarnessSkip struct{ reason string }

func (s eestHarnessSkip) Error() string { return "skip: " + s.reason }

func runEESTBlockchainSuite(t *testing.T, suite, label string) {
	dir := eest.Dir()
	if dir == "" {
		t.Skipf("set %s to an extracted EEST fixtures directory to run", eest.EnvDir)
	}
	root, files, err := eest.FixtureFiles(dir, suite)
	if err != nil {
		t.Fatalf("list %s: %v", suite, err)
	}
	if len(files) == 0 {
		t.Fatalf("no %s fixtures under %s", suite, root)
	}
	forks := eest.SelectedForks()
	report, err := eest.OpenReporter()
	if err != nil {
		t.Fatalf("open report: %v", err)
	}
	defer report.Close()

	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = ethel.ChainTableCfg(modules.N42TableCfg)
	defer func() { kv.ChaindataTablesCfg = prevTables }()
	tmp := t.TempDir()

	type job struct {
		rel, name string
		fx        eestBlockchainFixture
	}
	var (
		mu               sync.Mutex
		pass, fail, skip int
		wg               sync.WaitGroup
	)
	jobs := make(chan job, 64)
	workers := eest.Workers()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		dbDir := filepath.Join(tmp, fmt.Sprintf("db%d", w))
		go func() {
			defer wg.Done()
			for j := range jobs {
				err := runEESTBlockchainFixture(dbDir, &j.fx, label == "engine")
				var skipErr eestHarnessSkip
				mu.Lock()
				switch {
				case errors.As(err, &skipErr):
					skip++
					report.Record(label, j.fx.Network, j.rel, j.name, "skip", err.Error())
				case err != nil:
					fail++
					report.Record(label, j.fx.Network, j.rel, j.name, "fail", err.Error())
					t.Errorf("%s %s [%s]: %v", j.rel, j.name, j.fx.Network, err)
				default:
					pass++
					report.Record(label, j.fx.Network, j.rel, j.name, "pass", "")
				}
				mu.Unlock()
			}
		}()
	}
	for _, rel := range files {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("read %s: %v", rel, err)
			continue
		}
		var fixtures map[string]eestBlockchainFixture
		if err := json.Unmarshal(raw, &fixtures); err != nil {
			t.Errorf("decode %s: %v", rel, err)
			mu.Lock()
			fail++
			mu.Unlock()
			continue
		}
		for name, fx := range fixtures {
			if forks[fx.Network] {
				jobs <- job{rel: rel, name: name, fx: fx}
			}
		}
	}
	close(jobs)
	wg.Wait()
	t.Logf("EEST %s: %d passed, %d failed, %d skipped", suite, pass, fail, skip)
}

// eestNode is one eth-el Engine API stack over a fresh chaindata.
type eestNode struct {
	db   kv.RwDB
	cfg  *params.ChainConfig
	v1   *EngineAPIV1
	blob *EngineAPIBlob
	v4   *EngineAPIv4
}

func newEESTNode(dbDir string, fx *eestBlockchainFixture) (*eestNode, error) {
	cfg, err := eest.ChainConfig(fx.Network, fx.Config)
	if err != nil {
		return nil, eestHarnessSkip{err.Error()}
	}
	if !cfg.IsLondon(0) || cfg.TerminalTotalDifficulty == nil {
		return nil, eestHarnessSkip{"pre-merge network: eth-el imports post-merge blocks only"}
	}
	genesis, err := eestGenesis(cfg, fx)
	if err != nil {
		return nil, err
	}
	if err := os.RemoveAll(dbDir); err != nil {
		return nil, err
	}
	db := memdb.New(dbDir)
	var genesisBlock *block.Block
	err = db.Update(context.Background(), func(tx kv.RwTx) error {
		blk, _, err := (&internalcore.GenesisBlock{GenesisConfig: genesis}).Write(tx)
		if err != nil {
			return err
		}
		genesisBlock = blk
		// Seed the hashed leaves as the eth-el node does on start for a
		// custom genesis (Node.initializeCustomGenesisCommitment).
		return ethel.RebuildHashedState(tx)
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("write genesis: %w", err)
	}
	genesisHash := ethCompatibleBlockHash(genesisBlock, cfg)
	if genesisHash != fx.GenesisHeader.Hash {
		db.Close()
		return nil, fmt.Errorf("genesis hash mismatch: got %s, want %s", genesisHash.Hex(), fx.GenesisHeader.Hash.Hex())
	}

	engine := ethel.NewEthReplayEngine(cfg)
	core := NewAPI(nil, db, engine, nil, nil, cfg)
	adapter := NewEngineStateAdapter(db, nil, cfg, engine).WithHeadMarker(true)
	n := &eestNode{db: db, cfg: cfg}
	for _, a := range EngineAPIs(core) {
		switch svc := a.Service.(type) {
		case *EngineAPIV1:
			svc.SetStateAdapter(adapter)
			n.v1 = svc
		case *EngineAPIBlob:
			svc.SetStateAdapter(adapter)
			n.blob = svc
		case *EngineAPIv4:
			svc.SetStateAdapter(adapter)
			n.v4 = svc
		}
	}
	if n.v1 == nil || n.blob == nil || n.v4 == nil {
		db.Close()
		return nil, errors.New("EngineAPIs returned an incomplete set")
	}
	if err := n.forkchoice("1", genesisHash); err != nil {
		db.Close()
		return nil, fmt.Errorf("genesis forkchoice: %w", err)
	}
	return n, nil
}

func eestGenesis(cfg *params.ChainConfig, fx *eestBlockchainFixture) (*conf.Genesis, error) {
	h := fx.GenesisHeader
	alloc, err := eestAlloc(fx.Pre)
	if err != nil {
		return nil, err
	}
	g := &conf.Genesis{
		Config:     cfg,
		Alloc:      alloc,
		Number:     uint64(h.Number),
		GasLimit:   uint64(h.GasLimit),
		GasUsed:    uint64(h.GasUsed),
		Timestamp:  uint64(h.Timestamp),
		ExtraData:  h.ExtraData,
		Coinbase:   h.Coinbase,
		Mixhash:    h.MixHash,
		Difficulty: uint256.NewInt(0),
	}
	if h.Difficulty != nil {
		g.Difficulty, _ = uint256.FromBig(h.Difficulty.ToInt())
	}
	if h.Nonce != "" {
		nonce, err := eest.ParseBig(h.Nonce)
		if err != nil {
			return nil, err
		}
		g.Nonce = nonce.Uint64()
	}
	if h.BaseFee != nil {
		g.BaseFee, _ = uint256.FromBig(h.BaseFee.ToInt())
	}
	if h.BlobGasUsed != nil {
		g.BlobGasUsed = uint64(*h.BlobGasUsed)
	}
	if h.ExcessBlobGas != nil {
		g.ExcessBlobGas = uint64(*h.ExcessBlobGas)
	}
	return g, nil
}

func eestAlloc(pre map[types.Address]eestAccountJSON) (conf.GenesisAlloc, error) {
	alloc := make(conf.GenesisAlloc, len(pre))
	for addr, acc := range pre {
		ga := conf.GenesisAccount{Balance: acc.Balance, Code: acc.Code, Nonce: uint64(acc.Nonce)}
		if len(acc.Storage) > 0 {
			ga.Storage = make(map[types.Hash]types.Hash, len(acc.Storage))
			for k, v := range acc.Storage {
				key, err := eest.ParseBig(k)
				if err != nil {
					return nil, err
				}
				val, err := eest.ParseBig(v)
				if err != nil {
					return nil, err
				}
				ga.Storage[types.BigToHash(key)] = types.BigToHash(val)
			}
		}
		alloc[addr] = ga
	}
	return alloc, nil
}

func runEESTBlockchainFixture(dbDir string, fx *eestBlockchainFixture, engineMode bool) error {
	n, err := newEESTNode(dbDir, fx)
	if err != nil {
		return err
	}
	defer n.db.Close()

	if engineMode {
		if len(fx.EngineNewPayloads) == 0 {
			return eestHarnessSkip{"no engineNewPayloads"}
		}
		for i := range fx.EngineNewPayloads {
			if err := n.newPayload(&fx.EngineNewPayloads[i]); err != nil {
				return fmt.Errorf("payload %d/%d: %w", i+1, len(fx.EngineNewPayloads), err)
			}
		}
	} else {
		for i := range fx.Blocks {
			if err := n.importBlock(&fx.Blocks[i]); err != nil {
				return fmt.Errorf("block %d/%d: %w", i+1, len(fx.Blocks), err)
			}
		}
	}

	if head := n.v1.currentHeadHash(); head != fx.LastBlockHash {
		return fmt.Errorf("head mismatch: got %s, want lastblockhash %s", head.Hex(), fx.LastBlockHash.Hex())
	}
	return n.checkPostState(fx)
}

// importBlock imports one fixture block through the devp2p wire-block path.
func (n *eestNode) importBlock(b *eestBlockJSON) error {
	blk, withdrawals, err := ethel.DecodeRawBlock(b.RLP)
	if err != nil {
		if b.ExpectException != "" {
			return nil
		}
		return fmt.Errorf("decode: %w", err)
	}
	status, err := n.v1.ImportSyncedBlock(context.Background(), blk, eestWithdrawals(withdrawals))
	if err != nil {
		if b.ExpectException != "" {
			return nil
		}
		return fmt.Errorf("import: %w", err)
	}
	if b.ExpectException != "" {
		if status.Status == PayloadStatusValid {
			return fmt.Errorf("expected exception %s, block was accepted", b.ExpectException)
		}
		return nil
	}
	if status.Status != PayloadStatusValid {
		return fmt.Errorf("block %d rejected: status=%s err=%s", blk.Number64().Uint64(), status.Status, eestValidationError(status))
	}
	return n.forkchoice("3", ethCompatibleBlockHash(blk, n.cfg))
}

func eestWithdrawals(ws []*ethel.Withdrawal) []*Withdrawal {
	if ws == nil {
		return nil
	}
	out := make([]*Withdrawal, 0, len(ws))
	for _, w := range ws {
		out = append(out, &Withdrawal{
			Index:          hexutil.Uint64(w.Index),
			ValidatorIndex: hexutil.Uint64(w.Validator),
			Address:        w.Address,
			Amount:         hexutil.Uint64(w.Amount),
		})
	}
	return out
}

func eestValidationError(s *PayloadStatusV1) string {
	if s == nil || s.ValidationError == nil {
		return "<nil>"
	}
	return *s.ValidationError
}

// newPayload sends one engineNewPayloads entry to the named method.
func (n *eestNode) newPayload(call *eestEngineCallJSON) error {
	ctx := context.Background()
	expectInvalid := call.ValidationError != ""
	expectRPCError := len(call.ErrorCode) > 0 && string(call.ErrorCode) != "null"
	decode := func(i int, dst any) error {
		if i >= len(call.Params) {
			return fmt.Errorf("newPayloadV%s: missing param %d", call.NewPayloadVersion, i)
		}
		return json.Unmarshal(call.Params[i], dst)
	}

	var (
		status    *PayloadStatusV1
		err       error
		blockHash types.Hash
	)
	switch call.NewPayloadVersion {
	case "1":
		var p ExecutionPayloadV1
		if err := decode(0, &p); err != nil {
			return err
		}
		blockHash = p.BlockHash
		status, err = n.v1.NewPayloadV1(ctx, &p)
	case "2":
		var p ExecutionPayloadV2
		if err := decode(0, &p); err != nil {
			return err
		}
		blockHash = p.BlockHash
		status, err = n.v1.NewPayloadV2(ctx, &p)
	case "3", "4":
		var (
			hashes     []types.Hash
			beaconRoot types.Hash
		)
		if err := decode(1, &hashes); err != nil {
			return err
		}
		if err := decode(2, &beaconRoot); err != nil {
			return err
		}
		if call.NewPayloadVersion == "3" {
			var p ExecutionPayloadV3
			if err := decode(0, &p); err != nil {
				return err
			}
			blockHash = p.BlockHash
			status, err = n.blob.NewPayloadV3(ctx, &p, hashes, &beaconRoot)
		} else {
			var (
				p        ExecutionPayloadV4
				requests []hexutil.Bytes
			)
			if err := decode(0, &p); err != nil {
				return err
			}
			if err := decode(3, &requests); err != nil {
				return err
			}
			blockHash = p.BlockHash
			status, err = n.v4.NewPayloadV4(ctx, &p, hashes, &beaconRoot, requests)
		}
	default:
		return eestHarnessSkip{"unsupported newPayloadVersion " + call.NewPayloadVersion}
	}

	switch {
	case expectRPCError:
		if err == nil {
			return fmt.Errorf("expected JSON-RPC error %s, got status %s", string(call.ErrorCode), statusString(status))
		}
		return nil
	case err != nil:
		if expectInvalid {
			return fmt.Errorf("expected INVALID (%s), got JSON-RPC error: %w", call.ValidationError, err)
		}
		return fmt.Errorf("newPayloadV%s: %w", call.NewPayloadVersion, err)
	case status == nil:
		return fmt.Errorf("newPayloadV%s returned nil status", call.NewPayloadVersion)
	case expectInvalid:
		if status.Status != PayloadStatusInvalid {
			return fmt.Errorf("expected INVALID (%s), got %s", call.ValidationError, statusString(status))
		}
		return nil
	case status.Status != PayloadStatusValid:
		return fmt.Errorf("payload rejected: %s", statusString(status))
	}
	return n.forkchoice(call.ForkchoiceUpdatedVersion, blockHash)
}

func statusString(s *PayloadStatusV1) string {
	if s == nil {
		return "<nil>"
	}
	return s.Status + " (" + eestValidationError(s) + ")"
}

// forkchoice makes hash the head, safe and finalized block.
func (n *eestNode) forkchoice(version string, hash types.Hash) error {
	ctx := context.Background()
	state := &ForkchoiceStateV1{HeadBlockHash: hash, SafeBlockHash: hash, FinalizedBlockHash: hash}
	var (
		resp *ForkchoiceUpdatedResponseV3
		err  error
	)
	switch version {
	case "", "1":
		resp, err = n.v1.ForkchoiceUpdatedV1(ctx, state, nil)
	case "2":
		resp, err = n.v1.ForkchoiceUpdatedV2(ctx, state, nil)
	case "3":
		resp, err = n.blob.ForkchoiceUpdatedV3(ctx, state, nil)
	case "4":
		resp, err = n.v4.ForkchoiceUpdatedV4(ctx, state, nil)
	default:
		return eestHarnessSkip{"unsupported forkchoiceUpdatedVersion " + version}
	}
	if err != nil {
		return fmt.Errorf("forkchoiceUpdatedV%s: %w", version, err)
	}
	if resp == nil || resp.PayloadStatus.Status != PayloadStatusValid {
		var st *PayloadStatusV1
		if resp != nil {
			st = &resp.PayloadStatus
		}
		return fmt.Errorf("forkchoiceUpdatedV%s: %s", version, statusString(st))
	}
	return nil
}

// checkPostState compares the fixture's postState accounts with the
// chaindata's plain state.
func (n *eestNode) checkPostState(fx *eestBlockchainFixture) error {
	if len(fx.PostState) == 0 {
		return nil
	}
	tx, err := n.db.BeginRo(context.Background())
	if err != nil {
		return err
	}
	defer tx.Rollback()
	reader := state.NewPlainStateReader(tx)
	emptyCodeHash := crypto.Keccak256Hash(nil)
	var problems []string
	for addr, want := range fx.PostState {
		acc, err := reader.ReadAccountData(addr)
		if err != nil {
			return err
		}
		wantBalance, err := eest.ParseBig(want.Balance)
		if err != nil {
			return err
		}
		if acc == nil {
			if wantBalance.Sign() != 0 || want.Nonce != 0 || len(want.Code) > 0 || eestHasNonZero(want.Storage) {
				problems = append(problems, fmt.Sprintf("%s missing", addr.Hex()))
			}
			continue
		}
		if acc.Balance.ToBig().Cmp(wantBalance) != 0 {
			problems = append(problems, fmt.Sprintf("%s balance %s want %s", addr.Hex(), acc.Balance.ToBig(), wantBalance))
		}
		if acc.Nonce != uint64(want.Nonce) {
			problems = append(problems, fmt.Sprintf("%s nonce %d want %d", addr.Hex(), acc.Nonce, uint64(want.Nonce)))
		}
		wantCodeHash := emptyCodeHash
		if len(want.Code) > 0 {
			wantCodeHash = crypto.Keccak256Hash(want.Code)
		}
		gotCodeHash := acc.CodeHash
		if gotCodeHash == (types.Hash{}) {
			gotCodeHash = emptyCodeHash
		}
		if gotCodeHash != wantCodeHash {
			problems = append(problems, fmt.Sprintf("%s codeHash %s want %s", addr.Hex(), gotCodeHash.Hex(), wantCodeHash.Hex()))
		}
		for k, v := range want.Storage {
			key, err := eest.ParseBig(k)
			if err != nil {
				return err
			}
			wantVal, err := eest.ParseBig(v)
			if err != nil {
				return err
			}
			slot := types.BigToHash(key)
			got, err := reader.ReadAccountStorage(addr, &slot)
			if err != nil {
				return err
			}
			if new(uint256.Int).SetBytes(got).ToBig().Cmp(wantVal) != 0 {
				problems = append(problems, fmt.Sprintf("%s slot %s = %x want %s", addr.Hex(), slot.Hex(), got, wantVal.Text(16)))
			}
		}
	}
	if len(problems) > 0 {
		if len(problems) > 4 {
			problems = append(problems[:4], fmt.Sprintf("... %d more", len(problems)-4))
		}
		return fmt.Errorf("post state mismatch: %s", strings.Join(problems, "; "))
	}
	return nil
}

func eestHasNonZero(storage map[string]string) bool {
	for _, v := range storage {
		if b, err := eest.ParseBig(v); err == nil && b.Sign() != 0 {
			return true
		}
	}
	return false
}

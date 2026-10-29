// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// eest_state_test.go — runs the Ethereum execution-spec-tests (EEST)
// state_tests fixtures against N42's eth-el transaction execution.
//
// Opt-in: set N42_EEST_DIR to an extracted EEST "fixtures" directory; the
// test skips when it is unset. Each post entry is executed the way the
// eth-el block processor executes a transaction (signed tx bytes decoded
// with the Ethereum wire decoder, ethel's replay engine, ApplyTransaction),
// then the post-state MPT root and the logs hash are compared, as
// go-ethereum's tests/state_test_util.go does. For an entry that carries
// expectException the transaction must be rejected and the state must stay
// at the pre-state root.

package tests

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
	"github.com/n42blockchain/N42/tests/eest"
)

type eestStateFixture struct {
	Env    eestStateEnv                  `json:"env"`
	Pre    map[types.Address]eestAccount `json:"pre"`
	Post   map[string][]eestStatePost    `json:"post"`
	Config *eest.FixtureConfig           `json:"config"`
}

type eestStateEnv struct {
	Coinbase      types.Address `json:"currentCoinbase"`
	GasLimit      eest.Uint64   `json:"currentGasLimit"`
	Number        eest.Uint64   `json:"currentNumber"`
	Timestamp     eest.Uint64   `json:"currentTimestamp"`
	Difficulty    *eest.Big     `json:"currentDifficulty"`
	Random        *eest.Big     `json:"currentRandom"`
	BaseFee       *eest.Big     `json:"currentBaseFee"`
	ExcessBlobGas *eest.Uint64  `json:"currentExcessBlobGas"`
	SlotNumber    *eest.Uint64  `json:"slotNumber"`
}

type eestAccount struct {
	Nonce   eest.Uint64       `json:"nonce"`
	Balance *eest.Big         `json:"balance"`
	Code    hexutil.Bytes     `json:"code"`
	Storage map[string]string `json:"storage"`
}

type eestStatePost struct {
	Hash            types.Hash    `json:"hash"`
	Logs            types.Hash    `json:"logs"`
	TxBytes         hexutil.Bytes `json:"txbytes"`
	ExpectException string        `json:"expectException"`
	Indexes         struct {
		Data  int `json:"data"`
		Gas   int `json:"gas"`
		Value int `json:"value"`
	} `json:"indexes"`
}

func TestEESTStateTests(t *testing.T) {
	dir := eest.Dir()
	if dir == "" {
		t.Skipf("set %s to an extracted EEST fixtures directory to run", eest.EnvDir)
	}
	root, files, err := eest.FixtureFiles(dir, "state_tests")
	if err != nil {
		t.Fatalf("list state_tests: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("no state_tests fixtures under %s", root)
	}
	forks := eest.SelectedForks()
	report, err := eest.OpenReporter()
	if err != nil {
		t.Fatalf("open report: %v", err)
	}
	defer report.Close()

	var pass, fail, skip int
	for _, rel := range files {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		var fixtures map[string]eestStateFixture
		if err := json.Unmarshal(raw, &fixtures); err != nil {
			t.Errorf("decode %s: %v", rel, err)
			fail++
			continue
		}
		for name, fx := range fixtures {
			for network, posts := range fx.Post {
				if !forks[network] {
					continue
				}
				for i := range posts {
					post := &posts[i]
					id := fmt.Sprintf("%s/%d", name, i)
					err := eest.Guard(func() error { return runEESTStatePost(&fx, network, post) })
					var skipErr eestSkip
					switch {
					case errors.As(err, &skipErr):
						skip++
						report.Record("state", network, rel, id, "skip", err.Error())
					case err != nil:
						fail++
						report.Record("state", network, rel, id, "fail", err.Error())
						t.Errorf("%s %s [%s]: %v", rel, id, network, err)
					default:
						pass++
						report.Record("state", network, rel, id, "pass", "")
					}
				}
			}
		}
	}
	t.Logf("EEST state tests: %d passed, %d failed, %d skipped", pass, fail, skip)
}

// eestSkip marks a case the harness cannot run (not a client failure).
type eestSkip struct{ reason string }

func (s eestSkip) Error() string { return "skip: " + s.reason }

func runEESTStatePost(fx *eestStateFixture, network string, post *eestStatePost) error {
	cfg, err := eest.ChainConfig(network, fx.Config)
	if err != nil {
		return eestSkip{err.Error()}
	}
	if len(post.TxBytes) == 0 {
		return eestSkip{"post entry has no txbytes"}
	}

	mem := NewMemoryState()
	writeEESTPreState(mem, fx.Pre)
	ibs := state.New(mem)

	header := eestStateHeader(cfg, &fx.Env)
	number, timestamp := uint64(fx.Env.Number), uint64(fx.Env.Timestamp)
	rules := cfg.RulesWithTimestamp(number, timestamp)

	txErr := applyEESTStateTx(cfg, header, ibs, mem, post.TxBytes)
	if txErr != nil {
		if post.ExpectException == "" {
			return fmt.Errorf("unexpected error: %w", txErr)
		}
		// Rejected as expected: nothing was finalized, so the state must
		// still be the pre-state, which the fixture's post hash records.
		if got := eestMemoryStateRoot(mem); got != post.Hash {
			return fmt.Errorf("rejected tx (%v) but post-state root %s != %s", txErr, got.Hex(), post.Hash.Hex())
		}
		return nil
	}
	if post.ExpectException != "" {
		return fmt.Errorf("expected exception %s, tx was accepted", post.ExpectException)
	}
	logs := ibs.Logs()

	// Touch the coinbase with a zero reward after execution, as geth does, so
	// an empty coinbase is removed under EIP-158 even when it received no fee.
	ibs.AddBalance(header.Coinbase, new(uint256.Int))
	if err := ibs.FinalizeTx(rules, mem); err != nil {
		return fmt.Errorf("finalize: %w", err)
	}
	if err := ibs.CommitBlock(rules, mem); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	if got := eestMemoryStateRoot(mem); got != post.Hash {
		return fmt.Errorf("post state root mismatch: got %s, want %s", got.Hex(), post.Hash.Hex())
	}
	if got := eestLogsHash(logs); got != post.Logs {
		return fmt.Errorf("logs hash mismatch: got %s, want %s", got.Hex(), post.Logs.Hex())
	}
	return nil
}

// applyEESTStateTx decodes the signed tx bytes and runs them through the
// eth-el per-transaction path (ApplyTransaction with the replay engine).
func applyEESTStateTx(cfg *params.ChainConfig, header *block.Header, ibs *state.IntraBlockState, mem *MemoryState, txBytes []byte) error {
	tx, err := transaction.DecodeEthereumTransaction(txBytes)
	if err != nil {
		return fmt.Errorf("decode tx: %w", err)
	}
	// Block-level blob limit: state tests have no block body, so check the
	// per-block maximum here as geth's state test runner does.
	if cfg.IsCancunAt(header.Number.Uint64(), header.Time) {
		if n := uint64(len(tx.BlobHashes())); n > cfg.BlobMaxBlobsPerBlock(header.Time) {
			return fmt.Errorf("blob count %d exceeds block maximum", n)
		}
	}
	engine := ethel.NewEthReplayEngine(cfg)
	gp := new(common.GasPool).AddGas(header.GasLimit)
	var usedGas uint64
	ibs.Prepare(tx.Hash(), types.Hash{}, 0)
	_, _, err = internal.ApplyTransaction(cfg, eestBlockHash, engine, nil, gp, ibs, mem, header, tx, &usedGas, vm.Config{})
	return err
}

// eestBlockHash is the BLOCKHASH oracle state tests are generated with.
func eestBlockHash(n uint64) types.Hash {
	return types.BytesToHash(crypto.Keccak256([]byte(new(big.Int).SetUint64(n).String())))
}

func eestStateHeader(cfg *params.ChainConfig, env *eestStateEnv) *block.Header {
	h := &block.Header{
		Coinbase:   env.Coinbase,
		Number:     uint256.NewInt(uint64(env.Number)),
		GasLimit:   uint64(env.GasLimit),
		Time:       uint64(env.Timestamp),
		Difficulty: uint256.NewInt(0),
	}
	if env.Difficulty != nil {
		h.Difficulty, _ = uint256.FromBig(env.Difficulty.ToInt())
	}
	// Post-merge state tests carry currentRandom; the header then has zero
	// difficulty and PREVRANDAO reads the mix digest.
	if cfg.IsLondon(0) && env.Random != nil {
		h.Difficulty = uint256.NewInt(0)
		h.MixDigest = types.BigToHash(env.Random.ToInt())
	}
	if cfg.IsLondon(0) {
		baseFee := big.NewInt(0x0a) // retesteth default when the env omits it
		if env.BaseFee != nil {
			baseFee = env.BaseFee.ToInt()
		}
		h.BaseFee, _ = uint256.FromBig(baseFee)
	}
	if cfg.IsCancunAt(uint64(env.Number), h.Time) {
		var excess uint64
		if env.ExcessBlobGas != nil {
			excess = uint64(*env.ExcessBlobGas)
		}
		h.ExcessBlobGas = &excess
	}
	if env.SlotNumber != nil {
		slot := uint64(*env.SlotNumber)
		h.SlotNumber = &slot
	}
	return h
}

func writeEESTPreState(mem *MemoryState, pre map[types.Address]eestAccount) {
	for addr, a := range pre {
		acc := account.NewAccount()
		acc.Initialised = true
		acc.Nonce = uint64(a.Nonce)
		if a.Balance != nil {
			acc.Balance.SetFromBig(a.Balance.ToInt())
		}
		if len(a.Code) > 0 {
			acc.CodeHash = crypto.Keccak256Hash(a.Code)
			mem.code[addr] = append([]byte(nil), a.Code...)
		}
		mem.accounts[addr] = &acc
		for k, v := range a.Storage {
			valBig, err := eest.ParseBig(v)
			if err != nil || valBig.Sign() == 0 {
				continue
			}
			keyBig, _ := eest.ParseBig(k)
			val, _ := uint256.FromBig(valBig)
			key, _ := uint256.FromBig(keyBig)
			if mem.storage[addr] == nil {
				mem.storage[addr] = make(map[types.Hash][]byte)
			}
			mem.storage[addr][types.Hash(key.Bytes32())] = val.Bytes()
		}
	}
}

// eestMemoryStateRoot computes the Ethereum MPT state root of a MemoryState,
// with the same account/storage leaf encoding as ethcompat.VerifyStateRoot.
func eestMemoryStateRoot(mem *MemoryState) types.Hash {
	emptyCodeHash := crypto.Keccak256Hash(nil)
	accountTrie := hash.NewMPTTrie()
	for addr, acc := range mem.accounts {
		storageRoot := types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
		if slots := mem.storage[addr]; len(slots) > 0 {
			st := hash.NewMPTTrie()
			n := 0
			for k, v := range slots {
				if len(v) == 0 {
					continue
				}
				enc, _ := rlp.EncodeToBytes(new(uint256.Int).SetBytes(v).Bytes())
				st.Update(crypto.Keccak256(k[:]), enc)
				n++
			}
			if n > 0 {
				storageRoot = st.Hash()
			}
		}
		codeHash := acc.CodeHash
		if codeHash == (types.Hash{}) {
			codeHash = emptyCodeHash
		}
		enc, _ := rlp.EncodeToBytes([]interface{}{acc.Nonce, acc.Balance.ToBig(), storageRoot, codeHash})
		accountTrie.Update(crypto.Keccak256(addr[:]), enc)
	}
	return accountTrie.Hash()
}

// eestLogsHash is keccak256(rlp(logs)) over the consensus log fields.
func eestLogsHash(logs []*block.Log) types.Hash {
	type rlpLog struct {
		Address types.Address
		Topics  []types.Hash
		Data    []byte
	}
	out := make([]rlpLog, 0, len(logs))
	for _, l := range logs {
		out = append(out, rlpLog{Address: l.Address, Topics: l.Topics, Data: l.Data})
	}
	enc, _ := rlp.EncodeToBytes(out)
	return crypto.Keccak256Hash(enc)
}

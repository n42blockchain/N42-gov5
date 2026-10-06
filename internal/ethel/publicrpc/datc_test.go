// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package publicrpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/mptproof"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/params"
)

func TestParseDATCVerify(t *testing.T) {
	for in, want := range map[string]DATCVerify{"": DATCVerifyHeader, "header": DATCVerifyHeader, "strict": DATCVerifyStrict, "off": DATCVerifyOff} {
		if got, err := ParseDATCVerify(in); err != nil || got != want {
			t.Fatalf("%q -> %q, %v", in, got, err)
		}
	}
	if _, err := ParseDATCVerify("sometimes"); err == nil {
		t.Fatal("an unknown mode must be rejected")
	}
}

// TestDATCGetProofMainnet serves eth_getProof through the whole public RPC
// stack (JSON-RPC server -> shared handler -> DATC proof source -> archive on
// disk) and checks every answer with the independent EIP-1186 verifier
// (mptproof.VerifyStandardProof) against the real mainnet header's
// stateRoot: account fields, storage values, absent accounts and slots, at
// early, middle and late heights and in a giant contract's first blocks.
//
//	DATC_ARCHIVE=/data/blockchain/datc-out/datc-25m-v2-hi \
//	DATC_HEADERS=/data/blockchain/witness go test ./internal/ethel/publicrpc -run TestDATCGetProofMainnet -v
//
// With DATC_RPC_URL set, the same checks run against a LIVE eth-el started
// with --publicrpc.datc (DATC_ARCHIVE is then the node's business, not ours):
//
//	DATC_RPC_URL=http://127.0.0.1:20015 DATC_HEADERS=... go test ... -run TestDATCGetProofMainnet -v
func TestDATCGetProofMainnet(t *testing.T) {
	dir, hdrDir, liveURL := os.Getenv("DATC_ARCHIVE"), os.Getenv("DATC_HEADERS"), os.Getenv("DATC_RPC_URL")
	if hdrDir == "" || (dir == "" && liveURL == "") {
		t.Skip("DATC_HEADERS and one of DATC_ARCHIVE / DATC_RPC_URL not set")
	}
	hdrs, err := ethel.OpenHeaderCompact(hdrDir)
	if err != nil {
		t.Fatal(err)
	}
	defer hdrs.Close()

	modules.N42Init()
	prev := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prev })
	// The node's own DB is empty: no headers, no state. Every answer below can
	// only come from the archive.
	var svc *Service
	if liveURL == "" {
		// strict + the headerc freezer: the node's DB is empty, so every answer is
		// verified on the node against a freezer header before the test verifies
		// it again, independently.
		if svc, err = New(Config{DATCDir: dir, DATCVerify: DATCVerifyStrict, DATCHeaders: hdrDir}, params.MainnetChainConfig, nil, memdb.NewTestDB(t), nil); err != nil {
			t.Fatal(err)
		}
		defer svc.Stop()
	}

	usdt := "0xdac17f958d2ee523a2206206994597c13d831ec7"
	cases := []struct {
		addr   string
		slots  []string
		height uint64
		exists bool
	}{
		{"0x32be343b94f860124dc4fee278fdcbd38c102d88", nil, 60_000, true},                  // early mainnet
		{"0x32be343b94f860124dc4fee278fdcbd38c102d88", nil, 1_000_000, true},               // before the account birth partitions end
		{usdt, []string{"0x2", "0x3"}, 4_640_000, true},                                    // USDT days after its creation (growth stage 0)
		{usdt, []string{"0x2", randomSlot(1)}, 15_000_000, true},                           // mature, depth-3 exact ladder
		{usdt, []string{"0x2", randomSlot(2)}, 25_800_000, true},                           // late
		{"0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48", []string{"0x1"}, 20_000_000, true},  // USDC
		{"0x00000000000000000000000000000000000dead1", []string{"0x0"}, 20_000_000, false}, // no such account
	}
	for _, tc := range cases {
		h, err := hdrs.ReadHeader(tc.height)
		if err != nil {
			t.Fatalf("header %d: %v", tc.height, err)
		}
		t0 := time.Now()
		res := callGetProof(t, svc, liveURL, tc.addr, tc.slots, tc.height)
		took := time.Since(t0)
		root := h.StateRoot()

		addr := types.HexToAddress(tc.addr)
		ah := crypto.Keccak256(addr[:])
		val, found, err := mptproof.VerifyStandardProof(decodeNodes(t, res.AccountProof), root, ah)
		if err != nil {
			t.Fatalf("%s at %d: account proof does not verify: %v", tc.addr, tc.height, err)
		}
		if found != tc.exists {
			t.Fatalf("%s at %d: found=%v, want %v", tc.addr, tc.height, found, tc.exists)
		}
		if found {
			var acct struct {
				Nonce       uint64
				Balance     *uint256.Int
				StorageRoot types.Hash
				CodeHash    types.Hash
			}
			if err := rlp.DecodeBytes(val, &acct); err != nil {
				t.Fatalf("account leaf: %v", err)
			}
			if uint64(res.Nonce) != acct.Nonce || res.Balance.ToInt().Cmp(acct.Balance.ToBig()) != 0 ||
				res.StorageHash != acct.StorageRoot || res.CodeHash != acct.CodeHash {
				t.Fatalf("%s at %d: returned fields disagree with the proven leaf", tc.addr, tc.height)
			}
		}
		for i, sp := range res.StorageProof {
			if sp.Key != tc.slots[i] {
				t.Fatalf("storage key %d echoed as %q", i, sp.Key)
			}
			if !found || res.StorageHash == emptyRootHash {
				if sp.Value.ToInt().Sign() != 0 {
					t.Fatalf("slot of an account without storage has value %s", sp.Value)
				}
				continue
			}
			slot := types.HexToHash(sp.Key)
			sv, sfound, err := mptproof.VerifyStandardProof(decodeNodes(t, sp.Proof), res.StorageHash, crypto.Keccak256(slot[:]))
			if err != nil {
				t.Fatalf("%s/%s at %d: storage proof does not verify: %v", tc.addr, sp.Key, tc.height, err)
			}
			want := new(uint256.Int)
			if sfound {
				var raw []byte
				if err := rlp.DecodeBytes(sv, &raw); err != nil {
					t.Fatalf("slot leaf: %v", err)
				}
				want.SetBytes(raw)
			}
			if sp.Value.ToInt().Cmp(want.ToBig()) != 0 {
				t.Fatalf("%s/%s at %d: value %s, proven %s", tc.addr, sp.Key, tc.height, sp.Value, want)
			}
		}
		// Historical values through the plain methods must be the PROVEN ones.
		hx := hexutil.EncodeUint64(tc.height)
		var bal hexutil.Big
		var nonce hexutil.Uint64
		callRPC(t, svc, liveURL, "eth_getBalance", []any{tc.addr, hx}, &bal)
		callRPC(t, svc, liveURL, "eth_getTransactionCount", []any{tc.addr, hx}, &nonce)
		if bal.ToInt().Cmp(res.Balance.ToInt()) != 0 || nonce != res.Nonce {
			t.Fatalf("%s at %d: eth_getBalance/TransactionCount %s/%d, proven %s/%d", tc.addr, tc.height, bal.ToInt(), nonce, res.Balance.ToInt(), res.Nonce)
		}
		for _, sp := range res.StorageProof {
			var word hexutil.Bytes
			callRPC(t, svc, liveURL, "eth_getStorageAt", []any{tc.addr, sp.Key, hx}, &word)
			if new(uint256.Int).SetBytes(word).ToBig().Cmp(sp.Value.ToInt()) != 0 {
				t.Fatalf("%s/%s at %d: eth_getStorageAt %x, proven %s", tc.addr, sp.Key, tc.height, []byte(word), sp.Value)
			}
		}
		t.Logf("%s at %d: %d account nodes, %d slots, %v", tc.addr[:10], tc.height, len(res.AccountProof), len(res.StorageProof), took.Round(time.Millisecond))
	}
}

var emptyRootHash = types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")

func randomSlot(i int) string { return fmt.Sprintf("0x%064x", uint64(i)*0x9e3779b97f4a7c15) }

type proofResult struct {
	Nonce        hexutil.Uint64 `json:"nonce"`
	Balance      *hexutil.Big   `json:"balance"`
	CodeHash     types.Hash     `json:"codeHash"`
	StorageHash  types.Hash     `json:"storageHash"`
	AccountProof []string       `json:"accountProof"`
	StorageProof []struct {
		Key   string       `json:"key"`
		Value *hexutil.Big `json:"value"`
		Proof []string     `json:"proof"`
	} `json:"storageProof"`
}

func callGetProof(t *testing.T, svc *Service, liveURL, addr string, slots []string, height uint64) proofResult {
	t.Helper()
	if slots == nil {
		slots = []string{}
	}
	var out proofResult
	callRPC(t, svc, liveURL, "eth_getProof", []any{addr, slots, hexutil.EncodeUint64(height)}, &out)
	return out
}

// callRPC sends one JSON-RPC call to the live node (liveURL) or the in-process
// server and decodes a non-null result into out.
func callRPC(t *testing.T, svc *Service, liveURL, method string, params []any, out any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	var raw []byte
	if liveURL != "" {
		hr, err := http.Post(liveURL, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST %s: %v", liveURL, err)
		}
		raw, err = io.ReadAll(hr.Body)
		hr.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
	} else {
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		svc.rpc.ServeHTTP(rec, req)
		raw = rec.Body.Bytes()
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("response: %v: %s", err, raw)
	}
	if len(resp.Result) == 0 || string(resp.Result) == "null" {
		t.Fatalf("%s %v: no result: %s", method, params, strings.TrimSpace(string(raw)))
	}
	if err := json.Unmarshal(resp.Result, out); err != nil {
		t.Fatalf("%s result: %v: %s", method, err, resp.Result)
	}
}

func decodeNodes(t *testing.T, hexNodes []string) [][]byte {
	t.Helper()
	out := make([][]byte, len(hexNodes))
	for i, h := range hexNodes {
		b, err := hexutil.Decode(h)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = b
	}
	return out
}

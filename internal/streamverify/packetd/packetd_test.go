// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package packetd_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/cmd/evmsdk"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/streamverify/packetd"
	"github.com/n42blockchain/N42/internal/streamverify/packetd/packetdtest"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/params"
)

// mapReader is a parent state held in maps.
type mapReader struct {
	accounts map[types.Address]*account.StateAccount
	storage  map[types.Address]map[types.Hash][]byte
	codes    map[types.Hash][]byte
}

func (m *mapReader) ReadAccountData(a types.Address) (*account.StateAccount, error) {
	acc, ok := m.accounts[a]
	if !ok {
		return nil, nil
	}
	cp := *acc
	return &cp, nil
}

func (m *mapReader) ReadAccountStorage(a types.Address, k *types.Hash) ([]byte, error) {
	return m.storage[a][*k], nil
}

func (m *mapReader) ReadAccountCode(_ types.Address, h types.Hash) ([]byte, error) {
	return m.codes[h], nil
}

func (m *mapReader) ReadAccountCodeSize(a types.Address, h types.Hash) (int, error) {
	c, _ := m.ReadAccountCode(a, h)
	return len(c), nil
}

type fixturePacket struct {
	Name   string `json:"name"`
	Packet string `json:"packet"`
}

func loadFixturePackets(t *testing.T) map[string][]byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "mobileverify", "testdata", "cross_client_v1", "stream_packet.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Packets []fixturePacket `json:"packets"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	out := make(map[string][]byte)
	for _, p := range f.Packets {
		b, err := hex.DecodeString(p.Packet)
		if err != nil {
			t.Fatal(err)
		}
		out[p.Name] = b
	}
	return out
}

// fixtureCase is a fixture packet with the parent state and chain it was
// produced on (stream_packet.json's _doc).
type fixtureCase struct {
	name  string
	want  []byte
	cfg   *params.ChainConfig
	hdr   *block.Header
	txs   transaction.Transactions
	state *mapReader
}

func fixtureCases(t *testing.T) []fixtureCase {
	t.Helper()
	pkts := loadFixturePackets(t)
	var out []fixtureCase
	for _, c := range []struct{ name, chain string }{
		{"empty_block_88", "mainnet"},
		{"transfer", "mainnet_qmdb_staggered"},
	} {
		want, ok := pkts[c.name]
		if !ok {
			t.Fatalf("fixture packet %s missing", c.name)
		}
		pkt, err := evmsdk.DecodeStreamPacket(want)
		if err != nil {
			t.Fatal(err)
		}
		hdr := new(block.Header)
		if err := hdr.Unmarshal(pkt.HeaderRLP); err != nil {
			t.Fatal(err)
		}
		var txs transaction.Transactions
		for _, raw := range pkt.Transactions {
			tx := new(transaction.Transaction)
			if err := tx.Unmarshal(raw); err != nil {
				t.Fatal(err)
			}
			txs = append(txs, tx)
		}
		cfg := params.ChainConfigByChainName(c.chain)
		if cfg == nil {
			t.Fatalf("chain %s missing", c.chain)
		}
		st := &mapReader{accounts: map[types.Address]*account.StateAccount{}, codes: map[types.Hash][]byte{}}
		if c.name == "transfer" {
			// The generator's parent state: the sender (nonce 577, 1 ETH) and
			// both block-start system contracts as nonce-1 stubs.
			sysCode := []byte{0x60, 0x00, 0x60, 0x00, 0xf3}
			sys := account.NewAccount()
			sys.Initialised, sys.Nonce, sys.CodeHash = true, 1, crypto.Keccak256Hash(sysCode)
			beacon, history := sys, sys
			sender := account.NewAccount()
			sender.Initialised, sender.Nonce, sender.CodeHash = true, 577, evmsdk.EmptyCodeHash
			sender.Balance.SetUint64(1e18)
			st.accounts[params.BeaconRootsAddress] = &beacon
			st.accounts[vm.HistoryStorageAddress] = &history
			st.accounts[*txs[0].From()] = &sender
			st.codes[sys.CodeHash] = sysCode
		}
		out = append(out, fixtureCase{name: c.name, want: want, cfg: cfg, hdr: hdr, txs: txs, state: st})
	}
	return out
}

// The sidecar reproduces gov5's cross-client packets byte for byte from
// their header, transactions and parent state.
func TestBuildPacketReproducesCrossClientFixtures(t *testing.T) {
	for _, c := range fixtureCases(t) {
		t.Run(c.name, func(t *testing.T) {
			built, err := packetd.BuildPacket(c.cfg, nil, c.hdr, c.txs, c.state, nil)
			if err != nil {
				t.Fatal(err)
			}
			got, err := evmsdk.EncodeStreamPacket(built.Packet)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, c.want) {
				t.Fatalf("packet differs from the fixture\n got %x\nwant %x", got, c.want)
			}
		})
	}
}

// ingestServer stands in for the Rust node's loopback ingest: it decodes the
// packet, checks the header hash and answers as http.rs does.
func ingestServer(t *testing.T, got *[][]byte) *httptest.Server {
	seen := map[types.Hash]bool{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/mobileverify/packet" {
			http.Error(w, "bad route", http.StatusNotFound)
			return
		}
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		pkt, err := evmsdk.DecodeStreamPacket(buf.Bytes())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		n, err := pkt.HeaderInfo()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var h block.Header
		if err := h.Unmarshal(pkt.HeaderRLP); err != nil || h.Hash() != pkt.BlockHash {
			http.Error(w, "header does not hash to the block hash", http.StatusBadRequest)
			return
		}
		*got = append(*got, buf.Bytes())
		isNew := !seen[pkt.BlockHash]
		seen[pkt.BlockHash] = true
		fmt.Fprintf(w, "{\"hash\":\"%s\",\"new\":%v,\"number\":%d}\n", pkt.BlockHash.Hex(), isNew, n)
	}))
}

// The whole sidecar path against a JSON-RPC node: the block fetched as gov5
// types by each of the three routes (raw block, raw header, block JSON), the
// state read over eth_* at the parent, the packet written and PUT. Every
// route yields the fixture's bytes, and the phone's verifier accepts them.
func TestFollowerReproducesFixturesOverRPC(t *testing.T) {
	for _, c := range fixtureCases(t) {
		for _, route := range []struct {
			name                string
			noRawBlock, noRawHd bool
			want                string
		}{
			{"raw-block", false, false, "raw-block"},
			{"raw-header", true, false, "raw-header"},
			{"json-header", true, true, "json-header"},
		} {
			t.Run(c.name+"/"+route.name, func(t *testing.T) {
				n := c.hdr.Number.Uint64()
				node := &packetdtest.Node{
					Blocks:      []packetdtest.Block{{Header: c.hdr, Txs: c.txs}},
					First:       n,
					State:       c.state,
					StateBlock:  c.hdr.ParentHash,
					NoRawBlock:  route.noRawBlock,
					NoRawHeader: route.noRawHd,
				}
				rpc := httptest.NewServer(node)
				defer rpc.Close()
				var ingested [][]byte
				mv := ingestServer(t, &ingested)
				defer mv.Close()
				out := t.TempDir()

				var src string
				var res *packetd.IngestResult
				f := &packetd.Follower{
					RPC:         packetd.NewClient(rpc.URL, 5*time.Second),
					Chain:       c.cfg,
					IngestURL:   mv.URL,
					OutDir:      out,
					Concurrency: 4,
					Built: func(_ uint64, b *packetd.Block, _ *packetd.Built, r *packetd.IngestResult) {
						src, res = b.Source, r
					},
				}
				if err := f.Process(context.Background(), n); err != nil {
					t.Fatal(err)
				}
				if src != route.want {
					t.Fatalf("block fetched by %q, want %q", src, route.want)
				}
				hash := c.hdr.Hash()
				written, err := os.ReadFile(filepath.Join(out, hash.Hex()+".bin"))
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(written, c.want) {
					t.Fatalf("packet differs from the fixture\n got %x\nwant %x", written, c.want)
				}
				if len(ingested) != 1 || !bytes.Equal(ingested[0], c.want) {
					t.Fatalf("ingest received %d packets, want the fixture once", len(ingested))
				}
				if res == nil || !res.New || res.Number != n || types.HexToHash(res.Hash) != hash {
					t.Fatalf("ingest answer %+v", res)
				}
				// The same block again is remembered and not rebuilt.
				if err := f.Process(context.Background(), n); err != nil || len(ingested) != 1 {
					t.Fatalf("second Process: err %v, ingested %d", err, len(ingested))
				}

				pkt, err := evmsdk.DecodeStreamPacket(written)
				if err != nil {
					t.Fatal(err)
				}
				vr, err := evmsdk.ExecuteAndVerifyV2(pkt, evmsdk.NewCodeCache(16), c.cfg)
				if err != nil {
					t.Fatalf("phone verifier rejected the packet: %v", err)
				}
				if vr.TxCount != len(c.txs) || vr.ComputedReceiptsRoot != c.hdr.ReceiptHash {
					t.Fatalf("verifier result %+v", vr)
				}
			})
		}
	}
}

// A raw header that does not hash to the node's block hash fails the block
// for good, never yields a packet.
func TestFetchBlockRefusesHashMismatch(t *testing.T) {
	c := fixtureCases(t)[1]
	n := c.hdr.Number.Uint64()
	for _, noRawBlock := range []bool{false, true} {
		node := &packetdtest.Node{
			Blocks: []packetdtest.Block{{Header: c.hdr, Txs: c.txs}}, First: n,
			State: c.state, StateBlock: c.hdr.ParentHash, TamperRaw: true, NoRawBlock: noRawBlock,
		}
		rpc := httptest.NewServer(node)
		_, err := packetd.FetchBlock(context.Background(), packetd.NewClient(rpc.URL, 5*time.Second), c.cfg, n)
		rpc.Close()
		if err == nil || !packetd.IsPermanent(err) {
			t.Fatalf("noRawBlock=%v: err = %v, want a permanent hash mismatch", noRawBlock, err)
		}
	}
}

// A block whose header claims other gas than its execution uses is dropped
// for good: the phone would reject the packet.
func TestBuildPacketRefusesGasMismatch(t *testing.T) {
	c := fixtureCases(t)[1]
	h := block.CopyHeader(c.hdr)
	h.GasUsed++
	h.ResetHashCache()
	if _, err := packetd.BuildPacket(c.cfg, nil, h, c.txs, c.state, nil); err == nil || !packetd.IsPermanent(err) {
		t.Fatalf("err = %v, want a permanent gas mismatch", err)
	}
}

// The RPC reader: an empty account (nonce 0, balance 0, no code) is absent,
// storage values are minimal big-endian (zero = nil), code is checked against
// its hash, a state query the node refuses is an error kept for Err.
func TestRPCStateReader(t *testing.T) {
	code := []byte{0x60, 0x01, 0x60, 0x00, 0x55, 0x00}
	contract := account.NewAccount()
	contract.Initialised, contract.Nonce, contract.CodeHash = true, 1, crypto.Keccak256Hash(code)
	contract.Balance.SetUint64(7)
	empty := account.NewAccount()
	empty.Initialised, empty.CodeHash = true, evmsdk.EmptyCodeHash
	ca, ea, absent := types.Address{0xc}, types.Address{0xe}, types.Address{0xa}
	slot1, slot2 := types.HexToHash("0x01"), types.HexToHash("0x02")
	st := &mapReader{
		accounts: map[types.Address]*account.StateAccount{ca: &contract, ea: &empty},
		storage:  map[types.Address]map[types.Hash][]byte{ca: {slot1: {0x00, 0x2a}}},
		codes:    map[types.Hash][]byte{contract.CodeHash: code},
	}
	parent := types.Hash{0x99}
	node := &packetdtest.Node{State: st, StateBlock: parent}
	rpc := httptest.NewServer(node)
	defer rpc.Close()
	c := packetd.NewClient(rpc.URL, 5*time.Second)
	r := packetd.NewRPCStateReader(context.Background(), c, packetd.BlockHashParam(parent), 2)
	r.Prefetch([]types.Address{ca, ea}, map[types.Address][]types.Hash{ca: {slot1}})

	acc, err := r.ReadAccountData(ca)
	if err != nil || acc == nil || acc.Nonce != 1 || !acc.Balance.Eq(uint256.NewInt(7)) || acc.CodeHash != contract.CodeHash {
		t.Fatalf("contract = %+v, %v", acc, err)
	}
	for _, a := range []types.Address{ea, absent} {
		if acc, err := r.ReadAccountData(a); err != nil || acc != nil {
			t.Fatalf("%v = %+v, %v; want absent", a, acc, err)
		}
	}
	if v, err := r.ReadAccountStorage(ca, &slot1); err != nil || !bytes.Equal(v, []byte{0x2a}) {
		t.Fatalf("slot1 = %x, %v", v, err)
	}
	if v, err := r.ReadAccountStorage(ca, &slot2); err != nil || v != nil {
		t.Fatalf("slot2 = %x, %v; want nil", v, err)
	}
	if got, err := r.ReadAccountCode(ca, contract.CodeHash); err != nil || !bytes.Equal(got, code) {
		t.Fatalf("code = %x, %v", got, err)
	}
	if r.Err() != nil {
		t.Fatal(r.Err())
	}
	before := c.Requests()
	_, _ = r.ReadAccountData(ca)
	_, _ = r.ReadAccountStorage(ca, &slot1)
	if c.Requests() != before {
		t.Fatal("cached reads went to the node")
	}
	// Code under a hash the node's code does not have.
	if _, err := r.ReadAccountCode(ea, types.Hash{0x01}); err == nil {
		t.Fatal("code with the wrong hash accepted")
	}
	// The node refuses state at another block.
	other := packetd.NewRPCStateReader(context.Background(), c, packetd.BlockHashParam(types.Hash{0x01}), 1)
	if _, err := other.ReadAccountData(ca); err == nil || other.Err() == nil {
		t.Fatalf("state at an unserved block: err %v, Err %v", err, other.Err())
	}
}

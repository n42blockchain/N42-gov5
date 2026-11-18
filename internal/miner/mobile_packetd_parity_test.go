// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"bytes"
	"context"
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
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/streamverify/packetd"
	"github.com/n42blockchain/N42/internal/streamverify/packetd/packetdtest"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// viewReader answers each read in its own read transaction, so the JSON-RPC
// test node can serve the chain's state from its handler goroutines.
type viewReader struct{ db kv.RwDB }

func (v viewReader) view(f func(r state.StateReader) error) error {
	return v.db.View(context.Background(), func(tx kv.Tx) error { return f(state.NewPlainStateReader(tx)) })
}

func (v viewReader) ReadAccountData(a types.Address) (acc *account.StateAccount, err error) {
	err = v.view(func(r state.StateReader) error { acc, err = r.ReadAccountData(a); return err })
	return acc, err
}

func (v viewReader) ReadAccountStorage(a types.Address, k *types.Hash) (val []byte, err error) {
	err = v.view(func(r state.StateReader) error { val, err = r.ReadAccountStorage(a, k); return err })
	return val, err
}

func (v viewReader) ReadAccountCode(a types.Address, h types.Hash) (code []byte, err error) {
	err = v.view(func(r state.StateReader) error {
		c, e := r.ReadAccountCode(a, h)
		code = append([]byte(nil), c...)
		return e
	})
	return code, err
}

func (v viewReader) ReadAccountCodeSize(a types.Address, h types.Hash) (int, error) {
	c, err := v.ReadAccountCode(a, h)
	return len(c), err
}

// ppContract is a contract that reads and writes its slot 0, reads its empty
// slot 1 and the balance of an account that does not exist:
// SLOAD(0)+1 -> SSTORE(0); SLOAD(1); BALANCE(0xdead..).
var (
	ppContractAddr = types.HexToAddress("0x00000000000000000000000000000000000c0de1")
	ppContractCode = append(append([]byte{
		0x60, 0x00, 0x54, 0x60, 0x01, 0x01, 0x60, 0x00, 0x55, // slot0 = slot0 + 1
		0x60, 0x01, 0x54, 0x50, // SLOAD(1), POP
		0x73, // PUSH20
	}, types.HexToAddress("0x000000000000000000000000000000000000dead").Bytes()...),
		0x31, 0x50, 0x00) // BALANCE, POP, STOP
)

func ppWithFrom(f *mnTChainFixture, senderIdx int, tx *transaction.Transaction) *transaction.Transaction {
	// The pool sets the recovered sender at admission (txs_pool.go); the
	// leader's packet therefore carries `from`, and so must the sidecar's.
	tx.SetFrom(f.Senders[senderIdx])
	return tx
}

// TestPacketdPacketMatchesLeaderPacket: for a block the leader built, the
// sidecar's packet -- built from the sealed header and transactions alone,
// re-executing over the parent state -- is byte-identical to the leader's,
// (a) reading the chain's state directly and (b) over JSON-RPC from a node
// that serves the chain, fetching the block as an Ethereum-form raw block.
// The phone's verifier accepts it.
func TestPacketdPacketMatchesLeaderPacket(t *testing.T) {
	cases := []struct {
		name string
		pool func(t *testing.T, f *mnTChainFixture) map[types.Address][]*transaction.Transaction
		// diverged: the build threw reads away and the leader rebuilt its log
		diverged bool
	}{
		{
			name: "contract-and-transfers",
			pool: func(t *testing.T, f *mnTChainFixture) map[types.Address][]*transaction.Transaction {
				s := f.Senders
				call := ppWithFrom(f, 0, f.mpTransfer(t, 0, 0, 3_000_000_000, ppContractAddr, uint256.NewInt(0)))
				call2 := ppWithFrom(f, 0, f.mpTransfer(t, 0, 1, 3_000_000_000, ppContractAddr, uint256.NewInt(5)))
				xfer := ppWithFrom(f, 1, f.mpTransfer(t, 1, 0, 2_000_000_000, s[2], uint256.NewInt(1_000)))
				fresh := ppWithFrom(f, 2, f.mpTransfer(t, 2, 0, 1_000_000_000, types.HexToAddress("0x0000000000000000000000000000000000fe5101"), uint256.NewInt(1)))
				return map[types.Address][]*transaction.Transaction{s[0]: {call, call2}, s[1]: {xfer}, s[2]: {fresh}}
			},
		},
		{
			name: "failed-candidates",
			pool: func(t *testing.T, f *mnTChainFixture) map[types.Address][]*transaction.Transaction {
				p := mpFailingCandidatesPool(t, f)
				for i, a := range f.Senders {
					for _, tx := range p[a] {
						ppWithFrom(f, i, tx)
					}
				}
				return p
			},
			diverged: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := mnTNewChainFixtureWith(t, func(_ *params.ChainConfig, alloc conf.GenesisAlloc) {
				alloc[ppContractAddr] = conf.GenesisAccount{
					Balance: "0x0",
					Nonce:   1,
					Code:    ppContractCode,
					Storage: map[types.Hash]types.Hash{{}: types.BytesToHash([]byte{5})},
				}
			})
			w := mnTNewIdleWorker(t, f, &mnTStubTxsPool{})
			w.setCoinbase(f.Coinbase)
			_, env := mnTPrepareEnvAndState(t, w, f)
			w.txsPool = &mnTStubTxsPool{pending: c.pool(t, f)}

			rec, base := mpFillWithRecorder(t, w, f, env)
			if len(env.txs) == 0 {
				t.Fatal("the fill included no transactions")
			}
			if rec.Diverged() != c.diverged {
				t.Fatalf("leader read log diverged = %v, want %v", rec.Diverged(), c.diverged)
			}
			leader := mpBuildPacket(t, w, env, rec)
			want, err := evmsdk.EncodeStreamPacket(leader)
			if err != nil {
				t.Fatal(err)
			}
			if leader.ReadLogData == nil || len(leader.ReadLogData) <= 4 {
				t.Fatal("test setup: the leader's read log is empty")
			}

			// The sidecar sees only what the node serves: the sealed header
			// and the transactions.
			var hdr block.Header
			if err := hdr.Unmarshal(leader.HeaderRLP); err != nil {
				t.Fatal(err)
			}
			if hdr.Hash() != leader.BlockHash {
				t.Fatal("test setup: packet header does not hash to the block hash")
			}
			n := hdr.Number.Uint64()

			// (a) the chain's own state reader at the parent.
			built, err := packetd.BuildPacket(w.chainConfig, nil, &hdr, env.txs, base, nil)
			if err != nil {
				t.Fatalf("sidecar over the state reader: %v", err)
			}
			got, err := evmsdk.EncodeStreamPacket(built.Packet)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				mpAssertSameReadLog(t, built.Packet.ReadLogData, mustDecodeLog(t, leader.ReadLogData))
				t.Fatalf("sidecar packet (state reader) differs from the leader's\n got %x\nwant %x", got, want)
			}

			// (b) JSON-RPC: block from debug_getRawBlock, state from eth_* at
			// the parent hash.
			node := &packetdtest.Node{
				Blocks:     []packetdtest.Block{{Header: &hdr, Txs: env.txs}},
				First:      n,
				State:      viewReader{f.DB},
				StateBlock: hdr.ParentHash,
			}
			srv := httptest.NewServer(node)
			defer srv.Close()
			out := t.TempDir()
			fl := &packetd.Follower{
				RPC:         packetd.NewClient(srv.URL, 5*time.Second),
				Chain:       w.chainConfig,
				OutDir:      out,
				Concurrency: 4,
			}
			if err := fl.Process(context.Background(), n); err != nil {
				t.Fatalf("sidecar over JSON-RPC: %v", err)
			}
			viaRPC, err := os.ReadFile(filepath.Join(out, leader.BlockHash.Hex()+".bin"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(viaRPC, want) {
				pkt, derr := evmsdk.DecodeStreamPacket(viaRPC)
				if derr == nil {
					mpAssertSameReadLog(t, pkt.ReadLogData, mustDecodeLog(t, leader.ReadLogData))
				}
				t.Fatalf("sidecar packet (JSON-RPC) differs from the leader's\n got %x\nwant %x", viaRPC, want)
			}
			if node.Calls["debug_getRawBlock"] != 1 || node.Calls["eth_getStorageAt"] == 0 && c.name == "contract-and-transfers" {
				t.Fatalf("unexpected node calls %v", node.Calls)
			}

			// The phone accepts it.
			pkt, err := evmsdk.DecodeStreamPacket(viaRPC)
			if err != nil {
				t.Fatal(err)
			}
			res, err := evmsdk.ExecuteAndVerifyV2(pkt, evmsdk.NewCodeCache(16), w.chainConfig)
			if err != nil {
				t.Fatalf("phone verifier rejected the sidecar's packet: %v", err)
			}
			if res.TxCount != len(env.txs) {
				t.Fatalf("verifier executed %d txs, want %d", res.TxCount, len(env.txs))
			}
		})
	}
}

func mustDecodeLog(t *testing.T, data []byte) []evmsdk.ReadLogEntry {
	t.Helper()
	l, err := evmsdk.DecodeReadLog(data)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

// The cross-client vector produced by the n42-rs side
// (crates/n42/n42-testing/testdata/deferred_execution_vectors.json): a
// chain across the fork, per block the header as carried and `executed` =
// the block's own result. gov5's checker must accept every header: before
// the fork a header carries its own result, from the fork its parent's.
func TestDeferredExecutionCrossClientVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/deferred_execution_vectors.json")
	if err != nil {
		t.Skip("vector file not present")
	}
	var v struct {
		DeferredExecutionTime uint64 `json:"deferredExecutionTime"`
		ForkBlock             uint64 `json:"forkBlock"`
		Genesis               struct {
			Header map[string]string `json:"header"`
		} `json:"genesis"`
		Blocks []struct {
			Number   uint64            `json:"number"`
			Deferred bool              `json:"deferred"`
			Header   map[string]string `json:"header"`
			Executed map[string]string `json:"executed"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	cfg := &params.ChainConfig{DeferredExecutionTime: new(big.Int).SetUint64(v.DeferredExecutionTime)}
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	hexU64 := func(s string) uint64 {
		n, err := strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 64)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return n
	}
	hexHash := func(s string) types.Hash { return types.HexToHash(s) }
	hexBloom := func(s string) (b block.Bloom) {
		bs, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
		if err != nil || len(bs) != len(b) {
			t.Fatalf("bloom %q", s[:20])
		}
		copy(b[:], bs)
		return
	}
	toHeader := func(number uint64, m map[string]string) *block.Header {
		h := &block.Header{Number: uint256.NewInt(number), Time: hexU64(m["timestamp"]), Root: hexHash(m["stateRoot"]), ReceiptHash: hexHash(m["receiptsRoot"]), Bloom: hexBloom(m["logsBloom"]), GasUsed: hexU64(m["gasUsed"])}
		if p, ok := m["parentHash"]; ok {
			h.ParentHash = hexHash(p)
		}
		return h
	}
	toResult := func(m map[string]string) rawdb.ExecutedResult {
		return rawdb.ExecutedResult{Root: hexHash(m["stateRoot"]), ReceiptHash: hexHash(m["receiptsRoot"]), Bloom: hexBloom(m["logsBloom"]), GasUsed: hexU64(m["gasUsed"])}
	}

	parent := toHeader(0, v.Genesis.Header)
	for i, b := range v.Blocks {
		h := toHeader(b.Number, b.Header)
		if cfg.IsDeferredExecution(h.Time) != b.Deferred || (b.Number >= v.ForkBlock) != b.Deferred {
			t.Fatalf("block %d: gate disagrees with the vector's deferred flag", b.Number)
		}
		if b.Deferred {
			if err := checkDeferredHeader(cfg, tx, h, parent); err != nil {
				t.Fatalf("block %d: %v", b.Number, err)
			}
			// A header carrying its OWN result must be refused past the fork
			// (except at the fork block itself when they coincide).
			own := *h
			own.Root, own.ReceiptHash, own.Bloom, own.GasUsed = hexHash(b.Executed["stateRoot"]), hexHash(b.Executed["receiptsRoot"]), hexBloom(b.Executed["logsBloom"]), hexU64(b.Executed["gasUsed"])
			if own.Root != h.Root && checkDeferredHeader(cfg, tx, &own, parent) == nil {
				t.Fatalf("block %d: a header carrying its own result passed", b.Number)
			}
		} else if got := toResult(b.Header); got != toResult(b.Executed) {
			t.Fatalf("block %d: before the fork the header must carry its own result", b.Number)
		}
		// Store this block's own result the way the import does, so the next
		// header is checked against it.
		if err := rawdb.WriteExecutedResult(tx, h.Hash(), toResult(b.Executed)); err != nil {
			t.Fatal(err)
		}
		if i+1 < len(v.Blocks) && v.Blocks[i+1].Number != b.Number+1 {
			t.Fatalf("vector blocks not contiguous at %d", b.Number)
		}
		parent = h
	}
	// executed_root_of(n) = header(n+1).stateRoot from the fork on.
	for i := 0; i+1 < len(v.Blocks); i++ {
		n, next := v.Blocks[i], v.Blocks[i+1]
		if next.Deferred && next.Header["stateRoot"] != n.Executed["stateRoot"] {
			t.Fatalf("executed_root_of(%d) != header(%d).stateRoot", n.Number, next.Number)
		}
	}
}

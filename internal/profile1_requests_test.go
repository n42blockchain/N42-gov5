// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

// Test validator keys for the request calls (any 48 bytes: the native chain
// ignores requests, AM25).
var (
	p1ReqSourcePubkey = bytes.Repeat([]byte{0xa1}, 48)
	p1ReqTargetPubkey = bytes.Repeat([]byte{0xb2}, 48)
	p1ReqAmountGwei   = uint64(1_000_000_000)
)

// p1RequestTxs returns a fee-paying EIP-7002 withdrawal request (validator
// pubkey || amount, 8 bytes big-endian) and a fee-paying EIP-7251
// consolidation request (source pubkey || target pubkey) from the runner's
// sender. The fee with an empty queue is 1 wei.
func p1RequestTxs(r *v3Runner) []*transaction.Transaction {
	withdrawal := append(append([]byte(nil), p1ReqSourcePubkey...), make([]byte, 8)...)
	binary.BigEndian.PutUint64(withdrawal[48:], p1ReqAmountGwei)
	consolidation := append(append([]byte(nil), p1ReqSourcePubkey...), p1ReqTargetPubkey...)
	w, c := vm.WithdrawalRequestsAddress, vm.ConsolidationRequestsAddress
	return []*transaction.Transaction{
		r.sign(&w, uint256.NewInt(1), 10_000_000, withdrawal),
		r.sign(&c, uint256.NewInt(1), 10_000_000, consolidation),
	}
}

// p1WantRequests is what the end-of-block system calls return for
// p1RequestTxs from sender: type 0x01 source || pubkey || amount (the
// contract emits the amount little-endian, as EIP-7002 defines), type 0x02
// source || source pubkey || target pubkey (EIP-7002, EIP-7251).
func p1WantRequests(sender types.Address) []hexutil.Bytes {
	w := append([]byte{vm.WithdrawalRequestType}, sender.Bytes()...)
	w = append(w, p1ReqSourcePubkey...)
	w = binary.LittleEndian.AppendUint64(w, p1ReqAmountGwei)
	c := append([]byte{vm.ConsolidationRequestType}, sender.Bytes()...)
	c = append(c, p1ReqSourcePubkey...)
	c = append(c, p1ReqTargetPubkey...)
	return []hexutil.Bytes{w, c}
}

// p1V1Runner is a V3 runner on the V1 8 s genesis plus the funded sender.
func p1V1Runner(t *testing.T) *v3Runner {
	t.Helper()
	_, sender := v3Key(t)
	g := new(conf.Genesis)
	if err := json.Unmarshal(v1GenesisJSON(t), g); err != nil {
		t.Fatal(err)
	}
	g.Alloc[sender] = conf.GenesisAccount{Balance: v3SenderBalanceWei}
	r, _ := newV3RunnerFrom(t, g)
	return r
}

// On profile 1 a block whose transactions call the EIP-7002 and EIP-7251
// contracts produces their requests exactly as on Ethereum (AM25): the end of
// the block dequeues them, the block is valid, and its stored result holds
// the EIP-7685 hash sha256(sha256(0x01 || data) || sha256(0x02 || data)),
// which the next header carries. The block after it, whose queues are empty,
// produces sha256("").
func TestProfile1RequestsAreProducedAndHashed(t *testing.T) {
	r := p1V1Runner(t)
	r.run("requests", "", types.Hash{}, p1RequestTxs(r))
	for i, rc := range r.lastReceipts {
		if rc.Status != block.ReceiptStatusSuccessful {
			t.Fatalf("request tx %d failed", i)
		}
	}
	want := p1WantRequests(r.sender)
	if len(r.lastRequests) != 2 || !bytes.Equal(r.lastRequests[0], want[0]) || !bytes.Equal(r.lastRequests[1], want[1]) {
		t.Fatalf("requests %x, want %x", r.lastRequests, want)
	}
	agg := sha256.New()
	for _, q := range want {
		s := sha256.Sum256(q)
		agg.Write(s[:])
	}
	wantHash := types.BytesToHash(agg.Sum(nil))
	res := profile1ExecutedResult(r)
	if res.RequestsHash != wantHash || wantHash == rawdb.EmptyRequestsHash {
		t.Fatalf("stored requests hash %s, want %s", res.RequestsHash.Hex(), wantHash.Hex())
	}

	r.run("drained", "", types.Hash{}, nil)
	if len(r.lastRequests) != 0 || profile1ExecutedResult(r).RequestsHash != rawdb.EmptyRequestsHash {
		t.Fatalf("next block: requests %x, hash %s, want none and sha256(\"\")", r.lastRequests, profile1ExecutedResult(r).RequestsHash.Hex())
	}
}

// The deferred check compares a profile-1 header's requestsHash with the
// parent's executed value (AM25); genesis is sha256("") for block 1.
func TestProfile1CheckDeferredHeaderRequestsHash(t *testing.T) {
	cfg := &params.ChainConfig{N42Profile: params.N42Profile1, PragueTime: bigInt(0)}
	r := p1V1Runner(t)
	gen := r.parent
	if gen.RequestsHash == nil || *gen.RequestsHash != rawdb.EmptyRequestsHash {
		t.Fatalf("genesis requestsHash %v", gen.RequestsHash)
	}
	carry := func(parent *block.Header, res rawdb.ExecutedResult, rh types.Hash) *block.Header {
		return &block.Header{Number: uint256.NewInt(parent.Number.Uint64() + 1), Time: parent.Time, ParentHash: parent.Hash(),
			Root: res.Root, ReceiptHash: res.ReceiptHash, Bloom: res.Bloom, GasUsed: res.GasUsed, RequestsHash: &rh}
	}
	genRes, err := ExecutedResultOfHeader(cfg, r.tx, gen)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkDeferredHeader(cfg, r.tx, carry(gen, genRes, rawdb.EmptyRequestsHash), gen); err != nil {
		t.Fatalf("block 1 carrying sha256(\"\"): %v", err)
	}
	if err := checkDeferredHeader(cfg, r.tx, carry(gen, genRes, types.Hash{1}), gen); !errors.Is(err, ErrDeferredRequestsHash) {
		t.Fatalf("block 1 carrying another value: %v, want ErrDeferredRequestsHash", err)
	}

	parent := &block.Header{Number: uint256.NewInt(5), Time: 10, Root: types.Hash{1}}
	res := rawdb.ExecutedResult{Root: types.Hash{2}, GasUsed: 7, RequestsHash: types.Hash{0xaa}}
	if err := rawdb.WriteExecutedResult(r.tx, parent.Hash(), res); err != nil {
		t.Fatal(err)
	}
	if err := checkDeferredHeader(cfg, r.tx, carry(parent, res, res.RequestsHash), parent); err != nil {
		t.Fatalf("carrying the parent's requests hash: %v", err)
	}
	for _, bad := range []*block.Header{carry(parent, res, rawdb.EmptyRequestsHash), {Number: uint256.NewInt(6), Time: 10,
		Root: res.Root, GasUsed: res.GasUsed}} {
		if err := checkDeferredHeader(cfg, r.tx, bad, parent); !errors.Is(err, ErrDeferredRequestsHash) {
			t.Fatalf("carrying %v: %v, want ErrDeferredRequestsHash", bad.RequestsHash, err)
		}
	}
	// Legacy deferred chains keep the per-block check; the carried value is
	// not compared.
	legacy := &params.ChainConfig{DeferredExecutionTime: bigInt(0), PragueTime: bigInt(0)}
	if err := checkDeferredHeader(legacy, r.tx, carry(parent, res, types.Hash{3}), parent); err != nil {
		t.Fatalf("legacy chain: %v", err)
	}
}

// The write stores the requests hash end-of-block processing recorded, else
// the leader's; a profile-1 block with neither has none.
func TestExecutedRequestsHashForWrite(t *testing.T) {
	p1 := &params.ChainConfig{N42Profile: params.N42Profile1, PragueTime: bigInt(0)}
	h := &block.Header{Number: uint256.NewInt(3), Time: 5}
	hint := rawdb.ExecutedResult{RequestsHash: types.Hash{9}}
	if got, err := executedRequestsHashForWrite(p1, h, types.Hash{1}, true, hint, true); err != nil || got != (types.Hash{1}) {
		t.Fatalf("own: %s %v", got.Hex(), err)
	}
	if got, err := executedRequestsHashForWrite(p1, h, types.Hash{}, false, hint, true); err != nil || got != (types.Hash{9}) {
		t.Fatalf("hint: %s %v", got.Hex(), err)
	}
	if _, err := executedRequestsHashForWrite(p1, h, types.Hash{}, false, rawdb.ExecutedResult{}, false); err == nil {
		t.Fatal("profile 1 without a figure accepted")
	}
	if got, err := executedRequestsHashForWrite(&params.ChainConfig{}, h, types.Hash{}, false, rawdb.ExecutedResult{}, false); err != nil || got != rawdb.EmptyRequestsHash {
		t.Fatalf("legacy: %s %v", got.Hex(), err)
	}
}

// p1PragueGuardChain is p1GuardChain on a Prague profile-1 chain whose child
// carries childRequests as requestsHash.
func p1PragueGuardChain(t *testing.T, engine consensus.Engine, result rawdb.ExecutedResult, childRequests types.Hash) (*BlockChain, block.IBlock) {
	t.Helper()
	bc, parent, child := guardChain(t, true)
	bc.chainConfig = &params.ChainConfig{N42Profile: params.N42Profile1, PragueTime: bigInt(0)}
	bc.engine = engine
	child.RequestsHash = &childRequests
	tx, err := bc.ChainDB.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result.Root, result.ReceiptHash, result.Bloom, result.GasUsed = child.Root, child.ReceiptHash, child.Bloom, child.GasUsed
	if err := rawdb.WriteExecutedResult(tx, parent.Hash(), result); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return bc, block.NewBlock(child, nil)
}

// The Round-1 vote gate (A13) refuses a header whose requestsHash is not this
// node's executed requests hash of the parent -- sha256("") carried after a
// block that produced requests -- and passes the right value (AM25).
func TestProfile1VoteGateChecksCarriedRequestsHash(t *testing.T) {
	produced := types.HexToHash("0x5bd9d6b1d1b1d1ee50fbd5a1c7cbd1dd5e3cd0b5eeddf5d1bf6b1b9b5c2d3e4f")
	bc, blk := p1PragueGuardChain(t, &p1GateEngine{}, rawdb.ExecutedResult{RequestsHash: produced}, rawdb.EmptyRequestsHash)
	checked, retry, err := bc.CheckDeferredBlock(blk)
	if !checked || retry || !errors.Is(err, ErrProfile1DeferredCheck) || !errors.Is(err, ErrDeferredRequestsHash) {
		t.Fatalf("checked=%v retry=%v err=%v, want a requestsHash refusal", checked, retry, err)
	}

	bc, blk = p1PragueGuardChain(t, &p1GateEngine{}, rawdb.ExecutedResult{RequestsHash: produced}, produced)
	if checked, retry, err := bc.CheckDeferredBlock(blk); !checked || retry || err != nil {
		t.Fatalf("carrying the parent's requests hash: checked=%v retry=%v err=%v", checked, retry, err)
	}

	bc, blk = p1PragueGuardChain(t, &p1GateEngine{}, rawdb.ExecutedResult{RequestsHash: rawdb.EmptyRequestsHash}, rawdb.EmptyRequestsHash)
	if checked, retry, err := bc.CheckDeferredBlock(blk); !checked || retry || err != nil {
		t.Fatalf("the empty case: checked=%v retry=%v err=%v", checked, retry, err)
	}
}

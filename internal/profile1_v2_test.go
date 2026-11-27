// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/blspool"
	"github.com/n42blockchain/N42/lib/rlp"
)

// Vector V2 of the mixed production standard (Profile 1): the roots a
// header commits to. B7 receiptsRoot = keccak(concat(rlp([status,
// cumulativeGas, logs]))), B6 withdrawalsRoot = keccak(concat(rlp([address,
// amount]))) of the A8 rewards, both keccak("") when empty, and B5
// parentBeaconRoot = Blake3(committee evidence(parent.number, parent.hash,
// parent.receiptsRoot)), zero for block 1. Receipts come from gov5 executing
// real transactions; the committee evidence links V5's sealed chain.
//
// N42_PROFILE1_VECTORS_UPDATE=1 rewrites testdata/profile1/vectors/V2;
// N42_PROFILE1_VECTORS_MIRROR=<dir> also writes the files to <dir>/V2.

type v2Log struct {
	Address types.Address `json:"address"`
	Topics  []types.Hash  `json:"topics"`
	Data    hexutil.Bytes `json:"data"`
}

type v2Receipt struct {
	TxIndex           int           `json:"txIndex"`
	Status            uint64        `json:"status"`
	CumulativeGasUsed uint64        `json:"cumulativeGasUsed"`
	Logs              []v2Log       `json:"logs"`
	Encoding          hexutil.Bytes `json:"encoding"`
}

type v2ReceiptCase struct {
	Name     string     `json:"name"`
	Receipts []int      `json:"receiptIndexes"`
	Root     types.Hash `json:"receiptsRoot"`
}

type v2Reward struct {
	Address  types.Address `json:"address"`
	Amount   string        `json:"amountWei"`
	Encoding hexutil.Bytes `json:"encoding"`
}

type v2RewardCase struct {
	Name    string     `json:"name"`
	Rewards []v2Reward `json:"rewards"`
	Root    types.Hash `json:"withdrawalsRoot"`
}

type v2Evidence struct {
	Name               string        `json:"name"`
	ChildNumber        uint64        `json:"childNumber,omitempty"`
	ChildHash          string        `json:"childHash,omitempty"`
	ParentNumber       uint64        `json:"parentNumber"`
	ParentHash         types.Hash    `json:"parentHash"`
	ParentReceiptsRoot types.Hash    `json:"parentReceiptsRoot"`
	ActivePoolSize     int           `json:"activePoolSize"`
	Committee          []int         `json:"committee"`
	SigningMessage     hexutil.Bytes `json:"signingMessage"`
	SignerCount        uint16        `json:"signerCount"`
	SignersPacked      hexutil.Bytes `json:"signersPacked"`
	AggregateSignature hexutil.Bytes `json:"aggregateSignature"`
	Evidence           hexutil.Bytes `json:"evidence"`
	ParentBeaconRoot   types.Hash    `json:"parentBeaconRoot"`
}

type v2File struct {
	Vector       string                                `json:"vector"`
	Standard     string                                `json:"standard"`
	Rules        []string                              `json:"rules"`
	GenesisFile  string                                `json:"genesisFile"`
	AllocAdded   map[types.Address]conf.GenesisAccount `json:"allocAdditions"`
	Block        v3Header                              `json:"receiptsBlock"`
	Txs          []hexutil.Bytes                       `json:"transactions"`
	Receipts     []v2Receipt                           `json:"receipts"`
	ReceiptCases []v2ReceiptCase                       `json:"receiptsRoots"`
	RewardCases  []v2RewardCase                        `json:"withdrawalsRoots"`
	Pool         map[string]any                        `json:"committeePool"`
	Evidence     []v2Evidence                          `json:"committeeEvidence"`
	Notes        []string                              `json:"notes"`
}

// v2LogInitcode emits LOG2(topics 0x11.., 0x22.., data = 32-byte word 0x2a)
// and LOG0 (no data), then stops: the created contract has no code.
func v2LogInitcode() []byte {
	code := []byte{0x60, 0x2a, 0x5f, 0x52, 0x7f}                        // PUSH1 0x2a PUSH0 MSTORE PUSH32
	code = append(code, bytes.Repeat([]byte{0x22}, 32)...)              // topic 2
	code = append(code, 0x7f)                                           // PUSH32
	code = append(code, bytes.Repeat([]byte{0x11}, 32)...)              // topic 1
	code = append(code, 0x60, 0x20, 0x5f, 0xa2, 0x5f, 0x5f, 0xa0, 0x00) // PUSH1 32 PUSH0 LOG2 PUSH0 PUSH0 LOG0 STOP
	return code
}

// TestProfile1VectorV2 generates and checks the V2 roots vector.
func TestProfile1VectorV2(t *testing.T) {
	_, sender := v3Key(t)
	alloc := map[types.Address]conf.GenesisAccount{sender: {Balance: v3SenderBalanceWei}}
	r, _ := newV3Runner(t, alloc)
	cfg := r.cfg
	faucet := *cfg.HotStuff.DevFaucetAddress

	out := v2File{
		Vector:   "V2",
		Standard: "N42 mixed production standard profile 1; vector V2 (roots: B5 parentBeaconRoot / committee evidence, B6 withdrawalsRoot / rewards, B7 receiptsRoot)",
		Rules: []string{
			"B7: receiptsRoot = keccak256(rlp([status, cumulativeGasUsed, logs]) of every receipt, concatenated in order); keccak256(\"\") = 0xc5d24601...a470 when there are none. log = rlp([address, topics, data]). No type byte, no bloom.",
			"A10: header N carries the receiptsRoot of block N-1's execution (k = 1).",
			"B6: withdrawalsRoot = keccak256(rlp([address, amount in wei]) of every reward, concatenated in body order); keccak256(\"\") when there are none. Profile 1's A8 rewards: [coinbase, devFaucetAddress], devBlockReward each.",
			"B5: parentBeaconRoot of block N >= 2 = Blake3-256(evidence), evidence = gov5's ConsensusEvidence encoding of the all-simulated committee signature over (N-1, hash of N-1) with the mobile section's receiptsRoot = header N-1's receiptsRoot; block 1 carries zero.",
			"Committee: pool keys from blspool.DeriveKeys(seedHex, poolSize); active = committeeSize + (poolSize - committeeSize) * number / rampBlocks (poolSize from rampBlocks on); committee = blspool.Committee(number, hash, active, committeeSize); signature = BLS (POP DST) over number u64 LE || hash, aggregated as the scalar sum of the members' secret keys.",
		},
		GenesisFile: "../../" + profile1StandardFile,
		AllocAdded:  alloc,
		Notes: []string{
			"Generated by gov5 TestProfile1VectorV2 (internal/profile1_v2_test.go): receipts by gov5's ApplyTransaction on the standard genesis plus the V3 test sender, roots by ReceiptsRootFor / hash.DeriveSha, evidence by blspool.Pool.BuildSimulatedCE and ConsensusEvidence.Marshal/BeaconRoot.",
			"committeeEvidence rows named V5-block-N are the parentBeaconRoot of V5's (header_validity.json) block N, checked against that header; the ramp rows use made-up parents.",
		},
	}

	// B7: receipts from one executed block.
	logInit := v2LogInitcode()
	txs := []*transaction.Transaction{
		r.sign(nil, uint256.NewInt(0), 1_000_000, logInit),                // LOG2 + LOG0
		r.sign(nil, uint256.NewInt(0), 100_000, []byte{0x5f, 0x5f, 0xfd}), // REVERT: status 0
		r.sign(&faucet, uint256.NewInt(1), 21_000, nil),                   // plain transfer
	}
	b := r.run("receipts", "", types.Hash{}, txs)
	out.Block, out.Txs = b.Header, b.Txs
	receipts := r.lastReceipts
	if len(receipts) != 3 || receipts[0].Status != 1 || len(receipts[0].Logs) != 2 || receipts[1].Status != 0 || receipts[2].Status != 1 {
		t.Fatalf("receipts: %+v", receipts)
	}
	for i, rc := range receipts {
		var buf bytes.Buffer
		block.Receipts(receipts).EncodeIndex(i, &buf)
		logs := []v2Log{}
		for _, l := range rc.Logs {
			logs = append(logs, v2Log{Address: l.Address, Topics: append([]types.Hash{}, l.Topics...), Data: append(hexutil.Bytes{}, l.Data...)})
		}
		out.Receipts = append(out.Receipts, v2Receipt{TxIndex: i, Status: rc.Status, CumulativeGasUsed: rc.CumulativeGasUsed, Logs: logs, Encoding: buf.Bytes()})
	}
	number := b.Header.Number
	addRC := func(name string, idx ...int) {
		var rs []*block.Receipt
		concat := []byte{}
		for _, i := range idx {
			rs = append(rs, receipts[i])
			concat = append(concat, out.Receipts[i].Encoding...)
		}
		root := ReceiptsRootFor(cfg, number, rs)
		if root != crypto.Keccak256Hash(concat) {
			t.Fatalf("%s: root %s is not keccak of the concatenated encodings", name, root)
		}
		if idx == nil {
			idx = []int{}
		}
		out.ReceiptCases = append(out.ReceiptCases, v2ReceiptCase{Name: name, Receipts: idx, Root: root})
	}
	addRC("empty")
	addRC("logs (LOG2 + LOG0)", 0)
	addRC("reverted (status 0)", 1)
	addRC("block: logs, reverted, transfer", 0, 1, 2)
	if out.ReceiptCases[0].Root != hash.NilHash {
		t.Fatal("empty receipts root is not keccak(\"\")")
	}

	// B6: reward lists.
	reward := cfg.HotStuff.DevBlockReward.Amount()
	coinbase := types.HexToAddress(cfg.HotStuff.Validators[0].Address)
	maxGwei := new(uint256.Int).Mul(uint256.NewInt(^uint64(0)), uint256.NewInt(1_000_000_000))
	addRW := func(name string, rs block.Rewards) {
		c := v2RewardCase{Name: name, Rewards: []v2Reward{}, Root: hash.DeriveSha(rs)}
		concat := []byte{}
		for i, rw := range rs {
			var buf bytes.Buffer
			rs.EncodeIndex(i, &buf)
			concat = append(concat, buf.Bytes()...)
			c.Rewards = append(c.Rewards, v2Reward{Address: rw.Address, Amount: rw.Amount.Dec(), Encoding: buf.Bytes()})
		}
		if c.Root != crypto.Keccak256Hash(concat) {
			t.Fatalf("%s: root is not keccak of the concatenated encodings", name)
		}
		out.RewardCases = append(out.RewardCases, c)
	}
	addRW("empty", block.Rewards{})
	addRW("A8 pair: coinbase (validator 0), faucet", block.Rewards{{Address: coinbase, Amount: reward}, {Address: faucet, Amount: reward}})
	addRW("A8 pair reversed (order matters)", block.Rewards{{Address: faucet, Amount: reward}, {Address: coinbase, Amount: reward}})
	addRW("single reward", block.Rewards{{Address: coinbase, Amount: reward}})
	addRW("1 gwei and u64::MAX gwei", block.Rewards{{Address: coinbase, Amount: uint256.NewInt(1_000_000_000)}, {Address: faucet, Amount: maxGwei}})
	addRW("zero amount", block.Rewards{{Address: coinbase, Amount: uint256.NewInt(0)}})
	if out.RewardCases[0].Root != hash.NilHash || out.RewardCases[1].Root == out.RewardCases[2].Root {
		t.Fatal("reward roots: empty is not keccak(\"\") or order does not matter")
	}

	// B5: committee evidence. V5's chain (V1 genesis_8s, same committee pool).
	cp := cfg.HotStuff.CommitteePool
	seed, err := hexutil.Decode(cp.SeedHex)
	if err != nil || len(seed) != 32 {
		t.Fatalf("committee pool seed %q: %v", cp.SeedHex, err)
	}
	pool, err := blspool.NewSimulatedPool(blspool.PoolConfig{
		Seed: [32]byte(seed), PoolSize: cp.PoolSize, CommitteeSize: cp.CommitteeSize, RampBlocks: cp.RampBlocks,
	})
	if err != nil {
		t.Fatal(err)
	}
	out.Pool = map[string]any{"seedHex": cp.SeedHex, "poolSize": cp.PoolSize, "committeeSize": cp.CommitteeSize, "rampBlocks": cp.RampBlocks,
		"source": "standard genesis config.hotstuff.committeePool (V1 genesis_8s has the same)"}
	addCE := func(name string, childNum uint64, childHash string, parentNum uint64, parentHash, parentRR types.Hash) v2Evidence {
		ce, err := pool.BuildSimulatedCE(parentNum, parentHash, parentRR)
		if err != nil {
			t.Fatal(err)
		}
		committee := append([]int{}, pool.CommitteeAt(parentNum, parentHash)...)
		e := v2Evidence{Name: name, ChildNumber: childNum, ChildHash: childHash, ParentNumber: parentNum, ParentHash: parentHash,
			ParentReceiptsRoot: parentRR, ActivePoolSize: pool.ActiveSize(parentNum), Committee: committee,
			SigningMessage: blspool.SigningMessage(parentNum, parentHash), SignerCount: ce.SignerCount, SignersPacked: ce.SignersPacked,
			AggregateSignature: ce.AggregateSignature[:], Evidence: ce.Marshal(), ParentBeaconRoot: ce.BeaconRoot()}
		out.Evidence = append(out.Evidence, e)
		return e
	}
	raw, err := os.ReadFile(filepath.Join(profile1VectorsDir, "V5", "header_validity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v5 struct {
		Blocks []struct {
			RLP string `json:"rlp"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(raw, &v5); err != nil {
		t.Fatal(err)
	}
	var headers []*block.Header
	for _, bl := range v5.Blocks {
		enc, err := hexutil.Decode(bl.RLP)
		if err != nil {
			t.Fatal(err)
		}
		h := new(block.Header)
		if err := rlp.DecodeBytes(enc, h); err != nil {
			t.Fatal(err)
		}
		headers = append(headers, h)
	}
	if len(headers) < 3 || headers[0].ParentBeaconRoot == nil || *headers[0].ParentBeaconRoot != (types.Hash{}) {
		t.Fatal("V5 block 1 must carry a zero parentBeaconRoot")
	}
	for i := 1; i < len(headers); i++ {
		p, c := headers[i-1], headers[i]
		e := addCE("V5-block-"+c.Number.Dec(), c.Number.Uint64(), c.Hash().Hex(), p.Number.Uint64(), p.Hash(), p.ReceiptHash)
		if c.ParentBeaconRoot == nil || *c.ParentBeaconRoot != e.ParentBeaconRoot {
			t.Fatalf("V5 block %d: parentBeaconRoot %v, evidence gives %s", c.Number.Uint64(), c.ParentBeaconRoot, e.ParentBeaconRoot)
		}
	}
	// Ramp: the active pool grows with the parent number.
	for _, n := range []uint64{cp.RampBlocks / 2, cp.RampBlocks - 1, cp.RampBlocks} {
		ph := crypto.Keccak256Hash([]byte("n42-profile1-v2-parent"), uint256.NewInt(n).Bytes())
		addCE("ramp", 0, "", n, ph, hash.NilHash)
	}

	writeProfile1Vector(t, "V2", "roots.json", out)
	mirrorProfile1File(t, "V2", "README.md")
}

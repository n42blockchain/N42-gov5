// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal/blspool"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// Vector V5 of the mixed production standard (Profile 1): header validity.
// A sealed chain on the V1 genesis (8 s slot) plus one funded test sender,
// whose blocks carry real transactions, so that every header's deferred
// fields (A10, k = 1) are the parent's execution: in particular gasUsed is
// the EIP-8037 block figure max(block regular gas, block state gas), regular
// gas before refunds (B13), and requestsHash the parent's EIP-7685 requests
// hash (AM25): block 7 calls the EIP-7002 and EIP-7251 contracts, block 8
// carries the hash of the two requests. Blocks 1 and 2 have equal timestamps (B4). Each
// header passes gov5's import verification and its deferred-field check;
// each refusal breaks one rule and is refused by gov5.
//
// N42_PROFILE1_VECTORS_UPDATE=1 rewrites testdata/profile1/vectors/V5;
// N42_PROFILE1_VECTORS_MIRROR=<dir> also writes the files to <dir>/V5.

type v5Executed struct {
	StateRoot     types.Hash      `json:"stateRoot"`
	ReceiptsRoot  types.Hash      `json:"receiptsRoot"`
	LogsBloom     hexutil.Bytes   `json:"logsBloom"`
	RegularGas    uint64          `json:"blockRegularGas"`
	StateGas      uint64          `json:"blockStateGas"`
	ReceiptsGas   uint64          `json:"receiptsCumulativeGasUsed"`
	GasUsed       uint64          `json:"gasUsed"`
	GasUsedSource string          `json:"gasUsedIs"`
	Requests      []hexutil.Bytes `json:"requests"`
	RequestsHash  types.Hash      `json:"requestsHash"`
}

type v5Block struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	View        uint64          `json:"view"`
	Signer      int             `json:"signerIndex"`
	Header      json.RawMessage `json:"header"`
	RLP         string          `json:"rlp"`
	Hash        string          `json:"hash"`
	Txs         []hexutil.Bytes `json:"transactions"`
	Executed    v5Executed      `json:"executed"`
}

type v5Refusal struct {
	Name     string `json:"name"`
	Rule     string `json:"rule"`
	Check    string `json:"check"` // import | vote | deferred
	Now      uint64 `json:"nowUnix,omitempty"`
	RLP      string `json:"rlp"`
	Hash     string `json:"hash"`
	Error    string `json:"expectedError,omitempty"`
	Message  string `json:"gov5Message"`
	Mutation string `json:"mutation"`
}

type v5File struct {
	Vector       string                                `json:"vector"`
	Standard     string                                `json:"standard"`
	ChainID      uint64                                `json:"chainId"`
	GenesisFile  string                                `json:"genesisFile"`
	AllocAdded   map[types.Address]conf.GenesisAccount `json:"allocAdditions"`
	GenesisHash  string                                `json:"genesisHash"`
	Genesis      v1HeaderVector                        `json:"genesis"`
	GasUsedRule  string                                `json:"gasUsedRule"`
	RequestsRule string                                `json:"requestsHashRule"`
	Blocks       []v5Block                             `json:"blocks"`
	Refusals     []v5Refusal                           `json:"refusals"`
	Notes        []string                              `json:"notes"`
}

// TestProfile1VectorV5 builds, verifies and pins vector V5.
func TestProfile1VectorV5(t *testing.T) {
	saved := block.UseEthereumTxRoot
	block.UseEthereumTxRoot = true
	defer func() { block.UseEthereumTxRoot = saved }()

	_, sender := v3Key(t)
	alloc := map[types.Address]conf.GenesisAccount{sender: {Balance: v3SenderBalanceWei}}
	g := new(conf.Genesis)
	if err := json.Unmarshal(v1GenesisJSON(t), g); err != nil {
		t.Fatalf("8 s profile-1 genesis refused: %v", err)
	}
	for a, acct := range alloc {
		g.Alloc[a] = acct
	}
	cfg := g.Config
	r, _ := newV3RunnerFrom(t, g)
	genesis := r.parent
	sks := v1SecretKeys(t)
	vs := profile1TestValidators(t)
	addr := func(i int) types.Address { return types.HexToAddress(vs[i].Address) }
	identity := hotstuff.H2V4ChainIdentity{ChainID: cfg.ChainID.Uint64(), GenesisHash: genesis.Hash()}
	hs := hotstuff.New(cfg.HotStuff, cfg)
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
	hs.SetCommitteeEvidence(pool, nil)
	chain := &v1Chain{cfg: cfg, headers: []*block.Header{genesis}}
	leader := func(view uint64) int { return int(view % uint64(len(sks))) }
	reward := cfg.HotStuff.DevBlockReward.Amount()

	extraOf := func(view uint64, qc *hotstuff.QuorumCertificate) []byte {
		extra := append([]byte("N42H"), make([]byte, 8)...)
		for i := 0; i < 8; i++ {
			extra[4+i] = byte(view >> (8 * i))
		}
		if qc != nil {
			b, err := hotstuff.EncodeQC(qc)
			if err != nil {
				t.Fatal(err)
			}
			extra = append(extra, b...)
		}
		return append(extra, make([]byte, 96)...)
	}
	seal := func(h *block.Header) *block.Header {
		v, err := hotstuff.ExtractViewFromExtra(h.Extra)
		if err != nil {
			t.Fatal(err)
		}
		h.ResetHashCache()
		digest := hs.SealHash(h)
		copy(h.Extra[len(h.Extra)-96:], sks[leader(v)].Sign(digest[:]).Marshal())
		h.ResetHashCache()
		return h
	}
	commitQC := func(view uint64, blockHash types.Hash) *hotstuff.QuorumCertificate {
		msg := hotstuff.H2V4CommitSigningMessage(identity, view, blockHash, types.Hash{})
		bitmap := make([]bool, len(sks))
		var sigs []blscommon.Signature
		for _, i := range []int{0, 1, 2} {
			bitmap[i] = true
			sigs = append(sigs, sks[i].Sign(msg))
		}
		return &hotstuff.QuorumCertificate{View: view, BlockHash: blockHash,
			AggregateSignature: bls.AggregateSignatures(sigs).Marshal(), Signers: bitmap}
	}
	// child is the unsealed header of view `view` on parent: deferred fields
	// = carried (the parent's execution), body = txs.
	child := func(parent *block.Header, view, dt uint64, carried rawdb.ExecutedResult, txs []*transaction.Transaction) *block.Header {
		n := parent.Number.Uint64() + 1
		coinbase := addr(leader(view))
		rewards := block.Rewards{{Address: coinbase, Amount: reward}, {Address: *cfg.HotStuff.DevFaucetAddress, Amount: reward}}
		wh := hash.DeriveSha(rewards)
		baseFee, _ := uint256.FromBig(misc.CalcBaseFee(cfg, parent))
		zero, slot, rq := uint64(0), n, carried.RequestsHash
		pbr := new(types.Hash)
		if parent.Number.Uint64() > 0 {
			ce, err := pool.BuildSimulatedCE(parent.Number.Uint64(), parent.Hash(), parent.ReceiptHash)
			if err != nil {
				t.Fatal(err)
			}
			root := ce.BeaconRoot()
			pbr = &root
		}
		var qc *hotstuff.QuorumCertificate
		if parent.Number.Uint64() > 0 {
			pv, err := hotstuff.ExtractViewFromExtra(parent.Extra)
			if err != nil {
				t.Fatal(err)
			}
			qc = commitQC(pv, parent.Hash())
		}
		return &block.Header{
			ParentHash: parent.Hash(), Coinbase: coinbase, Root: carried.Root,
			TxHash: block.TxRoot(txs), ReceiptHash: carried.ReceiptHash, Bloom: carried.Bloom,
			Difficulty: uint256.NewInt(0), Number: uint256.NewInt(n), GasLimit: cfg.HotStuff.GasLimit,
			GasUsed: carried.GasUsed, Time: parent.Time + dt, Extra: extraOf(view, qc), BaseFee: baseFee,
			WithdrawalsHash: &wh, BlobGasUsed: &zero, ExcessBlobGas: &zero,
			ParentBeaconRoot: pbr, RequestsHash: &rq, SlotNumber: &slot,
			MobileRegistryRoot: new(types.Hash),
		}
	}
	verify := func(name string, h, parent *block.Header) {
		t.Helper()
		if err := hs.VerifyHeader(chain, h, true); err != nil {
			t.Fatalf("%s refused on import: %v", name, err)
		}
		if err := checkDeferredHeader(cfg, r.tx, h, parent); err != nil {
			t.Fatalf("%s: deferred fields refused: %v", name, err)
		}
		if err := hs.StaticHeaderCheck(chain, h, time.Unix(int64(h.Time), 0)); err != nil {
			t.Fatalf("%s refused at vote time: %v", name, err)
		}
	}

	out := v5File{
		Vector:       "V5",
		Standard:     "N42 mixed production standard profile 1; vector V5 (header validity: B4, B8, B9, B13, B14, A23, AM25)",
		ChainID:      cfg.ChainID.Uint64(),
		GenesisFile:  "../V1/" + v1Genesis,
		AllocAdded:   alloc,
		GenesisHash:  genesis.Hash().Hex(),
		Genesis:      v1Encode(t, "genesis", genesis),
		GasUsedRule:  "Header N carries gasUsed of block N-1 (A10, k = 1). A block's gasUsed (EIP-8037, B13) = max(sum of tx regular gas, sum of tx state gas); tx regular gas = max(gas used before refunds - tx state gas, calldata floor); refunds do not reduce it. Receipts keep cumulativeGasUsed = sum of tx gas used after refunds (state gas included).",
		RequestsRule: "Header N carries requestsHash of block N-1 (AM25, A10, k = 1); genesis and block 1 carry sha256(\"\") = 0xe3b0c442...b855. A block's requests are produced as on Ethereum Prague (EIP-6110 deposit log scan, then the EIP-7002 and EIP-7251 end-of-block system calls); requests_hash (EIP-7685) = sha256(sha256(r_1) || ... || sha256(r_n)) over the non-empty requests r_i = type || data in type order, sha256(\"\") when there are none. The native chain ignores the requests' content.",
		Notes: []string{
			"The genesis is V1's genesis_8s.json plus allocAdditions (the V3 test sender, key = keccak256(\"" + v3SenderSeed + "\")).",
			"Blocks are built like V1's (extraData N42H || view || CommitQC of the parent by validators 0, 1, 2 || seal, leader(v) = v mod 4, withdrawalsRoot of the A8 rewards, parentBeaconRoot = Blake3 of the parent's committee evidence from block 2); V1 has the per-field rules.",
			"executed = the block's own result, stored by gov5 and carried by the next header: stateRoot (QMDB), receiptsRoot, logsBloom, gasUsed. blockRegularGas, blockStateGas and receiptsCumulativeGasUsed show where gasUsed comes from.",
			"Blocks 1 and 2 have equal timestamps (B4: parent.time <= time); both run the EIP-4788 system call with the same timestamp, so block 2's root slot overwrites block 1's in the same ring-buffer index; the effect is in block 2's executed stateRoot, carried by block 3.",
			"check: import = header verification on import (gov5 VerifyHeader); vote = only before the Round-1 vote at nowUnix; deferred = the deferred fields against the parent's stored result (gov5 checkDeferredHeader, on import and in the vote gate).",
			"AM25: block 7 calls the EIP-7002 contract 0x00000961...7002 (validator pubkey 0xa1 x 48 || amount 1000000000 gwei, 8 bytes big-endian; 1 wei fee) and the EIP-7251 contract 0x0000BBdD...7251 (source pubkey 0xa1 x 48 || target pubkey 0xb2 x 48; 1 wei fee). Its end-of-block calls dequeue both: executed.requests (type 0x01: source address || pubkey || amount, 8 bytes little-endian as the contract emits it; type 0x02: source address || source pubkey || target pubkey) and executed.requestsHash. Block 8 carries that hash; its own queues are empty, so it produces sha256(\"\").",
		},
	}

	contract := crypto.CreateAddress(sender, 0)
	fresh := types.HexToAddress("0x00000000000000000000000000000000000000cc")
	faucet := *cfg.HotStuff.DevFaucetAddress
	type step struct {
		name, desc string
		dt         uint64
		txs        func() []*transaction.Transaction
		source     string
	}
	steps := []step{
		{"contract-create", "Deploys V3's store contract (an account creation and two SSTOREs from zero): block state gas above regular gas.", v1SlotSecs,
			func() []*transaction.Transaction {
				return []*transaction.Transaction{r.sign(nil, uint256.NewInt(0), 10_000_000, v3StoreInitcode)}
			}, "blockStateGas"},
		{"sstore-clear-equal-timestamp", "Same timestamp as block 1 (B4 allows parent.time == time). Clears the contract's slot 1 (1 -> 0): a refund, so receiptsCumulativeGasUsed is below blockRegularGas; gasUsed ignores the refund.", 0,
			func() []*transaction.Transaction {
				return []*transaction.Transaction{r.sign(&contract, uint256.NewInt(0), 10_000_000, storeCall(1, 0))}
			}, "blockRegularGas"},
		{"new-account-transfer", "1 wei to 0x..cc, which does not exist: the new account's state gas is above the 21000 regular gas.", v1SlotSecs,
			func() []*transaction.Transaction {
				return []*transaction.Transaction{r.sign(&fresh, uint256.NewInt(1), 10_000_000, nil)}
			}, "blockStateGas"},
		{"plain-transfer", "1 wei to the existing faucet account: regular = receipts = 21000, state 0.", v1SlotSecs,
			func() []*transaction.Transaction {
				return []*transaction.Transaction{r.sign(&faucet, uint256.NewInt(1), 10_000_000, nil)}
			}, "blockRegularGas"},
		{"idle", "No transactions: gasUsed 0.", v1SlotSecs, func() []*transaction.Transaction { return nil }, "zero"},
		{"carries-idle", "No transactions; carries block 5's gasUsed 0.", v1SlotSecs, func() []*transaction.Transaction { return nil }, "zero"},
		{"withdrawal-and-consolidation-requests", "A fee-paying EIP-7002 withdrawal request and a fee-paying EIP-7251 consolidation request (AM25): the end-of-block system calls dequeue both; the block is valid and its requestsHash is not sha256(\"\").", v1SlotSecs,
			func() []*transaction.Transaction { return p1RequestTxs(r) }, "auto"},
		{"carries-requests", "No transactions; carries block 7's requestsHash. Its own queues are empty: executed requestsHash sha256(\"\").", v1SlotSecs, func() []*transaction.Transaction { return nil }, "zero"},
	}

	carried := rawdb.ExecutedResult{Root: genesis.Root, ReceiptHash: genesis.ReceiptHash, Bloom: genesis.Bloom, GasUsed: genesis.GasUsed, RequestsHash: *genesis.RequestsHash}
	parent := genesis
	var headers []*block.Header
	var bodies [][]*transaction.Transaction
	var results []rawdb.ExecutedResult
	var receiptsGas []uint64
	for i, s := range steps {
		view := uint64(i + 1)
		txs := s.txs()
		h := seal(child(parent, view, s.dt, carried, txs))
		verify(fmt.Sprintf("block %d (%s)", view, s.name), h, parent)
		chain.headers = append(chain.headers, h)
		root, _, _, raw := r.execute(s.name, h, txs)
		exec, st := r.lastGP.AmsterdamCounters()
		res := ExecutedResultFor(cfg, h.Number.Uint64(), root, r.lastReceipts, HeaderGasUsed(cfg, h, r.lastGP, r.lastUsedGas), ExecutionRequestsHash(r.lastRequests))
		if res.GasUsed != max(exec, st) {
			t.Fatalf("%s: gasUsed %d, want max(%d, %d)", s.name, res.GasUsed, exec, st)
		}
		if s.source == "auto" {
			s.source = "blockRegularGas"
			if st > exec {
				s.source = "blockStateGas"
			}
		}
		switch s.source {
		case "blockStateGas":
			if st <= exec {
				t.Fatalf("%s: state gas %d not above regular %d", s.name, st, exec)
			}
		case "blockRegularGas":
			if exec < st || exec == 0 {
				t.Fatalf("%s: regular gas %d, state %d", s.name, exec, st)
			}
		case "zero":
			if res.GasUsed != 0 {
				t.Fatalf("%s: gasUsed %d", s.name, res.GasUsed)
			}
		}
		if err := rawdb.WriteExecutedResult(r.tx, h.Hash(), res); err != nil {
			t.Fatal(err)
		}
		hv := v1Encode(t, s.name, h)
		out.Blocks = append(out.Blocks, v5Block{
			Name: s.name, Description: s.desc, View: view, Signer: leader(view),
			Header: hv.Header, RLP: hv.RLP, Hash: hv.Hash, Txs: raw,
			Executed: v5Executed{StateRoot: res.Root, ReceiptsRoot: res.ReceiptHash, LogsBloom: res.Bloom[:],
				RegularGas: exec, StateGas: st, ReceiptsGas: r.lastUsedGas, GasUsed: res.GasUsed, GasUsedSource: s.source,
				Requests: append([]hexutil.Bytes{}, r.lastRequests...), RequestsHash: res.RequestsHash},
		})
		headers = append(headers, h)
		bodies = append(bodies, txs)
		results = append(results, res)
		receiptsGas = append(receiptsGas, r.lastUsedGas)
		carried, parent = res, h
	}
	if headers[1].Time != headers[0].Time {
		t.Fatal("blocks 1 and 2 must have equal timestamps")
	}
	if receiptsGas[1] >= results[1].GasUsed || receiptsGas[0] == results[0].GasUsed {
		t.Fatalf("refund/state-gas blocks do not separate gasUsed from the receipts: %v vs %+v", receiptsGas, results)
	}
	// AM25: only block 7 produces requests -- the two of p1RequestTxs --
	// and block 8 carries their hash.
	for i, res := range results {
		if want := i == 6; (res.RequestsHash != rawdb.EmptyRequestsHash) != want {
			t.Fatalf("block %d: requestsHash %s", i+1, res.RequestsHash.Hex())
		}
	}
	if wantReq := p1WantRequests(sender); len(out.Blocks[6].Executed.Requests) != 2 ||
		!bytes.Equal(out.Blocks[6].Executed.Requests[0], wantReq[0]) || !bytes.Equal(out.Blocks[6].Executed.Requests[1], wantReq[1]) {
		t.Fatalf("block 7 requests %x", out.Blocks[6].Executed.Requests)
	}
	if *headers[7].RequestsHash != results[6].RequestsHash {
		t.Fatal("block 8 does not carry block 7's requestsHash")
	}

	// Refusals: a header of the chain rebuilt with one rule broken and
	// resealed by its view's leader.
	rebuild := func(k int, mut func(*block.Header)) *block.Header {
		var par *block.Header
		car := rawdb.ExecutedResult{Root: genesis.Root, ReceiptHash: genesis.ReceiptHash, Bloom: genesis.Bloom, RequestsHash: *genesis.RequestsHash}
		if k == 0 {
			par = genesis
		} else {
			par, car = headers[k-1], results[k-1]
		}
		h := child(par, uint64(k+1), steps[k].dt, car, bodies[k])
		mut(h)
		return seal(h)
	}
	errName := map[error]string{
		hotstuff.ErrProfile1TimeBeforeParent: "ErrProfile1TimeBeforeParent",
		hotstuff.ErrProfile1TimeFuture:       "ErrProfile1TimeFuture",
		hotstuff.ErrProfile1BlobGas:          "ErrProfile1BlobGas",
		hotstuff.ErrProfile1BaseFee:          "ErrProfile1BaseFee",
		hotstuff.ErrProfile1GasLimit:         "ErrProfile1GasLimit",
		hotstuff.ErrProfile1RequestsHash:     "ErrProfile1RequestsHash",
		ErrDeferredRequestsHash:              "ErrDeferredRequestsHash",
	}
	add := func(name, rule, check, mutation string, now uint64, h *block.Header, err, want error, contains string) {
		t.Helper()
		switch {
		case err == nil:
			t.Fatalf("%s: accepted", name)
		case want != nil && !errors.Is(err, want):
			t.Fatalf("%s: got %v, want %v", name, err, want)
		case want == nil && !strings.Contains(err.Error(), contains):
			t.Fatalf("%s: got %v, want %q", name, err, contains)
		}
		raw, rerr := rlp.EncodeToBytes(h)
		if rerr != nil {
			t.Fatal(rerr)
		}
		out.Refusals = append(out.Refusals, v5Refusal{Name: name, Rule: rule, Check: check, Now: now,
			RLP: hexutil.Encode(raw), Hash: h.Hash().Hex(), Error: errName[want], Message: err.Error(), Mutation: mutation})
	}
	imp := func(name, rule, mutation string, k int, mut func(*block.Header), want error, contains string) {
		h := rebuild(k, mut)
		add(name, rule, "import", mutation, 0, h, hs.VerifyHeader(chain, h, true), want, contains)
	}
	defWith := func(name, rule, mutation string, k int, mut func(*block.Header), want error, contains string) {
		h := rebuild(k, mut)
		if err := hs.VerifyHeader(chain, h, true); err != nil {
			t.Fatalf("%s: refused on import, want the deferred check: %v", name, err)
		}
		par := genesis
		if k > 0 {
			par = headers[k-1]
		}
		add(name, rule, "deferred", mutation, 0, h, checkDeferredHeader(cfg, r.tx, h, par), want, contains)
	}
	def := func(name, rule, mutation string, k int, mut func(*block.Header)) {
		defWith(name, rule, mutation, k, mut, nil, "carries parent gas used")
	}
	one := uint64(1)
	def("gasUsed = receipts' cumulative gas of a state-gas block", "B13", "block 2 carrying block 1's receiptsCumulativeGasUsed instead of max(regular, state)", 1,
		func(h *block.Header) { h.GasUsed = receiptsGas[0] })
	def("gasUsed = receipts' cumulative gas of a refund block", "B13", "block 3 carrying block 2's receiptsCumulativeGasUsed (after the refund) instead of its regular gas", 2,
		func(h *block.Header) { h.GasUsed = receiptsGas[1] })
	def("gasUsed = regular + state gas", "B13", "block 4 carrying block 3's blockRegularGas + blockStateGas", 3,
		func(h *block.Header) { h.GasUsed = out.Blocks[2].Executed.RegularGas + out.Blocks[2].Executed.StateGas })
	imp("gasUsed above the parent's gasLimit", "B13", "block 3 with gasUsed = gasLimit + 1", 2,
		func(h *block.Header) { h.GasUsed = h.GasLimit + 1 }, nil, "exceeds gasLimit")
	imp("timestamp before parent", "B4", "block 2 with time = block 1 time - 1", 1,
		func(h *block.Header) { h.Time = headers[0].Time - 1 }, hotstuff.ErrProfile1TimeBeforeParent, "")
	imp("blobGasUsed 1", "B9", "block 3 with blobGasUsed = 1", 2,
		func(h *block.Header) { h.BlobGasUsed = &one }, hotstuff.ErrProfile1BlobGas, "")
	imp("excessBlobGas 1", "B9", "block 3 with excessBlobGas = 1", 2,
		func(h *block.Header) { h.ExcessBlobGas = &one }, hotstuff.ErrProfile1BlobGas, "")
	imp("baseFee from the receipts' gas", "B14", "block 3's baseFee computed from block 2 with gasUsed = block 1's receiptsCumulativeGasUsed", 2,
		func(h *block.Header) {
			p := *headers[1]
			p.GasUsed = receiptsGas[0]
			bf, _ := uint256.FromBig(misc.CalcBaseFee(cfg, &p))
			if bf.Eq(h.BaseFee) {
				bf.AddUint64(bf, 1) // the two parents round to the same fee
			}
			h.BaseFee = bf
		}, hotstuff.ErrProfile1BaseFee, "")
	imp("baseFee off by one", "B14", "block 4 with baseFee = EIP-1559 value + 1", 3,
		func(h *block.Header) { h.BaseFee = new(uint256.Int).AddUint64(h.BaseFee, 1) }, hotstuff.ErrProfile1BaseFee, "")
	imp("gasLimit not hotstuff.gasLimit", "A23", "block 3 with gasLimit = 480000000 - 1", 2,
		func(h *block.Header) { h.GasLimit-- }, hotstuff.ErrProfile1GasLimit, "")
	ahead := headers[2]
	now := ahead.Time - 3
	add("timestamp beyond floor(now)+2", "B4", "vote", "block 3 checked with now = time - 3 (accepted on import)", now,
		ahead, hs.StaticHeaderCheck(chain, ahead, time.Unix(int64(now), 0)), hotstuff.ErrProfile1TimeFuture, "")
	if err := hs.StaticHeaderCheck(chain, headers[1], time.Unix(int64(headers[1].Time), 0)); err != nil {
		t.Fatalf("block 2 (equal timestamp) refused at vote time: %v", err)
	}

	// AM25 (appended after the earlier refusals, whose order is published).
	defWith("requestsHash sha256(\"\") after a block that produced requests", "AM25", "block 8 carrying sha256(\"\") instead of block 7's requestsHash", 7,
		func(h *block.Header) { e := rawdb.EmptyRequestsHash; h.RequestsHash = &e }, ErrDeferredRequestsHash, "")
	defWith("requestsHash of the block's own execution", "AM25", "block 7 carrying its own requestsHash instead of block 6's sha256(\"\")", 6,
		func(h *block.Header) { e := results[6].RequestsHash; h.RequestsHash = &e }, ErrDeferredRequestsHash, "")
	defWith("requestsHash empty-trie root", "B8", "block 3 carrying requestsHash = 0x56e81f17...b421 (AM3's value) instead of block 2's sha256(\"\")", 2,
		func(h *block.Header) { e := hash.EmptyRootHash; h.RequestsHash = &e }, ErrDeferredRequestsHash, "")
	imp("requestsHash absent", "B8", "block 8 with requestsHash = nil (RLP stops before element 21)", 7,
		func(h *block.Header) { h.RequestsHash = nil }, hotstuff.ErrProfile1RequestsHash, "")

	writeProfile1Vector(t, "V5", "header_validity.json", out)
	mirrorProfile1File(t, "V5", "README.md")
}

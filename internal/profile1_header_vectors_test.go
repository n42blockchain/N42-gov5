// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Vector V1 of the N42 mixed production standard (profile 1): the header
// profile. gov5's code is the bytes reference: this test builds a profile-1
// test genesis with the AM22 values (8 s slot), two sealed headers (block 1
// without a QC, block 2 with the CommitQC of block 1), and one refused header
// per header rule, checks every one through gov5's verifier, and keeps the
// result equal to internal/testdata/profile1/vectors/V1.
//
// N42_UPDATE_PROFILE1_FIXTURE=1 rewrites the gov5 copy; N42_PROFILE1_V1_OUT
// names a further directory to write the same files to.

package internal

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal/blspool"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/params"
)

const (
	v1Dir      = "testdata/profile1/vectors/V1"
	v1File     = "header_profile_v1.json"
	v1Genesis  = "genesis_8s.json"
	v1Readme   = "README.md"
	v1SlotSecs = 8
)

// v1Chain is a header store for the verifier: genesis and the sealed blocks.
type v1Chain struct {
	cfg     *params.ChainConfig
	headers []*block.Header
}

func (c *v1Chain) Config() *params.ChainConfig                      { return c.cfg }
func (c *v1Chain) CurrentBlock() block.IBlock                       { return nil }
func (c *v1Chain) GetHeader(types.Hash, *uint256.Int) block.IHeader { return nil }
func (c *v1Chain) GetTd(types.Hash, *uint256.Int) *uint256.Int      { return nil }
func (c *v1Chain) GetBlockByNumber(*uint256.Int) (block.IBlock, error) {
	return nil, nil
}
func (c *v1Chain) GetHeaderByNumber(n *uint256.Int) block.IHeader {
	if n == nil || n.Uint64() >= uint64(len(c.headers)) {
		return nil
	}
	return c.headers[n.Uint64()]
}
func (c *v1Chain) GetHeaderByHash(h types.Hash) (block.IHeader, error) {
	for _, hd := range c.headers {
		if hd.Hash() == h {
			return hd, nil
		}
	}
	return nil, nil
}

// v1SecretKeys are the secret keys of profile1TestValidators, in list order.
func v1SecretKeys(t *testing.T) []blscommon.SecretKey {
	t.Helper()
	var out []blscommon.SecretKey
	for i := 0; i < 4; i++ {
		seed := sha256.Sum256([]byte(fmt.Sprintf("n42-profile1-test-validator-%d", i)))
		seed[0] = 0
		sk, err := bls.SecretKeyFromBytes(seed[:])
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, sk)
	}
	return out
}

// v1GenesisJSON is the standard genesis of slice 1 with the AM22 values of
// the 8-second slot.
func v1GenesisJSON(t *testing.T) []byte {
	t.Helper()
	var g map[string]any
	if err := json.Unmarshal(buildProfile1StandardGenesis(t), &g); err != nil {
		t.Fatal(err)
	}
	hs := g["config"].(map[string]any)["hotstuff"].(map[string]any)
	hs["blockIntervalMs"] = 8000
	hs["baseTimeoutMs"] = 24000
	hs["maxTimeoutMs"] = 96000
	hs["leaderTenure"] = 1
	hs["epochLength"] = 10800
	hs["gasLimit"] = 480000000
	hs["deferredExecutionDepth"] = 1
	out, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

type v1HeaderVector struct {
	Name     string          `json:"name"`
	View     uint64          `json:"view,omitempty"`
	Signer   *int            `json:"signerIndex,omitempty"`
	Header   json.RawMessage `json:"header"`
	RLP      string          `json:"rlp"`
	Hash     string          `json:"hash"`
	SealHash string          `json:"sealHash,omitempty"`
	Seal     string          `json:"seal,omitempty"`
	QC       *v1QC           `json:"qc,omitempty"`
}

type v1QC struct {
	View      uint64 `json:"view"`
	BlockHash string `json:"blockHash"`
	Signers   []int  `json:"signers"`
	SSZ       string `json:"ssz"`
	Domain    string `json:"domain"`
}

type v1Refusal struct {
	Name     string `json:"name"`
	Rule     string `json:"rule"`
	Check    string `json:"check"` // import | vote | execution
	Now      uint64 `json:"nowUnix,omitempty"`
	RLP      string `json:"rlp"`
	Hash     string `json:"hash"`
	Error    string `json:"expectedError"`
	Message  string `json:"gov5Message"`
	Mutation string `json:"mutation"`
}

type v1Validator struct {
	Index     int    `json:"index"`
	Address   string `json:"address"`
	BLSPub    string `json:"blsPublicKey"`
	BLSSecret string `json:"blsSecretKey"`
}

type v1File_ struct {
	Standard     string           `json:"standard"`
	ChainID      uint64           `json:"chainId"`
	GenesisFile  string           `json:"genesisFile"`
	GenesisHash  string           `json:"genesisHash"`
	Genesis      v1HeaderVector   `json:"genesis"`
	Validators   []v1Validator    `json:"validators"`
	SealDST      string           `json:"sealDst"`
	CommitDomain string           `json:"commitDomain"`
	Headers      []v1HeaderVector `json:"headers"`
	Refusals     []v1Refusal      `json:"refusals"`
}

func v1Encode(t *testing.T, name string, h *block.Header) v1HeaderVector {
	t.Helper()
	raw, err := rlp.EncodeToBytes(h)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	// encoding/json writes []byte as base64; give extraData as 0x-hex like
	// every other byte field.
	var m map[string]any
	if err := json.Unmarshal(enc, &m); err != nil {
		t.Fatal(err)
	}
	m["extraData"] = hexutil.Encode(h.Extra)
	fields, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return v1HeaderVector{Name: name, Header: fields, RLP: hexutil.Encode(raw), Hash: h.Hash().Hex()}
}

// TestProfile1HeaderVectorV1 builds, verifies and pins vector V1.
func TestProfile1HeaderVectorV1(t *testing.T) {
	// A QMDB chain (every profile-1 chain) roots its transactions in the
	// Ethereum trie (A12); NewBlockChain sets this process-wide flag.
	saved := block.UseEthereumTxRoot
	block.UseEthereumTxRoot = true
	defer func() { block.UseEthereumTxRoot = saved }()
	genesisJSON := v1GenesisJSON(t)
	g := new(conf.Genesis)
	if err := json.Unmarshal(genesisJSON, g); err != nil {
		t.Fatalf("8 s profile-1 genesis refused: %v", err)
	}
	cfg := g.Config
	gblk, _, err := (&GenesisBlock{GenesisConfig: g}).ToBlock()
	if err != nil {
		t.Fatal(err)
	}
	genesis := gblk.Header().(*block.Header)
	sks := v1SecretKeys(t)
	vs := profile1TestValidators(t)
	addr := func(i int) types.Address { return types.HexToAddress(vs[i].Address) }
	identity := hotstuff.H2V4ChainIdentity{ChainID: cfg.ChainID.Uint64(), GenesisHash: genesis.Hash()}
	hs := hotstuff.New(cfg.HotStuff, cfg)
	// The committee pool of the genesis (A19): parentBeaconRoot of block
	// N >= 2 is Blake3 of block N-1's committee evidence (B5).
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
	leader := func(view uint64) int { return int(view % uint64(len(sks))) } // tenure 1

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
	sealWith := func(h *block.Header, i int) *block.Header {
		h.ResetHashCache()
		digest := hs.SealHash(h)
		copy(h.Extra[len(h.Extra)-96:], sks[i].Sign(digest[:]).Marshal())
		h.ResetHashCache()
		return h
	}
	viewOf := func(h *block.Header) uint64 {
		v, err := hotstuff.ExtractViewFromExtra(h.Extra)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	seal := func(h *block.Header) *block.Header { return sealWith(h, leader(viewOf(h))) }
	reward := cfg.HotStuff.DevBlockReward.Amount()
	child := func(parent *block.Header, view uint64, qc *hotstuff.QuorumCertificate) *block.Header {
		n := parent.Number.Uint64() + 1
		coinbase := addr(leader(view))
		rewards := block.Rewards{
			{Address: coinbase, Amount: reward},
			{Address: *cfg.HotStuff.DevFaucetAddress, Amount: reward},
		}
		wh := hash.DeriveSha(rewards)
		baseFee, _ := uint256.FromBig(misc.CalcBaseFee(cfg, parent))
		zero, slot, rq := uint64(0), n, hotstuff.Profile1RequestsHash
		pbr := new(types.Hash)
		if parent.Number.Uint64() > 0 {
			ce, err := pool.BuildSimulatedCE(parent.Number.Uint64(), parent.Hash(), parent.ReceiptHash)
			if err != nil {
				t.Fatal(err)
			}
			root := ce.BeaconRoot()
			pbr = &root
		}
		return &block.Header{
			// Deferred execution (A10, k = 1): stateRoot, receiptsRoot,
			// logsBloom and gasUsed are the parent's execution; for blocks 1
			// and 2 of an idle chain they equal genesis's.
			ParentHash: parent.Hash(), Coinbase: coinbase, Root: genesis.Root,
			TxHash: block.TxRoot(nil), ReceiptHash: genesis.ReceiptHash, Bloom: genesis.Bloom,
			Difficulty: uint256.NewInt(0), Number: uint256.NewInt(n), GasLimit: cfg.HotStuff.GasLimit,
			GasUsed: 0, Time: parent.Time + v1SlotSecs, Extra: extraOf(view, qc), BaseFee: baseFee,
			WithdrawalsHash: &wh, BlobGasUsed: &zero, ExcessBlobGas: &zero,
			ParentBeaconRoot: pbr, RequestsHash: &rq, SlotNumber: &slot,
			MobileRegistryRoot: new(types.Hash),
		}
	}
	commitQC := func(view uint64, blockHash types.Hash, signers []int, commit bool) *hotstuff.QuorumCertificate {
		msg := hotstuff.H2V4VoteSigningMessage(identity, view, blockHash)
		if commit {
			msg = hotstuff.H2V4CommitSigningMessage(identity, view, blockHash, types.Hash{})
		}
		bitmap := make([]bool, len(sks))
		var sigs []blscommon.Signature
		for _, i := range signers {
			bitmap[i] = true
			sigs = append(sigs, sks[i].Sign(msg))
		}
		return &hotstuff.QuorumCertificate{View: view, BlockHash: blockHash,
			AggregateSignature: bls.AggregateSignatures(sigs).Marshal(), Signers: bitmap}
	}

	// Block 1, view 1: no QC (no commit yet). Block 2, view 2: the CommitQC
	// of block 1 by validators 0, 1, 2.
	b1 := seal(child(genesis, 1, nil))
	if err := hs.VerifyHeader(chain, b1, true); err != nil {
		t.Fatalf("block 1 refused: %v", err)
	}
	chain.headers = append(chain.headers, b1)
	qc1 := commitQC(1, b1.Hash(), []int{0, 1, 2}, true)
	b2 := seal(child(b1, 2, qc1))
	if err := hs.VerifyHeader(chain, b2, true); err != nil {
		t.Fatalf("block 2 refused: %v", err)
	}
	if err := checkProfile1Rewards(cfg, b2, []*block.Reward{
		{Address: b2.Coinbase, Amount: reward}, {Address: *cfg.HotStuff.DevFaucetAddress, Amount: reward},
	}, []*block.Reward{
		{Address: b2.Coinbase, Amount: reward}, {Address: *cfg.HotStuff.DevFaucetAddress, Amount: reward},
	}); err != nil {
		t.Fatalf("block 2 rewards refused: %v", err)
	}
	// B12: element 22 is the one-byte empty string 0x80 between
	// requestsHash and slotNumber.
	if raw, _ := rlp.EncodeToBytes(b2); !bytes.Contains(raw, append(append([]byte{0xa0}, hotstuff.Profile1RequestsHash[:]...), 0x80, 0x02)) {
		t.Fatal("block 2 RLP lacks the element-22 gap after requestsHash")
	}

	out := v1File_{
		Standard:     "N42 mixed production standard profile 1, v0.3 + AM1-AM23; vector V1 (header profile)",
		ChainID:      cfg.ChainID.Uint64(),
		GenesisFile:  v1Genesis,
		GenesisHash:  genesis.Hash().Hex(),
		Genesis:      v1Encode(t, "genesis", genesis),
		SealDST:      "BLS_SIG_BLS12381G2_XMD:SHA-256_SSWU_RO_POP_",
		CommitDomain: "H2-v4 commit, changesHash zero (H2V4CommitSigningMessage)",
	}
	for i, sk := range sks {
		out.Validators = append(out.Validators, v1Validator{Index: i, Address: vs[i].Address,
			BLSPub: vs[i].BLSKey, BLSSecret: hexutil.Encode(sk.Marshal())})
	}
	for _, hd := range []*block.Header{b1, b2} {
		v := v1Encode(t, fmt.Sprintf("block %d", hd.Number.Uint64()), hd)
		v.View = viewOf(hd)
		s := leader(v.View)
		v.Signer = &s
		sh := hs.SealHash(hd)
		v.SealHash = sh.Hex()
		v.Seal = hexutil.Encode(hd.Extra[len(hd.Extra)-96:])
		if qc, _ := hotstuff.ExtractHeaderQC(hd.Extra); qc != nil {
			b, _ := hotstuff.EncodeQC(qc)
			var signers []int
			for i, on := range qc.Signers {
				if on {
					signers = append(signers, i)
				}
			}
			v.QC = &v1QC{View: qc.View, BlockHash: qc.BlockHash.Hex(), Signers: signers,
				SSZ: hexutil.Encode(b), Domain: "commit"}
		}
		out.Headers = append(out.Headers, v)
	}

	// Refusals: each mutates block 2 (the QC-bearing header) and breaks one
	// rule; the header is resealed by the view's leader unless the rule is
	// the seal itself.
	h256 := func(s string) *types.Hash { v := types.HexToHash(s); return &v }
	one := uint64(1)
	type refusal struct {
		name, rule, mutation string
		build                func() *block.Header
		want                 error
	}
	fresh := func() *block.Header { return child(b1, 2, qc1) }
	sibling := seal(child(genesis, 1, nil))
	sibling.Time++
	seal(sibling)
	chain.headers = append(chain.headers, sibling) // stored, not an ancestor of block 2
	refusals := []refusal{
		{"extra over 4096 bytes", "B1", "4096 zero bytes inserted before the QC", func() *block.Header {
			h := fresh()
			h.Extra = append(append(append([]byte{}, h.Extra[:12]...), make([]byte, 4096)...), h.Extra[12:]...)
			return seal(h)
		}, hotstuff.ErrProfile1Extra},
		{"extra without seal", "B1", "extraData = N42H || view || QC (legacy layout, no seal)", func() *block.Header {
			h := fresh()
			h.Extra = h.Extra[:len(h.Extra)-96]
			return h
		}, hotstuff.ErrProfile1Extra},
		{"encoded empty QC", "B1", "QC slot holds an SSZ-encoded empty QC instead of zero bytes", func() *block.Header {
			h := fresh()
			empty, _ := hotstuff.EncodeQC(&hotstuff.QuorumCertificate{})
			h.Extra = append(append(extraOf(2, nil)[:12], empty...), make([]byte, 96)...)
			return seal(h)
		}, hotstuff.ErrProfile1Extra},
		{"QC view not below header view", "B1", "QC = CommitQC(view 2, block 1)", func() *block.Header {
			return seal(child(b1, 2, commitQC(2, b1.Hash(), []int{0, 1, 2}, true)))
		}, hotstuff.ErrProfile1QC},
		{"QC in the vote domain", "B1/E13", "QC signed in the H2-v4 vote (prepare) domain", func() *block.Header {
			return seal(child(b1, 2, commitQC(1, b1.Hash(), []int{0, 1, 2}, false)))
		}, hotstuff.ErrProfile1QC},
		{"QC below quorum", "E13", "QC signed by validators 0 and 1 only", func() *block.Header {
			return seal(child(b1, 2, commitQC(1, b1.Hash(), []int{0, 1}, true)))
		}, hotstuff.ErrProfile1QC},
		{"QC names a non-ancestor", "B1", "QC = CommitQC(view 1, a stored sibling of block 1)", func() *block.Header {
			return seal(child(b1, 2, commitQC(1, sibling.Hash(), []int{0, 1, 2}, true)))
		}, hotstuff.ErrProfile1QC},
		{"seal by a non-leader", "B2", "sealed by validator 3 (leader of view 2 is validator 2)", func() *block.Header {
			return sealWith(fresh(), 3)
		}, hotstuff.ErrProfile1Seal},
		{"seal over a different header", "B2", "gasUsed changed after sealing", func() *block.Header {
			h := seal(fresh())
			h.GasUsed = 1
			h.ResetHashCache()
			return h
		}, hotstuff.ErrProfile1Seal},
		{"coinbase not the leader", "B3", "coinbase = validator 3", func() *block.Header {
			h := fresh()
			h.Coinbase = addr(3)
			return seal(h)
		}, hotstuff.ErrProfile1Proposer},
		{"difficulty 1", "B3", "difficulty = 1", func() *block.Header {
			h := fresh()
			h.Difficulty = uint256.NewInt(1)
			return seal(h)
		}, hotstuff.ErrProfile1Difficulty},
		{"nonce 1", "B3", "nonce = 0x0000000000000001", func() *block.Header {
			h := fresh()
			h.Nonce = block.EncodeNonce(1)
			return seal(h)
		}, hotstuff.ErrProfile1Nonce},
		{"mixHash non-zero", "B3", "mixHash = 0x..01", func() *block.Header {
			h := fresh()
			h.MixDigest = types.HexToHash("0x01")
			return seal(h)
		}, hotstuff.ErrProfile1MixHash},
		{"ommersHash = empty-list hash", "B3", "ommersHash = 0x1dcc4de8...49347", func() *block.Header {
			h := fresh()
			h.UncleHash = hash.EmptyUncleHash
			return seal(h)
		}, hotstuff.ErrProfile1OmmersHash},
		{"timestamp before parent", "B4", "time = block 1 time - 1", func() *block.Header {
			h := fresh()
			h.Time = b1.Time - 1
			return seal(h)
		}, hotstuff.ErrProfile1TimeBeforeParent},
		{"withdrawalsRoot absent", "B6", "withdrawalsRoot = nil (RLP stops before element 17)", func() *block.Header {
			h := fresh()
			h.WithdrawalsHash = nil
			return seal(h)
		}, hotstuff.ErrProfile1WithdrawalsRoot},
		{"requestsHash sha256 of nothing", "B8", "requestsHash = 0xe3b0c442...b855", func() *block.Header {
			h := fresh()
			h.RequestsHash = h256("0xe3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
			return seal(h)
		}, hotstuff.ErrProfile1RequestsHash},
		{"requestsHash empty-ommers hash", "B8", "requestsHash = 0x1dcc4de8...49347", func() *block.Header {
			h := fresh()
			h.RequestsHash = h256(hash.EmptyUncleHash.Hex())
			return seal(h)
		}, hotstuff.ErrProfile1RequestsHash},
		{"blobGasUsed 1", "B9", "blobGasUsed = 1", func() *block.Header {
			h := fresh()
			h.BlobGasUsed = &one
			return seal(h)
		}, hotstuff.ErrProfile1BlobGas},
		{"excessBlobGas 1", "B9", "excessBlobGas = 1", func() *block.Header {
			h := fresh()
			h.ExcessBlobGas = &one
			return seal(h)
		}, hotstuff.ErrProfile1BlobGas},
		{"element 22 present", "B12", "element 22 = keccak256(\"\")", func() *block.Header {
			h := fresh()
			h.BlockAccessListHash = h256(hash.NilHash.Hex())
			return seal(h)
		}, hotstuff.ErrProfile1Element22},
		{"baseFee off by one", "B14", "baseFee = EIP-1559 value + 1", func() *block.Header {
			h := fresh()
			h.BaseFee = new(uint256.Int).AddUint64(h.BaseFee, 1)
			return seal(h)
		}, hotstuff.ErrProfile1BaseFee},
		{"gasLimit not hotstuff.gasLimit", "A23", "gasLimit = 480000000 + 1", func() *block.Header {
			h := fresh()
			h.GasLimit++
			return seal(h)
		}, hotstuff.ErrProfile1GasLimit},
	}
	errName := map[error]string{}
	for name, e := range map[string]error{
		"ErrProfile1Extra": hotstuff.ErrProfile1Extra, "ErrProfile1QC": hotstuff.ErrProfile1QC,
		"ErrProfile1Seal": hotstuff.ErrProfile1Seal, "ErrProfile1Proposer": hotstuff.ErrProfile1Proposer,
		"ErrProfile1Difficulty": hotstuff.ErrProfile1Difficulty, "ErrProfile1Nonce": hotstuff.ErrProfile1Nonce,
		"ErrProfile1MixHash": hotstuff.ErrProfile1MixHash, "ErrProfile1OmmersHash": hotstuff.ErrProfile1OmmersHash,
		"ErrProfile1TimeBeforeParent": hotstuff.ErrProfile1TimeBeforeParent,
		"ErrProfile1TimeFuture":       hotstuff.ErrProfile1TimeFuture,
		"ErrProfile1WithdrawalsRoot":  hotstuff.ErrProfile1WithdrawalsRoot,
		"ErrProfile1RequestsHash":     hotstuff.ErrProfile1RequestsHash, "ErrProfile1BlobGas": hotstuff.ErrProfile1BlobGas,
		"ErrProfile1Element22": hotstuff.ErrProfile1Element22, "ErrProfile1BaseFee": hotstuff.ErrProfile1BaseFee,
		"ErrProfile1GasLimit": hotstuff.ErrProfile1GasLimit, "ErrProfile1Rewards": ErrProfile1Rewards,
	} {
		errName[e] = name
	}
	add := func(name, rule, check, mutation string, now uint64, h *block.Header, err, want error) {
		if !errors.Is(err, want) {
			t.Fatalf("%s: got %v, want %v", name, err, want)
		}
		raw, rerr := rlp.EncodeToBytes(h)
		if rerr != nil {
			t.Fatal(rerr)
		}
		out.Refusals = append(out.Refusals, v1Refusal{Name: name, Rule: rule, Check: check, Now: now,
			RLP: hexutil.Encode(raw), Hash: h.Hash().Hex(), Error: errName[want], Message: err.Error(), Mutation: mutation})
	}
	for _, r := range refusals {
		h := r.build()
		add(r.name, r.rule, "import", r.mutation, 0, h, hs.VerifyHeader(chain, h, true), r.want)
	}
	// B4 upper bound: refused at vote time only; the same header imports.
	ahead := seal(fresh())
	now := ahead.Time - 3
	if err := hs.VerifyHeader(chain, ahead, true); err != nil {
		t.Fatalf("import of a header 3 s ahead of the clock refused: %v", err)
	}
	add("timestamp beyond floor(now)+2", "B4", "vote", "block 2 checked with now = time - 3 (accepted on import)", now,
		ahead, hs.StaticHeaderCheck(chain, ahead, time.Unix(int64(now), 0)), hotstuff.ErrProfile1TimeFuture)
	// B6 body check: the body's reward list does not match withdrawalsRoot.
	paid := []*block.Reward{{Address: b2.Coinbase, Amount: reward}, {Address: *cfg.HotStuff.DevFaucetAddress, Amount: reward}}
	add("body rewards differ from withdrawalsRoot", "B6", "execution",
		"block 2 with a body reward list that pays the faucet only", 0, b2,
		checkProfile1Rewards(cfg, b2, paid, paid[1:]), ErrProfile1Rewards)

	enc, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		v1File:    append(enc, '\n'),
		v1Genesis: genesisJSON,
		v1Readme:  []byte(v1ReadmeText(out)),
	}
	dirs := []string{}
	if os.Getenv("N42_UPDATE_PROFILE1_FIXTURE") == "1" {
		dirs = append(dirs, v1Dir)
	}
	if d := os.Getenv("N42_PROFILE1_V1_OUT"); d != "" {
		dirs = append(dirs, d)
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, b := range files {
			if err := os.WriteFile(filepath.Join(d, name), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(v1Dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s/%s differs from its derivation; rerun with N42_UPDATE_PROFILE1_FIXTURE=1 if intended", v1Dir, name)
		}
	}
	t.Logf("V1: genesis %s, block 1 %s, block 2 %s, %d refusals", out.GenesisHash, b1.Hash().Hex(), b2.Hash().Hex(), len(out.Refusals))
}

func v1ReadmeText(v v1File_) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, `# Vector V1: header profile (N42 mixed production standard, profile 1)

Generated by gov5 `+"`internal/profile1_header_vectors_test.go`"+` (TestProfile1HeaderVectorV1);
gov5's code is the bytes reference. Do not edit by hand: rerun the test with
`+"`N42_UPDATE_PROFILE1_FIXTURE=1`"+` (gov5 testdata copy) and `+"`N42_PROFILE1_V1_OUT=<dir>`"+`.

## Files

- `+"`%s`"+`: the profile-1 test genesis with the AM22 values (blockIntervalMs 8000,
  baseTimeoutMs 24000, maxTimeoutMs 96000, leaderTenure 1, epochLength 10800,
  gasLimit 480000000, deferredExecutionDepth 1), chain id %d, four test validators.
- `+"`%s`"+`: genesis header, two sealed headers, the validators (with their test
  BLS secret keys) and one refused header per rule.

Genesis hash: `+"`%s`"+`

## Contents of the JSON

- `+"`genesis`"+`: header fields, RLP and hash of the genesis (ommersHash zero, requestsHash the
  empty-trie root, element 22 the 0x80 gap, mobileRegistryRoot present and zero, extraData empty).
- `+"`headers`"+`: block 1 (view 1, no QC: nothing committed yet) and block 2 (view 2, the
  CommitQC of block 1 by validators 0, 1, 2). Each with its fields, RLP, hash, sealHash
  (keccak256 of the RLP with the last 96 extraData bytes removed, AM4), the seal (BLS,
  POP DST `+"`%s`"+`), the signer (leader(v) = v mod 4) and, for block 2, the QC
  (SSZ as embedded in extraData; signed in the %s).
- `+"`refusals`"+`: each entry is block 2 with one rule broken (resealed by the leader unless the
  seal is the rule), the rule id, where it is checked (`+"`import`"+` = header verification on
  import, `+"`vote`"+` = only before the Round-1 vote with the given `+"`nowUnix`"+`,
  `+"`execution`"+` = the body reward list against withdrawalsRoot after execution), gov5's
  sentinel error and full message.

## Rules covered

B1 extraData layout `+"`N42H || view u64 LE || [QC] || seal(96)`"+`, at most 4096 bytes, an absent
QC is zero bytes; the QC is a commit-domain QC of the genesis set, below the header's view,
naming an ancestor. B2 seal by the leader of the view over sealHash. B3 difficulty, nonce,
mixHash, ommersHash zero (the empty-list hash is refused), coinbase = leader. B4
parent.time <= time (import) and time <= floor(now)+2 (vote time only). B6 withdrawalsRoot
present and equal to keccak(concat(rlp([address, amount]))) of the rewards. B8 requestsHash
= 0x56e81f17...b421 (AM3). B9 blobGasUsed = excessBlobGas = 0. B12 element 22 = 0x80.
B14 EIP-1559 baseFee from the parent header. A23 gasLimit = hotstuff.gasLimit.

Deferred fields (A10/B13: stateRoot, receiptsRoot, logsBloom, gasUsed) of blocks 1 and 2
are genesis's (an idle chain); their checks belong to V5. parentBeaconRoot is zero on block 1
(B5) and Blake3 of block 1's committee evidence (the genesis committee pool) on block 2; the
evidence derivation itself is vector V2.
`, v1Genesis, v.ChainID, v1File, v.GenesisHash, v.SealDST, v.CommitDomain)
	return b.String()
}

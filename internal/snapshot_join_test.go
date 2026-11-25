// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
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
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/internal/snapshotjoin"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

// p1Node is a profile-1 BlockChain over its own MDBX directory, wired the
// way node.go wires it: the hotstuff engine, the QMDB root computer reloaded
// from the persisted forest.
type p1Node struct {
	db kv.RwDB
	bc *BlockChain
}

func openP1DB(t *testing.T, dir string) kv.RwDB {
	t.Helper()
	db, err := mdbx.NewMDBX(log.New()).Path(dir).Label(kv.ChainDB).
		WithTableCfg(func(kv.TableCfg) kv.TableCfg { return modules.N42TableCfg }).Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

func startP1Node(t *testing.T, db kv.RwDB, genesis *block.Block, cfg *params.ChainConfig, hs *hotstuff.HotStuff) *p1Node {
	t.Helper()
	bcIface, err := NewBlockChain(context.Background(), genesis, hs, db, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	bc := bcIface.(*BlockChain)
	t.Cleanup(func() { bc.Close() })
	rc := commitment.NewQMDBRootComputer()
	rtx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rc.SetCold(rtx)
	if err := rc.LoadFrom(rtx); err != nil {
		t.Fatalf("reload QMDB forest: %v", err)
	}
	rtx.Rollback()
	rc.SetCold(nil)
	rc.EnableUndoRecording()
	bc.SetQMDBRootComputer(rc)
	return &p1Node{db: db, bc: bc}
}

// p1Builder seals profile-1 blocks like the V5 vector: leader(v) = v mod 4,
// extra = N42H || view || commit QC of the parent || seal, the deferred
// fields carried from the parent's stored result.
type p1Builder struct {
	t        *testing.T
	cfg      *params.ChainConfig
	hs       *hotstuff.HotStuff
	pool     *blspool.Pool
	sks      []blscommon.SecretKey
	identity hotstuff.H2V4ChainIdentity
	key      *ecdsa.PrivateKey
	signer   transaction.Signer
	nonce    uint64
}

func (p *p1Builder) commitQC(view uint64, blockHash types.Hash, signers ...int) *hotstuff.QuorumCertificate {
	msg := hotstuff.H2V4CommitSigningMessage(p.identity, view, blockHash, types.Hash{})
	bitmap := make([]bool, len(p.sks))
	var sigs []blscommon.Signature
	for _, i := range signers {
		bitmap[i] = true
		sigs = append(sigs, p.sks[i].Sign(msg))
	}
	return &hotstuff.QuorumCertificate{View: view, BlockHash: blockHash,
		AggregateSignature: bls.AggregateSignatures(sigs).Marshal(), Signers: bitmap}
}

func (p *p1Builder) sign(to *types.Address, value uint64, data []byte) *transaction.Transaction {
	chainID, _ := uint256.FromBig(p.cfg.ChainID)
	tx, err := transaction.SignTx(transaction.NewTx(&transaction.DynamicFeeTx{
		ChainID: chainID, Nonce: p.nonce, GasTipCap: uint256.NewInt(v3TipCap), GasFeeCap: uint256.NewInt(v3FeeCap),
		Gas: 10_000_000, To: to, Value: uint256.NewInt(value), Data: data,
	}), p.signer, p.key)
	if err != nil {
		p.t.Fatal(err)
	}
	p.nonce++
	return tx
}

func (p *p1Builder) child(parent *block.Header, carried rawdb.ExecutedResult, txs []*transaction.Transaction) *block.Block {
	t := p.t
	var pv uint64
	if parent.Number.Uint64() > 0 {
		v, err := hotstuff.ExtractViewFromExtra(parent.Extra)
		if err != nil {
			t.Fatal(err)
		}
		pv = v
	}
	view := pv + 1
	n := parent.Number.Uint64() + 1
	leader := int(view % uint64(len(p.sks)))
	coinbase := types.HexToAddress(p.cfg.HotStuff.Validators[leader].Address)
	reward := p.cfg.HotStuff.DevBlockReward.Amount()
	rewards := block.Rewards{{Address: coinbase, Amount: reward}, {Address: *p.cfg.HotStuff.DevFaucetAddress, Amount: reward}}
	wh := hash.DeriveSha(rewards)
	baseFee, _ := uint256.FromBig(misc.CalcBaseFee(p.cfg, parent))
	zero, slot, rq := uint64(0), n, carried.RequestsHash
	pbr := new(types.Hash)
	if parent.Number.Uint64() > 0 {
		ce, err := p.pool.BuildSimulatedCE(parent.Number.Uint64(), parent.Hash(), parent.ReceiptHash)
		if err != nil {
			t.Fatal(err)
		}
		root := ce.BeaconRoot()
		pbr = &root
	}
	extra := append([]byte("N42H"), make([]byte, 8)...)
	for i := 0; i < 8; i++ {
		extra[4+i] = byte(view >> (8 * i))
	}
	if parent.Number.Uint64() > 0 {
		qc, err := hotstuff.EncodeQC(p.commitQC(pv, parent.Hash(), 0, 1, 2))
		if err != nil {
			t.Fatal(err)
		}
		extra = append(extra, qc...)
	}
	extra = append(extra, make([]byte, 96)...)
	h := &block.Header{
		ParentHash: parent.Hash(), Coinbase: coinbase, Root: carried.Root,
		TxHash: block.TxRoot(txs), ReceiptHash: carried.ReceiptHash, Bloom: carried.Bloom,
		Difficulty: uint256.NewInt(0), Number: uint256.NewInt(n), GasLimit: p.cfg.HotStuff.GasLimit,
		GasUsed: carried.GasUsed, Time: parent.Time + v1SlotSecs, Extra: extra, BaseFee: baseFee,
		WithdrawalsHash: &wh, BlobGasUsed: &zero, ExcessBlobGas: &zero,
		ParentBeaconRoot: pbr, RequestsHash: &rq, SlotNumber: &slot,
		MobileRegistryRoot: new(types.Hash),
	}
	digest := p.hs.SealHash(h)
	copy(h.Extra[len(h.Extra)-96:], p.sks[leader].Sign(digest[:]).Marshal())
	h.ResetHashCache()
	return block.NewBlockFromStorage(h.Hash(), h, &block.Body{Txs: txs, Rewards: rewards})
}

// insert imports blk on n and checks it became the applied head.
func (n *p1Node) insert(t *testing.T, blk *block.Block) rawdb.ExecutedResult {
	t.Helper()
	if _, err := n.bc.InsertChain([]block.IBlock{blk}); err != nil {
		t.Fatalf("insert block %d: %v", blk.Number64().Uint64(), err)
	}
	var res rawdb.ExecutedResult
	if err := n.db.View(context.Background(), func(tx kv.Tx) error {
		num, h, ok, err := rawdb.ReadQMDBApplied(tx)
		if err != nil || !ok || num != blk.Number64().Uint64() || types.Hash(h) != blk.Hash() {
			t.Fatalf("block %d not applied: marker %d %x %v %v", blk.Number64().Uint64(), num, h, ok, err)
		}
		r, ok, err := rawdb.ReadExecutedResult(tx, blk.Hash())
		if err != nil || !ok {
			t.Fatalf("block %d: no executed result (%v)", blk.Number64().Uint64(), err)
		}
		res = r
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return res
}

type p1Fixture struct {
	cfg     *params.ChainConfig
	genesis *block.Block
	b       *p1Builder
	src     *p1Node
	blocks  []*block.Block // blocks[i] is block i+1
	results []rawdb.ExecutedResult
	bundle  string
	pivot   uint64
}

// buildSnapshotJoinFixture runs a profile-1 chain to pivot P = 6 with
// transactions (a contract with storage, new accounts, a cleared slot),
// exports the bundle while blocks 7 and 8 (8 carries the commit QC of 7)
// are imported and committed, and keeps three more blocks for the joiner.
func buildSnapshotJoinFixture(t *testing.T) *p1Fixture {
	t.Helper()
	saved := block.UseEthereumTxRoot
	t.Cleanup(func() { block.UseEthereumTxRoot = saved })
	modules.N42Init()
	prevCfg := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevCfg })

	key, sender := v3Key(t)
	g := new(conf.Genesis)
	if err := json.Unmarshal(v1GenesisJSON(t), g); err != nil {
		t.Fatal(err)
	}
	g.Alloc[sender] = conf.GenesisAccount{Balance: v3SenderBalanceWei}
	cfg := g.Config
	srcDB := openP1DB(t, filepath.Join(t.TempDir(), "src"))
	var genesis *block.Block
	if err := srcDB.Update(context.Background(), func(tx kv.RwTx) error {
		var err error
		if genesis, _, err = (&GenesisBlock{GenesisConfig: g}).Write(tx); err != nil {
			return err
		}
		return rawdb.WriteChainConfig(tx, genesis.Hash(), cfg)
	}); err != nil {
		t.Fatal(err)
	}
	hs := hotstuff.New(cfg.HotStuff, cfg)
	cp := cfg.HotStuff.CommitteePool
	seed, err := hexutil.Decode(cp.SeedHex)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := blspool.NewSimulatedPool(blspool.PoolConfig{Seed: [32]byte(seed), PoolSize: cp.PoolSize, CommitteeSize: cp.CommitteeSize, RampBlocks: cp.RampBlocks})
	if err != nil {
		t.Fatal(err)
	}
	hs.SetCommitteeEvidence(pool, nil)
	// node.go wires apos.DoReward, which pays nothing on profile 1 (no
	// Apos config); apos imports this package, so the test passes the same.
	hs.SetRewardFunc(func(*params.ChainConfig, *state.IntraBlockState, *block.Header, consensus.N42ChainHeaderReader) ([]*block.Reward, map[types.Address]*uint256.Int, error) {
		return nil, nil, nil
	})
	f := &p1Fixture{cfg: cfg, genesis: genesis, pivot: 6}
	f.b = &p1Builder{t: t, cfg: cfg, hs: hs, pool: pool, sks: v1SecretKeys(t),
		identity: hotstuff.H2V4ChainIdentity{ChainID: cfg.ChainID.Uint64(), GenesisHash: genesis.Hash()},
		key:      key, signer: transaction.LatestSignerForChainID(cfg.ChainID)}
	f.src = startP1Node(t, srcDB, genesis, cfg, hs)

	contract := crypto.CreateAddress(sender, 0)
	fresh := func(i byte) *types.Address { a := types.Address{0xcc, i}; return &a }
	txs := [][]*transaction.Transaction{
		{f.b.sign(nil, 0, v3StoreInitcode)},
		{f.b.sign(&contract, 0, storeCall(1, 0)), f.b.sign(fresh(1), 7, nil)},
		{f.b.sign(&contract, 0, storeCall(5, 9)), f.b.sign(fresh(2), 1, nil)},
		{},
		{f.b.sign(fresh(3), 3, nil), f.b.sign(&contract, 0, storeCall(6, 1))},
		{f.b.sign(fresh(1), 2, nil)},
		// above the pivot
		{f.b.sign(&contract, 0, storeCall(5, 0)), f.b.sign(fresh(4), 5, nil)},
		{f.b.sign(fresh(5), 1, nil)},
		{f.b.sign(&contract, 0, storeCall(7, 3))},
		{},
		{f.b.sign(fresh(1), 4, nil)},
	}
	parent := genesis.Header().(*block.Header)
	carried := rawdb.ExecutedResult{Root: parent.Root, ReceiptHash: parent.ReceiptHash, Bloom: parent.Bloom, GasUsed: parent.GasUsed, RequestsHash: *parent.RequestsHash}
	build := func(i int) *block.Block {
		blk := f.b.child(parent, carried, txs[i])
		f.blocks = append(f.blocks, blk)
		parent = blk.Header().(*block.Header)
		return blk
	}
	commit := func(blk *block.Block) {
		if err := f.src.bc.CommitToCanonical(blk.Hash()); err != nil {
			t.Fatalf("commit block %d: %v", blk.Number64().Uint64(), err)
		}
	}
	for i := 0; i < int(f.pivot); i++ {
		carried = f.src.insert(t, build(i))
		f.results = append(f.results, carried)
		if i > 0 {
			commit(f.blocks[i-1]) // block i+1 carries the commit QC of block i
		}
	}

	// Export from the "running" source: the state is read at P, then P+1
	// and P+2 arrive and P+2's header carries the commit QC of P+1.
	f.bundle = filepath.Join(t.TempDir(), "bundle")
	stateRead := make(chan struct{})
	type exported struct {
		m   *snapshotjoin.Manifest
		err error
	}
	done := make(chan exported, 1)
	go func() {
		m, err := snapshotjoin.ExportBundle(context.Background(), srcDB, f.bundle, snapshotjoin.ExportOptions{
			ChainID: cfg.ChainID.Uint64(), Wait: time.Minute, Poll: 20 * time.Millisecond,
			OnStateRead: func(uint64, types.Hash) { close(stateRead) },
		})
		done <- exported{m, err}
	}()
	select {
	case <-stateRead:
	case r := <-done:
		t.Fatalf("export ended before reading the state: %v", r.err)
	}
	for i := int(f.pivot); i < len(txs); i++ {
		carried = f.src.insert(t, build(i))
		f.results = append(f.results, carried)
		commit(f.blocks[i-1])
	}
	r := <-done
	if r.err != nil {
		t.Fatalf("export: %v", r.err)
	}
	if r.m.PivotNumber != f.pivot || r.m.PivotHash != f.blocks[f.pivot-1].Hash().Hex() {
		t.Fatalf("bundle pivot %d %s, want %d", r.m.PivotNumber, r.m.PivotHash, f.pivot)
	}
	if r.m.PostStateRoot != f.blocks[f.pivot].Header().(*block.Header).Root.Hex() || r.m.PostStateRoot != f.results[f.pivot-1].Root.Hex() {
		t.Fatalf("bundle root %s is not header(P+1).Root", r.m.PostStateRoot)
	}
	if r.m.AnchorNumber != f.pivot+1 || r.m.AnchorLow != 1 {
		t.Fatalf("anchor on block %d down to %d, want %d down to 1", r.m.AnchorNumber, r.m.AnchorLow, f.pivot+1)
	}
	if r.m.Accounts == 0 || r.m.StorageSlots == 0 {
		t.Fatalf("bundle without accounts or storage: %+v", r.m)
	}
	return f
}

// importInto initializes a fresh datadir with the genesis and the bundle in
// dir; the error is Import's (the transaction is rolled back on error).
func (f *p1Fixture) importInto(t *testing.T, db kv.RwDB, dir string, cfg *params.ChainConfig) (*snapshotjoin.Result, error) {
	t.Helper()
	g := new(conf.Genesis)
	if err := json.Unmarshal(v1GenesisJSON(t), g); err != nil {
		t.Fatal(err)
	}
	_, sender := v3Key(t)
	g.Alloc[sender] = conf.GenesisAccount{Balance: v3SenderBalanceWei}
	if cfg != nil {
		g.Config = cfg
	}
	var res *snapshotjoin.Result
	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		genesis, _, err := (&GenesisBlock{GenesisConfig: g}).Write(tx)
		if err != nil {
			return err
		}
		if err := rawdb.WriteChainConfig(tx, genesis.Hash(), g.Config); err != nil {
			return err
		}
		res, err = snapshotjoin.Import(tx, g.Config, genesis.Hash(), dir, nil)
		return err
	})
	return res, err
}

func TestSnapshotJoinRoundTrip(t *testing.T) {
	f := buildSnapshotJoinFixture(t)
	db := openP1DB(t, filepath.Join(t.TempDir(), "joiner"))
	res, err := f.importInto(t, db, f.bundle, nil)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Pivot != f.pivot || res.Root != f.results[f.pivot-1].Root || res.Headers != int(f.pivot) {
		t.Fatalf("import result %+v", res)
	}
	// The joiner starts at head P and continues: every block above the
	// pivot passes the deferred check and executes to the source's result.
	node := startP1Node(t, db, f.genesis, f.cfg, f.b.hs)
	if cur := node.bc.CurrentBlock(); cur == nil || cur.Hash() != f.blocks[f.pivot-1].Hash() {
		t.Fatalf("joiner head %v, want block %d", cur, f.pivot)
	}
	for i := int(f.pivot); i < len(f.blocks); i++ {
		got := node.insert(t, f.blocks[i])
		if got != f.results[i] {
			t.Fatalf("block %d executed to %+v on the joiner, %+v on the source", i+1, got, f.results[i])
		}
		if err := node.bc.CommitToCanonical(f.blocks[i-1].Hash()); err != nil {
			t.Fatalf("joiner commit %d: %v", i, err)
		}
	}
	// The joiner's state equals the source's: plain state and forest.
	if err := f.src.db.View(context.Background(), func(stx kv.Tx) error {
		return db.View(context.Background(), func(jtx kv.Tx) error {
			for _, table := range []string{modules.Account, modules.Storage, modules.Code} {
				if a, b := tableDigest(t, stx, table), tableDigest(t, jtx, table); a != b {
					t.Fatalf("table %s differs between the source and the joiner", table)
				}
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
}

func tableDigest(t *testing.T, tx kv.Tx, table string) string {
	t.Helper()
	h := sha256.New()
	if err := tx.ForEach(table, nil, func(k, v []byte) error {
		h.Write(k)
		h.Write([]byte{0xff})
		h.Write(v)
		h.Write([]byte{0xfe})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// copyBundle copies the fixture's bundle to a new directory for tampering.
func copyBundle(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "bundle")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

// rewrite replaces a bundle file and its manifest digest (so the refusal is
// the content check, not the damaged-copy check).
func rewrite(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, snapshotjoin.FileManifest))
	if err != nil {
		t.Fatal(err)
	}
	var m snapshotjoin.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	m.Files[name] = hex.EncodeToString(sum[:])
	raw, _ = json.MarshalIndent(&m, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, snapshotjoin.FileManifest), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	fh, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	var lines []string
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}

func TestSnapshotJoinRefusals(t *testing.T) {
	f := buildSnapshotJoinFixture(t)
	read := func(name string) []byte {
		data, err := os.ReadFile(filepath.Join(f.bundle, name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	anchor, err := snapshotjoin.DecodeAnchor(read(snapshotjoin.FileAnchor))
	if err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, filepath.Join(f.bundle, snapshotjoin.FileState))
	_, sender := v3Key(t)
	contract := strings.ToLower(crypto.CreateAddress(sender, 0).Hex())

	cases := []struct {
		name   string
		tamper func(dir string)
		cfg    func() *params.ChainConfig
		want   string
	}{
		{"qc-two-signers", func(dir string) {
			a := *anchor
			a.QC = f.b.commitQC(anchor.QC.View, anchor.QC.BlockHash, 0, 1)
			enc, _ := snapshotjoin.EncodeAnchor(&a)
			rewrite(t, dir, snapshotjoin.FileAnchor, enc)
		}, nil, "does not verify against the genesis validator set"},
		{"qc-prepare-domain", func(dir string) {
			a := *anchor
			msg := hotstuff.H2V4VoteSigningMessage(f.b.identity, anchor.QC.View, anchor.QC.BlockHash)
			var sigs []blscommon.Signature
			for i := 0; i < 3; i++ {
				sigs = append(sigs, f.b.sks[i].Sign(msg))
			}
			qc := *anchor.QC
			qc.AggregateSignature = bls.AggregateSignatures(sigs).Marshal()
			a.QC = &qc
			enc, _ := snapshotjoin.EncodeAnchor(&a)
			rewrite(t, dir, snapshotjoin.FileAnchor, enc)
		}, nil, "does not verify against the genesis validator set"},
		{"qc-other-block", func(dir string) {
			a := *anchor
			a.QC = f.b.commitQC(anchor.QC.View+1, f.blocks[f.pivot+1].Hash(), 0, 1, 2)
			enc, _ := snapshotjoin.EncodeAnchor(&a)
			rewrite(t, dir, snapshotjoin.FileAnchor, enc)
		}, nil, "is not the QC's block"},
		{"next-header-root", func(dir string) {
			h := block.CopyHeader(f.blocks[f.pivot].Header().(*block.Header))
			h.Root[0] ^= 1
			enc, _ := rlp.EncodeToBytes(h)
			rewrite(t, dir, snapshotjoin.FileNextHeader, enc)
		}, nil, "the certified chain has block"},
		{"qmdb-root", func(dir string) {
			tree, _, err := qmdb.ImportPortableSnapshot(read(snapshotjoin.FileQMDB), f.cfg.ChainID.Uint64(), qmdb.Hash(f.genesis.Hash()))
			if err != nil {
				t.Fatal(err)
			}
			tree.Set(qmdb.Hash{0x42}, []byte{1, 2, 3}) // one more leaf: another root
			var buf bytes.Buffer
			md := qmdb.PortableSnapshotMetadata{ChainID: f.cfg.ChainID.Uint64(), GenesisHash: qmdb.Hash(f.genesis.Hash()),
				BlockNumber: f.pivot, BlockHash: qmdb.Hash(f.blocks[f.pivot-1].Hash()), Root: tree.Root(), NextSlot: tree.NextSlot()}
			if _, err := qmdb.WritePortableSnapshotTombstone(&buf, md, tree, nil); err != nil {
				t.Fatal(err)
			}
			rewrite(t, dir, snapshotjoin.FileQMDB, buf.Bytes())
		}, nil, "rebuilds to root"},
		{"qmdb-label", func(dir string) {
			tree, _, err := qmdb.ImportPortableSnapshot(read(snapshotjoin.FileQMDB), f.cfg.ChainID.Uint64(), qmdb.Hash(f.genesis.Hash()))
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			md := qmdb.PortableSnapshotMetadata{ChainID: f.cfg.ChainID.Uint64(), GenesisHash: qmdb.Hash(f.genesis.Hash()),
				BlockNumber: f.pivot + 1, BlockHash: qmdb.Hash(f.blocks[f.pivot].Hash()), Root: tree.Root(), NextSlot: tree.NextSlot()}
			if _, err := qmdb.WritePortableSnapshotTombstone(&buf, md, tree, nil); err != nil {
				t.Fatal(err)
			}
			rewrite(t, dir, snapshotjoin.FileQMDB, buf.Bytes())
		}, nil, "is labelled block"},
		{"jsonl-missing-account", func(dir string) {
			var keep []string
			dropped := false
			for _, l := range lines {
				if !dropped && strings.Contains(l, `"address":"0xcc01`) {
					dropped = true
					continue
				}
				keep = append(keep, l)
			}
			if !dropped {
				t.Fatal("no fresh account row to drop")
			}
			rewrite(t, dir, snapshotjoin.FileState, []byte(strings.Join(keep, "\n")+"\n"))
		}, nil, "does not cover the QMDB tree"},
		{"jsonl-missing-slot", func(dir string) {
			out := append([]string{}, lines...)
			found := false
			for i, l := range out {
				if !strings.Contains(l, `"address":"`+contract) {
					continue
				}
				var row map[string]any
				if err := json.Unmarshal([]byte(l), &row); err != nil {
					t.Fatal(err)
				}
				st := row["storage"].(map[string]any)
				for k := range st {
					delete(st, k)
					break
				}
				b, _ := json.Marshal(row)
				out[i], found = string(b), true
			}
			if !found {
				t.Fatal("no contract row")
			}
			rewrite(t, dir, snapshotjoin.FileState, []byte(strings.Join(out, "\n")+"\n"))
		}, nil, "does not cover the QMDB tree"},
		{"jsonl-extra-row", func(dir string) {
			extra := `{"address":"0xffffffffffffffffffffffffffffffffffffff00","balance":"0x1","nonce":"0x0"}`
			rewrite(t, dir, snapshotjoin.FileState, []byte(strings.Join(append(append([]string{}, lines...), extra), "\n")+"\n"))
		}, nil, "is not authenticated by the QMDB root"},
		{"jsonl-balance", func(dir string) {
			out := append([]string{}, lines...)
			out[1] = strings.Replace(out[1], `"balance":"0x`, `"balance":"0x1`, 1)
			rewrite(t, dir, snapshotjoin.FileState, []byte(strings.Join(out, "\n")+"\n"))
		}, nil, "is not authenticated by the QMDB root"},
		{"jsonl-root-line", func(dir string) {
			out := append([]string{}, lines...)
			out[0] = `{"root":"` + f.blocks[f.pivot-1].Header().(*block.Header).Root.Hex() + `"}`
			rewrite(t, dir, snapshotjoin.FileState, []byte(strings.Join(out, "\n")+"\n"))
		}, nil, "is not header(P+1).Root"},
		{"jsonl-damaged-copy", func(dir string) {
			if err := os.WriteFile(filepath.Join(dir, snapshotjoin.FileState), []byte(strings.Join(lines[:len(lines)-1], "\n")+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, nil, "damaged copy"},
		{"wrong-genesis", nil, func() *params.ChainConfig {
			g := new(conf.Genesis)
			if err := json.Unmarshal(v1GenesisJSON(t), g); err != nil {
				t.Fatal(err)
			}
			c := g.Config
			c.ChainID = new(uint256.Int).AddUint64(uint256.MustFromBig(c.ChainID), 1).ToBig()
			return c
		}, "wrong chain"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := copyBundle(t, f.bundle)
			if c.tamper != nil {
				c.tamper(dir)
			}
			var cfg *params.ChainConfig
			if c.cfg != nil {
				cfg = c.cfg()
			}
			db := openP1DB(t, filepath.Join(t.TempDir(), "joiner"))
			_, err := f.importInto(t, db, dir, cfg)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want an error containing %q", err, c.want)
			}
			// Nothing was written: the datadir has no chain.
			if err := db.View(context.Background(), func(tx kv.Tx) error {
				if h, _ := rawdb.ReadCanonicalHash(tx, 0); h != (types.Hash{}) {
					t.Fatal("a refused import left a genesis behind")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
	// The untouched copy imports (the refusals above are the tampering).
	db := openP1DB(t, filepath.Join(t.TempDir(), "joiner"))
	if _, err := f.importInto(t, db, copyBundle(t, f.bundle), nil); err != nil {
		t.Fatalf("untouched bundle refused: %v", err)
	}
}

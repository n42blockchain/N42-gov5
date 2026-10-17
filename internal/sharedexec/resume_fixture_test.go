package sharedexec

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/apos"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

type resumeExecutionFixture struct {
	genesis                                    *conf.Genesis
	genesisBlock                               *block.Block
	genesisRaw, manifestRaw, proofRaw, planRaw []byte
	sourceRoot, dbPath                         string
	descriptor                                 CorpusDescriptor
	senders                                    [4]types.Address
	private                                    [4]*ecdsa.PrivateKey
	keys                                       []blscommon.SecretKey
	frames                                     [4]CorpusFrame
	chain                                      *internal.BlockChain
	server                                     *Server
	public, leader                             *hotstuff.HotStuff
	closeOwner                                 func()
	baseline                                   []CanonicalAccount
	plan                                       ResumePlan
	planID                                     types.Hash
	lastQC                                     hotstuff.QuorumCertificate
}

// Real signed source and real public-QC execution establish the four canonical
// prefixes. No PlainState nonce is written by the fixture or a mocked RPC.
func newResumeExecutionFixture(t *testing.T) *resumeExecutionFixture {
	t.Helper()
	f := &resumeExecutionFixture{sourceRoot: filepath.Join(t.TempDir(), "source"), dbPath: filepath.Join(t.TempDir(), "chaindata"), lastQC: hotstuff.GenesisQC()}
	if err := os.Mkdir(f.sourceRoot, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../scripts/gov5-perf/isolated-chain.json")
	if err != nil {
		t.Fatal(err)
	}
	f.genesis = new(conf.Genesis)
	if err := json.Unmarshal(raw, f.genesis); err != nil {
		t.Fatal(err)
	}
	g := f.genesis
	g.BaseFee = uint256.NewInt(1)
	g.Alloc = make(conf.GenesisAlloc)
	g.Config.HotStuff.Validators = nil
	for i := range f.private {
		key, err := crypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		f.private[i] = key
		f.senders[i] = crypto.PubkeyToAddress(key.PublicKey)
	}
	if sort.SliceIsSorted(f.senders[:], func(i, j int) bool { return bytes.Compare(f.senders[i][:], f.senders[j][:]) < 0 }) {
		f.private[0], f.private[1] = f.private[1], f.private[0]
		f.senders[0], f.senders[1] = f.senders[1], f.senders[0]
	}
	for _, address := range f.senders {
		g.Alloc[address] = conf.GenesisAccount{Balance: "0x100000000000000000"}
	}
	for i := 0; i < 7; i++ {
		key, err := bls.RandKey()
		if err != nil {
			t.Fatal(err)
		}
		f.keys = append(f.keys, key)
		g.Config.HotStuff.Validators = append(g.Config.HotStuff.Validators, params.HotStuffValidatorConfig{Address: fmt.Sprintf("0x%040x", i+1), BLSKey: "0x" + hex.EncodeToString(key.PublicKey().Marshal())})
	}
	encoded, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &doc); err != nil {
		t.Fatal(err)
	}
	doc["extraData"], err = json.Marshal("0x" + hex.EncodeToString(g.ExtraData))
	if err != nil {
		t.Fatal(err)
	}
	f.genesisRaw, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db := mdbx.NewMDBX(log.New()).Path(f.dbPath).MustOpen()
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		var err error
		f.genesisBlock, _, err = (&internal.GenesisBlock{GenesisConfig: g}).Write(tx)
		return err
	}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	f.public = hotstuff.New(g.Config.HotStuff, g.Config)
	f.public.SetRewardFunc(apos.DoReward)
	if err := f.public.InitVerifierFromConfig(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.public.Close() })
	f.leader = hotstuff.New(g.Config.HotStuff, g.Config)
	set := f.public.Engine().ValidatorSetForView(1)
	index := hotstuff.LeaderForView(1, set)
	address, err := set.GetAddress(index)
	if err != nil {
		t.Fatal(err)
	}
	f.leader.Authorize(address, f.keys[int(index)])
	if err := f.leader.InitEngineFromConfig(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.leader.Close() })
	f.open(t)
	d := corpusDescriptorForTest()
	d.Identity = f.server.Identity()
	d.GenesisFileHash = types.Hash(sha256.Sum256(f.genesisRaw))
	d.SenderCount = 4
	d.NonceStart = 0
	d.NoncesPerSender = 1000
	d.ShardCount = 2
	d.FrameTransactions = 500
	d.RecipientCount = 17
	f.descriptor = d
	id, err := d.ID()
	if err != nil {
		t.Fatal(err)
	}
	manifest := CorpusManifest{Descriptor: d, CorpusID: id}
	var prefixRaws [][]byte
	for shard := uint16(0); shard < 2; shard++ {
		var contents bytes.Buffer
		for sequence := uint64(0); sequence < 4; sequence++ {
			sender, nonce, count, err := d.FrameRange(shard, sequence)
			if err != nil {
				t.Fatal(err)
			}
			raws := make([][]byte, count)
			for j := range raws {
				n := nonce + uint64(j)
				to := CorpusRecipient(uint32(uint64(sender)*d.NoncesPerSender+n), d.RecipientCount)
				tx, err := transaction.SignNewTx(f.private[sender], transaction.NewEIP155Signer(g.Config.ChainID), &transaction.LegacyTx{Nonce: n, To: &to, Gas: 21000, GasPrice: uint256.NewInt(1), Value: uint256.NewInt(1)})
				if err != nil {
					t.Fatal(err)
				}
				raws[j], err = tx.EthEncoded()
				if err != nil {
					t.Fatal(err)
				}
				if n < []uint64{0, 41, 42, 500}[sender] {
					prefixRaws = append(prefixRaws, raws[j])
				}
			}
			frame, err := MakeCorpusFrame(CorpusFrameID{CorpusID: id, Shard: shard, Sequence: sequence}, raws)
			if err != nil {
				t.Fatal(err)
			}
			if nonce == 0 {
				f.frames[sender] = frame
			}
			if err := WriteCorpusFrame(&contents, frame); err != nil {
				t.Fatal(err)
			}
		}
		name := fmt.Sprintf("shard-%02d.gcf", shard)
		if err := os.WriteFile(filepath.Join(f.sourceRoot, name), contents.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		manifest.Shards = append(manifest.Shards, CorpusShard{Index: shard, File: name, Transactions: 2000, Frames: 4, Bytes: uint64(contents.Len()), SHA256: types.Hash(sha256.Sum256(contents.Bytes()))})
	}
	f.manifestRaw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	b := f.execute(t, prefixRaws, 1)
	f.commit(t, b, 1, 5)
	f.closeOwner()
	f.open(t) // Physical MDBX close/reopen before plan arming.
	f.plan = ResumePlan{Version: 1, Identity: f.server.Identity(), ManifestSHA256: types.Hash(sha256.Sum256(f.manifestRaw)), CorpusID: id, GenesisFileSHA256: d.GenesisFileHash, RecoveryOuterSHA256: types.Hash{1}, RecoverySHA256: types.Hash{2}, OriginalRestoreSHA256: types.Hash{3}, Canonical: ResumeCanonicalAnchor{Number: 1, Hash: b.Hash(), Root: b.StateRoot()}, CanonicalNonceSum: 583}
	addresses := append([]types.Address(nil), f.senders[:]...)
	sort.Slice(addresses, func(i, j int) bool { return bytes.Compare(addresses[i][:], addresses[j][:]) < 0 })
	snapshot, err := f.chain.ReadPerformanceCanonicalAccounts(context.Background(), addresses)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Root != b.StateRoot() || snapshot.AppliedRoot != snapshot.Root {
		t.Fatal("physical baseline root differs")
	}
	for _, address := range addresses {
		a := snapshot.Accounts[address]
		f.baseline = append(f.baseline, CanonicalAccount{Address: address, Exists: snapshot.Exists[address], Nonce: a.Nonce, Balance: a.Balance.Dec(), CodeHash: a.CodeHash})
	}
	vector, err := json.Marshal(f.baseline)
	if err != nil {
		t.Fatal(err)
	}
	f.plan.AccountVectorSHA256 = types.Hash(sha256.Sum256(vector))
	scan, err := ScanResumeSources(context.Background(), f.sourceRoot, f.manifestRaw, f.genesisRaw, f.baseline)
	if err != nil {
		t.Fatal(err)
	}
	_, f.proofRaw, err = EncodeResumeSourceProof(f.manifestRaw, scan)
	if err != nil {
		t.Fatal(err)
	}
	f.plan.Senders, f.plan.Frames, f.plan.MappingSHA256 = scan.Senders, scan.Frames, scan.MappingSHA256
	f.plan.SourceProofSHA256 = types.Hash(sha256.Sum256(f.proofRaw))
	f.planID, err = ResumePlanID(f.plan, f.manifestRaw, f.baseline)
	if err != nil {
		t.Fatal(err)
	}
	f.planRaw, err = json.Marshal(ResumePlanDocument{Kind: "gov5-resume-four-frame-plan-v1", Plan: f.plan, PlanID: f.planID})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *resumeExecutionFixture) open(t *testing.T) {
	t.Helper()
	db := mdbx.NewMDBX(log.New()).Path(f.dbPath).MustOpen()
	ci, err := internal.NewBlockChain(context.Background(), f.genesisBlock, f.public, db, nil, f.genesis.Config)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	f.chain = ci.(*internal.BlockChain)
	root := commitment.NewQMDBRootComputer()
	if err := db.View(context.Background(), func(tx kv.Tx) error { root.SetCold(tx); return root.LoadFrom(tx) }); err != nil {
		db.Close()
		t.Fatal(err)
	}
	root.SetCold(nil)
	root.EnableUndoRecording()
	f.chain.SetQMDBRootComputer(root)
	f.server, err = New(f.chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	server, chain := f.server, f.chain
	var once sync.Once
	f.closeOwner = func() { once.Do(func() { server.Close(); _ = chain.Close(); db.Close() }) }
	t.Cleanup(f.closeOwner)
}
func (f *resumeExecutionFixture) options() ResumeIngressOptions {
	return ResumeIngressOptions{ManifestRaw: f.manifestRaw, GenesisRaw: f.genesisRaw, SourceRoot: f.sourceRoot, PlanDocumentRaw: f.planRaw, SourceProofRaw: f.proofRaw, ExpectedPlanID: f.planID, MaxTransactions: 1417, MaxEncodedBytes: 1 << 20}
}
func (f *resumeExecutionFixture) execute(t *testing.T, raws [][]byte, view hotstuff.ViewNumber) *block.Block {
	t.Helper()
	f.leader.Engine().RestoreState(view, f.lastQC, f.lastQC, 0)
	parent := f.chain.CurrentBlock()
	header := &block.Header{ParentHash: parent.Hash(), Number: uint256.NewInt(parent.Number64().Uint64() + 1), GasLimit: f.genesis.GasLimit, BaseFee: uint256.NewInt(1)}
	if err := f.leader.Prepare(f.chain, header); err != nil {
		t.Fatal(err)
	}
	var txs []*transaction.Transaction
	for _, raw := range raws {
		tx, err := transaction.DecodeEthereumTransaction(raw)
		if err != nil {
			t.Fatal(err)
		}
		txs = append(txs, tx)
	}
	header.TxHash = block.TxRootAt(txs, header.Time)
	built, err := f.server.Build(context.Background(), buildRequest(t, f.server.Identity(), block.NewBlock(header, txs).(*block.Block)))
	if err != nil {
		t.Fatal(err)
	}
	return f.sealImportBuilt(t, built)
}

func (f *resumeExecutionFixture) executeQueue(t *testing.T, view hotstuff.ViewNumber) *block.Block {
	t.Helper()
	f.leader.Engine().RestoreState(view, f.lastQC, f.lastQC, 0)
	parent := f.chain.CurrentBlock()
	header := &block.Header{ParentHash: parent.Hash(), Number: uint256.NewInt(parent.Number64().Uint64() + 1), GasLimit: f.genesis.GasLimit, BaseFee: uint256.NewInt(1)}
	if err := f.leader.Prepare(f.chain, header); err != nil {
		t.Fatal(err)
	}
	header.TxHash = block.TxRootAt(nil, header.Time)
	raw, err := block.NewBlock(header, nil).(*block.Block).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	status, err := f.server.StrictIngressStatus()
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(startExecutionSocket(t, f.server), f.server.Identity())
	defer client.Close()
	built, err := client.BuildFromQueue(context.Background(), QueueBuildRequest{Identity: f.server.Identity(), Epoch: status.Epoch, PreparedHash: crypto.Keccak256Hash(raw), PreparedRLP: raw, MaxTransactions: 1417, MaxEncodedBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	var unsigned block.Block
	if err := unsigned.Unmarshal(built.BlockRLP); err != nil {
		t.Fatal(err)
	}
	if len(unsigned.Transactions()) != 1417 {
		t.Fatal("owner queue did not select all original suffix transactions")
	}
	return f.sealImportBuilt(t, built.BuildResult)
}

func (f *resumeExecutionFixture) sealImportBuilt(t *testing.T, built BuildResult) *block.Block {
	t.Helper()
	var candidate block.Block
	if err := candidate.Unmarshal(built.BlockRLP); err != nil {
		t.Fatal(err)
	}
	output := make(chan block.IBlock, 1)
	if err := f.leader.Seal(f.chain, &candidate, output, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	b := (<-output).(*block.Block)
	encoded, err := b.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.server.Import(context.Background(), ImportRequest{Identity: f.server.Identity(), BlockHash: b.Hash(), BlockRLP: encoded})
	if err != nil || !result.Durable || result.Result != built.Result {
		t.Fatalf("real resume fixture import %+v %v", result, err)
	}
	return b
}
func (f *resumeExecutionFixture) commit(t *testing.T, b *block.Block, view hotstuff.ViewNumber, votes int) {
	t.Helper()
	qc := hotstuff.QuorumCertificate{View: view, BlockHash: b.Hash(), Signers: make([]bool, 7)}
	var signatures []blscommon.Signature
	for i := 0; i < votes; i++ {
		qc.Signers[i] = true
		signatures = append(signatures, f.keys[i].Sign(hotstuff.CommitSigningMessage(view, b.Hash())))
	}
	qc.AggregateSignature = bls.AggregateSignatures(signatures).Marshal()
	_, err := f.server.CommitCertified(context.Background(), CertifiedCommitRequest{CommitRequest: CommitRequest{Identity: f.server.Identity(), BlockHash: b.Hash(), ParentHash: b.ParentHash(), Number: b.Number64().Uint64()}, Certificate: qc})
	if votes < 5 {
		if err == nil {
			t.Fatal("four votes committed")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	f.lastQC = qc
}

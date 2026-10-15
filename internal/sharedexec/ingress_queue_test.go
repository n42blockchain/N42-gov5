package sharedexec

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

type strictIngressFixture struct {
	server    *Server
	chain     *internal.BlockChain
	leader    *hotstuff.HotStuff
	template  *block.Block
	private   [2]*ecdsa.PrivateKey
	from      [2]types.Address
	keys      []blscommon.SecretKey
	frames    [2]CorpusFrame
	recipient types.Address
}

func newStrictIngressFixture(t *testing.T, maxTx, maxBytes int, frameSize ...uint16) strictIngressFixture {
	t.Helper()
	var private [2]*ecdsa.PrivateKey
	var from [2]types.Address
	for i := range private {
		var err error
		private[i], err = crypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		from[i] = crypto.PubkeyToAddress(private[i].PublicKey)
	}
	var keys []blscommon.SecretKey
	chain, _, leader, template, recipient, _ := unsignedBuildFixtureWithSet(t, 7, func(k []blscommon.SecretKey) { keys = k }, func(g *conf.Genesis) {
		g.BaseFee = uint256.NewInt(1)
		for _, address := range from {
			g.Alloc[address] = conf.GenesisAccount{Balance: "0x56bc75e2d63100000"}
		}
	})
	server, err := New(chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.StrictIngressStatus(); err == nil {
		t.Fatal("default owner silently enabled ingress")
	}
	d := corpusDescriptorForTest()
	d.Identity = server.Identity()
	d.SenderCount = 2
	d.ShardCount = 2
	d.NonceStart = 0
	d.NoncesPerSender = 3
	d.FrameTransactions = 3
	if len(frameSize) > 0 {
		d.FrameTransactions = frameSize[0]
	}
	id, err := d.ID()
	if err != nil {
		t.Fatal(err)
	}
	manifest := CorpusManifest{Descriptor: d, CorpusID: id}
	var frames [2]CorpusFrame
	for i, key := range private {
		var raws [][]byte
		for nonce := uint64(0); nonce < 3; nonce++ {
			txn, err := transaction.SignNewTx(key, transaction.NewEIP155Signer(chain.Config().ChainID), &transaction.LegacyTx{Nonce: nonce, To: &recipient, Gas: 21000, GasPrice: uint256.NewInt(1), Value: uint256.NewInt(1)})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := txn.EthEncoded()
			if err != nil {
				t.Fatal(err)
			}
			raws = append(raws, raw)
		}
		var encoded bytes.Buffer
		frameCount := uint64(0)
		for start := 0; start < len(raws); start += int(d.FrameTransactions) {
			end := start + int(d.FrameTransactions)
			if end > len(raws) {
				end = len(raws)
			}
			frame, err := MakeCorpusFrame(CorpusFrameID{CorpusID: id, Shard: uint16(i), Sequence: frameCount}, raws[start:end])
			if err != nil {
				t.Fatal(err)
			}
			if frameCount == 0 {
				frames[i] = frame
			}
			frameCount++
			if err := WriteCorpusFrame(&encoded, frame); err != nil {
				t.Fatal(err)
			}
		}
		manifest.Shards = append(manifest.Shards, CorpusShard{Index: uint16(i), File: fmt.Sprintf("shard-%02d.gcf", i), SHA256: types.Hash(sha256.Sum256(encoded.Bytes())), Bytes: uint64(encoded.Len()), Frames: frameCount, Transactions: 3})
	}
	encodedManifest, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.EnableStrictIngress(StrictIngressOptions{Manifest: encodedManifest, MaxTransactions: maxTx, MaxEncodedBytes: maxBytes}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	return strictIngressFixture{server: server, chain: chain, leader: leader, template: template, private: private, from: from, keys: keys, frames: frames, recipient: recipient}
}

func TestStrictIngressRealExecutionAndCertifiedCanonicalRelease(t *testing.T) {
	testStrictIngressExecution(t, false)
}

func TestStrictIngressUnixRealExecutionAndCertifiedCanonicalRelease(t *testing.T) {
	testStrictIngressExecution(t, true)
}

func testStrictIngressExecution(t *testing.T, transport bool) {
	ctx := context.Background()
	f := newStrictIngressFixture(t, 10, 1<<20)
	server, chain, leader, template, frames, keys, from, recipient := f.server, f.chain, f.leader, f.template, f.frames, f.keys, f.from, f.recipient
	status, err := server.StrictIngressStatus()
	if err != nil {
		t.Fatal(err)
	}
	admit := func(frame CorpusFrame) (StrictIngressAck, error) {
		return server.AdmitStrictFrame(ctx, server.Identity(), status.Epoch, frame)
	}
	if transport {
		path := startStrictIngressSocket(t, server)
		admit = func(frame CorpusFrame) (StrictIngressAck, error) {
			c, err := DialStrictIngress(ctx, path, server.Identity(), status.ManifestHash, frame.Shard)
			if err != nil {
				return StrictIngressAck{}, err
			}
			defer c.Close()
			return c.Admit(ctx, frame)
		}
	}
	for _, frame := range frames {
		ack, err := admit(frame)
		if err != nil || ack.NewlyAdmitted != 3 {
			t.Fatalf("strict signed admission: %+v %v", ack, err)
		}
	}
	duplicate, err := admit(frames[0])
	if err != nil || duplicate.NewlyAdmitted != 0 || duplicate.Duplicates != 3 {
		t.Fatalf("duplicate: %+v %v", duplicate, err)
	}
	status, _ = server.StrictIngressStatus()
	if status.PendingTransactions != 6 || status.NewlyAdmitted != 6 || status.CryptoVerified != 6 || status.ReservedTransactions != 0 || status.ReservedEncodedBytes != 0 {
		t.Fatalf("admitted counters %+v", status)
	}
	raws, err := server.ingressTransactions(10)
	if err != nil {
		t.Fatal(err)
	}
	var transactions []*transaction.Transaction
	for _, raw := range raws {
		txn, err := transaction.DecodeEthereumTransaction(raw)
		if err != nil {
			t.Fatal(err)
		}
		transactions = append(transactions, txn)
	}
	header := block.CopyHeader(template.Header().(*block.Header))
	header.TxHash = block.TxRootAt(transactions, header.Time)
	header.ResetHashCache()
	selected := block.NewBlock(header, transactions).(*block.Block)
	client := NewClient(startExecutionSocket(t, server), server.Identity())
	defer client.Close()
	built, err := client.Build(ctx, buildRequest(t, server.Identity(), selected))
	if err != nil {
		t.Fatal(err)
	}
	var candidate block.Block
	if err := candidate.Unmarshal(built.BlockRLP); err != nil {
		t.Fatal(err)
	}
	sealed := make(chan block.IBlock, 1)
	if err := leader.Seal(chain, &candidate, sealed, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	b := (<-sealed).(*block.Block)
	raw, err := b.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	imported, err := client.Import(ctx, ImportRequest{Identity: server.Identity(), BlockHash: b.Hash(), BlockRLP: raw})
	if err != nil || !imported.Durable || imported.Result != built.Result {
		t.Fatalf("real execution %+v %v", imported, err)
	}
	status, _ = server.StrictIngressStatus()
	if status.PendingTransactions != 6 {
		t.Fatal("build/import released uncommitted queue")
	}
	request := CommitRequest{Identity: server.Identity(), BlockHash: b.Hash(), ParentHash: b.ParentHash(), Number: 1}
	if _, err := client.CommitCertified(ctx, request, commitProof(keys, b.Hash(), 4, false)); err == nil {
		t.Fatal("four votes committed")
	}
	status, _ = server.StrictIngressStatus()
	if status.PendingTransactions != 6 || status.Reconciling {
		t.Fatalf("rejected proof pruned or wedged queue %+v", status)
	}
	for range 2 {
		committed, err := client.CommitCertified(ctx, request, commitProof(keys, b.Hash(), 5, false))
		if err != nil || !committed.Committed {
			t.Fatalf("certified commit %+v %v", committed, err)
		}
	}
	status, _ = server.StrictIngressStatus()
	if status.PendingTransactions != 0 || status.RetainedEncodedBytes != 0 || status.ReservedTransactions != 0 || status.Reconciling || status.CanonicalHash != b.Hash() || status.CanonicalNumber != 1 {
		t.Fatalf("canonical queue release %+v", status)
	}
	server.ingress.mu.Lock()
	for _, lane := range server.ingress.lanes {
		if lane.entries != nil {
			t.Error("empty lane retained backing metadata array")
		}
	}
	server.ingress.mu.Unlock()
	snapshot, err := chain.ReadPerformanceCanonicalAccounts(ctx, []types.Address{from[0], from[1], recipient})
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range from {
		if snapshot.Accounts[address].Nonce != 3 {
			t.Fatal("canonical sender nonce differs")
		}
	}
	received := snapshot.Accounts[recipient]
	if received.Balance.Uint64() != 7 {
		t.Fatalf("canonical transfer balance %s", received.Balance.Dec())
	}
	if err := chain.DB().View(ctx, func(tx kv.Tx) error {
		receipts, err := rawdb.ReadReceiptsByHash(tx, b.Hash())
		if err != nil {
			return err
		}
		if len(receipts) != 6 {
			t.Fatal("canonical receipts missing")
		}
		for i, r := range receipts {
			if r.Status != 1 || r.CumulativeGasUsed != uint64(i+1)*21000 {
				t.Fatal("receipt differs from actual successful transfer")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func strictFrameRaws(t *testing.T, frame CorpusFrame) [][]byte {
	t.Helper()
	var raws [][]byte
	if err := WalkCorpusPayload(frame.Payload, frame.Transactions, func(raw []byte) error { raws = append(raws, append([]byte(nil), raw...)); return nil }); err != nil {
		t.Fatal(err)
	}
	return raws
}

func strictExecute(t *testing.T, f strictIngressFixture, raws [][]byte) *block.Block {
	t.Helper()
	ctx := context.Background()
	var txs []*transaction.Transaction
	for _, raw := range raws {
		tx, err := transaction.DecodeEthereumTransaction(raw)
		if err != nil {
			t.Fatal(err)
		}
		txs = append(txs, tx)
	}
	h := block.CopyHeader(f.template.Header().(*block.Header))
	h.TxHash = block.TxRootAt(txs, h.Time)
	h.ResetHashCache()
	result, err := f.server.Build(ctx, buildRequest(t, f.server.Identity(), block.NewBlock(h, txs).(*block.Block)))
	if err != nil {
		t.Fatal(err)
	}
	var candidate block.Block
	if err := candidate.Unmarshal(result.BlockRLP); err != nil {
		t.Fatal(err)
	}
	sealed := make(chan block.IBlock, 1)
	if err := f.leader.Seal(f.chain, &candidate, sealed, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	b := (<-sealed).(*block.Block)
	raw, err := b.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	imported, err := f.server.Import(ctx, ImportRequest{Identity: f.server.Identity(), BlockHash: b.Hash(), BlockRLP: raw})
	if err != nil || !imported.Durable || imported.Result != result.Result {
		t.Fatalf("real strict fixture import %+v %v", imported, err)
	}
	return b
}

func strictCommit(t *testing.T, f strictIngressFixture, b *block.Block, ctx context.Context) error {
	t.Helper()
	_, err := f.server.CommitCertified(ctx, CertifiedCommitRequest{CommitRequest: CommitRequest{Identity: f.server.Identity(), BlockHash: b.Hash(), ParentHash: b.ParentHash(), Number: b.Number64().Uint64()}, Certificate: *commitProof(f.keys, b.Hash(), 5, false)})
	return err
}

func TestStrictIngressRejectsWholeInvalidFrameAndReleasesReservations(t *testing.T) {
	for _, mode := range []string{"gap", "wrong-chain", "bad-signature", "insufficient", "under-basefee"} {
		t.Run(mode, func(t *testing.T) {
			f := newStrictIngressFixture(t, 10, 1<<20)
			status, _ := f.server.StrictIngressStatus()
			raws := strictFrameRaws(t, f.frames[0])
			if mode == "gap" {
				raws[1] = raws[2]
			} else {
				chainID := new(big.Int).Set(f.chain.Config().ChainID)
				if mode == "wrong-chain" {
					chainID.Add(chainID, big.NewInt(1))
				}
				value, price := uint256.NewInt(1), uint256.NewInt(1)
				if mode == "insufficient" {
					value.Lsh(value, 250)
				}
				if mode == "under-basefee" {
					price.Clear()
				}
				txn, err := transaction.SignNewTx(f.private[0], transaction.NewEIP155Signer(chainID), &transaction.LegacyTx{Nonce: 1, To: &f.recipient, Gas: 21000, GasPrice: price, Value: value})
				if err != nil {
					t.Fatal(err)
				}
				if mode == "bad-signature" {
					v, _, _ := txn.RawSignatureValues()
					txn, err = txn.WithSignatureValues(v, uint256.NewInt(0), uint256.NewInt(1))
					if err != nil {
						t.Fatal(err)
					}
				}
				raws[1], err = txn.EthEncoded()
				if err != nil {
					t.Fatal(err)
				}
			}
			frame, err := MakeCorpusFrame(f.frames[0].CorpusFrameID, raws)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.server.AdmitStrictFrame(context.Background(), f.server.Identity(), status.Epoch, frame); err == nil {
				t.Fatal("invalid frame admitted")
			}
			after, _ := f.server.StrictIngressStatus()
			if after.PendingTransactions != 0 || after.RetainedEncodedBytes != 0 || after.ReservedTransactions != 0 || after.ReservedEncodedBytes != 0 || after.NewlyAdmitted != 0 || len(f.server.ingress.lanes) != 0 {
				t.Fatalf("partial admission %+v", after)
			}
			if ack, err := f.server.AdmitStrictFrame(context.Background(), f.server.Identity(), status.Epoch, f.frames[0]); err != nil || ack.NewlyAdmitted != 3 {
				t.Fatalf("valid retry after rejection %+v %v", ack, err)
			}
		})
	}
}

func TestStrictIngressCreditsConflictIdentityAndPayloadOwnership(t *testing.T) {
	f := newStrictIngressFixture(t, 3, 1<<20)
	status, _ := f.server.StrictIngressStatus()
	ctx := context.Background()
	wrong := f.server.Identity()
	wrong.GenesisHash[0] ^= 1
	if _, err := f.server.AdmitStrictFrame(ctx, wrong, status.Epoch, f.frames[0]); err == nil {
		t.Fatal("wrong identity admitted")
	}
	epoch := status.Epoch
	epoch[0] ^= 1
	if _, err := f.server.AdmitStrictFrame(ctx, f.server.Identity(), epoch, f.frames[0]); err == nil {
		t.Fatal("wrong epoch admitted")
	}
	original := strictFrameRaws(t, f.frames[0])
	if _, err := f.server.AdmitStrictFrame(ctx, f.server.Identity(), status.Epoch, f.frames[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.AdmitStrictFrame(ctx, f.server.Identity(), status.Epoch, f.frames[1]); err == nil {
		t.Fatal("transaction credit overflow")
	}
	f.frames[0].Payload[4] ^= 1
	if _, err := f.server.AdmitStrictFrame(ctx, f.server.Identity(), status.Epoch, f.frames[0]); err == nil {
		t.Fatal("memo bypassed payload validation")
	}
	selected, err := f.server.ingressTransactions(3)
	if err != nil {
		t.Fatal(err)
	}
	for i := range selected {
		if !bytes.Equal(selected[i], original[i]) {
			t.Fatal("caller mutated retained slab")
		}
	}
	selected[0][0] ^= 1
	again, err := f.server.ingressTransactions(3)
	if err != nil || !bytes.Equal(again[0], original[0]) {
		t.Fatal("selector leaked mutable payload")
	}
	f.server.Close()
	closed, _ := f.server.StrictIngressStatus()
	if closed.PendingTransactions != 0 || closed.RetainedEncodedBytes != 0 {
		t.Fatal("close retained queue")
	}
	// Independent byte credits reject before signature verification or mutation.
	g := newStrictIngressFixture(t, 10, 1)
	gs, _ := g.server.StrictIngressStatus()
	if _, err := g.server.AdmitStrictFrame(ctx, g.server.Identity(), gs.Epoch, g.frames[0]); err == nil {
		t.Fatal("encoded byte credit overflow")
	}
	gs, _ = g.server.StrictIngressStatus()
	if gs.CryptoVerified != 0 || gs.PendingTransactions != 0 || gs.ReservedEncodedBytes != 0 {
		t.Fatal("byte rejection allocated admission state")
	}
}

func TestStrictIngressPartialCanonicalCommitKeepsWholeSlabCharged(t *testing.T) {
	f := newStrictIngressFixture(t, 10, 1<<20)
	ctx := context.Background()
	status, _ := f.server.StrictIngressStatus()
	for _, frame := range f.frames {
		if _, err := f.server.AdmitStrictFrame(ctx, f.server.Identity(), status.Epoch, frame); err != nil {
			t.Fatal(err)
		}
	}
	raws, err := f.server.ingressTransactions(1)
	if err != nil {
		t.Fatal(err)
	}
	b := strictExecute(t, f, raws)
	if _, err := f.chain.ReadPerformanceCanonicalAccounts(ctx, f.from[:]); err == nil {
		t.Fatal("speculative applied nonce masqueraded as canonical")
	}
	if err := strictCommit(t, f, b, ctx); err != nil {
		t.Fatal(err)
	}
	status, _ = f.server.StrictIngressStatus()
	if status.PendingTransactions != 5 || status.RetainedEncodedBytes != len(f.frames[0].Payload)+len(f.frames[1].Payload) {
		t.Fatalf("partial slab charged incorrectly %+v", status)
	}
	f.server.ingress.mu.Lock()
	for _, lane := range f.server.ingress.lanes {
		if len(lane.entries) == 2 && cap(lane.entries) > 2*len(lane.entries) {
			t.Error("partial prune retained oversized metadata backing array")
		}
	}
	f.server.ingress.mu.Unlock()
	if err := strictCommit(t, f, b, ctx); err != nil {
		t.Fatal(err)
	}
	after, _ := f.server.StrictIngressStatus()
	if after.PendingTransactions != 5 || after.RetainedEncodedBytes != status.RetainedEncodedBytes {
		t.Fatal("duplicate canonical prune released twice")
	}
}

type cancelAfterCanonical struct {
	kv.RwDB
	hash             types.Hash
	cancel           context.CancelFunc
	triggered        bool
	failOwnerContext context.Context
	failOwnerRead    bool
}

func (db *cancelAfterCanonical) View(ctx context.Context, fn func(kv.Tx) error) error {
	if db.triggered && db.failOwnerRead && ctx == db.failOwnerContext {
		db.failOwnerRead = false
		return errors.New("injected owner reconciliation read failure")
	}
	return db.RwDB.View(ctx, fn)
}

func (db *cancelAfterCanonical) Update(ctx context.Context, fn func(kv.RwTx) error) error {
	err := db.RwDB.Update(ctx, fn)
	if err == nil && !db.triggered {
		_ = db.RwDB.View(context.Background(), func(tx kv.Tx) error {
			if rawdb.ReadHeadBlockHash(tx) == db.hash && rawdb.ReadHotStuffCommittedHead(tx) == db.hash {
				db.triggered = true
				db.cancel()
			}
			return nil
		})
	}
	return err
}

func TestStrictIngressCanceledCommitReconcilesWithOwnerLifetime(t *testing.T) {
	f := newStrictIngressFixture(t, 10, 1<<20)
	status, _ := f.server.StrictIngressStatus()
	ctx := context.Background()
	for _, frame := range f.frames {
		if _, err := f.server.AdmitStrictFrame(ctx, f.server.Identity(), status.Epoch, frame); err != nil {
			t.Fatal(err)
		}
	}
	raws, err := f.server.ingressTransactions(10)
	if err != nil {
		t.Fatal(err)
	}
	b := strictExecute(t, f, raws)
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	wrapped := &cancelAfterCanonical{RwDB: f.chain.DB(), hash: b.Hash(), cancel: cancel}
	f.chain.ChainDB = wrapped
	err = strictCommit(t, f, b, requestCtx)
	if !wrapped.triggered || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation did not cover post-mutation read: %v %v", wrapped.triggered, err)
	}
	status, _ = f.server.StrictIngressStatus()
	if status.Reconciling || status.PendingTransactions != 0 || status.RetainedEncodedBytes != 0 || status.CanonicalHash != b.Hash() {
		t.Fatalf("cancel left stale queue %+v", status)
	}
}

func TestStrictIngressCanonicalGenerationRejectsSnapshotAfterDirtyCleared(t *testing.T) {
	f := newStrictIngressFixture(t, 10, 1<<20, 1)
	ctx := context.Background()
	q := f.server.ingress
	q.mu.Lock()
	oldStamp := q.stamp
	q.mu.Unlock()
	oldSnapshot, err := f.chain.ReadPerformanceCanonicalAccounts(ctx, []types.Address{f.from[0], f.recipient})
	if err != nil {
		t.Fatal(err)
	}
	// Pause at the actual publish helper boundary, after account snapshot read.
	// Another ingress source commits nonce zero before this frame publishes.
	b := strictExecute(t, f, strictFrameRaws(t, f.frames[0]))
	if err := strictCommit(t, f, b, ctx); err != nil {
		t.Fatal(err)
	}
	status, _ := f.server.StrictIngressStatus()
	if status.Reconciling || status.CanonicalNumber != 1 {
		t.Fatal("fixture did not finish canonical reconciliation")
	}
	if _, err := f.server.admitVerifiedIngress(ctx, q, f.frames[0], oldStamp, oldSnapshot, nil, nil); !errors.Is(err, ErrIngressSnapshotChanged) {
		t.Fatalf("old snapshot survived completed reconcile: %v", err)
	}
	if len(q.lanes) != 0 {
		t.Fatal("stale publish created a new sender lane")
	}
	if _, err := f.server.AdmitStrictFrame(ctx, f.server.Identity(), status.Epoch, f.frames[0]); err == nil {
		t.Fatal("canonical nonce zero re-admitted")
	}
	txn, err := transaction.SignNewTx(f.private[0], transaction.NewEIP155Signer(f.chain.Config().ChainID), &transaction.LegacyTx{Nonce: 1, To: &f.recipient, Gas: 21000, GasPrice: uint256.NewInt(1), Value: uint256.NewInt(1)})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := txn.EthEncoded()
	if err != nil {
		t.Fatal(err)
	}
	id := f.frames[0].CorpusFrameID
	id.Sequence = 1
	frame, err := MakeCorpusFrame(id, [][]byte{raw})
	if err != nil {
		t.Fatal(err)
	}
	ack, err := f.server.AdmitStrictFrame(ctx, f.server.Identity(), status.Epoch, frame)
	if err != nil || ack.NewlyAdmitted != 1 {
		t.Fatalf("fresh canonical nonce one %+v %v", ack, err)
	}
	q.mu.Lock()
	q.reconciling = true
	q.stamp.generation++
	q.mu.Unlock()
	if _, err := f.server.AdmitStrictFrame(ctx, f.server.Identity(), status.Epoch, frame); err == nil {
		t.Fatal("admitted while canonical mutation is in progress")
	}
	if err := strictCommit(t, f, b, ctx); err != nil {
		t.Fatal(err)
	}
	after, _ := f.server.StrictIngressStatus()
	if after.Reconciling || after.NewlyAdmitted != 1 || after.PendingTransactions != 1 || after.ReservedTransactions != 0 || after.ReservedEncodedBytes != 0 {
		t.Fatalf("snapshot race altered counters %+v", after)
	}
}

func TestStrictIngressCanonicalReadFailureKeepsAdmissionClosed(t *testing.T) {
	f := newStrictIngressFixture(t, 10, 1<<20)
	ctx := context.Background()
	status, _ := f.server.StrictIngressStatus()
	for _, frame := range f.frames {
		if _, err := f.server.AdmitStrictFrame(ctx, f.server.Identity(), status.Epoch, frame); err != nil {
			t.Fatal(err)
		}
	}
	raws, err := f.server.ingressTransactions(10)
	if err != nil {
		t.Fatal(err)
	}
	b := strictExecute(t, f, raws)
	wrapped := &cancelAfterCanonical{RwDB: f.chain.DB(), hash: b.Hash(), cancel: func() {}, failOwnerRead: true, failOwnerContext: f.server.ctx}
	f.chain.ChainDB = wrapped
	if err := strictCommit(t, f, b, ctx); err == nil {
		t.Fatal("owner read failure was hidden")
	}
	status, _ = f.server.StrictIngressStatus()
	if !wrapped.triggered || !status.Reconciling || status.PendingTransactions != 6 || status.CanonicalNumber != 0 || f.chain.CurrentBlock().Hash() != b.Hash() {
		t.Fatalf("canonical failure did not retain closed queue %+v", status)
	}
	if _, err := f.server.AdmitStrictFrame(ctx, f.server.Identity(), status.Epoch, f.frames[0]); err == nil {
		t.Fatal("admission bypassed dirty canonical gate")
	}
	if err := strictCommit(t, f, b, ctx); err != nil {
		t.Fatal(err)
	}
	status, _ = f.server.StrictIngressStatus()
	if status.Reconciling || status.PendingTransactions != 0 || status.RetainedEncodedBytes != 0 || status.CanonicalHash != b.Hash() {
		t.Fatalf("duplicate did not reconcile durable prefix %+v", status)
	}
}

func TestStrictIngressConcurrentFrameIdentityAndCreditPublication(t *testing.T) {
	for _, different := range []bool{false, true} {
		t.Run(fmt.Sprint("different_payload=", different), func(t *testing.T) {
			f := newStrictIngressFixture(t, 6, 1<<20)
			q := f.server.ingress
			ctx := context.Background()
			frames := [2]CorpusFrame{f.frames[0], f.frames[0]}
			if different {
				frames[1] = f.frames[1]
				frames[1].CorpusFrameID = frames[0].CorpusFrameID
			}
			snapshot, err := f.chain.ReadPerformanceCanonicalAccounts(ctx, []types.Address{f.from[0], f.from[1], f.recipient})
			if err != nil {
				t.Fatal(err)
			}
			q.mu.Lock()
			stamp := q.stamp
			q.mu.Unlock()
			var prepared [2][]ingressVerified
			var reservations [2]*ingressReservation
			// Both requests have passed the initial lookup and direct signature
			// recovery before either reaches the final publication mutex.
			for i, frame := range frames {
				if err := frame.Validate(q.descriptor); err != nil {
					t.Fatal(err)
				}
				offset := 0
				for _, raw := range strictFrameRaws(t, frame) {
					txn, err := transaction.DecodeEthereumTransaction(raw)
					if err != nil {
						t.Fatal(err)
					}
					from, err := transaction.NewEIP155Signer(f.chain.Config().ChainID).Sender(txn)
					if err != nil {
						t.Fatal(err)
					}
					start := offset + 4
					offset = start + len(raw)
					prepared[i] = append(prepared[i], ingressVerified{sender: from, nonce: txn.Nonce(), hash: txn.Hash(), start: start, end: offset, cost: *uint256.NewInt(21001), gasPrice: *uint256.NewInt(1)})
				}
				q.mu.Lock()
				q.reservedTx += 3
				q.reservedBytes += len(frame.Payload)
				q.mu.Unlock()
				reservations[i] = &ingressReservation{queue: q, transactions: 3, bytes: len(frame.Payload), held: true}
			}
			type outcome struct {
				index  int
				ack    StrictIngressAck
				err    error
				status StrictIngressStatus
			}
			results := make(chan outcome, 2)
			var wg sync.WaitGroup
			for i := range frames {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					defer reservations[i].release()
					ack, err := f.server.admitVerifiedIngress(ctx, q, frames[i], stamp, snapshot, prepared[i], reservations[i])
					status, _ := f.server.StrictIngressStatus()
					results <- outcome{index: i, ack: ack, err: err, status: status}
				}(i)
			}
			wg.Wait()
			close(results)
			newCount, duplicates, failures, winner := 0, 0, 0, -1
			for result := range results {
				if result.status.PendingTransactions+result.status.ReservedTransactions > q.maxTx || result.status.RetainedEncodedBytes+result.status.ReservedEncodedBytes > q.maxBytes {
					t.Fatal("publication double charged in-flight and retained credits")
				}
				if result.err != nil {
					failures++
					continue
				}
				newCount += result.ack.NewlyAdmitted
				duplicates += result.ack.Duplicates
				if result.ack.NewlyAdmitted != 0 {
					winner = result.index
				}
			}
			if newCount != 3 || (different && failures != 1) || (!different && (failures != 0 || duplicates != 3)) {
				t.Fatalf("concurrent frame outcomes new=%d dup=%d errors=%d", newCount, duplicates, failures)
			}
			status, _ := f.server.StrictIngressStatus()
			if winner < 0 || status.NewlyAdmitted != 3 || status.PendingTransactions != 3 || status.RetainedEncodedBytes != len(frames[winner].Payload) || status.ReservedTransactions != 0 || status.ReservedEncodedBytes != 0 || len(q.lanes) != 1 || len(q.recent[0]) != 1 {
				t.Fatalf("conflicting frame changed retained state %+v", status)
			}
		})
	}
}

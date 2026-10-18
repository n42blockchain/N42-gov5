package sharedexec

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func queueRequest(t *testing.T, f strictIngressFixture, header *block.Header, limit int) QueueBuildRequest {
	t.Helper()
	h := block.CopyHeader(header)
	h.TxHash = block.TxRootAt(nil, h.Time)
	h.ResetHashCache()
	raw, err := block.NewBlock(h, nil).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	status, _ := f.server.StrictIngressStatus()
	return QueueBuildRequest{Identity: f.server.Identity(), Epoch: status.Epoch, PreparedHash: crypto.Keccak256Hash(raw), PreparedRLP: raw, MaxTransactions: limit, MaxEncodedBytes: 1 << 20}
}
func queueSealImport(t *testing.T, f strictIngressFixture, client *Client, leader *hotstuff.HotStuff, built QueueBuildResult) *block.Block {
	t.Helper()
	var unsigned block.Block
	if err := unsigned.Unmarshal(built.BlockRLP); err != nil {
		t.Fatal(err)
	}
	sealed := make(chan block.IBlock, 1)
	if err := leader.Seal(f.chain, &unsigned, sealed, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	b := (<-sealed).(*block.Block)
	raw, err := b.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	imported, err := client.Import(context.Background(), ImportRequest{Identity: f.server.Identity(), BlockHash: b.Hash(), BlockRLP: raw})
	if err != nil || !imported.Durable || imported.Result != built.Result {
		t.Fatalf("queue actual import %+v %v", imported, err)
	}
	return b
}
func queueQC(keys []blscommon.SecretKey, hash types.Hash, view hotstuff.ViewNumber, count int) *hotstuff.QuorumCertificate {
	qc := &hotstuff.QuorumCertificate{View: view, BlockHash: hash, Signers: make([]bool, len(keys))}
	var signatures []blscommon.Signature
	for i := 0; i < count; i++ {
		qc.Signers[i] = true
		signatures = append(signatures, keys[i].Sign(hotstuff.CommitSigningMessage(view, hash)))
	}
	qc.AggregateSignature = bls.AggregateSignatures(signatures).Marshal()
	return qc
}
func TestQueueBuildUnixTwoLeadersConsumeOneStrictQueue(t *testing.T) {
	ctx := context.Background()
	f := newStrictIngressFixture(t, 6, 1<<20)
	path := startStrictIngressSocket(t, f.server)
	status, _ := f.server.StrictIngressStatus()
	for _, frame := range f.frames {
		ingress, err := DialStrictIngress(ctx, path, f.server.Identity(), status.ManifestHash, frame.Shard)
		if err != nil {
			t.Fatal(err)
		}
		ack, err := ingress.Admit(ctx, frame)
		_ = ingress.Close()
		if err != nil || ack.NewlyAdmitted != 3 {
			t.Fatalf("strict socket %+v %v", ack, err)
		}
	}
	client := NewClient(startExecutionSocket(t, f.server), f.server.Identity())
	defer client.Close()
	request := queueRequest(t, f, f.template.Header().(*block.Header), 4)
	first, err := client.BuildFromQueue(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	var unsigned block.Block
	if err := unsigned.Unmarshal(first.BlockRLP); err != nil {
		t.Fatal(err)
	}
	if len(unsigned.Transactions()) != 4 || first.Result.GasUsed != 4*21000 {
		t.Fatal("queue selection did not execute four transfers")
	}
	b := queueSealImport(t, f, client, f.leader, first)
	// Actual applied state advanced, but the pinned successful result survives.
	again, err := client.BuildFromQueue(ctx, request)
	if err != nil || again.BlockHash != first.BlockHash {
		t.Fatalf("pinned request retry %v", err)
	}
	commit := CommitRequest{Identity: f.server.Identity(), BlockHash: b.Hash(), ParentHash: b.ParentHash(), Number: 1}
	if _, err := client.CommitCertified(ctx, commit, queueQC(f.keys, b.Hash(), 1, 4)); err == nil {
		t.Fatal("four votes committed queue")
	}
	status, _ = f.server.StrictIngressStatus()
	if status.PendingTransactions != 6 {
		t.Fatal("build/import/invalid QC released queue")
	}
	if _, err := client.CommitCertified(ctx, commit, queueQC(f.keys, b.Hash(), 1, 5)); err != nil {
		t.Fatal(err)
	}
	status, _ = f.server.StrictIngressStatus()
	if status.PendingTransactions != 2 {
		t.Fatalf("canonical prune %+v", status)
	}
	if _, err := client.BuildFromQueue(ctx, request); err == nil {
		t.Fatal("expired request was recreated")
	}
	f.server.mu.Lock()
	plans, bytes := len(f.server.queuePlans), f.server.buildBytes
	f.server.mu.Unlock()
	if plans != 0 || bytes != 0 {
		t.Fatalf("canonical plan retained %d/%d", plans, bytes)
	}
	cfg := f.chain.Config()
	second := hotstuff.New(cfg.HotStuff, cfg)
	defer second.Close()
	set := f.chain.Engine().(*hotstuff.HotStuff).Engine().ValidatorSetForView(2)
	index := hotstuff.LeaderForView(2, set)
	address, err := set.GetAddress(index)
	if err != nil {
		t.Fatal(err)
	}
	second.Authorize(address, f.keys[int(index)])
	if err := second.InitEngineFromConfig(); err != nil {
		t.Fatal(err)
	}
	proof := queueQC(f.keys, b.Hash(), 1, 5)
	second.Engine().RestoreState(2, *proof, *proof, 0)
	header := &block.Header{ParentHash: b.Hash(), Number: uint256.NewInt(2), GasLimit: b.GasLimit(), Difficulty: uint256.NewInt(0)}
	header.BaseFee, _ = uint256.FromBig(misc.CalcBaseFee(cfg, b.Header().(*block.Header)))
	if err := second.Prepare(f.chain, header); err != nil {
		t.Fatal(err)
	}
	next, err := client.BuildFromQueue(ctx, queueRequest(t, f, header, 4))
	if err != nil {
		t.Fatal(err)
	}
	b2 := queueSealImport(t, f, client, second, next)
	if len(b2.Transactions()) != 2 {
		t.Fatal("second leader did not consume remaining queue")
	}
	commit = CommitRequest{Identity: f.server.Identity(), BlockHash: b2.Hash(), ParentHash: b.Hash(), Number: 2}
	for range 2 {
		if _, err := client.CommitCertified(ctx, commit, queueQC(f.keys, b2.Hash(), 2, 5)); err != nil {
			t.Fatal(err)
		}
	}
	status, _ = f.server.StrictIngressStatus()
	if status.PendingTransactions != 0 || status.RetainedEncodedBytes != 0 || status.NewlyAdmitted != 6 || status.CanonicalNumber != 2 {
		t.Fatalf("two leader final queue %+v", status)
	}
	accounts, err := f.chain.ReadPerformanceCanonicalAccounts(ctx, []types.Address{f.from[0], f.from[1], f.recipient})
	if err != nil {
		t.Fatal(err)
	}
	for _, sender := range f.from {
		if accounts.Accounts[sender].Nonce != 3 {
			t.Fatal("queue nonce continuity differs")
		}
	}
	received := accounts.Accounts[f.recipient]
	if received.Balance.Uint64() != 7 {
		t.Fatal("queue canonical balance differs")
	}
	if _, key := f.chain.Engine().(*hotstuff.HotStuff).SignerKey(); key != nil {
		t.Fatal("owner acquired leader key")
	}
	if err := f.chain.DB().View(ctx, func(tx kv.Tx) error {
		for _, candidate := range []*block.Block{b, b2} {
			receipts, err := rawdb.ReadReceiptsByHash(tx, candidate.Hash())
			if err != nil {
				return err
			}
			if len(receipts) != len(candidate.Transactions()) {
				t.Fatal("queue receipt count differs")
			}
			for i, r := range receipts {
				if r.Status != 1 || r.CumulativeGasUsed != uint64(i+1)*21000 {
					t.Fatal("queue receipt execution differs")
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type queueBuildReadHook struct {
	kv.RwDB
	before func() error
}

func (db *queueBuildReadHook) BeginRo(ctx context.Context) (kv.Tx, error) {
	if err := db.before(); err != nil {
		return nil, err
	}
	return db.RwDB.BeginRo(ctx)
}
func TestQueueBuildCancellationKeepsSelectedOwnerResult(t *testing.T) {
	f := newStrictIngressFixture(t, 6, 1<<20)
	status, _ := f.server.StrictIngressStatus()
	if _, err := f.server.AdmitStrictFrame(context.Background(), f.server.Identity(), status.Epoch, f.frames[0]); err != nil {
		t.Fatal(err)
	}
	request := queueRequest(t, f, f.template.Header().(*block.Header), 6)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := f.chain.ChainDB
	fired := false
	f.chain.ChainDB = &queueBuildReadHook{RwDB: original, before: func() error {
		// The actual builder opens its parent reader after the immutable template
		// is registered. Cancel only the requester, leaving owner execution alive.
		if !fired {
			f.server.mu.Lock()
			plan := f.server.queuePlans[1]
			selected := plan != nil && len(plan.input) > 0
			f.server.mu.Unlock()
			if selected {
				fired = true
				cancel()
			}
		}
		return nil
	}}
	defer func() { f.chain.ChainDB = original }()
	if _, err := f.server.BuildFromQueue(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("request cancellation %v", err)
	}
	if !fired {
		t.Fatal("cancel did not occur after selection")
	}
	if _, err := f.server.AdmitStrictFrame(context.Background(), f.server.Identity(), status.Epoch, f.frames[1]); err != nil {
		t.Fatal(err)
	}
	result, err := f.server.BuildFromQueue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var built block.Block
	if err := built.Unmarshal(result.BlockRLP); err != nil {
		t.Fatal(err)
	}
	if len(built.Transactions()) != 3 || f.server.BuildCalls() != 1 {
		t.Fatal("canceled requester changed body or rebuilt")
	}
	result.BlockRLP[0] ^= 1
	cached, err := f.server.BuildFromQueue(context.Background(), request)
	if err != nil || bytes.Equal(result.BlockRLP, cached.BlockRLP) {
		t.Fatalf("caller changed retained result %v", err)
	}
	changed := request
	changed.MaxTransactions--
	if _, err := f.server.BuildFromQueue(context.Background(), changed); !errors.Is(err, ErrQueueBuildConflict) {
		t.Fatalf("same view changed limits %v", err)
	}
	f.server.mu.Lock()
	plan := f.server.queuePlans[1]
	retained := f.server.buildBytes
	want := len(plan.input) + len(plan.result.BlockRLP)
	f.server.mu.Unlock()
	if retained != want {
		t.Fatalf("retained input/result budget %d want %d", retained, want)
	}
}
func TestQueueBuildFailedAttemptRetainsTemplateAndBudget(t *testing.T) {
	f := newStrictIngressFixture(t, 6, 1<<20)
	status, _ := f.server.StrictIngressStatus()
	ctx := context.Background()
	if _, err := f.server.AdmitStrictFrame(ctx, f.server.Identity(), status.Epoch, f.frames[0]); err != nil {
		t.Fatal(err)
	}
	request := queueRequest(t, f, f.template.Header().(*block.Header), 6)
	original := f.chain.ChainDB
	fired := false
	f.chain.ChainDB = &queueBuildReadHook{RwDB: original, before: func() error {
		if !fired {
			f.server.mu.Lock()
			plan := f.server.queuePlans[1]
			selected := plan != nil && len(plan.input) > 0
			f.server.mu.Unlock()
			if selected {
				fired = true
				return errors.New("injected selected-plan parent reader failure")
			}
		}
		return nil
	}}
	defer func() { f.chain.ChainDB = original }()
	if _, err := f.server.BuildFromQueue(ctx, request); err == nil || !fired {
		t.Fatalf("read failure not exercised %v", err)
	}
	f.server.mu.Lock()
	plan := f.server.queuePlans[1]
	input := append([]byte(nil), plan.input...)
	retained := f.server.buildBytes
	f.server.mu.Unlock()
	if retained != len(input) || len(input) == 0 {
		t.Fatal("failed plan input lost or output reservation leaked")
	}
	if _, err := f.server.AdmitStrictFrame(ctx, f.server.Identity(), status.Epoch, f.frames[1]); err != nil {
		t.Fatal(err)
	}
	result, err := f.server.BuildFromQueue(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	var built block.Block
	if err := built.Unmarshal(result.BlockRLP); err != nil {
		t.Fatal(err)
	}
	if len(built.Transactions()) != 3 || result.TemplateHash != crypto.Keccak256Hash(input) || f.server.BuildCalls() != 2 {
		t.Fatal("retry changed selected template or did not execute recovery")
	}
	status, _ = f.server.StrictIngressStatus()
	if status.PendingTransactions != 6 {
		t.Fatal("build retry pruned queue")
	}
}

func queueLeader(t *testing.T, f strictIngressFixture, parent *block.Block, view hotstuff.ViewNumber) (*hotstuff.HotStuff, *block.Header) {
	t.Helper()
	cfg := f.chain.Config()
	engine := hotstuff.New(cfg.HotStuff, cfg)
	t.Cleanup(func() { _ = engine.Close() })
	set := f.chain.Engine().(*hotstuff.HotStuff).Engine().ValidatorSetForView(view)
	index := hotstuff.LeaderForView(view, set)
	address, err := set.GetAddress(index)
	if err != nil {
		t.Fatal(err)
	}
	engine.Authorize(address, f.keys[int(index)])
	if err := engine.InitEngineFromConfig(); err != nil {
		t.Fatal(err)
	}
	engine.Engine().RestoreState(view, hotstuff.GenesisQC(), hotstuff.GenesisQC(), 0)
	h := &block.Header{ParentHash: parent.Hash(), Number: uint256.NewInt(parent.Number64().Uint64() + 1), GasLimit: parent.GasLimit(), Difficulty: uint256.NewInt(0)}
	h.BaseFee, _ = uint256.FromBig(misc.CalcBaseFee(cfg, parent.Header().(*block.Header)))
	if err := engine.Prepare(f.chain, h); err != nil {
		t.Fatal(err)
	}
	return engine, h
}
func TestQueueBuildUsesAppliedParentBeforeCanonicalCatchesUp(t *testing.T) {
	ctx := context.Background()
	f := newStrictIngressFixture(t, 6, 1<<20)
	status, _ := f.server.StrictIngressStatus()
	for _, frame := range f.frames {
		if _, err := f.server.AdmitStrictFrame(ctx, f.server.Identity(), status.Epoch, frame); err != nil {
			t.Fatal(err)
		}
	}
	client := NewClient(startExecutionSocket(t, f.server), f.server.Identity())
	defer client.Close()
	first, err := client.BuildFromQueue(ctx, queueRequest(t, f, f.template.Header().(*block.Header), 4))
	if err != nil {
		t.Fatal(err)
	}
	b := queueSealImport(t, f, client, f.leader, first)
	if _, err := f.chain.ReadPerformanceCanonicalAccounts(ctx, []types.Address{f.from[0]}); err == nil {
		t.Fatal("speculative accounts became admission baseline")
	}
	if _, err := f.chain.ReadPerformanceParentAccounts(ctx, f.chain.GenesisBlock().Hash(), 0, nil); err == nil {
		t.Fatal("old parent state was allowed")
	}
	actual, err := f.chain.ReadPerformanceParentAccounts(ctx, b.Hash(), 1, []types.Address{f.from[0], f.from[1]})
	if err != nil {
		t.Fatal(err)
	}
	for _, from := range f.from {
		if actual.Accounts[from].Nonce != 2 {
			t.Fatal("actual applied nonce differs")
		}
	}
	second, header := queueLeader(t, f, b, 2)
	next, err := client.BuildFromQueue(ctx, queueRequest(t, f, header, 4))
	if err != nil {
		t.Fatal(err)
	}
	b2 := queueSealImport(t, f, client, second, next)
	if len(b2.Transactions()) != 2 {
		t.Fatal("applied ancestor transactions selected twice")
	}
	for _, txn := range b2.Transactions() {
		if txn.Nonce() != 2 {
			t.Fatal("queue selector used canonical nonce instead of applied nonce")
		}
	}
	status, _ = f.server.StrictIngressStatus()
	if status.PendingTransactions != 6 || status.CanonicalNumber != 0 {
		t.Fatal("applied builds removed uncommitted transactions")
	}
	// Finalize the prefix in order, while actual execution is already at child 2.
	for i, b := range []*block.Block{b, b2} {
		request := CommitRequest{Identity: f.server.Identity(), BlockHash: b.Hash(), ParentHash: b.ParentHash(), Number: uint64(i + 1)}
		if _, err := client.CommitCertified(ctx, request, queueQC(f.keys, b.Hash(), hotstuff.ViewNumber(i+1), 5)); err != nil {
			t.Fatal(err)
		}
	}
	status, _ = f.server.StrictIngressStatus()
	if status.PendingTransactions != 0 || status.CanonicalNumber != 2 {
		t.Fatalf("canonical catchup %+v", status)
	}
}
func TestQueueBuildRejectsInvalidSelectionBeforeBindingView(t *testing.T) {
	f := newStrictIngressFixture(t, 6, 1<<20)
	ctx := context.Background()
	request := queueRequest(t, f, f.template.Header().(*block.Header), 6)
	for name, mutate := range map[string]func(*QueueBuildRequest){
		"identity": func(r *QueueBuildRequest) { r.Identity.GenesisHash[0] ^= 1 },
		"epoch":    func(r *QueueBuildRequest) { r.Epoch[0] ^= 1 },
		"hash":     func(r *QueueBuildRequest) { r.PreparedHash[0] ^= 1 },
		"count":    func(r *QueueBuildRequest) { r.MaxTransactions = 0 },
		"bytes":    func(r *QueueBuildRequest) { r.MaxEncodedBytes = MaxQueueBuildEncodedBytes + 1 },
		"body": func(r *QueueBuildRequest) {
			r.PreparedRLP, _ = f.template.Marshal()
			r.PreparedHash = crypto.Keccak256Hash(r.PreparedRLP)
		},
		"sealed": func(r *QueueBuildRequest) {
			var b block.Block
			_ = b.Unmarshal(r.PreparedRLP)
			h := block.CopyHeader(b.Header().(*block.Header))
			h.Extra[len(h.Extra)-1] = 1
			r.PreparedRLP, _ = block.NewBlock(h, nil).Marshal()
			r.PreparedHash = crypto.Keccak256Hash(r.PreparedRLP)
		},
		"execution": func(r *QueueBuildRequest) {
			var b block.Block
			_ = b.Unmarshal(r.PreparedRLP)
			h := block.CopyHeader(b.Header().(*block.Header))
			h.Root = types.Hash{1}
			r.PreparedRLP, _ = block.NewBlock(h, nil).Marshal()
			r.PreparedHash = crypto.Keccak256Hash(r.PreparedRLP)
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := request
			r.PreparedRLP = append([]byte(nil), request.PreparedRLP...)
			mutate(&r)
			if _, err := f.server.BuildFromQueue(ctx, r); err == nil {
				t.Fatal("invalid selection accepted")
			}
			f.server.mu.Lock()
			plans, bytes := len(f.server.queuePlans), f.server.buildBytes
			f.server.mu.Unlock()
			if plans != 0 || bytes != 0 {
				t.Fatalf("invalid request retained plan %d/%d", plans, bytes)
			}
		})
	}
	// A valid nonempty selection remains possible after every malformed request.
	status, _ := f.server.StrictIngressStatus()
	if _, err := f.server.AdmitStrictFrame(ctx, f.server.Identity(), status.Epoch, f.frames[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.BuildFromQueue(ctx, request); err != nil {
		t.Fatal(err)
	}
}
func TestQueueBuildPinnedPlanSurvivesDefaultCachePressureAndChecksResponses(t *testing.T) {
	f := newStrictIngressFixture(t, 6, 1<<20)
	f.server.capacity = 1
	ctx := context.Background()
	status, _ := f.server.StrictIngressStatus()
	if _, err := f.server.AdmitStrictFrame(ctx, f.server.Identity(), status.Epoch, f.frames[0]); err != nil {
		t.Fatal(err)
	}
	request := queueRequest(t, f, f.template.Header().(*block.Header), 6)
	result, err := f.server.BuildFromQueue(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.Build(ctx, buildRequest(t, f.server.Identity(), f.template)); err == nil {
		t.Fatal("default Build evicted a pinned plan")
	}
	again, err := f.server.BuildFromQueue(ctx, request)
	if err != nil || again.BlockHash != result.BlockHash || f.server.BuildCalls() != 1 {
		t.Fatal("default pressure changed queue plan")
	}
	var prepared block.Block
	if err := prepared.Unmarshal(request.PreparedRLP); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*QueueBuildResult){
		"identity": func(r *QueueBuildResult) { r.Identity.GenesisHash[0] ^= 1 },
		"epoch":    func(r *QueueBuildResult) { r.Epoch[0] ^= 1 },
		"prepared": func(r *QueueBuildResult) { r.PreparedHash[0] ^= 1 },
		"selected": func(r *QueueBuildResult) { r.TemplateHash[0] ^= 1 },
		"result":   func(r *QueueBuildResult) { r.Result.Root[0] ^= 1 },
		"proof": func(r *QueueBuildResult) {
			var b block.Block
			_ = b.Unmarshal(r.BlockRLP)
			b.Body().(*block.Body).ZkProof = []byte{1}
			r.BlockRLP, _ = b.Marshal()
		},
		"verifier": func(r *QueueBuildResult) {
			var b block.Block
			_ = b.Unmarshal(r.BlockRLP)
			b.Body().(*block.Body).Verifiers = []*block.Verify{{Address: types.Address{1}}}
			r.BlockRLP, _ = b.Marshal()
		},
		"rewards": func(r *QueueBuildResult) {
			var b block.Block
			_ = b.Unmarshal(r.BlockRLP)
			body := b.Body().(*block.Body)
			// The fixture predates Shanghai; explicitly add a valid original
			// rewards commitment, then mutate only the rewards beneath it.
			h := b.Header().(*block.Header)
			original := hash.DeriveSha(block.Rewards(body.Rewards))
			h.WithdrawalsHash = &original
			h.ResetHashCache()
			body.Rewards = append(body.Rewards, &block.Reward{Address: types.Address{1}, Amount: uint256.NewInt(1)})
			mutated := b.WithSeal(h)
			r.BlockRLP, _ = mutated.Marshal()
			r.BlockHash = mutated.Hash()
		},
		"header": func(r *QueueBuildResult) {
			var b block.Block
			_ = b.Unmarshal(r.BlockRLP)
			h := block.CopyHeader(b.Header().(*block.Header))
			h.Coinbase[0] ^= 1
			h.ResetHashCache()
			mutated := block.NewBlock(h, b.Transactions())
			r.BlockRLP, _ = mutated.Marshal()
			r.BlockHash = mutated.Hash()
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := result
			r.BlockRLP = append([]byte(nil), result.BlockRLP...)
			mutate(&r)
			var wire block.Block
			if err := wire.Unmarshal(r.BlockRLP); err != nil {
				t.Fatalf("invalid mutated response fixture: %v", err)
			}
			if (name == "proof" || name == "verifier") && wire.Hash() != result.BlockHash {
				t.Fatal("aux body mutation unexpectedly changed block hash")
			}
			if err := validateQueueBuildResponse(f.server.Identity(), request, &prepared, r); err == nil {
				t.Fatal("bad queue response accepted")
			}
		})
	}
}

func TestQueueBuildExpiredViewCannotRebindAtHigherHeight(t *testing.T) {
	f := newStrictIngressFixture(t, 6, 1<<20)
	ctx := context.Background()
	status, _ := f.server.StrictIngressStatus()
	if _, err := f.server.AdmitStrictFrame(ctx, f.server.Identity(), status.Epoch, f.frames[0]); err != nil {
		t.Fatal(err)
	}
	client := NewClient(startExecutionSocket(t, f.server), f.server.Identity())
	defer client.Close()
	first, err := client.BuildFromQueue(ctx, queueRequest(t, f, f.template.Header().(*block.Header), 3))
	if err != nil {
		t.Fatal(err)
	}
	// View two prepares a sibling before either candidate is applied. Its guard
	// must remain an expiry floor even if view one's block becomes canonical.
	_, sibling := queueLeader(t, f, f.chain.GenesisBlock().(*block.Block), 2)
	if _, err := client.BuildFromQueue(ctx, queueRequest(t, f, sibling, 3)); err != nil {
		t.Fatal(err)
	}
	b := queueSealImport(t, f, client, f.leader, first)
	request := CommitRequest{Identity: f.server.Identity(), BlockHash: b.Hash(), ParentHash: b.ParentHash(), Number: 1}
	if _, err := client.CommitCertified(ctx, request, queueQC(f.keys, b.Hash(), 1, 5)); err != nil {
		t.Fatal(err)
	}
	_, child := queueLeader(t, f, b, 2)
	if _, err := f.server.BuildFromQueue(ctx, queueRequest(t, f, child, 3)); !errors.Is(err, ErrQueueBuildExpired) {
		t.Fatalf("pruned view guard resurrected at new height %v", err)
	}
	f.server.mu.Lock()
	plans, retained := len(f.server.queuePlans), f.server.buildBytes
	f.server.mu.Unlock()
	if plans != 0 || retained != 0 {
		t.Fatalf("expired plans retained %d/%d", plans, retained)
	}
}

func TestQueueBuildEncodedLimitSelectsWholeNoncePrefix(t *testing.T) {
	f := newStrictIngressFixture(t, 6, 1<<20)
	ctx := context.Background()
	status, _ := f.server.StrictIngressStatus()
	maxWire := 0
	for _, frame := range f.frames {
		if _, err := f.server.AdmitStrictFrame(ctx, f.server.Identity(), status.Epoch, frame); err != nil {
			t.Fatal(err)
		}
		for _, raw := range strictFrameRaws(t, frame) {
			if len(raw)+4 > maxWire {
				maxWire = len(raw) + 4
			}
		}
	}
	request := queueRequest(t, f, f.template.Header().(*block.Header), 6)
	request.MaxEncodedBytes = maxWire
	client := NewClient(startExecutionSocket(t, f.server), f.server.Identity())
	defer client.Close()
	result, err := client.BuildFromQueue(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	var b block.Block
	if err := b.Unmarshal(result.BlockRLP); err != nil {
		t.Fatal(err)
	}
	if len(b.Transactions()) != 1 || b.Transactions()[0].Nonce() != 0 || result.Result.GasUsed != 21000 {
		t.Fatal("byte limit split or skipped nonce prefix")
	}
	status, _ = f.server.StrictIngressStatus()
	if status.PendingTransactions != 6 || status.ReservedTransactions != 0 {
		t.Fatal("byte-limited selection removed queue")
	}
}

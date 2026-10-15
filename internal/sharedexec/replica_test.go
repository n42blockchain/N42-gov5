package sharedexec_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
)

type replicaTestFetcher struct{ requested chan types.Hash }

func (f replicaTestFetcher) FetchBlockByHash(hash types.Hash) {
	select {
	case f.requested <- hash:
	default:
	}
}
func (replicaTestFetcher) CatchUp()             {}
func (replicaTestFetcher) HeightBehind() uint64 { return 0 }

func signedFixtureBlock(t *testing.T, f *sharedexec.ExecutionFixtureForTest, client *sharedexec.Client) *block.Block {
	t.Helper()
	raw, err := f.Template.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Build(context.Background(), sharedexec.BuildRequest{Identity: client.Identity(), TemplateHash: crypto.Keccak256Hash(raw), TemplateRLP: raw})
	if err != nil {
		t.Fatal(err)
	}
	var candidate block.Block
	if err := candidate.Unmarshal(result.BlockRLP); err != nil {
		t.Fatal(err)
	}
	sealed := make(chan block.IBlock, 1)
	if err := f.Leader.Seal(f.Chain, &candidate, sealed, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	return (<-sealed).(*block.Block)
}

func replicaQC(keys []blscommon.SecretKey, hash types.Hash, count int) *hotstuff.QuorumCertificate {
	qc := &hotstuff.QuorumCertificate{View: 1, BlockHash: hash, Signers: make([]bool, 7)}
	var signatures []blscommon.Signature
	for i := 0; i < count; i++ {
		qc.Signers[i] = true
		signatures = append(signatures, keys[i].Sign(hotstuff.CommitSigningMessage(1, hash)))
	}
	qc.AggregateSignature = bls.AggregateSignatures(signatures).Marshal()
	return qc
}

func assertNoExecutionState(t *testing.T, db kv.RwDB) {
	t.Helper()
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		cursor, err := tx.Cursor(modules.Account)
		if err != nil {
			return err
		}
		defer cursor.Close()
		key, _, err := cursor.First()
		if err != nil {
			return err
		}
		if key != nil {
			t.Fatal("validator performed account execution")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSevenMetadataReplicasShareExecutionAndCommitOnlyAfterQC(t *testing.T) {
	ctx := context.Background()
	fixture := sharedexec.NewExecutionFixtureForTest(t)
	server, err := sharedexec.New(fixture.Chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	socket := sharedexec.StartUnixExecutionForTest(t, server)
	builder := sharedexec.NewClient(socket, server.Identity())
	defer builder.Close()
	b := signedFixtureBlock(t, fixture, builder)
	raw, err := b.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var replicas []*sharedvalidator.Replica
	var services []*hotstuff.Service
	var stores []kv.RwDB
	for i := 0; i < 7; i++ {
		db := memdb.New(t.TempDir())
		t.Cleanup(db.Close)
		stores = append(stores, db)
		client := sharedexec.NewClient(socket, server.Identity())
		t.Cleanup(client.Close)
		engine := hotstuff.New(fixture.Chain.Config().HotStuff, fixture.Chain.Config())
		t.Cleanup(func() { _ = engine.Close() })
		engine.Authorize(types.HexToAddress(fixture.Chain.Config().HotStuff.Validators[i].Address), fixture.Keys[i])
		if err := engine.InitEngineFromConfig(); err != nil {
			t.Fatal(err)
		}
		replica, err := sharedvalidator.New(ctx, db, fixture.Chain.Config(), fixture.Chain.GenesisBlock().(*block.Block), engine, client)
		if err != nil {
			t.Fatal(err)
		}
		service := hotstuff.NewService(engine, nil, db, "unused", "unused")
		t.Cleanup(service.Stop)
		if err := replica.AttachService(service, replicaTestFetcher{requested: make(chan types.Hash, 8)}); err != nil {
			t.Fatal(err)
		}
		if replica.BlockApplied(b.Hash(), 1) {
			t.Fatal("body not received yet claims execution")
		}
		replicas = append(replicas, replica)
		services = append(services, service)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 7)
	for _, replica := range replicas {
		wg.Add(1)
		go func(r *sharedvalidator.Replica) {
			defer wg.Done()
			result, err := r.Import(ctx, raw)
			if err != nil {
				errs <- err
			} else if !result.Durable {
				errs <- context.DeadlineExceeded
			}
		}(replica)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if server.ImportCalls() != 1 {
		t.Fatalf("seven validators executed %d times", server.ImportCalls())
	}
	for i, replica := range replicas {
		if !replica.BlockApplied(b.Hash(), 1) || replica.CurrentBlock().Hash() != fixture.Chain.GenesisBlock().Hash() {
			t.Fatal("execution evidence and canonical commit were conflated")
		}
		assertNoExecutionState(t, stores[i])
	}
	if err := services[0].CommitCertificate(ctx, replicaQC(fixture.Keys, b.Hash(), 4)); err == nil {
		t.Fatal("four votes advanced replica canonical")
	}
	qc := replicaQC(fixture.Keys, b.Hash(), 5)
	for _, service := range services {
		if err := service.CommitCertificate(ctx, qc); err != nil {
			t.Fatal(err)
		}
	}
	for i, replica := range replicas {
		if replica.CurrentBlock().Hash() != b.Hash() {
			t.Fatal("authenticated ACK did not advance local metadata")
		}
		assertNoExecutionState(t, stores[i])
		pending, err := services[i].PendingCommitCertificates(ctx)
		if err != nil || len(pending) != 0 {
			t.Fatal("local ACK did not clear outbox")
		}
	}
	if err := fixture.Chain.DB().View(ctx, func(tx kv.Tx) error {
		ibs := state.New(state.NewPlainStateReader(tx))
		if ibs.GetBalance(fixture.Recipient).Uint64() != 24 {
			t.Fatal("owner state execution missing")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := sharedvalidator.New(ctx, fixture.Chain.DB(), fixture.Chain.Config(), fixture.Chain.GenesisBlock().(*block.Block), fixture.Public, builder); err == nil {
		t.Fatal("executor account database reused as validator metadata")
	}
	// Restart a metadata replica on the exact same immutable genesis/config.
	restarted, err := sharedvalidator.New(ctx, stores[0], fixture.Chain.Config(), fixture.Chain.GenesisBlock().(*block.Block), fixture.Public, builder)
	if err != nil {
		t.Fatal(err)
	}
	if !restarted.BlockApplied(b.Hash(), 1) || restarted.CurrentBlock().Hash() != b.Hash() {
		t.Fatal("metadata restart lost bound execution/finality evidence")
	}
}

func TestReplicaAvailabilityIsNotRemoteExecutionEvidence(t *testing.T) {
	ctx := context.Background()
	fixture := sharedexec.NewExecutionFixtureForTest(t)
	server, err := sharedexec.New(fixture.Chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	client := sharedexec.NewClient(sharedexec.StartUnixExecutionForTest(t, server), server.Identity())
	defer client.Close()
	first := signedFixtureBlock(t, fixture, client)
	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	replica, err := sharedvalidator.New(ctx, db, fixture.Chain.Config(), fixture.Chain.GenesisBlock().(*block.Block), fixture.Public, client)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(ctx, func(tx kv.RwTx) error { return rawdb.WriteBlock(tx, first) }); err != nil {
		t.Fatal(err)
	}
	header := first.Header().(*block.Header)
	header.ParentHash = first.Hash()
	header.Number = uint256.NewInt(2)
	header.GasUsed = 0
	header.Root = first.StateRoot()
	if err := fixture.Leader.Prepare(replica, header); err != nil {
		t.Fatal(err)
	}
	child := block.NewBlockFromReceipt(header, nil, nil, nil, nil).(*block.Block)
	sealed := make(chan block.IBlock, 1)
	if err := fixture.Leader.Seal(replica, child, sealed, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	second := (<-sealed).(*block.Block)
	raw, err := second.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	result, err := replica.Import(ctx, raw)
	if err != nil || result.Durable || replica.BlockApplied(second.Hash(), 2) {
		t.Fatalf("accepted child claimed execution: %+v %v", result, err)
	}
	if _, err := replica.GetHeaderByHash(second.Hash()); err != nil {
		t.Fatal(err)
	}
	assertNoExecutionState(t, db)
	firstRaw, err := first.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if result, err := replica.Import(ctx, firstRaw); err != nil || !result.Durable {
		t.Fatalf("parent execution: %v", err)
	}
	if result, err := replica.Import(ctx, raw); err != nil || !result.Durable || !replica.BlockApplied(second.Hash(), 2) {
		t.Fatalf("child retry failed: %+v %v", result, err)
	}
	if err := replica.CommitCertificate(ctx, replicaQC(fixture.Keys, second.Hash(), 5)); err == nil {
		t.Fatal("local or remote canonical skipped parent")
	}
	if err := replica.CommitCertificate(ctx, replicaQC(fixture.Keys, first.Hash(), 5)); err != nil {
		t.Fatal(err)
	}
	if err := replica.CommitCertificate(ctx, replicaQC(fixture.Keys, second.Hash(), 5)); err != nil {
		t.Fatal(err)
	}
	if replica.CurrentBlock().Hash() != second.Hash() {
		t.Fatal("canonical retries did not converge")
	}
	assertNoExecutionState(t, db)
}

type rollbackMetadataCanonical struct {
	kv.RwDB
	hash types.Hash
	fail atomic.Bool
}

func (db *rollbackMetadataCanonical) Update(ctx context.Context, fn func(kv.RwTx) error) error {
	return db.RwDB.Update(ctx, func(tx kv.RwTx) error {
		if err := fn(tx); err != nil {
			return err
		}
		if rawdb.ReadHeadBlockHash(tx) == db.hash && db.fail.CompareAndSwap(true, false) {
			return errors.New("test metadata canonical transaction rolled back")
		}
		return nil
	})
}

func TestReplicaRecoversRemoteAckAfterLocalCanonicalRollback(t *testing.T) {
	ctx := context.Background()
	fixture := sharedexec.NewExecutionFixtureForTest(t)
	server, err := sharedexec.New(fixture.Chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	client := sharedexec.NewClient(sharedexec.StartUnixExecutionForTest(t, server), server.Identity())
	defer client.Close()
	b := signedFixtureBlock(t, fixture, client)
	raw, err := b.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	store := memdb.New(t.TempDir())
	t.Cleanup(store.Close)
	db := &rollbackMetadataCanonical{RwDB: store, hash: b.Hash()}
	db.fail.Store(true)
	replica, err := sharedvalidator.New(ctx, db, fixture.Chain.Config(), fixture.Chain.GenesisBlock().(*block.Block), fixture.Public, client)
	if err != nil {
		t.Fatal(err)
	}
	service := hotstuff.NewService(fixture.Public, nil, db, "unused", "unused")
	defer service.Stop()
	if err := replica.AttachService(service, replicaTestFetcher{requested: make(chan types.Hash, 8)}); err != nil {
		t.Fatal(err)
	}
	if result, err := replica.Import(ctx, raw); err != nil || !result.Durable {
		t.Fatalf("remote import: %v", err)
	}
	if err := service.CommitCertificate(ctx, replicaQC(fixture.Keys, b.Hash(), 5)); err == nil {
		t.Fatal("rolled back local canonical reported committed")
	}
	if fixture.Chain.CurrentBlock().Hash() != b.Hash() || replica.CurrentBlock().Hash() != fixture.Chain.GenesisBlock().Hash() {
		t.Fatal("cross-database rollback boundary not preserved")
	}
	pending, err := service.PendingCommitCertificates(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatal("local rollback lost retry certificate")
	}
	if err := service.ReplayCommitCertificates(ctx); err != nil {
		t.Fatal(err)
	}
	if replica.CurrentBlock().Hash() != b.Hash() || server.ImportCalls() != 1 {
		t.Fatal("idempotent ACK retry failed or re-executed")
	}
	assertNoExecutionState(t, store)
}

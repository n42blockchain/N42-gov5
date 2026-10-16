package sharedexec_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func strictQueueReplica(t *testing.T, f *sharedexec.StrictIngressFixtureForTest, client *sharedexec.Client, db kv.RwDB) (*sharedvalidator.Replica, *hotstuff.Service, *sharedvalidator.Producer) {
	t.Helper()
	replica, err := sharedvalidator.New(context.Background(), db, f.Chain.Config(), f.Chain.GenesisBlock().(*block.Block), f.Leader, client)
	if err != nil {
		t.Fatal(err)
	}
	service := hotstuff.NewService(f.Leader, nil, db, "unused", "unused")
	t.Cleanup(service.Stop)
	if err := replica.AttachService(service, replicaTestFetcher{requested: make(chan types.Hash, 8)}); err != nil {
		t.Fatal(err)
	}
	p, err := sharedvalidator.NewProducer(context.Background(), replica, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Stop)
	return replica, service, p
}
func strictQueueAdmit(t *testing.T, f *sharedexec.StrictIngressFixtureForTest) {
	t.Helper()
	path := sharedexec.StartUnixStrictIngressForTest(t, f.Server)
	status, _ := f.Server.StrictIngressStatus()
	for _, frame := range f.Frames {
		c, err := sharedexec.DialStrictIngress(context.Background(), path, f.Server.Identity(), status.ManifestHash, frame.Shard)
		if err != nil {
			t.Fatal(err)
		}
		ack, err := c.Admit(context.Background(), frame)
		_ = c.Close()
		if err != nil || ack.NewlyAdmitted != 3 {
			t.Fatalf("strict socket %+v %v", ack, err)
		}
	}
}
func TestOwnerQueueProducerConsumesStrictIngressThroughRealSealAndQC(t *testing.T) {
	ctx := context.Background()
	f := sharedexec.NewStrictIngressFixtureForTest(t)
	strictQueueAdmit(t, f)
	client := sharedexec.NewClient(sharedexec.StartUnixExecutionForTest(t, f.Server), f.Server.Identity())
	defer client.Close()
	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	replica, service, p := strictQueueReplica(t, f, client, db)
	if err := p.UseOwnerQueue(ctx, sharedvalidator.OwnerQueueOptions{MaxTransactions: 6, MaxEncodedBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	binding := p.OwnerQueueBinding()
	if binding == nil || binding.Epoch == ([16]byte{}) || binding.MaxTransactions != 6 {
		t.Fatal("queue startup not bound")
	}
	if err := p.SubmitTransactions(f.Template.Transactions()); !errors.Is(err, sharedvalidator.ErrOwnerQueueSubmissionDisabled) {
		t.Fatalf("local queue accepted %v", err)
	}
	head, err := replica.CanonicalProgress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := f.Template.Transactions()[0].EthEncoded()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SubmitRawTransactionsAtHead(ctx, [][]byte{raw}, head.BlockHash, head.Number); !errors.Is(err, sharedvalidator.ErrOwnerQueueSubmissionDisabled) {
		t.Fatalf("raw local pool accepted %v", err)
	}
	api, err := sharedvalidator.NewAPI(replica, p)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "validator.sock")
	httpServer, listener, err := api.ListenUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = httpServer.Serve(listener) }()
	t.Cleanup(func() { _ = httpServer.Close(); api.Stop() })
	apiClient := sharedvalidator.NewAPIClient(path, client.Identity())
	defer apiClient.Close()
	info, err := apiClient.Status(ctx)
	if err != nil || info.OwnerQueue == nil || *info.OwnerQueue != *binding || info.QueuedTransactions != 0 {
		t.Fatalf("local/global queue scope hidden %+v %v", info, err)
	}
	if _, err := apiClient.Submit(ctx, head, [][]byte{raw}); err == nil {
		t.Fatal("HTTP local pool ACKed unconsumed transactions")
	}
	b, err := p.Produce(ctx, f.Chain.GenesisBlock().Hash())
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Transactions()) != 6 || b.Header().(*block.Header).GasUsed != 6*21000 || !replica.BlockApplied(b.Hash(), 1) {
		t.Fatal("queue producer did not execute complete signed body")
	}
	if f.Server.BuildCalls() != 1 || f.Server.ImportCalls() != 1 {
		t.Fatal("queue producer did not use one real build/import")
	}
	var view [8]byte
	binary.BigEndian.PutUint64(view[:], 1)
	sealedKey := append([]byte("performance-own-candidate/"), view[:]...)
	requestKey := append([]byte("performance-own-queue-request/"), view[:]...)
	if err := db.View(ctx, func(tx kv.Tx) error {
		sealed, err := tx.GetOne(modules.HotStuffState, sealedKey)
		if err != nil {
			return err
		}
		request, err := tx.GetOne(modules.HotStuffState, requestKey)
		if err != nil {
			return err
		}
		if len(sealed) == 0 || len(request) != 0 {
			t.Fatal("sealed WAL did not atomically replace prepared queue request")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// An already sealed candidate is stronger recovery evidence than any
	// leftover unsealed request, even one carrying an obsolete epoch/schema.
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return tx.Put(modules.HotStuffState, requestKey, []byte("obsolete-unsealed-request"))
	}); err != nil {
		t.Fatal(err)
	}
	restarted, err := sharedvalidator.NewProducer(ctx, replica, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Stop()
	if err := restarted.UseOwnerQueue(ctx, sharedvalidator.OwnerQueueOptions{MaxTransactions: 6, MaxEncodedBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	again, err := restarted.Produce(ctx, f.Chain.GenesisBlock().Hash())
	if err != nil || again.Hash() != b.Hash() || f.Server.BuildCalls() != 1 || f.Server.ImportCalls() != 1 {
		t.Fatalf("sealed WAL queue retry %v", err)
	}
	if err := service.CommitCertificate(ctx, replicaQC(f.Keys, b.Hash(), 4)); err == nil {
		t.Fatal("four votes finalized owner queue")
	}
	status, _ := f.Server.StrictIngressStatus()
	if status.PendingTransactions != 6 {
		t.Fatal("proposal/4QC released owner queue")
	}
	if err := service.CommitCertificate(ctx, replicaQC(f.Keys, b.Hash(), 5)); err != nil {
		t.Fatal(err)
	}
	progress, err := replica.CanonicalProgress(ctx)
	if err != nil || progress.CommittedExecutedTransactions != 6 || progress.BlockHash != b.Hash() {
		t.Fatalf("queue committed counter %+v %v", progress, err)
	}
	status, _ = f.Server.StrictIngressStatus()
	if status.PendingTransactions != 0 || status.RetainedEncodedBytes != 0 {
		t.Fatalf("QC did not prune owner queue %+v", status)
	}
	assertNoExecutionState(t, db)
	if _, key := f.Public.SignerKey(); key != nil {
		t.Fatal("owner received BLS key")
	}
	if err := p.UseOwnerQueue(ctx, sharedvalidator.OwnerQueueOptions{}); err == nil {
		t.Fatal("queue changed after production")
	}
}

type blockedQueueProducer struct {
	fixture  *sharedexec.StrictIngressFixtureForTest
	db       kv.RwDB
	client   *sharedexec.Client
	replica  *sharedvalidator.Replica
	service  *hotstuff.Service
	producer *sharedvalidator.Producer
	requests chan []byte
	entered  chan struct{}
}

func newBlockedQueueProducer(t *testing.T) blockedQueueProducer {
	t.Helper()
	f := sharedexec.NewStrictIngressFixtureForTest(t)
	strictQueueAdmit(t, f)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "execute.sock")
	httpServer, listener, err := f.Server.ListenUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan []byte, 4)
	entered := make(chan struct{})
	var first atomic.Bool
	httpServer.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/build-queue" {
			raw, err := io.ReadAll(io.LimitReader(r.Body, 2*sharedexec.MaxQueuePreparedBytes+4097))
			if err != nil {
				http.Error(w, "test body read", 400)
				return
			}
			requests <- raw
			if first.CompareAndSwap(false, true) {
				close(entered)
				<-r.Context().Done()
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		f.Server.ServeHTTP(w, r)
	})
	go func() { _ = httpServer.Serve(listener) }()
	t.Cleanup(func() { _ = httpServer.Close() })
	client := sharedexec.NewClient(path, f.Server.Identity())
	t.Cleanup(client.Close)
	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	replica, service, p := strictQueueReplica(t, f, client, db)
	if err := p.UseOwnerQueue(context.Background(), sharedvalidator.OwnerQueueOptions{MaxTransactions: 6, MaxEncodedBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	return blockedQueueProducer{f, db, client, replica, service, p, requests, entered}
}
func cancelFirstQueueRequest(t *testing.T, f blockedQueueProducer) []byte {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := f.producer.Produce(ctx, f.fixture.Chain.GenesisBlock().Hash()); done <- err }()
	select {
	case <-f.entered:
	case <-time.After(time.Second):
		t.Fatal("queue RPC did not start")
	}
	// WAL must exist before the server has even started selecting a body.
	var view [8]byte
	binary.BigEndian.PutUint64(view[:], 1)
	key := append([]byte("performance-own-queue-request/"), view[:]...)
	var stored []byte
	if err := f.db.View(context.Background(), func(tx kv.Tx) error {
		raw, err := tx.GetOne(modules.HotStuffState, key)
		stored = append([]byte(nil), raw...)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(stored) == 0 {
		t.Fatal("queue request was sent before WAL was durable")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled producer proposed")
		}
	case <-time.After(time.Second):
		t.Fatal("queue request cancellation did not stop producer")
	}
	if f.fixture.Server.BuildCalls() != 0 || f.fixture.Server.ImportCalls() != 0 {
		t.Fatal("blocked queue request executed")
	}
	return stored
}
func TestOwnerQueuePreparedRequestSurvivesProducerRestart(t *testing.T) {
	f := newBlockedQueueProducer(t)
	cancelFirstQueueRequest(t, f)
	restarted, err := sharedvalidator.NewProducer(context.Background(), f.replica, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Stop()
	if err := restarted.UseOwnerQueue(context.Background(), sharedvalidator.OwnerQueueOptions{MaxTransactions: 6, MaxEncodedBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	b, err := restarted.Produce(context.Background(), f.fixture.Chain.GenesisBlock().Hash())
	if err != nil || len(b.Transactions()) != 6 {
		t.Fatalf("prepared request restart %v", err)
	}
	first, second := <-f.requests, <-f.requests
	if !bytes.Equal(first, second) {
		t.Fatal("producer restart changed prepared queue request")
	}
	if f.fixture.Server.BuildCalls() != 1 || f.fixture.Server.ImportCalls() != 1 {
		t.Fatal("restart duplicated queue execution")
	}
	// The returned binding is a copy, never a mutable process-role handle.
	binding := restarted.OwnerQueueBinding()
	original := *binding
	binding.Epoch[0] ^= 1
	if *restarted.OwnerQueueBinding() != original {
		t.Fatal("caller changed pinned owner epoch")
	}
}
func TestOwnerQueueNewEpochRejectsUnsealedPreparedWAL(t *testing.T) {
	f := newBlockedQueueProducer(t)
	before := cancelFirstQueueRequest(t, f)
	old, _ := f.fixture.Server.StrictIngressStatus()
	f.producer.Stop()
	f.service.Stop()
	f.fixture.Server.Close()
	owner, err := sharedexec.New(f.fixture.Chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.EnableStrictIngress(sharedexec.StrictIngressOptions{Manifest: f.fixture.Manifest, MaxTransactions: 32, MaxEncodedBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	client := sharedexec.NewClient(sharedexec.StartUnixExecutionForTest(t, owner), owner.Identity())
	defer client.Close()
	f.fixture.Server = owner
	_, _, p := strictQueueReplica(t, f.fixture, client, f.db)
	if err := p.UseOwnerQueue(context.Background(), sharedvalidator.OwnerQueueOptions{MaxTransactions: 6, MaxEncodedBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	if p.OwnerQueueBinding().Epoch == old.Epoch {
		t.Fatal("new owner epoch repeated")
	}
	if _, err := p.Produce(context.Background(), f.fixture.Chain.GenesisBlock().Hash()); err == nil {
		t.Fatal("old unsealed request silently rebound to new epoch")
	}
	if owner.BuildCalls() != 0 || owner.ImportCalls() != 0 {
		t.Fatal("stale epoch reached executor")
	}
	var view [8]byte
	binary.BigEndian.PutUint64(view[:], 1)
	key := append([]byte("performance-own-queue-request/"), view[:]...)
	if err := f.db.View(context.Background(), func(tx kv.Tx) error {
		after, err := tx.GetOne(modules.HotStuffState, key)
		if !bytes.Equal(before, after) {
			t.Fatal("stale WAL overwritten")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

type queueViewJumpDB struct {
	kv.RwDB
	leader *hotstuff.HotStuff
	fired  atomic.Bool
}
type queueViewJumpTx struct {
	kv.Tx
	db *queueViewJumpDB
}

func (tx queueViewJumpTx) GetOne(table string, key []byte) ([]byte, error) {
	if table == modules.HotStuffState && bytes.HasPrefix(key, []byte("performance-own-queue-request/")) && tx.db.fired.CompareAndSwap(false, true) {
		// Advance an entire leader rotation during the prepared-WAL lookup. Prepare
		// sees view 8, while Produce has captured 1 and the signer remains leader.
		tx.db.leader.Engine().RestoreState(8, hotstuff.GenesisQC(), hotstuff.GenesisQC(), 0)
	}
	return tx.Tx.GetOne(table, key)
}
func (db *queueViewJumpDB) View(ctx context.Context, fn func(kv.Tx) error) error {
	return db.RwDB.View(ctx, func(tx kv.Tx) error { return fn(queueViewJumpTx{tx, db}) })
}
func TestOwnerQueuePrepareViewChangeCannotBindWrongWAL(t *testing.T) {
	f := sharedexec.NewStrictIngressFixtureForTest(t)
	strictQueueAdmit(t, f)
	client := sharedexec.NewClient(sharedexec.StartUnixExecutionForTest(t, f.Server), f.Server.Identity())
	defer client.Close()
	base := memdb.New(t.TempDir())
	t.Cleanup(base.Close)
	db := &queueViewJumpDB{RwDB: base, leader: f.Leader}
	_, _, p := strictQueueReplica(t, f, client, db)
	if err := p.UseOwnerQueue(context.Background(), sharedvalidator.OwnerQueueOptions{MaxTransactions: 6, MaxEncodedBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Produce(context.Background(), f.Chain.GenesisBlock().Hash()); err == nil {
		t.Fatal("view changed during Prepare was accepted")
	}
	if !db.fired.Load() || f.Leader.Engine().CurrentView() != 8 || !f.Leader.IsCurrentLeader() {
		t.Fatal("same-leader view race not exercised")
	}
	if f.Server.BuildCalls() != 0 || f.Server.ImportCalls() != 0 {
		t.Fatal("wrong-view request reached execution")
	}
	if err := base.View(context.Background(), func(tx kv.Tx) error {
		for _, view := range []uint64{1, 8} {
			var raw [8]byte
			binary.BigEndian.PutUint64(raw[:], view)
			key := append([]byte("performance-own-queue-request/"), raw[:]...)
			value, err := tx.GetOne(modules.HotStuffState, key)
			if err != nil {
				return err
			}
			if len(value) > 0 {
				t.Fatal("wrong-view prepared WAL persisted")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerQueueStopCancelsBlockedBuildWithoutProposal(t *testing.T) {
	f := newBlockedQueueProducer(t)
	done := make(chan error, 1)
	go func() {
		_, err := f.producer.Produce(context.Background(), f.fixture.Chain.GenesisBlock().Hash())
		done <- err
	}()
	select {
	case <-f.entered:
	case <-time.After(time.Second):
		t.Fatal("queue request did not enter")
	}
	stopped := make(chan struct{})
	go func() { f.producer.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel/drain queue build")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stopped queue producer proposed")
		}
	case <-time.After(time.Second):
		t.Fatal("queue Produce survived Stop")
	}
	if f.fixture.Server.BuildCalls() != 0 || f.fixture.Server.ImportCalls() != 0 {
		t.Fatal("stopped queue request executed")
	}
	var view [8]byte
	binary.BigEndian.PutUint64(view[:], 1)
	key := append([]byte("performance-own-queue-request/"), view[:]...)
	if err := f.db.View(context.Background(), func(tx kv.Tx) error {
		raw, err := tx.GetOne(modules.HotStuffState, key)
		if len(raw) == 0 {
			t.Fatal("Stop discarded prepared request WAL")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

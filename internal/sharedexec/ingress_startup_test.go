package sharedexec

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

func appliedAheadQueueFixture(t *testing.T) (strictIngressFixture, *block.Block, *block.Block) {
	t.Helper()
	f := newStrictIngressFixture(t, 32, 1<<20)
	client := NewClient(startExecutionSocket(t, f.server), f.server.Identity())
	t.Cleanup(client.Close)
	request := queueRequest(t, f, f.template.Header().(*block.Header), 6)
	built, err := client.BuildFromQueue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	first := queueSealImport(t, f, client, f.leader, built)
	commit := CommitRequest{Identity: f.server.Identity(), BlockHash: first.Hash(), ParentHash: first.ParentHash(), Number: 1}
	if _, err := client.CommitCertified(context.Background(), commit, queueQC(f.keys, first.Hash(), 1, 5)); err != nil {
		t.Fatal(err)
	}
	leader, header := queueLeader(t, f, first, 2)
	built, err = client.BuildFromQueue(context.Background(), queueRequest(t, f, header, 6))
	if err != nil {
		t.Fatal(err)
	}
	second := queueSealImport(t, f, client, leader, built)
	f.server.Close()
	return f, first, second
}

func TestStrictIngressStartupRecoveryRequiresActualCanonicalQC(t *testing.T) {
	ctx := context.Background()
	f, first, second := appliedAheadQueueFixture(t)
	snapshot, err := f.chain.ReadPerformanceCanonicalAccounts(ctx, nil)
	if !errors.Is(err, internal.ErrPerformanceCanonicalNotApplied) || snapshot.Hash != first.Hash() || snapshot.Number != 1 || snapshot.Accounts != nil {
		t.Fatalf("typed metadata contract: %+v %v", snapshot, err)
	}
	owner, err := New(f.chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	if err := owner.EnableStrictIngress(StrictIngressOptions{Manifest: f.manifest, MaxTransactions: 32, MaxEncodedBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	f.server = owner
	client := NewClient(startExecutionSocket(t, owner), owner.Identity())
	defer client.Close()
	status, _ := owner.StrictIngressStatus()
	if !status.Reconciling || status.CanonicalHash != first.Hash() || status.CanonicalNumber != 1 || !owner.ingress.bootstrapPending {
		t.Fatalf("startup was silently state-ready: %+v", status)
	}
	ack, err := owner.AdmitStrictFrame(ctx, owner.Identity(), status.Epoch, f.frames[0])
	if !errors.Is(err, ErrIngressSnapshotChanged) || ack.NewlyAdmitted != 0 {
		t.Fatalf("admission escaped recovery gate: %+v %v", ack, err)
	}
	_, header := queueLeader(t, f, second, 3)
	if _, err := client.BuildFromQueue(ctx, queueRequest(t, f, header, 6)); err == nil || !strings.Contains(err.Error(), "reconciling") {
		t.Fatalf("new candidate escaped recovery gate: %v", err)
	}
	// A locked parent at the tip has no child to re-propose. This state stays
	// blocked without a usable CommitQC; the owner must not invent one/build.
	none, err := client.AppliedChild(ctx, AppliedChildRequest{Identity: owner.Identity(), ParentHash: second.Hash(), Number: 3, View: 3})
	if err != nil || len(none.BlockRLP) != 0 || none.Executed != nil {
		t.Fatalf("tip unexpectedly recovered itself: %+v %v", none, err)
	}
	child, err := client.AppliedChild(ctx, AppliedChildRequest{Identity: owner.Identity(), ParentHash: first.Hash(), Number: 2, View: 3})
	if err != nil || child.Executed == nil || child.Executed.BlockHash != second.Hash() {
		t.Fatalf("existing applied child unavailable during recovery: %+v %v", child, err)
	}
	old := CommitRequest{Identity: owner.Identity(), BlockHash: first.Hash(), ParentHash: first.ParentHash(), Number: 1}
	if got, err := client.CommitCertified(ctx, old, queueQC(f.keys, first.Hash(), 1, 5)); err != nil || !got.Committed {
		t.Fatalf("valid duplicate QC was not acknowledged: %+v %v", got, err)
	}
	status, _ = owner.StrictIngressStatus()
	if !status.Reconciling || !owner.ingress.bootstrapPending || status.CanonicalHash != first.Hash() {
		t.Fatal("duplicate lower QC cleared recovery gate")
	}
	current := CommitRequest{Identity: owner.Identity(), BlockHash: second.Hash(), ParentHash: first.Hash(), Number: 2}
	if _, err := client.CommitCertified(ctx, current, queueQC(f.keys, second.Hash(), 2, 4)); err == nil {
		t.Fatal("four votes finalized recovery")
	}
	status, _ = owner.StrictIngressStatus()
	if !status.Reconciling || status.CanonicalHash != first.Hash() {
		t.Fatal("rejected QC changed recovery state")
	}
	if got, err := client.CommitCertified(ctx, current, queueQC(f.keys, second.Hash(), 2, 5)); err != nil || !got.Committed {
		t.Fatalf("five-vote recovery failed: %+v %v", got, err)
	}
	status, _ = owner.StrictIngressStatus()
	if status.Reconciling || owner.ingress.bootstrapPending || status.CanonicalHash != second.Hash() || owner.BuildCalls() != 0 || owner.ImportCalls() != 0 {
		t.Fatalf("QC did not reconcile without execution: %+v", status)
	}
	ack, err = owner.AdmitStrictFrame(ctx, owner.Identity(), status.Epoch, f.frames[0])
	if err != nil || ack.NewlyAdmitted != 3 {
		t.Fatalf("real canonical nonce baseline not restored: %+v %v", ack, err)
	}
}

func TestStrictIngressStartupRecoveryRejectsMissingAppliedMarker(t *testing.T) {
	f, _, _ := appliedAheadQueueFixture(t)
	if err := f.chain.DB().Update(context.Background(), func(tx kv.RwTx) error { return tx.Delete(modules.DatabaseInfo, []byte("qmdbAppliedHead")) }); err != nil {
		t.Fatal(err)
	}
	owner, err := New(f.chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := owner.EnableStrictIngress(StrictIngressOptions{Manifest: f.manifest, MaxTransactions: 32, MaxEncodedBytes: 1 << 20}); err == nil || errors.Is(err, internal.ErrPerformanceCanonicalNotApplied) {
		t.Fatalf("generic storage failure became recoverable: %v", err)
	}
}

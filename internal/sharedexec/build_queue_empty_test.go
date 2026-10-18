package sharedexec

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/block"
)

func queuePlanBudgetForTest(s *Server) (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queuePlans), s.buildBytes
}

func TestQueueBuildEmptySelectionRetainsNoPlanAndSameRequestLaterAdmits(t *testing.T) {
	ctx := context.Background()
	f := newStrictIngressFixture(t, 6, 1<<20)
	request := queueRequest(t, f, f.template.Header().(*block.Header), 6)
	before, _ := f.server.StrictIngressStatus()
	for i := 0; i < 2; i++ {
		result, err := f.server.BuildFromQueue(ctx, request)
		if !errors.Is(err, ErrQueueBuildEmpty) || len(result.BlockRLP) != 0 {
			t.Fatalf("empty selection result %v %v", result, err)
		}
		after, _ := f.server.StrictIngressStatus()
		if after != before || f.server.BuildCalls() != 0 || f.server.ImportCalls() != 0 || f.chain.CurrentBlock().Hash() != f.chain.GenesisBlock().Hash() {
			t.Fatal("empty selection executed, imported, or changed canonical ingress")
		}
		if plans, budget := queuePlanBudgetForTest(f.server); plans != 0 || budget != 0 {
			t.Fatalf("empty selection retained plan/budget %d/%d", plans, budget)
		}
	}
	client := NewClient(startExecutionSocket(t, f.server), f.server.Identity())
	defer client.Close()
	// The existing Unix API still returns a generic HTTP 409 error.
	if result, err := client.BuildFromQueue(ctx, request); err == nil || !strings.Contains(err.Error(), ErrQueueBuildEmpty.Error()) || len(result.BlockRLP) != 0 {
		t.Fatalf("empty Unix selection %v %v", result, err)
	}
	if plans, budget := queuePlanBudgetForTest(f.server); plans != 0 || budget != 0 {
		t.Fatal("empty Unix request retained owner plan")
	}
	if _, err := f.server.AdmitStrictFrame(ctx, f.server.Identity(), before.Epoch, f.frames[0]); err != nil {
		t.Fatal(err)
	}
	result, err := client.BuildFromQueue(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	var built block.Block
	if err := built.Unmarshal(result.BlockRLP); err != nil || len(built.Transactions()) != 3 || f.server.BuildCalls() != 1 || f.server.ImportCalls() != 0 {
		t.Fatalf("same request did not select later admitted work: %v", err)
	}
}

func TestQueueBuildNonemptyIneligibleByteLimitDoesNotBindView(t *testing.T) {
	ctx := context.Background()
	f := newStrictIngressFixture(t, 6, 1<<20)
	status, _ := f.server.StrictIngressStatus()
	if _, err := f.server.AdmitStrictFrame(ctx, f.server.Identity(), status.Epoch, f.frames[0]); err != nil {
		t.Fatal(err)
	}
	before, _ := f.server.StrictIngressStatus()
	request := queueRequest(t, f, f.template.Header().(*block.Header), 6)
	request.MaxEncodedBytes = 1
	if _, err := f.server.BuildFromQueue(ctx, request); !errors.Is(err, ErrQueueBuildEmpty) {
		t.Fatalf("ineligible nonempty queue %v", err)
	}
	after, _ := f.server.StrictIngressStatus()
	if before.PendingTransactions != 3 || after != before || f.server.BuildCalls() != 0 || f.server.ImportCalls() != 0 {
		t.Fatal("unselected queue entries changed")
	}
	if plans, budget := queuePlanBudgetForTest(f.server); plans != 0 || budget != 0 {
		t.Fatal("ineligible selection retained plan or budget")
	}
	// No selected body existed, so the view may bind to newly usable limits.
	request.MaxEncodedBytes = 1 << 20
	if _, err := f.server.BuildFromQueue(ctx, request); err != nil {
		t.Fatal(err)
	}
}

func TestQueueBuildAlreadyAppliedQueueDoesNotCreateEmptyDescendant(t *testing.T) {
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
	first, err := client.BuildFromQueue(ctx, queueRequest(t, f, f.template.Header().(*block.Header), 6))
	if err != nil {
		t.Fatal(err)
	}
	applied := queueSealImport(t, f, client, f.leader, first)
	_, header := queueLeader(t, f, applied, 2)
	before, _ := f.server.StrictIngressStatus()
	plansBefore, budgetBefore := queuePlanBudgetForTest(f.server)
	if before.PendingTransactions != 6 || before.CanonicalNumber != 0 {
		t.Fatal("fixture must retain uncommitted queue entries")
	}
	if _, err := f.server.BuildFromQueue(ctx, queueRequest(t, f, header, 6)); !errors.Is(err, ErrQueueBuildEmpty) {
		t.Fatalf("already applied entries produced an empty descendant: %v", err)
	}
	after, _ := f.server.StrictIngressStatus()
	plansAfter, budgetAfter := queuePlanBudgetForTest(f.server)
	if after != before || plansAfter != plansBefore || budgetAfter != budgetBefore || f.server.BuildCalls() != 1 || f.server.ImportCalls() != 1 {
		t.Fatal("empty descendant changed execution, queue, or retained first plan")
	}
	recovered, err := client.AppliedChild(ctx, AppliedChildRequest{Identity: f.server.Identity(), ParentHash: f.chain.GenesisBlock().Hash(), Number: 1, View: 2})
	if err != nil || recovered.Executed == nil || recovered.Executed.BlockHash != applied.Hash() {
		t.Fatalf("empty selection blocked retained applied child: %v", err)
	}
}

package sharedexec

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
)

func TestResumeIngressOriginalFramesActualExecutionAndPhysicalReopen(t *testing.T) {
	f := newResumeExecutionFixture(t)
	ctx := context.Background()
	if err := f.server.EnableResumeIngress(ctx, f.options()); err != nil {
		t.Fatal(err)
	}
	initial, err := f.server.StrictIngressStatus()
	if err != nil {
		t.Fatal(err)
	}
	if initial.NewlyAdmitted != 0 || initial.CryptoVerified != 0 || initial.PendingTransactions != 0 || initial.Reconciling {
		t.Fatal("source verification leaked into online counters")
	}
	if err := f.server.EnableResumeIngress(ctx, f.options()); err == nil {
		t.Fatal("same owner armed twice")
	}
	socket := startStrictIngressSocket(t, f.server)
	var ackNew, ackSkipped int
	for _, selected := range f.plan.Frames {
		frame := f.frames[selected.SenderIndex]
		prior, _ := f.server.StrictIngressStatus()
		if _, err := f.server.AdmitStrictFrame(ctx, f.server.Identity(), initial.Epoch, frame); err == nil {
			t.Fatal("legacy API bypassed sealed plan")
		}
		client, err := DialResumeIngress(ctx, socket, f.server.Identity(), initial.ManifestHash, f.planID, initial.Epoch, frame.Shard)
		if err != nil {
			t.Fatal(err)
		}
		ack, err := client.Admit(ctx, frame, selected)
		_ = client.Close()
		if err != nil || ack.Duplicate || ack.NewlyAdmitted != int(selected.NewlyAdmitted) || ack.CanonicalSkipped != int(selected.CanonicalSkipped) {
			t.Fatalf("sealed original ACK %+v %v", ack, err)
		}
		ackNew += ack.NewlyAdmitted
		ackSkipped += ack.CanonicalSkipped
		if selected.NewlyAdmitted == 0 && (ack.Status.PendingTransactions != prior.PendingTransactions || ack.Status.RetainedEncodedBytes != prior.RetainedEncodedBytes) {
			t.Fatal("all-skipped frame retained a queue slab")
		}
	}
	status, _ := f.server.StrictIngressStatus()
	var retained int
	for _, frame := range f.plan.Frames {
		if frame.NewlyAdmitted > 0 {
			retained += int(frame.PayloadBytes)
		}
	}
	if ackNew != 1417 || ackSkipped != 583 || status.CryptoVerified != 2000 || status.NewlyAdmitted != 1417 || status.PendingTransactions != 1417 || status.RetainedEncodedBytes != retained || status.ReservedTransactions != 0 || status.ReservedEncodedBytes != 0 || f.server.ingress.resume.canonicalSkipped != 583 {
		t.Fatalf("online counters or full-slab credit differs %+v", status)
	}
	// A retry of a published original frame authenticates its full 500 tx but
	// returns the immutable receipt without adding skip/new/retained credits.
	selected := f.plan.Frames[1]
	frame := f.frames[selected.SenderIndex]
	duplicate, err := f.server.AdmitResumeFrame(ctx, f.server.Identity(), f.planID, initial.Epoch, frame)
	if err != nil || !duplicate.Duplicate || duplicate.NewlyAdmitted != 0 || duplicate.PriorAdmission != int(selected.NewlyAdmitted) {
		t.Fatalf("duplicate %+v %v", duplicate, err)
	}
	b := f.executeQueue(t, 8)
	before, _ := f.server.StrictIngressStatus()
	if before.PendingTransactions != 1417 {
		t.Fatal("build/import released uncommitted resume suffix")
	}
	f.commit(t, b, 8, 4)
	afterFour, _ := f.server.StrictIngressStatus()
	if afterFour.PendingTransactions != 1417 || afterFour.Reconciling {
		t.Fatal("four votes pruned or wedged queue")
	}
	f.commit(t, b, 8, 5)
	f.commit(t, b, 8, 5)
	drained, _ := f.server.StrictIngressStatus()
	if drained.PendingTransactions != 0 || drained.RetainedEncodedBytes != 0 || drained.ReservedTransactions != 0 || drained.ReservedEncodedBytes != 0 || drained.NewlyAdmitted != 1417 || f.server.ingress.resume.canonicalSkipped != 583 {
		t.Fatalf("canonical credit release %+v", drained)
	}
	duplicate, err = f.server.AdmitResumeFrame(ctx, f.server.Identity(), f.planID, initial.Epoch, frame)
	if err != nil || !duplicate.Duplicate || duplicate.NewlyAdmitted != 0 {
		t.Fatalf("committed receipt lost %+v %v", duplicate, err)
	}
	f.closeOwner()
	f.open(t)
	if err := f.server.EnableResumeIngress(ctx, f.options()); err == nil {
		t.Fatal("old anchor rearmed after real progress")
	}
	if f.server.ingress != nil || f.server.resumeArming {
		t.Fatal("failed rearm left a partial session")
	}
	if err := f.server.EnableStrictIngress(StrictIngressOptions{Manifest: f.manifestRaw, MaxTransactions: 1417, MaxEncodedBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	fresh, _ := f.server.StrictIngressStatus()
	if fresh.Epoch == initial.Epoch || fresh.NewlyAdmitted != 0 || fresh.CryptoVerified != 0 || fresh.PendingTransactions != 0 || fresh.Reconciling {
		t.Fatal("physical reopen reused volatile ACK state")
	}
	snapshot, err := f.chain.ReadPerformanceCanonicalAccounts(ctx, f.senders[:])
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Number != 2 || snapshot.Hash != b.Hash() || snapshot.Root != b.StateRoot() || snapshot.AppliedRoot != snapshot.Root {
		t.Fatal("physical reopen canonical/root differs")
	}
	var sum uint64
	for _, address := range f.senders {
		a := snapshot.Accounts[address]
		sum += a.Nonce
		if a.Nonce != 500 {
			t.Fatalf("canonical suffix nonce %d", a.Nonce)
		}
	}
	if sum != 2000 {
		t.Fatal("canonical nonce total differs")
	}
}

func TestResumeIngressArmingExcludesNewWorkAndCloseDrains(t *testing.T) {
	f := newResumeExecutionFixture(t)
	f.server.execute.Lock()
	var unlocked sync.Once
	unlock := func() { unlocked.Do(f.server.execute.Unlock) }
	defer unlock()
	done := make(chan error, 1)
	go func() { done <- f.server.EnableResumeIngress(context.Background(), f.options()) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.server.mu.Lock()
		arming := f.server.resumeArming
		f.server.mu.Unlock()
		if arming {
			break
		}
		if time.Now().After(deadline) {
			unlock()
			t.Fatal("arming not registered")
		}
		time.Sleep(time.Millisecond)
	}
	dir := t.TempDir()
	socket := filepath.Join(dir, "never.sock")
	if _, listener, err := f.server.ListenUnix(socket); err == nil {
		if listener != nil {
			_ = listener.Close()
		}
		t.Error("listen bypassed arming")
	}
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Error("arming created a socket")
	}
	request := httptest.NewRequest(http.MethodPost, "http://unix/v1/ingress-status", bytes.NewBufferString(`{}`))
	response := httptest.NewRecorder()
	f.server.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatal("HTTP served during arming")
	}
	if err := f.server.EnableOwnBuildReuse(); err == nil {
		t.Error("concurrent configuration accepted")
	}
	if err := f.server.EnableResumeIngress(context.Background(), f.options()); err == nil {
		t.Error("second arming accepted")
	}
	// The real API registration gate is exercised with valid bounded content;
	// no test-only callback or weaker execution path is installed.
	p := f.chain.CurrentBlock()
	view := hotstuff.ViewNumber(8)
	f.leader.Engine().RestoreState(view, f.lastQC, f.lastQC, 0)
	raw, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.server.Import(context.Background(), ImportRequest{Identity: f.server.Identity(), BlockHash: p.Hash(), BlockRLP: raw})
	if err == nil {
		t.Error("import registered during arming")
	}
	requestBuild := BuildRequest{Identity: f.server.Identity(), TemplateHash: crypto.Keccak256Hash(raw), TemplateRLP: raw}
	if _, err := f.server.Build(context.Background(), requestBuild); err == nil {
		t.Error("build registered during arming")
	}
	_, err = f.server.commit(context.Background(), CommitRequest{Identity: f.server.Identity(), BlockHash: p.Hash(), ParentHash: p.ParentHash(), Number: p.Number64().Uint64()}, nil)
	if err == nil {
		t.Error("commit registered during arming")
	}
	_, err = f.server.AppliedChild(context.Background(), AppliedChildRequest{Identity: f.server.Identity(), ParentHash: p.Hash(), Number: p.Number64().Uint64() + 1, View: view})
	if err == nil {
		t.Error("applied-child registered during arming")
	}
	f.server.mu.Lock()
	if len(f.server.jobs) != 0 || len(f.server.builds) != 0 || f.server.started {
		t.Error("arming registered new work")
	}
	f.server.mu.Unlock()
	closed := make(chan struct{})
	go func() { f.server.Close(); close(closed) }()
	select {
	case <-closed:
		t.Error("Close returned before startup drained")
	case <-time.After(5 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-done:
		if err == nil {
			t.Error("canceled startup succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("startup did not leave execute")
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not drain startup")
	}
	if f.server.ingress != nil || f.server.resumeArming {
		t.Fatal("canceled startup published or retained gate")
	}
}

func TestResumeIngressFailedPinsAndUsedOwnerNeverPublish(t *testing.T) {
	f := newResumeExecutionFixture(t)
	for _, change := range []func(*ResumeIngressOptions){
		func(o *ResumeIngressOptions) { o.ExpectedPlanID[0]++ },
		func(o *ResumeIngressOptions) {
			o.SourceProofRaw = append([]byte(nil), o.SourceProofRaw...)
			o.SourceProofRaw[0] ^= 1
		},
		func(o *ResumeIngressOptions) { o.MaxEncodedBytes = 1 },
		func(o *ResumeIngressOptions) { o.GenesisRaw = append(o.GenesisRaw, ' ') },
	} {
		options := f.options()
		change(&options)
		if err := f.server.EnableResumeIngress(context.Background(), options); err == nil {
			t.Fatal("wrong pinned startup accepted")
		}
		if f.server.ingress != nil || f.server.resumeArming {
			t.Fatal("failed startup left state")
		}
	}
	// These owner-only changes leave the already-stored genesis/header and
	// public Identity unchanged. The public typed startup API must still reject
	// a source prepared for the original fork/validator configuration.
	identity := f.server.Identity()
	config := f.chain.Config()
	originalShanghai := config.ShanghaiTime
	config.ShanghaiTime = big.NewInt(123456789)
	err := f.server.EnableResumeIngress(context.Background(), f.options())
	config.ShanghaiTime = originalShanghai
	if err == nil || !strings.Contains(err.Error(), "source/owner chain configuration differs") {
		t.Fatalf("same-identity different fork accepted: %v", err)
	}
	originalValidator := config.HotStuff.Validators[0].Address
	config.HotStuff.Validators[0].Address = "0x000000000000000000000000000000000000ffff"
	err = f.server.EnableResumeIngress(context.Background(), f.options())
	config.HotStuff.Validators[0].Address = originalValidator
	if err == nil || !strings.Contains(err.Error(), "source/owner chain configuration differs") {
		t.Fatalf("same-identity different validator accepted: %v", err)
	}
	if f.server.Identity() != identity || f.server.ingress != nil || f.server.resumeArming {
		t.Fatal("configuration rejection changed identity or published startup state")
	}
	response := httptest.NewRecorder()
	f.server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://unix/unused", nil))
	if err := f.server.EnableResumeIngress(context.Background(), f.options()); err == nil {
		t.Fatal("used owner with zero jobs rearmed")
	}
	if f.server.ingress != nil || f.server.resumeArming {
		t.Fatal("used owner published state")
	}
}

func TestResumeIngressChangedPayloadRejectedAtomically(t *testing.T) {
	f := newResumeExecutionFixture(t)
	ctx := context.Background()
	if err := f.server.EnableResumeIngress(ctx, f.options()); err != nil {
		t.Fatal(err)
	}
	status, _ := f.server.StrictIngressStatus()
	before := status
	frame := f.frames[1]
	frame.Payload = append([]byte(nil), frame.Payload...)
	frame.Payload[len(frame.Payload)-1] ^= 1
	if _, err := f.server.AdmitResumeFrame(ctx, f.server.Identity(), f.planID, status.Epoch, frame); err == nil {
		t.Fatal("changed late original bytes accepted")
	}
	after, _ := f.server.StrictIngressStatus()
	if !reflect.DeepEqual(before, after) || len(f.server.ingress.resume.receipts) != 0 {
		t.Fatal("invalid frame partially published")
	}
	wrong := f.planID
	wrong[0]++
	if _, err := f.server.AdmitResumeFrame(ctx, f.server.Identity(), wrong, status.Epoch, f.frames[1]); err == nil {
		t.Fatal("wrong plan accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := f.server.AdmitResumeFrame(canceled, f.server.Identity(), f.planID, status.Epoch, f.frames[1]); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not retained %v", err)
	}
	after, _ = f.server.StrictIngressStatus()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("canceled frame leaked reservation")
	}
}

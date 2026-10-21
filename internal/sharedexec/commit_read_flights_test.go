package sharedexec

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
)

func TestCertifiedDuplicateFlightKeyBindsCompleteRequestAndQC(t *testing.T) {
	request := CommitRequest{Identity: Identity{Version: 1, Namespace: "one", ChainID: "4205001", GenesisHash: types.Hash{1}}, BlockHash: types.Hash{2}, ParentHash: types.Hash{3}, Number: 4}
	certificate := hotstuff.QuorumCertificate{View: 5, BlockHash: request.BlockHash, Signers: []bool{true, true, true, true, true, false, false}, AggregateSignature: make([]byte, 96)}
	key, ok := certifiedDuplicateKey(request, &certificate)
	if !ok {
		t.Fatal("valid fixed-size key rejected")
	}
	mutations := []func(*CommitRequest, *hotstuff.QuorumCertificate){
		func(r *CommitRequest, _ *hotstuff.QuorumCertificate) { r.Identity.Version++ },
		func(r *CommitRequest, _ *hotstuff.QuorumCertificate) { r.Identity.Namespace += "other" },
		func(r *CommitRequest, _ *hotstuff.QuorumCertificate) { r.Identity.ChainID += "other" },
		func(r *CommitRequest, _ *hotstuff.QuorumCertificate) { r.Identity.GenesisHash[0]++ },
		func(r *CommitRequest, _ *hotstuff.QuorumCertificate) { r.BlockHash[0]++ },
		func(r *CommitRequest, _ *hotstuff.QuorumCertificate) { r.ParentHash[0]++ },
		func(r *CommitRequest, _ *hotstuff.QuorumCertificate) { r.Number++ },
		func(_ *CommitRequest, q *hotstuff.QuorumCertificate) { q.View++ },
		func(_ *CommitRequest, q *hotstuff.QuorumCertificate) { q.BlockHash[0]++ },
	}
	for _, mutate := range mutations {
		r, q := request, certificate.Clone()
		mutate(&r, &q)
		if changed, ok := certifiedDuplicateKey(r, &q); !ok || changed == key {
			t.Fatal("different request or QC shared a key")
		}
	}
	for i := range certificate.Signers {
		q := certificate.Clone()
		q.Signers[i] = !q.Signers[i]
		if changed, _ := certifiedDuplicateKey(request, &q); changed == key {
			t.Fatal("signer bit omitted from key")
		}
	}
	for i := range certificate.AggregateSignature {
		q := certificate.Clone()
		q.AggregateSignature[i]++
		if changed, _ := certifiedDuplicateKey(request, &q); changed == key {
			t.Fatal("signature byte omitted from key")
		}
	}
	for _, lengths := range [][2]int{{6, 96}, {8, 96}, {7, 95}, {7, 97}} {
		q := certificate.Clone()
		q.Signers, q.AggregateSignature = make([]bool, lengths[0]), make([]byte, lengths[1])
		if _, ok := certifiedDuplicateKey(request, &q); ok {
			t.Fatal("invalid QC dimensions truncated into a valid key")
		}
	}
	if _, ok := certifiedDuplicateKey(request, nil); ok {
		t.Fatal("nil QC accepted")
	}
}

// Keep the first caller alive after publication without delaying its worker or
// holding a production lock. A later caller must verify the database again.
type certifiedPublishedWaitContext struct {
	context.Context
	job     atomic.Pointer[certifiedReadJob]
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *certifiedPublishedWaitContext) Err() error {
	if job := c.job.Load(); job != nil {
		select {
		case <-job.done:
			c.once.Do(func() { close(c.entered) })
			<-c.release
		default:
		}
	}
	return c.Context.Err()
}

func TestCertifiedDuplicateFlightDeletesBeforeOldWaiterReturns(t *testing.T) {
	f, b, request := certifiedCanonicalFixture(t, true)
	before, _ := f.server.StrictIngressStatus()
	d := readerGate(t, f, b, false)
	ctx := &certifiedPublishedWaitContext{Context: context.Background(), entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(ctx.release) }) }
	defer release()
	first := make(chan error, 1)
	go func() { _, err := f.server.CommitCertified(ctx, request); first <- err }()
	awaitCertifiedEvent(t, d.readerEntered, "physical read before publication")
	key, _ := certifiedDuplicateKey(request.CommitRequest, &request.Certificate)
	f.server.mu.Lock()
	job := f.server.certifiedReads[key]
	f.server.mu.Unlock()
	if job == nil {
		t.Fatal("active physical read was not registered")
	}
	ctx.job.Store(job)
	d.releaseReaders()
	awaitCertifiedEvent(t, ctx.entered, "old waiter paused after publication")
	f.server.mu.Lock()
	remaining := len(f.server.certifiedReads)
	f.server.mu.Unlock()
	if remaining != 0 {
		t.Fatal("completed flight retained in map")
	}
	result, err := f.server.CommitCertified(context.Background(), request)
	if err != nil || !result.Committed || d.reads.Load() != 2 {
		t.Fatalf("new request reused completed observation: %+v %v reads=%d", result, err, d.reads.Load())
	}
	select {
	case <-first:
		t.Fatal("old waiter was not held across the second physical check")
	default:
	}
	release()
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	after, _ := f.server.StrictIngressStatus()
	if !reflect.DeepEqual(before, after) || d.writes.Load() != 0 {
		t.Fatal("read-only publication changed queue or DB")
	}
	flights, joins := f.server.CertifiedDuplicateReadCounts()
	if flights != 2 || joins != 0 {
		t.Fatalf("unexpected actual flight counters: %d/%d", flights, joins)
	}
}

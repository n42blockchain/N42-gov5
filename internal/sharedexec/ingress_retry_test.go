package sharedexec

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

func TestStrictIngressSnapshotRetryAfterBodyKeepsConnection(t *testing.T) {
	f := newStrictIngressFixture(t, 6, 1<<20)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// Import an independently signed child but deliberately withhold CommitQC.
	// Its speculative PlainState must never be a new sender's nonce baseline.
	b := strictExecute(t, f, strictFrameRaws(t, f.frames[0]))
	if _, err := f.chain.ReadPerformanceCanonicalAccounts(ctx, nil); !errors.Is(err, internal.ErrPerformanceCanonicalNotApplied) {
		t.Fatalf("missing typed actual/canonical mismatch: %v", err)
	}
	path := startStrictIngressSocket(t, f.server)
	before, _ := f.server.StrictIngressStatus()
	c, err := DialStrictIngress(ctx, path, f.server.Identity(), before.ManifestHash, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Epoch() != before.Epoch {
		t.Fatal("welcome epoch unavailable before send")
	}
	if ack, err := c.Admit(ctx, f.frames[1]); !errors.Is(err, ErrIngressSnapshotChanged) || ack.NewlyAdmitted != 0 || ack.Duplicates != 0 {
		t.Fatalf("snapshot retry: %+v %v", ack, err)
	}
	after, _ := f.server.StrictIngressStatus()
	if after.NewlyAdmitted != 0 || after.PendingTransactions != 0 || after.ReservedTransactions != 0 || after.RetainedEncodedBytes != 0 || after.ReservedEncodedBytes != 0 {
		t.Fatalf("retry leaked publication or grant %+v", after)
	}
	if err := strictCommit(t, f, b, ctx); err != nil {
		t.Fatal(err)
	}
	ack, err := c.Admit(ctx, f.frames[1])
	if err != nil || ack.NewlyAdmitted != 3 || ack.Duplicates != 0 {
		t.Fatalf("same connection retry: %+v %v", ack, err)
	}
	after, _ = f.server.StrictIngressStatus()
	if after.NewlyAdmitted != 3 || after.PendingTransactions != 3 || after.CryptoVerified != 6 || after.ReservedTransactions != 0 || after.ReservedEncodedBytes != 0 {
		t.Fatalf("retry accounting %+v", after)
	}
}

func TestStrictIngressSnapshotRetryBeforeGrantReadsNoBody(t *testing.T) {
	f := newStrictIngressFixture(t, 6, 1<<20)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	path := startStrictIngressSocket(t, f.server)
	status, _ := f.server.StrictIngressStatus()
	c, err := DialStrictIngress(ctx, path, f.server.Identity(), status.ManifestHash, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	f.server.ingress.mu.Lock()
	f.server.ingress.reconciling = true
	f.server.ingress.mu.Unlock()
	if ack, err := c.Admit(ctx, f.frames[0]); !errors.Is(err, ErrIngressSnapshotChanged) || ack.NewlyAdmitted != 0 || ack.Duplicates != 0 {
		t.Fatalf("header retry %+v %v", ack, err)
	}
	status, _ = f.server.StrictIngressStatus()
	if status.CryptoVerified != 0 || status.ReservedTransactions != 0 || status.ReservedEncodedBytes != 0 {
		t.Fatal("header retry allocated/read body")
	}
	f.server.ingress.mu.Lock()
	f.server.ingress.reconciling = false
	f.server.ingress.mu.Unlock()
	if ack, err := c.Admit(ctx, f.frames[0]); err != nil || ack.NewlyAdmitted != 3 {
		t.Fatalf("header retry lost framing %+v %v", ack, err)
	}
}

func TestStrictIngressMalformedRetryIsUnknownAndNeverReplayed(t *testing.T) {
	for _, name := range []string{"unknown-reason", "nonzero-admission", "reason-on-grant"} {
		t.Run(name, func(t *testing.T) {
			f := newStrictIngressFixture(t, 6, 1<<20)
			status, _ := f.server.StrictIngressStatus()
			clientConn, serverConn := net.Pipe()
			defer clientConn.Close()
			defer serverConn.Close()
			c := &StrictIngressClient{conn: clientConn, identity: f.server.Identity(), manifest: status.ManifestHash, epoch: status.Epoch, shard: 0}
			done := make(chan error, 1)
			go func() {
				var header [CorpusWireHeaderSize]byte
				if _, err := io.ReadFull(serverConn, header[:]); err != nil {
					done <- err
					return
				}
				reply := f.server.ingressReply(f.server.ingress, "retry", f.frames[0], StrictIngressAck{}, ErrIngressSnapshotChanged)
				switch name {
				case "unknown-reason":
					reply.RetryReason = "storage_failure"
				case "nonzero-admission":
					reply.Ack.NewlyAdmitted = 1
				case "reason-on-grant":
					reply.Kind = "grant"
				}
				done <- writeIngressMetadata(serverConn, reply)
			}()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := c.Admit(ctx, f.frames[0]); !errors.Is(err, ErrIngressUnknownOutcome) {
				t.Fatalf("malformed retry accepted: %v", err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if _, err := c.Admit(ctx, f.frames[0]); !errors.Is(err, ErrIngressUnknownOutcome) {
				t.Fatalf("poisoned connection replayed: %v", err)
			}
		})
	}
}

func TestStrictIngressMissingAppliedMarkerRemainsFatal(t *testing.T) {
	f := newStrictIngressFixture(t, 6, 1<<20)
	ctx := context.Background()
	_ = strictExecute(t, f, strictFrameRaws(t, f.frames[0]))
	if err := f.chain.DB().Update(ctx, func(tx kv.RwTx) error { return tx.Delete(modules.DatabaseInfo, []byte("qmdbAppliedHead")) }); err != nil {
		t.Fatal(err)
	}
	if _, err := f.chain.ReadPerformanceCanonicalAccounts(ctx, nil); err == nil || errors.Is(err, internal.ErrPerformanceCanonicalNotApplied) {
		t.Fatalf("missing marker incorrectly became retryable: %v", err)
	}
	status, _ := f.server.StrictIngressStatus()
	if _, err := f.server.AdmitStrictFrame(ctx, f.server.Identity(), status.Epoch, f.frames[1]); err == nil || errors.Is(err, ErrIngressSnapshotChanged) {
		t.Fatalf("missing marker admission became retryable: %v", err)
	}
	after, _ := f.server.StrictIngressStatus()
	if after.NewlyAdmitted != 0 || after.ReservedTransactions != 0 || after.ReservedEncodedBytes != 0 {
		t.Fatal("fatal storage error leaked admission/grant")
	}
}

func TestStrictIngressNonceRejectionNeverBecomesSnapshotRetry(t *testing.T) {
	f := newStrictIngressFixture(t, 6, 1<<20)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	path := startStrictIngressSocket(t, f.server)
	status, _ := f.server.StrictIngressStatus()
	c, err := DialStrictIngress(ctx, path, f.server.Identity(), status.ManifestHash, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	raws := strictFrameRaws(t, f.frames[0])
	raws[0], raws[1] = raws[1], raws[0]
	frame, err := MakeCorpusFrame(f.frames[0].CorpusFrameID, raws)
	if err != nil {
		t.Fatal(err)
	}
	if ack, err := c.Admit(ctx, frame); err == nil || errors.Is(err, ErrIngressSnapshotChanged) || errors.Is(err, ErrIngressUnknownOutcome) || ack.NewlyAdmitted != 0 {
		t.Fatalf("nonce rejection changed class %+v %v", ack, err)
	}
	after, _ := f.server.StrictIngressStatus()
	if after.NewlyAdmitted != 0 || after.ReservedTransactions != 0 || after.ReservedEncodedBytes != 0 {
		t.Fatal("nonce rejection retained grant")
	}
	if _, err := c.Admit(ctx, f.frames[0]); !errors.Is(err, ErrIngressUnknownOutcome) {
		t.Fatalf("fatal connection reused: %v", err)
	}
}

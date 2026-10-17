package sharedexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"
)

// The proxy forwards the real hello, grant and complete original frame. It
// drops only the final ACK after the actual owner has already published it.
func TestResumeIngressLostPublishedFinalAckNeverReplays(t *testing.T) {
	f := newResumeExecutionFixture(t)
	ctx := context.Background()
	if err := f.server.EnableResumeIngress(ctx, f.options()); err != nil {
		t.Fatal(err)
	}
	status, _ := f.server.StrictIngressStatus()
	upstream := startStrictIngressSocket(t, f.server)
	proxyPath := filepath.Join(t.TempDir(), "ack-loss.sock")
	proxy, err := listenPrivateExecutionUnix(proxyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	done := make(chan error, 1)
	go func() {
		front, err := proxy.Accept()
		if err != nil {
			done <- err
			return
		}
		defer front.Close()
		back, err := net.Dial("unix", upstream)
		if err != nil {
			done <- err
			return
		}
		defer back.Close()
		_ = front.SetDeadline(time.Now().Add(10 * time.Second))
		_ = back.SetDeadline(time.Now().Add(10 * time.Second))
		run := func() error {
			var magic [4]byte
			if _, err := io.ReadFull(front, magic[:]); err != nil {
				return err
			}
			if err := writeIngressBytes(back, magic[:]); err != nil {
				return err
			}
			var hello resumeHello
			if err := readResumeMetadata(front, &hello); err != nil {
				return err
			}
			if err := writeIngressMetadata(back, hello); err != nil {
				return err
			}
			var reply resumeReply
			if err := readResumeMetadata(back, &reply); err != nil {
				return err
			}
			if err := writeIngressMetadata(front, reply); err != nil {
				return err
			}
			var header [CorpusWireHeaderSize]byte
			if _, err := io.ReadFull(front, header[:]); err != nil {
				return err
			}
			if err := writeIngressBytes(back, header[:]); err != nil {
				return err
			}
			if err := readResumeMetadata(back, &reply); err != nil {
				return err
			}
			if reply.Kind != "grant" {
				return fmt.Errorf("expected grant got %s", reply.Kind)
			}
			if err := writeIngressMetadata(front, reply); err != nil {
				return err
			}
			body := make([]byte, f.plan.Frames[0].PayloadBytes)
			if _, err := io.ReadFull(front, body); err != nil {
				return err
			}
			if err := writeIngressBytes(back, body); err != nil {
				return err
			}
			if err := readResumeMetadata(back, &reply); err != nil {
				return err
			}
			if reply.Kind != "admitted" || reply.Ack.NewlyAdmitted != 500 {
				return errors.New("owner did not actually publish before ACK loss")
			}
			return nil
		}
		done <- run()
	}()
	client, err := DialResumeIngress(ctx, proxyPath, f.server.Identity(), status.ManifestHash, f.planID, status.Epoch, f.frames[0].Shard)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	selected := f.plan.Frames[0]
	if _, err := client.Admit(ctx, f.frames[0], selected); !errors.Is(err, ErrIngressUnknownOutcome) {
		t.Fatalf("lost published ACK %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := client.Admit(ctx, f.frames[0], selected); !errors.Is(err, ErrIngressUnknownOutcome) {
		t.Fatal("poisoned connection replayed", err)
	}
	actual, _ := f.server.StrictIngressStatus()
	if actual.NewlyAdmitted != 500 || actual.PendingTransactions != 500 || actual.CryptoVerified != 500 || actual.ReservedTransactions != 0 || actual.ReservedEncodedBytes != 0 || len(f.server.ingress.resume.receipts) != 1 {
		t.Fatalf("unknown outcome was replayed or revoked %+v", actual)
	}
}

func TestResumeIngressConcurrentSameFramePublishesOneImmutableReceipt(t *testing.T) {
	f := newResumeExecutionFixture(t)
	ctx := context.Background()
	if err := f.server.EnableResumeIngress(ctx, f.options()); err != nil {
		t.Fatal(err)
	}
	status, _ := f.server.StrictIngressStatus()
	frame := f.frames[1]
	// Reserve twice before either publication, precisely the concurrent first
	// request boundary. No source/identity validation is replaced by this seam.
	first, err := f.server.reserveResumeIngress(f.server.ingress, f.planID, status.Epoch, frame, len(frame.Payload))
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.server.reserveResumeIngress(f.server.ingress, f.planID, status.Epoch, frame, len(frame.Payload))
	if err != nil {
		first.release()
		t.Fatal(err)
	}
	defer first.release()
	defer second.release()
	type result struct {
		ack ResumeIngressAck
		err error
	}
	results := make(chan result, 2)
	for _, reservation := range []*resumeReservation{first, second} {
		go func(r *resumeReservation) {
			ack, err := f.server.verifyReservedResume(ctx, f.server.ingress, frame, r)
			results <- result{ack, err}
		}(reservation)
	}
	newTotal, duplicates := 0, 0
	for range 2 {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		newTotal += got.ack.NewlyAdmitted
		if got.ack.Duplicate {
			duplicates++
			if got.ack.NewlyAdmitted != 0 || got.ack.PriorAdmission != 459 {
				t.Fatalf("duplicate receipt %+v", got.ack)
			}
		}
	}
	if newTotal != 459 || duplicates != 1 {
		t.Fatal("concurrent frame published twice")
	}
	after, _ := f.server.StrictIngressStatus()
	if after.NewlyAdmitted != 459 || after.PendingTransactions != 459 || after.ReservedTransactions != 0 || after.ReservedEncodedBytes != 0 || after.RetainedEncodedBytes != len(frame.Payload) || after.CryptoVerified != 1000 || f.server.ingress.resume.canonicalSkipped != 41 || len(f.server.ingress.resume.receipts) != 1 {
		t.Fatalf("concurrent publication leaked credits/counters %+v", after)
	}
	// A changed generation may become clean before old verification returns.
	// The stale publication must still return exact zero and release both budgets.
	freshFrame := f.frames[2]
	reservation, err := f.server.reserveResumeIngress(f.server.ingress, f.planID, status.Epoch, freshFrame, len(freshFrame.Payload))
	if err != nil {
		t.Fatal(err)
	}
	f.server.ingress.mu.Lock()
	f.server.ingress.stamp.generation++
	f.server.ingress.mu.Unlock()
	_, err = f.server.verifyReservedResume(ctx, f.server.ingress, freshFrame, reservation)
	reservation.release()
	if !errors.Is(err, ErrIngressSnapshotChanged) {
		t.Fatalf("stale canonical generation %v", err)
	}
	final, _ := f.server.StrictIngressStatus()
	if final.NewlyAdmitted != after.NewlyAdmitted || final.PendingTransactions != after.PendingTransactions || final.RetainedEncodedBytes != after.RetainedEncodedBytes || final.ReservedTransactions != 0 || final.ReservedEncodedBytes != 0 || len(f.server.ingress.resume.receipts) != 1 {
		t.Fatal("stale frame partially published")
	}
}

func TestResumeIngressMalformedSuccessAckIsUnknownAndPoisons(t *testing.T) {
	plan, _, _ := resumeStructureFixture(t)
	selected := plan.Frames[0]
	frame := CorpusFrame{CorpusFrameID: selected.ID, Transactions: 500, PayloadHash: selected.PayloadHash, Payload: make([]byte, selected.PayloadBytes)}
	for _, mode := range []string{"grant-error", "final-error", "plan", "epoch", "payload", "grant-count", "final-count"} {
		t.Run(mode, func(t *testing.T) {
			front, back := net.Pipe()
			defer front.Close()
			defer back.Close()
			epoch := [16]byte{1}
			planID := plan.SourceProofSHA256
			base := &StrictIngressClient{conn: front, identity: plan.Identity, manifest: plan.ManifestSHA256, epoch: epoch, shard: frame.Shard}
			client := &ResumeIngressClient{base: base, planID: planID}
			done := make(chan error, 1)
			go func() {
				defer back.Close()
				_ = back.SetDeadline(time.Now().Add(5 * time.Second))
				var header [CorpusWireHeaderSize]byte
				if _, err := io.ReadFull(back, header[:]); err != nil {
					done <- err
					return
				}
				status := StrictIngressStatus{Identity: plan.Identity, Epoch: epoch, ManifestHash: plan.ManifestSHA256, CanonicalHash: plan.Canonical.Hash, CanonicalNumber: plan.Canonical.Number}
				reply := resumeReply{Kind: "grant", Identity: plan.Identity, ManifestHash: plan.ManifestSHA256, PlanID: planID, Epoch: epoch, FrameID: frame.CorpusFrameID, PayloadHash: frame.PayloadHash, Ack: ResumeIngressAck{Status: status, PlanID: planID}}
				switch mode {
				case "grant-error":
					reply.Error = "inconsistent success"
				case "plan":
					reply.PlanID[0]++
				case "epoch":
					reply.Epoch[0]++
				case "payload":
					reply.PayloadHash[0]++
				case "grant-count":
					reply.Ack.CanonicalSkipped = 1
				}
				if err := writeIngressMetadata(back, reply); err != nil {
					done <- err
					return
				}
				if mode == "final-error" || mode == "final-count" {
					payload := make([]byte, len(frame.Payload))
					if _, err := io.ReadFull(back, payload); err != nil {
						done <- err
						return
					}
					reply.Kind = "admitted"
					reply.Ack.NewlyAdmitted = int(selected.NewlyAdmitted)
					reply.Ack.PriorAdmission = int(selected.NewlyAdmitted)
					reply.Ack.CanonicalSkipped = int(selected.CanonicalSkipped)
					if mode == "final-error" {
						reply.Error = "inconsistent success"
					} else {
						reply.Ack.NewlyAdmitted--
					}
					if err := writeIngressMetadata(back, reply); err != nil {
						done <- err
						return
					}
				}
				done <- nil
			}()
			if _, err := client.Admit(context.Background(), frame, selected); !errors.Is(err, ErrIngressUnknownOutcome) {
				t.Fatalf("malformed ACK accepted: %v", err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if _, err := client.Admit(context.Background(), frame, selected); !errors.Is(err, ErrIngressUnknownOutcome) {
				t.Fatal("malformed ACK allowed replay")
			}
		})
	}
}

// Pure transport fixture: known-zero results permit only explicit caller
// retries on the same connection and with the exact same original header.
func TestResumeIngressKnownZeroRetriesKeepSameConnectionAndFrame(t *testing.T) {
	plan, _, _ := resumeStructureFixture(t)
	selected := plan.Frames[1]
	frame := CorpusFrame{CorpusFrameID: selected.ID, Transactions: 500, PayloadHash: selected.PayloadHash, Payload: make([]byte, selected.PayloadBytes)}
	front, back := net.Pipe()
	defer front.Close()
	defer back.Close()
	epoch := [16]byte{2}
	planID := plan.SourceProofSHA256
	client := &ResumeIngressClient{base: &StrictIngressClient{conn: front, identity: plan.Identity, manifest: plan.ManifestSHA256, epoch: epoch, shard: frame.Shard}, planID: planID}
	done := make(chan error, 1)
	go func() {
		defer back.Close()
		_ = back.SetDeadline(time.Now().Add(5 * time.Second))
		headerExpected, err := frame.Header()
		if err != nil {
			done <- err
			return
		}
		for attempt := 0; attempt < 4; attempt++ {
			var header [CorpusWireHeaderSize]byte
			if _, err := io.ReadFull(back, header[:]); err != nil {
				done <- err
				return
			}
			if header != headerExpected {
				done <- errors.New("known-zero retry changed original header")
				return
			}
			status := StrictIngressStatus{Identity: plan.Identity, Epoch: epoch, ManifestHash: plan.ManifestSHA256, CanonicalHash: plan.Canonical.Hash, CanonicalNumber: plan.Canonical.Number}
			reply := resumeReply{Identity: plan.Identity, ManifestHash: plan.ManifestSHA256, PlanID: planID, Epoch: epoch, FrameID: frame.CorpusFrameID, PayloadHash: frame.PayloadHash, Ack: ResumeIngressAck{Status: status, PlanID: planID}}
			switch attempt {
			case 0:
				reply.Kind = "busy"
			case 1:
				reply.Kind = "retry"
				reply.RetryReason = ingressRetrySnapshot
			default:
				reply.Kind = "grant"
			}
			if err := writeIngressMetadata(back, reply); err != nil {
				done <- err
				return
			}
			if attempt >= 2 {
				payload := make([]byte, len(frame.Payload))
				if _, err := io.ReadFull(back, payload); err != nil {
					done <- err
					return
				}
				if !bytes.Equal(payload, frame.Payload) {
					done <- errors.New("retry changed original payload")
					return
				}
				if attempt == 2 {
					reply.Kind = "retry"
					reply.RetryReason = ingressRetrySnapshot
				} else {
					reply.Kind = "admitted"
					reply.Ack.CanonicalSkipped = int(selected.CanonicalSkipped)
					reply.Ack.NewlyAdmitted = int(selected.NewlyAdmitted)
					reply.Ack.PriorAdmission = int(selected.NewlyAdmitted)
				}
				if err := writeIngressMetadata(back, reply); err != nil {
					done <- err
					return
				}
			}
		}
		done <- nil
	}()
	for attempt := 0; attempt < 4; attempt++ {
		ack, err := client.Admit(context.Background(), frame, selected)
		expected := ErrIngressSnapshotChanged
		if attempt == 0 {
			expected = ErrIngressCreditsExhausted
		}
		if attempt < 3 {
			if !errors.Is(err, expected) || !zeroResumeCounts(ack) || client.base.broken {
				t.Fatalf("known-zero attempt%d %+v %v", attempt, ack, err)
			}
		} else if err != nil || ack.NewlyAdmitted != 459 || ack.CanonicalSkipped != 41 || ack.Duplicate {
			t.Fatalf("final explicit retry %+v %v", ack, err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

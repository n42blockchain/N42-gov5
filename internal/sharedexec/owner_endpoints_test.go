package sharedexec

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func ownerEndpointFixture(t *testing.T) (strictIngressFixture, string, context.CancelFunc, <-chan error) {
	t.Helper()
	f := newStrictIngressFixture(t, 6, 1<<20)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.server.ServePrivateUnix(ctx, dir, true) }()
	t.Cleanup(func() { cancel(); f.server.Close() })
	deadline := time.Now().Add(time.Second)
	for {
		_, httpErr := os.Stat(filepath.Join(dir, "execution.sock"))
		_, ingressErr := os.Stat(filepath.Join(dir, "ingress.sock"))
		if httpErr == nil && ingressErr == nil {
			return f, dir, cancel, done
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("owner endpoints did not bind")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestOwnerPrivateEndpointsDrainBothListeners(t *testing.T) {
	f, dir, cancel, done := ownerEndpointFixture(t)
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	client := NewClient(filepath.Join(dir, "execution.sock"), f.server.Identity())
	defer client.Close()
	status, err := client.StrictIngressStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := DialStrictIngress(ctx, filepath.Join(dir, "ingress.sock"), f.server.Identity(), status.ManifestHash, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer admitted.Close()
	if ack, err := admitted.Admit(ctx, f.frames[0]); err != nil || ack.NewlyAdmitted != 3 {
		t.Fatalf("dual endpoint admission %+v %v", ack, err)
	}
	partial := rawStrictIngress(t, filepath.Join(dir, "ingress.sock"), f.server, 1)
	if reply := strictHeader(t, partial, f.frames[1]); reply.Kind != "grant" {
		t.Fatal(reply)
	}
	if err := writeIngressBytes(partial, f.frames[1].Payload[:7]); err != nil {
		t.Fatal(err)
	}
	waitStrictReservations(t, f.server, 3)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("owner shutdown did not drain ingress body")
	}
	status, _ = f.server.StrictIngressStatus()
	if status.PendingTransactions != 0 || status.ReservedTransactions != 0 || status.RetainedEncodedBytes != 0 || status.ReservedEncodedBytes != 0 {
		t.Fatalf("owner endpoint shutdown retained queue %+v", status)
	}
	for _, name := range []string{"execution.sock", "ingress.sock"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("owner retained %s: %v", name, err)
		}
	}
}
func TestOwnerPrivateEndpointsPartialStartupPreservesExistingPath(t *testing.T) {
	f := newStrictIngressFixture(t, 6, 1<<20)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(dir, "ingress.sock")
	if err := os.WriteFile(blocked, []byte("preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.server.ServePrivateUnix(context.Background(), dir, true); err == nil {
		t.Fatal("existing ingress path overwritten")
	}
	if raw, err := os.ReadFile(blocked); err != nil || string(raw) != "preserved" {
		t.Fatal("foreign path changed")
	}
	if _, err := os.Lstat(filepath.Join(dir, "execution.sock")); !os.IsNotExist(err) {
		t.Fatal("partial startup leaked HTTP listener")
	}
}
func TestOwnerPrivateEndpointExitStopsOtherEndpoint(t *testing.T) {
	f, dir, _, done := ownerEndpointFixture(t)
	f.server.Close() // closes the strict endpoint through owner-lifetime context
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("unexpected endpoint exit was hidden")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("one endpoint exited while the other remained alive")
	}
	if _, err := os.Lstat(filepath.Join(dir, "execution.sock")); !os.IsNotExist(err) {
		t.Fatal("other listener remained after endpoint failure")
	}
}

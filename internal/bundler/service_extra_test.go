package bundler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
)

// fakeTxSubmitter is a controllable TxSubmitter for bundleLoop tests.
type fakeTxSubmitter struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakeTxSubmitter) SubmitBundleTx(entryPoint types.Address, calldata []byte, gasLimit uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.err
}

func (f *fakeTxSubmitter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestBundlerService_StartStop_CreatesBundle drives Start/Stop/bundleLoop and
// tryCreateBundle end to end: a pending op should be picked up by the loop
// and submitted through the TxSubmitter within a couple of ticks.
func TestBundlerService_StartStop_CreatesBundle(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BundleInterval = 5 * time.Millisecond
	svc := NewBundlerService(cfg, 1)

	submitter := &fakeTxSubmitter{}
	svc.SetTxSubmitter(submitter)

	sender := types.HexToAddress("0x01")
	if _, err := svc.SendUserOperation(makeTestOp(sender, 0), cfg.EntryPoints[0]); err != nil {
		t.Fatalf("SendUserOperation() error = %v", err)
	}

	svc.Start(context.Background())

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if submitter.callCount() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	svc.Stop()

	if submitter.callCount() == 0 {
		t.Fatal("expected bundleLoop to submit at least one bundle")
	}
}

// TestBundlerService_StartStop_NoSubmitter covers the branch in
// tryCreateBundle where no TxSubmitter is configured: ops are retained and
// no panic occurs.
func TestBundlerService_StartStop_NoSubmitter(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BundleInterval = 5 * time.Millisecond
	svc := NewBundlerService(cfg, 1)

	sender := types.HexToAddress("0x02")
	if _, err := svc.SendUserOperation(makeTestOp(sender, 0), cfg.EntryPoints[0]); err != nil {
		t.Fatalf("SendUserOperation() error = %v", err)
	}

	svc.Start(context.Background())
	time.Sleep(30 * time.Millisecond)
	svc.Stop()

	if svc.Pool().Count() == 0 {
		t.Fatal("expected op to be retained in the pool when no submitter is configured")
	}
}

// TestBundlerService_StartStop_SubmitterError covers the error-propagation
// branch in tryCreateBundle, which increments bundlesFailed and logs a
// warning but must not panic or stop the loop.
func TestBundlerService_StartStop_SubmitterError(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BundleInterval = 5 * time.Millisecond
	svc := NewBundlerService(cfg, 1)

	submitter := &fakeTxSubmitter{err: errors.New("submit failed")}
	svc.SetTxSubmitter(submitter)

	sender := types.HexToAddress("0x03")
	if _, err := svc.SendUserOperation(makeTestOp(sender, 0), cfg.EntryPoints[0]); err != nil {
		t.Fatalf("SendUserOperation() error = %v", err)
	}

	svc.Start(context.Background())

	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		if submitter.callCount() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	svc.Stop()

	if submitter.callCount() == 0 {
		t.Fatal("expected submitter to be invoked despite returning an error")
	}
}

// TestBundlerService_SendUserOperationWithState covers both the success and
// stateful-validation-failure paths.
func TestBundlerService_SendUserOperationWithState(t *testing.T) {
	cfg := DefaultConfig()
	svc := NewBundlerService(cfg, 1)

	sender := types.HexToAddress("0x04")
	op := makeTestOp(sender, 0)
	state := newFakeStateReader()
	state.nonce[sender] = 1
	state.balance[sender] = uint256.MustFromDecimal("1000000000000000000")

	hash, err := svc.SendUserOperationWithState(op, cfg.EntryPoints[0], state)
	if err != nil {
		t.Fatalf("SendUserOperationWithState() error = %v", err)
	}
	if hash == (types.Hash{}) {
		t.Fatal("expected non-zero hash")
	}

	// Stateful validation failure: insufficient balance.
	op2 := makeTestOp(types.HexToAddress("0x05"), 0)
	state2 := newFakeStateReader()
	state2.nonce[op2.Sender] = 1
	_, err = svc.SendUserOperationWithState(op2, cfg.EntryPoints[0], state2)
	if err == nil {
		t.Fatal("expected error for insufficient balance via SendUserOperationWithState")
	}
}

// TestBundlerService_Pool covers the Pool accessor.
func TestBundlerService_Pool(t *testing.T) {
	cfg := DefaultConfig()
	svc := NewBundlerService(cfg, 1)
	if svc.Pool() == nil {
		t.Fatal("Pool() = nil")
	}
}

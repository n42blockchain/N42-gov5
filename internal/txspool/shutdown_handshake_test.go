// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package txspool

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/transaction"
)

// TestScheduleLoopExitsWhenRequesterAbandonsHandshake pins the shutdown
// path: requestReset/requestPromoteExecutables abandon the reorgDoneCh
// handshake on ctx.Done, so scheduleLoop's reply send must have the same
// escape. Without it the loop blocks forever on the unbuffered send,
// pool.wg.Wait() in Stop() never returns and the node dies on the
// shutdown watchdog.
func TestScheduleLoopExitsWhenRequesterAbandonsHandshake(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pool := &TxsPool{
		ctx:            ctx,
		cancel:         cancel,
		reqResetCh:     make(chan *txspoolResetRequest),
		reqPromoteCh:   make(chan *accountSet),
		queueTxEventCh: make(chan *transaction.Transaction),
		reorgDoneCh:    make(chan chan struct{}),
	}

	var wg sync.WaitGroup
	wg.Add(1)
	pool.wg.Add(1)
	go func() {
		defer wg.Done()
		pool.scheduleLoop()
	}()

	// Deliver a request, then walk away without reading the reply —
	// exactly what a requester does when ctx fires mid-handshake.
	select {
	case pool.reqPromoteCh <- newAccountSet():
	case <-time.After(2 * time.Second):
		cancel()
		wg.Wait()
		t.Fatal("scheduleLoop never accepted the request")
	}

	cancel()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("scheduleLoop wedged on the reorgDoneCh reply after cancellation; " +
			"Stop() would hang on wg.Wait() and the node would die on the shutdown watchdog")
	}
}

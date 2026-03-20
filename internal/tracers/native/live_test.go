package native

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
)

func TestLiveTracerBasicEvents(t *testing.T) {
	eventCh := make(chan LiveTracerEvent, 100)
	tracer := NewLiveTracer(42, eventCh, 0)

	from := types.HexToAddress("0x1111111111111111111111111111111111111111")
	to := types.HexToAddress("0x2222222222222222222222222222222222222222")

	tracer.SetTxIndex(0)
	tracer.CaptureTxStart(21000)
	tracer.CaptureStart(nil, from, to, false, []byte{0x01, 0x02}, 21000, uint256.NewInt(1000))
	tracer.CaptureEnd(nil, 21000, nil)
	tracer.CaptureTxEnd(0)
	tracer.Close()

	if tracer.EventCount() != 4 {
		t.Fatalf("expected 4 events, got %d", tracer.EventCount())
	}

	evt1 := <-eventCh
	if evt1.Type != "txStart" || evt1.BlockNumber != 42 || evt1.TxIndex != 0 {
		t.Errorf("event 1: got type=%s block=%d tx=%d", evt1.Type, evt1.BlockNumber, evt1.TxIndex)
	}

	evt2 := <-eventCh
	if evt2.Type != "call" || *evt2.From != from || *evt2.To != to {
		t.Errorf("event 2: got type=%s from=%v to=%v", evt2.Type, evt2.From, evt2.To)
	}
}

func TestLiveTracerMaxEvents(t *testing.T) {
	eventCh := make(chan LiveTracerEvent, 100)
	tracer := NewLiveTracer(1, eventCh, 3)

	for i := 0; i < 10; i++ {
		tracer.CaptureTxStart(21000)
	}

	if tracer.EventCount() != 3 {
		t.Fatalf("expected 3 events (capped), got %d", tracer.EventCount())
	}
}

func TestLiveTracerClosed(t *testing.T) {
	eventCh := make(chan LiveTracerEvent, 100)
	tracer := NewLiveTracer(1, eventCh, 0)

	tracer.CaptureTxStart(21000)
	tracer.Close()
	tracer.CaptureTxStart(21000)

	if tracer.EventCount() != 1 {
		t.Fatalf("expected 1 event (closed), got %d", tracer.EventCount())
	}
}

func TestLiveTracerNonBlocking(t *testing.T) {
	eventCh := make(chan LiveTracerEvent, 1) // tiny buffer
	tracer := NewLiveTracer(1, eventCh, 0)

	// Fill the channel
	tracer.CaptureTxStart(21000)
	// This should NOT block even though channel is full
	tracer.CaptureTxStart(21000)
	tracer.CaptureTxStart(21000)

	// Should have attempted 3 but only 1 was received
	if len(eventCh) != 1 {
		t.Fatalf("expected 1 buffered event, got %d", len(eventCh))
	}
}

func TestLiveTracerCaptureStateNoOp(t *testing.T) {
	eventCh := make(chan LiveTracerEvent, 100)
	tracer := NewLiveTracer(1, eventCh, 0)

	// CaptureState should not emit events (too verbose)
	tracer.CaptureState(0, vm.ADD, 100, 3, nil, nil, 1, nil)
	tracer.CaptureState(1, vm.PUSH1, 97, 3, nil, nil, 1, nil)

	if tracer.EventCount() != 0 {
		t.Fatalf("CaptureState should not emit events, got %d", tracer.EventCount())
	}
}

func TestLiveTracerFaultEvent(t *testing.T) {
	eventCh := make(chan LiveTracerEvent, 100)
	tracer := NewLiveTracer(5, eventCh, 0)

	tracer.CaptureFault(100, vm.REVERT, 0, 0, nil, 2, vm.ErrOutOfGas)

	evt := <-eventCh
	if evt.Type != "fault" || evt.Depth != 2 || evt.Error != vm.ErrOutOfGas.Error() {
		t.Errorf("fault event: type=%s depth=%d err=%s", evt.Type, evt.Depth, evt.Error)
	}
}

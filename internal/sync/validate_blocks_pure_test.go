package sync

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/lib/rlp"
)

func TestCaptureArrivalTimeMetricAcceptsPastTime(t *testing.T) {
	headerTime := uint64(time.Now().Add(-5 * time.Second).Unix())
	if err := captureArrivalTimeMetric(headerTime); err != nil {
		t.Fatalf("captureArrivalTimeMetric: %v", err)
	}
}

func TestCaptureArrivalTimeMetricRejectsFutureTime(t *testing.T) {
	headerTime := uint64(time.Now().Add(1 * time.Hour).Unix())
	if err := captureArrivalTimeMetric(headerTime); err == nil {
		t.Fatal("expected error for a future block time")
	}
}

func TestPeekBlockHeaderDecodesHeaderOnly(t *testing.T) {
	blk := syncTSmallBlock(9)
	raw, err := rlp.EncodeToBytes(blk)
	if err != nil {
		t.Fatalf("rlp encode: %v", err)
	}
	h, err := peekBlockHeader(raw)
	if err != nil {
		t.Fatalf("peekBlockHeader: %v", err)
	}
	if h.Number64().Uint64() != 9 {
		t.Fatalf("peeked header number = %d, want 9", h.Number64().Uint64())
	}
}

func TestPeekBlockHeaderRejectsGarbage(t *testing.T) {
	if _, err := peekBlockHeader([]byte{0xFF, 0xFF, 0xFF}); err == nil {
		t.Fatal("expected error decoding garbage bytes as a header")
	}
}

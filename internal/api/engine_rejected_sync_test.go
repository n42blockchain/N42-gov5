package api

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestMarkRejectedPayloadHashSharesEngineOverlay(t *testing.T) {
	core := NewAPI(nil, nil, nil, nil, nil, nil)
	rejected := types.HexToHash("0x11")
	latest := types.HexToHash("0x22")

	core.MarkRejectedPayloadHash(rejected, &latest)
	got, ok := core.engineOverlay.rejectedLatestValidHash(rejected)
	if !ok || got == nil || *got != latest {
		t.Fatalf("rejected payload lookup = (%v, %v), want (%s, true)", got, ok, latest)
	}
}

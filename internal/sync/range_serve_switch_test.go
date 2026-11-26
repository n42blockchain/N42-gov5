package sync

import (
	"testing"

	"github.com/n42blockchain/N42/internal/sync/syncwire"
)

func TestRangeServingSwitch(t *testing.T) {
	t.Setenv("N42_SYNC_RANGE_SERVE", "")
	if !rangeServingEnabled() {
		t.Fatal("range serving must be on by default")
	}
	t.Setenv("N42_SYNC_RANGE_SERVE", "off")
	if rangeServingEnabled() {
		t.Fatal("N42_SYNC_RANGE_SERVE=off must disable range serving")
	}
	for _, p := range []string{syncwire.ProtocolBodiesByRangeV2, syncwire.ProtocolCommitQC} {
		if !rangeServingProtocol(p) {
			t.Fatalf("%s must be switched off", p)
		}
	}
	for _, p := range []string{syncwire.ProtocolQMDBStatus, syncwire.ProtocolQMDBManifest, syncwire.ProtocolQMDBTwigs, syncwire.ProtocolCodeByHash} {
		if rangeServingProtocol(p) {
			t.Fatalf("%s must stay served", p)
		}
	}
}

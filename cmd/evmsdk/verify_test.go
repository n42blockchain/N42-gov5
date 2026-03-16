package evmsdk

import (
	"testing"

	"github.com/holiman/uint256"
)

func TestRequireBlockNumberRejectsNil(t *testing.T) {
	if _, err := requireBlockNumber(nil); err == nil || err.Error() != "block number unavailable" {
		t.Fatalf("requireBlockNumber(nil) error = %v", err)
	}
}

func TestRequireBlockNumberAcceptsValue(t *testing.T) {
	got, err := requireBlockNumber(uint256.NewInt(7))
	if err != nil {
		t.Fatalf("requireBlockNumber(7) error = %v", err)
	}
	if got != 7 {
		t.Fatalf("requireBlockNumber(7) = %d, want 7", got)
	}
}

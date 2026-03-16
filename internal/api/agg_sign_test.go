package api

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/block"
)

func TestSignMergeRejectsMissingHeaderNumber(t *testing.T) {
	_, _, err := SignMerge(context.Background(), &block.Header{}, 0)
	if err == nil || err.Error() != "header number unavailable" {
		t.Fatalf("SignMerge() error = %v", err)
	}
}

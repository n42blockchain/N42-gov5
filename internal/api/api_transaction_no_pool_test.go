package api

import (
	"context"
	"testing"
)

func TestSubmitTransactionRejectsMissingPool(t *testing.T) {
	_, err := SubmitTransaction(context.Background(), &API{}, nil)
	if err == nil || err.Error() != "transaction pool unavailable" {
		t.Fatalf("SubmitTransaction error = %v, want transaction pool unavailable", err)
	}
}

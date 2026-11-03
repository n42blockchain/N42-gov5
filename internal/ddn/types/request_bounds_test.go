package types

import (
	"math"
	"strings"
	"testing"

	chain "github.com/n42blockchain/N42/common/types"
)

func TestMaxCostBounds(t *testing.T) {
	for _, cost := range []string{"", "00", "-1", "+1", strings.Repeat("9", 1<<20), "115792089237316195423570985008687907853269984665640564039457584007913129639936"} {
		if _, err := ParseMaxCost(cost); err == nil {
			t.Fatal("invalid cost accepted")
		}
	}
	if _, err := ParseMaxCost("115792089237316195423570985008687907853269984665640564039457584007913129639935"); err != nil {
		t.Fatal(err)
	}
}
func TestRequestDeadlineDoesNotWrap(t *testing.T) {
	r := DecisionRequest{Version: 1, Task: "t", SchemaID: "s", Requester: "r", InputHash: chain.Hash{1}, Quorum: 1, MaxLatencyMs: 1, MaxCost: "0", PrivacyMode: "public", Deadline: math.MaxUint64}
	if err := r.Validate(math.MaxUint64 - 1); err != nil {
		t.Fatalf("bounded future deadline near uint64 limit: %v", err)
	}
}

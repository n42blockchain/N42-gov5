package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDepthCreditDoesNotAccumulateWhileRPCIsSlow(t *testing.T) {
	credit := make(chan struct{}, 1000)
	for i := 0; i < 100; i++ {
		refillDepthPermits(credit, 1000, 0, 200, 0, 0)
	}
	if len(credit) != 5 {
		t.Fatalf("100 polls allocated %d frames, want only 5", len(credit))
	}
	// Two workers hold their frames while the node has not reported them yet.
	<-credit
	<-credit
	for i := 0; i < 100; i++ {
		refillDepthPermits(credit, 1000, 0, 200, 2, 0)
	}
	if len(credit) != 3 {
		t.Fatalf("in-flight work was credited twice: queued %d", len(credit))
	}
	// Gossip/other callers filled the pool. Outstanding unsent work is revoked.
	refillDepthPermits(credit, 1000, 1200, 200, 2, 0)
	if len(credit) != 0 {
		t.Fatal("saturated pool retained stale submission credit")
	}
}

func TestDepthCreditRateAndPartialBatches(t *testing.T) {
	credit := make(chan struct{}, 10)
	refillDepthPermits(credit, 1001, 0, 200, 0, 400)
	if len(credit) != 2 {
		t.Fatal("rate ceiling not applied to new credit")
	}
	refillDepthPermits(credit, 1001, 600, 200, 1, 0)
	if len(credit) != 2 { // ceil(401/200) - one outstanding frame
		t.Fatal("partial batch accounting lost or duplicated credit")
	}
	refillDepthPermits(credit, 0, 0, 200, 1, 0)
	if len(credit) != 0 {
		t.Fatal("failed depth probe could not revoke credit")
	}
}

func TestPoolDepthRejectsIncompleteFleetSnapshot(t *testing.T) {
	for _, invalid := range []string{
		`{"result":{}}`, `{"result":{"pending":"0x"}}`,
		`{"result":{"pending":"0xffffffffffffffff"}}`,
		`{"error":{"message":"namespace disabled"}}`, `not json`,
	} {
		t.Run(invalid, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/good" {
					fmt.Fprint(w, `{"result":{"pending":"0xa","queued":"0xffff"}}`)
				} else {
					fmt.Fprint(w, invalid)
				}
			}))
			defer server.Close()
			if _, err := poolDepth([]string{server.URL + "/good", server.URL + "/bad"}); err == nil {
				t.Fatal("incomplete fleet snapshot treated as known pool depth")
			}
			if got, err := poolDepth([]string{server.URL + "/good"}); err != nil || got != 10 {
				t.Fatalf("pending depth = %d, err %v; queued must not count as executable", got, err)
			}
		})
	}
}

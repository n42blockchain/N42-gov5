package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestIsPoolBackpressure(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"txpool: above high water", true},
		{"txpool is full", true},
		{"some prefix: txpool is full, try later", true},
		{"transaction underpriced", false},
		{"nonce too low", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isPoolBackpressure(c.msg); got != c.want {
			t.Errorf("isPoolBackpressure(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}

// TestSubmitWithBackoffRetriesSameCall: on a backpressure error, the SAME
// submit closure (i.e. the same nonce/batch) is called again -- not skipped
// -- until it succeeds. A fake RPC counts calls and fails the first N.
func TestSubmitWithBackoffRetriesSameCall(t *testing.T) {
	calls := 0
	failUntil := 3
	submit := func() (json.RawMessage, error) {
		calls++
		if calls <= failUntil {
			return nil, fmt.Errorf("txpool: above high water")
		}
		return json.RawMessage(`"0x1"`), nil
	}

	start := time.Now()
	ok, err, retries := submitWithBackoff(submit)
	elapsed := time.Since(start)

	if !ok || err != nil {
		t.Fatalf("submitWithBackoff() = (%v, %v), want (true, nil)", ok, err)
	}
	if retries != int64(failUntil) {
		t.Fatalf("retries = %d, want %d", retries, failUntil)
	}
	if calls != failUntil+1 {
		t.Fatalf("submit called %d times, want %d (same call retried, not skipped)", calls, failUntil+1)
	}
	// 20ms + 40ms + 80ms backoff (doubling, capped at 100ms) = 140ms minimum.
	if elapsed < 140*time.Millisecond {
		t.Fatalf("elapsed %s, want at least 140ms of backoff", elapsed)
	}
}

// TestSubmitWithBackoffStopsOnOtherError: a non-backpressure error (bad
// nonce, underpriced, malformed tx) is returned immediately, unretried --
// today's existing handling for it is unchanged.
func TestSubmitWithBackoffStopsOnOtherError(t *testing.T) {
	calls := 0
	submit := func() (json.RawMessage, error) {
		calls++
		return nil, fmt.Errorf("nonce too low")
	}

	ok, err, retries := submitWithBackoff(submit)
	if ok {
		t.Fatal("submitWithBackoff() ok = true, want false for a non-backpressure error")
	}
	if err == nil || err.Error() != "nonce too low" {
		t.Fatalf("err = %v, want \"nonce too low\"", err)
	}
	if retries != 0 {
		t.Fatalf("retries = %d, want 0 (must not retry a non-backpressure error)", retries)
	}
	if calls != 1 {
		t.Fatalf("submit called %d times, want 1", calls)
	}
}

// TestSubmitWithBackoffNoRetryOnFirstSuccess: the success path costs no
// backoff sleep and reports zero retries.
func TestSubmitWithBackoffNoRetryOnFirstSuccess(t *testing.T) {
	submit := func() (json.RawMessage, error) { return json.RawMessage(`"0x1"`), nil }

	start := time.Now()
	ok, err, retries := submitWithBackoff(submit)
	elapsed := time.Since(start)

	if !ok || err != nil || retries != 0 {
		t.Fatalf("submitWithBackoff() = (%v, %v, %d), want (true, nil, 0)", ok, err, retries)
	}
	if elapsed > 5*time.Millisecond {
		t.Fatalf("elapsed %s, want near-instant with no backoff", elapsed)
	}
}

// TestSubmitWithBackoffCapsAtMaxBackoff: many consecutive backpressure
// errors must not let the sleep grow past the 100ms ceiling.
func TestSubmitWithBackoffCapsAtMaxBackoff(t *testing.T) {
	calls := 0
	failUntil := 6 // backoff would be 20,40,80,100,100,100ms uncapped-doubling would blow past 100ms by the 3rd retry
	submit := func() (json.RawMessage, error) {
		calls++
		if calls <= failUntil {
			return nil, fmt.Errorf("txpool is full")
		}
		return json.RawMessage(`"0x1"`), nil
	}

	start := time.Now()
	ok, _, retries := submitWithBackoff(submit)
	elapsed := time.Since(start)

	if !ok || retries != int64(failUntil) {
		t.Fatalf("ok=%v retries=%d, want ok=true retries=%d", ok, retries, failUntil)
	}
	// Uncapped doubling from 20ms would be 20+40+80+160+320+640=1260ms.
	// Capped at 100ms after the 3rd retry: 20+40+80+100+100+100=440ms.
	if elapsed >= 700*time.Millisecond {
		t.Fatalf("elapsed %s, backoff does not appear to be capped at 100ms", elapsed)
	}
}

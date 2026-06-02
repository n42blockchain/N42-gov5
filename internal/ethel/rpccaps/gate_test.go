package rpccaps

import (
	"errors"
	"testing"
)

func TestGateRejectsUnsupported(t *testing.T) {
	g := Gate{Mode: M1}
	// M1 serves latest state but not historical, not tx-by-hash, not logs.
	if err := g.Check("eth_getBalance", Latest); err != nil {
		t.Errorf("M1 getBalance latest should pass: %v", err)
	}
	for _, c := range []struct {
		m string
		s Scope
	}{
		{"eth_getBalance", Historical},
		{"eth_getTransactionByHash", Latest},
		{"eth_getLogs", Latest},
		{"eth_sendRawTransaction", Latest},
	} {
		err := g.Check(c.m, c.s)
		if err == nil {
			t.Errorf("M1 %s should be gated", c.m)
			continue
		}
		var e *ErrNotInMode
		if !errors.As(err, &e) {
			t.Errorf("want *ErrNotInMode for %s, got %T", c.m, err)
		}
	}
}

func TestGateSlowPolicy(t *testing.T) {
	// M1 receipt is Slow (recompute). Default: rejected. AllowSlow: accepted.
	if err := (Gate{Mode: M1}).Check("eth_getTransactionReceipt", Latest); err == nil {
		t.Error("M1 receipt should be rejected by default (Slow)")
	}
	if err := (Gate{Mode: M1, AllowSlow: true}).Check("eth_getTransactionReceipt", Latest); err != nil {
		t.Errorf("M1 receipt with AllowSlow should pass: %v", err)
	}
}

func TestGateFullAdvertised(t *testing.T) {
	adv := Gate{Mode: Full}.Advertised()
	set := map[string]bool{}
	for _, m := range adv {
		set[m] = true
	}
	// Full advertises latest-state + tx + receipts/logs.
	for _, m := range []string{"eth_getBalance", "eth_call", "eth_getTransactionByHash",
		"eth_getBlockByNumber", "eth_getTransactionReceipt", "eth_getLogs", "eth_sendRawTransaction"} {
		if !set[m] {
			t.Errorf("Full should advertise %s; got %v", m, adv)
		}
	}
	// sorted + non-empty
	for i := 1; i < len(adv); i++ {
		if adv[i] < adv[i-1] {
			t.Errorf("Advertised not sorted at %d: %v", i, adv)
		}
	}
}

func TestGateModeMonotone(t *testing.T) {
	// Archive advertises a superset of Full at Latest (it keeps strictly more data).
	full := map[string]bool{}
	for _, m := range (Gate{Mode: Full}).Advertised() {
		full[m] = true
	}
	for m := range full {
		if (Gate{Mode: Archive}).Check(m, Latest) != nil {
			t.Errorf("Archive should serve everything Full serves; missing %s", m)
		}
	}
}

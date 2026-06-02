package rpccaps

import "testing"

// TestFullProfile asserts the exact "full" profile the spec requires:
// sync-to-tip + consensus + P2P (data classes) and the eth_ surface
// latest-state + post-merge tx/block + latest receipts/logs.
func TestFullProfile(t *testing.T) {
	cases := []struct {
		method string
		scope  Scope
		want   Support
	}{
		{"eth_getBalance", Latest, Yes},
		{"eth_call", Latest, Yes},
		{"eth_getTransactionByHash", Latest, Yes}, // post-merge tx index
		{"eth_getBlockByNumber", Latest, Yes},     // post-merge txs
		{"eth_getTransactionReceipt", Latest, Yes},
		{"eth_getLogs", Latest, Yes},
		{"eth_getBlockReceipts", Latest, Yes},
		// Full deliberately prunes history → historical state RPC is NOT served.
		{"eth_getBalance", Historical, No},
		{"eth_call", Historical, No},
		{"eth_getProof", Historical, No},
		{"eth_getLogs", Historical, No},
		{"eth_getTransactionReceipt", Historical, No},
	}
	for _, c := range cases {
		if got := Serviceable(Full, c.method, c.scope); got != c.want {
			t.Errorf("Full %s @%v = %v, want %v", c.method, c.scope, got, c.want)
		}
	}
}

func TestM1(t *testing.T) {
	// M1 = snapshot + hot ⇒ full CURRENT state, no history, no stored receipts.
	yes := []string{"eth_getBalance", "eth_getCode", "eth_getStorageAt", "eth_getTransactionCount", "eth_getProof", "eth_call", "eth_estimateGas"}
	for _, m := range yes {
		if got := Serviceable(M1, m, Latest); got != Yes {
			t.Errorf("M1 %s @latest = %v, want Yes", m, got)
		}
		if got := Serviceable(M1, m, Historical); got != No {
			t.Errorf("M1 %s @historical = %v, want No", m, got)
		}
	}
	if got := Serviceable(M1, "eth_getTransactionReceipt", Latest); got != Slow {
		t.Errorf("M1 receipt = %v, want Slow (recompute)", got)
	}
	if got := Serviceable(M1, "eth_getLogs", Latest); got != No {
		t.Errorf("M1 getLogs = %v, want No", got)
	}
	if got := Serviceable(M1, "eth_getTransactionByHash", Latest); got != No {
		t.Errorf("M1 txByHash = %v, want No (no global index)", got)
	}
}

func TestM0(t *testing.T) {
	// M0 = witness-direct ⇒ recent-changes only.
	if got := Serviceable(M0, "eth_getBalance", Latest); got != Window {
		t.Errorf("M0 getBalance @latest = %v, want Window", got)
	}
	if got := Serviceable(M0, "eth_getBalance", Historical); got != No {
		t.Errorf("M0 getBalance @historical = %v, want No", got)
	}
	if got := Serviceable(M0, "eth_getTransactionReceipt", Latest); got != Window {
		t.Errorf("M0 receipt = %v, want Window", got)
	}
	if got := Serviceable(M0, "eth_getLogs", Latest); got != Window {
		t.Errorf("M0 getLogs = %v, want Window", got)
	}
	if got := Serviceable(M0, "eth_getTransactionByHash", Latest); got != No {
		t.Errorf("M0 txByHash = %v, want No", got)
	}
	if got := Serviceable(M0, "eth_sendRawTransaction", Latest); got != No {
		t.Errorf("M0 sendRawTransaction = %v, want No (forward to producer)", got)
	}
}

func TestArchiveServesEverything(t *testing.T) {
	for _, m := range Methods() {
		if Serviceable(Archive, m, Latest) == No {
			t.Errorf("Archive should serve %s @latest", m)
		}
	}
	// Archive is the only mode with historical state/proof/logs.
	for _, m := range []string{"eth_getBalance", "eth_getProof", "eth_call", "eth_getLogs", "eth_getTransactionReceipt"} {
		if Serviceable(Archive, m, Historical) != Yes {
			t.Errorf("Archive %s @historical should be Yes", m)
		}
	}
}

// TestHistoricalIsArchiveOnly: no non-archive mode serves any historical
// state/proof/EVM/logs/receipt query.
func TestHistoricalIsArchiveOnly(t *testing.T) {
	histMethods := []string{"eth_getBalance", "eth_getProof", "eth_call", "eth_getLogs", "eth_getTransactionReceipt"}
	for _, m := range []Mode{M0, M1, Full} {
		for _, meth := range histMethods {
			if got := Serviceable(m, meth, Historical); got != No {
				t.Errorf("%v %s @historical = %v, want No (archive-only)", m, meth, got)
			}
		}
	}
}

// TestFullTrimming: the data-trimming spec for the Full profile — what it keeps
// and what it may prune.
func TestFullTrimming(t *testing.T) {
	keep := map[DataClass]bool{}
	for _, d := range RequiredData(Full) {
		keep[d] = true
	}
	mustKeep := []DataClass{Headers, PostMergeBodies, TxHashIndex, LatestState, RecentReceipts, LogIndex, Consensus, P2P, Mempool}
	for _, d := range mustKeep {
		if !keep[d] {
			t.Errorf("Full must keep %v", d)
		}
	}
	mustPrune := []DataClass{HistoricalState, AllBodies, AllReceipts, RollingChangeset}
	prunable := map[DataClass]bool{}
	for _, d := range Prunable(Full) {
		prunable[d] = true
	}
	for _, d := range mustPrune {
		if !prunable[d] {
			t.Errorf("Full should be able to prune %v", d)
		}
		if keep[d] {
			t.Errorf("Full must NOT keep %v (trim it)", d)
		}
	}
}

// TestModeDataConsistency: every mode keeps Headers (① chain), and only Archive
// keeps HistoricalState/AllReceipts.
func TestModeDataConsistency(t *testing.T) {
	for _, m := range []Mode{M0, M1, Full, Archive} {
		has := map[DataClass]bool{}
		for _, d := range RequiredData(m) {
			has[d] = true
		}
		if !has[Headers] {
			t.Errorf("%v must keep Headers", m)
		}
		if m != Archive && (has[HistoricalState] || has[AllReceipts] || has[AllBodies]) {
			t.Errorf("%v must not keep archive-only data", m)
		}
	}
}

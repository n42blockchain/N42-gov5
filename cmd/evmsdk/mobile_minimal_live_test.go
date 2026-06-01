package evmsdk

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestMobileMinimalLive drives the mobile minimal facade against a RUNNING
// n42-stateless-serve (real data). Gated on env so it never runs in CI without a
// server:
//
//	N42_LIVE_URL=http://127.0.0.1:8555 \
//	N42_LIVE_CHECKPOINT=990000 \
//	N42_LIVE_CHECKPOINT_HASH=0x31568223eb0a... \
//	N42_LIVE_SYNC_TO=1000000 \
//	N42_LIVE_ACCOUNT=0xdAC17F958D2ee523a2206206994597C13D831ec7 \
//	go test -tags nosqlite,noboltdb ./cmd/evmsdk/ -run TestMobileMinimalLive -v
func TestMobileMinimalLive(t *testing.T) {
	url := os.Getenv("N42_LIVE_URL")
	if url == "" {
		t.Skip("set N42_LIVE_URL to run the live mobile-facade smoke")
	}
	cp := envU64(t, "N42_LIVE_CHECKPOINT")
	cpHash := os.Getenv("N42_LIVE_CHECKPOINT_HASH")
	syncTo := int64(envU64(t, "N42_LIVE_SYNC_TO"))
	acct := os.Getenv("N42_LIVE_ACCOUNT")

	if e := MobileMinimalInit(url, int64(cp), cpHash, 7200); e != "" {
		t.Fatalf("Init: %s", e)
	}
	defer MobileMinimalFree()

	syncJSON := MobileMinimalSyncTo(syncTo)
	if strings.HasPrefix(syncJSON, "error:") {
		t.Fatalf("SyncTo: %s", syncJSON)
	}
	t.Logf("① header-chain synced: %s", syncJSON)
	var st struct {
		Head uint64 `json:"head"`
	}
	_ = json.Unmarshal([]byte(syncJSON), &st)
	if st.Head < uint64(syncTo) {
		t.Fatalf("head %d < syncTo %d", st.Head, syncTo)
	}

	if vb := os.Getenv("N42_LIVE_VERIFY_BLOCK"); vb != "" {
		start := atoi(vb)
		count := int64(1)
		if c := os.Getenv("N42_LIVE_VERIFY_COUNT"); c != "" { count = int64(atoi(c)) }
		totalCodeFetched := 0
		contractBlocks := 0
		for bn := start; bn < start+count; bn++ {
			j := MobileVerifyBlock(bn)
			if strings.HasPrefix(j, "error:") { t.Fatalf("VerifyBlock %d: %s", bn, j) }
			var v struct {
				Verified    bool `json:"verified"`
				Error       string `json:"error"`
				TxCount     int  `json:"txCount"`
				CodeFetched int  `json:"codeFetched"`
			}
			_ = json.Unmarshal([]byte(j), &v)
			if !v.Verified { t.Fatalf("② block %d not verified: %s", bn, v.Error) }
			totalCodeFetched += v.CodeFetched
			if v.CodeFetched > 0 {
				contractBlocks++
				t.Logf("② block %d: ✓ %d tx, %d code fetched (CONTRACT block)", bn, v.TxCount, v.CodeFetched)
			}
		}
		t.Logf("② span [%d,%d): all verified; %d contract blocks, %d total bytecodes fetched via /code-by-addr",
			start, start+count, contractBlocks, totalCodeFetched)
	}

	if acct != "" {
		balJSON := MobileBalanceOf(acct)
		if strings.HasPrefix(balJSON, "error:") {
			t.Fatalf("BalanceOf: %s", balJSON)
		}
		t.Logf("③ trustless balance: %s", balJSON)
		var bal struct {
			Verified   bool `json:"verified"`
			ProofBytes int  `json:"proofBytes"`
		}
		if err := json.Unmarshal([]byte(balJSON), &bal); err != nil {
			t.Fatalf("decode balance: %v", err)
		}
		if !bal.Verified {
			t.Fatal("balance not verified")
		}
		if bal.ProofBytes == 0 || bal.ProofBytes > 50_000 {
			t.Fatalf("proof bytes %d not bounded (~KB expected)", bal.ProofBytes)
		}
	}
}

func atoi(s string) int64 {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

func envU64(t *testing.T, k string) uint64 {
	t.Helper()
	v := os.Getenv(k)
	if v == "" {
		t.Skipf("set %s to run the live smoke", k)
	}
	var n uint64
	for _, c := range v {
		if c < '0' || c > '9' {
			t.Fatalf("%s not numeric: %q", k, v)
		}
		n = n*10 + uint64(c-'0')
	}
	return n
}

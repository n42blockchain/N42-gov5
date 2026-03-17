package tests

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"
)

func TestSupportedForksContainsExpectedForks(t *testing.T) {
	t.Parallel()

	forks := SupportedForks()
	if len(forks) == 0 {
		t.Fatal("SupportedForks returned no forks")
	}

	seen := make(map[string]struct{}, len(forks))
	for _, fork := range forks {
		if _, ok := seen[fork]; ok {
			t.Fatalf("duplicate fork %q", fork)
		}
		seen[fork] = struct{}{}
	}

	for _, fork := range []string{"Berlin", "London", "Shanghai", "Cancun", "Prague"} {
		if _, ok := seen[fork]; !ok {
			t.Fatalf("expected fork %q in SupportedForks", fork)
		}
	}
}

func TestIsSupportedFork(t *testing.T) {
	t.Parallel()

	if !IsSupportedFork("Cancun") {
		t.Fatal("expected Cancun to be supported")
	}
	if IsSupportedFork("Osaka") {
		t.Fatal("did not expect Osaka to be reported as supported")
	}
}

func TestGetChainConfigActivatesExpectedForkFields(t *testing.T) {
	t.Parallel()

	cancun := GetChainConfig("Cancun")
	if cancun.CancunBlock == nil || cancun.CancunBlock.Sign() != 0 {
		t.Fatalf("expected CancunBlock=0, got %v", cancun.CancunBlock)
	}
	if cancun.ShanghaiBlock == nil || cancun.ShanghaiBlock.Sign() != 0 {
		t.Fatalf("expected ShanghaiBlock=0 for Cancun config, got %v", cancun.ShanghaiBlock)
	}

	prague := GetChainConfig("Prague")
	if prague.PragueTime == nil || prague.PragueTime.Sign() != 0 {
		t.Fatalf("expected PragueTime=0, got %v", prague.PragueTime)
	}
	if prague.CancunBlock == nil || prague.CancunBlock.Sign() != 0 {
		t.Fatalf("expected CancunBlock=0 for Prague config, got %v", prague.CancunBlock)
	}
}

func TestParseHexOrDecimal(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		input   string
		want    uint64
		wantErr bool
	}{
		{name: "empty", input: "", want: 0},
		{name: "decimal", input: "42", want: 42},
		{name: "hex", input: "0x2a", want: 42},
		{name: "uppercase hex", input: "0X2A", want: 42},
		{name: "invalid", input: "not-a-number", wantErr: true},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseHexOrDecimal(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseHexOrDecimal(%q) returned error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("ParseHexOrDecimal(%q)=%d, want %d", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseBigInt(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		input   string
		want    *big.Int
		wantErr bool
	}{
		{name: "empty", input: "", want: big.NewInt(0)},
		{name: "zero hex", input: "0x", want: big.NewInt(0)},
		{name: "decimal", input: "12345", want: big.NewInt(12345)},
		{name: "hex", input: "0xff", want: big.NewInt(255)},
		{name: "invalid", input: "xyz", wantErr: true},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseBigInt(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseBigInt(%q) returned error: %v", tc.input, err)
			}
			if got.Cmp(tc.want) != 0 {
				t.Fatalf("ParseBigInt(%q)=%s, want %s", tc.input, got, tc.want)
			}
		})
	}
}

func TestLoadTest(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "state_test.json")

	payload := map[string]StateTest{
		"sample": {
			Env: stEnv{
				CurrentCoinbase:   "0x0000000000000000000000000000000000000000",
				CurrentDifficulty: "0x1",
				CurrentGasLimit:   "0x5208",
				CurrentNumber:     "0x1",
				CurrentTimestamp:  "0x2",
			},
			Pre: map[string]stAccount{
				"0x0000000000000000000000000000000000000001": {
					Balance: "0x0",
					Code:    "0x",
					Nonce:   "0x0",
					Storage: map[string]string{},
				},
			},
			Transaction: stTransaction{
				Data:      []string{"0x"},
				GasLimit:  []string{"0x5208"},
				Nonce:     "0x0",
				SecretKey: "0x01",
				To:        "0x0000000000000000000000000000000000000002",
				Value:     []string{"0x0"},
			},
		},
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("failed to write test fixture: %v", err)
	}

	loaded, err := LoadTest(path)
	if err != nil {
		t.Fatalf("LoadTest returned error: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 test, got %d", len(loaded))
	}
	if _, ok := loaded["sample"]; !ok {
		t.Fatal("expected sample test in loaded fixture")
	}
}

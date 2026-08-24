package main

import (
	"math/big"
	"testing"
)

func TestFundingAmountsIncludeFaucetTransferGas(t *testing.T) {
	fundVal, total := fundingAmounts(3000, 3000, 10_000_000_000)
	if got, want := fundVal.ToBig(), bigFromDecimal(t, "632100000000000000"); got.Cmp(want) != 0 {
		t.Fatalf("fund value = %s, want %s", got, want)
	}
	if want := bigFromDecimal(t, "1896930000000000000000"); total.Cmp(want) != 0 {
		t.Fatalf("total funding cost = %s, want %s", total, want)
	}
}

func TestHexToBigDoesNotTruncateBalance(t *testing.T) {
	got, err := hexToBig("0xcba8dcad23af059a4b42")
	if err != nil {
		t.Fatal(err)
	}
	want := bigFromHex(t, "cba8dcad23af059a4b42")
	if got.Cmp(want) != 0 {
		t.Fatalf("balance = %s, want %s", got, want)
	}
}

func bigFromDecimal(t *testing.T, s string) *big.Int {
	t.Helper()
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("invalid decimal fixture %q", s)
	}
	return n
}

func bigFromHex(t *testing.T, s string) *big.Int {
	t.Helper()
	n, ok := new(big.Int).SetString(s, 16)
	if !ok {
		t.Fatalf("invalid hex fixture %q", s)
	}
	return n
}

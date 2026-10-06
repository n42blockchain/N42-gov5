package filters

import (
	"encoding/json"
	"testing"
)

// g41FilterQueryCase groups a JSON payload with its expected outcome for
// FilterCriteria.UnmarshalJSON.
type g41FilterQueryCase struct {
	name    string
	payload string
	wantErr bool
}

func TestFilterCriteriaUnmarshalJSON(t *testing.T) {
	cases := []g41FilterQueryCase{
		{"empty object", `{}`, false},
		{"from and to block", `{"fromBlock":"0x1","toBlock":"0x10"}`, false},
		{
			"block hash with from block conflicts",
			`{"blockHash":"0x1111111111111111111111111111111111111111111111111111111111111111","fromBlock":"0x1"}`,
			true,
		},
		{
			"block hash only",
			`{"blockHash":"0x1111111111111111111111111111111111111111111111111111111111111111"}`,
			false,
		},
		{
			"single address string",
			`{"address":"0x1111111111111111111111111111111111111111"}`,
			false,
		},
		{
			"address array",
			`{"address":["0x1111111111111111111111111111111111111111","0x2222222222222222222222222222222222222222"]}`,
			false,
		},
		{
			"non string address in array",
			`{"address":[1234]}`,
			true,
		},
		{
			"invalid address string",
			`{"address":"not-hex"}`,
			true,
		},
		{
			"invalid addresses type",
			`{"address":1234}`,
			true,
		},
		{
			"topics single string",
			`{"topics":["0x1111111111111111111111111111111111111111111111111111111111111111"]}`,
			false,
		},
		{
			"topics null entry",
			`{"topics":[null]}`,
			false,
		},
		{
			"topics or list",
			`{"topics":[["0x1111111111111111111111111111111111111111111111111111111111111111","0x2222222222222222222222222222222222222222222222222222222222222222"]]}`,
			false,
		},
		{
			"topics or list with null",
			`{"topics":[[null,"0x1111111111111111111111111111111111111111111111111111111111111111"]]}`,
			false,
		},
		{
			"topics or list with non string",
			`{"topics":[[1234]]}`,
			true,
		},
		{
			"topics invalid string",
			`{"topics":["not-hex"]}`,
			true,
		},
		{
			"topics invalid shape",
			`{"topics":[1234]}`,
			true,
		},
		{
			"invalid json",
			`{`,
			true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var crit FilterCriteria
			err := json.Unmarshal([]byte(c.payload), &crit)
			if c.wantErr && err == nil {
				t.Fatalf("expected error for payload %s, got nil", c.payload)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("unexpected error for payload %s: %v", c.payload, err)
			}
		})
	}
}

func TestDecodeAddress(t *testing.T) {
	if _, err := decodeAddress("0x1111111111111111111111111111111111111111"); err != nil {
		t.Fatalf("decodeAddress valid: %v", err)
	}
	if _, err := decodeAddress("0x1234"); err == nil {
		t.Fatal("decodeAddress expected length error")
	}
	if _, err := decodeAddress("zz"); err == nil {
		t.Fatal("decodeAddress expected decode error")
	}
}

func TestDecodeTopic(t *testing.T) {
	if _, err := decodeTopic("0x1111111111111111111111111111111111111111111111111111111111111111"); err != nil {
		t.Fatalf("decodeTopic valid: %v", err)
	}
	if _, err := decodeTopic("0x1234"); err == nil {
		t.Fatal("decodeTopic expected length error")
	}
	if _, err := decodeTopic("zz"); err == nil {
		t.Fatal("decodeTopic expected decode error")
	}
}

package qmdb

import (
	"bytes"
	"testing"
)

func TestMarshalFinalizedRangeDeterministicAndBounded(t *testing.T) {
	a := FinalizedRangeEntry{Number: 7, BlockHash: Hash{7}, ParentHash: Hash{6}, HeaderRLP: []byte{1}, BlockRLP: []byte{2}}
	b := FinalizedRangeEntry{Number: 8, BlockHash: Hash{8}, ParentHash: a.BlockHash, HeaderRLP: []byte{3}, BlockRLP: []byte{4}, Receipts: []byte{5}}
	r := &FinalizedRange{ChainID: 1143, GenesisHash: Hash{0x11}, FromBlock: 7, ToBlock: 8, Entries: []FinalizedRangeEntry{a, b}}
	one, err := MarshalFinalizedRange(r)
	if err != nil {
		t.Fatal(err)
	}
	two, err := MarshalFinalizedRange(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(one, two) || len(one) < 32 || string(one[:8]) != "N42FRNG\x01" {
		t.Fatal("finalized range encoding is not deterministic v1")
	}
}

func TestMarshalFinalizedRangeRejectsBrokenLineage(t *testing.T) {
	r := &FinalizedRange{FromBlock: 1, ToBlock: 2, Entries: []FinalizedRangeEntry{
		{Number: 1, BlockHash: Hash{1}, HeaderRLP: []byte{1}, BlockRLP: []byte{1}},
		{Number: 2, BlockHash: Hash{2}, ParentHash: Hash{9}, HeaderRLP: []byte{1}, BlockRLP: []byte{1}},
	}}
	if _, err := MarshalFinalizedRange(r); err == nil {
		t.Fatal("broken parent lineage accepted")
	}
}

package filters

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

func g41Addr(b byte) types.Address {
	var a types.Address
	a[len(a)-1] = b
	return a
}

func g41Topic(b byte) types.Hash {
	var h types.Hash
	h[len(h)-1] = b
	return h
}

func g41Log(addr types.Address, blockNum uint64, topics ...types.Hash) *block.Log {
	return &block.Log{
		Address:     addr,
		Topics:      topics,
		BlockNumber: uint256.NewInt(blockNum),
		TxHash:      g41Topic(0xee), // non-zero: avoids the receipt-fallback path in checkMatches
	}
}

func TestIncludes(t *testing.T) {
	addrs := []types.Address{g41Addr(1), g41Addr(2)}
	if !includes(addrs, g41Addr(1)) {
		t.Error("expected includes to find address 1")
	}
	if includes(addrs, g41Addr(3)) {
		t.Error("did not expect includes to find address 3")
	}
	if includes(nil, g41Addr(1)) {
		t.Error("includes on nil slice should be false")
	}
}

func TestFilterLogsByBlockRange(t *testing.T) {
	logs := []*block.Log{
		g41Log(g41Addr(1), 10),
		g41Log(g41Addr(1), 20),
		g41Log(g41Addr(1), 30),
	}

	ret := filterLogs(logs, big.NewInt(15), big.NewInt(25), nil, nil)
	if len(ret) != 1 || ret[0].BlockNumber.Uint64() != 20 {
		t.Fatalf("expected only block 20, got %+v", ret)
	}

	// Negative fromBlock/toBlock sentinels (e.g. pending) should be ignored.
	ret = filterLogs(logs, big.NewInt(-1), big.NewInt(-1), nil, nil)
	if len(ret) != 3 {
		t.Fatalf("expected all logs when range sentinels are negative, got %d", len(ret))
	}
}

func TestFilterLogsByAddress(t *testing.T) {
	logs := []*block.Log{
		g41Log(g41Addr(1), 1),
		g41Log(g41Addr(2), 2),
	}
	ret := filterLogs(logs, nil, nil, []types.Address{g41Addr(2)}, nil)
	if len(ret) != 1 || ret[0].Address != g41Addr(2) {
		t.Fatalf("expected only address 2 log, got %+v", ret)
	}
}

func TestFilterLogsByTopics(t *testing.T) {
	logs := []*block.Log{
		g41Log(g41Addr(1), 1, g41Topic(1), g41Topic(2)),
		g41Log(g41Addr(1), 2, g41Topic(3), g41Topic(4)),
		g41Log(g41Addr(1), 3, g41Topic(1)), // only one topic
	}

	// Wildcard first position, specific second position.
	ret := filterLogs(logs, nil, nil, nil, [][]types.Hash{{}, {g41Topic(2)}})
	if len(ret) != 1 || ret[0].BlockNumber.Uint64() != 1 {
		t.Fatalf("expected block 1, got %+v", ret)
	}

	// OR match across multiple topic alternatives.
	ret = filterLogs(logs, nil, nil, nil, [][]types.Hash{{g41Topic(1), g41Topic(3)}})
	if len(ret) != 3 {
		t.Fatalf("expected 3 matches for OR topic filter, got %d", len(ret))
	}

	// Topic filter longer than the log's topic list should be skipped.
	ret = filterLogs(logs, nil, nil, nil, [][]types.Hash{{g41Topic(1)}, {g41Topic(2)}})
	for _, l := range ret {
		if l.BlockNumber.Uint64() == 3 {
			t.Fatal("log with too few topics should not match")
		}
	}
}

func TestBloomFilterEmptyCriteria(t *testing.T) {
	var bloom block.Bloom
	if !bloomFilter(bloom, nil, nil) {
		t.Fatal("empty filter criteria should always match")
	}
}

func TestBloomFilterByAddress(t *testing.T) {
	var bloom block.Bloom
	addr := g41Addr(7)
	bloom.Add(addr.Bytes())

	if !bloomFilter(bloom, []types.Address{addr}, nil) {
		t.Fatal("expected bloom match for included address")
	}
	if bloomFilter(bloom, []types.Address{g41Addr(99)}, nil) {
		t.Fatal("expected no bloom match for excluded address")
	}
}

func TestBloomFilterByTopics(t *testing.T) {
	var bloom block.Bloom
	topic := g41Topic(5)
	bloom.Add(topic.Bytes())

	if !bloomFilter(bloom, nil, [][]types.Hash{{topic}}) {
		t.Fatal("expected bloom match for included topic")
	}
	if bloomFilter(bloom, nil, [][]types.Hash{{g41Topic(42)}}) {
		t.Fatal("expected no bloom match for excluded topic")
	}
	// Wildcard topic position always matches.
	if !bloomFilter(bloom, nil, [][]types.Hash{{}}) {
		t.Fatal("wildcard topic position should match")
	}
}

func TestBloomFilterAddressAndTopicCombined(t *testing.T) {
	var bloom block.Bloom
	addr := g41Addr(1)
	topic := g41Topic(2)
	bloom.Add(addr.Bytes())
	bloom.Add(topic.Bytes())

	if !bloomFilter(bloom, []types.Address{addr}, [][]types.Hash{{topic}}) {
		t.Fatal("expected match when both address and topic are present")
	}
	// Address present but topic missing -> no match.
	if bloomFilter(bloom, []types.Address{addr}, [][]types.Hash{{g41Topic(99)}}) {
		t.Fatal("expected no match when topic is absent from bloom")
	}
}

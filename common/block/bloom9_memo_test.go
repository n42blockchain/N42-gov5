// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package block

import (
	"math/rand"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// referenceBloom is the pre-memo implementation: one Keccak per log address and
// per topic, no caching. Every test below compares against it, so the memo can
// only pass by producing byte-identical blooms.
func referenceBloom(receipts Receipts) Bloom {
	buf := make([]byte, 6)
	var bin Bloom
	sha := crypto.NewKeccakState()
	defer crypto.ReturnKeccakState(sha)
	for _, receipt := range receipts {
		for _, log := range receipt.Logs {
			bin.addWithHasher(log.Address[:], buf, sha)
			for _, b := range log.Topics {
				bin.addWithHasher(b[:], buf, sha)
			}
		}
	}
	return bin
}

// randomReceipts builds logs whose addresses and topics repeat the way mainnet
// blocks do — a handful of hot contracts and event signatures plus a long tail —
// so the memo is actually exercised rather than always missing.
func randomReceipts(rng *rand.Rand, receiptCount, hotAddrs, hotTopics int) Receipts {
	addrPool := make([]types.Address, hotAddrs)
	for i := range addrPool {
		rng.Read(addrPool[i][:])
	}
	topicPool := make([]types.Hash, hotTopics)
	for i := range topicPool {
		rng.Read(topicPool[i][:])
	}

	receipts := make(Receipts, receiptCount)
	for i := range receipts {
		logCount := rng.Intn(6)
		logs := make([]*Log, logCount)
		for j := range logs {
			var addr types.Address
			if rng.Intn(4) == 0 {
				rng.Read(addr[:]) // cold tail
			} else {
				addr = addrPool[rng.Intn(len(addrPool))]
			}
			topicCount := rng.Intn(5)
			topics := make([]types.Hash, topicCount)
			for k := range topics {
				if rng.Intn(3) == 0 {
					rng.Read(topics[k][:])
				} else {
					topics[k] = topicPool[rng.Intn(len(topicPool))]
				}
			}
			data := make([]byte, rng.Intn(64))
			rng.Read(data)
			logs[j] = &Log{Address: addr, Topics: topics, Data: data}
		}
		receipts[i] = &Receipt{Logs: logs}
	}
	return receipts
}

func TestCreateBloomMatchesUnmemoized(t *testing.T) {
	rng := rand.New(rand.NewSource(20260825))
	for round := 0; round < 200; round++ {
		receipts := randomReceipts(rng, 1+rng.Intn(40), 8, 12)
		if got, want := CreateBloom(receipts), referenceBloom(receipts); got != want {
			t.Fatalf("round %d: memoized bloom differs from reference", round)
		}
	}
}

// TestCreateBloomAddressTopicCollision is the specific trap a single memo keyed
// by raw bytes would fall into: a 20-byte address and a 32-byte topic whose
// first 20 bytes are identical hash to different values and must not share a
// memo entry.
func TestCreateBloomAddressTopicCollision(t *testing.T) {
	var addr types.Address
	for i := range addr {
		addr[i] = byte(i + 1)
	}
	var topic types.Hash
	copy(topic[:], addr[:]) // remaining 12 bytes stay zero

	receipts := Receipts{{Logs: []*Log{{Address: addr, Topics: []types.Hash{topic}}}}}
	if got, want := CreateBloom(receipts), referenceBloom(receipts); got != want {
		t.Fatalf("address/topic prefix collision produced the wrong bloom")
	}
}

// TestCreateBloomBeyondMemoLimit drives more distinct inputs than the memo
// holds, so the wholesale clear path runs and must not corrupt results.
func TestCreateBloomBeyondMemoLimit(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	receipts := make(Receipts, 0, bloomMemoLimit+64)
	for i := 0; i < bloomMemoLimit+64; i++ {
		var addr types.Address
		var topic types.Hash
		rng.Read(addr[:])
		rng.Read(topic[:])
		receipts = append(receipts, &Receipt{Logs: []*Log{{Address: addr, Topics: []types.Hash{topic}}}})
	}
	if got, want := CreateBloom(receipts), referenceBloom(receipts); got != want {
		t.Fatalf("bloom differs after the memo overflowed its limit")
	}
}

func TestLogsBloomMatchesCreateBloom(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	receipts := randomReceipts(rng, 25, 5, 7)
	var logs []*Log
	for _, r := range receipts {
		logs = append(logs, r.Logs...)
	}
	want := referenceBloom(receipts)
	if got := BytesToBloom(LogsBloom(logs)); got != want {
		t.Fatalf("LogsBloom differs from the reference bloom over the same logs")
	}
}

// TestCreateBloomConcurrent runs the shared pool from many goroutines: each
// must get a memo it owns for the duration of its call. Run under -race.
func TestCreateBloomConcurrent(t *testing.T) {
	rng := rand.New(rand.NewSource(4242))
	cases := make([]Receipts, 16)
	wants := make([]Bloom, len(cases))
	for i := range cases {
		cases[i] = randomReceipts(rng, 10+i, 6, 9)
		wants[i] = referenceBloom(cases[i])
	}

	errs := make(chan error, 64)
	done := make(chan struct{})
	for g := 0; g < 64; g++ {
		go func(g int) {
			defer func() { done <- struct{}{} }()
			for iter := 0; iter < 200; iter++ {
				idx := (g + iter) % len(cases)
				if CreateBloom(cases[idx]) != wants[idx] {
					errs <- errBloomMismatch
					return
				}
			}
		}(g)
	}
	for i := 0; i < 64; i++ {
		<-done
	}
	select {
	case <-errs:
		t.Fatal("concurrent CreateBloom produced a wrong bloom")
	default:
	}
}

var errBloomMismatch = errStr("bloom mismatch")

type errStr string

func (e errStr) Error() string { return string(e) }

func BenchmarkCreateBloomRepeated(b *testing.B) {
	rng := rand.New(rand.NewSource(1))
	receipts := randomReceipts(rng, 200, 8, 12)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = CreateBloom(receipts)
	}
}

func BenchmarkCreateBloomReference(b *testing.B) {
	rng := rand.New(rand.NewSource(1))
	receipts := randomReceipts(rng, 200, 8, 12)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = referenceBloom(receipts)
	}
}

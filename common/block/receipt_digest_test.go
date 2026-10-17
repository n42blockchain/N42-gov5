// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package block

import (
	"bytes"
	"math"
	"math/rand"
	"testing"

	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// refReceipts is the pre-digest Receipts.EncodeIndex: reflection RLP of
// storedReceipt. It pins the hand-written encoder byte for byte.
type refReceipts Receipts

func (rs refReceipts) Len() int { return len(rs) }

func (rs refReceipts) EncodeIndex(i int, w *bytes.Buffer) {
	r := rs[i]
	logs := make([]*storedLog, len(r.Logs))
	for k, log := range r.Logs {
		logs[k] = &storedLog{Address: log.Address, Topics: log.Topics, Data: log.Data}
	}
	rlp.Encode(w, &storedReceipt{r.Status, r.CumulativeGasUsed, logs}) //nolint:errcheck
}

// refMarshalCompact is the pre-digest Receipts.MarshalCompact.
func refMarshalCompact(rs Receipts) []byte {
	out := make([]byte, 0, 64*len(rs))
	out = append(out, compactReceiptsMarker, compactReceiptsVersion)
	out = rcAppendUvarint(out, uint64(len(rs)))
	for _, r := range rs {
		out = append(out, r.Type)
		var flags byte
		if len(r.PostState) == 32 {
			flags |= rcfPostState
		}
		hasContract := r.ContractAddress != (types.Address{})
		if hasContract {
			flags |= rcfContract
		}
		out = append(out, flags)
		if flags&rcfPostState != 0 {
			out = append(out, r.PostState...)
		} else {
			out = rcAppendUvarint(out, r.Status)
		}
		if hasContract {
			out = append(out, r.ContractAddress[:]...)
		}
		out = rcAppendUvarint(out, r.CumulativeGasUsed)
		out = rcAppendUvarint(out, uint64(len(r.Logs)))
		for _, lg := range r.Logs {
			out = append(out, lg.Address[:]...)
			out = rcAppendUvarint(out, uint64(len(lg.Topics)))
			for _, tp := range lg.Topics {
				out = append(out, tp[:]...)
			}
			out = rcAppendUvarint(out, uint64(len(lg.Data)))
			out = append(out, lg.Data...)
		}
	}
	return out
}

// mixedReceipts builds n receipts: mostly plain transfers, with failures,
// contract creations, post-state receipts and logs whose topic counts and
// data lengths straddle every RLP header boundary.
func mixedReceipts(n int, seed int64) Receipts {
	return mixedReceiptsWith(n, seed, []int{0, 1, 2, 55, 56, 57, 255, 256, 1024, 70000})
}

func mixedReceiptsWith(n int, seed int64, dataLens []int) Receipts {
	rng := rand.New(rand.NewSource(seed))
	rs := make(Receipts, n)
	var cum uint64
	for i := range rs {
		r := &Receipt{Type: uint8(rng.Intn(3)), Status: ReceiptStatusSuccessful}
		g := uint64(21000)
		switch k := rng.Intn(10); {
		case k == 0:
			r.Status = ReceiptStatusFailed
			g = uint64(rng.Intn(1 << 20))
		case k == 1:
			rng.Read(r.ContractAddress[:])
			g = 53000 + uint64(rng.Intn(1<<16))
		case k == 2:
			r.PostState = make([]byte, 32)
			rng.Read(r.PostState)
		case k <= 5:
			nl := 1 + rng.Intn(4)
			for j := 0; j < nl; j++ {
				l := &Log{Topics: make([]types.Hash, rng.Intn(5))}
				rng.Read(l.Address[:])
				for t := range l.Topics {
					rng.Read(l.Topics[t][:])
				}
				if dl := dataLens[rng.Intn(len(dataLens))]; dl > 0 {
					l.Data = make([]byte, dl)
					rng.Read(l.Data)
					if dl == 1 && rng.Intn(2) == 0 {
						l.Data[0] &= 0x7f // single byte below 0x80 encodes as itself
					}
				}
				r.Logs = append(r.Logs, l)
			}
		}
		cum += g
		r.CumulativeGasUsed = cum
		rs[i] = r
	}
	return rs
}

func TestReceiptsDigestMatchesSeparatePasses(t *testing.T) {
	cases := map[string]Receipts{
		"empty":     nil,
		"one":       {{Status: 1, CumulativeGasUsed: 21000}},
		"zero":      {{}},
		"maxGas":    {{Status: 1, CumulativeGasUsed: math.MaxUint64}},
		"fixture":   receiptsForCompactTest(),
		"mixed-1k":  mixedReceipts(1000, 1),
		"mixed-20k": mixedReceipts(20000, 2),
		"transfers": transferReceipts(5000),
	}
	for name, rs := range cases {
		t.Run(name, func(t *testing.T) {
			wantRoot := hash.DeriveSha(refReceipts(rs))
			wantBloom := CreateBloom(rs)
			wantEnc := refMarshalCompact(rs)

			root, bloom, enc := ReceiptsDigest(rs, true, true)
			if root != wantRoot {
				t.Fatalf("root %x, want %x", root, wantRoot)
			}
			if bloom != wantBloom {
				t.Fatal("bloom differs from CreateBloom")
			}
			if !bytes.Equal(enc, wantEnc) {
				t.Fatal("compact row differs from MarshalCompact")
			}
			if got := hash.DeriveSha(rs); got != wantRoot {
				t.Fatalf("Receipts.EncodeIndex root %x, want %x", got, wantRoot)
			}
			if got := rs.MarshalCompact(); !bytes.Equal(got, wantEnc) {
				t.Fatal("MarshalCompact differs from the reference")
			}
			for i := range rs {
				var a, b bytes.Buffer
				rs.EncodeIndex(i, &a)
				refReceipts(rs).EncodeIndex(i, &b)
				if !bytes.Equal(a.Bytes(), b.Bytes()) {
					t.Fatalf("receipt %d: native encoding %x, want %x", i, a.Bytes(), b.Bytes())
				}
			}

			r2, b2, e2 := ReceiptsDigest(rs, false, false)
			if r2 != (types.Hash{}) || b2 != wantBloom || e2 != nil {
				t.Fatal("skipped outputs must stay zero, bloom must still be set")
			}
		})
	}
}

// transferReceipts lays receipts out as the parallel processor does: plain
// successful transfers carved from 256-receipt chunks.
func transferReceipts(n int) Receipts {
	rs := make(Receipts, n)
	var chunk []Receipt
	for i := range rs {
		if len(chunk) == 0 {
			chunk = make([]Receipt, 256)
		}
		r := &chunk[0]
		chunk = chunk[1:]
		r.Status = ReceiptStatusSuccessful
		r.CumulativeGasUsed = uint64(i+1) * 21000
		r.GasUsed = 21000
		r.TxHash = types.Hash{byte(i), byte(i >> 8), byte(i >> 16)}
		rs[i] = r
	}
	return rs
}

const benchBlockTxs = 163_000

// BenchmarkReceiptsSeparatePasses is the pre-digest write path: compact
// row, native root via the reflection encoder, then the bloom.
func BenchmarkReceiptsSeparatePasses(b *testing.B) {
	rs := transferReceipts(benchBlockTxs)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = refMarshalCompact(rs)
		_ = refDeriveSha(refReceipts(rs))
		_ = CreateBloom(rs)
	}
}

func BenchmarkReceiptsDigest(b *testing.B) {
	rs := transferReceipts(benchBlockTxs)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = ReceiptsDigest(rs, true, true)
	}
}

// refDeriveSha is hash.DeriveShaWith as it ran before: encode, copy, write.
func refDeriveSha(list hash.DerivableList) (h types.Hash) {
	sha := crypto.NewKeccakState()
	var buf bytes.Buffer
	for i := 0; i < list.Len(); i++ {
		buf.Reset()
		list.EncodeIndex(i, &buf)
		sha.Write(types.CopyBytes(buf.Bytes())) //nolint:errcheck
	}
	sha.Read(h[:]) //nolint:errcheck
	return h
}

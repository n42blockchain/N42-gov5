// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package block

import "testing"

// Proto-vs-compact codec speed for the dominant storage shapes. The compact
// codecs do straight-line byte writes/reads (no reflection, no proto tag
// parsing), so they should beat proto on both directions in addition to size.

func BenchmarkHeaderMarshalProto(b *testing.B) {
	h := headersForCompactTest()["resealEmpty"]
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := h.Marshal(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHeaderMarshalCompact(b *testing.B) {
	h := headersForCompactTest()["resealEmpty"]
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = h.MarshalCompact()
	}
}

func BenchmarkHeaderUnmarshalProto(b *testing.B) {
	h := headersForCompactTest()["resealEmpty"]
	enc, _ := h.Marshal()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var out Header
		if err := out.Unmarshal(enc); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHeaderUnmarshalCompact(b *testing.B) {
	h := headersForCompactTest()["resealEmpty"]
	enc := h.MarshalCompact()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var out Header
		if err := out.Unmarshal(enc); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReceiptsMarshalProto(b *testing.B) {
	rs := receiptsForCompactTest()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := rs.Marshal(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReceiptsMarshalCompact(b *testing.B) {
	rs := receiptsForCompactTest()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = rs.MarshalCompact()
	}
}

func BenchmarkReceiptsUnmarshalProto(b *testing.B) {
	rs := receiptsForCompactTest()
	enc, _ := rs.Marshal()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var out Receipts
		if err := out.Unmarshal(enc); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReceiptsUnmarshalCompact(b *testing.B) {
	rs := receiptsForCompactTest()
	enc := rs.MarshalCompact()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var out Receipts
		if err := out.Unmarshal(enc); err != nil {
			b.Fatal(err)
		}
	}
}

// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import "testing"

func TestPostQuantumIntrinsicGasExtra(t *testing.T) {
	cases := []struct {
		name    string
		algo    uint8
		sigLen  int
		pubLen  int
		wantVfy uint64
	}{
		{"falcon", PQAlgoFalcon512, 690, 897, pqVerifyGasFalcon512},
		{"sqisign", PQAlgoSQIsign, 177, 64, pqVerifyGasSQIsign},
		{"dilithium2", PQAlgoDilithium2, 2420, 1312, pqVerifyGasDilithium2},
		{"dilithium3", PQAlgoDilithium3, 3293, 1952, pqVerifyGasDilithium3},
		{"unknown-algo-conservative", 99, 100, 50, pqVerifyGasSQIsign},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pq := &PostQuantumTx{
				SigAlgo:     c.algo,
				PQSignature: make([]byte, c.sigLen),
				PubKeyData:  make([]byte, c.pubLen),
			}
			want := c.wantVfy + uint64(c.sigLen+c.pubLen)*pqSigByteGas
			if got := pq.IntrinsicGasExtra(); got != want {
				t.Fatalf("PostQuantumTx.IntrinsicGasExtra = %d, want %d", got, want)
			}
			// Same via the Transaction wrapper.
			tx := NewTxOwned(pq)
			if got := tx.IntrinsicGasExtra(); got != want {
				t.Fatalf("Transaction.IntrinsicGasExtra = %d, want %d", got, want)
			}
		})
	}
}

func TestNonPQIntrinsicGasExtraIsZero(t *testing.T) {
	tx := NewTxOwned(&DynamicFeeTx{})
	if got := tx.IntrinsicGasExtra(); got != 0 {
		t.Fatalf("non-PQ IntrinsicGasExtra = %d, want 0", got)
	}
}

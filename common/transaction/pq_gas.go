// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Gas schedule for the post-quantum transaction type (0x05). A PQ tx carries a
// large signature (and possibly a full public key) and its verification is far
// costlier than a secp256k1 ecrecover (3000 gas). Without an explicit surcharge a
// PQ tx would be drastically underpriced relative to the CPU + bytes it imposes —
// a DoS vector. The surcharge is added to the message's intrinsic gas in both the
// txpool pre-check and consensus execution (see Message.intrinsicGasExtra).
//
// The per-algorithm verify constants are calibration placeholders (priced ABOVE
// ecrecover, in rough proportion to verify cost); benchmark before mainnet and
// fold the result here. The per-byte rate matches non-zero calldata (16 gas/byte)
// since the signature + pubkey are extra bytes the network carries and stores.

package transaction

// Per-algorithm signature-verification gas, CALIBRATED from measured verify cost
// (perf_bench_test.go, Ryzen 9 9950X) against the secp256k1 ecrecover reference
// (3000 gas @ 21608 ns → 0.139 gas/ns). PQ verify turned out CHEAPER than
// ecrecover (no key recovery — the pubkey is supplied), so these are well below
// 3000. Note: the dominant PQ tx cost is the BYTE charge below (sigs are
// 0.7–3.3 KB), not the verify CPU — so precision here barely moves total cost.
//
//	algo         verify ns/op   ×0.139 gas/ns   set (rounded up, margin)
//	Falcon-512        5104           709            1000
//	Dilithium2        8666          1203            1500
//	Dilithium3       10981          1524            2000
//	(ecrecover ref)  21608          3000            —)
const (
	pqVerifyGasFalcon512  uint64 = 1000  // measured 5104 ns
	pqVerifyGasSQIsign    uint64 = 24000 // verify UNIMPLEMENTED → unbenchmarked; keep conservative (isogeny is slow)
	pqVerifyGasDilithium2 uint64 = 1500  // measured 8666 ns
	pqVerifyGasDilithium3 uint64 = 2000  // measured 10981 ns

	// Per-byte gas for the PQ signature + public-key bytes carried in the tx,
	// charged at the non-zero calldata rate (the bytes are real network/storage
	// load and DOMINATE the surcharge: Falcon ~1.5 KB→~25k gas, Dilithium2
	// ~3.7 KB→~60k gas).
	pqSigByteGas uint64 = 16
)

// pqVerifyGas returns the verification-CPU gas for a PQ signature algorithm.
// An unknown algo returns the most conservative (highest) cost.
func pqVerifyGas(algo uint8) uint64 {
	switch algo {
	case PQAlgoFalcon512:
		return pqVerifyGasFalcon512
	case PQAlgoSQIsign:
		return pqVerifyGasSQIsign
	case PQAlgoDilithium2:
		return pqVerifyGasDilithium2
	case PQAlgoDilithium3:
		return pqVerifyGasDilithium3
	default:
		return pqVerifyGasSQIsign
	}
}

// IntrinsicGasExtra is the PQ-specific gas added on top of the standard intrinsic
// gas: per-algo verify cost + per-byte cost of the signature and embedded pubkey.
func (tx *PostQuantumTx) IntrinsicGasExtra() uint64 {
	bytes := uint64(len(tx.PQSignature) + len(tx.PubKeyData))
	return pqVerifyGas(tx.SigAlgo) + bytes*pqSigByteGas
}

// IntrinsicGasExtra returns the type-specific intrinsic-gas surcharge for a
// transaction (PQ verify+bytes for 0x05; 0 for every standard type). Used by
// AsMessage and the txpool pre-check so both price PQ txs identically.
func (tx *Transaction) IntrinsicGasExtra() uint64 {
	if pq, ok := tx.inner.(*PostQuantumTx); ok {
		return pq.IntrinsicGasExtra()
	}
	return 0
}

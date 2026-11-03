package transaction

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
)

// testBlobTxBody returns the canonical blob tx fields used by the sidecar
// wire-format tests.
func testBlobTxBody() blobTxRLP {
	return blobTxRLP{
		ChainID:    uint256.NewInt(1),
		Nonce:      7,
		GasTipCap:  uint256.NewInt(2),
		GasFeeCap:  uint256.NewInt(3),
		Gas:        21_000,
		To:         types.HexToAddress("0x1111111111111111111111111111111111111111"),
		Value:      new(uint256.Int),
		BlobFeeCap: uint256.NewInt(4),
		BlobHashes: []types.Hash{
			types.HexToHash("0x0100000000000000000000000000000000000000000000000000000000000001"),
			types.HexToHash("0x0100000000000000000000000000000000000000000000000000000000000002"),
		},
		V: new(uint256.Int),
		R: uint256.NewInt(5),
		S: uint256.NewInt(6),
	}
}

// encodeNetworkBlobTxV1 builds the EIP-7594 network form by hand, element by
// element, so the test does not depend on the codec under test:
//
//	0x03 || rlp([tx_payload_body, wrapper_version, blobs, commitments, cell_proofs])
func encodeNetworkBlobTxV1(t *testing.T, body blobTxRLP, version byte, blobs []Blob, commitments []Commitment, proofs []Proof) []byte {
	t.Helper()
	var content []byte
	for _, v := range []interface{}{&body, version, blobs, commitments, proofs} {
		enc, err := rlp.EncodeToBytes(v)
		if err != nil {
			t.Fatalf("encode element: %v", err)
		}
		content = append(content, enc...)
	}
	var out bytes.Buffer
	out.WriteByte(BlobTxType)
	if n := len(content); n < 56 {
		out.WriteByte(0xc0 + byte(n))
	} else {
		var lenBytes []byte
		for x := n; x > 0; x >>= 8 {
			lenBytes = append([]byte{byte(x)}, lenBytes...)
		}
		out.WriteByte(0xf7 + byte(len(lenBytes)))
		out.Write(lenBytes)
	}
	out.Write(content)
	return out.Bytes()
}

func testCellProofs(nBlobs int) []Proof {
	proofs := make([]Proof, nBlobs*CellProofsPerBlob)
	for i := range proofs {
		proofs[i][0] = byte(i)
		proofs[i][1] = byte(i >> 8)
		proofs[i][47] = 0xaa
	}
	return proofs
}

func decodeMust(t *testing.T, raw []byte) *Transaction {
	t.Helper()
	tx, err := DecodeEthereumTransaction(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return tx
}

// TestDecodeEthereumBlobTransactionNetworkWrapperV1 checks the EIP-7594
// version-1 pooled transaction form: the version byte follows the tx body,
// the proofs are CELLS_PER_EXT_BLOB cell proofs per blob, and re-encoding
// reproduces the received bytes exactly.
func TestDecodeEthereumBlobTransactionNetworkWrapperV1(t *testing.T) {
	body := testBlobTxBody()
	blobs := []Blob{{1}, {9}}
	commitments := []Commitment{{2}, {8}}
	proofs := testCellProofs(len(blobs))
	raw := encodeNetworkBlobTxV1(t, body, BlobSidecarVersion1, blobs, commitments, proofs)

	tx, err := DecodeEthereumTransaction(raw)
	if err != nil {
		t.Fatalf("DecodeEthereumTransaction(v1) error = %v", err)
	}
	if tx.Type() != BlobTxType || tx.Nonce() != 7 || len(tx.BlobHashes()) != 2 {
		t.Fatalf("decoded tx = type %d nonce %d hashes %d", tx.Type(), tx.Nonce(), len(tx.BlobHashes()))
	}
	sc := tx.BlobTxSidecar()
	if sc == nil {
		t.Fatal("v1 sidecar missing")
	}
	if sc.Version != BlobSidecarVersion1 {
		t.Fatalf("sidecar version = %d, want 1", sc.Version)
	}
	if len(sc.Blobs) != 2 || sc.Blobs[1][0] != 9 || len(sc.Commitments) != 2 || sc.Commitments[1][0] != 8 {
		t.Fatalf("sidecar blobs/commitments = %d/%d", len(sc.Blobs), len(sc.Commitments))
	}
	if len(sc.Proofs) != 2*CellProofsPerBlob {
		t.Fatalf("cell proofs = %d, want %d", len(sc.Proofs), 2*CellProofsPerBlob)
	}
	cell, err := sc.CellProofsAt(1)
	if err != nil {
		t.Fatalf("CellProofsAt(1): %v", err)
	}
	if len(cell) != CellProofsPerBlob || cell[0] != proofs[CellProofsPerBlob] {
		t.Fatalf("CellProofsAt(1) returned the wrong slice")
	}

	// The pooled encoding must be byte-identical to what was received, so a
	// relayed transaction is the same object at every peer.
	pooled, err := EncodeEthereumPooledTransaction(tx)
	if err != nil {
		t.Fatalf("EncodeEthereumPooledTransaction(v1) error = %v", err)
	}
	if !bytes.Equal(pooled, raw) {
		t.Fatalf("v1 pooled re-encoding differs from the received bytes")
	}

	// The canonical (block) encoding drops the sidecar and its version.
	canonical, err := EncodeEthereumTransaction(tx)
	if err != nil {
		t.Fatalf("EncodeEthereumTransaction: %v", err)
	}
	want, err := rlp.EncodeToBytes(&body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, append([]byte{BlobTxType}, want...)) {
		t.Fatalf("canonical encoding carries sidecar data")
	}
	cached, err := tx.EthEncoded()
	if err != nil {
		t.Fatalf("EthEncoded: %v", err)
	}
	if !bytes.Equal(cached, canonical) {
		t.Fatalf("EthEncoded of a network-form tx returned the wrapper, not the canonical encoding")
	}
	if tx.Hash() != decodeMust(t, canonical).Hash() {
		t.Fatalf("network-form tx hash differs from the canonical tx hash")
	}
}

// TestDecodeEthereumBlobTransactionNetworkWrapperV0KeepsVersion0 keeps the
// legacy (version-less) form decodable and re-encodable as version 0.
func TestDecodeEthereumBlobTransactionNetworkWrapperV0KeepsVersion0(t *testing.T) {
	wrapper := blobTxNetworkWrapperRLP{
		Tx:          testBlobTxBody(),
		Blobs:       []Blob{{1}, {2}},
		Commitments: []Commitment{{3}, {4}},
		Proofs:      []Proof{{5}, {6}},
	}
	payload, err := rlp.EncodeToBytes(&wrapper)
	if err != nil {
		t.Fatal(err)
	}
	raw := append([]byte{BlobTxType}, payload...)
	tx := decodeMust(t, raw)
	sc := tx.BlobTxSidecar()
	if sc == nil || sc.Version != BlobSidecarVersion0 || len(sc.Proofs) != 2 {
		t.Fatalf("v0 sidecar = %#v", sc)
	}
	if _, err := sc.CellProofsAt(0); err == nil {
		t.Fatal("CellProofsAt on a version-0 sidecar must fail")
	}
	pooled, err := EncodeEthereumPooledTransaction(tx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pooled, raw) {
		t.Fatal("v0 pooled re-encoding differs from the received bytes")
	}
	// The cached canonical encoding (used for the transactions root) must be
	// the bare transaction, never the network wrapper it was decoded from.
	cached, err := tx.EthEncoded()
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := EncodeEthereumTransaction(tx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cached, canonical) {
		t.Fatal("EthEncoded of a v0 network-form tx returned the wrapper, not the canonical encoding")
	}
}

// TestDecodeEthereumBlobTransactionRejectsUnknownSidecarVersion mirrors
// geth: a version byte other than 1 in the versioned layout is an error.
func TestDecodeEthereumBlobTransactionRejectsUnknownSidecarVersion(t *testing.T) {
	for _, version := range []byte{0, 2} {
		raw := encodeNetworkBlobTxV1(t, testBlobTxBody(), version, []Blob{{1}}, []Commitment{{2}}, testCellProofs(1))
		if _, err := DecodeEthereumTransaction(raw); err == nil {
			t.Fatalf("version %d in the versioned layout decoded without error", version)
		}
	}
}

// TestBlobTxSidecarCopyKeepsVersion guards the deep copy used by the pool.
func TestBlobTxSidecarCopyKeepsVersion(t *testing.T) {
	sc := &BlobTxSidecar{Version: BlobSidecarVersion1, Blobs: []Blob{{1}}, Commitments: []Commitment{{2}}, Proofs: testCellProofs(1)}
	cpy := sc.Copy()
	if cpy.Version != BlobSidecarVersion1 || len(cpy.Proofs) != CellProofsPerBlob {
		t.Fatalf("copy = version %d proofs %d", cpy.Version, len(cpy.Proofs))
	}
}

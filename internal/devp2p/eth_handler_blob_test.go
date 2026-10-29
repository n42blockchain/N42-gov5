package devp2p

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/kzg"
	"github.com/n42blockchain/N42/params"
)

// pooledBlobRaw signs a one-blob transaction carrying sidecar and returns it
// as a PooledTransactions list element.
func pooledBlobRaw(t *testing.T, chainID *big.Int, nonce uint64, commitment transaction.Commitment, sidecar *transaction.BlobTxSidecar) (rlp.RawValue, types.Hash) {
	t.Helper()
	key, err := crypto.HexToECDSA("4c0883a69102937d6231471b5dbb6204fe512961708279c7b07b8d2f2b1dd1c7")
	if err != nil {
		t.Fatal(err)
	}
	inner := &transaction.BlobTx{
		ChainID:    uint256.MustFromBig(chainID),
		Nonce:      nonce,
		GasTipCap:  uint256.NewInt(1),
		GasFeeCap:  uint256.NewInt(2),
		Gas:        21000,
		Value:      new(uint256.Int),
		BlobFeeCap: uint256.NewInt(1),
		BlobHashes: []types.Hash{kzg.CommitmentToVersionedHash(commitment)},
		Sidecar:    sidecar,
	}
	signed, err := transaction.SignTx(transaction.NewTx(inner), transaction.NewLondonSigner(chainID), key)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := transaction.EncodeEthereumPooledTransaction(signed)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := pooledTransactionRawValue(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return raw, signed.Hash()
}

// TestAcceptPooledTransactionsVerifiesBlobSidecars checks that blob
// transactions gossiped by eth/68+ peers pass EIP-7594 admission before they
// reach the pool: at Osaka a version-1 sidecar with valid cell proofs is
// added, a tampered cell proof or a version-0 sidecar is dropped.
func TestAcceptPooledTransactionsVerifiesBlobSidecars(t *testing.T) {
	var blob transaction.Blob
	for i := 0; i < kzg.FieldElementsPerBlob; i++ {
		blob[i*32+31] = byte(i)
	}
	commitment, err := kzg.BlobToCommitment(&blob)
	if err != nil {
		t.Fatal(err)
	}
	cellProofs, err := kzg.ComputeCellProofs(&blob)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := kzg.GetContext()
	if err != nil {
		t.Fatal(err)
	}
	blobProof, err := ctx.ComputeBlobProof(&blob, commitment)
	if err != nil {
		t.Fatal(err)
	}
	v1 := func() *transaction.BlobTxSidecar {
		return &transaction.BlobTxSidecar{
			Version:     transaction.BlobSidecarVersion1,
			Blobs:       []transaction.Blob{blob},
			Commitments: []transaction.Commitment{commitment},
			Proofs:      append([]transaction.Proof(nil), cellProofs...),
		}
	}
	tampered := v1()
	tampered.Proofs[100][9] ^= 0x01
	v0 := &transaction.BlobTxSidecar{
		Blobs:       []transaction.Blob{blob},
		Commitments: []transaction.Commitment{commitment},
		Proofs:      []transaction.Proof{blobProof},
	}

	chainID := big.NewInt(1)
	cfg := &params.ChainConfig{ChainID: chainID, OsakaTime: big.NewInt(0)}
	good, goodHash := pooledBlobRaw(t, chainID, 0, commitment, v1())
	bad, _ := pooledBlobRaw(t, chainID, 1, commitment, tampered)
	old, _ := pooledBlobRaw(t, chainID, 2, commitment, v0)

	pool := &pooledTxPoolStub{byHash: map[types.Hash]*transaction.Transaction{}}
	h := &EthHandler{txpool: pool, provider: acceptPooledHeadStub{}, chainConfig: cfg}
	if err := h.acceptPooledTransactions([]rlp.RawValue{good, bad, old}); err != nil {
		t.Fatalf("acceptPooledTransactions() error = %v", err)
	}
	if len(pool.added) != 1 {
		t.Fatalf("added = %d transactions, want only the valid version-1 one", len(pool.added))
	}
	if pool.added[0].Hash() != goodHash {
		t.Fatalf("added %s, want %s", pool.added[0].Hash(), goodHash)
	}
}
